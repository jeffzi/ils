package landing_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
)

const (
	pushedSHA = "2222222"
	staleSHA  = "1111111"
	// movedSHA is a commit pushed to the pull request's branch after the pushed one.
	movedSHA = "3333333"

	// bound is the time a wait has left unless its test says otherwise.
	bound = 100 * time.Second

	headPoll    = 2 * time.Second
	mergePoll   = 2 * time.Second
	verdictPoll = 5 * time.Second

	// headReadReserve is the time a wait keeps before its deadline for the head read after the checks.
	headReadReserve = 3 * time.Second

	// mergeWindow must equal the unexported window in wait.go: a wait that window ends reports it
	// as Window.
	mergeWindow = 30 * time.Second

	// midPoll is how far into its first poll a wait is when its test ends the context: every
	// poll lasts longer.
	midPoll = time.Second

	// heldRead is how far into a read held until its context ends the reserve ends it, when the
	// read follows the first poll of the checks and a wait has heldLeft at that poll.
	heldRead = 500 * time.Millisecond

	// heldLeft is the time a wait has left when its first poll of the checks is reached: one poll
	// fits with the 3 seconds kept for the head read after it, so the read after the poll begins
	// 3.5 seconds before the deadline, and the reserve ends it heldRead in.
	heldLeft = verdictPoll + headReadReserve + heldRead
)

// heldPauses are the fake times a wait lets pass when the reserve ends the read after its first
// poll of the checks: the poll, then the read.
func heldPauses() []time.Duration { return []time.Duration{verdictPoll, heldRead} }

// ignoreSince leaves out the time the verdict wait began.
func ignoreSince() cmp.Option {
	return cmpopts.IgnoreFields(landing.Waited{}, "Since")
}

// orBound returns left, or the whole bound when left is zero.
func orBound(left time.Duration) time.Duration {
	if left == 0 {
		return bound
	}

	return left
}

// readLog keeps the fake time of each forge read.
type readLog struct {
	at []time.Time
}

// timed returns script with a hook that keeps the time of each read, and then runs the hook
// script came with.
func (l *readLog) timed(script forgetest.Script) forgetest.Script {
	then := script.OnRead
	script.OnRead = func(call forgetest.Call) {
		l.at = append(l.at, time.Now())

		if then != nil {
			then(call)
		}
	}

	return script
}

// pauses returns the times that passed between one read and the next, and between the last
// read and end, in order and but for those of no length.
func (l *readLog) pauses(end time.Time) []time.Duration {
	var pauses []time.Duration

	for i, at := range l.at {
		next := end
		if i+1 < len(l.at) {
			next = l.at[i+1]
		}

		if pause := next.Sub(at); pause != 0 {
			pauses = append(pauses, pause)
		}
	}

	return pauses
}

// vigil is one wait inside a bubble: the forge it reads, the times of its reads, and the deadline
// it runs under.
type vigil struct {
	ctx   context.Context
	forge *forgetest.Fake
	log   *readLog
	// deadline is the fake time the context ends at; zero when it has no deadline.
	deadline time.Time
	// ended is the fake time at which the wait returned.
	ended time.Time
	deps  landing.Deps
}

// newVigilOn returns a wait under ctx that reads a forge answering from script.
func newVigilOn(ctx context.Context, t *testing.T, script forgetest.Script) *vigil {
	t.Helper()

	log := &readLog{}
	fake := forgetest.New(t, log.timed(script))
	deadline, _ := ctx.Deadline()

	return &vigil{ctx: ctx, forge: fake, log: log, deadline: deadline, deps: landing.Deps{Forge: fake}}
}

// newVigil is newVigilOn with a context whose deadline is left of fake time away.
func newVigil(t *testing.T, script forgetest.Script, left time.Duration) *vigil {
	t.Helper()

	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(left))
	t.Cleanup(cancel)

	return newVigilOn(ctx, t, script)
}

// pauses returns the fake times the wait let pass between its forge reads and after the last, in order.
func (v *vigil) pauses() []time.Duration {
	return v.log.pauses(v.ended)
}

// pushedTip is the pushed commit of pull request 12, with the checks the trunk requires.
func pushedTip(required ...string) landing.Pushed {
	return landing.Pushed{Repo: widgets(false, true), SHA: pushedSHA, Number: opened, Required: required}
}

// tip returns the pull request's head on the pushed commit in the merge state state.
func tip(state forge.MergeState, raw string) forge.Head {
	return forge.Head{SHA: pushedSHA, MergeState: state, RawMergeState: raw}
}

// clean, unknown, and stale are the heads most waits read: the pushed commit ready to merge, the
// pushed commit before the forge worked its merge state out, and the commit before the push.
func clean() forge.Head   { return tip(forge.MergeClean, "CLEAN") }
func unknown() forge.Head { return tip(forge.MergeUnknown, "UNKNOWN") }
func stale() forge.Head {
	return forge.Head{SHA: staleSHA, MergeState: forge.MergeClean, RawMergeState: "CLEAN"}
}

