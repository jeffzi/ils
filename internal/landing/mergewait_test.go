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

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

const (
	readMessage = "the pull request's message"

	nextReviewStop  = "Next: request a review, and stop: a review is not the agent's to give\n"
	nextMissingStop = "Next: report that the required check has no check on the head, and stop\n"
	nextBlockedStop = "Next: report that GitHub blocks the merge, and stop\n"
	nextNothingHead = "Next: report that no check or status reported for the head, and stop\n"

	// worded is the title pull request 12 holds when its message is read after the wait.
	worded = "docs(readme): tidy the wording"
)

// waiting is merging with a forge that shows pull request 12 clean at the tip and answers the
// checks with each of verdicts in turn. It has no answer for the message read.
func waiting(verdicts ...forge.Verdict) forgeFor {
	return merging().showing(inState(forge.MergeClean, "CLEAN")).with(func(script *forgetest.Script, _ string) {
		script.Verdict = says(verdicts...)
	})
}

// ruling returns build with a trunk whose rules allow squash merges and require the checks names.
func (build forgeFor) ruling(names ...string) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		rules := forge.Protection{RequiredChecks: names, SquashAllowed: true}
		script.TrunkProtection = []forgetest.Answer[forge.Protection]{{Value: rules}}
	})
}

// saying returns build with a forge whose message of pull request 12 answers as answer.
func (build forgeFor) saying(answer forgetest.Answer[forge.Message]) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.Message = []forgetest.Answer[forge.Message]{answer}
	})
}

// failingChecks returns build with a read of the checks that fails with err.
func (build forgeFor) failingChecks(err error) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.Verdict = []forgetest.Answer[forge.Verdict]{{Err: err}}
	})
}

// messageTaking returns build with a message read that lets d of fake time pass.
func (build forgeFor) messageTaking(d time.Duration) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.OnRead = func(call forgetest.Call) {
			if call.Op == forgetest.OpMessage {
				time.Sleep(d)
			}
		}
	})
}

// titledAfter is a message of pull request 12 titled worded, with body and trailers.
func titledAfter(body string, trailers ...string) forgetest.Answer[forge.Message] {
	return forgetest.Answer[forge.Message]{Value: forge.Message{Title: worded, Body: body, Trailers: trailers}}
}

// mergeDetail returns the payload of `ils merge` on pull request 12 once its checks were read, but
// for the sha: the verdict's name, the merge state in the words of ils, and the deciding checks.
func mergeDetail(verdict forge.VerdictState, state forge.MergeState, checks ...forge.Check) *result.Merge {
	push := detail(verdict, state, checks...)

	return &result.Merge{
		URL: push.URL, Number: push.Number, Verdict: push.Verdict, MergeState: push.MergeState, Checks: push.Checks,
	}
}

// composed returns the payload of a green run on a clean pull request 12 with subject and body.
func composed(subject, body string) *result.Merge {
	payload := mergeDetail(forge.VerdictGreen, forge.MergeClean, passing().Checks...)
	payload.Subject = subject
	payload.Body = body

	return payload
}

// made returns payload as that of a run that made the merge and ended on the trunk.
func made(payload *result.Merge) *result.Merge {
	payload.Merged = true
	payload.Ending = result.EndingTrunk

	return payload
}

// landing returns build with a forge whose landing answers land and whose branch delete answers
// remove; nil is a call that succeeds.
func (build forgeFor) landing(land, remove error) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.Land = []error{land}
		script.DeleteBranch = []error{remove}
	})
}

// withLines returns the refusal text with lines printed between its reason and its `Next:` line.
func withLines(text, lines string) string {
	reason, next, _ := strings.Cut(text, "\n")

	return reason + "\n" + lines + next
}

// mergeRun is one run of `ils merge` on a scene `setting` describes.
type mergeRun struct {
	// ended is the fake time at which Merge returned.
	ended time.Time
	*scene
	tip string
	// before is everything the run could change, as it was before the run.
	before string
	got    result.Result
}

