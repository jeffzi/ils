package landing_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// redRow is a push whose lint check ended in bucket beside a passing test, with log as its failed
// log: the lines say the check failed, or was canceled, and the excerpt block is excerpt.
func redRow(name string, bucket forge.Bucket, log forge.FailedLog, excerpt string) pushRow {
	said := "failed"
	if bucket == forge.BucketCanceled {
		said = "was canceled"
	}

	log.Check = "lint"
	verdict := forge.Verdict{
		Checks: []forge.Check{
			{Name: "lint", Bucket: bucket, Workflow: "ci", Link: lintURL},
			checked("test", forge.BucketPass),
		},
		Runs:      ran(forge.RunCompleted),
		FailedLog: log,
	}
	payload := detail(forge.VerdictRed, forge.MergeBlockedOther, verdict.Checks...)
	payload.FailedLog = log.Text

	return pushRow{
		name:   name,
		script: opening(verdict).showing(inState(forge.MergeBlockedOther, "UNSTABLE")),
		want: finish{
			outcome: result.DeliveredNotGreen, reason: "the check lint " + said, payload: payload,
			text: pushedLine + openedLine + "Check lint " + said + ": " + lintURL + "\n" +
				stateLine(forge.MergeBlockedOther, "UNSTABLE") + excerpt + "Delivered, not green: the check lint " + said + "\n" + nextRed,
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
		forge.FailedLog{State: forge.FailedLogInProgress}, "no log: the log of lint was not available yet\n"))
	unfinished.left = 12 * time.Second
	shorter := redRow("a check failed and its log is shorter than the excerpt", forge.BucketFail, short, steps(1, 3))
	shorter.drawn = []drawnPart{
		{within: "failed: " + lintURL, part: lintURL, want: resulttest.URLLook()},
		{within: "run ils push again", part: "ils push", want: resulttest.NameLook()},
	}

	return []pushRow{
		unfinished,
		redRow("a check failed and its log is longer than the excerpt", forge.BucketFail, read, steps(2, 61)),
		shorter,
		redRow("a check failed that no workflow run stands behind", forge.BucketFail,
			forge.FailedLog{State: forge.FailedLogNoRun}, "no log: lint is not a workflow run\n"),
		redRow("a check was canceled and its log cannot be read", forge.BucketCanceled,
			forge.FailedLog{State: forge.FailedLogUnreadable}, "no log: the log of lint could not be read\n"),
	}
}

func TestPush_WhenACheckFailed_ReportsTheExcerptOfItsLog(t *testing.T) {
	t.Parallel()

	runPushes(t, pushRedRows())
}

// stoppedRow is a push the pull request's merge state stops before any check is read.
func stoppedRow(state forge.MergeState, raw, reason string) pushRow {
	return pushRow{
		name:   string(state),
		script: reusing().showing(inState(state, raw)),
		want: finish{
			outcome: result.DeliveredNotGreen, reason: reason,
			text: pushedLine + foundLine + stateLine(state, raw) +
				"Delivered, not green: " + reason + "\n" + nextTrunk,
			payload: detail(forge.VerdictNone, state),
		},
	}
}

func TestPush_WhenTheMergeStateKeepsThePullRequestApart_DeliveredNotGreen(t *testing.T) {
	t.Parallel()

	runPushes(t, []pushRow{
		stoppedRow(forge.MergeConflicting, "DIRTY", "the pull request conflicts with the trunk"),
		stoppedRow(forge.MergeBehind, "BEHIND", "the pull request is behind the trunk"),
	})
}

// pending is a push to the open pull request that ended pending for reason, after lines.
func pending(reason, lines string, payload *result.Push) finish {
	return finish{
		outcome: result.Pending, reason: reason, payload: payload,
		text: pushedLine + foundLine + lines + "Pending: " + reason + "\n" + nextPush,
	}
}

// cutShortRead is a push to the open pull request that ended pending because c ended a read of
// read.
func cutShortRead(c cut, read string) finish {
	return pending(c.ends+" while reading "+read+"; nothing more will change", cleanLine(),
		detail(forge.VerdictNone, forge.MergeClean))
}

// pushCutShortRows are the pushes whose wait a cut ended inside a read or a poll, for each cut. A
// cut in a poll is not known ahead of it.
func pushCutShortRows() []pushRow {
	rows := []pushRow{
		{
			// One poll fits in nine seconds with the 3 seconds kept for the head read after it; the time
			// limit ends the read after the poll a second in, and the head is read with 3 seconds left.
			name: "the time limit ends a later read of checks still running", left: 9 * time.Second,
			script: reusing(running()).
				showing(inState(forge.MergeClean, "CLEAN"), inState(forge.MergeBlockedByReview, "BLOCKED")).
				holdingNextRead(),
			want: pending("the checks were still running at the time limit",
				"Still running after 6 seconds: test\n"+blockedLine(),
				detail(forge.VerdictPending, forge.MergeBlockedByReview, running().Checks...)),
		},
	}

	for _, c := range cuts() {
		unsaid := "GitHub did not say whether pull request #12 can merge " + c.before
		rows = append(rows,
			pushRow{
				name: c.name + " ends the first read of the checks", want: cutShortRead(c, readChecks),
				script: reusing().with(func(script *forgetest.Script, _ string) {
					script.Verdict = []forgetest.Answer[forge.Verdict]{{Err: c.of("gh")}}
				}),
			},
			pushRow{
				name:   c.name + " ends the read of the merge state after the checks",
				script: reusing(passing()).thenFailingHead(c.of("gh")),
				want:   pending(unsaid, "", detail(forge.VerdictNone, forge.MergeClean)),
			},
			pushRow{
				name: c.name + " ends a poll of the head", script: reusing().showing(stale()), cut: c.inPoll,
				want: pending("GitHub did not show "+tipMark+" on pull request #12 "+c.before, "",
					detail(forge.VerdictNone, forge.MergeClean)),
			},
			pushRow{
				name: c.name + " ends a poll of the merge state", cut: c.inPoll,
				script: reusing().showing(inState(forge.MergeUnknown, "UNKNOWN")),
				want:   pending(unsaid, "", detail(forge.VerdictNone, forge.MergeUnknown)),
			},
			pushRow{
				name: c.name + " ends a poll of checks still running", script: reusing(running()), cut: c.inPoll,
				want: cutShortRead(c, readChecks),
			},
		)
	}

	return rows
}