// moved is the head of a pull request whose branch a commit after the pushed one reached, in a
// merge state of its own, unlike the pushed commit's.
func moved() forge.Head {
	return forge.Head{SHA: movedSHA, MergeState: forge.MergeBlockedByReview, RawMergeState: "BLOCKED"}
}

func blockedByReview() forge.Head { return tip(forge.MergeBlockedByReview, "BLOCKED") }

// lateHead is the answer of a read of the head that the deadline ended.
func lateHead() forgetest.Answer[forge.Head] {
	return forgetest.Answer[forge.Head]{Err: fmt.Errorf("gh: %w", context.DeadlineExceeded)}
}

// inTurn returns a sequence that answers with each of values in turn.
func inTurn[T any](values ...T) []forgetest.Answer[T] {
	answers := make([]forgetest.Answer[T], 0, len(values))
	for _, value := range values {
		answers = append(answers, forgetest.Answer[T]{Value: value})
	}

	return answers
}

// shows returns a head sequence that answers with each of heads in turn.
func shows(heads ...forge.Head) []forgetest.Answer[forge.Head] { return inTurn(heads...) }

// says returns a verdict sequence that answers with each of verdicts in turn.
func says(verdicts ...forge.Verdict) []forgetest.Answer[forge.Verdict] { return inTurn(verdicts...) }

// heldAfter returns a verdict sequence that answers with each of verdicts in turn, then holds
// every later read until its context ends.
func heldAfter(verdicts ...forge.Verdict) []forgetest.Answer[forge.Verdict] {
	return append(says(verdicts...), forgetest.Answer[forge.Verdict]{Hold: true})
}

// checked returns a check of the ci workflow that the trunk does not require.
func checked(name string, bucket forge.Bucket) forge.Check {
	return forge.Check{Name: name, Bucket: bucket, Workflow: "ci"}
}

// needed returns a check of the ci workflow that the trunk requires.
func needed(name string, bucket forge.Bucket) forge.Check {
	return forge.Check{Name: name, Bucket: bucket, Workflow: "ci", Required: true}
}

// ran returns the ci workflow's run in status.
func ran(status forge.RunStatus) []forge.Run {
	return []forge.Run{{Name: "ci", Status: status}}
}

// passing is a verdict whose one check passed and whose run completed.
func passing() forge.Verdict {
	return forge.Verdict{Checks: []forge.Check{checked("test", forge.BucketPass)}, Runs: ran(forge.RunCompleted)}
}

// appPassing is a verdict whose one check, of an app, passed: no workflow run stands behind it.
func appPassing() forge.Verdict {
	return forge.Verdict{Checks: []forge.Check{{Name: "scan", Bucket: forge.BucketPass}}}
}

// running is a verdict whose one check still runs.
func running() forge.Verdict {
	return forge.Verdict{Checks: []forge.Check{checked("test", forge.BucketPending)}, Runs: ran(forge.RunInProgress)}
}

// requiredPassing is a verdict whose required checks lint and test passed and whose run completed.
func requiredPassing() forge.Verdict {
	return forge.Verdict{
		Checks: []forge.Check{needed("lint", forge.BucketPass), needed("test", forge.BucketPass)},
		Runs:   ran(forge.RunCompleted),
	}
}

// readsOf returns the forge reads of a wait: heads head reads, then verdicts verdict reads, then
// after head reads.
func readsOf(heads, verdicts, after int) []forgetest.Op {
	return slices.Concat(
		slices.Repeat([]forgetest.Op{forgetest.OpHead}, heads),
		slices.Repeat([]forgetest.Op{forgetest.OpVerdict}, verdicts),
		slices.Repeat([]forgetest.Op{forgetest.OpHead}, after),
	)
}

// polls returns times pauses of every.
func polls(times int, every time.Duration) []time.Duration {
	return slices.Repeat([]time.Duration{every}, times)
}

// concluded returns how a wait on a clean pull request ends once verdict was read: as outcome,
// with every check deciding, and passed of them passed and skipped of them skipped.
func concluded(outcome landing.WaitOutcome, verdict forge.Verdict, passed, skipped int) landing.Waited {
	return landing.Waited{
		Outcome:       outcome,
		MergeState:    forge.MergeClean,
		RawMergeState: "CLEAN",
		Verdict:       verdict,
		Deciding:      verdict.Checks,
		Passed:        passed,
		Skipped:       skipped,
	}
}

// green is how a wait ends when passing was read.
func green() landing.Waited { return concluded(landing.WaitGreen, passing(), 1, 0) }

// stateOf returns waited with the merge state of head.
func stateOf(waited landing.Waited, head forge.Head) landing.Waited {
	waited.MergeState = head.MergeState
	waited.RawMergeState = head.RawMergeState

	return waited
}

// pendingAt returns how a wait ends pending at stage, with the merge state of head.
func pendingAt(stage landing.WaitStage, head forge.Head) landing.Waited {
	return stateOf(landing.Waited{Outcome: landing.WaitPending, Stage: stage}, head)
}

