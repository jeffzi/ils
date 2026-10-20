package landing_test

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
)

const (
	pushedSHA = "2222222"
	staleSHA  = "1111111"

	// bound is the time a wait has left unless its test says otherwise.
	bound = 100 * time.Second

	headPoll    = 2 * time.Second
	mergePoll   = 2 * time.Second
	verdictPoll = 5 * time.Second

	// mergeWindow is how long a wait polls a merge state the forge has not worked out.
	mergeWindow = 30 * time.Second

	// midPoll is how far into its first poll a wait is when its test ends the context: every
	// poll lasts longer.
	midPoll = time.Second
)

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

func TestAwaitVerdict_ReadsNoCheckUntilTheHeadIsThePushedCommit(t *testing.T) {
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
			want: stateOf(landing.Waited{Outcome: landing.WaitPending, Stage: landing.StageHead}, stale()),
			ops:  readsOf(3, 0, 0), waits: polls(2, headPoll),
		},
	})
}

// mergeRows are the waits decided by the merge state the head read carries.
func mergeRows() []vigilRow {
	conflicting := tip(forge.MergeConflicting, "DIRTY")
	behind := tip(forge.MergeBehind, "BEHIND")
	stillUnknown := stateOf(landing.Waited{Outcome: landing.WaitPending, Stage: landing.StageMergeState}, unknown())
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

func TestAwaitVerdict_ReadsTheMergeStateBeforeTheChecks(t *testing.T) {
	t.Parallel()

	runVigils(t, slices.Concat(mergeRows(), mergeableRows()))
}

func TestAwaitVerdict_WhenChecksConclude_ReturnsTheMergeStateReadAfterThem(t *testing.T) {
	t.Parallel()

	reviewed := tip(forge.MergeBlockedByReview, "BLOCKED")

	runVigils(t, []vigilRow{{
		name:  "clean, then blocked by review after the checks",
		heads: shows(clean(), reviewed), verdicts: says(passing()),
		want: stateOf(green(), reviewed), ops: readsOf(1, 1, 1),
	}})
}

// everyCheckRows are the verdict waits where the trunk names no required check.
func everyCheckRows() []vigilRow {
	started := forge.Verdict{Runs: ran(forge.RunInProgress)}
	finishing := forge.Verdict{Checks: passing().Checks, Runs: ran(forge.RunInProgress)}
	approving := forge.Verdict{Checks: passing().Checks, Runs: ran(forge.RunAwaitingPerson)}

	return []vigilRow{
		{
			name:     "checks pending then green",
			verdicts: says(running(), running(), passing()),
			want:     green(), ops: readsOf(1, 3, 1), waits: polls(2, verdictPoll),
		},
		{
			name:     "a run in progress with no check yet keeps the wait past 20 seconds",
			verdicts: says(started, started, started, started, started, started, passing()),
			want:     green(), ops: readsOf(1, 7, 1), waits: polls(6, verdictPoll),
		},
		{
			name:     "every check concluded while a run is in progress",
			verdicts: says(finishing, passing()),
			want:     green(), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			name:     "every check concluded while a run awaits a person",
			verdicts: says(approving),
			want:     concluded(landing.WaitGreen, approving, 1, 0), ops: readsOf(1, 1, 1),
		},
		{
			name:     "nothing reported for 20 seconds",
			verdicts: says(forge.Verdict{}),
			want:     concluded(landing.WaitNothingReported, forge.Verdict{}, 0, 0),
			ops:      readsOf(1, 5, 1), waits: polls(4, verdictPoll),
		},
		{
			name:     "nothing reported, then a check appears within the retry",
			verdicts: says(forge.Verdict{}, forge.Verdict{}, passing()),
			want:     green(), ops: readsOf(1, 3, 1), waits: polls(2, verdictPoll),
		},
	}
}

// lintFailed is the failed lint check of the ci workflow, which the trunk does not require.
func lintFailed() forge.Check {
	return forge.Check{Name: "lint", Bucket: forge.BucketFail, Workflow: "ci", Link: "https://example.com/acme/widgets/runs/7"}
}

// redRows are the verdict waits where a check failed: they end once its log is settled, or at
// the deadline.
func redRows() []vigilRow {
	// unfinished is lint failed while test still runs: the run has not completed, so the
	// forge holds lint's log back.
	unfinished := forge.Verdict{
		Checks:    []forge.Check{lintFailed(), checked("test", forge.BucketPending)},
		Runs:      ran(forge.RunInProgress),
		FailedLog: forge.FailedLog{Check: "lint", State: forge.FailedLogInProgress},
	}
	completed := forge.Verdict{
		Checks:    []forge.Check{lintFailed(), checked("test", forge.BucketPass)},
		Runs:      ran(forge.RunCompleted),
		FailedLog: forge.FailedLog{Check: "lint", Text: "lint: unused variable", State: forge.FailedLogRead},
	}
	gone := forge.Verdict{
		Checks:    unfinished.Checks,
		Runs:      ran(forge.RunInProgress),
		FailedLog: forge.FailedLog{Check: "lint", State: forge.FailedLogUnreadable},
	}
	status := forge.Verdict{
		Checks:    []forge.Check{{Name: "scan", Bucket: forge.BucketFail}, checked("test", forge.BucketPending)},
		Runs:      ran(forge.RunInProgress),
		FailedLog: forge.FailedLog{Check: "scan", State: forge.FailedLogNoRun},
	}

	return []vigilRow{
		{
			name:     "a check failed while another runs, then its run completed",
			verdicts: says(unfinished, completed),
			want:     concluded(landing.WaitRed, completed, 1, 0), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			// A third poll would end three seconds past the deadline.
			name:     "a check failed and its run had not completed at the deadline",
			verdicts: says(unfinished), left: 12 * time.Second,
			want: concluded(landing.WaitRed, unfinished, 0, 0), ops: readsOf(1, 3, 1), waits: polls(2, verdictPoll),
		},
		{
			name: "a check failed and the deadline ended inside the read after it",
			verdicts: []forgetest.Answer[forge.Verdict]{
				{Value: unfinished}, {Err: fmt.Errorf("gh: %w", context.DeadlineExceeded)},
			},
			want: concluded(landing.WaitRed, unfinished, 0, 0), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			name:     "a check failed while its run goes on, then its log could not be read",
			verdicts: says(unfinished, gone),
			want:     concluded(landing.WaitRed, gone, 0, 0), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			name:     "a check failed and its log was read",
			verdicts: says(completed),
			want:     concluded(landing.WaitRed, completed, 1, 0), ops: readsOf(1, 1, 1),
		},
		{
			name:     "a check failed and its log could not be read while its run goes on",
			verdicts: says(gone),
			want:     concluded(landing.WaitRed, gone, 0, 0), ops: readsOf(1, 1, 1),
		},
		{
			name:     "a check failed that no run stands behind while a run goes on",
			verdicts: says(status),
			want:     concluded(landing.WaitRed, status, 0, 0), ops: readsOf(1, 1, 1),
		},
	}
}

func TestAwaitVerdict_WhenACheckFailed_EndsRedOnceItsLogIsSettled(t *testing.T) {
	t.Parallel()

	rows := redRows()
	for i := range rows {
		rows[i].heads = shows(clean())
	}

	runVigils(t, rows)
}

// heldRows are the verdict waits a check that reported and still runs holds, though no run does.
func heldRows() []vigilRow {
	lingering := forge.Verdict{Checks: running().Checks, Runs: ran(forge.RunCompleted)}
	external := forge.Verdict{Checks: []forge.Check{{Name: "deploy", Bucket: forge.BucketPending}}}
	deployed := forge.Verdict{Checks: []forge.Check{{Name: "deploy", Bucket: forge.BucketPass}}}

	return []vigilRow{
		{
			name:     "a check pending after every run completed, then green",
			verdicts: says(lingering, passing()),
			want:     green(), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			name:     "a check pending that no run stands behind, then green",
			verdicts: says(external, external, deployed),
			want:     concluded(landing.WaitGreen, deployed, 1, 0), ops: readsOf(1, 3, 1), waits: polls(2, verdictPoll),
		},
	}
}

func TestAwaitVerdict_WhenTrunkRequiresNoCheck_WaitsUntilNothingStillRuns(t *testing.T) {
	t.Parallel()

	rows := slices.Concat(everyCheckRows(), heldRows())
	for i := range rows {
		rows[i].heads = shows(clean())
	}

	runVigils(t, rows)
}

// requiredRows are the verdict waits where the trunk requires the checks lint and test.
func requiredRows() []vigilRow {
	lint := needed("lint", forge.BucketPass)
	absent := forge.Verdict{Checks: []forge.Check{lint}, MissingRequired: []string{"test"}, Runs: ran(forge.RunQueued)}
	both := requiredPassing()
	optional := forge.Verdict{
		Checks: []forge.Check{lint, needed("test", forge.BucketSkipped), checked("docs", forge.BucketPending)},
		Runs:   ran(forge.RunInProgress),
	}
	decided := concluded(landing.WaitGreen, optional, 1, 1)
	decided.Deciding = optional.Checks[:2]
	never := forge.Verdict{Checks: []forge.Check{lint}, MissingRequired: []string{"test"}, Runs: ran(forge.RunCompleted)}
	unapproved := forge.Verdict{MissingRequired: []string{"lint", "test"}, Runs: ran(forge.RunAwaitingPerson)}
	silence := forge.Verdict{MissingRequired: []string{"lint", "test"}}
	held := forge.Verdict{Checks: []forge.Check{lint, needed("test", forge.BucketPending)}, Runs: ran(forge.RunCompleted)}
	holding := concluded(landing.WaitPending, held, 1, 0)
	holding.Stage = landing.StageVerdict
	aside := forge.Verdict{Checks: []forge.Check{checked("docs", forge.BucketPass)}, MissingRequired: []string{"test"}}
	unmet := concluded(landing.WaitNeverReported, aside, 0, 0)
	unmet.Deciding = nil

	return []vigilRow{
		{
			name:     "required name absent beside a check that is not required, no run going",
			verdicts: says(aside),
			want:     unmet, ops: readsOf(1, 1, 1),
		},
		{
			// A third poll would end three seconds past the deadline.
			name:     "required check pending after every run completed, until the deadline",
			verdicts: says(held), left: 12 * time.Second,
			want: holding, ops: readsOf(1, 3, 1), waits: polls(2, verdictPoll),
		},
		{
			name:     "required name absent with a queued run, then concluded",
			verdicts: says(absent, both),
			want:     concluded(landing.WaitGreen, both, 2, 0), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			name:     "every required name concluded while another check runs",
			verdicts: says(optional),
			want:     decided, ops: readsOf(1, 1, 1),
		},
		{
			name:     "required name absent, run completed",
			verdicts: says(never),
			want:     concluded(landing.WaitNeverReported, never, 1, 0), ops: readsOf(1, 1, 1),
		},
		{
			name:     "required name absent, run awaiting a person",
			verdicts: says(unapproved),
			want:     concluded(landing.WaitNeverReported, unapproved, 0, 0), ops: readsOf(1, 1, 1),
		},
		{
			name:     "nothing reported for 20 seconds",
			verdicts: says(silence),
			want:     concluded(landing.WaitNothingReported, silence, 0, 0),
			ops:      readsOf(1, 5, 1), waits: polls(4, verdictPoll),
		},
	}
}

func TestAwaitVerdict_WhenTrunkRequiresChecks_WaitsForEveryRequiredName(t *testing.T) {
	t.Parallel()

	rows := requiredRows()
	for i := range rows {
		rows[i].heads = shows(clean())
		rows[i].required = []string{"lint", "test"}
	}

	runVigils(t, rows)
}

func TestAwaitVerdict_WhenChecksStillRunAtTheDeadline_EndsPendingWithThem(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		began := time.Now()
		// One head poll, then two verdict polls; a third would end three seconds past the deadline.
		v := newVigil(t, forgetest.Script{Head: shows(stale(), clean()), Verdict: says(running())}, 14*time.Second)

		got := v.await(t)

		want := concluded(landing.WaitPending, running(), 0, 0)
		want.Stage = landing.StageVerdict
		want.Since = began.Add(headPoll)

		assert.Check(t, is.DeepEqual(got, want))
		assert.Check(t, is.DeepEqual(v.opsOf(), readsOf(2, 3, 1)))
		assert.Check(t, is.DeepEqual(v.pauses(), slices.Concat(polls(1, headPoll), polls(2, verdictPoll))))
	})
}

