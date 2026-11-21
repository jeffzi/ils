package github_test

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
)

// ---------------------------------------------------------------------------
// Reads of a commit's checks
// ---------------------------------------------------------------------------

const (
	ciSuite    = 11
	otherSuite = 12
	thirdSuite = 13
)

const statusLink = "https://ci.example.com/builds/15"

func checkRunsArgs() []string { return pagedAPIArgs("commits/" + headSHA + "/check-runs") }

func commitStatusArgs() []string { return pagedAPIArgs("commits/" + headSHA + "/status") }

func workflowRunsArgs() []string { return pagedAPIArgs("actions/runs?head_sha=" + headSHA) }

// checkRunJSON is one check run of the check-runs read: the check run numbered id, named name, of
// the check suite suite, with conclusion, whose link is runLink; an empty conclusion is null and
// leaves the run in progress, any other completes it. A higher id is a later start.
func checkRunJSON(id, suite int, name, conclusion string) string {
	return checkRunAt(checkRun{id: id, suite: suite, name: name, conclusion: conclusion, link: runLink})
}

// checkRun is one check run of the check-runs read, as checkRunJSON describes it.
type checkRun struct {
	name, conclusion, link string
	id, suite              int
}

// checkRunAt is checkRunJSON for a check run whose link is run.link.
func checkRunAt(run checkRun) string {
	status, concluded := "in_progress", "null"
	if run.conclusion != "" {
		status, concluded = "completed", `"`+run.conclusion+`"`
	}

	return `{"id":` + strconv.Itoa(run.id) + `,"name":"` + run.name + `","status":"` + status + `","conclusion":` +
		concluded + `,"started_at":"2026-10-22T10:` + fmt.Sprintf("%02d", run.id) + `:00Z","html_url":"` + run.link +
		`","check_suite":{"id":` + strconv.Itoa(run.suite) + `}}`
}

func checkRunsPage(runs ...string) string { return pageJSON("check_runs", runs...) }

// statusJSON is one commit status of the combined status read: the context name in state.
func statusJSON(name, state string) string {
	return `{"context":"` + name + `","state":"` + state + `","target_url":"` + statusLink + `"}`
}

func statusPage(statuses ...string) string {
	return `{"state":"pending","sha":"` + headSHA + `","total_count":` + strconv.Itoa(len(statuses)) +
		`,"statuses":` + ghtest.JSONArray(statuses...) + `}`
}

// workflowRunJSON is one run of the workflow-runs read: a run of the workflow name, started by
// event, in status, whose checks belong to the check suite suite.
func workflowRunJSON(suite int, name, event, status string) string {
	return `{"id":` + strconv.Itoa(suite+9000) + `,"name":"` + name + `","event":"` + event + `","status":"` + status +
		`","check_suite_id":` + strconv.Itoa(suite) + `}`
}

func workflowRunsPage(runs ...string) string { return pageJSON("workflow_runs", runs...) }

// commitReads scripts the check-runs, combined status and workflow-runs reads of the head commit
// to answer checkRuns, statuses and runs.
func commitReads(checkRuns, statuses, runs string) []ghtest.Call {
	return []ghtest.Call{
		ghtest.Says(checkRuns, checkRunsArgs()...),
		ghtest.Says(statuses, commitStatusArgs()...),
		ghtest.Says(runs, workflowRunsArgs()...),
	}
}

// pushCI is the workflow-runs read's answer when the one run on the commit is the completed push
// run of CI, whose check suite is ciSuite.
func pushCI() string { return workflowRunsPage(workflowRunJSON(ciSuite, "CI", "push", "completed")) }

// failingBuild is the check-runs read's answer when the one check run on the commit is the failed
// build job of the check suite ciSuite.
func failingBuild() string { return checkRunsPage(checkRunJSON(1, ciSuite, "build", "failure")) }

// sameChecks compares checks by workflow, name, bucket and required, in any order.
func sameChecks() cmp.Options {
	return cmp.Options{
		cmpopts.IgnoreFields(forge.Check{}, "Link", "StartedAt"),
		cmpopts.SortSlices(func(a, b forge.Check) bool {
			return a.Workflow+"\x00"+a.Name+"\x00"+string(a.Bucket) < b.Workflow+"\x00"+b.Name+"\x00"+string(b.Bucket)
		}),
		cmpopts.EquateEmpty(),
	}
}

