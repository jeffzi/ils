package landing_test

import (
	"context"
	"errors"
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
	"github.com/jeffzi/ils/internal/result"
)

// The `Next:` lines that stop a merge for the user to look at.

func nextReviewStop() string {
	return expect("request a review", "not the agent's to give")
}

func nextMissingStop() string {
	return expect("required check has no check on the head")
}

func nextBlockedStop() string {
	return expect("GitHub blocks the merge")
}

func nextNothingHead() string {
	return expect("no check or status reported for the head")
}

const (
	readMessage = "the pull request's message"

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
		script.TrunkProtection = forgetest.Values(rules)
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

// waitRow is one run of `ils merge` that passes every check before the wait, and how it ends.
type waitRow struct {
	script  forgeFor
	payload *result.Merge
	cut     func(context.Context) context.Context
	name    string
	text    string
	outcome result.Outcome
	left    time.Duration
}

// runWaits runs each row as a test in a bubble of its own: the run carries the row's payload,
// writes nothing, and ends as the row says.
func runWaits(t *testing.T, rows []waitRow) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				run := runMergeOn(t, setting{script: tt.script, left: tt.left, cut: tt.cut})

				want := *tt.payload
				want.SHA = run.tip
				assert.Check(t, is.DeepEqual(mergePayload(t, run.got), &want, cmpopts.EquateEmpty()))
				assert.Check(t, is.Equal(run.written(), run.before))
				assertEndedBy(t, run.got, run.gitDir, ended{text: mark(tt.text, run.tip), req: mergeRequest(), outcome: tt.outcome})
			})
		})
	}
}