// await runs AwaitVerdict on the pushed commit of pull request 12, whose trunk requires the checks
// required, keeps the fake time it returned at, and fails the test when it returns an error.
func (v *vigil) await(t *testing.T, required ...string) landing.Waited {
	t.Helper()

	got, err := landing.AwaitVerdict(v.ctx, v.deps, pushedTip(required...))
	v.ended = time.Now()

	assert.NilError(t, err)

	return got
}

// waitError returns err as the *landing.WaitError it must be, and fails the test when it is not.
func waitError(t *testing.T, err error) *landing.WaitError {
	t.Helper()

	got, ok := errors.AsType[*landing.WaitError](err)
	assert.Assert(t, ok, "AwaitVerdict() error = %v, want a *landing.WaitError", err)

	return got
}

// opsOf returns the operations the vigil's forge was asked for, in order.
func (v *vigil) opsOf() []forgetest.Op {
	var ops []forgetest.Op
	for _, call := range v.forge.Calls() {
		ops = append(ops, call.Op)
	}

	return ops
}

// vigilRow is one wait: what the forge answers, and how the wait must end.
type vigilRow struct {
	name     string
	heads    []forgetest.Answer[forge.Head]
	verdicts []forgetest.Answer[forge.Verdict]
	// required names the checks that decide; when empty, every check decides.
	required []string
	// ops are the forge reads the wait makes, in order.
	ops []forgetest.Op
	// waits are the fake times the wait lets pass between its forge reads, in order.
	waits []time.Duration
	// want is what the wait returns, but for the time its verdict wait began.
	want landing.Waited
	// left is the time before the deadline; zero is the whole bound.
	left time.Duration
}

// runVigils runs each row as a test in a bubble of its own: the wait ends as the row wants, having
// read the forge and let fake time pass between its reads exactly as the row says.
func runVigils(t *testing.T, rows []vigilRow) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				v := newVigil(t, forgetest.Script{Head: tt.heads, Verdict: tt.verdicts}, orBound(tt.left))

				got := v.await(t, tt.required...)

				assert.Check(t, is.DeepEqual(got, tt.want, ignoreSince(), cmpopts.EquateEmpty()))
				assert.Check(t, is.DeepEqual(v.opsOf(), tt.ops, cmpopts.EquateEmpty()))
				assert.Check(t, is.DeepEqual(v.pauses(), tt.waits, cmpopts.EquateEmpty()))
			})
		})
	}
}

func TestAwaitVerdict_WhenTheHeadIsNotYetThePushedCommit_ReadsNoCheck(t *testing.T) {
	t.Parallel()

	runVigils(t, []vigilRow{
		{
			name:  "pushed commit on the first read",
			heads: shows(clean()), verdicts: says(passing()),
			want: green(), ops: readsOf(1, 1, 1),
		},
		{
			name:  "previous commit for two reads, then the pushed one",
			heads: shows(stale(), stale(), clean()), verdicts: says(passing()),
			want: green(), ops: readsOf(3, 1, 1), waits: polls(2, headPoll),
		},
		{
			// A third poll would end a second past the deadline.
			name:  "previous commit until the deadline",
			heads: shows(stale()), left: 5 * time.Second,
			want: pendingAt(landing.StageHead, stale()),
			ops:  readsOf(3, 0, 0), waits: polls(2, headPoll),
		},
	})
}

// mergeRows are the waits decided by the merge state the head read carries.
func mergeRows() []vigilRow {
	conflicting := tip(forge.MergeConflicting, "DIRTY")
	behind := tip(forge.MergeBehind, "BEHIND")
	stillUnknown := pendingAt(landing.StageMergeState, unknown())
	// gaveUp is the wait that its own window ended, with time left before the deadline.
	gaveUp := stillUnknown
	gaveUp.Window = mergeWindow

	return []vigilRow{
		{
			name:  "unknown then clean",
			heads: shows(unknown(), clean()), verdicts: says(passing()),
			want: green(), ops: readsOf(2, 1, 1), waits: polls(1, mergePoll),
		},
		{
			name:  "unknown for 30 seconds",
			heads: shows(unknown()),
			want:  gaveUp, ops: readsOf(16, 0, 0), waits: polls(15, mergePoll),
		},
		{
			// A third poll would end a second past the deadline.
			name:  "unknown with 5 seconds left",
			heads: shows(unknown()), left: 5 * time.Second,
			want: stillUnknown, ops: readsOf(3, 0, 0), waits: polls(2, mergePoll),
		},
		{
			name:  "conflicting",
			heads: shows(conflicting),
			want:  stateOf(landing.Waited{Outcome: landing.WaitConflicting}, conflicting), ops: readsOf(1, 0, 0),
		},
		{
			name:  "behind",
			heads: shows(behind),
			want:  stateOf(landing.Waited{Outcome: landing.WaitBehind}, behind), ops: readsOf(1, 0, 0),
		},
	}
}