// eachCommitRead is each read of a commit's checks, made by run, with the other two answering
// empty pages.
func eachCommitRead(run func(ctx context.Context, f *github.Forge) error) []readCase {
	checkRuns := ghtest.Says(checkRunsPage(), checkRunsArgs()...)
	statuses := ghtest.Says(statusPage(), commitStatusArgs()...)
	runs := ghtest.Says(workflowRunsPage(), workflowRunsArgs()...)

	return []readCase{
		{name: "check runs", run: run, args: checkRunsArgs(), before: []ghtest.Call{statuses, runs}},
		{name: "statuses", run: run, args: commitStatusArgs(), before: []ghtest.Call{checkRuns, runs}},
		{name: "workflow runs", run: run, args: workflowRunsArgs(), before: []ghtest.Call{checkRuns, statuses}},
	}
}

// ---------------------------------------------------------------------------
// Verdict
// ---------------------------------------------------------------------------

func verdictQuery(required ...string) forge.VerdictQuery {
	return forge.VerdictQuery{SHA: headSHA, Number: pullNumber, Required: required}
}

// verdictRun reads the verdict of the head commit, reduced to its error.
func verdictRun(ctx context.Context, f *github.Forge) error {
	_, err := f.Verdict(ctx, engine(), verdictQuery())
	return err
}

// verdictOf reads the verdict of the head commit, asking for required as required, while gh
// answers the commit's reads as reads script them (see commitReads).
func verdictOf(t *testing.T, reads []ghtest.Call, required ...string) forge.Verdict {
	t.Helper()
	ghtest.Setup(t, reads...)

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery(required...))
	assert.NilError(t, err)

	return got
}