// waitVerdicts are the runs of `ils merge` whose wait ends in a verdict that stops the merge.
func waitVerdicts() []waitRow {
	red := lintRed()
	red.FailedLog.Text = steps(1, 3)
	nothing := cleanLine() + expect("no check or status reported", tipMark, "#12") + nextNothingHead()

	return []waitRow{
		{
			name: "a deciding check failed", script: waiting(red).showing(inState(forge.MergeBlockedOther, "UNSTABLE")),
			outcome: result.Refused, payload: mergeDetail(forge.VerdictRed, forge.MergeBlockedOther, red.Checks...),
			text: expect("lint", "failed", lintURL, partsOf(stateLine(forge.MergeBlockedOther, "UNSTABLE"))[0]+steps(1, 3),
				"lint", "failed") + nextRed(),
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
	rows := []waitRow{
		{
			// The head catches up after one poll of two seconds, which the count leaves out; then two
			// polls of five seconds pass, and a third would leave too little time for the head read
			// after it.
			name: "a check still runs", left: 37 * time.Second,
			script: waiting(running()).showing(stale(), inState(forge.MergeClean, "CLEAN")), outcome: result.Pending,
			text:    expect("Still running", "10 seconds", "test") + cleanLine() + expect("still running") + nextPush(),
			payload: mergeDetail(forge.VerdictPending, forge.MergeClean, running().Checks...),
		},
	}

	for _, c := range cuts() {
		pendingText := func(read string) string {
			return cleanLine() + expect(c.ends, read, "nothing more will change") + nextPush()
		}
		rows = append(rows, waitRow{
			name:    c.name + " ends the CI read",
			script:  waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{Err: c.of("gh")}),
			outcome: result.Pending, text: pendingText(readCI), payload: mergeDetail(forge.VerdictNone, forge.MergeClean),
		})
	}

	return rows
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

func TestMerge_WhenTheWaitStopsTheMerge_EndsWithNothingWritten(t *testing.T) {
	t.Parallel()

	runWaits(t, slices.Concat(waitVerdicts(), pendingWaits(), waitBreaks()))
}

// afterWaitStops are the runs of `ils merge` whose checks are green and which stop on what they
// read after the wait.
func afterWaitStops() []waitRow {
	greenThen := func(state forge.MergeState, raw, lead, rest string) waitRow {
		return waitRow{
			name:    string(state),
			script:  waiting(passing()).showing(inState(forge.MergeClean, "CLEAN"), inState(state, raw)),
			outcome: result.Refused, text: lead + stateLine(state, raw) + rest,
			payload: mergeDetail(forge.VerdictGreen, state, passing().Checks...),
		}
	}
	reviewed := inState(forge.MergeBlockedByReview, "BLOCKED")
	// The forge reports no required check missing, so the wait ends green and GitHub's state decides.
	// The head has checks named lint and test; named is the line that names the required checks.
	missing := func(name, named string, required ...string) waitRow {
		row := greenThen(forge.MergeBlockedByMissingCheck, "BLOCKED",
			expect(tipMark, "2 passed, 0 skipped")+named,
			expect("requires has no check", tipMark)+nextMissingStop())
		row.name = name
		row.script = waiting(requiredPassing()).ruling(required...).
			showing(inState(forge.MergeClean, "CLEAN"), inState(forge.MergeBlockedByMissingCheck, "BLOCKED"))
		row.payload = mergeDetail(forge.VerdictGreen, forge.MergeBlockedByMissingCheck, requiredPassing().Checks...)

		return row
	}

	conflicting := greenThen(forge.MergeConflicting, "DIRTY",
		countLine(), expect("conflicts with the trunk")+nextTrunk())
	conflicting.name = "conflicting after the checks"

	return []waitRow{
		greenThen(forge.MergeDraft, "DRAFT", countLine(), draftText()),
		conflicting,
		greenThen(forge.MergeBlockedByReview, "BLOCKED",
			countLine(), expect("#12", "requires a review")+nextReviewStop()),
		missing(string(forge.MergeBlockedByMissingCheck), expect("no check on", tipMark, ": build\n"),
			"lint", "test", "build"),
		// GitHub expects a check ils cannot tell apart, so every required check is named.
		missing("blocked-by-missing-check with a check for each required one", expect("Required by the trunk: lint, test\n"),
			"lint", "test"),
		greenThen(forge.MergeBlockedOther, "BLOCKED",
			countLine(), expect("blocks", "#12", "reason ils does not read", pullURL)+nextBlockedStop()),
		{
			name: "blocked by review with nothing reported and no CI on the trunk",
			script: waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{}).
				showing(inState(forge.MergeClean, "CLEAN"), reviewed),
			outcome: result.Refused, payload: mergeDetail(forge.VerdictNone, forge.MergeBlockedByReview),
			text: noCILine() + blockedLine() + expect("#12", "requires a review") + nextReviewStop(),
		},
	}
}

// settledStops are the runs of `ils merge` whose checks are green and whose merge state, read again
// after the wait, stops the merge once a poll finds it or fails.
func settledStops() []waitRow {
	clean, unknown := inState(forge.MergeClean, "CLEAN"), inState(forge.MergeUnknown, "UNKNOWN")
	rows := []waitRow{
		{
			name:   "behind after an unknown poll",
			script: waiting(passing()).showing(clean, unknown, inState(forge.MergeBehind, "BEHIND")), outcome: result.Refused,
			text:    countLine() + stateLine(forge.MergeBehind, "BEHIND") + expect("behind the trunk") + nextTrunk(),
			payload: mergeDetail(forge.VerdictGreen, forge.MergeBehind, passing().Checks...),
		},
		{
			name: "conflicting with nothing reported and no CI on the trunk",
			script: waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{}).
				showing(clean, inState(forge.MergeConflicting, "DIRTY")),
			outcome: result.Refused, payload: mergeDetail(forge.VerdictNone, forge.MergeConflicting),
			text: noCILine() + stateLine(forge.MergeConflicting, "DIRTY") + expect("conflicts with the trunk") + nextTrunk(),
		},
		{
			name:    "a poll of the merge state fails",
			script:  waiting(passing()).showing(clean, unknown).thenFailingHead(errors.New("HTTP 502")),
			outcome: result.Broke, text: unreadText(mergeCommand(), readMergeState, "HTTP 502"),
			payload: mergeDetail(forge.VerdictGreen, forge.MergeUnknown, passing().Checks...),
		},
	}

	c := byTimeLimit()

	return append(rows, waitRow{
		name: c.name + " ends the poll of the merge state", script: waiting(passing()).showing(clean, unknown),
		cut:     c.inPoll,
		outcome: result.Pending, payload: mergeDetail(forge.VerdictGreen, forge.MergeUnknown, passing().Checks...),
		text: countLine() + expect("#12", "can merge", c.before) + nextPush(),
	})
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
		countLine()+cleanLine()+expect("for the merge", "nothing was written", "for the merge")+nextPush())
	// The wait's deadline is 80 seconds away: the read ends 19 seconds before the command's.
	late.script = late.script.messageTaking(81 * time.Second)
	late.payload = composed(worded+" (#12)", "")
	unread := row("the message read fails", forgetest.Answer[forge.Message]{Err: errors.New("HTTP 502")},
		result.Broke, unreadText(mergeCommand(), readMessage, "HTTP 502"))

	rows := []waitRow{
		row("the title no longer parses", forgetest.Answer[forge.Message]{Value: forge.Message{Title: "fix-thing"}},
			result.Refused, countLine()+cleanLine()+titleText("fix-thing")),
		unread,
	}

	return append(rows, late)
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
		inTime,
	}
}

