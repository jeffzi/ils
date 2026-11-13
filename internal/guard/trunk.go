//go:build unix

package guard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
)

// trunkAnswer is origin's default branch as the trunk rule learned it for one git dir; found is
// false when origin did not answer and the clone holds no cached default branch.
type trunkAnswer struct {
	// protection is nil until an invocation on the trunk needs it.
	protection *protectionAnswer
	trunk      string
	found      bool
}

// trunks is the trunk rule for one command, holding the answer for each git dir it asked. It asks
// origin for the trunk, and GitHub whether the trunk is protected, at most once per git dir, so
// every invocation of the command in one repository is judged against the same answers.
type trunks map[string]trunkAnswer

// refuseOnTrunk refuses a subcommand that adds commits to the branch HEAD is on, or the branch a
// switch earlier in its `&&` list left HEAD on, when that branch is the trunk of a repository that
// has an origin and GitHub protects it, and one whose directory, target, or HEAD the guard cannot
// tell. A `git rebase` whose branch argument is the trunk is judged as a rebase on the trunk, and
// one whose branch argument the guard cannot read is refused, since that branch may be the trunk.
//
// It reads origin and HEAD locally before it asks origin for the trunk, so a repository with no
// origin or a detached HEAD starts no call to origin, and it asks GitHub nothing off the trunk.
func (t trunks) refuseOnTrunk(ctx context.Context, caller Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok || !trunkJudged(sub, inv.Git.Args) {
		return Verdict{Outcome: Allow}, nil
	}

	placeUnknown := inv.Dir == "" || inv.Target.Dir == "" || inv.Target.Unknown || inv.Git.GitDirUnknown || inv.HeadUnknown
	if placeUnknown {
		reason := fmt.Sprintf("git %s: the guard cannot tell where the command runs, or which branch HEAD "+
			"is on, so it cannot tell whether it lands on the trunk; %s", sub, moveFirst)

		return refuse(reason), nil
	}

	if inv.Git.GitDir == "" {
		return Verdict{Outcome: Allow}, nil
	}

	// Every read here is of the repository, never of its work tree, so the git dir alone places it.
	repo := git.Repo{Runner: targetRunner(caller.Git, inv.Target.Dir, GitTarget{GitDir: inv.Git.GitDir})}
	standing, err := repo.Standing(ctx)
	if err != nil {
		return Verdict{}, fmt.Errorf("read the origin and HEAD git %s runs with: %w", sub, err)
	}

	branch, headOnBranch := headBranch(standing, inv)
	rebased, named := rebasedBranch(sub, inv.Git.Args)
	if named {
		branch, headOnBranch = rebased.Text, true
	}

	if standing.Kind == git.NoOrigin || !headOnBranch {
		return Verdict{Outcome: Allow}, nil
	}

	if named && !rebased.Literal {
		reason := fmt.Sprintf("git %s: the guard cannot read the branch it switches to, so it cannot tell "+
			"which branch the rebase lands on; write the branch as a literal", sub)

		return refuse(reason), nil
	}

	return t.judgeBranch(ctx, caller, trunkCall{inv: inv, repo: repo, sub: sub, branch: branch})
}

// trunkCall is the trunk-judged git subcommand sub of inv, which runs in repo and lands on branch.
type trunkCall struct {
	sub    string
	branch string
	repo   git.Repo
	inv    Invocation
}

// judgeBranch judges call: a refusal when origin's trunk cannot be told, and otherwise onTrunk's
// verdict on the trunk when GitHub protects it, as protectionAnswer.verdict reads it.
func (t trunks) judgeBranch(ctx context.Context, caller Caller, call trunkCall) (Verdict, error) {
	sub := call.sub

	answer, err := t.trunkOf(ctx, call.repo, call.inv.Git.GitDir)
	if err != nil {
		return Verdict{}, fmt.Errorf("read the trunk git %s runs against: %w", sub, err)
	}

	if !answer.found {
		reason := fmt.Sprintf("git %s: origin could not be reached and the clone holds no cached default "+
			"branch (refs/remotes/origin/HEAD), so the guard cannot tell the trunk; run `! git remote set-head origin --auto`, or run it outside the agent, "+
			"to cache it", sub)

		return refuse(reason), nil
	}

	if call.branch != answer.trunk {
		return Verdict{Outcome: Allow}, nil
	}

	verdict := onTrunk(sub, call.inv.Git.Args, answer.trunk)
	if verdict.Outcome == Allow {
		return verdict, nil
	}

	protection := t.protectionOf(ctx, caller, call)
	if protection.err != nil {
		return Verdict{}, fmt.Errorf("read the push URL of the origin git %s runs against: %w", sub, protection.err)
	}

	return protection.verdict(sub, answer.trunk, verdict), nil
}

// headBranch returns the branch HEAD is on where inv runs, and false when it is on none: the branch
// a switch earlier in its `&&` list left it on, or else the one standing reads.
func headBranch(standing git.Standing, inv Invocation) (string, bool) {
	if inv.head != "" {
		return inv.head, true
	}

	return standing.Branch, standing.Kind == git.OnBranch
}