// setting is the scene of a run of `ils merge`; by default its branch passes every check before the
// wait.
type setting struct {
	// arrange changes the repository before the scene reads it; nil changes nothing.
	arrange func(*gittest.Fixture)
	// rewire changes how the scene reaches git once it is built; nil changes nothing.
	rewire func(t *testing.T, s *scene)
	// landed runs once a landing the forge lets through has squashed the tip onto origin's trunk.
	landed func(t *testing.T, f *gittest.Fixture)
	script forgeFor
	// left is the time before the command's deadline; zero is the whole bound.
	left time.Duration
	// unbounded runs under a context with no deadline, which left then has no bearing on.
	unbounded bool
}

// runMergeIn runs `ils merge` inside the test's bubble with the forge script builds and a deadline
// left of fake time away.
func runMergeIn(t *testing.T, script forgeFor, left time.Duration) mergeRun {
	t.Helper()

	return runMergeOn(t, setting{script: script, left: left})
}

// runMergeOn runs `ils merge` inside the test's bubble on the scene set describes, under a deadline
// unless set is unbounded. A landing the forge lets through squashes the tip onto origin's trunk, as
// GitHub would.
func runMergeOn(t *testing.T, set setting) mergeRun {
	t.Helper()

	s := newScene(t, set.arrange, forgetest.Script{}, false)
	tip := s.fixture.Git("rev-parse", topic)

	script := set.script(tip)
	if len(script.Land) > 0 && script.Land[0] == nil {
		script.OnLand = squashOnLand(t, s.fixture, tip, set.landed)
	}

	s.useForge(t, script)

	if set.rewire != nil {
		set.rewire(t, s)
	}

	ctx := t.Context()
	if !set.unbounded {
		bounded, cancel := context.WithDeadline(ctx, time.Now().Add(orBound(set.left)))
		t.Cleanup(cancel)

		ctx = bounded
	}

	before := s.written()

	got := landing.Merge(ctx, s.deps, mergeRequest())

	return mergeRun{scene: s, got: got, tip: tip, before: before, ended: time.Now()}
}

// squashOnLand returns a landing hook that squashes tip onto origin's trunk with the landing's subject
// and body, then runs landed when it is set.
func squashOnLand(t *testing.T, f *gittest.Fixture, tip string, landed func(*testing.T, *gittest.Fixture)) func(forgetest.Call) {
	return func(call forgetest.Call) {
		f.SquashOnOrigin(tip, call.Landing.Subject, call.Landing.Body)

		if landed != nil {
			landed(t, f)
		}
	}
}

// waitRow is one run of `ils merge` that passes every check before the wait, and how it ends.
type waitRow struct {
	script forgeFor
	// payload is the merge payload the run carries, but for the sha.
	payload *result.Merge
	name    string
	// text is the whole text of the result, with tipMark where the sha of the tip is printed.
	text    string
	outcome result.Outcome
	// left is the time before the command's deadline; zero is the whole bound.
	left time.Duration
}

// runWaits runs each row as a test in a bubble of its own: the run carries the row's payload,
// writes nothing, and ends as the row says.
func runWaits(t *testing.T, rows []waitRow) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				run := runMergeIn(t, tt.script, tt.left)

				want := *tt.payload
				want.SHA = run.tip
				assert.Check(t, is.DeepEqual(mergePayload(run.got), &want, cmpopts.EquateEmpty()))
				assert.Check(t, is.Equal(run.written(), run.before))
				assertStoppedBy(t, run.got, tt.outcome, mergeRequest())

				text, _ := shown(t, run.got, run.gitDir)
				assert.Check(t, is.Equal(text, mark(tt.text, run.tip)))
			})
		})
	}
}