// runMergeIn runs `ils merge` inside the test's bubble with the forge script builds and a deadline
// left of fake time away.
func runMergeIn(t *testing.T, script forgeFor, left time.Duration) mergeRun {
	t.Helper()

	return runMergeOn(t, setting{script: script, left: left})
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
				assert.Check(t, is.DeepEqual(mergePayload(t, run.got), &want, cmpopts.EquateEmpty()))
			})
		})
	}
}

// movedTo is the reason of a refusal of pull request 12, whose head moved to movedSHA.
func movedTo() string { return expect("#12", "moved to "+movedSHA) }

func TestMerge_WhenTheHeadMovesPastThePushedCommit_RefusesWithoutMerging(t *testing.T) {
	t.Parallel()

	unknownHead := inState(forge.MergeUnknown, "UNKNOWN")
	tests := []struct {
		name  string
		state forge.MergeState
		heads []forge.Head
	}{
		{name: "after the checks", heads: []forge.Head{inState(forge.MergeClean, "CLEAN"), moved()}, state: forge.MergeClean},
		{
			// The head moves to a commit in a merge state of its own.
			name:  "while the merge state is read again",
			heads: []forge.Head{inState(forge.MergeClean, "CLEAN"), unknownHead, unknownHead, moved()},
			state: forge.MergeUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				// The forge would compose and land the merge, were it asked to.
				script := waiting(passing()).showing(tt.heads...).saying(titledAfter("Tidy the wording.")).landing(nil, nil)

				run := runMergeIn(t, script, 0)

				assertEndedBy(t, run.got, run.gitDir, ended{text: movedTo() + mergeCommand().after("pull the new commit"), req: mergeRequest(), outcome: result.Refused})
				want := mergeDetail(forge.VerdictNone, tt.state)
				want.SHA = run.tip
				assert.Check(t, is.DeepEqual(mergePayload(t, run.got), want, cmpopts.EquateEmpty()))
				assert.Check(t, is.Equal(run.written(), run.before))
			})
		})
	}
}

func TestMerge_WhenTrunkRequiresChecks_WaitsForThoseOfTheHead(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		required := []string{"lint", "test"}

		run := runMergeIn(t, waiting(requiredPassing()).ruling(required...).saying(titledAfter("")).landing(nil, nil), 0)

		assertReadsRequire(t, run.forge.Calls(), run.tip, required)
	})
}
