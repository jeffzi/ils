package landing

import (
	"context"
	"errors"
	"fmt"
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
	// WaitConflicting is a pull request that conflicts with the trunk, found before the checks
	// were read, by the merge state read after they were green, or by the merge state read after
	// nothing reported, when the trunk has no CI.
	WaitConflicting WaitOutcome = "conflicting"
	// WaitBehind is a pull request behind a trunk it must be up to date with, found before the
	// checks were read, by the merge state read after they were green, or by the merge state read
	// after nothing reported, when the trunk has no CI.
	WaitBehind WaitOutcome = "behind"
	// WaitNeverReported is a required check with no check on the commit and no run that
	// could still start one, while something else did report; Waited.Verdict.MissingRequired
	// names the checks.
	WaitNeverReported WaitOutcome = "never-reported"
	// WaitNothingReported is no deciding check on the commit once the wait had nothing left
	// to wait for.
	WaitNothingReported WaitOutcome = "nothing-reported"
	// WaitPending is a wait that ended before the forge had an answer: the deadline ended it,
	// or the stage's own window did when Waited.Window is set. Waited.Stage says what it was
	// waiting for.
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
	Number   int
}

// Waited is what the wait after a push found.
type Waited struct {
	// Since is when the wait for the checks began; it is the zero time when no verdict read
	// completed.
	Since   time.Time
	Outcome WaitOutcome
	// Stage is the stage the wait ended in; it is set only when Outcome is WaitPending.
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
	// Window is how long the stage waited when that, and not the deadline, ended the wait; it
	// is zero when the deadline ended it.
	Window time.Duration
}

// takeState records the merge state head reports and the forge's own value behind it.
func (w *Waited) takeState(head forge.Head) {
	w.MergeState, w.RawMergeState = head.MergeState, head.RawMergeState
}

// apartAfterGreen turns green checks into WaitConflicting or WaitBehind when state, read after
// them, says the pull request cannot merge: green checks say nothing about that.
func (w *Waited) apartAfterGreen(state forge.MergeState) {
	if w.Outcome != WaitGreen {
		return
	}

	w.takeApart(state)
}

// apartWithoutCI turns nothing reported into WaitConflicting or WaitBehind when state, read after
// the wait, says the pull request cannot merge and the trunk has no CI, and reports whether it did.
func (w *Waited) apartWithoutCI(state forge.MergeState) bool {
	if w.Outcome != WaitNothingReported {
		return false
	}

	return w.takeApart(state)
}

// takeApart sets the outcome to WaitConflicting or WaitBehind when state says the pull request
// cannot merge, and reports whether it did.
func (w *Waited) takeApart(state forge.MergeState) bool {
	outcome, apart := apartOutcome(state)
	if apart {
		w.Outcome = outcome
	}

	return apart
}

// apartOutcome returns the outcome for a merge state that says the pull request cannot merge, and
// false for any other state.
func apartOutcome(state forge.MergeState) (WaitOutcome, bool) {
	switch state {
	case forge.MergeConflicting:
		return WaitConflicting, true
	case forge.MergeBehind:
		return WaitBehind, true
	default:
		return "", false
	}
}

// WaitError reports a read of the wait after a push that failed for a reason other than the
// context's deadline.
type WaitError struct {
	Err   error
	Stage WaitStage
}

// Error names the read that failed and why.
func (e *WaitError) Error() string {
	return fmt.Sprintf("read %s: %s", e.Stage, e.Err)
}

// Unwrap returns the failed read's error, so that errors.Is and errors.AsType see the cause
// behind the stage.
func (e *WaitError) Unwrap() error {
	return e.Err
}

// errOutOfTime reports that the deadline leaves no time for another poll and the read after it.
var errOutOfTime = errors.New("the deadline leaves no time for another read")

// errWindowPassed reports that mergeWindow passed with the merge state still unknown.
var errWindowPassed = errors.New("the merge state stayed unknown for the whole window")