// waitVerdicts are the runs of `ils merge` whose wait ends in a verdict that stops the merge.
func waitVerdicts() []waitRow {
	red := forge.Verdict{
		Checks: []forge.Check{
			{Name: "lint", Bucket: forge.BucketFail, Workflow: "ci", Link: lintURL}, checked("test", forge.BucketPass),
		},
		Runs: ran(forge.RunCompleted),
		// The log is one line longer than the excerpt may be.
		FailedLog: forge.FailedLog{Check: "lint", Text: steps(1, 61), State: forge.FailedLogRead},
	}
	lint := needed("lint", forge.BucketPass)
	never := forge.Verdict{Checks: []forge.Check{lint}, MissingRequired: []string{"test"}, Runs: ran(forge.RunCompleted)}
	nothing := "Refused: no check or status reported for " + tipMark + " on pull request #12\n" + cleanLine + nextNothingHead
	apart := func(state forge.MergeState, raw, reason string) waitRow {
		return waitRow{
			name: string(state), script: waiting().showing(inState(state, raw)), outcome: result.Refused,
			text:    "Refused: " + reason + "\nGitHub: " + raw + " (" + string(state) + ")\n" + nextTrunk,
			payload: mergeDetail(forge.VerdictNone, state),
		}
	}

	return []waitRow{
		{
			name: "a deciding check failed", script: waiting(red).showing(inState(forge.MergeBlockedOther, "UNSTABLE")),
			outcome: result.Refused, payload: mergeDetail(forge.VerdictRed, forge.MergeBlockedOther, red.Checks...),
			text: "Refused: the check lint failed\nCheck lint failed: " + lintURL + "\n" +
				"GitHub: UNSTABLE (blocked-other)\n" + steps(2, 61) + nextRed,
		},
		apart(forge.MergeConflicting, "DIRTY", "the pull request conflicts with the trunk"),
		{
			name:    "a required check never reported",
			script:  waiting(never).ruling("lint", "test").showing(inState(forge.MergeBlockedByMissingCheck, "BLOCKED")),
			outcome: result.Refused, payload: mergeDetail(forge.VerdictGreen, forge.MergeBlockedByMissingCheck, lint),
			text: "Refused: a required check never reported\nRequired and never reported: test\n" +
				"GitHub: BLOCKED (blocked-by-missing-check)\n" + nextNever,
		},
		{
			name:    "nothing reported in a repository with CI on its trunk",
			script:  waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{Value: true}),
			outcome: result.Refused, text: nothing, payload: mergeDetail(forge.VerdictNone, forge.MergeClean),
		},
		{
			// A trunk that requires checks has CI, so the forge has no answer for the CI read.
			name:    "nothing reported though the trunk requires checks",
			script:  waiting(forge.Verdict{MissingRequired: []string{"lint", "test"}}).ruling("lint", "test"),
			outcome: result.Refused, text: nothing, payload: mergeDetail(forge.VerdictNone, forge.MergeClean),
		},
	}
}

// pendingWaits are the runs of `ils merge` whose wait, on a deadline 20 seconds before the
// command's, ends before the forge had an answer.
func pendingWaits() []waitRow {
	ended := fmt.Errorf("gh: %w", context.DeadlineExceeded)
	cut := func(read string) string {
		return "The time limit ended while reading " + read + "; nothing more will change\n" + cleanLine + nextPush
	}

	return []waitRow{
		{
			// The head catches up after one poll of two seconds, which the count leaves out; then two
			// polls of five seconds pass, and a third would end three seconds past the wait's deadline.
			name: "a check still runs", left: 34 * time.Second,
			script: waiting(running()).showing(stale(), inState(forge.MergeClean, "CLEAN")), outcome: result.Pending,
			text:    "Still running after 10 seconds: test\n" + cleanLine + nextPush,
			payload: mergeDetail(forge.VerdictPending, forge.MergeClean, running().Checks...),
		},
		{
			name:    "the deadline ends in the CI read",
			script:  waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{Err: ended}),
			outcome: result.Pending, text: cut(readCI), payload: mergeDetail(forge.VerdictNone, forge.MergeClean),
		},
	}
}

// waitBreaks are the runs of `ils merge` whose read during the wait fails.
func waitBreaks() []waitRow {
	failed := errors.New("HTTP 502")
	row := func(read string, script forgeFor) waitRow {
		return waitRow{
			name: read, script: script, outcome: result.Broke, text: unreadText(mergeCommand(), read, "HTTP 502"),
			payload: mergeDetail(forge.VerdictNone, forge.MergeUnknown),
		}
	}
	unread := row(readCI, waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{Err: failed}))
	unread.payload = mergeDetail(forge.VerdictNone, forge.MergeClean)

	return []waitRow{
		row(readChecks, waiting().failingChecks(failed)),
		unread,
	}
}