func TestAwaitVerdict_WhenChecksRunWithFiveSecondsLeft_EndsPendingAfterOneVerdictRead(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		v := newVigil(t, forgetest.Script{Head: shows(clean()), Verdict: says(running())}, 5*time.Second)

		got := v.await(t)

		assert.Check(t, is.Equal(got.Outcome, landing.WaitPending))
		assert.Check(t, is.Equal(got.Stage, landing.StageVerdict))

		verdicts := slices.DeleteFunc(v.opsOf(), func(op forgetest.Op) bool { return op != forgetest.OpVerdict })
		assert.Check(t, is.Len(verdicts, 1))
		// The one poll ends on the deadline, and is taken.
		assert.Check(t, is.DeepEqual(v.pauses(), polls(1, verdictPoll)))
		assert.Check(t, !v.ended.After(v.deadline), "AwaitVerdict() returned at %v, want it no later than the deadline %v",
			v.ended, v.deadline)
	})
}

func TestAwaitVerdict_WhenDeadlineEndedDuringTheWait_ReturnsTheEarlierMergeState(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		script := forgetest.Script{
			Head:    shows(clean(), tip(forge.MergeBlockedByReview, "BLOCKED")),
			Verdict: says(passing()),
			// The verdict read takes the whole time the wait had left, and answers once the context ended.
			OnRead: func(call forgetest.Call) {
				if call.Op == forgetest.OpVerdict {
					time.Sleep(bound)
					synctest.Wait()
				}
			},
		}
		v := newVigil(t, script, bound)

		got := v.await(t)

		assert.Check(t, is.DeepEqual(got, green(), ignoreSince()))
	})
}

