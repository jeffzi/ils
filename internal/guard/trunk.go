package guard

import (
	"context"
	"fmt"
	"slices"

	"github.com/jeffzi/ils/internal/git"
)

// refusePush refuses every `git push`, `git send-pack`, and `git http-push`, in any repository or
// none: `ils push` is the one way an agent pushes a branch.
func refusePush(_ context.Context, _ Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok || sub != "push" && sub != "send-pack" && sub != "http-push" {
		return Verdict{Outcome: Allow}, nil
	}

	reason := fmt.Sprintf("git %s: `ils push` is the one way an agent pushes a branch; where `ils push` "+
		"refuses the origin, the user pushes by hand", sub)

	return refuse(reason), nil
}

// trunkAnswer is origin's default branch as the trunk rule learned it for one git dir; found is
// false when origin did not answer and the clone holds no cached default branch.
type trunkAnswer struct {
	trunk string
	found bool
}

// trunks is the trunk rule for one command. It asks origin for the trunk at most once per git dir,
// so every invocation of the command in one repository is judged against the same answer.
type trunks struct {
	asked map[string]trunkAnswer
}

func newTrunks() *trunks {
	return &trunks{asked: map[string]trunkAnswer{}}
}

// refuseOnTrunk refuses a subcommand that adds commits to the branch HEAD is on when that branch is
// the trunk of a repository that has an origin, and one whose directory, target, or HEAD the guard
// cannot tell. A `git rebase` whose branch argument is the trunk is judged as a rebase on the trunk.
//
// It reads origin and HEAD locally before it asks origin for the trunk, so a repository with no
// origin or a detached HEAD starts no call to origin.
func (t *trunks) refuseOnTrunk(ctx context.Context, caller Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok || !trunkJudged(sub, inv.Git.Args) {
		return Verdict{Outcome: Allow}, nil
	}

	if inv.Dir == "" || inv.Target.Dir == "" || inv.Target.Unknown || inv.Git.GitDirUnknown || inv.HeadUnknown {
		reason := fmt.Sprintf("git %s: the guard cannot tell where the command runs, so it cannot tell "+
			"whether it lands on the trunk", sub)

		return refuse(reason), nil
	}

	if inv.Git.GitDir == "" {
		return Verdict{Outcome: Allow}, nil
	}

	// Every read here is of the repository, never of its work tree, so the git dir alone places it.
	repo := git.Repo{Runner: targetRunner(caller.Git, Invocation{Dir: inv.Target.Dir, Target: GitTarget{GitDir: inv.Git.GitDir}})}
	standing, err := repo.Standing(ctx)
	if err != nil {
		return Verdict{}, fmt.Errorf("read the origin and HEAD git %s runs with: %w", sub, err)
	}

	branch, onBranch := standing.Branch, standing.Kind == git.OnBranch
	if rebased, named := rebasedBranch(sub, inv.Git.Args); named {
		branch, onBranch = rebased, true
	}

	if standing.Kind == git.NoOrigin || !onBranch {
		return Verdict{Outcome: Allow}, nil
	}

	answer, err := t.trunkOf(ctx, repo, inv.Git.GitDir)
	if err != nil {
		return Verdict{}, fmt.Errorf("read the trunk git %s runs against: %w", sub, err)
	}

	if !answer.found {
		reason := fmt.Sprintf("git %s: origin could not be reached and the clone holds no cached default "+
			"branch (refs/remotes/origin/HEAD), so the guard cannot tell the trunk; `git fetch origin` caches it", sub)

		return refuse(reason), nil
	}

	if branch != answer.trunk {
		return Verdict{Outcome: Allow}, nil
	}

	reason := fmt.Sprintf("git %s on %s, the trunk: work goes on a branch, started with `ils branch <name>`",
		sub, answer.trunk)

	return refuse(reason), nil
}

// trunkOf returns the trunk of the repository at gitDir, asking origin only the first time.
func (t *trunks) trunkOf(ctx context.Context, repo git.Repo, gitDir string) (trunkAnswer, error) {
	if answer, asked := t.asked[gitDir]; asked {
		return answer, nil
	}

	trunk, found, err := repo.TrunkOrLastFetched(ctx)
	if err != nil {
		return trunkAnswer{}, err
	}

	answer := trunkAnswer{trunk: trunk, found: found}
	t.asked[gitDir] = answer

	return answer, nil
}

// trunkJudged reports whether the git subcommand sub, given args, adds commits to the branch it
// runs on: `git pull` does unless it may only fast-forward.
func trunkJudged(sub string, args []Word) bool {
	switch sub {
	case "commit", "merge", "cherry-pick", "revert", "am", "rebase":
		return true
	case "pull":
		options := readArgs(args, optionsOf(sub))

		return !slices.ContainsFunc(options.long, func(l longOption) bool { return l.name == "--ff-only" })
	default:
		return false
	}
}

// rebasedBranch returns the branch argument of `git rebase` with args, the branch it switches to
// before it rebases, and false when sub is not rebase or names no literal branch. Without `--root`
// the branch is the operand after the upstream; with it, the only operand.
func rebasedBranch(sub string, args []Word) (string, bool) {
	if sub != "rebase" {
		return "", false
	}

	options := readArgs(args, optionsOf(sub))
	at := 1
	if options.sets(0, "--root") {
		at = 0
	}

	if len(options.positional) <= at || !options.positional[at].Literal {
		return "", false
	}

	return options.positional[at].Text, true
}
