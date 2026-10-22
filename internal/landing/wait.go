package landing

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
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
	// WaitPending is a wait that ended before the forge had an answer: the deadline ended it,
	// or the stage's own window did when Waited.Window is set. Waited.Stage says what it was
	// waiting for.
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

// unconfirmed returns a Waited holding only the merge state w read before the checks, as the wait
// ends when no read of the head after the checks showed the pushed commit.
func (w Waited) unconfirmed() Waited {
	return Waited{MergeState: w.MergeState, RawMergeState: w.RawMergeState}
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
// a reason other than the context's deadline.
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

// endedByDeadline reports whether err says the deadline ended a read or left no time for one.
func endedByDeadline(err error) bool {
	return errors.Is(err, errOutOfTime) || errors.Is(err, context.DeadlineExceeded)
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
// No poll is started that would end past ctx's deadline.
// A deadline that ends a stage, inside a read or a poll or between two, is the outcome
// WaitPending with that stage and no error, but for a red verdict still waiting for its failed
// log when the deadline leaves no time for another poll and the read of the head after the
// checks still shows the pushed commit, which stays WaitRed. A merge state
// still unknown after its 30 seconds, with time left before the deadline, is WaitPending too,
// with Waited.Window set. A read that fails for any other reason is a *WaitError naming its
// stage. The read of the merge state made after the checks belongs to StageMergeState: its
// failure is a *WaitError, and the deadline ending before it is made or inside it is
// WaitPending at that stage with no verdict, whatever the checks said, since the checks are
// read for the pull request's current head and only that read shows it is still the pushed
// commit.
func AwaitVerdict(ctx context.Context, deps Deps, pushed Pushed) (Waited, error) {
	w := waiter{deps: deps, pushed: pushed}

	waited, stage, err := w.run(ctx)
	if err == nil || waited.endPending(stage, err) {
		return waited, nil
	}

	return Waited{}, &WaitError{Stage: stage, Err: err}
}

// endPending makes w WaitPending at stage when err is the deadline or mergeWindow ending that
// stage, with Window set when mergeWindow did, and reports whether it was.
func (w *Waited) endPending(stage WaitStage, err error) bool {
	windowPassed := errors.Is(err, errWindowPassed)
	if !windowPassed && !endedByDeadline(err) {
		return false
	}

	w.Outcome, w.Stage = WaitPending, stage

	if windowPassed {
		w.Window = mergeWindow
	}

	return true
}

// waiter is one wait for the checks of a pull request's head: what it reads through and the
// commit it reads about.
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
	if err == nil && w.movedOn(&waited, head) {
		return waited, "", nil
	}

	waited.takeState(head)

	if err != nil {
		return waited, StageMergeState, err
	}

	// A conflicting pull request runs no workflow, so its missing checks say nothing.
	if waited.takeApart() {
		return waited, "", nil
	}

	verdict, outcome, err := w.concluded(ctx, &waited)
	waited.Outcome = outcome
	waited.Verdict = verdict
	waited.Deciding = verdict.Deciding()
	waited.Passed, waited.Skipped = tally(waited.Deciding)

	// The merge state is read again only when the checks concluded or ran out of time with
	// some left: a verdict read that failed is the failure to report.
	if err != nil && !errors.Is(err, errOutOfTime) {
		return waited, StageVerdict, err
	}

	// The checks are read for the pull request's current head, so only a read of the head after
	// them showing the pushed commit makes the verdict that commit's: without one, it may be a
	// newer commit's and is dropped.
	if ctxEnded(ctx) {
		return waited.unconfirmed(), StageMergeState, cmp.Or(ctx.Err(), errOutOfTime)
	}

	after, afterErr := w.head(ctx)
	if afterErr != nil {
		return waited.unconfirmed(), StageMergeState, afterErr
	}

	if w.movedOn(&waited, after) {
		return waited, "", nil
	}

	waited.takeState(after)

	// Green checks say nothing about whether the pull request can merge.
	if waited.Outcome == WaitGreen {
		waited.takeApart()
	}

	return waited, StageVerdict, err
}

// movedOn reports whether head, read once the pushed commit was the head, is on another commit
// than the pushed one, and makes waited WaitMoved to that commit when it is. Only the merge state
// waited already holds is kept: the verdict and the merge state head carries may be the other
// commit's.
func (w waiter) movedOn(waited *Waited, head forge.Head) bool {
	if head.SHA == w.pushed.SHA {
		return false
	}

	*waited = Waited{
		Outcome: WaitMoved, MovedTo: head.SHA, MergeState: waited.MergeState, RawMergeState: waited.RawMergeState,
	}

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
func (w waiter) mergeKnown(ctx context.Context, head forge.Head) (forge.Head, error) {
	began := time.Now()

	for head.MergeState == forge.MergeUnknown && head.SHA == w.pushed.SHA {
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
		// Runs appear some seconds after a push, so a verdict without them is only believed once
		// that has lasted. A failed check stays failed whatever runs start later.
		if done && outcome != WaitRed && len(verdict.Runs) == 0 && time.Since(began)+verdictPoll <= silentRetry {
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
	if outcome == WaitRed && endedByDeadline(err) {
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

// tally counts the checks in the pass bucket and the checks in the skipped bucket.
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

// The reads of the wait for the checks of a pull request's head, as a result names them.
const (
	readHead       = "the pull request's head"
	readMergeState = "whether the pull request can merge"
	readChecks     = "the checks of the pushed commit"
	readCI         = "whether the repository has CI"
)

// excerptLines is how many lines of a failing check's log a red verdict's excerpt holds, counted from the
// log's end: the excerpt is bounded so that the failure stays readable in a terminal. The push
// payload carries the whole log; the merge payload carries none of it, so a merge result has only
// the excerpt.
const excerptLines = 60

// waitRead returns the read a failed wait names and the error of that read.
func waitRead(err error) (string, error) {
	failed, ok := errors.AsType[*WaitError](err)
	if !ok {
		return readChecks, err
	}

	switch failed.Stage {
	case StageHead:
		return readHead, failed.Err
	case StageMergeState:
		return readMergeState, failed.Err
	default:
		return readChecks, failed.Err
	}
}

// cutShortReading returns the line and the reason that say the time limit ended while reading
// read.
func cutShortReading(read string) (line, reason result.Phrase) {
	return result.Say("The time limit ended while reading %s; nothing more will change", read),
		result.Say("the time limit ended while reading %s", read)
}

// waitReport is what a result says about a wait whose outcome stops the command: its lines, the
// failed log's excerpt, its reason, and its next step.
type waitReport struct {
	excerpt string
	lines   []result.Phrase
	reason  result.Phrase
	next    result.Next
	// pending is whether the wait ended before the forge had an answer; otherwise it ended on an
	// answer that is not green.
	pending bool
	// refused is whether the answer is about no commit the command can report on, so that the
	// command is refused whatever it ends a stop as.
	refused bool
}

// outcome returns result.Pending for a pending report, result.Refused for a refused one, and
// stopped for any other.
func (r waitReport) outcome(stopped result.Outcome) result.Outcome {
	switch {
	case r.pending:
		return result.Pending
	case r.refused:
		return result.Refused
	default:
		return stopped
	}
}

// stopReport returns the report of a wait that ended red, apart from the trunk, with a required
// check that never reported, pending, or on a head that moved, about the commit sha of pull request
// number, whose trunk is trunk, for the command named command. It returns false for green checks
// and for nothing reported, which each command maps on its own.
func (w Waited) stopReport(command, sha, trunk string, number int) (waitReport, bool) {
	switch w.Outcome {
	case WaitGreen, WaitNothingReported:
		return waitReport{}, false
	case WaitRed:
		return w.redReport(), true
	case WaitConflicting:
		return w.apartReport(sha, trunk, result.Say("the pull request conflicts with the trunk")), true
	case WaitBehind:
		return w.apartReport(sha, trunk, result.Say("the pull request is behind the trunk")), true
	case WaitNeverReported:
		return waitReport{
			lines: []result.Phrase{
				result.Say("Required and never reported: %s", strings.Join(w.Verdict.MissingRequired, ", ")),
				w.mergeStateLine(),
			},
			reason: result.Say("a required check never reported"),
			next:   result.NextStep("report that a required check never reported, and stop"),
		}, true
	case WaitMoved:
		return waitReport{
			reason:  result.Say("the head of pull request #%s moved to %s", number, w.MovedTo),
			next:    result.NextStep("pull the new commit"+thenRunAgain, command),
			refused: true,
		}, true
	default:
		return w.pendingReport(sha, number), true
	}
}

// redReport is a deciding check that failed or was canceled, with the end of its log as the
// excerpt.
func (w Waited) redReport() waitReport {
	failed, _ := w.Verdict.Failed()

	line := result.Say("Check %s failed: %s", failed.Name, failed.Link)
	reason := result.Say("the check %s failed", failed.Name)

	if failed.Canceled {
		line = result.Say("Check %s was canceled: %s", failed.Name, failed.Link)
		reason = result.Say("the check %s was canceled", failed.Name)
	}

	return waitReport{
		lines:   []result.Phrase{line, w.mergeStateLine()},
		excerpt: w.excerpt(),
		reason:  reason,
		next:    result.NextStep("fix the failing check, commit, and run ils push again"),
	}
}

// apartReport is a pull request that cannot merge until the trunk is merged into the branch. The
// checks are said only when some decided: they were green, and the merge state read after them
// found it apart.
func (w Waited) apartReport(sha, trunk string, reason result.Phrase) waitReport {
	var lines []result.Phrase
	if len(w.Deciding) > 0 {
		lines = append(lines, w.checksLine(sha))
	}

	return waitReport{
		lines:  append(lines, w.mergeStateLine()),
		reason: reason,
		next: result.NextStep("merge the trunk into the branch with `git merge origin/%s`, resolve the conflicts, "+
			"commit, and run ils push again", trunk),
	}
}

// pendingReport is a wait that ended before the forge had an answer about the commit sha of pull
// request number, at the stage it was in.
func (w Waited) pendingReport(sha string, number int) waitReport {
	report := waitReport{pending: true, next: result.NextCommand(PushCommand)}

	switch w.Stage {
	case StageHead:
		report.lines = []result.Phrase{result.Say("GitHub does not show %s on pull request #%s yet", sha, number)}
		report.reason = result.Say("GitHub did not show the pushed commit on the pull request before the time limit")
	case StageMergeState:
		report.lines = []result.Phrase{result.Say("GitHub has not said whether pull request #%s can merge yet", number)}
		report.reason = result.Say("GitHub did not say whether the pull request can merge before the time limit")

		// The stage's own window ended the wait: the time limit was not reached.
		if w.Window > 0 {
			report.reason = result.Say("GitHub did not say whether the pull request can merge within %s seconds",
				int(w.Window/time.Second))
		}
	default:
		report.lines, report.reason = w.unconcluded(sha, number)
	}

	return report
}

// unconcluded returns what the checks stage had found when the deadline ended it, then the merge
// state, and the reason: no verdict read at all, nothing reported, a workflow run with no check
// yet, checks concluded before any workflow run started, or the checks still running.
func (w Waited) unconcluded(sha string, number int) ([]result.Phrase, result.Phrase) {
	var lines []result.Phrase

	reason := result.Say("the checks were still running at the time limit")

	switch {
	case w.Since.IsZero():
		var line result.Phrase

		line, reason = cutShortReading(readChecks)
		lines = append(lines, line)
	case silent(w.Verdict):
		lines = append(lines, result.Say("No check or status has reported for %s on pull request #%s yet", sha, number))
		reason = result.Say("no check or status had reported at the time limit")
	case len(w.Verdict.Checks) == 0 && w.Verdict.Running():
		lines = append(lines, result.Say(
			"No check has reported for %s on pull request #%s yet; a workflow run is still going", sha, number))
		reason = result.Say("the checks had not reported at the time limit")
	// The checks concluded, but the deadline ended the wait for workflow runs to appear.
	case len(w.Verdict.Runs) == 0 && len(w.Verdict.Pending()) == 0:
		lines = append(lines, result.Say("No workflow run has started for %s on pull request #%s yet", sha, number))
		reason = result.Say("no workflow run had started at the time limit")
	default:
		seconds := int(time.Since(w.Since) / time.Second)
		for _, check := range w.Verdict.Pending() {
			lines = append(lines, result.Say("Still running after %s seconds: %s", seconds, check.Name))
		}
	}

	return append(lines, w.mergeStateLine()), reason
}

// ciCutShortReport is a read of whether the repository has CI that the deadline cut short.
func (w Waited) ciCutShortReport() waitReport {
	line, reason := cutShortReading(readCI)

	return waitReport{
		lines: []result.Phrase{line, w.mergeStateLine()}, reason: reason, next: result.NextCommand(PushCommand), pending: true,
	}
}

// nothingReported returns the line and the reason that say no check or status reported for the
// commit sha of pull request number.
func nothingReported(sha string, number int) (line, reason result.Phrase) {
	return result.Say("No check or status reported for %s on pull request #%s", sha, number),
		result.Say("no check or status reported for %s on pull request #%s", sha, number)
}

// noCI says that the trunk has no CI, so that nothing verified the commit sha.
func noCI(trunk, sha string) result.Phrase {
	return result.Say("No CI is configured on %s, so nothing verified %s", trunk, sha)
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

// payloadFields returns the verdict state, the merge state and the deciding checks as the push
// and merge payloads carry them; the merge state is unknown until the wait has read one.
func (w Waited) payloadFields() (verdict, mergeState string, checks []result.Check) {
	state := w.MergeState
	if state == "" {
		state = forge.MergeUnknown
	}

	checks = make([]result.Check, 0, len(w.Deciding))
	for _, check := range w.Deciding {
		checks = append(checks, result.Check{
			Name: check.Name, Bucket: string(check.Bucket), Workflow: check.Workflow, Link: check.Link,
		})
	}

	return string(w.Verdict.State()), string(state), checks
}

// mergeStateLine says the merge state as GitHub spells it, with the word ils has for it.
func (w Waited) mergeStateLine() result.Phrase {
	return result.Say("GitHub: %s (%s)", w.RawMergeState, w.MergeState)
}

// checksLine says how many deciding checks of the commit sha passed and how many were skipped.
func (w Waited) checksLine(sha string) result.Phrase {
	return result.Say("Checks for %s: %s passed, %s skipped", sha, w.Passed, w.Skipped)
}

// excerpt returns the last excerptLines lines of the failed log of the check that failed, or
// one line saying why there is no log.
func (w Waited) excerpt() string {
	failed, _ := w.Verdict.Failed()
	check, log := failed.Name, w.Verdict.FailedLog

	switch log.State {
	case forge.FailedLogRead:
		lines := strings.Split(strings.TrimRight(log.Text, "\n"), "\n")

		return strings.Join(lines[max(0, len(lines)-excerptLines):], "\n")
	case forge.FailedLogNoRun:
		return "no log: " + check + " is not a workflow run"
	case forge.FailedLogInProgress:
		return "no log: the log of " + check + " was not available yet; its workflow run had not finished"
	default:
		return "no log: the log of " + check + " could not be read"
	}
}
