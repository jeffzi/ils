package landing_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
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

// within returns the test's context with a deadline left away from now, or the whole bound when
// left is zero; it is canceled when the test ends.
func within(t *testing.T, left time.Duration) context.Context {
	t.Helper()

	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(orBound(left)))
	t.Cleanup(cancel)

	return ctx
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

// vigil is one wait inside a bubble: the forge it reads, the times of its reads, and how the
// context it runs under is made.
type vigil struct {
	// under makes the context of the wait from the test's.
	under func(context.Context) context.Context
	forge *forgetest.Fake
	log   *readLog
	// ended is the fake time at which the wait returned.
	ended time.Time
	deps  landing.Deps
}

// newVigilOn returns a wait under the context under makes, that reads a forge answering from
// script.
func newVigilOn(t *testing.T, script forgetest.Script, under func(context.Context) context.Context) *vigil {
	t.Helper()

	log := &readLog{}
	fake := forgetest.New(t, log.timed(script))

	return &vigil{under: under, forge: fake, log: log, deps: landing.Deps{Forge: fake}}
}

// newVigil is newVigilOn with a context whose deadline is left of fake time away, or the whole
// bound when left is zero.
func newVigil(t *testing.T, script forgetest.Script, left time.Duration) *vigil {
	t.Helper()

	return newVigilOn(t, script, func(context.Context) context.Context { return within(t, left) })
}

func (v *vigil) context(t *testing.T) context.Context {
	t.Helper()

	return v.under(t.Context())
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
func tip(state forge.MergeState) forge.Head {
	return forge.Head{SHA: pushedSHA, MergeState: state}
}

// clean, unknown, and stale are the heads most waits read: the pushed commit ready to merge, the
// pushed commit before the forge worked its merge state out, and the commit before the push.
func clean() forge.Head   { return tip(forge.MergeClean) }
func unknown() forge.Head { return tip(forge.MergeUnknown) }
func stale() forge.Head {
	return forge.Head{SHA: staleSHA, MergeState: forge.MergeClean}
}

// moved is the head of a pull request whose branch a commit after the pushed one reached, in a
// merge state of its own, unlike the pushed commit's.
func moved() forge.Head {
	return forge.Head{SHA: movedSHA, MergeState: forge.MergeBlockedByReview}
}

// blockedByReview is the pushed commit, blocked until a review approves it.
func blockedByReview() forge.Head { return tip(forge.MergeBlockedByReview) }

// lateHead is the answer of a read of the head that c ended.
func lateHead(c cut) forgetest.Answer[forge.Head] {
	return forgetest.Answer[forge.Head]{Err: c.of("gh")}
}

// shows returns a head sequence that answers with each of heads in turn.
func shows(heads ...forge.Head) []forgetest.Answer[forge.Head] { return forgetest.Values(heads...) }

// says returns a verdict sequence that answers with each of verdicts in turn.
func says(verdicts ...forge.Verdict) []forgetest.Answer[forge.Verdict] {
	return forgetest.Values(verdicts...)
}

// heldAfter returns a verdict sequence that answers with each of verdicts in turn, then holds
// every later read until its context ends.
func heldAfter(verdicts ...forge.Verdict) []forgetest.Answer[forge.Verdict] {
	return append(says(verdicts...), forgetest.Answer[forge.Verdict]{Hold: true})
}

// ran returns the ci workflow's run in status.
func ran(status forge.RunStatus) []forge.Run {
	return []forge.Run{{Name: "ci", Status: status}}
}

// passing is a verdict whose one check passed and whose run completed.
func passing() forge.Verdict {
	return forge.Verdict{Checks: []forge.Check{forgetest.Check("test", forge.BucketPass)}, Runs: ran(forge.RunCompleted)}
}

// running is a verdict whose one check still runs.
func running() forge.Verdict {
	return forge.Verdict{Checks: []forge.Check{forgetest.Check("test", forge.BucketPending)}, Runs: ran(forge.RunInProgress)}
}

// bothRunning is a verdict whose two checks, lint and test, still run.
func bothRunning() forge.Verdict {
	return forge.Verdict{
		Checks: []forge.Check{forgetest.Check("lint", forge.BucketPending), forgetest.Check("test", forge.BucketPending)},
		Runs:   ran(forge.RunInProgress),
	}
}

// requiredPassing is a verdict whose required checks lint and test passed and whose run completed.
func requiredPassing() forge.Verdict {
	return forge.Verdict{
		Checks: []forge.Check{forgetest.RequiredCheck("lint", forge.BucketPass), forgetest.RequiredCheck("test", forge.BucketPass)},
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
		Outcome:    outcome,
		MergeState: forge.MergeClean,
		Verdict:    verdict,
		Deciding:   verdict.Checks,
		Passed:     passed,
		Skipped:    skipped,
	}
}

// green is how a wait ends when passing was read.
func green() landing.Waited { return concluded(landing.WaitGreen, passing(), 1, 0) }

// stateOf returns waited with the merge state of head.
func stateOf(waited landing.Waited, head forge.Head) landing.Waited {
	waited.MergeState = head.MergeState

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

	got, err := landing.AwaitVerdict(v.context(t), v.deps, pushedTip(required...))
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

// vigilRow is one wait: what the forge answers, and how the wait must end.
type vigilRow struct {
	name string
	// told, when set, is what the wait must tell its progress hook, in order.
	told     []string
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

// onCleanHead returns rows, each of which reads the clean head.
func onCleanHead(rows []vigilRow) []vigilRow {
	for i := range rows {
		rows[i].heads = shows(clean())
	}

	return rows
}

// runVigils runs each row as a test in a bubble of its own: the wait ends as the row wants, having
// read the forge and let fake time pass between its reads exactly as the row says.
func runVigils(t *testing.T, rows []vigilRow) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				v := newVigil(t, forgetest.Script{Head: tt.heads, Verdict: tt.verdicts}, tt.left)
				w := watchOn(&v.deps)

				got := v.await(t, tt.required...)

				assert.Check(t, is.DeepEqual(got, tt.want, ignoreSince(), cmpopts.EquateEmpty()))
				assert.Check(t, is.DeepEqual(opsOf(v.forge.Calls()), tt.ops, cmpopts.EquateEmpty()))
				assert.Check(t, is.DeepEqual(v.pauses(), tt.waits, cmpopts.EquateEmpty()))
				if tt.told != nil {
					assertTold(t, w.told(t), tt.told)
				}
			})
		})
	}
}

