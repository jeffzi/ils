package landing_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
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
				"GitHub: UNSTABLE (blocked-other)\n" + excerpt + "Delivered, not green: the check lint " + said + "\n" + nextRed,
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

// heldBack returns row with a verdict read ahead of its own: the same failed check, with its
// workflow run still going and its log not yet available.
func heldBack(row pushRow) pushRow {
	row.script = row.script.with(func(script *forgetest.Script, _ string) {
		early := script.Verdict[0].Value
		early.Runs = ran(forge.RunInProgress)
		early.FailedLog = forge.FailedLog{Check: early.FailedLog.Check, State: forge.FailedLogInProgress}
		script.Verdict = append(says(early), script.Verdict...)
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

	return []pushRow{
		heldBack(redRow("a check failed before its workflow run finished, and its log was read once it had", forge.BucketFail,
			short, steps(1, 3))),
		unfinished,
		redRow("a check failed and its log is longer than the excerpt", forge.BucketFail, read, steps(2, 61)),
		redRow("a check failed and its log is shorter than the excerpt", forge.BucketFail, short, steps(1, 3)),
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
			text: pushedLine + foundLine + "GitHub: " + raw + " (" + string(state) + ")\n" +
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

// cutShortRead is a push to the open pull request that ended pending because the time limit ended
// while reading read.
func cutShortRead(read string) finish {
	return pending("the time limit ended while reading "+read+"; nothing more will change", cleanLine,
		detail(forge.VerdictNone, forge.MergeClean))
}

// pushCutShortRows are the pushes whose deadline ended inside a read of the wait.
func pushCutShortRows() []pushRow {
	ended := fmt.Errorf("gh: %w", context.DeadlineExceeded)

	return []pushRow{
		{
			name: "the deadline ends in the first read of the checks",
			script: reusing().with(func(script *forgetest.Script, _ string) {
				script.Verdict = []forgetest.Answer[forge.Verdict]{{Err: ended}}
			}),
			want: cutShortRead(readChecks),
		},
		{
			name:   "the deadline ends in the read of the merge state after the checks",
			script: reusing(passing()).thenFailingHead(ended),
			want: pending("GitHub did not say whether pull request #12 can merge before the time limit", "",
				detail(forge.VerdictNone, forge.MergeClean)),
		},
	}
}

// pushRunlessRows are the pushes whose deadline ended inside the 20 seconds a commit is given
// for its workflow runs to appear, before any of them had a check to report.
func pushRunlessRows() []pushRow {
	started := forge.Verdict{Runs: ran(forge.RunInProgress)}
	scanned := forge.Verdict{Checks: []forge.Check{{Name: "scan", Bucket: forge.BucketPass}}}

	return []pushRow{
		{
			name: "nothing has reported yet", left: 12 * time.Second, script: reusing(forge.Verdict{}),
			want: pending("no check or status had reported for "+tipMark+" on pull request #12 at the time limit",
				cleanLine,
				detail(forge.VerdictNone, forge.MergeClean)),
		},
		{
			name: "a workflow run is going and no check has reported yet", left: 12 * time.Second,
			script: reusing(started),
			want: pending("the checks had not reported at the time limit",
				"No check has reported for "+tipMark+" on pull request #12 yet; a workflow run is still going\n"+cleanLine,
				detail(forge.VerdictNone, forge.MergeClean)),
		},
		{
			name: "a check passed and no workflow run has started yet", left: 12 * time.Second,
			script: reusing(scanned),
			want: pending("no workflow run had started for "+tipMark+" on pull request #12 at the time limit",
				cleanLine,
				detail(forge.VerdictGreen, forge.MergeClean, scanned.Checks...)),
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
		{
			// The head catches up after one poll of two seconds, which the count leaves out; then
			// two polls of five seconds pass, and a third would end three seconds past the deadline.
			name: "a check still runs", left: 14 * time.Second,
			script: reusing(running()).showing(stale(), inState(forge.MergeClean, "CLEAN")),
			want: pending("the checks were still running at the time limit",
				"Still running after 10 seconds: test\n"+cleanLine,
				detail(forge.VerdictPending, forge.MergeClean, running().Checks...)),
		},
	}
}

func TestPush_WhenTheWaitEndsBeforeAnAnswer_PendingSayingWhy(t *testing.T) {
	t.Parallel()

	runPushes(t, slices.Concat(pushRunlessRows(), pushPendingRows(), pushCutShortRows()))
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