// pushRunlessRows are the pushes whose deadline ended inside the 20 seconds a commit is given
// for its workflow runs to appear, before any of them had a check to report.
func pushRunlessRows() []pushRow {
	started := forge.Verdict{Runs: ran(forge.RunInProgress)}
	scanned := appPassing()
	silent := pending("no check or status had reported for "+tipMark+" on pull request #12 at the time limit",
		cleanLine(), detail(forge.VerdictNone, forge.MergeClean))
	noRun := pending("no workflow run had started for "+tipMark+" on pull request #12 at the time limit",
		cleanLine(), detail(forge.VerdictGreen, forge.MergeClean, scanned.Checks...))

	return []pushRow{
		{name: "nothing has reported yet", left: 12 * time.Second, script: reusing(forge.Verdict{}), want: silent},
		{
			name: "a workflow run is going and no check has reported yet", left: 12 * time.Second,
			script: reusing(started),
			want: pending("the checks had not reported at the time limit",
				"No check has reported for "+tipMark+" on pull request #12 yet; a workflow run is still going\n"+cleanLine(),
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
			// A third poll would end a second past the deadline.
			name: "the pull request still shows an earlier commit", left: 5 * time.Second,
			script: reusing().showing(stale()),
			want: pending("GitHub did not show "+tipMark+" on pull request #12 before the time limit", "",
				detail(forge.VerdictNone, forge.MergeClean)),
			drawn: []drawnPart{
				{within: "Pull request #12:", part: "#12", want: resulttest.NameLook()},
				{within: "#12: " + pullURL, part: pullURL, want: resulttest.URLLook()},
				{within: "show " + tipMark, part: tipMark, want: resulttest.SHALook()},
				{within: "on pull request #12 before", part: "#12", want: resulttest.NameLook()},
				{within: "Next: ils push", part: "ils push", want: resulttest.NameLook()},
			},
		},
		{
			name:   "the merge state is still unknown",
			script: reusing().showing(inState(forge.MergeUnknown, "UNKNOWN")),
			want: pending("GitHub did not say whether pull request #12 can merge within 30 seconds", "",
				detail(forge.VerdictNone, forge.MergeUnknown)),
		},
		{
			// A third poll would end a second past the deadline.
			name: "the merge state is still unknown and the deadline leaves no room for another poll",
			left: 5 * time.Second, script: reusing().showing(inState(forge.MergeUnknown, "UNKNOWN")),
			want: pending("GitHub did not say whether pull request #12 can merge before the time limit", "",
				detail(forge.VerdictNone, forge.MergeUnknown)),
		},
	}
}

// slowHeadAfterChecks returns a read hook under which each read of the head after the first read
// of the checks takes d.
func slowHeadAfterChecks(d time.Duration) func(forgetest.Call) {
	checked := false

	return func(call forgetest.Call) {
		switch {
		case call.Op == forgetest.OpVerdict:
			checked = true
		case call.Op == forgetest.OpHead && checked:
			time.Sleep(d)
		}
	}
}

func pushStillRunningRows() []pushRow {
	return []pushRow{
		{
			// The head catches up after one poll of two seconds, which the count leaves out; then two
			// polls of five seconds pass, and a third would leave too little time for the head read
			// after it.
			name: "a check still runs", left: 17 * time.Second,
			script: reusing(running()).showing(stale(), inState(forge.MergeClean, "CLEAN")),
			want: pending("the checks were still running at the time limit",
				"Still running after 10 seconds: test\n"+cleanLine(),
				detail(forge.VerdictPending, forge.MergeClean, running().Checks...)),
			told: []string{
				checkingStep, pushingStep, said(pushedLine), said(foundLine),
				waitingStep(17 * time.Second), "count 0 of 1", "count 0 of 1", "count 0 of 1",
			},
		},
		{
			// Seven seconds leave no room for a poll and the head read after it, so the head is read at
			// once after the checks; that read takes the one second the checks are waited on.
			name: "a check still runs a second into the wait", left: 7 * time.Second,
			script: reusing(running()).
				showing(inState(forge.MergeClean, "CLEAN"), inState(forge.MergeBlockedByReview, "BLOCKED")).
				with(func(script *forgetest.Script, _ string) {
					script.OnRead = slowHeadAfterChecks(time.Second)
				}),
			want: pending("the checks were still running at the time limit",
				"Still running after 1 second: test\n"+blockedLine(),
				detail(forge.VerdictPending, forge.MergeBlockedByReview, running().Checks...)),
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
	undecided := opening(passing()).showing(inState(forge.MergeUnknown, "UNKNOWN")).thenFailingHead(err)
	opens := pushedLine + openedLine

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
			script: breaking(func(script *forgetest.Script) {
				script.Verdict = []forgetest.Answer[forge.Verdict]{{Err: err}}
			}),
		},
	}
}

func TestPush_WhenAReadOfTheWaitFails_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()

	runPushes(t, brokenAfter(waitReads(errors.New("HTTP 502"))))
}
