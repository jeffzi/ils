package landing

import (
	"context"
	"slices"

	"github.com/jeffzi/ils/internal/result"
)

// endStep is one git call of the local ending: the command a result names it by, the call, and
// the next step when it fails.
type endStep struct {
	run     func(ctx context.Context) error
	command result.Name
	next    result.Next
}

// localEnding is a local ending: where it leaves the checkout, its steps in order, and the lines
// it says once every step ran.
type localEnding struct {
	ending result.Ending
	steps  []endStep
	done   []result.Phrase
}

// end runs the local ending, on the trunk unless another worktree has it checked out, and ends the
// run done, with the lines said so far and where the checkout ended.
func (m *mergeReady) end(ctx context.Context, deps Deps, req Request) *result.Result {
	repo, trunk, branch := deps.Repo, m.Trunk, m.branch
	deps.started(result.Say("Moving to origin's tip of %s", result.Branch(trunk)))
	drop := endStep{
		run:     func(ctx context.Context) error { return repo.DeleteBranch(ctx, branch) },
		command: result.Concat("git branch -D ", result.Branch(branch)),
	}

	if m.elsewhere {
		drop.next = result.NextStep("run `git branch -D %s` by hand", result.Branch(branch))

		return m.endSteps(ctx, deps, req, localEnding{ending: result.EndingDetached, steps: []endStep{
			{
				run: func(ctx context.Context) error {
					_, err := repo.Fetch(ctx, trunk)

					return err
				},
				command: result.Concat("git fetch origin ", result.Branch(trunk)), next: req.onceFixed(),
			},
			{
				run:     func(ctx context.Context) error { return repo.DetachAtOrigin(ctx, trunk) },
				command: result.Concat("git switch --detach refs/remotes/origin/", result.Branch(trunk)), next: req.onceFixed(),
			},
			drop,
		}, done: []result.Phrase{
			result.Say("Detached at origin's tip of %s; deleted local %s", result.Branch(trunk), result.Branch(branch)),
			result.Say("The trunk %s is checked out in another worktree and updates there on its next "+
				"`git pull --ff-only`", result.Branch(trunk)),
		}})
	}

	drop.next = result.NextStep("run `git branch -D %s` and `git pull --ff-only origin %s` by hand",
		result.Branch(branch), result.Branch(trunk))

	return m.endSteps(ctx, deps, req, localEnding{ending: result.EndingTrunk, steps: []endStep{
		{
			run:     func(ctx context.Context) error { return repo.Switch(ctx, trunk) },
			command: result.Concat("git switch ", result.Branch(trunk)), next: req.onceFixed(),
		},
		drop,
		{
			run:     func(ctx context.Context) error { return repo.FastForwardTrunk(ctx, trunk) },
			command: result.Concat("git pull --ff-only origin ", result.Branch(trunk)),
			next:    result.NextStep("run `git pull --ff-only origin %s` by hand", result.Branch(trunk)),
		},
	}, done: []result.Phrase{
		result.Say("Switched to %s at origin's tip; deleted local %s", result.Branch(trunk), result.Branch(branch)),
	}})
}

// endSteps runs the steps of local in order and ends the run done where local leaves the checkout,
// saying what local says once done, the first of which is the line the ending's step says. A step
// that fails is broke, with lines that say which steps ran, which failed with its message, and
// which did not run. A step an interrupt stopped is broke the same way, saying the interrupt
// stopped it in place of its message.
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
// its lines say which steps ran, which failed, and which did not run.
func (m *mergeReady) stepFailed(req Request, steps []endStep, ran []result.Phrase, err error) *result.Result {
	step := steps[0]
	failed := result.Say("`%s` failed: %s", step.command, err.Error())
	reason := result.Say("`%s` failed", step.command)

	if interrupted(err) {
		failed = result.Say("`%s` stopped when "+runInterrupted, step.command)
		reason = result.Say(runInterrupted+" during `%s`", step.command)
	}

	lines := slices.Concat(ran, []result.Phrase{failed})
	for _, rest := range steps[1:] {
		lines = append(lines, result.Say("Not run: `%s`", rest.command))
	}

	return m.broke(req, reason, step.next, lines...)
}