// AwaitVerdict waits, after a push, for what the forge says about the pushed commit.
//
// It reads the pull request's head every 2 seconds until that is the pushed commit, reads the
// merge state from the same read, every 2 seconds for up to 30 seconds while it is unknown,
// and stops there on a conflict or a branch behind its trunk. Otherwise it reads the verdict
// every 5 seconds until the checks conclude, a red verdict concluding once the forge has
// settled its failed log, and then reads the merge state once more; a
// conflict or a branch behind its trunk found by that read ends green checks as WaitConflicting
// or WaitBehind. AwaitVerdict does not make the third route to those outcomes: a caller turns
// WaitNothingReported into them by the merge state it reads after nothing reported, when the
// trunk has no CI.
//
// No poll is started that would end past ctx's deadline.
// A deadline that ends a stage, inside a read or a poll or between two, is the outcome
// WaitPending with that stage and no error, but for a red verdict still waiting for its failed
// log, which stays WaitRed. A merge state still unknown after its 30 seconds,
// with time left before the deadline, is WaitPending too, with Waited.Window set. A read that
// fails for any other reason is a *WaitError naming its stage. The read of the merge state
// made after the checks belongs to StageMergeState: its failure is a *WaitError, and the
// deadline ending inside it is WaitPending, at that stage when the checks had concluded, with
// the verdict they gave.
func AwaitVerdict(ctx context.Context, deps Deps, pushed Pushed) (Waited, error) {
	w := waiter{deps: deps, pushed: pushed}

	waited, stage, err := w.run(ctx)
	if err == nil {
		return waited, nil
	}

	windowPassed := errors.Is(err, errWindowPassed)
	if windowPassed || errors.Is(err, errOutOfTime) || errors.Is(err, context.DeadlineExceeded) {
		waited.Outcome = WaitPending
		waited.Stage = stage

		if windowPassed {
			waited.Window = mergeWindow
		}

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
	waited.takeState(head)

	if err != nil {
		return waited, StageHead, err
	}

	head, err = w.mergeKnown(ctx, head)
	waited.takeState(head)

	if err != nil {
		return waited, StageMergeState, err
	}

	// A conflicting pull request runs no workflow, so its missing checks say nothing.
	if waited.takeApart(head.MergeState) {
		return waited, "", nil
	}

	verdict, outcome, err := w.concluded(ctx, &waited)
	waited.Outcome = outcome
	waited.Verdict = verdict
	waited.Deciding = verdict.Deciding()
	waited.Passed, waited.Skipped = tally(waited.Deciding)

	// The merge state is read again only when the checks concluded or ran out of time with
	// some left: a verdict read that failed is the failure to report.
	verdictFailed := err != nil && !errors.Is(err, errOutOfTime)
	if ctxEnded(ctx) || verdictFailed {
		return waited, StageVerdict, err
	}

	after, afterErr := w.head(ctx)

	switch {
	case afterErr == nil:
		waited.takeState(after)
		waited.apartAfterGreen(after.MergeState)
	// A failure of this read is reported whatever the checks said. The deadline ending
	// inside it matters only when the checks concluded: a pending wait stays pending.
	case err == nil || !errors.Is(afterErr, context.DeadlineExceeded):
		return waited, StageMergeState, afterErr
	}

	return waited, StageVerdict, err
}

// head reads the pull request's head and merge state, naming the trunk's required checks so that
// the state accounts for them.
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
			return head, errWindowPassed
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
// with how they concluded. It sets waited.Since to when the wait began once the first verdict
// read has completed, so that Since stays the zero time when no verdict was read.
func (w waiter) concluded(ctx context.Context, waited *Waited) (forge.Verdict, WaitOutcome, error) {
	query := forge.VerdictQuery{SHA: w.pushed.SHA, Number: w.pushed.Number, Required: w.pushed.Required}
	began := time.Now()

	var (
		last    forge.Verdict
		outcome WaitOutcome
		done    bool
	)

	for {
		verdict, err := w.deps.Forge.Verdict(ctx, w.pushed.Repo, query)
		if err != nil {
			outcome, err = cutShort(outcome, err)

			return last, outcome, err
		}

		last = verdict
		waited.Since = began

		outcome, done = w.judge(verdict)
		// Runs appear some seconds after a push, so silence is only believed once it has lasted.
		if done && silent(verdict) && time.Since(began)+verdictPoll <= silentRetry {
			done = false
		}

		if done {
			return last, outcome, nil
		}

		if err = pause(ctx, verdictPoll); err != nil {
			outcome, err = cutShort(outcome, err)

			return last, outcome, err
		}
	}
}

// cutShort returns how a wait for the verdict that err ended concluded: a red verdict still
// waiting for its failed log stays red when the deadline ends the wait, since the log is all it
// lacked, and anything else is err with no outcome.
func cutShort(outcome WaitOutcome, err error) (WaitOutcome, error) {
	if outcome == WaitRed && (errors.Is(err, errOutOfTime) || errors.Is(err, context.DeadlineExceeded)) {
		return WaitRed, nil
	}

	return "", err
}

// judge returns how the checks concluded, and false while the wait for them goes on.
func (w waiter) judge(verdict forge.Verdict) (WaitOutcome, bool) {
	state := verdict.State()
	missing := len(verdict.MissingRequired) > 0

	switch {
	// The forge gives a failed log only once the run completes, and the excerpt is what the
	// fix starts from, so a red verdict waits for it like a pending one would.
	case state == forge.VerdictRed && verdict.FailedLog.State == forge.FailedLogInProgress:
		return WaitRed, false
	case state == forge.VerdictRed:
		return WaitRed, true
	case state == forge.VerdictPending:
		return "", false
	// Where every check decides, a run still going can create a check that is not there yet;
	// where required names decide, it matters only while one of them is absent.
	case verdict.Running() && (missing || len(w.pushed.Required) == 0):
		return "", false
	case missing && !silent(verdict):
		return WaitNeverReported, true
	case missing || state == forge.VerdictNone:
		return WaitNothingReported, true
	default:
		return WaitGreen, true
	}
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

// silent reports whether nothing has reported on the commit at all: no check and no run.
func silent(verdict forge.Verdict) bool {
	return len(verdict.Checks) == 0 && len(verdict.Runs) == 0
}

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