func TestMerge_WhenTheWaitEnds_StopsAsPushReportsItsVerdict(t *testing.T) {
	t.Parallel()

	runWaits(t, slices.Concat(waitVerdicts(), pendingWaits(), waitBreaks()))
}

// afterWaitStops are the runs of `ils merge` whose checks are green and which stop on what they
// read after the wait.
func afterWaitStops() []waitRow {
	greenThen := func(state forge.MergeState, raw, text string) waitRow {
		return waitRow{
			name:    string(state),
			script:  waiting(passing()).showing(inState(forge.MergeClean, "CLEAN"), inState(state, raw)),
			outcome: result.Refused, text: text, payload: mergeDetail(forge.VerdictGreen, state, passing().Checks...),
		}
	}
	reviewed := inState(forge.MergeBlockedByReview, "BLOCKED")
	// The forge reports no required check missing, so the wait ends green and GitHub's state decides.
	// The head has checks named lint and test; named is the line that names the required checks.
	missing := func(name, named string, required ...string) waitRow {
		row := greenThen(forge.MergeBlockedByMissingCheck, "BLOCKED",
			"Refused: a check the trunk requires has no check on "+tipMark+"\n"+
				"Checks for "+tipMark+": 2 passed, 0 skipped\n"+named+"\n"+
				"GitHub: BLOCKED (blocked-by-missing-check)\n"+nextMissingStop)
		row.name = name
		row.script = waiting(requiredPassing()).ruling(required...).
			showing(inState(forge.MergeClean, "CLEAN"), inState(forge.MergeBlockedByMissingCheck, "BLOCKED"))
		row.payload = mergeDetail(forge.VerdictGreen, forge.MergeBlockedByMissingCheck, requiredPassing().Checks...)

		return row
	}

	return []waitRow{
		greenThen(forge.MergeDraft, "DRAFT", withLines(draftText, countLine+"GitHub: DRAFT (draft)\n")),
		greenThen(forge.MergeBlockedByReview, "BLOCKED",
			"Refused: pull request #12 requires a review\n"+countLine+"GitHub: BLOCKED (blocked-by-review)\n"+nextReviewStop),
		missing(string(forge.MergeBlockedByMissingCheck), "Required by the trunk with no check on "+tipMark+": build",
			"lint", "test", "build"),
		// GitHub expects a check ils cannot tell apart, so every required check is named.
		missing("blocked-by-missing-check with a check for each required one", "Required by the trunk: lint, test",
			"lint", "test"),
		greenThen(forge.MergeBlockedOther, "BLOCKED",
			"Refused: GitHub blocks pull request #12 for a reason ils does not read: "+pullURL+"\n"+
				countLine+"GitHub: BLOCKED (blocked-other)\n"+nextBlockedStop),
		{
			name: "blocked by review with nothing reported and no CI on the trunk",
			script: waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{}).
				showing(inState(forge.MergeClean, "CLEAN"), reviewed),
			outcome: result.Refused, payload: mergeDetail(forge.VerdictNone, forge.MergeBlockedByReview),
			text: "Refused: pull request #12 requires a review\n" + noCILine +
				"GitHub: BLOCKED (blocked-by-review)\n" + nextReviewStop,
		},
		{
			// Polls at two and four seconds find it unknown; a third would end a second past the
			// wait's deadline, 20 seconds before the command's.
			name: "the merge state is still unknown at the wait's deadline", left: 25 * time.Second,
			script:  waiting(passing()).showing(inState(forge.MergeClean, "CLEAN"), inState(forge.MergeUnknown, "UNKNOWN")),
			outcome: result.Pending, payload: mergeDetail(forge.VerdictGreen, forge.MergeUnknown, passing().Checks...),
			text: countLine + "GitHub has not said whether pull request #12 can merge yet\n" + nextPush,
		},
	}
}