// onTrunk returns the verdict on the trunk-judged git subcommand sub, given args, run on trunk,
// assuming GitHub protects the trunk. protectionAnswer.verdict decides whether that verdict applies.
// A `git pull` that may only fast-forward is allowed when it names origin and the trunk alone; one
// that names any other repository or refspec, or one the guard cannot read, can fast-forward the
// trunk to another branch.
func onTrunk(sub string, args []Word, trunk string) Verdict {
	options := readArgs(args, optionsOf(sub))
	if sub == "pull" && fastForwardsOnly(options.long) {
		// origin alone pulls what a plain `git pull --ff-only` pulls: the trunk's own upstream. A
		// refspec names the trunk with or without a leading `+` and its full ref.
		named := wordTexts(options.positional)
		if len(named) == 2 {
			named[1] = strings.TrimPrefix(strings.TrimPrefix(named[1], "+"), git.BranchRefPrefix)
		}

		pullsTrunk := slices.Equal(named, []string{git.OriginRemote}) || slices.Equal(named, []string{git.OriginRemote, trunk})
		if !options.opaque() && pullsTrunk {
			return Verdict{Outcome: Allow}
		}

		return refuse(fmt.Sprintf("git pull --ff-only on %s, the trunk, naming a repository or refspec other "+
			"than origin %s: it can fast-forward the trunk to another branch; run `git pull --ff-only` alone",
			trunk, trunk))
	}

	return refuse(fmt.Sprintf("git %s on %s, the protected trunk: %s", sub, trunk, branchAdvice))
}

// branchAdvice is where work goes instead of the trunk.
const branchAdvice = "work goes on a branch, started with `ils branch <name>`"

// trunkOf returns the trunk of the repository at gitDir, asking origin only the first time.
func (t trunks) trunkOf(ctx context.Context, repo git.Repo, gitDir string) (trunkAnswer, error) {
	if answer, asked := t[gitDir]; asked {
		return answer, nil
	}

	trunk, found, err := repo.TrunkOrLastFetched(ctx)
	if err != nil {
		return trunkAnswer{}, err
	}

	answer := trunkAnswer{trunk: trunk, found: found}
	t[gitDir] = answer

	return answer, nil
}

// trunkJudged reports whether the git subcommand sub, given args, may add commits to the branch
// it runs on: `git pull` may unless it may only fast-forward from the branch's upstream, naming no
// repository or refspec and holding nothing the guard cannot read.
func trunkJudged(sub string, args []Word) bool {
	switch sub {
	case "commit", "merge", "cherry-pick", "revert", "am", "rebase":
		return true
	case "pull":
		options := readArgs(args, optionsOf(sub))

		return !fastForwardsOnly(options.long) || len(options.positional) > 0 || options.opaque()
	default:
		return false
	}
}

// fastForwardsOnly reports whether the long options of `git pull` leave it fast-forwarding only:
// the last of `--ff-only`, `--ff` and `--no-ff` decides, as it does in git.
func fastForwardsOnly(options []longOption) bool {
	only := false
	for _, l := range options {
		switch l.name {
		case "--ff-only":
			only = true
		case "--ff", "--no-ff":
			only = false
		}
	}

	return only
}

// rebasedBranch returns the branch argument of `git rebase` with args, the branch it switches to
// before it rebases, and false when sub is not rebase or names no branch.
func rebasedBranch(sub string, args []Word) (Word, bool) {
	if sub != "rebase" {
		return Word{}, false
	}

	branch, named, _ := rebaseBranch(args)

	return branch, named
}

// protectionState is what the trunk rule learned of whether GitHub protects origin's trunk.
type protectionState int

const (
	trunkUnprotected protectionState = iota
	trunkProtected
	// ghMissing and ghLoggedOut are a gh that could not read the protection on github.com.
	ghMissing
	ghLoggedOut
	// ghConfigUnknown is an invocation whose gh config directory the guard cannot tell; the
	// detail names the variable.
	ghConfigUnknown
	// protectionUnread is any other failed read; the detail says what broke.
	protectionUnread
)

// protectionAnswer is whether GitHub protects origin's trunk, as the trunk rule read it for one
// git dir. err is the failure to read origin's push URL from git, which fails the verdict.
type protectionAnswer struct {
	err    error
	detail string
	state  protectionState
}

// protectionOf returns whether GitHub protects the trunk of the repository call runs in, reading it
// only the first time: a failed read is kept and never retried within the command.
func (t trunks) protectionOf(ctx context.Context, caller Caller, call trunkCall) protectionAnswer {
	gitDir := call.inv.Git.GitDir

	answer := t[gitDir]
	if answer.protection == nil {
		read := readProtection(ctx, caller, call, answer.trunk)
		answer.protection = &read
		t[gitDir] = answer
	}

	return *answer.protection
}