func TestForge_Verdict_WhenRunsOfSeveralEventsReportChecks_KeepsTheChecksOfEachEvent(t *testing.T) {
	tests := []struct {
		name      string
		checkRuns string
		runs      string
		want      []forge.Check
	}{
		{
			name: "red push run beside a re-run green pull_request run of the same workflow",
			checkRuns: checkRunsPage(
				checkRunJSON(1, ciSuite, "build", "failure"),
				checkRunJSON(2, otherSuite, "build", "failure"),
				checkRunJSON(3, otherSuite, "build", "success"),
			),
			runs: workflowRunsPage(
				workflowRunJSON(ciSuite, "CI", "push", "completed"),
				workflowRunJSON(otherSuite, "CI", "pull_request", "completed"),
			),
			want: []forge.Check{
				{Name: "build", Bucket: forge.BucketFail, Workflow: "CI"},
				{Name: "build", Bucket: forge.BucketPass, Workflow: "CI"},
			},
		},
		{
			name:      "run started by another run",
			checkRuns: checkRunsPage(checkRunJSON(1, ciSuite, "build", "success"), checkRunJSON(2, otherSuite, "scan", "failure")),
			runs: workflowRunsPage(
				workflowRunJSON(ciSuite, "CI", "push", "completed"),
				workflowRunJSON(otherSuite, "Deploy", "workflow_run", "completed"),
			),
			want: []forge.Check{
				{Name: "build", Bucket: forge.BucketPass, Workflow: "CI"},
				{Name: "scan", Bucket: forge.BucketFail, Workflow: "Deploy"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verdictOf(t, commitReads(tt.checkRuns, statusPage(), tt.runs))

			assert.Check(t, is.DeepEqual(got.Checks, tt.want, sameChecks()))
		})
	}
}

func TestForge_Verdict_WhenRunsOfAnyEventAreOnTheCommit_ListsEachRunWithItsStatus(t *testing.T) {
	runs := workflowRunsPage(
		workflowRunJSON(ciSuite, "CI", "push", "queued"),
		workflowRunJSON(otherSuite, "Lint", "pull_request", "in_progress"),
		workflowRunJSON(thirdSuite, "Deploy", "workflow_run", "completed"),
	)

	got := verdictOf(t, commitReads(checkRunsPage(), statusPage(), runs))

	want := []forge.Run{
		{Name: "CI", Status: forge.RunQueued},
		{Name: "Deploy", Status: forge.RunCompleted},
		{Name: "Lint", Status: forge.RunInProgress},
	}
	assert.Check(t, is.DeepEqual(got.Runs, want, cmpopts.SortSlices(func(a, b forge.Run) bool { return a.Name < b.Name })))
}

// requiredVerdict reads the verdict of a commit whose checks are lint failing and test passing,
// asking for build, test and deploy as required.
func requiredVerdict(t *testing.T) forge.Verdict {
	t.Helper()
	checkRuns := checkRunsPage(checkRunJSON(1, ciSuite, "lint", "failure"), checkRunJSON(2, ciSuite, "test", "success"))

	return verdictOf(t, commitReads(checkRuns, statusPage(), pushCI()), "build", "test", "deploy")
}

func TestForge_Verdict_WhenQueryNamesRequiredChecks_MarksReportedRequiredChecks(t *testing.T) {
	got := requiredVerdict(t)

	want := []forge.Check{
		{Name: "lint", Bucket: forge.BucketFail, Workflow: "CI"},
		{Name: "test", Bucket: forge.BucketPass, Workflow: "CI", Required: true},
	}
	assert.Check(t, is.DeepEqual(got.Checks, want, sameChecks()))
}

func TestForge_Verdict_WhenQueryNamesRequiredChecks_ListsRequiredChecksWithNoReport(t *testing.T) {
	got := requiredVerdict(t)

	assert.Check(t, is.DeepEqual(got.MissingRequired, []string{"build", "deploy"}))
}

func logArgs() []string { return ghtest.LogArgs(engineSlug, runID, jobID) }

func runStatusArgs() []string { return apiArgs("actions/runs/" + runID) }

// runStatusAnswer is the run read's answer for the run the failing check belongs to, in status.
func runStatusAnswer(status string) ghtest.Answer {
	return ghtest.Answer{Stdout: `{"id":` + runID + `,"status":"` + status + `"}`}
}

func jobLogArgs() []string {
	return []string{"api", "--hostname", engineHost, "repos/ada/engine/actions/jobs/" + jobID + "/logs"}
}

// failedLogRow is a verdict whose check runs and statuses name the log to read, the answers gh
// gives to the reads of that log, and the failed log the forge returns.
type failedLogRow struct {
	name      string
	checkRuns string
	// statuses is the combined status read's answer; empty, it lists no status.
	statuses string
	want     forge.FailedLog
	// run is the answer to the read of the status of the run runLink names.
	run ghtest.Answer
	log ghtest.Answer
	// jobLog is the answer to the read of the log of the job runLink names.
	jobLog ghtest.Answer
}

// failedLogRows is every failed-log case Verdict reads behind its checks.
func failedLogRows() []failedLogRow {
	const (
		logText = "build\tRun tests\t--- FAIL: TestForge (0.01s)\nbuild\tRun tests\tFAIL\n"
		jobText = "2026-10-22T10:00:00.0000000Z --- FAIL: TestForge (0.01s)\n"
		// runOnlyLink has no /job/ segment, so it names no job whose log to read.
		runOnlyLink = "https://git.example.com/ada/engine/actions/runs/" + runID
	)

	failing := failingBuild()
	empty := ghtest.Answer{}
	logged := ghtest.Answer{Stdout: logText}
	// blank is output of only whitespace, which counts as no log, like empty output.
	blank := ghtest.Answer{Stdout: " \n\t\n"}
	jobLogged := ghtest.Answer{Stdout: jobText}
	jobGone := notFoundAnswer()
	gone := ghtest.Answer{Stderr: "run " + runID + " not found\n", Code: 1}
	completed := runStatusAnswer("completed")
	read := forge.FailedLog{Text: logText, State: forge.FailedLogRead}
	jobRead := forge.FailedLog{Text: jobText, State: forge.FailedLogRead}
	noRun := forge.FailedLog{State: forge.FailedLogNoRun}
	unreadable := forge.FailedLog{State: forge.FailedLogUnreadable}
	inProgress := forge.FailedLog{State: forge.FailedLogInProgress}

	return []failedLogRow{
		{name: "failing check of a completed run", checkRuns: failing, run: completed, log: logged, want: read},
		{name: "failing status with no run", checkRuns: checkRunsPage(), statuses: statusPage(statusJSON("scan", "failure")), log: logged, want: noRun},
		{name: "run view fails for a completed run", checkRuns: failing, run: completed, log: gone, want: unreadable},
		// The failing check's own job has completed while other jobs of its run still go.
		{name: "run of the failing check not completed", checkRuns: failing, run: runStatusAnswer("in_progress"), log: logged, want: inProgress},
		{name: "run status read answered 4xx", checkRuns: failing, run: notFoundAnswer(), log: logged, want: unreadable},
		{name: "failed log only whitespace reads the job log", checkRuns: failing, run: completed, log: blank, jobLog: jobLogged, want: jobRead},
		{name: "failed log empty and job log only whitespace", checkRuns: failing, run: completed, log: empty, jobLog: blank, want: inProgress},
		{name: "failed log empty and job log read fails", checkRuns: failing, run: completed, log: empty, jobLog: jobGone, want: inProgress},
		{name: "link names no job", checkRuns: checkRunsPage(checkRunAt(checkRun{id: 1, suite: ciSuite, name: "build", conclusion: "failure", link: runOnlyLink})), run: completed, log: logged, jobLog: jobLogged, want: unreadable},
		{name: "check of a run still pending", checkRuns: checkRunsPage(checkRunJSON(1, ciSuite, "build", "")), log: logged},
		// A canceled check never gets a log: its empty log reads would otherwise count as one to come.
		{name: "canceled check of a run reads no log", checkRuns: checkRunsPage(checkRunJSON(1, ciSuite, "build", "cancelled")), log: empty, jobLog: empty}, //nolint:misspell // GitHub's REST API spells the conclusion this way.
	}
}

func TestForge_Verdict_FailedLog(t *testing.T) {
	for _, tt := range failedLogRows() {
		t.Run(tt.name, func(t *testing.T) {
			statuses := tt.statuses
			if statuses == "" {
				statuses = statusPage()
			}

			logReads := []ghtest.Call{ghtest.Exits(tt.run, runStatusArgs()...), ghtest.Exits(tt.log, logArgs()...), ghtest.Exits(tt.jobLog, jobLogArgs()...)}
			ghtest.Setup(t, append(commitReads(tt.checkRuns, statuses, pushCI()), logReads...)...)

			synctest.Test(t, func(t *testing.T) {
				got, err := newForge().Verdict(t.Context(), engine(), verdictQuery())
				assert.NilError(t, err)

				assert.Check(t, is.DeepEqual(got.FailedLog, tt.want))
			})
		})
	}
}

func TestForge_Verdict_WhenACutEndsTheFailedLogRead_ReportsTheLogUnreadable(t *testing.T) {
	completedRun := ghtest.Exits(runStatusAnswer("completed"), runStatusArgs()...)
	logged := ghtest.Says("build\tRun tests\tFAIL\n", logArgs()...)
	tests := []struct {
		err  error
		name string
		// held is the read whose answer the cut ends; other answers the other read of the failed log.
		held  []string
		other ghtest.Call
	}{
		{name: "the time limit during the run status read", err: context.DeadlineExceeded, held: runStatusArgs(), other: logged},
		{name: "an interrupt during the log read", err: context.Canceled, held: logArgs(), other: completedRun},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failing := failingBuild()
			// The held read holds its answer open far longer than the test runs: only the cut ends it.
			held := ghtest.Answer{Delay: time.Hour}
			fake := ghtest.Setup(t, append(commitReads(failing, statusPage(), pushCI()), tt.other, ghtest.Exits(held, tt.held...))...)
			cut := make(chan struct{})
			fake.WhileHolding(sync.OnceValue(func() error {
				close(cut)
				return nil
			}))
			ctx := gittest.Ending(t.Context(), cut, tt.err)

			got, err := newForge().Verdict(ctx, engine(), verdictQuery())
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got.FailedLog, forge.FailedLog{State: forge.FailedLogUnreadable}))
		})
	}
}

