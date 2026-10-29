package landing

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/result"
)

// The reads of the wait for the checks of a pull request's head, as a result names them.
const (
	readHead       = "the pull request's head"
	readMergeState = "whether the pull request can merge"
	readChecks     = "the checks of the pushed commit"
	readCI         = "whether the repository has CI"
)

// excerptLines is how many lines of a failing check's log a red verdict's excerpt holds, counted
// from the log's end, so that the failure stays readable in a terminal.
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

// cutShortReading returns the reason that says the time limit, or an interrupt when byInterrupt
// is true, ended a read of read, with nothing more to change.
func cutShortReading(byInterrupt bool, read string) result.Phrase {
	if byInterrupt {
		return result.Say(runInterrupted+" while reading %s; nothing more will change", read)
	}

	return result.Say(timeLimitEnded+" while reading %s; nothing more will change", TimeLimit, read)
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
// number, whose trunk is trunk, for the ils command command. It returns false for green checks
// and for nothing reported, which each command maps on its own.
func (w Waited) stopReport(command result.Name, sha, trunk string, number int) (waitReport, bool) {
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
			lines: append([]result.Phrase{
				result.Say("Required and never reported: %s", strings.Join(w.Verdict.MissingRequired, ", ")),
			}, w.mergeStateLines()...),
			reason: result.Say("a required check never reported"),
			next:   result.NextStep("report that a required check never reported, and stop"),
		}, true
	case WaitMoved:
		return waitReport{
			reason:  result.Say("the head of pull request %s moved to %s", result.PullRequest(number), result.SHA(w.MovedTo)),
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

	line := result.Say("Check %s failed: %s", failed.Name, result.URL(failed.Link))
	reason := result.Say("the check %s failed", failed.Name)

	if failed.Canceled {
		line = result.Say("Check %s was canceled: %s", failed.Name, result.URL(failed.Link))
		reason = result.Say("the check %s was canceled", failed.Name)
	}

	return waitReport{
		lines:   append([]result.Phrase{line}, w.mergeStateLines()...),
		excerpt: w.excerpt(),
		reason:  reason,
		next:    result.NextStep("fix the failing check, commit, and run %s again", result.Command(PushCommand)),
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
		lines:  append(lines, w.mergeStateLines()...),
		reason: reason,
		next: result.NextStep("merge the trunk into the branch with `git merge origin/%s`, resolve the conflicts, "+
			"commit, and run %s again", result.Branch(trunk), result.Command(PushCommand)),
	}
}

// pendingReport is a wait that ended before the forge had an answer about the commit sha of pull
// request number, at the stage it was in.
func (w Waited) pendingReport(sha string, number int) waitReport {
	report := waitReport{pending: true, next: result.NextCommand(PushCommand)}
	pull := result.PullRequest(number)

	switch {
	case w.Stage == StageHead && w.Cutoff == CutoffInterrupt:
		report.reason = result.Say("GitHub did not show %s on pull request %s before "+runInterrupted, result.SHA(sha), pull)
	case w.Stage == StageHead:
		report.reason = result.Say("GitHub did not show %s on pull request %s before the %s time limit",
			result.SHA(sha), pull, TimeLimit)
	case w.Stage == StageMergeState && w.Cutoff == CutoffInterrupt:
		report.reason = result.Say("GitHub did not say whether pull request %s can merge before "+runInterrupted, pull)
	case w.Stage == StageMergeState:
		report.reason = result.Say("GitHub did not say whether pull request %s can merge before the %s time limit",
			pull, TimeLimit)

		// The stage's own window ended the wait: the time limit was not reached.
		if w.Cutoff == CutoffWindow {
			report.reason = result.Say("GitHub did not say whether pull request %s can merge within %s seconds",
				pull, int(mergeWindow/time.Second))
		}
	default:
		report.lines, report.reason = w.unconcluded(sha, number)
	}

	return report
}

// unconcluded returns what the checks stage had found when the deadline or an interrupt ended it,
// then the merge state, and the reason: no verdict read at all, nothing reported, a workflow run
// with no check yet, checks concluded before any workflow run started, or the checks still
// running. A wait an interrupt ended holds no verdict read, since no read of the head after the
// checks confirmed one.
func (w Waited) unconcluded(sha string, number int) ([]result.Phrase, result.Phrase) {
	var lines []result.Phrase

	reason := result.Say("the checks were still running when the %s time limit ran out", TimeLimit)
	commit, pull := result.SHA(sha), result.PullRequest(number)

	switch {
	case w.Since.IsZero():
		reason = cutShortReading(w.Cutoff == CutoffInterrupt, readChecks)
	case silent(w.Verdict):
		reason = result.Say("no check or status had reported for %s on pull request %s when the %s time limit ran out",
			commit, pull, TimeLimit)
	case len(w.Verdict.Checks) == 0 && w.Verdict.Running():
		lines = append(lines, result.Say(
			"No check has reported for %s on pull request %s yet; a workflow run is still going", commit, pull))
		reason = result.Say("the checks had not reported when the %s time limit ran out", TimeLimit)
	// The checks concluded, but the deadline ended the wait for workflow runs to appear.
	case len(w.Verdict.Runs) == 0 && len(w.Verdict.Pending()) == 0:
		reason = result.Say("no workflow run had started for %s on pull request %s when the %s time limit ran out",
			commit, pull, TimeLimit)
	default:
		pending := w.Verdict.Pending()
		elapsed := time.Since(w.Since).Truncate(time.Second)
		lines = runningList(result.Say("Still running after %s (%s):", elapsed, checkCount(len(pending))), pending)
	}

	return append(lines, w.mergeStateLines()...), reason
}

// ciCutShortReport is a read of whether the repository has CI that the time limit, or an
// interrupt when byInterrupt is true, cut short.
func (w Waited) ciCutShortReport(byInterrupt bool) waitReport {
	return waitReport{
		lines: w.mergeStateLines(), reason: cutShortReading(byInterrupt, readCI),
		next: result.NextCommand(PushCommand), pending: true,
	}
}

func nothingReported(sha string, number int) result.Phrase {
	return result.Say("no check or status reported for %s on pull request %s", result.SHA(sha), result.PullRequest(number))
}

// noCI says that the trunk has no CI, so that nothing verified the commit sha.
func noCI(trunk, sha string) result.Phrase {
	return result.Say("No CI is configured on %s, so nothing verified %s", result.Branch(trunk), result.SHA(sha))
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

// mergeStateLines says the merge state in plain words on one line, or on none when it is clean. A
// state not read yet, or one ils has no word for, reads as unknown.
func (w Waited) mergeStateLines() []result.Phrase {
	var line result.Phrase

	switch w.MergeState {
	case forge.MergeClean:
		return nil
	case forge.MergeBehind:
		line = result.Say("The pull request is out of date with the trunk")
	case forge.MergeConflicting:
		line = result.Say("The pull request has merge conflicts")
	case forge.MergeDraft:
		line = result.Say("The pull request is still a draft")
	case forge.MergeBlockedByReview:
		line = result.Say("The pull request waits for an approving review")
	case forge.MergeBlockedByMissingCheck:
		line = result.Say("The pull request waits for a required check")
	case forge.MergeBlockedOther:
		line = result.Say("A branch rule ils cannot name blocks the merge")
	default:
		line = result.Say("GitHub has not decided yet whether the merge would succeed")
	}

	return []result.Phrase{line}
}

// runningList returns header, then each check of pending on a line of its own, indented under it.
func runningList(header result.Phrase, pending []forge.Check) []result.Phrase {
	lines := make([]result.Phrase, 0, len(pending)+1)
	lines = append(lines, header)

	for _, check := range pending {
		lines = append(lines, result.Say("  ◌ %s", check.Name))
	}

	return lines
}

// checkCount is n as a count of checks: "1 check", or "<n> checks" for any other n.
func checkCount(n int) string {
	if n == 1 {
		return "1 check"
	}

	return strconv.Itoa(n) + " checks"
}

// checksLine says how many deciding checks of the commit sha passed and how many were skipped,
// leaving out a tally of zero. When both are zero, no deciding check concluded: the trunk requires
// checks and none of them has reported, so the line says that instead.
func (w Waited) checksLine(sha string) result.Phrase {
	commit := result.SHA(sha)

	switch {
	case w.Passed == 0 && w.Skipped == 0:
		return result.Say("No required check has reported for %s", commit)
	case w.Passed > 0 && w.Skipped == 0:
		return result.Say("Checks for %s: %s", commit, result.Passed(w.Passed))
	case w.Passed == 0 && w.Skipped > 0:
		return result.Say("Checks for %s: %s", commit, result.Skipped(w.Skipped))
	default:
		return result.Say("Checks for %s: %s, %s", commit, result.Passed(w.Passed), result.Skipped(w.Skipped))
	}
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
		return "no log: the log of " + check + " was not available yet"
	default:
		return "no log: the log of " + check + " could not be read"
	}
}
