package landing_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

// redRow is a push whose lint check ended in bucket beside a passing test, with log as its failed
// log: the lines say the check failed, or was canceled, and the excerpt block is excerpt.
func redRow(name string, bucket forge.Bucket, log forge.FailedLog, excerpt string) pushRow {
	said := "failed"
	if bucket == forge.BucketCanceled {
		said = "was canceled"
	}

	log.Check = "lint"
	lint := lintFailed()
	lint.Bucket = bucket
	verdict := forge.Verdict{
		Checks:    []forge.Check{lint, forgetest.Check("test", forge.BucketPass)},
		Runs:      ran(forge.RunCompleted),
		FailedLog: log,
	}
	payload := detail(forge.VerdictRed, forge.MergeBlockedOther, verdict.Checks...)
	payload.FailedLog = log.Text

	state := stateLine(forge.MergeBlockedOther)
	if !strings.Contains(excerpt, partEnd) {
		// An excerpt of the log follows the state line at once, so that its first line is held too.
		state, excerpt = expect(partsOf(state)[0]+excerpt), ""
	}

	return pushRow{
		name:   name,
		script: opening(verdict).showing(inState(forge.MergeBlockedOther)),
		want: finish{
			outcome: result.DeliveredNotGreen, reason: expect("lint", said), payload: payload,
			text: pushedLine() + openedLine() + expect("lint", said, lintURL) +
				state + excerpt + expect("lint", said) + nextRed(),
		},
	}
}

// runningOn returns row with its one verdict read while the workflow run still goes on.
func runningOn(row pushRow) pushRow {
	row.script = row.script.with(func(script *forgetest.Script, _ string) {
		script.Verdict[0].Value.Runs = ran(forge.RunInProgress)
	})

	return row
}

// pushRedRows are the pushes whose deciding check failed or was canceled.
func pushRedRows() []pushRow {
	// The log is one line longer than the excerpt may be.
	read := forge.FailedLog{Text: steps(1, 61), State: forge.FailedLogRead}
	short := forge.FailedLog{Text: steps(1, 3), State: forge.FailedLogRead}
	// unfinished stays held back up to the time limit: a third poll would end three seconds past it.
	unfinished := runningOn(redRow("a check failed and its workflow run had not finished at the time limit", forge.BucketFail,
		forge.FailedLog{State: forge.FailedLogInProgress}, expect("lint", "not available yet")))
	unfinished.left = 12 * time.Second
	shorter := redRow("a check failed and its log is shorter than the excerpt", forge.BucketFail, short, steps(1, 3))

	return []pushRow{
		unfinished,
		redRow("a check failed and its log is longer than the excerpt", forge.BucketFail, read, steps(2, 61)),
		shorter,
		redRow("a check failed that no workflow run stands behind", forge.BucketFail,
			forge.FailedLog{State: forge.FailedLogNoRun}, expect("lint", "not a workflow run")),
		redRow("a check was canceled and its log cannot be read", forge.BucketCanceled,
			forge.FailedLog{State: forge.FailedLogUnreadable}, expect("lint", "could not be read")),
	}
}

func TestPush_WhenACheckFailed_ReportsTheExcerptOfItsLog(t *testing.T) {
	t.Parallel()

	runPushes(t, pushRedRows())
}

// stoppedRow is a push the pull request's merge state stops before any check is read.
func stoppedRow(state forge.MergeState, reason string) pushRow {
	return pushRow{
		name:   string(state),
		script: reusing().showing(inState(state)),
		want: finish{
			outcome: result.DeliveredNotGreen, reason: expect(reason),
			text:    pushedLine() + foundLine() + stateLine(state) + expect(reason) + nextTrunk(),
			payload: detail(forge.VerdictNone, state),
		},
	}
}

func TestPush_WhenTheMergeStateKeepsThePullRequestApart_DeliveredNotGreen(t *testing.T) {
	t.Parallel()

	runPushes(t, []pushRow{
		stoppedRow(forge.MergeConflicting, "conflicts with the trunk"),
		stoppedRow(forge.MergeBehind, "behind the trunk"),
	})
}