// mergeableRows are the merge states that do not stop the wait before the checks.
func mergeableRows() []vigilRow {
	heads := []forge.Head{
		tip(forge.MergeDraft, "DRAFT"),
		tip(forge.MergeBlockedByReview, "BLOCKED"),
		tip(forge.MergeBlockedByMissingCheck, "BLOCKED"),
		tip(forge.MergeBlockedOther, "UNSTABLE"),
	}

	rows := make([]vigilRow, 0, len(heads))
	for _, head := range heads {
		rows = append(rows, vigilRow{
			name:  string(head.MergeState),
			heads: shows(head), verdicts: says(passing()),
			want: stateOf(green(), head), ops: readsOf(1, 1, 1),
		})
	}

	return rows
}

func TestAwaitVerdict_WhenTheMergeStateDecides_ReadsItBeforeTheChecks(t *testing.T) {
	t.Parallel()

	runVigils(t, slices.Concat(mergeRows(), mergeableRows()))
}

func TestAwaitVerdict_WhenChecksConclude_ReturnsTheMergeStateReadAfterThem(t *testing.T) {
	t.Parallel()

	reviewed := blockedByReview()

	runVigils(t, []vigilRow{{
		name:  "clean, then blocked by review after the checks",
		heads: shows(clean(), reviewed), verdicts: says(passing()),
		want: stateOf(green(), reviewed), ops: readsOf(1, 1, 1),
	}})
}

// slowVerdict returns a read hook under which each read of the verdict takes d, and answers once
// the bubble is idle after it: given the time a wait has left, the read answers once its context
// ended.
func slowVerdict(d time.Duration) func(forgetest.Call) {
	return func(call forgetest.Call) {
		if call.Op == forgetest.OpVerdict {
			time.Sleep(d)
			synctest.Wait()
		}
	}
}

// logRow is a wait and the debug records its logger receives.
type logRow struct {
	name   string
	script forgetest.Script
	want   []string
	left   time.Duration
}

// logRows lists the waits whose debug records are checked, one per way a wait ends but for
// checks still running at the deadline, which stillRunningLogRows lists.
func logRows() []logRow {
	return []logRow{
		{
			name:   "a wait that ends green",
			script: forgetest.Script{Head: shows(unknown(), clean()), Verdict: says(running(), passing())},
			want: []string{
				"DEBUG wait read=merge-state state=unknown window=30s",
				"DEBUG wait read=merge-state state=clean window=28s",
				"DEBUG wait read=checks done=0 pending=1 verdict=pending",
				"DEBUG wait read=checks done=1 pending=0 verdict=green",
				"DEBUG wait read=merge-state state=clean",
				"DEBUG wait ended=green",
			},
		},
		{
			// A third poll would end a second past the deadline.
			name:   "a wait that ends pending on the previous commit",
			script: forgetest.Script{Head: shows(stale())}, left: 5 * time.Second,
			want: []string{"DEBUG wait ended=pending stage=head"},
		},
		{
			name: "a wait whose read of the merge state fails",
			script: forgetest.Script{
				Head: append(shows(unknown()), forgetest.Answer[forge.Head]{Err: errors.New("HTTP 502")}),
			},
			want: []string{
				"DEBUG wait read=merge-state state=unknown window=30s",
				"DEBUG wait ended=failed stage=merge-state",
			},
		},
	}
}

// stillRunningLogRows lists the waits that end with checks still running at the deadline, one per
// way the read of the head after them goes.
func stillRunningLogRows() []logRow {
	return []logRow{
		{
			name:   "a wait that ends pending with checks still running",
			script: forgetest.Script{Head: shows(clean()), Verdict: says(running())}, left: 5 * time.Second,
			want: []string{
				"DEBUG wait read=merge-state state=clean window=30s",
				"DEBUG wait read=checks done=0 pending=1 verdict=pending",
				"DEBUG wait read=merge-state state=clean",
				"DEBUG wait ended=pending stage=verdict",
			},
		},
		{
			name: "a wait whose head read after checks still running is cut short",
			script: forgetest.Script{
				Head:    append(shows(clean()), lateHead()),
				Verdict: says(running()),
			},
			left: 5 * time.Second,
			want: []string{
				"DEBUG wait read=merge-state state=clean window=30s",
				"DEBUG wait read=checks done=0 pending=1 verdict=pending",
				"DEBUG wait ended=pending stage=verdict",
			},
		},
		{
			name:   "a wait whose later read of checks still running the reserve ends",
			script: forgetest.Script{Head: shows(clean()), Verdict: heldAfter(running())}, left: heldLeft,
			want: []string{
				"DEBUG wait read=merge-state state=clean window=30s",
				"DEBUG wait read=checks done=0 pending=1 verdict=pending",
				"DEBUG wait read=merge-state state=clean",
				"DEBUG wait ended=pending stage=verdict",
			},
		},
		{
			name: "a wait whose checks still running leave no time to read the head after them",
			script: forgetest.Script{
				Head: shows(clean(), blockedByReview()), Verdict: says(running()), OnRead: slowVerdict(10 * time.Second),
			},
			left: 10 * time.Second,
			want: []string{
				"DEBUG wait read=merge-state state=clean window=30s",
				"DEBUG wait read=checks done=0 pending=1 verdict=pending",
				"DEBUG wait ended=pending stage=verdict",
			},
		},
	}
}

