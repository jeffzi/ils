package landing_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
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
)

// epoch is the time every fake clock starts at.
var epoch = time.Date(2026, time.March, 3, 9, 0, 0, 0, time.UTC)

// ignoreSince leaves out the time the verdict wait began.
var ignoreSince = cmpopts.IgnoreFields(landing.Waited{}, "Since")

// orBound returns left, or the whole bound when left is zero.
func orBound(left time.Duration) time.Duration {
	if left == 0 {
		return bound
	}

	return left
}

// fakeClock is a clock that moves only when it is told to wait, and keeps each wait it was asked for.
type fakeClock struct {
	now time.Time
	// err, when set, is what every wait returns in place of passing the time.
	err   error
	waits []time.Duration
}

// Now returns the time the waits so far have added up to.
func (c *fakeClock) Now() time.Time { return c.now }

// Wait keeps d and passes it at once, or returns the clock's error.
func (c *fakeClock) Wait(_ context.Context, d time.Duration) error {
	c.waits = append(c.waits, d)

	if c.err != nil {
		return c.err
	}

	c.now = c.now.Add(d)

	return nil
}

// bounded is a context whose deadline is a time of the fake clock; it never ends on its own.
type bounded struct {
	context.Context

	deadline time.Time
}

// Deadline returns the fake clock's time at which the wait must have ended.
func (b bounded) Deadline() (time.Time, bool) { return b.deadline, true }

// vigil is one wait: the forge it reads, the clock it waits on, and the deadline it runs under.
type vigil struct {
	ctx      context.Context
	forge    *forgetest.Fake
	clock    *fakeClock
	deadline time.Time
	deps     landing.Deps
}

// newVigilOn returns a wait on clock that has left before its deadline and reads a forge that
// answers from script.
func newVigilOn(t *testing.T, clock *fakeClock, script forgetest.Script, left time.Duration) *vigil {
	t.Helper()

	fake := forgetest.New(t, script)
	deadline := clock.now.Add(left)

	return &vigil{
		ctx:      bounded{Context: t.Context(), deadline: deadline},
		forge:    fake,
		clock:    clock,
		deadline: deadline,
		deps:     landing.Deps{Forge: fake, Clock: clock},
	}
}