// settledStops are the runs of `ils merge` whose checks are green and whose merge state, read again
// after the wait, stops the merge once a poll finds it or fails.
func settledStops() []waitRow {
	clean, unknown := inState(forge.MergeClean, "CLEAN"), inState(forge.MergeUnknown, "UNKNOWN")
	apart := func(state forge.MergeState, raw, reason string) waitRow {
		return waitRow{
			name:   string(state) + " after an unknown poll",
			script: waiting(passing()).showing(clean, unknown, inState(state, raw)), outcome: result.Refused,
			text:    "Refused: " + reason + "\n" + countLine + "GitHub: " + raw + " (" + string(state) + ")\n" + nextTrunk,
			payload: mergeDetail(forge.VerdictGreen, state, passing().Checks...),
		}
	}

	return []waitRow{
		apart(forge.MergeBehind, "BEHIND", "the pull request is behind the trunk"),
		apart(forge.MergeConflicting, "DIRTY", "the pull request conflicts with the trunk"),
		{
			name: "conflicting with nothing reported and no CI on the trunk",
			script: waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{}).
				showing(clean, inState(forge.MergeConflicting, "DIRTY")),
			outcome: result.Refused, payload: mergeDetail(forge.VerdictNone, forge.MergeConflicting),
			text: "Refused: the pull request conflicts with the trunk\n" + noCILine +
				"GitHub: DIRTY (conflicting)\n" + nextTrunk,
		},
		{
			name:    "a poll of the merge state fails",
			script:  waiting(passing()).showing(clean, unknown).thenFailingHead(errors.New("HTTP 502")),
			outcome: result.Broke, text: unreadText(mergeCommand(), readMergeState, "HTTP 502"),
			payload: mergeDetail(forge.VerdictGreen, forge.MergeUnknown, passing().Checks...),
		},
	}
}

// messageStops are the runs of `ils merge` whose checks are green on a clean pull request and which
// stop on its message, read after the wait, or on the time that read left.
func messageStops() []waitRow {
	green := mergeDetail(forge.VerdictGreen, forge.MergeClean, passing().Checks...)
	row := func(name string, answer forgetest.Answer[forge.Message], outcome result.Outcome, text string) waitRow {
		return waitRow{
			name: name, script: waiting(passing()).saying(answer), outcome: outcome, text: text, payload: green,
		}
	}
	late := row("the message read leaves fewer than 20 seconds", titledAfter(""), result.Pending,
		countLine+cleanLine+"Fewer than 20 seconds of the time limit were left for the merge, so nothing was written\n"+nextPush)
	// The wait's deadline is 80 seconds away: the read ends 19 seconds before the command's.
	late.script = late.script.messageTaking(81 * time.Second)
	late.payload = composed(worded+" (#12)", "")

	return []waitRow{
		row("the title no longer parses", forgetest.Answer[forge.Message]{Value: forge.Message{Title: "fix-thing"}},
			result.Refused, withLines(titleText("fix-thing"), countLine+cleanLine)),
		row("the message read fails", forgetest.Answer[forge.Message]{Err: errors.New("HTTP 502")},
			result.Broke, unreadText(mergeCommand(), readMessage, "HTTP 502")),
		row("the deadline ends in the message read",
			forgetest.Answer[forge.Message]{Err: fmt.Errorf("gh: %w", context.DeadlineExceeded)},
			result.Refused, overtimeText(mergeCommand(), readMessage)),
		late,
	}
}

func TestMerge_WhenWhatIsReadAfterTheWaitStopsTheMerge_EndsWithNothingWritten(t *testing.T) {
	t.Parallel()

	runWaits(t, slices.Concat(afterWaitStops(), settledStops(), messageStops()))
}

// readyRow is one run of `ils merge` that reaches the merge, and the payload it carries.
type readyRow struct {
	script forgeFor
	// payload is the merge payload the run carries, but for the sha.
	payload *result.Merge
	name    string
}

