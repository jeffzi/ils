package landing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jeffzi/ils/internal/forge"
)

const (
	// headPoll is how often the pull request's head is read until it is the pushed commit:
	// GitHub catches up with a push within seconds, and a verdict read before it has is the
	// previous commit's.
	headPoll = 2 * time.Second
	// mergePoll is how often the merge state is read while GitHub has not worked it out:
	// GitHub computes it lazily, on the first read after a push, and has it seconds later.
	mergePoll = 2 * time.Second
	// mergeWindow is how long an unknown merge state is polled: longer than GitHub takes to
	// compute one, and short enough to leave most of the bound to the checks.
	mergeWindow = 30 * time.Second
	// verdictPoll is how often the verdict is read: one read is several calls to GitHub, and
	// no check concludes faster than a few seconds.
	verdictPoll = 5 * time.Second
	// silentRetry is how long the verdict is read again while nothing has reported at all:
	// GitHub creates a push's workflow runs some seconds after the push, and CI that has not
	// shown by then did not fire.
	silentRetry = 20 * time.Second
)

// WaitOutcome is how the wait after a push ended.
type WaitOutcome string

// The outcomes of the wait after a push.
const (
	// WaitGreen is at least one deciding check, each one passed or skipped.
	WaitGreen WaitOutcome = "green"
	// WaitRed is a deciding check that failed or was canceled.
	WaitRed WaitOutcome = "red"
	// WaitConflicting is a pull request that conflicts with the trunk; no verdict was read.
	WaitConflicting WaitOutcome = "conflicting"
	// WaitBehind is a pull request behind a trunk it must be up to date with; no verdict was read.
	WaitBehind WaitOutcome = "behind"
	// WaitNeverReported is a required check with no check on the commit and no run that
	// could still start one, while something else did report; Waited.Verdict.MissingRequired
	// names the checks.
	WaitNeverReported WaitOutcome = "never-reported"
	// WaitNothingReported is no deciding check on the commit once the wait had nothing left
	// to wait for.
	WaitNothingReported WaitOutcome = "nothing-reported"
	// WaitPending is a wait the deadline ended; Waited.Stage says what it was waiting for.
	WaitPending WaitOutcome = "pending"
)

// WaitStage is one part of the wait after a push, named for what it reads.
type WaitStage string

// The stages of the wait after a push, in order.
const (
	// StageHead waits for the pull request's head to be the pushed commit.
	StageHead WaitStage = "head"
	// StageMergeState waits for the forge to work the merge state out.
	StageMergeState WaitStage = "merge-state"
	// StageVerdict waits for the checks of the pushed commit.
	StageVerdict WaitStage = "verdict"
)

// Pushed names the commit a push put on a pull request.
type Pushed struct {
	SHA string
	// Required names the checks the trunk's protection requires; when empty, every check decides.
	Required []string
	Repo     forge.Repo
	// Number is the pull request's.
	Number int
}

// Waited is what the wait after a push found.
type Waited struct {
	// Since is when the wait for the checks began; it is the zero time when no verdict was read.
	Since   time.Time
	Outcome WaitOutcome
	// Stage is the stage the deadline ended; it is set only when Outcome is WaitPending.
	Stage WaitStage
	// MergeState is the merge state read after the wait for the checks, or the one read
	// before it when the deadline had ended by then, or cut that read short, or no verdict
	// was read.
	MergeState forge.MergeState
	// RawMergeState is the forge's own value behind MergeState, kept for printing.
	RawMergeState string
	// Deciding are the checks that decide: the required ones when Pushed.Required names
	// any, and every check otherwise.
	Deciding []forge.Check
	// Verdict is the last verdict read; its Pending method returns the checks still running.
	Verdict forge.Verdict
	// Passed and Skipped count the deciding checks in each of those buckets.
	Passed  int
	Skipped int
}

// WaitError reports a read of the wait after a push that failed for a reason other than the
// context's deadline.
type WaitError struct {
	Err error
	// Stage names the read that failed.
	Stage WaitStage
}

// Error names the read that failed and why.
func (e *WaitError) Error() string {
	return fmt.Sprintf("read %s: %s", e.Stage, e.Err)
}

// Unwrap returns the error of the read.
func (e *WaitError) Unwrap() error {
	return e.Err
}