// readProtection reads whether GitHub protects trunk in the repository origin's push URL names,
// through the gh the agent would run in the environment of call. A push URL that names no
// repository, such as a local path, names nothing GitHub protects.
func readProtection(ctx context.Context, caller Caller, call trunkCall, trunk string) protectionAnswer {
	origin, named, err := pushRepository(ctx, call.repo)
	if err != nil {
		return protectionAnswer{err: err}
	}

	if !named {
		return protectionAnswer{state: trunkUnprotected}
	}

	runner, unknown := ghConfigRunner(caller.GH, call.inv.Dir, call.inv.ghConfig)
	if unknown != "" {
		return protectionAnswer{state: ghConfigUnknown, detail: unknown}
	}

	protected, err := isProtected(ctx, runner, forge.RepoOf(origin), trunk)
	if err != nil {
		return unreadProtection(ctx, runner, origin.Host, err)
	}

	if protected {
		return protectionAnswer{state: trunkProtected}
	}

	return protectionAnswer{state: trunkUnprotected}
}

// pushRepository returns the repository origin's first push URL names, the one git pushes to
// first, and false when the URL is of no shape git.ParseOrigin accepts. The fetch URL plays no
// part, so its failure to parse is not returned. A failure to read the URL from git, or to resolve
// the host of an ssh URL, is an error.
func pushRepository(ctx context.Context, repo git.Repo) (git.Origin, bool, error) {
	urls, err := repo.OriginURLs(ctx)
	if len(urls.Push) == 0 {
		return git.Origin{}, false, err
	}

	push := urls.Push[0]
	if push.Origin != (git.Origin{}) {
		return push.Origin, true, nil
	}

	if !repositoryShaped(push.URL) {
		return git.Origin{}, false, nil
	}

	return git.Origin{}, false, err
}

// repositoryShaped reports whether url is of a shape git.ParseOrigin accepts.
func repositoryShaped(url string) bool {
	_, err := git.ParseOrigin(url)

	return err == nil
}

// isProtected reports whether at least one active ruleset rule applies to trunk in repo, or else
// classic branch protection covers it. A plan that allows no rules leaves classic protection to
// decide.
func isProtected(ctx context.Context, runner github.Runner, repo forge.Repo, trunk string) (bool, error) {
	rules, err := github.TrunkProtection(ctx, runner, repo, trunk)
	if err != nil {
		return false, fmt.Errorf("read the rules on %s: %w", trunk, err)
	}

	if rules.Rules == forge.RulesProtect {
		return true, nil
	}

	protected, err := github.ClassicProtected(ctx, runner, repo, trunk)
	if err != nil {
		return false, fmt.Errorf("read the branch protection of %s: %w", trunk, err)
	}

	return protected, nil
}

// unreadProtection tells why the protection read on host failed with err. Only then does it run
// `gh auth status`, to tell a gh with no login for host from other failures. On a host other than
// github.com, a missing gh or one with no login there reads as no protection.
func unreadProtection(ctx context.Context, runner github.Runner, host string, err error) protectionAnswer {
	_, missing := errors.AsType[*child.ToolMissingError](err)

	loggedOut := false
	if !missing {
		_, loggedOut = errors.AsType[*forge.NotLoggedInError](runner.CheckLogin(ctx, host))
	}

	switch {
	// github.com is the one host whose trunk the rule refuses work on when gh is missing or holds no
	// login for it. Any other host may be no GitHub at all, such as a GitLab server, which gh never
	// holds a login for.
	case (missing || loggedOut) && host != git.GitHubHost:
		return protectionAnswer{state: trunkUnprotected}
	case missing:
		return protectionAnswer{state: ghMissing}
	case loggedOut:
		return protectionAnswer{state: ghLoggedOut}
	default:
		return protectionAnswer{state: protectionUnread, detail: err.Error()}
	}
}

// verdict returns the verdict on the git subcommand sub run on trunk, given p: protectedVerdict, the
// verdict on the trunk were it protected, when it is; an allow when it is not; and a refusal naming
// what kept the guard from reading it otherwise. A state the switch does not name refuses.
func (p protectionAnswer) verdict(sub, trunk string, protectedVerdict Verdict) Verdict {
	unread := fmt.Sprintf("git %s on %s, the trunk", sub, trunk)

	switch p.state {
	case trunkUnprotected:
		return Verdict{Outcome: Allow}
	case trunkProtected:
		return protectedVerdict
	case ghMissing:
		return refuse(fmt.Sprintf("%s: gh is missing, so the guard cannot read whether the trunk is protected; "+
			"install gh, or %s", unread, branchAdvice))
	case ghLoggedOut:
		return refuse(fmt.Sprintf("%s: gh is not logged in to %s, so the guard cannot read whether the trunk is "+
			"protected; run `! gh auth login --hostname %s`, or %s", unread, git.GitHubHost, git.GitHubHost, branchAdvice))
	case ghConfigUnknown:
		return refuse(fmt.Sprintf("%s: the guard cannot tell the %s gh runs with, so it cannot read whether "+
			"the trunk is protected; %s", unread, p.detail, branchAdvice))
	case protectionUnread:
		return refuse(fmt.Sprintf("%s: the guard could not read whether the trunk is protected: %s; %s",
			unread, p.detail, branchAdvice))
	}

	return refuse(fmt.Sprintf("%s: the guard could not read whether the trunk is protected; %s", unread, branchAdvice))
}
