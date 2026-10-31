package landing

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/result"
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
	// headReadReserve is the time kept before the deadline for the read of the head after the
	// checks, from the verdict poll and from every verdict read after the first: one gh read of the
	// head, without which a verdict of checks still running cannot be shown to be the pushed
	// commit's.
	headReadReserve = 3 * time.Second
	// silentRetry is how long the verdict is read again while no workflow run has appeared and
	// nothing failed: GitHub creates a push's workflow runs some seconds after the push, while an
	// app or a status can report sooner, and CI that has not shown by then did not fire.
	silentRetry = 20 * time.Second
)

// WaitOutcome is how the wait for the checks of a pull request's head ended.
type WaitOutcome string

// The outcomes of the wait for the checks of a pull request's head.
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
	// WaitPending is a wait that ended before the forge had an answer: Waited.Cutoff says what
	// ended it, the deadline, an interrupt, or the stage's own window. Waited.Stage says what it
	// was waiting for. Checks still running at the deadline end it at StageVerdict, holding their
	// verdict when the read of the head after them showed the pushed commit, and none, with
	// Waited.Since zero, when the deadline took that read, the first read of the checks, or a poll
	// of them. A later read of the checks that the 3 seconds kept for the head read ended keeps
	// the verdict before it, which the head read after the checks confirms. Checks that had
	// concluded end it at StageMergeState, with no verdict, when the deadline took that read. The
	// verdict poll and every verdict read after the first keep 3 seconds before the deadline for
	// that read.
	WaitPending WaitOutcome = "pending"
	// WaitMoved is a pull request whose head, read after the checks, is no longer the pushed
	// commit; Waited.MovedTo names the commit it is on. The forge reads the checks of the pull
	// request's current head, so the verdict read may be the new commit's and is not kept.
	WaitMoved WaitOutcome = "moved"
)

// WaitStage is one part of the wait for the checks of a pull request's head, named for what it
// reads.
type WaitStage string

// The stages of the wait for the checks of a pull request's head, in order.
const (
	// StageHead waits for the pull request's head to be the pushed commit.
	StageHead WaitStage = "head"
	// StageMergeState waits for the forge to work the merge state out.
	StageMergeState WaitStage = "merge-state"
	// StageVerdict waits for the checks of the pushed commit.
	StageVerdict WaitStage = "verdict"
)

// Pushed names the commit whose checks the wait reads and the pull request it is on: the commit
// a push just put there, or the head `ils merge` is about to land.
type Pushed struct {
	SHA string
	// Required names the checks the trunk's protection requires; when empty, every check decides.
	Required []string
	Repo     forge.Repo
	Number   int
}

// Waited is what the wait for the checks of a pull request's head found.
type Waited struct {
	// Since is when the wait for the checks began; it is the zero time when Verdict holds no
	// verdict read.
	Since   time.Time
	Outcome WaitOutcome
	// MovedTo is the commit the pull request's head is on when Outcome is WaitMoved, and empty
	// otherwise.
	MovedTo string
	// Stage is the stage the wait ended in; it is set only when Outcome is WaitPending.
	Stage WaitStage
	// MergeState is the merge state read after the wait for the checks, or the one read
	// before it when no read after it showed the pushed commit.
	MergeState forge.MergeState
	// Deciding are the checks that decide: the required ones when Pushed.Required names
	// any, and every check otherwise.
	Deciding []forge.Check
	// Verdict is the last verdict read; its Pending method returns the checks still running.
	Verdict forge.Verdict
	// Passed and Skipped count the deciding checks in each of those buckets.
	Passed  int
	Skipped int
	// Cutoff is what ended the wait when Outcome is WaitPending; the zero value is the deadline.
	Cutoff WaitCutoff
}

// WaitCutoff is what ended a pending wait.
type WaitCutoff int

// The causes that end a wait before the forge had an answer.
const (
	// CutoffDeadline is the deadline of the wait's context, or too little time left before it.
	CutoffDeadline WaitCutoff = iota
	// CutoffWindow is mergeWindow passing with the merge state still unknown, with time left
	// before the deadline.
	CutoffWindow
	// CutoffInterrupt is an interrupt, which cancels the wait's context.
	CutoffInterrupt
)