func nextNever() string {
	return expect("a required check never reported")
}

func TestPush_WhenARequiredCheckNeverReported_DeliveredNotGreen(t *testing.T) {
	t.Parallel()

	lint := forgetest.RequiredCheck("lint", forge.BucketPass)
	never := forge.Verdict{Checks: []forge.Check{lint}, MissingRequired: []string{"test"}, Runs: ran(forge.RunCompleted)}
	runPushes(t, []pushRow{
		{
			name:   "a required check never reported",
			script: opening(never).requiring("lint", "test").showing(inState(forge.MergeBlockedByMissingCheck)),
			want: finish{
				outcome: result.DeliveredNotGreen, reason: expect("a required check never reported"),
				text: pushedLine() + openedLine() + expect("never reported", "test") +
					stateLine(forge.MergeBlockedByMissingCheck) + expect("a required check never reported") + nextNever(),
				payload: detail(forge.VerdictGreen, forge.MergeBlockedByMissingCheck, lint),
			},
		},
	})
}

func TestPush_WhenTheHeadMovesPastThePushedCommit_RefusesNamingTheNewHead(t *testing.T) {
	t.Parallel()

	refused := finish{
		outcome: result.Refused, reason: movedTo(),
		text:    pushedLine() + foundLine() + movedTo() + pushCommand().after("pull the new commit"),
		payload: detail(forge.VerdictNone, forge.MergeClean),
	}
	runPushes(t, []pushRow{
		{
			name: "after a green verdict", want: refused,
			script: reusing(passing()).showing(cleanHead(), moved()),
		},
	})
}

// pending is a push to the open pull request that ended pending for reason, after lines.
func pending(reason, lines string, payload *result.Push) finish {
	return finish{
		outcome: result.Pending, reason: reason, payload: payload,
		text: pushedLine() + foundLine() + lines + reason + nextPush(),
	}
}

// cutShortRead is a push to the open pull request that ended pending because c ended a read of
// read.
func cutShortRead(c cut, read string) finish {
	return pending(expect(c.ends, read, "nothing more will change"), "",
		detail(forge.VerdictNone, forge.MergeClean))
}

// pushCutShortRows are the pushes whose wait a cut ended inside a read or a poll, for each cut. A
// cut in a poll is not known ahead of it.
func pushCutShortRows() []pushRow {
	var rows []pushRow
	for _, c := range cuts() {
		unsaid := expect("#12", "can merge", c.before)
		rows = append(rows,
			pushRow{
				name: c.name + " ends the first read of the checks", want: cutShortRead(c, readChecks),
				script: reusing().failingChecks(c.of("gh")),
			},
			pushRow{
				name:   c.name + " ends the read of the merge state after the checks",
				script: reusing(passing()).thenFailingHead(c.of("gh")),
				want:   pending(unsaid, "", detail(forge.VerdictNone, forge.MergeClean)),
			},
			pushRow{
				name: c.name + " ends a poll of the head", script: reusing().showing(stale()), cut: c.inPoll,
				want: pending(expect("did not show", tipMark, "#12", c.before), "",
					detail(forge.VerdictNone, forge.MergeClean)),
			},
		)
	}

	return rows
}

// appPassing is a verdict whose one check, of an app, passed: no workflow run stands behind it.
func appPassing() forge.Verdict {
	return forge.Verdict{Checks: []forge.Check{{Name: "scan", Bucket: forge.BucketPass}}}
}

// pushRunlessRows are the pushes whose deadline ended inside the 20 seconds a commit is given
// for its workflow runs to appear, before any of them had a check to report.
func pushRunlessRows() []pushRow {
	started := forge.Verdict{Runs: ran(forge.RunInProgress)}
	scanned := appPassing()
	silent := pending(expect("no check or status", tipMark, "#12"),
		"", detail(forge.VerdictNone, forge.MergeClean))
	noRun := pending(expect("no workflow run", tipMark, "#12"),
		"", detail(forge.VerdictGreen, forge.MergeClean, scanned.Checks...))

	return []pushRow{
		{name: "nothing has reported yet", left: 12 * time.Second, script: reusing(forge.Verdict{}), want: silent},
		{
			name: "a workflow run is going and no check has reported yet", left: 12 * time.Second,
			script: reusing(started),
			want: pending(expect("had not reported"),
				expect("No check has reported", tipMark, "#12", "workflow run is still going"),
				detail(forge.VerdictNone, forge.MergeClean)),
		},
		{
			name: "a check passed and no workflow run has started yet", left: 12 * time.Second,
			script: reusing(scanned), want: noRun,
		},
	}
}