func TestAwaitVerdict_WhenTheHeadIsNotYetThePushedCommit_ReadsNoCheck(t *testing.T) {
	t.Parallel()

	runVigils(t, []vigilRow{
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
	conflicting := tip(forge.MergeConflicting)
	behind := tip(forge.MergeBehind)
	stillUnknown := pendingAt(landing.StageMergeState, unknown())
	// gaveUp is the wait that its own window ended, with time left before the deadline.
	gaveUp := stillUnknown
	gaveUp.Cutoff = landing.CutoffWindow

	return []vigilRow{
		{
			name:  "unknown then clean",
			heads: shows(unknown(), clean()), verdicts: says(passing()),
			want: green(), ops: readsOf(2, 1, 1), waits: polls(1, mergePoll),
			told: []string{waitingStep(bound), mergeStateStep(), waitingStep(bound - mergePoll), "count 1 of 1"},
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

func TestAwaitVerdict_WhenTheMergeStateDecides_ReadsItBeforeTheChecks(t *testing.T) {
	t.Parallel()

	runVigils(t, mergeRows())
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
	fails  bool
}

// logRows lists the waits whose debug records are checked, one per way a wait ends but for
// checks still running at the deadline, which stillRunningLogRows lists.
func logRows() []logRow {
	return []logRow{
		{
			name:   "a wait that ends green",
			script: forgetest.Script{Head: shows(unknown(), clean()), Verdict: says(running(), passing())},
			want: []string{
				"DEBUG wait read=merge-state state=unknown window=" + landing.MergeWindow.String(),
				"DEBUG wait read=merge-state state=clean window=" + (landing.MergeWindow - mergePoll).String(),
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
				Head: append(shows(unknown()), forgetest.Answer[forge.Head]{Err: errors.New(forgeFailure)}),
			},
			fails: true,
			want: []string{
				"DEBUG wait read=merge-state state=unknown window=" + landing.MergeWindow.String(),
				"DEBUG wait ended=failed stage=merge-state",
			},
		},
	}
}

// stillRunningLogRows lists the waits that end with checks still running at the deadline whose
// read of the head after them is cut short.
func stillRunningLogRows() []logRow {
	return []logRow{
		{
			name: "a wait whose head read after checks still running is cut short",
			script: forgetest.Script{
				Head:    append(shows(clean()), lateHead(byTimeLimit())),
				Verdict: says(running()),
			},
			left: 5 * time.Second,
			want: []string{
				"DEBUG wait read=merge-state state=clean window=" + landing.MergeWindow.String(),
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
				v := newVigil(t, tt.script, tt.left)
				w := watchOn(&v.deps)

				_, err := landing.AwaitVerdict(v.context(t), v.deps, pushedTip())
				if tt.fails {
					_, ok := errors.AsType[*landing.WaitError](err)
					assert.Check(t, ok, "AwaitVerdict() error = %v, want a *landing.WaitError", err)
				} else {
					assert.NilError(t, err)
				}

				assert.Check(t, is.DeepEqual(w.logged(), tt.want))
			})
		})
	}
}

func TestAwaitVerdict_WhenTheHeadMovesPastThePushedCommit_EndsMovedToTheNewHead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		heads    []forgetest.Answer[forge.Head]
		before   forge.Head
		verdicts []forgetest.Answer[forge.Verdict]
		ops      []forgetest.Op
		left     time.Duration
	}{
		{
			name: "after a green verdict", heads: shows(clean(), moved()), before: clean(), verdicts: says(passing()),
			ops: readsOf(1, 1, 1),
		},
		{
			// Ten seconds leave room for one poll and then for the head read, but not for a second poll
			// with the head read after it.
			name: "after checks still running at the deadline", heads: shows(clean(), moved()), before: clean(),
			verdicts: says(running()), left: 10 * time.Second, ops: readsOf(1, 2, 1),
		},
		{
			name: "before its merge state is known", heads: shows(unknown(), moved()), before: unknown(),
			verdicts: says(passing()), ops: readsOf(2, 0, 0),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				v := newVigil(t, forgetest.Script{Head: tt.heads, Verdict: tt.verdicts}, tt.left)

				got := v.await(t)

				want := stateOf(landing.Waited{Outcome: landing.WaitMoved, MovedTo: movedSHA}, tt.before)
				assert.Check(t, is.DeepEqual(got, want))
				assert.Check(t, is.DeepEqual(opsOf(v.forge.Calls()), tt.ops))
			})
		})
	}
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
				assert.Check(t, is.DeepEqual(opsOf(v.forge.Calls()), tt.ops))
				assert.Check(t, is.DeepEqual(v.pauses(), tt.waits, cmpopts.EquateEmpty()))
			})
		})
	}
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

	synctest.Test(t, func(t *testing.T) {
		reviewed := blockedByReview()
		// The read after the poll begins 3.5 seconds before the deadline and takes a second, so it
		// answers past the reserve with time left for the head read.
		script := forgetest.Script{
			Head: shows(clean(), reviewed), Verdict: says(running(), passing()), OnRead: slowLaterVerdict(time.Second),
		}
		v := newVigil(t, script, heldLeft)

		got := v.await(t)

		assert.Check(t, is.DeepEqual(got, stateOf(green(), reviewed), ignoreSince(), cmpopts.EquateEmpty()))
		assert.Check(t, is.DeepEqual(opsOf(v.forge.Calls()), readsOf(1, 2, 1)))
	})
}