func TestAwaitVerdict_WhenGivenALogger_LogsEachDecisionOfTheWait(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(logRows(), stillRunningLogRows()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				v := newVigil(t, tt.script, orBound(tt.left))
				w := watchOn(&v.deps)

				_, _ = landing.AwaitVerdict(v.ctx, v.deps, pushedTip())

				assert.Check(t, is.DeepEqual(w.logged(), tt.want))
			})
		})
	}
}

func TestAwaitVerdict_WhenTheHeadMovesPastThePushedCommit_EndsMovedToTheNewHead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		verdicts []forgetest.Answer[forge.Verdict]
		ops      []forgetest.Op
		left     time.Duration
	}{
		{name: "after a green verdict", verdicts: says(passing()), ops: readsOf(1, 1, 1)},
		{name: "after a red verdict", verdicts: says(lintRed()), ops: readsOf(1, 1, 1)},
		{name: "after nothing reported", verdicts: says(forge.Verdict{}), ops: readsOf(1, 5, 1)},
		{
			// Ten seconds leave room for one poll and then for the head read, but not for a second poll
			// with the head read after it.
			name: "after checks still running at the deadline", verdicts: says(running()), left: 10 * time.Second,
			ops: readsOf(1, 2, 1),
		},
		{
			name:     "after a check failed with its log held back and five seconds left",
			verdicts: says(lintHeldBack()), left: 5 * time.Second,
			ops: readsOf(1, 1, 1),
		},
		{
			name:     "after checks still running whose later read the reserve ended",
			verdicts: heldAfter(running()), left: heldLeft,
			ops: readsOf(1, 2, 1),
		},
		{
			name:     "after a check failed with its log held back and the reserve ended the read after it",
			verdicts: heldAfter(lintHeldBack()), left: heldLeft,
			ops: readsOf(1, 2, 1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				v := newVigil(t, forgetest.Script{Head: shows(clean(), moved()), Verdict: tt.verdicts}, orBound(tt.left))

				got := v.await(t)

				want := stateOf(landing.Waited{Outcome: landing.WaitMoved, MovedTo: movedSHA}, clean())
				assert.Check(t, is.DeepEqual(got, want))
				assert.Check(t, is.DeepEqual(v.opsOf(), tt.ops))
			})
		})
	}
}

func TestAwaitVerdict_WhenTheHeadMovesBeforeItsMergeStateIsKnown_EndsMovedReadingNoCheck(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		v := newVigil(t, forgetest.Script{Head: shows(unknown(), moved()), Verdict: says(passing())}, orBound(0))

		got := v.await(t)

		want := stateOf(landing.Waited{Outcome: landing.WaitMoved, MovedTo: movedSHA}, unknown())
		assert.Check(t, is.DeepEqual(got, want))
		assert.Check(t, is.DeepEqual(v.opsOf(), readsOf(2, 0, 0)))
	})
}

func TestAwaitVerdict_WhenChecksStillRunAtTheDeadline_EndsPendingWithThem(t *testing.T) {
	t.Parallel()

	reviewed := blockedByReview()
	tests := []struct {
		name     string
		heads    []forgetest.Answer[forge.Head]
		verdicts []forgetest.Answer[forge.Verdict]
		// after is the head read after the checks, whose merge state the wait ends in.
		after forge.Head
		ops   []forgetest.Op
		waits []time.Duration
		// since is how long after the wait began its verdict wait did.
		since time.Duration
		left  time.Duration
	}{
		{
			// One head poll, then two verdict polls; a third would leave too little time for the head
			// read after it.
			name:  "after a head poll and two verdict polls",
			heads: shows(stale(), clean()), verdicts: says(running()), after: clean(), since: headPoll,
			left: 17 * time.Second,
			ops:  readsOf(2, 3, 1), waits: slices.Concat(polls(1, headPoll), polls(2, verdictPoll)),
		},
		{
			// A poll of five seconds fits in seven, but not with the head read after it.
			name:  "with seven seconds left",
			heads: shows(clean(), reviewed), verdicts: says(running()), after: reviewed, left: 7 * time.Second,
			ops: readsOf(1, 1, 1),
		},
		{
			// Eight seconds hold one poll and the head read after it, and no more.
			name:  "with eight seconds left",
			heads: shows(clean(), reviewed), verdicts: says(running()), after: reviewed, left: 8 * time.Second,
			ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			// The read after the poll lasts until its context ends, as a read that would answer green
			// inside the last 3 seconds does: the reserve ends it, and the head is read with 3 seconds
			// left.
			name:  "when the reserve ends the read after a poll",
			heads: shows(clean(), reviewed), verdicts: heldAfter(running()), after: reviewed, left: heldLeft,
			ops: readsOf(1, 2, 1), waits: heldPauses(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				began := time.Now()
				v := newVigil(t, forgetest.Script{Head: tt.heads, Verdict: tt.verdicts}, tt.left)

				got := v.await(t)

				want := stateOf(concluded(landing.WaitPending, running(), 0, 0), tt.after)
				want.Stage = landing.StageVerdict
				want.Since = began.Add(tt.since)
				assert.Check(t, is.DeepEqual(got, want))
				assert.Check(t, is.DeepEqual(v.opsOf(), tt.ops))
				assert.Check(t, is.DeepEqual(v.pauses(), tt.waits, cmpopts.EquateEmpty()))
			})
		})
	}
}