// unconfirmed returns a Waited holding only the merge state w read before the checks, as the wait
// ends when no read of the head after the checks showed the pushed commit.
func (w Waited) unconfirmed() Waited {
	return Waited{MergeState: w.MergeState}
}

// apartWithoutCI turns nothing reported into WaitConflicting or WaitBehind when the merge state,
// read after the wait, says the pull request cannot merge and the trunk has no CI, and reports
// whether it did.
func (w *Waited) apartWithoutCI() bool {
	if w.Outcome != WaitNothingReported {
		return false
	}

	return w.takeApart()
}

// takeApart sets the outcome to WaitConflicting or WaitBehind when the merge state says the pull
// request cannot merge, and reports whether it did.
func (w *Waited) takeApart() bool {
	switch w.MergeState {
	case forge.MergeConflicting:
		w.Outcome = WaitConflicting
	case forge.MergeBehind:
		w.Outcome = WaitBehind
	default:
		return false
	}

	return true
}

// WaitError reports a read of the wait for the checks of a pull request's head that failed for
// a reason other than the context ending.
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

// errOutOfTime reports that the deadline leaves no time for another poll and the read after it, or
// that headReadReserve before the deadline ended a verdict read already in flight.
var errOutOfTime = errors.New("the deadline leaves no time for another read")

// errWindowPassed reports that mergeWindow passed with the merge state still unknown.
var errWindowPassed = errors.New("the merge state stayed unknown for the whole window")

// cutOff reports whether err says the deadline or an interrupt ended a read, or the deadline left
// no time for one.
func cutOff(err error) bool {
	return errors.Is(err, errOutOfTime) || endedEarly(err)
}

// AwaitVerdict waits for what the forge says about the commit in pushed, the one a push just
// put on the pull request or the head a merge is about to land.
//
// It reads the pull request's head every 2 seconds until that is the pushed commit, reads the
// merge state from the same read, every 2 seconds for up to 30 seconds while it is unknown,
// and stops there on a conflict or a branch behind its trunk. Otherwise it reads the verdict
// every 5 seconds until the checks conclude, a red verdict concluding once the forge has settled
// its failed log, and then reads the merge state once more; a conflict or a branch behind its
// trunk found by that read ends green checks as WaitConflicting or WaitBehind. That read, or a
// read of the merge state before the checks, finding the head on another commit than the pushed
// one ends the wait as WaitMoved, whatever the checks said, since the verdict and the merge state
// may be that commit's.
// AwaitVerdict does not make the third route to WaitConflicting and WaitBehind: a caller turns
// WaitNothingReported into them by the merge state it reads after nothing reported, when the
// trunk has no CI.
//
// No poll of the head or of the merge state is started that would end past ctx's deadline, and
// no poll of the verdict that would leave less than 3 seconds before it: that time is kept for the
// read of the head after the checks, which is made at once instead. Every read of the verdict
// after the first runs under a deadline 3 seconds before ctx's for the same reason; the first
// runs under ctx's own.
// A deadline that ends a stage, inside a read or a poll or between two, is the outcome
// WaitPending with that stage and no error. So is an interrupt, which cancels ctx, with
// Waited.Cutoff CutoffInterrupt; it ends the wait at that instant, as a deadline not known ahead does.
// The exception is a red verdict still waiting for its failed log, which stays WaitRed when the
// deadline or an interrupt ends the read after it, the deadline leaves no time for another poll,
// or the 3 seconds kept end the read after it, and the read of the head after the checks still
// shows the pushed commit.
// A merge state still unknown after its 30 seconds, with time left before the deadline,
// is WaitPending too, with Waited.Cutoff CutoffWindow. A read that fails for any other reason is a
// *WaitError naming its stage.
//
// The read of the head and merge state made after the checks belongs to StageMergeState: its
// failure is a *WaitError at that stage. The deadline ending before it is made, inside the first
// read or a poll of the checks, inside a later read the 3 seconds kept did not end, or inside it,
// is WaitPending with no verdict, since the checks are read for the pull request's current head
// and only that read shows it is still the pushed commit: at StageVerdict when the checks were
// still running, and at StageMergeState when they had concluded. When the 3 seconds kept end a
// later read of the checks, the head is read in those 3 seconds: on the pushed commit, the wait
// ends with the verdict read before, WaitPending at StageVerdict, or WaitRed for a red verdict
// still waiting for its failed log; on another commit, WaitMoved; and a failure of that read is a
// *WaitError at StageMergeState.
//
// It tells deps' progress hook when it starts waiting for the checks, and again once it has waited
// for the merge state, and gives deps' logger a record of each read of the merge state and of the
// checks, and of how it ended.
func AwaitVerdict(ctx context.Context, deps Deps, pushed Pushed) (Waited, error) {
	w := waiter{deps: deps, pushed: pushed}

	waited, stage, err := w.run(ctx)
	if err == nil || waited.endPending(stage, err) {
		w.logEnd(ctx, waited)

		return waited, nil
	}

	deps.logWait(ctx, slog.String("ended", "failed"), slog.String("stage", string(stage)))

	return Waited{}, &WaitError{Stage: stage, Err: err}
}

