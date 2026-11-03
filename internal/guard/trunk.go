//go:build unix

package guard

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jeffzi/ils/internal/git"
)

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
// cannot tell. A `git rebase` whose branch argument is the trunk is judged as a rebase on the trunk,
// and one whose branch argument the guard cannot read is refused, since that branch may be the trunk.
//
// It reads origin and HEAD locally before it asks origin for the trunk, so a repository with no
// origin or a detached HEAD starts no call to origin.
func (t *trunks) refuseOnTrunk(ctx context.Context, caller Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok || !trunkJudged(sub, inv.Git.Args) {
		return Verdict{Outcome: Allow}, nil
	}

	placeUnknown := inv.Dir == "" || inv.Target.Dir == "" || inv.Target.Unknown || inv.Git.GitDirUnknown || inv.HeadUnknown
	if placeUnknown {
		reason := fmt.Sprintf("git %s: the guard cannot tell where the command runs, so it cannot tell "+
			"whether it lands on the trunk", sub)

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

	branch, onBranch := standing.Branch, standing.Kind == git.OnBranch
	rebased, named := rebasedBranch(sub, inv.Git.Args)
	if named {
		branch, onBranch = rebased.Text, true
	}

	if standing.Kind == git.NoOrigin || !onBranch {
		return Verdict{Outcome: Allow}, nil
	}

	if named && !rebased.Literal {
		reason := fmt.Sprintf("git %s: the guard cannot read the branch it switches to, so it cannot tell "+
			"which branch the rebase lands on", sub)

		return refuse(reason), nil
	}

	answer, err := t.trunkOf(ctx, repo, inv.Git.GitDir)
	if err != nil {
		return Verdict{}, fmt.Errorf("read the trunk git %s runs against: %w", sub, err)
	}

	if !answer.found {
		reason := fmt.Sprintf("git %s: origin could not be reached and the clone holds no cached default "+
			"branch (refs/remotes/origin/HEAD), so the guard cannot tell the trunk; run `! git remote set-head origin --auto`, or run it outside the agent, "+
			"to cache it", sub)

		return refuse(reason), nil
	}

	if branch != answer.trunk {
		return Verdict{Outcome: Allow}, nil
	}

	return onTrunk(sub, inv.Git.Args, answer.trunk), nil
}

// onTrunk returns the verdict on the trunk-judged git subcommand sub, given args, run on trunk. A
// `git pull` that may only fast-forward is allowed when it names origin and the trunk alone; one
// that names any other repository or refspec, or one the guard cannot read, can fast-forward the
// trunk to another branch.
func onTrunk(sub string, args []Word, trunk string) Verdict {
	options := readArgs(args, optionsOf(sub))
	if sub == "pull" && fastForwardsOnly(options.long) {
		// origin alone pulls what a plain `git pull --ff-only` pulls: the trunk's own upstream. A
		// refspec names the trunk with or without a leading `+` and its full ref.
		named := wordTexts(options.positional)
		if len(named) == 2 {
			named[1] = strings.TrimPrefix(strings.TrimPrefix(named[1], "+"), "refs/heads/")
		}

		pullsTrunk := slices.Equal(named, []string{"origin"}) || slices.Equal(named, []string{"origin", trunk})
		if !options.opaque() && pullsTrunk {
			return Verdict{Outcome: Allow}
		}

		return refuse(fmt.Sprintf("git pull --ff-only on %s, the trunk, naming a repository or refspec other "+
			"than origin %s: it can fast-forward the trunk to another branch; run `git pull --ff-only` alone",
			trunk, trunk))
	}

	return refuse(fmt.Sprintf("git %s on %s, the trunk: work goes on a branch, started with `ils branch <name>`",
		sub, trunk))
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