// heldOver is how a wait on a clean pull request ends when the reserve ended the read after the
// poll that followed verdict, which the wait would have read again.
func heldOver(verdict forge.Verdict, passed int) landing.Waited {
	waited := concluded(landing.WaitPending, verdict, passed, 0)
	waited.Stage = landing.StageVerdict

	return waited
}

func TestAwaitVerdict_WhenTheReserveEndsALaterVerdictRead_KeepsTheVerdictBeforeIt(t *testing.T) {
	t.Parallel()

	scanned := appPassing()
	rows := []vigilRow{
		{
			name:     "a check failed with its log held back",
			verdicts: heldAfter(lintHeldBack()),
			want:     concluded(landing.WaitRed, lintHeldBack(), 0, 0),
		},
		{name: "nothing reported yet", verdicts: heldAfter(forge.Verdict{}), want: heldOver(forge.Verdict{}, 0)},
		{name: "a check passed and no workflow run started yet", verdicts: heldAfter(scanned), want: heldOver(scanned, 1)},
	}
	for i := range rows {
		rows[i].heads = shows(clean())
		rows[i].left = heldLeft
		rows[i].ops = readsOf(1, 2, 1)
		rows[i].waits = heldPauses()
	}

	runVigils(t, rows)
}

// slowLaterVerdict returns a read hook under which each read of the verdict after the first takes
// d, whatever its context, and answers once the bubble is idle after it.
func slowLaterVerdict(d time.Duration) func(forgetest.Call) {
	read := false

	return func(call forgetest.Call) {
		if call.Op != forgetest.OpVerdict {
			return
		}

		if read {
			time.Sleep(d)
			synctest.Wait()
		}

		read = true
	}
}

func TestAwaitVerdict_WhenALaterVerdictReadAnswersPastTheReserve_JudgesWhatItSays(t *testing.T) {
	t.Parallel()

	reviewed := blockedByReview()
	tests := []struct {
		name string
		ops  []forgetest.Op
		want landing.Waited
		// took is how long the read after the poll takes; it begins 3.5 seconds before the deadline.
		took time.Duration
	}{
		{
			name: "checks still running with time left for the head read",
			want: stateOf(heldOver(running(), 0), reviewed), ops: readsOf(1, 2, 1), took: time.Second,
		},
		{
			name: "checks still running past the deadline",
			want: pendingAt(landing.StageVerdict, clean()),
			ops:  readsOf(1, 2, 0), took: 4 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				script := forgetest.Script{
					Head: shows(clean(), reviewed), Verdict: says(running()), OnRead: slowLaterVerdict(tt.took),
				}
				v := newVigil(t, script, heldLeft)

				got := v.await(t)

				assert.Check(t, is.DeepEqual(got, tt.want, ignoreSince(), cmpopts.EquateEmpty()))
				assert.Check(t, is.DeepEqual(v.opsOf(), tt.ops))
			})
		})
	}
}

func TestAwaitVerdict_WhenTheHeadAfterTheChecksIsNotRead_EndsPendingWithoutTheVerdict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		want     landing.WaitStage
		heads    []forgetest.Answer[forge.Head]
		verdicts []forgetest.Answer[forge.Verdict]
		ops      []forgetest.Op
		waits    []time.Duration
		left     time.Duration
	}{
		{
			name: "the deadline ends inside the head read after green checks", want: landing.StageMergeState,
			heads: append(shows(clean()), lateHead()), verdicts: says(passing()),
			ops: readsOf(1, 1, 1),
		},
		{
			// Ten seconds leave room for one poll and then for the head read, but not for a second poll
			// with the head read after it.
			name: "the deadline ends inside the head read after checks still running", want: landing.StageVerdict,
			heads: append(shows(clean()), lateHead()), verdicts: says(running()), left: 10 * time.Second,
			ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			// The reserve ends the read after the poll; the wait's own deadline then ends the head read.
			name: "the deadline ends inside the head read after a later read the reserve ended", want: landing.StageVerdict,
			heads:    []forgetest.Answer[forge.Head]{{Value: clean()}, {Hold: true}},
			verdicts: heldAfter(running()), left: heldLeft,
			ops: readsOf(1, 2, 1), waits: append(heldPauses(), headReadReserve),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				v := newVigil(t, forgetest.Script{Head: tt.heads, Verdict: tt.verdicts}, orBound(tt.left))

				got := v.await(t)

				want := pendingAt(tt.want, clean())
				assert.Check(t, is.DeepEqual(got, want, cmpopts.EquateEmpty()))
				assert.Check(t, is.DeepEqual(v.opsOf(), tt.ops, cmpopts.EquateEmpty()))
				assert.Check(t, is.DeepEqual(v.pauses(), tt.waits, cmpopts.EquateEmpty()))
			})
		})
	}
}