func TestAwaitVerdict_WhenTheHeadAfterTheChecksIsNotRead_EndsPendingWithoutTheVerdict(t *testing.T) {
	t.Parallel()

	type row struct {
		name     string
		heads    []forgetest.Answer[forge.Head]
		verdicts []forgetest.Answer[forge.Verdict]
		ops      []forgetest.Op
		waits    []time.Duration
		want     landing.Waited
		left     time.Duration
	}

	c := byTimeLimit()
	tests := []row{
		{
			// The reserve ends the read after the poll; the wait's own deadline then ends the head read.
			name:     "the deadline ends inside the head read after a later read the reserve ended",
			want:     pendingAt(landing.StageVerdict, clean()),
			heads:    []forgetest.Answer[forge.Head]{{Value: clean()}, {Hold: true}},
			verdicts: heldAfter(running()),
			left:     heldLeft,
			ops:      readsOf(1, 2, 1),
			waits:    append(heldPauses(), headReadReserve),
		},
		{
			name:     c.name + " ends the head read after green checks",
			want:     pendingAt(landing.StageMergeState, clean()),
			heads:    append(shows(clean()), lateHead(c)),
			verdicts: says(passing()),
			ops:      readsOf(1, 1, 1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				v := newVigil(t, forgetest.Script{Head: tt.heads, Verdict: tt.verdicts}, tt.left)

				got := v.await(t)

				assert.Check(t, is.DeepEqual(got, tt.want, cmpopts.EquateEmpty()))
				assert.Check(t, is.DeepEqual(opsOf(v.forge.Calls()), tt.ops, cmpopts.EquateEmpty()))
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

// cutShort is one wait a cut ends inside a read or a poll.
type cutShort struct {
	name     string
	heads    []forgetest.Answer[forge.Head]
	verdicts []forgetest.Answer[forge.Verdict]
	want     landing.Waited
}

// endedBy returns waited as a wait ends when c ended it: an interrupt is its cutoff.
func endedBy(c cut, waited landing.Waited) landing.Waited {
	if errors.Is(c.err, context.Canceled) {
		waited.Cutoff = landing.CutoffInterrupt
	}

	return waited
}

// atVerdict is the wait c ended at the read of the checks. It holds no verdict: only a head read
// after the checks shows they are the pushed commit's.
func atVerdict(c cut) landing.Waited {
	return endedBy(c, pendingAt(landing.StageVerdict, clean()))
}

// atMergeState is the wait c ended at the read of the merge state.
func atMergeState(c cut) landing.Waited {
	return endedBy(c, pendingAt(landing.StageMergeState, unknown()))
}

// readCutWaits are the waits whose read c ends: the read fails with c's error.
func readCutWaits(c cut) []cutShort {
	lateVerdict := forgetest.Answer[forge.Verdict]{Err: c.of("gh")}
	atHead := endedBy(c, landing.Waited{Outcome: landing.WaitPending, Stage: landing.StageHead})

	return []cutShort{
		{name: "inside the head read", want: atHead, heads: []forgetest.Answer[forge.Head]{lateHead(c)}},
		{name: "inside the merge state read", want: atMergeState(c), heads: append(shows(unknown()), lateHead(c))},
		{
			name: "inside a later verdict read of checks still running", want: atVerdict(c),
			heads: shows(clean()), verdicts: append(says(running()), lateVerdict),
		},
	}
}

// headPollCut is the wait c ends midPoll into its first head poll.
func headPollCut(c cut) cutShort {
	return cutShort{
		name: "inside the head poll", heads: shows(stale()),
		want: stateOf(endedBy(c, landing.Waited{Outcome: landing.WaitPending, Stage: landing.StageHead}), stale()),
	}
}

// pollCutWaits are the waits c ends midPoll into their first poll, without it being known ahead.
func pollCutWaits(c cut) []cutShort {
	return []cutShort{
		headPollCut(c),
		{name: "inside the merge state poll", want: atMergeState(c), heads: shows(unknown())},
		{
			name: "inside the verdict poll of checks still running", want: atVerdict(c),
			heads: shows(clean()), verdicts: says(running()),
		},
	}
}

func TestAwaitVerdict_WhenACutEndsARead_EndsPendingAtThatStage(t *testing.T) {
	t.Parallel()

	for _, tt := range readCutWaits(byTimeLimit()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				v := newVigil(t, forgetest.Script{Head: tt.heads, Verdict: tt.verdicts}, bound)

				got := v.await(t)

				assert.Check(t, is.DeepEqual(got, tt.want, cmpopts.EquateEmpty()))
			})
		})
	}
}

