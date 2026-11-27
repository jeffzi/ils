package landing

import (
	"context"
	"errors"
	"slices"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// endStep is one git call of the local ending: the command a result names it by, the call, the
// next step when it fails, the next step when the branch it deletes moved past the tip, and the
// next step when it deleted the branch but not the branch's settings.
type endStep struct {
	run     func(ctx context.Context) error
	command result.Name
	next    result.Next
	moved   result.Next
	kept    result.Next
}

// localEnding is a local ending: where it leaves the checkout, its steps in order, and the lines
// it says once every step ran.
type localEnding struct {
	ending result.Ending
	steps  []endStep
	done   []result.Phrase
}

// end runs the local ending, on the trunk unless another worktree has it checked out, and ends the
// run done, with the lines said so far and where the checkout ended. It deletes the branch only
// while it still points at the tip the run checked, so a commit made since is kept.
func (m *mergeReady) end(ctx context.Context, deps Deps, req Request) *result.Result {
	repo, trunk, branch, tip := deps.Repo, m.Trunk, m.branch, m.tip
	markedTrunk, markedBranch := result.Branch(trunk), result.Branch(branch)
	deps.started(result.Say("Moving to origin's tip of %s", markedTrunk))
	drop := endStep{
		run:     func(ctx context.Context) error { return repo.DeleteBranch(ctx, branch, tip) },
		command: result.Concat("git branch -D ", markedBranch),
	}

	if m.elsewhere {
		drop.next = result.NextStep("run `git branch -D %s` by hand", markedBranch)
		drop.moved = result.NextStep("keep the commits past %s with `git branch <name> %s`, "+
			"then run `git branch -D %s` by hand", result.SHA(tip), markedBranch, markedBranch)
		drop.kept = result.NextStep("run `git config --local --remove-section branch.%s` by hand", markedBranch)

		return m.endSteps(ctx, deps, req, localEnding{ending: result.EndingDetached, steps: []endStep{
			{
				run: func(ctx context.Context) error {
					_, err := repo.Fetch(ctx, trunk)

					return err
				},
				command: result.Concat("git fetch origin ", markedTrunk), next: req.onceFixed(),
			},
			{
				run:     func(ctx context.Context) error { return repo.DetachAtOrigin(ctx, trunk) },
				command: result.Concat("git switch --detach refs/remotes/origin/", markedTrunk), next: req.onceFixed(),
			},
			drop,
		}, done: []result.Phrase{
			result.Say("Detached at origin's tip of %s; deleted local %s", markedTrunk, markedBranch),
			result.Say("The trunk %s is checked out in another worktree and updates there on its next "+
				"`git pull --ff-only`", markedTrunk),
		}})
	}

	drop.next = result.NextStep("run `git branch -D %s` and `git pull --ff-only origin %s` by hand",
		markedBranch, markedTrunk)
	drop.moved = result.NextStep("keep the commits past %s with `git branch <name> %s`, then run "+
		"`git branch -D %s` and `git pull --ff-only origin %s` by hand", result.SHA(tip), markedBranch,
		markedBranch, markedTrunk)
	drop.kept = result.NextStep("run `git config --local --remove-section branch.%s` and "+
		"`git pull --ff-only origin %s` by hand", markedBranch, markedTrunk)

	return m.endSteps(ctx, deps, req, localEnding{ending: result.EndingTrunk, steps: []endStep{
		{
			run:     func(ctx context.Context) error { return repo.Switch(ctx, trunk) },
			command: result.Concat("git switch ", markedTrunk), next: req.onceFixed(),
		},
		drop,
		{
			run:     func(ctx context.Context) error { return repo.FastForwardTrunk(ctx, trunk) },
			command: result.Concat("git pull --ff-only origin ", markedTrunk),
			next:    result.NextStep("run `git pull --ff-only origin %s` by hand", markedTrunk),
		},
	}, done: []result.Phrase{
		result.Say("Switched to %s at origin's tip; deleted local %s", markedTrunk, markedBranch),
	}})
}

// endSteps runs the steps of local in order and ends the run done where local leaves the checkout,
// saying what local says once done, the first of which is the line the ending's step says. A step
// that fails is broke, with lines that say which steps ran, which failed with its message, and
// which did not run. A step an interrupt stopped is broke the same way, saying the interrupt
// stopped it in place of its message. A delete refused because the branch moved is broke too,
// saying the step did not run.
func (m *mergeReady) endSteps(ctx context.Context, deps Deps, req Request, local localEnding) *result.Result {
	var ran []result.Phrase

	for i, step := range local.steps {
		if err := step.run(ctx); err != nil {
			return m.stepFailed(req, local.steps[i:], ran, err)
		}

		ran = append(ran, result.Say("Ran `%s`", step.command))
	}

	m.ending = local.ending
	deps.said(local.done[0])
	finished := req.ends(result.Done, result.Phrase{}, result.NextNone())
	finished.Lines = slices.Concat(m.lines, local.done)

	return m.carrying(finished)
}

// stepFailed is the broke result of the first of steps failing with err, after the steps of ran:
// its lines say which steps ran, which failed, and which did not run. A delete that removed the
// branch but not its settings ran, and the removal of the settings is the step that failed.
func (m *mergeReady) stepFailed(req Request, steps []endStep, ran []result.Phrase, err error) *result.Result {
	if errors.Is(err, git.ErrBranchMoved) {
		return m.branchMoved(req, steps, ran)
	}

	if errors.Is(err, git.ErrSettingsKept) {
		ran = append(ran, result.Say("Ran `%s`", steps[0].command))
		unset := endStep{
			command: result.Concat("git config --local --remove-section branch.", result.Branch(m.branch)),
			next:    steps[0].kept,
		}
		steps = slices.Concat([]endStep{unset}, steps[1:])
	}

	step := steps[0]
	failed := result.Say("`%s` failed: %s", step.command, err.Error())
	reason := result.Say("`%s` failed", step.command)

	if interrupted(err) {
		failed = result.Say("`%s` stopped when "+runInterrupted, step.command)
		reason = result.Say(runInterrupted+" during `%s`", step.command)
	}

	lines := slices.Concat(ran, []result.Phrase{failed}, notRunLines(steps[1:]))

	return m.broke(req, reason, step.next, lines...)
}

// notRunLines returns the lines that say steps did not run.
func notRunLines(steps []endStep) []result.Phrase {
	lines := make([]result.Phrase, 0, len(steps))
	for _, step := range steps {
		lines = append(lines, result.Say("Not run: `%s`", step.command))
	}

	return lines
}

// branchMoved is the broke result of the first of steps, the delete of the branch, refused because
// the branch moved past the tip the run checked, after the steps of ran: the branch is kept, and
// its lines say which steps ran and that the rest did not run.
func (m *mergeReady) branchMoved(req Request, steps []endStep, ran []result.Phrase) *result.Result {
	reason := result.Say("%s moved past %s, the tip `ils merge` checked, so it was kept",
		result.Branch(m.branch), result.SHA(m.tip))

	return m.broke(req, reason, steps[0].moved, slices.Concat(ran, notRunLines(steps))...)
}