// cutShort is one wait whose deadline ends inside a read or a poll.
type cutShort struct {
	name     string
	want     landing.WaitStage
	heads    []forgetest.Answer[forge.Head]
	verdicts []forgetest.Answer[forge.Verdict]
	// left is the time before the deadline; zero is the whole bound.
	left time.Duration
	// poll is whether the deadline ends inside the first poll, and is not known ahead of it.
	poll bool
}

func cutShortWaits() []cutShort {
	ended := fmt.Errorf("gh: %w", context.DeadlineExceeded)
	lateHead := forgetest.Answer[forge.Head]{Err: ended}
	lateVerdict := []forgetest.Answer[forge.Verdict]{{Err: ended}}

	return []cutShort{
		{name: "inside the head read", want: landing.StageHead, heads: []forgetest.Answer[forge.Head]{lateHead}},
		{name: "inside the head poll", want: landing.StageHead, heads: shows(stale()), poll: true},
		{name: "inside the merge state read", want: landing.StageMergeState, heads: append(shows(unknown()), lateHead)},
		{name: "inside the merge state poll", want: landing.StageMergeState, heads: shows(unknown()), poll: true},
		{name: "inside the verdict read", want: landing.StageVerdict, heads: shows(clean()), verdicts: lateVerdict},
		{
			name: "inside the verdict poll", want: landing.StageVerdict,
			heads: shows(clean()), verdicts: says(running()), poll: true,
		},
		{
			name: "inside the merge state read after the checks", want: landing.StageMergeState,
			heads: append(shows(clean()), lateHead), verdicts: says(passing()),
		},
		{
			// Seven seconds leave room for one poll and then for this read: the wait was pending
			// on the checks before the read, and stays so.
			name: "inside the merge state read after checks that were still running", want: landing.StageVerdict,
			heads: append(shows(clean()), lateHead), verdicts: says(running()), left: 7 * time.Second,
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

				assert.Check(t, is.Equal(got.Outcome, landing.WaitPending))
				assert.Check(t, is.Equal(got.Stage, tt.want))
			})
		})
	}
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()

				if tt.after == 0 {
					cancel()
				} else {
					time.AfterFunc(tt.after, cancel)
				}

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
			// Seven seconds leave room for one poll and then for this read, but not for a second poll.
			name: "the merge state after checks that were still running", want: landing.StageMergeState,
			heads: append(shows(clean()), failed), verdicts: says(running()), left: 7 * time.Second,
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