func TestForge_Verdict_WhenTheTimeLimitEndsTheJobLogRead_ReportsTheLogUnreadable(t *testing.T) {
	completedRun := ghtest.Exits(runStatusAnswer("completed"), runStatusArgs()...)
	// An empty failed log sends the read on to the job's own log, which the cut ends.
	emptyLog := ghtest.Says("", logArgs()...)
	held := ghtest.Exits(ghtest.Answer{Delay: time.Hour}, jobLogArgs()...)
	fake := ghtest.Setup(t, append(commitReads(failingBuild(), statusPage(), pushCI()), completedRun, emptyLog, held)...)
	cut := make(chan struct{})
	fake.WhileHolding(sync.OnceValue(func() error {
		close(cut)
		return nil
	}))
	ctx := gittest.Ending(t.Context(), cut, context.DeadlineExceeded)

	got, err := newForge().Verdict(ctx, engine(), verdictQuery())
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got.FailedLog, forge.FailedLog{State: forge.FailedLogUnreadable}))
}

func TestForge_Verdict_WhenAReadExitsNonZero_ReturnsExitErrorNamingTheRead(t *testing.T) {
	for _, tt := range eachCommitRead(verdictRun) {
		t.Run(tt.name, func(t *testing.T) {
			assertExitErrorNamesCall(t, tt)
		})
	}
}