// errOutOfTime reports that the deadline leaves no time for another poll and the read after it.
var errOutOfTime = errors.New("the deadline leaves no time for another read")

// AwaitVerdict waits, after a push, for what the forge says about the pushed commit.
//
// It reads the pull request's head every 2 seconds until that is the pushed commit, reads the
// merge state from the same read, every 2 seconds for up to 30 seconds while it is unknown,
// and stops there on a conflict or a branch behind its trunk. Otherwise it reads the verdict
// every 5 seconds until the checks conclude, and then reads the merge state once more.
//
// No poll is started that would end past ctx's deadline.
// A deadline that ends a stage, inside a read or a poll or between two, is the outcome
// WaitPending with that stage and no error. A read that fails for any other reason is a
// *WaitError naming its stage. The read of the merge state made after the checks belongs to
// StageMergeState: its failure is a *WaitError, and the deadline ending inside it is
// WaitPending, at that stage when the checks had concluded, with the verdict they gave.
func AwaitVerdict(ctx context.Context, deps Deps, pushed Pushed) (Waited, error) {
	w := waiter{deps: deps, pushed: pushed}

	waited, stage, err := w.run(ctx)
	if err == nil {
		return waited, nil
	}

	if errors.Is(err, errOutOfTime) || errors.Is(err, context.DeadlineExceeded) {
		waited.Outcome = WaitPending
		waited.Stage = stage

		return waited, nil
	}

	return Waited{}, &WaitError{Stage: stage, Err: err}
}

// waiter is one wait after a push: what it reads through and the commit it reads about.
type waiter struct {
	deps   Deps
	pushed Pushed
}

// run goes through the stages in order and returns what they found; when a stage fails or
// runs out of time, it returns that stage, the error, and what was found up to there.
func (w waiter) run(ctx context.Context) (Waited, WaitStage, error) {
	var waited Waited

	head, err := w.caughtUp(ctx)
	waited.MergeState, waited.RawMergeState = head.MergeState, head.RawMergeState

	if err != nil {
		return waited, StageHead, err
	}

	head, err = w.mergeKnown(ctx, head)
	waited.MergeState, waited.RawMergeState = head.MergeState, head.RawMergeState

	if err != nil {
		return waited, StageMergeState, err
	}

	// A conflicting pull request runs no workflow, so its missing checks say nothing.
	switch head.MergeState {
	case forge.MergeConflicting:
		waited.Outcome = WaitConflicting

		return waited, "", nil
	case forge.MergeBehind:
		waited.Outcome = WaitBehind

		return waited, "", nil
	default:
	}

	waited.Since = time.Now()

	verdict, outcome, err := w.concluded(ctx, waited.Since)
	waited.Outcome = outcome
	waited.Verdict = verdict
	waited.Deciding = w.deciding(verdict)
	waited.Passed, waited.Skipped = tally(waited.Deciding)

	// The merge state is read again only when the checks concluded or ran out of time with
	// some left: a verdict read that failed is the failure to report.
	if ctxEnded(ctx) || (err != nil && !errors.Is(err, errOutOfTime)) {
		return waited, StageVerdict, err
	}

	after, afterErr := w.head(ctx)

	switch {
	case afterErr == nil:
		waited.MergeState, waited.RawMergeState = after.MergeState, after.RawMergeState
	// A failure of this read is reported whatever the checks said. The deadline ending
	// inside it matters only when the checks concluded: a pending wait stays pending.
	case err == nil || !errors.Is(afterErr, context.DeadlineExceeded):
		return waited, StageMergeState, afterErr
	}

	return waited, StageVerdict, err
}

// head reads the pull request's head.
func (w waiter) head(ctx context.Context) (forge.Head, error) {
	query := forge.HeadQuery{Number: w.pushed.Number, Required: w.pushed.Required}

	return w.deps.Forge.Head(ctx, w.pushed.Repo, query)
}

// caughtUp polls the head until it is the pushed commit and returns the last head it read.
func (w waiter) caughtUp(ctx context.Context) (forge.Head, error) {
	var last forge.Head

	for {
		head, err := w.head(ctx)
		if err != nil {
			return last, err
		}

		last = head
		if head.SHA == w.pushed.SHA {
			return last, nil
		}

		if err = pause(ctx, headPoll); err != nil {
			return last, err
		}
	}
}