func TestAwaitVerdict_WhenDeadlineEndedDuringTheWait_ReturnsTheEarlierMergeState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		want    landing.WaitStage
		verdict forge.Verdict
	}{
		{name: "green checks", verdict: passing(), want: landing.StageMergeState},
		{name: "checks still running", verdict: running(), want: landing.StageVerdict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				script := forgetest.Script{
					Head:    shows(clean(), blockedByReview()),
					Verdict: says(tt.verdict),
					// The verdict read takes the whole time the wait had left.
					OnRead: slowVerdict(bound),
				}
				v := newVigil(t, script, bound)

				got := v.await(t)

				want := pendingAt(tt.want, clean())
				assert.Check(t, is.DeepEqual(got, want, cmpopts.EquateEmpty()))
			})
		})
	}
}

// cutShort is one wait whose deadline ends inside a read or a poll.
type cutShort struct {
	name     string
	heads    []forgetest.Answer[forge.Head]
	verdicts []forgetest.Answer[forge.Verdict]
	want     landing.Waited
	// left is the time before the deadline; zero is the whole bound.
	left time.Duration
	// poll is whether the deadline ends inside the first poll, and is not known ahead of it.
	poll bool
}

func cutShortWaits() []cutShort {
	ended := fmt.Errorf("gh: %w", context.DeadlineExceeded)
	lateVerdict := forgetest.Answer[forge.Verdict]{Err: ended}
	atHead := landing.Waited{Outcome: landing.WaitPending, Stage: landing.StageHead}
	atMergeState := pendingAt(landing.StageMergeState, unknown())
	// atVerdict holds no verdict: only a head read after the checks shows they are the pushed commit's.
	atVerdict := pendingAt(landing.StageVerdict, clean())

	return []cutShort{
		{name: "inside the head read", want: atHead, heads: []forgetest.Answer[forge.Head]{lateHead()}},
		{name: "inside the head poll", want: stateOf(atHead, stale()), heads: shows(stale()), poll: true},
		{name: "inside the merge state read", want: atMergeState, heads: append(shows(unknown()), lateHead())},
		{name: "inside the merge state poll", want: atMergeState, heads: shows(unknown()), poll: true},
		{
			name: "inside the first verdict read", want: atVerdict,
			heads: shows(clean()), verdicts: []forgetest.Answer[forge.Verdict]{lateVerdict},
		},
		{
			name: "inside a later verdict read of checks still running", want: atVerdict,
			heads: shows(clean()), verdicts: append(says(running()), lateVerdict),
		},
		{
			name: "inside the verdict poll of checks still running", want: atVerdict,
			heads: shows(clean()), verdicts: says(running()), poll: true,
		},
	}
}

// newCutShort returns the wait of tt: under a deadline the time tt has left away, or under one
// that ends midPoll into the first poll without being known ahead of it.
func newCutShort(t *testing.T, tt cutShort) *vigil {
	t.Helper()

	script := forgetest.Script{Head: tt.heads, Verdict: tt.verdicts}
	if !tt.poll {
		return newVigil(t, script, orBound(tt.left))
	}

	ended := make(chan struct{})
	time.AfterFunc(midPoll, func() { close(ended) })

	return newVigilOn(gittest.Ending(t.Context(), ended, context.DeadlineExceeded), t, script)
}

func TestAwaitVerdict_WhenDeadlineEndsInsideAReadOrAPoll_EndsPendingAtThatStage(t *testing.T) {
	t.Parallel()

	for _, tt := range cutShortWaits() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				v := newCutShort(t, tt)

				got := v.await(t)

				assert.Check(t, is.DeepEqual(got, tt.want, cmpopts.EquateEmpty()))
			})
		})
	}
}

func TestAwaitVerdict_WhenTheDeadlineEndsTheFirstVerdictRead_EndsPendingWithoutReadingTheHeadAgain(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		v := newVigil(t, forgetest.Script{
			Head:    shows(clean()),
			Verdict: []forgetest.Answer[forge.Verdict]{{Hold: true}},
		}, heldLeft)
		began := time.Now()

		got := v.await(t)

		assert.Check(t, is.DeepEqual(got, pendingAt(landing.StageVerdict, clean()), cmpopts.EquateEmpty()))
		assert.Check(t, is.DeepEqual(v.opsOf(), readsOf(1, 1, 0)))
		assert.Check(t, is.Equal(time.Since(began), heldLeft), "the first read runs under the wait's own deadline")
	})
}

// canceledAfter returns a context canceled after the given time, at once when it is zero, and
// bounded by a deadline left away when left is not zero.
func canceledAfter(t *testing.T, after, left time.Duration) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	if left > 0 {
		bounded, stop := context.WithDeadline(ctx, time.Now().Add(left))
		t.Cleanup(stop)

		ctx = bounded
	}

	if after == 0 {
		cancel()
	} else {
		time.AfterFunc(after, cancel)
	}

	return ctx
}