// logEnd logs how the wait ended: its outcome, and the stage a pending wait ended in.
func (w waiter) logEnd(ctx context.Context, waited Waited) {
	attrs := []slog.Attr{slog.String("ended", string(waited.Outcome))}
	if waited.Outcome == WaitPending {
		attrs = append(attrs, slog.String("stage", string(waited.Stage)))
	}

	w.deps.logWait(ctx, attrs...)
}

func (w waiter) startChecks() {
	w.deps.started(result.Say("Waiting for checks on %s", result.PullRequest(w.pushed.Number)))
}

func (d Deps) logWait(ctx context.Context, attrs ...slog.Attr) {
	if d.Logger != nil {
		d.Logger.LogAttrs(ctx, slog.LevelDebug, "wait", attrs...)
	}
}

// noPoll is the window of a read of the merge state that no poll follows.
const noPoll time.Duration = -1

// logState logs a read of the merge state that showed head. window is the time left to poll an
// unknown state, or noPoll for a read no poll follows.
func (w waiter) logState(ctx context.Context, head forge.Head, window time.Duration) {
	attrs := []slog.Attr{slog.String("read", "merge-state"), slog.String("state", string(head.MergeState))}
	if window >= 0 {
		attrs = append(attrs, slog.Duration("window", window))
	}

	w.deps.logWait(ctx, attrs...)
}

// logChecks logs a read of the checks: how many deciding checks concluded, how many still run, and
// the verdict's state. It tells the progress hook the count.
func (w waiter) logChecks(ctx context.Context, verdict forge.Verdict) {
	deciding, pending := len(verdict.Deciding()), len(verdict.Pending())

	done := deciding - pending

	w.deps.counted(done, deciding)
	w.deps.logWait(ctx, slog.String("read", "checks"), slog.Int("done", done),
		slog.Int("pending", pending), slog.String("verdict", string(verdict.State())))
}

// endPending makes w WaitPending at stage when err is the deadline, an interrupt, or mergeWindow
// ending that stage, with Cutoff saying which did, and reports whether it was.
func (w *Waited) endPending(stage WaitStage, err error) bool {
	switch {
	case errors.Is(err, errWindowPassed):
		w.Cutoff = CutoffWindow
	case interrupted(err):
		w.Cutoff = CutoffInterrupt
	case cutOff(err):
		w.Cutoff = CutoffDeadline
	default:
		return false
	}

	w.Outcome, w.Stage = WaitPending, stage

	return true
}

// take records verdict and the outcome it was judged as, with its deciding checks and how many of
// them passed or were skipped.
func (w *Waited) take(verdict forge.Verdict, outcome WaitOutcome) {
	w.Outcome = outcome
	w.Verdict = verdict
	w.Deciding = verdict.Deciding()
	w.Passed, w.Skipped = tally(w.Deciding)
}