// mergeKnown polls the head, starting from the one just read, until its merge state is known
// or mergeWindow has passed, and returns the last head it read.
func (w waiter) mergeKnown(ctx context.Context, head forge.Head) (forge.Head, error) {
	began := time.Now()

	for head.MergeState == forge.MergeUnknown {
		if time.Since(began)+mergePoll > mergeWindow {
			return head, errOutOfTime
		}

		if err := pause(ctx, mergePoll); err != nil {
			return head, err
		}

		next, err := w.head(ctx)
		if err != nil {
			return head, err
		}

		head = next
	}

	return head, nil
}

// concluded polls the verdict until the checks conclude and returns the last verdict it read
// with how they concluded; began is when the wait for them started.
func (w waiter) concluded(ctx context.Context, began time.Time) (forge.Verdict, WaitOutcome, error) {
	query := forge.VerdictQuery{SHA: w.pushed.SHA, Number: w.pushed.Number, Required: w.pushed.Required}

	var last forge.Verdict

	for {
		verdict, err := w.deps.Forge.Verdict(ctx, w.pushed.Repo, query)
		if err != nil {
			return last, "", err
		}

		last = verdict

		outcome, done := w.judge(verdict)
		// Runs appear some seconds after a push, so silence is only believed once it has lasted.
		if done && silent(verdict) && time.Since(began)+verdictPoll <= silentRetry {
			done = false
		}

		if done {
			return last, outcome, nil
		}

		if err = pause(ctx, verdictPoll); err != nil {
			return last, "", err
		}
	}
}

// judge returns how the checks concluded, and false while the wait for them goes on.
func (w waiter) judge(verdict forge.Verdict) (WaitOutcome, bool) {
	state := verdict.State()
	missing := len(verdict.MissingRequired) > 0

	switch {
	case state == forge.VerdictRed:
		return WaitRed, true
	case state == forge.VerdictPending:
		return "", false
	// Where every check decides, a run still going can create a check that is not there yet;
	// where required names decide, it matters only while one of them is absent.
	case running(verdict) && (missing || len(w.pushed.Required) == 0):
		return "", false
	case missing && !silent(verdict):
		return WaitNeverReported, true
	case missing || state == forge.VerdictNone:
		return WaitNothingReported, true
	default:
		return WaitGreen, true
	}
}

// deciding returns the checks of verdict that decide.
func (w waiter) deciding(verdict forge.Verdict) []forge.Check {
	if len(w.pushed.Required) == 0 {
		return verdict.Checks
	}

	var deciding []forge.Check

	for _, check := range verdict.Checks {
		if check.Required {
			deciding = append(deciding, check)
		}
	}

	return deciding
}

// ctxEnded reports whether ctx has ended or its deadline is no longer ahead of the current time.
func ctxEnded(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}

	deadline, ok := ctx.Deadline()

	return ok && time.Until(deadline) <= 0
}

// pause waits until d has passed, or returns ctx's error as soon as ctx ends. It returns
// errOutOfTime, without waiting, when the wait would end past ctx's deadline, and after
// waiting when it ended on the deadline, which leaves no time for a read.
func pause(ctx context.Context, d time.Duration) error {
	if deadline, ok := ctx.Deadline(); ok && d > time.Until(deadline) {
		return errOutOfTime
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait %s: %w", d, ctx.Err())
	case <-timer.C:
	}

	if ctxEnded(ctx) {
		return errOutOfTime
	}

	return nil
}

// running reports whether a workflow run of verdict is queued or in progress; a run that
// awaits a person is not, since nothing says it will ever start.
func running(verdict forge.Verdict) bool {
	return slices.ContainsFunc(verdict.Runs, func(run forge.Run) bool {
		return run.Status == forge.RunQueued || run.Status == forge.RunInProgress
	})
}

// silent reports whether nothing has reported on the commit at all: no check and no run.
func silent(verdict forge.Verdict) bool {
	return len(verdict.Checks) == 0 && len(verdict.Runs) == 0
}

// tally counts the checks that passed and those that were skipped.
func tally(checks []forge.Check) (passed, skipped int) {
	for _, check := range checks {
		switch check.Bucket {
		case forge.BucketPass:
			passed++
		case forge.BucketSkipped:
			skipped++
		default:
		}
	}

	return passed, skipped
}