func TestForge_Verdict_WhenCheckRunsReadPrintsMalformedJSON_ReturnsDecodeErrorNamingIt(t *testing.T) {
	assertDecodeErrorNamesCall(t, eachCommitRead(verdictRun)[0])
}

// ---------------------------------------------------------------------------
// Commit verdict
// ---------------------------------------------------------------------------

// failedLogText is the failed log of the job runLink names, as failedLogReads answers it.
const failedLogText = "build\tRun tests\t--- FAIL: TestRelease (0.01s)\n"

// failedLogReads scripts the reads of the failed log of the job runLink names: its run has
// completed and its failed log is failedLogText.
func failedLogReads() []ghtest.Call {
	return []ghtest.Call{
		ghtest.Exits(runStatusAnswer("completed"), runStatusArgs()...),
		ghtest.Says(failedLogText, logArgs()...),
	}
}

// logReadsAmong is the calls among calls that read a run's status or a job's log.
func logReadsAmong(calls [][]string) [][]string {
	var reads [][]string
	for _, call := range calls {
		for _, args := range [][]string{runStatusArgs(), logArgs(), jobLogArgs()} {
			if ghtest.Matches(ghArgv(args), call) {
				reads = append(reads, call)
			}
		}
	}

	return reads
}

// commitVerdict reads the verdict of the head commit while gh answers the check-runs, combined
// status and workflow-runs reads with checkRuns, statuses and runs, and the reads of the failed
// log as failedLogReads scripts them.
func commitVerdict(t *testing.T, checkRuns, statuses, runs string) forge.Verdict {
	t.Helper()
	ghtest.Setup(t, append(commitReads(checkRuns, statuses, runs), failedLogReads()...)...)

	got, err := newForge().CommitVerdict(t.Context(), engine(), headSHA)
	assert.NilError(t, err)

	return got
}

func TestForge_CommitVerdict_WhenARedCheckRunIsOnTheCommit_KeepsItUnlessItsRunIsOfAnotherEvent(t *testing.T) {
	build := forge.Check{Name: "build", Bucket: forge.BucketPass, Workflow: "CI"}
	tests := []struct {
		name string
		// run is the workflow run of the red check run's check suite; empty when it has none.
		run  string
		want []forge.Check
	}{
		{name: "run started by another run", run: workflowRunJSON(otherSuite, "Deploy", "workflow_run", "completed"), want: []forge.Check{build}},
		{name: "GitHub App with no workflow run", want: []forge.Check{build, {Name: "scan", Bucket: forge.BucketFail}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkRuns := checkRunsPage(checkRunJSON(1, ciSuite, "build", "success"), checkRunJSON(2, otherSuite, "scan", "failure"))
			runs := []string{workflowRunJSON(ciSuite, "CI", "push", "completed")}
			if tt.run != "" {
				runs = append(runs, tt.run)
			}

			got := commitVerdict(t, checkRuns, statusPage(), workflowRunsPage(runs...))

			assert.Check(t, is.DeepEqual(got.Checks, tt.want, sameChecks()))
		})
	}
}