// pushPendingRows are the pushes whose wait ended before the forge had an answer, one per way it ends.
func pushPendingRows() []pushRow {
	return []pushRow{
		{
			name:   "the merge state is still unknown",
			script: reusing().showing(inState(forge.MergeUnknown)),
			want: pending(expect("#12", "can merge", "within"), "",
				detail(forge.VerdictNone, forge.MergeUnknown)),
		},
	}
}

// onHeadAfterChecks returns a read hook that runs do on each read of the head after the first read
// of the checks.
func onHeadAfterChecks(do func()) func(forgetest.Call) {
	checked := false

	return func(call forgetest.Call) {
		switch {
		case call.Op == forgetest.OpVerdict:
			checked = true
		case call.Op == forgetest.OpHead && checked:
			do()
		}
	}
}

// startedAt returns build with the checks of every verdict it answers started at the times starts
// gives for the moment the push begins, in order; the zero time leaves a check not started.
func startedAt(build forgeFor, starts func(begun time.Time) []time.Time) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		at := starts(time.Now())
		for _, answer := range script.Verdict {
			for i := range answer.Value.Checks {
				answer.Value.Checks[i].StartedAt = at[i]
			}
		}
	})
}

// pushStillRunningRows are the pushes whose wait ended with a check still running at the time limit.
func pushStillRunningRows() []pushRow {
	return []pushRow{
		{
			// The head catches up after one poll of two seconds; then two polls of five seconds pass,
			// and a third would leave too little time for the head read after it.
			name: "a check still runs", left: 17 * time.Second,
			script: reusing(running()).showing(stale(), cleanHead()),
			want: pending(expect("still running"),
				expect("Still running", "(1 check):\n  ◌ test"),
				detail(forge.VerdictPending, forge.MergeClean, running().Checks...)),
			told: []string{
				checkingStep(), pushingStep(), said(pushedLine()), said(foundLine()),
				waitingStep(), "count 0 of 1", "count 0 of 1", "count 0 of 1",
			},
		},
		{
			// Seven seconds leave no room for a poll and the head read after it, so the head is read at
			// once after the checks; that read takes the second and a half the checks are waited on,
			// so a check begun a minute before the push has run a minute and a second and a half, which
			// the result says in whole seconds.
			name: "a check still runs a second and a half into the wait", left: 7 * time.Second,
			script: startedAt(reusing(running()).
				showing(cleanHead(), inState(forge.MergeBlockedByReview)).
				with(func(script *forgetest.Script, _ string) {
					script.OnRead = onHeadAfterChecks(func() { time.Sleep(1500 * time.Millisecond) })
				}), func(begun time.Time) []time.Time { return []time.Time{begun.Add(-time.Minute)} }),
			want: pending(expect("still running"),
				expect("Still running", "(1 check):\n  ◌ test, running 1m1s\n")+blockedLine(),
				detail(forge.VerdictPending, forge.MergeBlockedByReview, running().Checks...)),
		},
		{
			// GitHub has not started lint, and says test started a minute after the push began, ahead
			// of the local clock.
			name: "two checks still run, one not started and one dated ahead of the local clock", left: 7 * time.Second,
			script: startedAt(reusing(bothRunning()).showing(cleanHead(), cleanHead()),
				func(begun time.Time) []time.Time { return []time.Time{{}, begun.Add(time.Minute)} }),
			want: pending(expect("still running"),
				expect("Still running", "(2 checks):\n  ◌ lint, not started yet\n  ◌ test, running 0s\n"),
				detail(forge.VerdictPending, forge.MergeClean, bothRunning().Checks...)),
		},
	}
}

