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
	trunk string
	found bool
}

// trunks is the trunk rule for one command, holding the answer for each git dir it asked. It asks
// origin for the trunk at most once per git dir, so every invocation of the command in one
// repository is judged against the same trunk.
type trunks map[string]trunkAnswer

// refuseOnTrunk refuses a subcommand that adds commits to the branch HEAD is on, or the branch a
// switch earlier in its `&&` list left HEAD on, when that branch is the trunk of a repository whose
// origin is on GitHub, as refuseOnGitHub tells it, and one whose directory, target, or HEAD the guard
// cannot tell. A `git rebase` whose branch argument is the trunk is judged as a rebase on the trunk, and
// one whose branch argument the guard cannot read is refused, since that branch may be the trunk.
//
// It reads origin and HEAD locally before it asks origin for the trunk, so a repository with no
// origin or a detached HEAD starts no call to origin, and it runs no gh off the trunk.
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
// verdict on the trunk when origin is on GitHub, as refuseOnGitHub tells it.
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

	return refuseOnGitHub(ctx, caller, call, verdict)
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
// assuming origin is on GitHub. refuseOnGitHub decides whether that verdict applies.
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

	return refuse(fmt.Sprintf("git %s on %s, the default branch, which takes changes only through a pull request: %s",
		sub, trunk, branchAdvice))
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

// refuseOnGitHub returns refusal, the verdict on call run on its branch, the trunk, when origin's
// first push URL names a repository on GitHub, since GitHub is where a default branch takes changes
// only through a pull request. A repository on github.com gets refusal whatever gh holds there,
// and one on another host gets it when isGitHubHost takes that host for GitHub. A push URL that
// names no repository, such as a local path, gets an allow.
//
// The guard checks the login with the gh config the command sets, so it uses the login the agent's
// own gh would use, and refuses naming what kept it from telling. A failure to read the push URL,
// or the host of an ssh push URL, from git is an error.
func refuseOnGitHub(ctx context.Context, caller Caller, call trunkCall, refusal Verdict) (Verdict, error) {
	origin, named, err := pushRepository(ctx, call.repo)
	if err != nil {
		return Verdict{}, fmt.Errorf("read the push URL of the origin git %s runs against: %w", call.sub, err)
	}

	if !named {
		return Verdict{Outcome: Allow}, nil
	}

	if origin.Host == git.GitHubHost {
		return refusal, nil
	}

	where := fmt.Sprintf("git %s on %s, the trunk", call.sub, call.branch)

	runner, unknown := ghConfigRunner(caller.GH, call.inv.Dir, call.inv.ghConfig)
	if unknown != "" {
		return refuse(fmt.Sprintf("%s: the guard cannot tell the %s gh runs with, so it cannot tell whether %s "+
			"is GitHub; %s", where, unknown, origin.Host, branchAdvice)), nil
	}

	onGitHub, err := isGitHubHost(ctx, runner, origin.Host)
	if err != nil {
		return refuse(fmt.Sprintf("%s: the guard cannot tell whether %s is GitHub: %s; %s",
			where, origin.Host, err, branchAdvice)), nil
	}

	if !onGitHub {
		return Verdict{Outcome: Allow}, nil
	}

	return refusal, nil
}

// isGitHubHost reports whether host, a host other than github.com, is GitHub: whether the gh that
// runner runs holds a login there that passes gh's check. A missing gh, or one with no login for
// host, reads as false: host may be no GitHub at all, such as a GitLab server, which gh never holds
// a login for. Any other failure of the check is an error.
func isGitHubHost(ctx context.Context, runner github.Runner, host string) (bool, error) {
	err := runner.CheckLogin(ctx, host)
	if err == nil {
		return true, nil
	}

	_, missing := errors.AsType[*child.ToolMissingError](err)
	_, loggedOut := errors.AsType[*forge.NotLoggedInError](err)
	if missing || loggedOut {
		return false, nil
	}

	return false, fmt.Errorf("check gh's login to %s: %w", host, err)
}