func TestForge_CommitVerdict_WhenTwoWorkflowsHaveAJobOfTheSameName_KeepsOneCheckForEach(t *testing.T) {
	checkRuns := checkRunsPage(checkRunJSON(1, ciSuite, "test", "failure"), checkRunJSON(2, otherSuite, "test", "success"))
	runs := workflowRunsPage(workflowRunJSON(ciSuite, "CI", "push", "completed"), workflowRunJSON(otherSuite, "Lint", "push", "completed"))

	got := commitVerdict(t, checkRuns, statusPage(), runs)

	want := []forge.Check{{Name: "test", Bucket: forge.BucketFail, Workflow: "CI"}, {Name: "test", Bucket: forge.BucketPass, Workflow: "Lint"}}
	assert.Check(t, is.DeepEqual(got.Checks, want, sameChecks()))
}

func TestForge_CommitVerdict_WhenAWorkflowReportsACheckTwice_KeepsTheMostRecent(t *testing.T) {
	failed := checkRunJSON(1, ciSuite, "test", "failure")
	rerun := checkRunJSON(2, ciSuite, "test", "success")
	tests := []struct {
		name      string
		checkRuns string
	}{
		{name: "re-run listed first", checkRuns: checkRunsPage(rerun, failed)},
		{name: "re-run listed last", checkRuns: checkRunsPage(failed, rerun)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := commitVerdict(t, tt.checkRuns, statusPage(), pushCI())

			want := []forge.Check{{Name: "test", Bucket: forge.BucketPass, Workflow: "CI"}}
			assert.Check(t, is.DeepEqual(got.Checks, want, sameChecks()))
		})
	}
}

func TestForge_CommitVerdict_WhenACommitStatusIsReported_BucketsItsState(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  forge.Bucket
	}{
		{name: "success", state: "success", want: forge.BucketPass},
		{name: "failure", state: "failure", want: forge.BucketFail},
		{name: "state this version does not know", state: "stale", want: forge.BucketPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := commitVerdict(t, checkRunsPage(), statusPage(statusJSON("ci/scan", tt.state)), workflowRunsPage())

			want := []forge.Check{{Name: "ci/scan", Bucket: tt.want}}
			assert.Check(t, is.DeepEqual(got.Checks, want, sameChecks()))
		})
	}
}

func TestForge_CommitVerdict_WhenACheckRunIsReported_BucketsItsOutcome(t *testing.T) {
	tests := []struct {
		name       string
		conclusion string
		want       forge.Bucket
	}{
		{name: "success", conclusion: "success", want: forge.BucketPass},
		{name: "skipped", conclusion: "skipped", want: forge.BucketSkipped},
		{name: "canceled", conclusion: "cancelled", want: forge.BucketCanceled}, //nolint:misspell // GitHub's REST API spells the conclusion this way.
		{name: "failure", conclusion: "failure", want: forge.BucketFail},
		{name: "conclusion this version does not know", conclusion: "mystery", want: forge.BucketPending},
		{name: "not completed", want: forge.BucketPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkRuns := checkRunsPage(checkRunJSON(1, ciSuite, "build", tt.conclusion))

			got := commitVerdict(t, checkRuns, statusPage(), workflowRunsPage(workflowRunJSON(ciSuite, "CI", "push", "in_progress")))

			want := []forge.Check{{Name: "build", Bucket: tt.want, Workflow: "CI"}}
			assert.Check(t, is.DeepEqual(got.Checks, want, sameChecks()))
		})
	}
}

func TestForge_CommitVerdict_WhenRunsAreOnTheCommit_ListsThePushRunsWithTheirStatus(t *testing.T) {
	runs := workflowRunsPage(
		workflowRunJSON(ciSuite, "CI", "push", "queued"),
		workflowRunJSON(otherSuite, "Deploy", "workflow_run", "in_progress"),
	)

	got := commitVerdict(t, checkRunsPage(), statusPage(), runs)

	want := []forge.Run{{Name: "CI", Status: forge.RunQueued}}
	assert.Check(t, is.DeepEqual(got.Runs, want, cmpopts.SortSlices(func(a, b forge.Run) bool { return a.Name < b.Name })))
}