// waiter is one wait for the checks of a pull request's head: what it reads through and the
// commit it reads about.
type waiter struct {
	deps   Deps
	pushed Pushed
}

// run goes through the stages in order and returns what they found; when a stage fails or
// runs out of time, it returns that stage, the error, and what was found up to there, but for a
// verdict that no read of the head after the checks showed to be the pushed commit's.
func (w waiter) run(ctx context.Context) (Waited, WaitStage, error) {
	var waited Waited

	if ended, stage, err := w.readied(ctx, &waited); ended {
		return waited, stage, err
	}

	verdict, outcome, err := w.concluded(ctx, &waited)
	waited.take(verdict, outcome)

	stillRunning := errors.Is(err, errOutOfTime)

	// The checks are read for the pull request's current head, so only a read of the head after
	// them showing the pushed commit makes the verdict that commit's: without one, it may be a
	// newer commit's and is dropped. That read is made only when the checks concluded or ran out of
	// time with some left, which a later verdict read the reserve ended counts as: a verdict read
	// that failed, or the deadline ending a read or a poll of the checks, ends the wait before it.
	if err != nil && !stillRunning {
		return waited.unconfirmed(), StageVerdict, err
	}

	if ctxEnded(ctx) {
		lost := cmp.Or(ctx.Err(), errOutOfTime)

		return waited.unconfirmed(), lostStage(stillRunning, lost), lost
	}

	after, afterErr := w.head(ctx)
	if afterErr != nil {
		return waited.unconfirmed(), lostStage(stillRunning, afterErr), afterErr
	}

	w.logState(ctx, after, noPoll)

	if w.movedOn(&waited, after) {
		return waited, "", nil
	}

	waited.MergeState = after.MergeState

	// Green checks say nothing about whether the pull request can merge.
	if waited.Outcome == WaitGreen {
		waited.takeApart()
	}

	return waited, StageVerdict, err
}

// lostStage returns the stage a wait ends in when the read of the head after the checks was not
// made or failed with headErr: StageVerdict when the checks were still running, as stillRunning
// tells, and the deadline or an interrupt took the read, since the time limit is what left them
// unconcluded, and the read's own StageMergeState otherwise.
func lostStage(stillRunning bool, headErr error) WaitStage {
	if stillRunning && cutOff(headErr) {
		return StageVerdict
	}

	return StageMergeState
}

// readied runs the stages before the checks: until the pushed commit is the head, then until its
// merge state is known. It reports whether they ended the wait, by a stage that failed or ran out of
// time, which it returns with its error, by a head on another commit, or by a pull request apart
// from its trunk. Once it waited for the merge state, it tells the progress hook the wait for the
// checks goes on.
func (w waiter) readied(ctx context.Context, waited *Waited) (ended bool, stage WaitStage, err error) {
	w.startChecks()

	head, err := w.caughtUp(ctx)
	waited.MergeState = head.MergeState

	if err != nil {
		return true, StageHead, err
	}

	w.logState(ctx, head, mergeWindow)

	moved, polled, err := w.settled(ctx, waited, head)
	if moved {
		return true, "", nil
	}

	if err != nil {
		return true, StageMergeState, err
	}

	// A conflicting pull request runs no workflow, so its missing checks say nothing.
	if waited.takeApart() {
		return true, "", nil
	}

	if polled {
		w.startChecks()
	}

	return false, "", nil
}

// settled polls the merge state of head, read once the pushed commit was the head, until it is
// known, and has waited take it. It reports whether the poll read a head on another commit, which
// leaves waited WaitMoved instead, whether the state was still unknown so that it polled, and the
// poll's error, which ended it with the last state it read taken.
func (w waiter) settled(ctx context.Context, waited *Waited, head forge.Head) (moved, polled bool, err error) {
	polled = w.unknownOnPushed(head)

	head, err = w.mergeKnown(ctx, head)
	if err == nil && w.movedOn(waited, head) {
		return true, polled, nil
	}

	waited.MergeState = head.MergeState

	return false, polled, err
}