func TestPush_WhenTheWaitEndsBeforeAnAnswer_PendingSayingWhy(t *testing.T) {
	t.Parallel()

	runPushes(t, slices.Concat(pushRunlessRows(), pushPendingRows(), pushStillRunningRows(), pushCutShortRows()))
}

// waitReads returns the reads of a push's wait, in the order the wait makes them, each failing
// with err.
func waitReads(err error) []failingRead {
	// undecided shows the tip in a merge state the forge has not worked out, and then fails.
	undecided := opening(passing()).showing(inState(forge.MergeUnknown)).thenFailingHead(err)
	opens := pushedLine() + openedLine()

	return []failingRead{
		{
			name: readHead, before: opens, known: true,
			script: breaking(func(script *forgetest.Script) {
				script.Head = []forgetest.Answer[forge.Head]{{Err: err}}
			}),
		},
		{name: readMergeState, before: opens, known: true, script: undecided},
		{
			name: readChecks, before: opens, known: true,
			script: opening(passing()).failingChecks(err),
		},
	}
}

func TestPush_WhenAReadOfTheWaitFails_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()

	runPushes(t, brokenAfter(waitReads(errors.New(forgeFailure))))
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
			assert.Check(t, is.DeepEqual(call.HeadQuery, forge.HeadQuery{Number: opened, Required: required}))
		}

		if call.Op == forgetest.OpVerdict {
			verdicts++
			assert.Check(t, is.DeepEqual(call.VerdictQuery, forge.VerdictQuery{SHA: tip, Number: opened, Required: required}))
		}
	}

	assert.Check(t, heads > 0 && verdicts > 0, "forge reads = %d heads and %d verdicts, want some of each", heads, verdicts)
}

// stateWords returns the end of the line that says each merge state but clean in plain words.
func stateWords() map[forge.MergeState]string {
	return map[forge.MergeState]string{
		forge.MergeBehind:                "out of date with the trunk",
		forge.MergeConflicting:           "has merge conflicts",
		forge.MergeDraft:                 "still a draft",
		forge.MergeBlockedByReview:       "waits for an approving review",
		forge.MergeBlockedByMissingCheck: "waits for a required check",
		forge.MergeBlockedByRunningCheck: "waits for a required check to finish",
		forge.MergeBlockedOther:          "ils cannot name blocks the merge",
		forge.MergeUnknown:               "has not decided yet whether the merge would succeed",
	}
}

// stateLine is the line that says the merge state state in plain words.
func stateLine(state forge.MergeState) string {
	return expect(stateWords()[state] + "\n")
}

// assertNoStateLine fails on any plain-words line from stateWords; a clean state prints none.
func assertNoStateLine(t *testing.T, text string) {
	t.Helper()

	for state, words := range stateWords() {
		assert.Check(t, !strings.Contains(text, words), "text %q says the merge state %s", text, state)
	}
}

func TestPush_WhenTheMergeStateDoesNotStopThePush_SaysNoMergeState(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, d := newDelivery(t, pushRow{left: 12 * time.Second, script: reusing(forge.Verdict{})})

		got := landing.Push(ctx, d.deps, pushRequest())

		assert.Assert(t, is.Equal(got.Outcome, result.Pending))
		text, decoded := rendered(t, got, d.gitDir)
		assertNoStateLine(t, text)
		assertNoStateLine(t, linesText(decoded.Lines))
	})
}

func TestPush_WhenTheMergeStateStopsThePush_SaysItInPlainWords(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, d := newDelivery(t, pushRow{script: reusing().showing(inState(forge.MergeConflicting))})

		got := landing.Push(ctx, d.deps, pushRequest())

		assert.Assert(t, is.Equal(got.Outcome, result.DeliveredNotGreen))
		text, decoded := rendered(t, got, d.gitDir)
		assertSays(t, text, stateLine(forge.MergeConflicting))
		assertSays(t, linesText(decoded.Lines), stateLine(forge.MergeConflicting))
	})
}

func TestLastLines_WhenTheTextIsEmpty_ReturnsNoExcerpt(t *testing.T) {
	t.Parallel()

	got := landing.LastLines("")

	assert.Check(t, is.Equal(got, ""))
}