// newVigil is newVigilOn with a clock at epoch.
func newVigil(t *testing.T, script forgetest.Script, left time.Duration) *vigil {
	t.Helper()

	return newVigilOn(t, &fakeClock{now: epoch}, script, left)
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

// shows returns a head sequence that answers with each of heads in turn.
func shows(heads ...forge.Head) []forgetest.Answer[forge.Head] {
	answers := make([]forgetest.Answer[forge.Head], 0, len(heads))
	for _, head := range heads {
		answers = append(answers, forgetest.Answer[forge.Head]{Value: head})
	}

	return answers
}

// says returns a verdict sequence that answers with each of verdicts in turn.
func says(verdicts ...forge.Verdict) []forgetest.Answer[forge.Verdict] {
	answers := make([]forgetest.Answer[forge.Verdict], 0, len(verdicts))
	for _, verdict := range verdicts {
		answers = append(answers, forgetest.Answer[forge.Verdict]{Value: verdict})
	}

	return answers
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

// polls returns times waits of every.
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
// required, and fails the test when it returns an error.
func (v *vigil) await(t *testing.T, required ...string) landing.Waited {
	t.Helper()

	got, err := landing.AwaitVerdict(v.ctx, v.deps, pushedTip(required...))
	if err != nil {
		t.Fatalf("AwaitVerdict() error = %v, want nil", err)
	}

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
	// required names the checks the trunk requires.
	required []string
	// ops are the forge reads the wait makes, in order.
	ops []forgetest.Op
	// waits are the waits the clock is asked for, in order.
	waits []time.Duration
	// want is what the wait returns, but for the time its verdict wait began.
	want landing.Waited
	// left is the time before the deadline; zero is the whole bound.
	left time.Duration
}

// runVigils runs each row as a test of its own: the wait ends as the row wants, having read the forge and
// waited on the clock exactly as the row says.
func runVigils(t *testing.T, rows []vigilRow) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			v := newVigil(t, forgetest.Script{Head: tt.heads, Verdict: tt.verdicts}, orBound(tt.left))

			got := v.await(t, tt.required...)

			if diff := cmp.Diff(tt.want, got, ignoreSince, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("AwaitVerdict() mismatch (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tt.ops, v.opsOf(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("forge reads mismatch (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tt.waits, v.clock.waits, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("clock waits mismatch (-want +got):\n%s", diff)
			}
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
			// A third wait would end a second past the deadline.
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

	return []vigilRow{
		{
			name:  "unknown then clean",
			heads: shows(unknown(), clean()), verdicts: says(passing()),
			want: green(), ops: readsOf(2, 1, 1), waits: polls(1, mergePoll),
		},
		{
			name:  "unknown for 30 seconds",
			heads: shows(unknown()),
			want:  stillUnknown, ops: readsOf(16, 0, 0), waits: polls(15, mergePoll),
		},
		{
			// A third wait would end a second past the deadline.
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

// everyCheckRows are the verdict waits where the trunk names no required check.
func everyCheckRows() []vigilRow {
	started := forge.Verdict{Runs: ran(forge.RunInProgress)}
	finishing := forge.Verdict{Checks: passing().Checks, Runs: ran(forge.RunInProgress)}
	approving := forge.Verdict{Checks: passing().Checks, Runs: ran(forge.RunAwaitingPerson)}
	failing := forge.Verdict{
		Checks: []forge.Check{
			{Name: "lint", Bucket: forge.BucketFail, Workflow: "ci", Link: "https://example.com/acme/widgets/runs/7"},
			checked("test", forge.BucketPending),
		},
		Runs:      ran(forge.RunInProgress),
		FailedLog: forge.FailedLog{Check: "lint", Text: "lint: unused variable", State: forge.FailedLogRead},
	}

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
			name:     "a check failed while another runs",
			verdicts: says(failing),
			want:     concluded(landing.WaitRed, failing, 0, 0), ops: readsOf(1, 1, 1),
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

func TestAwaitVerdict_WithoutRequiredChecks_WaitsForEveryCheckAndRun(t *testing.T) {
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

	return []vigilRow{
		{
			// A third wait would end three seconds past the deadline.
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

func TestAwaitVerdict_WithRequiredChecks_WaitsForEveryRequiredName(t *testing.T) {
	t.Parallel()

	rows := requiredRows()
	for i := range rows {
		rows[i].heads = shows(clean())
		rows[i].required = []string{"lint", "test"}
	}

	runVigils(t, rows)
}

func TestAwaitVerdict_WhenChecksStillRunAtTheDeadline_EndsPendingWithThemAndWhenTheWaitBegan(t *testing.T) {
	t.Parallel()
	// A third wait would end three seconds past the deadline.
	v := newVigil(t, forgetest.Script{Head: shows(clean()), Verdict: says(running())}, 12*time.Second)

	got := v.await(t)

	want := concluded(landing.WaitPending, running(), 0, 0)
	want.Stage = landing.StageVerdict
	want.Since = epoch

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("AwaitVerdict() mismatch (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff(readsOf(1, 3, 1), v.opsOf()); diff != "" {
		t.Errorf("forge reads mismatch (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff(polls(2, verdictPoll), v.clock.waits); diff != "" {
		t.Errorf("clock waits mismatch (-want +got):\n%s", diff)
	}
}

func TestAwaitVerdict_WhenChecksRunWithFiveSecondsLeft_EndsPendingAfterOneVerdictRead(t *testing.T) {
	t.Parallel()
	v := newVigil(t, forgetest.Script{Head: shows(clean()), Verdict: says(running())}, 5*time.Second)

	got := v.await(t)

	if got.Outcome != landing.WaitPending || got.Stage != landing.StageVerdict {
		t.Errorf("AwaitVerdict() = %s at %q, want %s at %q",
			got.Outcome, got.Stage, landing.WaitPending, landing.StageVerdict)
	}

	verdicts := slices.DeleteFunc(v.opsOf(), func(op forgetest.Op) bool { return op != forgetest.OpVerdict })
	if len(verdicts) != 1 {
		t.Errorf("verdict reads = %d, want 1", len(verdicts))
	}

	if v.clock.now.After(v.deadline) {
		t.Errorf("clock after AwaitVerdict() = %v, want it no later than the deadline %v", v.clock.now, v.deadline)
	}
}

func TestAwaitVerdict_WhenMergeStateChangedDuringTheWait_ReturnsTheStateReadAfterIt(t *testing.T) {
	t.Parallel()

	reviewed := tip(forge.MergeBlockedByReview, "BLOCKED")
	v := newVigil(t, forgetest.Script{Head: shows(clean(), reviewed), Verdict: says(passing())}, bound)

	got := v.await(t)

	if diff := cmp.Diff(stateOf(green(), reviewed), got, ignoreSince); diff != "" {
		t.Errorf("AwaitVerdict() mismatch (-want +got):\n%s", diff)
	}
}

func TestAwaitVerdict_WhenDeadlineEndedDuringTheWait_ReturnsTheEarlierMergeState(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: epoch}
	script := forgetest.Script{
		Head:    shows(clean(), tip(forge.MergeBlockedByReview, "BLOCKED")),
		Verdict: says(passing()),
		// The verdict read takes the whole time the wait had left.
		OnRead: func(call forgetest.Call) {
			if call.Op == forgetest.OpVerdict {
				clock.now = clock.now.Add(bound)
			}
		},
	}
	v := newVigilOn(t, clock, script, bound)

	got := v.await(t)

	if diff := cmp.Diff(green(), got, ignoreSince); diff != "" {
		t.Errorf("AwaitVerdict() mismatch (-want +got):\n%s", diff)
	}
}

// cutShort is one wait whose deadline ends inside a read or a poll.
type cutShort struct {
	// poll is what the clock's waits return; nil lets them pass the time.
	poll     error
	name     string
	want     landing.WaitStage
	heads    []forgetest.Answer[forge.Head]
	verdicts []forgetest.Answer[forge.Verdict]
	// left is the time before the deadline; zero is the whole bound.
	left time.Duration
}

func cutShortWaits() []cutShort {
	ended := fmt.Errorf("gh: %w", context.DeadlineExceeded)
	lateHead := forgetest.Answer[forge.Head]{Err: ended}
	lateVerdict := []forgetest.Answer[forge.Verdict]{{Err: ended}}

	return []cutShort{
		{name: "inside the head read", want: landing.StageHead, heads: []forgetest.Answer[forge.Head]{lateHead}},
		{name: "inside the head poll", want: landing.StageHead, heads: shows(stale()), poll: context.DeadlineExceeded},
		{name: "inside the merge state read", want: landing.StageMergeState, heads: append(shows(unknown()), lateHead)},
		{
			name: "inside the merge state poll", want: landing.StageMergeState,
			heads: shows(unknown()), poll: context.DeadlineExceeded,
		},
		{name: "inside the verdict read", want: landing.StageVerdict, heads: shows(clean()), verdicts: lateVerdict},
		{
			name: "inside the verdict poll", want: landing.StageVerdict,
			heads: shows(clean()), verdicts: says(running()), poll: context.DeadlineExceeded,
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

func TestAwaitVerdict_WhenDeadlineEndsInsideAReadOrAPoll_EndsPendingAtThatStage(t *testing.T) {
	t.Parallel()

	for _, tt := range cutShortWaits() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clock := &fakeClock{now: epoch, err: tt.poll}
			v := newVigilOn(t, clock, forgetest.Script{Head: tt.heads, Verdict: tt.verdicts}, orBound(tt.left))

			got := v.await(t)

			if got.Outcome != landing.WaitPending || got.Stage != tt.want {
				t.Errorf("AwaitVerdict() = %s at %q, want %s at %q", got.Outcome, got.Stage, landing.WaitPending, tt.want)
			}
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
			v := newVigil(t, forgetest.Script{Head: tt.heads, Verdict: tt.verdicts}, orBound(tt.left))

			_, err := landing.AwaitVerdict(v.ctx, v.deps, pushedTip())

			got, ok := errors.AsType[*landing.WaitError](err)
			if !ok {
				t.Fatalf("AwaitVerdict() error = %v, want a *landing.WaitError", err)
			}

			if got.Stage != tt.want {
				t.Errorf("WaitError.Stage = %q, want %q", got.Stage, tt.want)
			}

			if !errors.Is(err, cause) {
				t.Errorf("AwaitVerdict() error = %v, want it to wrap %v", err, cause)
			}
		})
	}
}

func TestAwaitVerdict_AsksAboutThePushedCommitAndTheRequiredChecks(t *testing.T) {
	t.Parallel()

	required := []string{"lint", "test"}
	v := newVigil(t, forgetest.Script{Head: shows(clean()), Verdict: says(requiredPassing())}, bound)

	v.await(t, required...)

	head := forgetest.Call{
		Op: forgetest.OpHead, Repo: widgets(false, true),
		Head: forge.HeadQuery{Number: opened, Required: required},
	}
	verdict := forgetest.Call{
		Op: forgetest.OpVerdict, Repo: widgets(false, true),
		Query: forge.VerdictQuery{SHA: pushedSHA, Number: opened, Required: required},
	}

	if diff := cmp.Diff([]forgetest.Call{head, verdict, head}, v.forge.Calls()); diff != "" {
		t.Errorf("forge calls mismatch (-want +got):\n%s", diff)
	}
}