func TestAwaitVerdict_WhenACutEndsAPoll_EndsPendingAtThatStageMidPoll(t *testing.T) {
	t.Parallel()

	// Every poll runs under the time limit; the head poll, where a wait's cutoff is first told
	// apart, runs under an interrupt too.
	type pollCut struct {
		c  cut
		tt cutShort
	}
	var tests []pollCut
	for _, tt := range pollCutWaits(byTimeLimit()) {
		tests = append(tests, pollCut{c: byTimeLimit(), tt: tt})
	}
	tests = append(tests, pollCut{c: byInterrupt(), tt: headPollCut(byInterrupt())})
	for _, pc := range tests {
		c, tt := pc.c, pc.tt
		t.Run(c.name+"/"+tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				v := newVigilOn(t, forgetest.Script{Head: tt.heads, Verdict: tt.verdicts}, c.inPoll)
				began := time.Now()

				got := v.await(t)

				assert.Check(t, is.DeepEqual(got, tt.want, cmpopts.EquateEmpty()))
				assert.Check(t, is.Equal(time.Since(began), midPoll))
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
		assert.Check(t, is.DeepEqual(opsOf(v.forge.Calls()), readsOf(1, 1, 0)))
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

func TestAwaitVerdict_WhenContextIsCanceledDuringALaterVerdictRead_EndsPendingInterruptedAtTheVerdict(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		after := verdictPoll + midPoll
		v := newVigilOn(t, forgetest.Script{Head: shows(clean()), Verdict: heldAfter(running())},
			func(context.Context) context.Context { return canceledAfter(t, after, bound) })
		began := time.Now()

		got := v.await(t)

		want := endedBy(byInterrupt(), pendingAt(landing.StageVerdict, clean()))
		assert.Check(t, is.DeepEqual(got, want, ignoreSince(), cmpopts.EquateEmpty()))
		assert.Check(t, is.Equal(time.Since(began), after))
	})
}

// lateInterrupt is a context an interrupt cancels at a set moment, which a look at its Err sees
// from that moment on; its Done channel never closes, and Err is all the test reads. It stands for
// an interrupt that lands after a wait's timer fired and before the wait looks at its context
// again, a moment no context canceled by a timer of its own is sure to hit, since the wait's timer
// and that one fire at the same instant. Nothing over it cancels it.
type lateInterrupt struct {
	context.Context

	at time.Time
}

// interruptIn returns a lateInterrupt over the test's values that an interrupt cancels d of fake
// time from now.
func interruptIn(t *testing.T, d time.Duration) *lateInterrupt {
	t.Helper()

	return &lateInterrupt{Context: context.WithoutCancel(t.Context()), at: time.Now().Add(d)}
}

// Err returns context.Canceled once the interrupt landed, and nil before.
func (l *lateInterrupt) Err() error {
	if time.Now().Before(l.at) {
		return nil
	}

	return context.Canceled
}

func TestAwaitVerdict_WhenAnInterruptLandsAsTheHeadPollEnds_EndsPendingInterruptedAtTheHead(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// The first head read is at once, so the poll after it ends headPoll from now. The script goes
		// on to a green verdict, which is how a wait that missed the interrupt would end.
		v := newVigilOn(t, forgetest.Script{Head: shows(stale(), clean()), Verdict: says(passing())},
			func(context.Context) context.Context { return interruptIn(t, headPoll) })
		began := time.Now()

		got := v.await(t)

		want := endedBy(byInterrupt(), pendingAt(landing.StageHead, stale()))
		assert.Check(t, is.DeepEqual(got, want, ignoreSince(), cmpopts.EquateEmpty()))
		assert.Check(t, is.Equal(time.Since(began), headPoll))
	})
}

func TestAwaitVerdict_WhenAReadFails_ReturnsAnErrorNamingTheRead(t *testing.T) {
	t.Parallel()

	cause := errors.New(forgeFailure)
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				v := newVigil(t, forgetest.Script{Head: tt.heads, Verdict: tt.verdicts}, tt.left)

				_, err := landing.AwaitVerdict(v.context(t), v.deps, pushedTip())

				got := waitError(t, err)
				assert.Check(t, is.Equal(got.Stage, tt.want))
				assert.Check(t, is.ErrorIs(err, cause))
			})
		})
	}
}