// movedOn reports whether head, read once the pushed commit was the head, is on another commit
// than the pushed one, and makes waited WaitMoved to that commit when it is. Only the merge state
// waited already holds is kept: the verdict and the merge state head carries may be the other
// commit's.
func (w waiter) movedOn(waited *Waited, head forge.Head) bool {
	if head.SHA == w.pushed.SHA {
		return false
	}

	moved := waited.unconfirmed()
	moved.Outcome, moved.MovedTo = WaitMoved, head.SHA
	*waited = moved

	return true
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

// mergeKnown polls the head, starting from the one just read, until its merge state is known,
// the head is on another commit than the pushed one, or mergeWindow has passed, and returns the
// last head it read. A head on another commit ends the poll: the state it carries is that commit's.
//
// It tells the progress hook when it starts polling, and logs each read it makes.
func (w waiter) mergeKnown(ctx context.Context, head forge.Head) (forge.Head, error) {
	began := time.Now()

	if w.unknownOnPushed(head) {
		w.deps.started(result.Say("Waiting up to %s for GitHub to say whether %s can merge", span(mergeWindow),
			result.PullRequest(w.pushed.Number)))
	}

	for w.unknownOnPushed(head) {
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
		w.logState(ctx, head, mergeWindow-time.Since(began))
	}

	return head, nil
}

// unknownOnPushed reports whether head is the pushed commit with its merge state still unknown.
func (w waiter) unknownOnPushed(head forge.Head) bool {
	return head.MergeState == forge.MergeUnknown && head.SHA == w.pushed.SHA
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
		verdict, err := w.readVerdict(ctx, query, !waited.Since.IsZero())
		if err != nil {
			outcome, err = cutShort(outcome, err)

			return last, outcome, err
		}

		last = verdict
		waited.Since = began
		w.logChecks(ctx, verdict)

		outcome, done = judge(verdict, w.pushed.Required)
		// A failed check stays failed whatever runs start later.
		if done && outcome != WaitRed && runsMayStillAppear(verdict, began) {
			done = false
		}

		if done {
			return last, outcome, nil
		}

		if err = pollVerdict(ctx); err != nil {
			outcome, err = cutShort(outcome, err)

			return last, outcome, err
		}
	}
}

// readVerdict reads the verdict under ctx. When later is true (a read after the first) and ctx
// has a deadline, it reads under a deadline headReadReserve before ctx's, so that the read of the
// head after the checks keeps its time whatever the read costs. It returns an error wrapping
// errOutOfTime when that deadline, and not ctx ending, ended the read.
func (w waiter) readVerdict(ctx context.Context, query forge.VerdictQuery, later bool) (forge.Verdict, error) {
	if !later {
		return w.deps.Forge.Verdict(ctx, w.pushed.Repo, query)
	}

	readCtx, cancel := beforeDeadline(ctx, headReadReserve)
	defer cancel()

	verdict, err := w.deps.Forge.Verdict(readCtx, w.pushed.Repo, query)
	if err != nil && readCtx.Err() != nil && ctx.Err() == nil {
		return verdict, fmt.Errorf("%w: %w", errOutOfTime, err)
	}

	return verdict, err
}

// cutShort returns how a wait for the verdict that err ended concluded: a red verdict still
// waiting for its failed log stays red when the deadline or an interrupt ends the wait, since the
// log is all it lacked, and anything else is err with no outcome.
func cutShort(outcome WaitOutcome, err error) (WaitOutcome, error) {
	if outcome == WaitRed && cutOff(err) {
		return WaitRed, nil
	}

	return "", err
}