func TestForge_CommitVerdict_WhenAReadSpansTwoPages_ReadsEveryPage(t *testing.T) {
	ci := workflowRunJSON(ciSuite, "CI", "push", "completed")
	tests := []struct {
		name      string
		checkRuns string
		statuses  string
		runs      string
		want      forge.Verdict
	}{
		{
			name:      "check runs",
			checkRuns: checkRunsPage(checkRunJSON(1, ciSuite, "build", "success")) + checkRunsPage(checkRunJSON(2, ciSuite, "test", "failure")),
			statuses:  statusPage(),
			runs:      workflowRunsPage(ci),
			want: forge.Verdict{
				Checks: []forge.Check{{Name: "build", Bucket: forge.BucketPass, Workflow: "CI"}, {Name: "test", Bucket: forge.BucketFail, Workflow: "CI"}},
				Runs:   []forge.Run{{Name: "CI", Status: forge.RunCompleted}},
			},
		},
		{
			name:      "statuses",
			checkRuns: checkRunsPage(),
			statuses:  statusPage(statusJSON("ci/scan", "success")) + statusPage(statusJSON("ci/audit", "failure")),
			runs:      workflowRunsPage(),
			want:      forge.Verdict{Checks: []forge.Check{{Name: "ci/audit", Bucket: forge.BucketFail}, {Name: "ci/scan", Bucket: forge.BucketPass}}},
		},
		{
			name:      "workflow runs",
			checkRuns: checkRunsPage(checkRunJSON(1, otherSuite, "lint", "success")),
			statuses:  statusPage(),
			runs:      workflowRunsPage(ci) + workflowRunsPage(workflowRunJSON(otherSuite, "Lint", "push", "completed")),
			want: forge.Verdict{
				Checks: []forge.Check{{Name: "lint", Bucket: forge.BucketPass, Workflow: "Lint"}},
				Runs:   []forge.Run{{Name: "CI", Status: forge.RunCompleted}, {Name: "Lint", Status: forge.RunCompleted}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := commitVerdict(t, tt.checkRuns, tt.statuses, tt.runs)

			assert.Check(t, is.DeepEqual(got, tt.want, sameChecks(), cmpopts.IgnoreFields(forge.Verdict{}, "FailedLog")))
		})
	}
}

func TestForge_CommitVerdict_WhenACheckFails_CarriesItsFailedLog(t *testing.T) {
	got := commitVerdict(t, failingBuild(), statusPage(), pushCI())

	assert.Check(t, is.DeepEqual(got.FailedLog, forge.FailedLog{Text: failedLogText, State: forge.FailedLogRead}))
}

func TestForge_CommitVerdict_WhenACutEndsTheFailedLogRead_ReportsTheLogUnreadable(t *testing.T) {
	// The log read holds its answer open far longer than the test runs: only the cut ends it.
	held := ghtest.Exits(ghtest.Answer{Delay: time.Hour}, logArgs()...)
	completedRun := ghtest.Exits(runStatusAnswer("completed"), runStatusArgs()...)
	fake := ghtest.Setup(t, append(commitReads(failingBuild(), statusPage(), pushCI()), completedRun, held)...)
	cut := make(chan struct{})
	fake.WhileHolding(sync.OnceValue(func() error {
		close(cut)
		return nil
	}))
	ctx := gittest.Ending(t.Context(), cut, context.DeadlineExceeded)

	got, err := newForge().CommitVerdict(ctx, engine(), headSHA)
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got.FailedLog, forge.FailedLog{State: forge.FailedLogUnreadable}))
}

func TestForge_CommitVerdict_WhenNoCheckFails_ReadsNoLog(t *testing.T) {
	tests := []struct {
		name       string
		conclusion string
	}{
		{name: "passing", conclusion: "success"},
		{name: "pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkRuns := checkRunsPage(checkRunJSON(1, ciSuite, "build", tt.conclusion))
			fake := ghtest.Setup(t, append(commitReads(checkRuns, statusPage(), pushCI()), failedLogReads()...)...)

			_, err := newForge().CommitVerdict(t.Context(), engine(), headSHA)
			assert.NilError(t, err)

			assert.Check(t, is.Len(logReadsAmong(fake.Calls()), 0))
		})
	}
}