func TestAwaitVerdict_WhenContextIsCanceled_ReturnsAnErrorNamingTheStageAtThatInstant(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		want     landing.WaitStage
		heads    []forgetest.Answer[forge.Head]
		verdicts []forgetest.Answer[forge.Verdict]
		// after is how far into the first poll the context is canceled; zero is before the wait.
		after time.Duration
		// left is the time before the deadline; zero is no deadline.
		left time.Duration
	}{
		// Each script goes on to a green verdict, which is how a wait that slept through the
		// cancellation would end.
		{
			name: "during the head poll", want: landing.StageHead,
			heads: shows(stale(), clean()), verdicts: says(passing()), after: midPoll,
		},
		{
			name: "during the merge state poll", want: landing.StageMergeState,
			heads: shows(unknown(), clean()), verdicts: says(passing()), after: midPoll,
		},
		{
			name: "during the verdict poll", want: landing.StageVerdict,
			heads: shows(clean()), verdicts: says(running(), passing()), after: midPoll,
		},
		{
			name: "before the first poll", want: landing.StageHead,
			heads: shows(stale(), clean()), verdicts: says(passing()),
		},
		{
			name: "during a later verdict read under a deadline", want: landing.StageVerdict,
			heads: shows(clean()), verdicts: heldAfter(running()), after: verdictPoll + midPoll, left: bound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ctx := canceledAfter(t, tt.after, tt.left)
				v := newVigilOn(ctx, t, forgetest.Script{Head: tt.heads, Verdict: tt.verdicts})
				began := time.Now()

				_, err := landing.AwaitVerdict(v.ctx, v.deps, pushedTip())

				got := waitError(t, err)
				assert.Check(t, is.Equal(got.Stage, tt.want))
				assert.Check(t, is.ErrorIs(err, context.Canceled))
				assert.Check(t, is.Equal(time.Since(began), tt.after))
			})
		})
	}
}

func TestAwaitVerdict_WhenAReadFails_ReturnsAnErrorNamingTheRead(t *testing.T) {
	t.Parallel()

	cause := errors.New("HTTP 502")
	failed := forgetest.Answer[forge.Head]{Err: cause}
	tests := []struct {
		name     string
		want     landing.WaitStage
		heads    []forgetest.Answer[forge.Head]
		verdicts []forgetest.Answer[forge.Verdict]
		left     time.Duration
	}{
		{name: "the head", want: landing.StageHead, heads: []forgetest.Answer[forge.Head]{failed}},
		{name: "the merge state", want: landing.StageMergeState, heads: append(shows(unknown()), failed)},
		{
			name: "the verdict", want: landing.StageVerdict,
			heads: shows(clean()), verdicts: []forgetest.Answer[forge.Verdict]{{Err: cause}},
		},
		{
			name: "the merge state after the checks", want: landing.StageMergeState,
			heads: append(shows(clean()), failed), verdicts: says(passing()),
		},
		{
			// Ten seconds leave room for one poll and then for this read, but not for a second poll
			// with this read after it.
			name: "the merge state after checks that were still running", want: landing.StageMergeState,
			heads: append(shows(clean()), failed), verdicts: says(running()), left: 10 * time.Second,
		},
		{
			name: "the merge state after a later read of checks still running that the reserve ended",
			want: landing.StageMergeState, heads: append(shows(clean()), failed), verdicts: heldAfter(running()),
			left: heldLeft,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				v := newVigil(t, forgetest.Script{Head: tt.heads, Verdict: tt.verdicts}, orBound(tt.left))

				_, err := landing.AwaitVerdict(v.ctx, v.deps, pushedTip())

				got := waitError(t, err)
				assert.Check(t, is.Equal(got.Stage, tt.want))
				assert.Check(t, is.ErrorIs(err, cause))
			})
		})
	}
}

const (
	readHead       = "the pull request's head"
	readMergeState = "whether the pull request can merge"
	readChecks     = "the checks of the pushed commit"
)

// steps returns the lines "step <first>" to "step <last>", each ended by a line break.
func steps(first, last int) string {
	var lines strings.Builder
	for i := first; i <= last; i++ {
		fmt.Fprintf(&lines, "step %d\n", i)
	}

	return lines.String()
}

// assertReadsRequire fails unless calls hold reads of both the head and the verdict of pull request
// 12, each asking for exactly the checks required, and each verdict asked for the commit tip.
func assertReadsRequire(t *testing.T, calls []forgetest.Call, tip string, required []string) {
	t.Helper()

	var heads, verdicts int

	for _, call := range calls {
		if call.Op == forgetest.OpHead {
			heads++
			assert.Check(t, is.DeepEqual(call.Head, forge.HeadQuery{Number: opened, Required: required}))
		}

		if call.Op == forgetest.OpVerdict {
			verdicts++
			assert.Check(t, is.DeepEqual(call.Query, forge.VerdictQuery{SHA: tip, Number: opened, Required: required}))
		}
	}

	assert.Check(t, heads > 0 && verdicts > 0, "forge reads = %d heads and %d verdicts, want some of each", heads, verdicts)
}