// judge returns how the checks of verdict concluded, and false while the wait for them goes on.
// required names the checks the trunk's protection requires; when empty, every check decides.
func judge(verdict forge.Verdict, required []string) (WaitOutcome, bool) {
	state := verdict.State()
	missing := len(verdict.MissingRequired) > 0

	switch {
	// The forge may have no failed log yet, while the run goes on or just after it completes,
	// and the excerpt is what the fix starts from, so a red verdict waits for it like a
	// pending one would.
	case state == forge.VerdictRed && verdict.FailedLog.State == forge.FailedLogInProgress:
		return WaitRed, false
	case state == forge.VerdictRed:
		return WaitRed, true
	case state == forge.VerdictPending:
		return "", false
	// Where every check decides, a run still going can create a check that is not there yet;
	// where required names decide, it matters only while one of them is absent.
	case verdict.Running() && (missing || len(required) == 0):
		return "", false
	case missing && !silent(verdict):
		return WaitNeverReported, true
	case missing || state == forge.VerdictNone:
		return WaitNothingReported, true
	default:
		return WaitGreen, true
	}
}

// runsMayStillAppear reports whether verdict, read in a wait for the checks that began at began,
// holds no workflow run while one may still appear before the next read. Runs appear some seconds
// after a push, so a verdict without them is only believed once that has lasted.
func runsMayStillAppear(verdict forge.Verdict, began time.Time) bool {
	return len(verdict.Runs) == 0 && time.Since(began)+verdictPoll <= silentRetry
}

// beforeDeadline returns a context that ends reserve before ctx's deadline, or with ctx when it has
// none.
func beforeDeadline(ctx context.Context, reserve time.Duration) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}

	return context.WithDeadline(ctx, deadline.Add(-reserve))
}

// ctxEnded reports whether ctx has ended or its deadline is no longer ahead of the current time.
func ctxEnded(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}

	deadline, ok := ctx.Deadline()

	return ok && time.Until(deadline) <= 0
}

// deadlineWithin reports whether ctx has a deadline less than d away. A ctx without a deadline
// is never within.
func deadlineWithin(ctx context.Context, d time.Duration) bool {
	deadline, ok := ctx.Deadline()

	return ok && time.Until(deadline) < d
}

// pause waits until d has passed, or returns ctx's error as soon as ctx ends, an interrupt found
// once d has passed included. It returns errOutOfTime, without waiting, when the wait would end
// past ctx's deadline, and after waiting when it ended on the deadline, which leaves no time for
// a read. The polls of the head
// and of the merge state call it as is; the verdict poll goes through pollVerdict, which keeps
// headReadReserve on top, as readVerdict does for every verdict read after the first.
func pause(ctx context.Context, d time.Duration) error {
	if deadlineWithin(ctx, d) {
		return errOutOfTime
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait %s: %w", d, ctx.Err())
	case <-timer.C:
	}

	// An interrupt that lands as the timer fires is still an interrupt: only the deadline makes
	// the wait out of time.
	if err := ctx.Err(); interrupted(err) {
		return fmt.Errorf("wait %s: %w", d, err)
	}

	if ctxEnded(ctx) {
		return errOutOfTime
	}

	return nil
}

// pollVerdict waits verdictPoll before the next read of the verdict, as pause does, but returns
// errOutOfTime without waiting when the poll would leave less than headReadReserve before ctx's
// deadline, so that the read of the head after the checks is still made. It is one of the two
// places that keep the reserve; readVerdict, which bounds every verdict read after the first, is
// the other.
func pollVerdict(ctx context.Context) error {
	if deadlineWithin(ctx, verdictPoll+headReadReserve) {
		return errOutOfTime
	}

	return pause(ctx, verdictPoll)
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
			// A failed, pending, or canceled check counts toward neither.
		}
	}

	return passed, skipped
}

// trunkWithoutCI reports whether ready's trunk has no CI, asked of the forge only when nothing
// reported for the commit the wait read and the trunk requires none of its checks: a trunk that
// requires checks has CI. It returns false when the question does not arise. A ctx that has
// already ended is returned as the read's error, and so is any failure of the read.
func (w Waited) trunkWithoutCI(ctx context.Context, deps Deps, ready Ready, required []string) (bool, error) {
	if w.Outcome != WaitNothingReported || len(required) > 0 {
		return false, nil
	}

	if err := ctx.Err(); err != nil {
		return false, err
	}

	hasCI, err := deps.Forge.HasTrunkCI(ctx, ready.Repo, ready.Trunk)
	if err != nil {
		return false, err
	}

	return !hasCI, nil
}