// readyRows are the runs of `ils merge` that reach the merge, each with the subject and body it
// composed from the message read after the wait.
func readyRows() []readyRow {
	const body = "Tidy the readme.\n\n# Notes\nKeep the heading."

	trailers := []string{"Co-authored-by: Sam Smith <sam@example.com>", "Signed-off-by: Kim Lee <kim@example.com>"}
	block := strings.Join(trailers, "\n")
	subject := worded + " (#12)"
	row := func(name string, answer forgetest.Answer[forge.Message], want string) readyRow {
		return readyRow{
			name: name, script: waiting(passing()).saying(answer).landing(nil, nil),
			payload: made(composed(subject, want)),
		}
	}
	// The wait's deadline is 80 seconds away: the read ends 20 seconds before the command's.
	inTime := row("the message read leaves 20 seconds", titledAfter(""), "")
	inTime.script = inTime.script.messageTaking(80 * time.Second)

	return []readyRow{
		row("a body with a line starting with # and trailers", titledAfter(body, trailers...), body+"\n\n"+block),
		row("an empty body and trailers", titledAfter("", trailers...), block),
		row("a body and no trailers", titledAfter(body), body),
		row("neither a body nor trailers", titledAfter(""), ""),
		inTime,
	}
}

func TestMerge_WhenEveryCheckBeforeTheMergeHolds_ComposesTheMessageFromThePullRequest(t *testing.T) {
	t.Parallel()

	for _, tt := range readyRows() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				run := runMergeIn(t, tt.script, 0)

				assertStoppedBy(t, run.got, result.Done, mergeRequest())

				want := *tt.payload
				want.SHA = run.tip
				assert.Check(t, is.DeepEqual(mergePayload(run.got), &want, cmpopts.EquateEmpty()))
			})
		})
	}
}

func TestMerge_WhenTheMergeStateIsUnknownAfterTheWait_ReadsItAgainEveryTwoSeconds(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		log := &readLog{}
		unknownHead := inState(forge.MergeUnknown, "UNKNOWN")
		build := waiting(passing()).showing(inState(forge.MergeClean, "CLEAN"), unknownHead, unknownHead,
			inState(forge.MergeClean, "CLEAN")).saying(titledAfter("")).landing(nil, nil)
		timed := func(tip string) forgetest.Script { return log.timed(build(tip)) }

		run := runMergeIn(t, timed, 0)

		want := made(composed(worded+" (#12)", ""))
		want.SHA = run.tip
		assert.Check(t, is.DeepEqual(mergePayload(run.got), want, cmpopts.EquateEmpty()))
		assert.Check(t, is.DeepEqual(log.pauses(run.ended), polls(2, mergePoll)))
	})
}

func TestMerge_WhenTheMergeStateStaysUnknownAfterTheWait_StopsOnceItsWindowPassed(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		log := &readLog{}
		build := waiting(passing()).showing(inState(forge.MergeClean, "CLEAN"), inState(forge.MergeUnknown, "UNKNOWN"))
		timed := func(tip string) forgetest.Script { return log.timed(build(tip)) }

		// The deadline leaves time for many more polls than the window allows.
		run := runMergeIn(t, timed, 10*time.Minute)

		assertStoppedBy(t, run.got, result.Pending, mergeRequest())

		text, reason := shown(t, run.got, run.gitDir)
		assert.Check(t, is.Equal(text,
			mark(countLine+"GitHub has not said whether pull request #12 can merge yet\n"+nextPush, run.tip)))
		assert.Check(t, is.Equal(reason, "GitHub did not say whether the pull request can merge within 30 seconds"))
		assert.Check(t, is.DeepEqual(log.pauses(run.ended), polls(15, mergePoll)))
		assert.Check(t, is.Equal(run.written(), run.before))
	})
}

func TestMerge_WhenTrunkRequiresChecks_WaitsForThoseOfTheHead(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		required := []string{"lint", "test"}

		run := runMergeIn(t, waiting(requiredPassing()).ruling(required...).saying(titledAfter("")).landing(nil, nil), 0)

		assertReadsRequire(t, run.forge.Calls(), run.tip, required)
	})
}
