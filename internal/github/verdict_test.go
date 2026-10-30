package github_test

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
)

func TestForge_Verdict_WhenChecksAreReported_ReadsEachBucketWhateverGhExitsWith(t *testing.T) {
	tests := []struct {
		name   string
		bucket string
		want   forge.Bucket
		code   int
	}{
		{name: "pass", bucket: "pass", code: 0, want: forge.BucketPass},
		{name: "fail exits 1", bucket: "fail", code: 1, want: forge.BucketFail},
		{name: "skipping", bucket: "skipping", code: 0, want: forge.BucketSkipped},
		{name: "cancel exits 0", bucket: "cancel", code: 0, want: forge.BucketCanceled},
		{name: "bucket this version does not know", bucket: "stale", code: 0, want: forge.BucketPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks := ghtest.Answer{Stdout: ghtest.JSONArray(ghtest.CheckJSON("build", tt.bucket, statusLink)), Stderr: "notice\n", Code: tt.code}
			ghtest.Setup(t, ghtest.Exits(checks, checksArgs()...), ghtest.Says("[]", runsArgs()...))

			got, err := newForge().Verdict(t.Context(), engine(), verdictQuery())
			assert.NilError(t, err)

			want := []forge.Check{{Name: "build", Bucket: tt.want, Workflow: "CI", Link: statusLink}}
			assert.Check(t, is.DeepEqual(got.Checks, want))
		})
	}
}

const statusLink = "https://ci.example.com/builds/15"

func logArgs() []string { return ghtest.LogArgs(engineSlug, runID) }

func jobLogArgs() []string {
	return []string{"api", "--hostname", engineHost, "repos/ada/engine/actions/jobs/" + jobID + "/logs"}
}

func noChecksAnswer() ghtest.Answer {
	return ghtest.Answer{Stderr: "no checks reported on the 'feature' branch\n", Code: 1}
}

func TestForge_Verdict_WhenNoCheckReported_ReturnsZeroChecks(t *testing.T) {
	ghtest.Setup(t, ghtest.Exits(noChecksAnswer(), checksArgs()...), ghtest.Says("[]", runsArgs()...))

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery())
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got, forge.Verdict{}, cmpopts.EquateEmpty()))
}

func TestForge_Verdict_WhenCommitHasRuns_ReturnsEachRunStatus(t *testing.T) {
	runs := `[
		{"status":"queued","name":"Add the forge","workflowName":"CI"},
		{"status":"in_progress","name":"Add the forge","workflowName":"Lint"},
		{"status":"waiting","name":"Add the forge","workflowName":"Preview"},
		{"status":"completed","name":"Add the forge","workflowName":"Build"}
	]`
	ghtest.Setup(t, ghtest.Says("[]", checksArgs()...), ghtest.Says(runs, runsArgs()...))

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery())
	assert.NilError(t, err)

	want := []forge.Run{
		{Name: "CI", Status: forge.RunQueued},
		{Name: "Lint", Status: forge.RunInProgress},
		{Name: "Preview", Status: forge.RunAwaitingPerson},
		{Name: "Build", Status: forge.RunCompleted},
	}
	assert.Check(t, is.DeepEqual(got.Runs, want))
}

func TestForge_Verdict_WhenRunHasNoWorkflowName_NamesItByTheRun(t *testing.T) {
	runs := `[{"status":"completed","name":"Scan","workflowName":""}]`
	ghtest.Setup(t, ghtest.Says("[]", checksArgs()...), ghtest.Says(runs, runsArgs()...))

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery())
	assert.NilError(t, err)

	want := []forge.Run{{Name: "Scan", Status: forge.RunCompleted}}
	assert.Check(t, is.DeepEqual(got.Runs, want))
}

// requiredVerdict reads the verdict of a pull request whose checks are lint failing and test
// passing, asking for build, test and deploy as required.
func requiredVerdict(t *testing.T) forge.Verdict {
	t.Helper()
	checks := ghtest.JSONArray(ghtest.CheckJSON("lint", "fail", runLink), ghtest.CheckJSON("test", "pass", statusLink))
	ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: checks, Code: 1}, checksArgs()...), ghtest.Says("[]", runsArgs()...))

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery("build", "test", "deploy"))
	assert.NilError(t, err)

	return got
}

func TestForge_Verdict_WhenQueryNamesRequiredChecks_MarksReportedRequiredChecks(t *testing.T) {
	got := requiredVerdict(t)

	want := []forge.Check{
		{Name: "lint", Bucket: forge.BucketFail, Workflow: "CI", Link: runLink},
		{Name: "test", Bucket: forge.BucketPass, Workflow: "CI", Link: statusLink, Required: true},
	}
	assert.Check(t, is.DeepEqual(got.Checks, want))
}

func TestForge_Verdict_WhenQueryNamesRequiredChecks_ListsRequiredChecksWithNoReport(t *testing.T) {
	got := requiredVerdict(t)

	assert.Check(t, is.DeepEqual(got.MissingRequired, []string{"build", "deploy"}))
}

// failedLogRow is a verdict whose checks name the log to read, the answers gh gives to the reads of
// that log, and the failed log the forge returns.
type failedLogRow struct {
	name   string
	checks string
	// wantCalls is every argv gh receives, in any order: the log is read only for a failing check of
	// a run.
	wantCalls [][]string
	want      forge.FailedLog
	log       ghtest.Answer
	// jobLog is the answer to the read of the log of the job runLink names.
	jobLog ghtest.Answer
}

// failedLogRows is every failed-log case Verdict reads behind its checks.
func failedLogRows() []failedLogRow {
	const (
		logText = "build\tRun tests\t--- FAIL: TestForge (0.01s)\nbuild\tRun tests\tFAIL\n"
		jobText = "2026-10-22T10:00:00.0000000Z --- FAIL: TestForge (0.01s)\n"
		// runOnlyLink has no /job/ segment, so failedLog has no job log to fall back to.
		runOnlyLink = "https://git.example.com/ada/engine/actions/runs/" + runID
	)

	failing := ghtest.CheckJSON("build", "fail", runLink)
	empty := ghtest.Answer{}
	logged := ghtest.Answer{Stdout: logText}
	// blank is output of only whitespace, which counts as no log, like empty output.
	blank := ghtest.Answer{Stdout: " \n\t\n"}
	jobLogged := ghtest.Answer{Stdout: jobText}
	jobGone := notFoundAnswer()
	gone := ghtest.Answer{Stderr: "run " + runID + " not found\n", Code: 1}
	// unfinished is what gh run view --log-failed says of a run that has not completed.
	unfinished := ghtest.Answer{Stderr: "run " + runID + " is still in progress; logs will be available when it is complete\n", Code: 1}
	read := forge.FailedLog{Check: "build", Text: logText, State: forge.FailedLogRead}
	jobRead := forge.FailedLog{Check: "build", Text: jobText, State: forge.FailedLogRead}
	noRun := forge.FailedLog{Check: "scan", State: forge.FailedLogNoRun}
	unreadable := forge.FailedLog{Check: "build", State: forge.FailedLogUnreadable}
	inProgress := forge.FailedLog{Check: "build", State: forge.FailedLogInProgress}
	withLog := received(checksArgs(), runsArgs(), logArgs())
	noLog := received(checksArgs(), runsArgs())
	withJobLog := received(checksArgs(), runsArgs(), logArgs(), jobLogArgs())

	return []failedLogRow{
		{name: "failing check of a run", checks: failing, log: logged, want: read, wantCalls: withLog},
		{name: "failing status with no run", checks: ghtest.CheckJSON("scan", "fail", statusLink), log: logged, want: noRun, wantCalls: noLog},
		{name: "run view fails", checks: failing, log: gone, want: unreadable, wantCalls: withLog},
		{name: "run of the failing check still in progress", checks: failing, log: unfinished, want: inProgress, wantCalls: withLog},
		{name: "failed log only whitespace reads the job log", checks: failing, log: blank, jobLog: jobLogged, want: jobRead, wantCalls: withJobLog},
		{name: "failed log empty and job log only whitespace", checks: failing, log: empty, jobLog: blank, want: inProgress, wantCalls: withJobLog},
		{name: "failed log empty and job log read fails", checks: failing, log: empty, jobLog: jobGone, want: inProgress, wantCalls: withJobLog},
		{name: "failed log empty and link names no job", checks: ghtest.CheckJSON("build", "fail", runOnlyLink), log: empty, jobLog: jobLogged, want: inProgress, wantCalls: withLog},
		{name: "check of a run still pending", checks: ghtest.CheckJSON("build", "pending", runLink), log: logged, wantCalls: noLog},
	}
}

func TestForge_Verdict_FailedLog(t *testing.T) {
	for _, tt := range failedLogRows() {
		t.Run(tt.name, func(t *testing.T) {
			fake := ghtest.Setup(t, ghtest.Says(ghtest.JSONArray(tt.checks), checksArgs()...), ghtest.Says("[]", runsArgs()...), ghtest.Exits(tt.log, logArgs()...), ghtest.Exits(tt.jobLog, jobLogArgs()...))

			got, err := newForge().Verdict(t.Context(), engine(), verdictQuery())
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got.FailedLog, tt.want))
			byArgv := cmpopts.SortSlices(func(a, b []string) bool { return slices.Compare(a, b) < 0 })
			scripted := cmp.Comparer(func(a, b []string) bool { return ghtest.Matches(a, b) || ghtest.Matches(b, a) })
			assert.Check(t, is.DeepEqual(fake.Calls(), tt.wantCalls, byArgv, scripted))
		})
	}
}

func TestForge_Verdict_WhenAReadExitsNonZero_ReturnsExitErrorNamingTheRead(t *testing.T) {
	verdict := operationsByName()["Verdict"]
	tests := []readCase{
		{name: "pr checks", run: verdict, args: checksArgs()},
		{name: "run list", run: verdict, args: runsArgs(), before: []ghtest.Call{ghtest.Says("[]", checksArgs()...)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertExitErrorNamesCall(t, tt)
		})
	}
}

func TestForge_Verdict_WhenChecksReadPrintsMalformedJSON_ReturnsDecodeErrorNamingIt(t *testing.T) {
	assertDecodeErrorNamesCall(t, readCase{run: operationsByName()["Verdict"], args: checksArgs()})
}

// ---------------------------------------------------------------------------
// Commit verdict
// ---------------------------------------------------------------------------

// The check suites of the workflow runs the commit verdict tests script.
const (
	ciSuite    = 11
	otherSuite = 12
)

// commitAPIArgs is the argv of the paginated read of rest under the engine repository.
func commitAPIArgs(rest string) []string {
	return []string{"api", "--hostname", engineHost, "--paginate", "repos/ada/engine/" + rest}
}

func checkRunsArgs() []string { return commitAPIArgs("commits/" + headSHA + "/check-runs") }

func commitStatusArgs() []string { return commitAPIArgs("commits/" + headSHA + "/status") }

func workflowRunsArgs() []string { return commitAPIArgs("actions/runs?head_sha=" + headSHA) }

// checkRunJSON is one check run of the check-runs read: the check run numbered id, named name, of
// the check suite suite, with conclusion; an empty conclusion is null and leaves the run in
// progress, any other completes it. A higher id is a later start.
func checkRunJSON(id, suite int, name, conclusion string) string {
	status, concluded := "in_progress", "null"
	if conclusion != "" {
		status, concluded = "completed", `"`+conclusion+`"`
	}

	return `{"id":` + strconv.Itoa(id) + `,"name":"` + name + `","status":"` + status + `","conclusion":` + concluded +
		`,"started_at":"2026-10-22T10:` + fmt.Sprintf("%02d", id) + `:00Z","html_url":"` + runLink +
		`","check_suite":{"id":` + strconv.Itoa(suite) + `}}`
}

func checkRunsPage(runs ...string) string {
	return `{"total_count":` + strconv.Itoa(len(runs)) + `,"check_runs":` + ghtest.JSONArray(runs...) + `}`
}

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

func workflowRunsPage(runs ...string) string {
	return `{"total_count":` + strconv.Itoa(len(runs)) + `,"workflow_runs":` + ghtest.JSONArray(runs...) + `}`
}

// commitVerdict reads the verdict of the head commit while gh answers the check-runs, combined
// status and workflow-runs reads with checkRuns, statuses and runs.
func commitVerdict(t *testing.T, checkRuns, statuses, runs string) forge.Verdict {
	t.Helper()
	ghtest.Setup(t,
		ghtest.Says(checkRuns, checkRunsArgs()...),
		ghtest.Says(statuses, commitStatusArgs()...),
		ghtest.Says(runs, workflowRunsArgs()...),
	)

	got, err := newForge().CommitVerdict(t.Context(), engine(), headSHA)
	assert.NilError(t, err)

	return got
}

// sameChecks compares checks by workflow, name, bucket and required, in any order.
func sameChecks() cmp.Options {
	return cmp.Options{
		cmpopts.IgnoreFields(forge.Check{}, "Link", "StartedAt"),
		cmpopts.SortSlices(func(a, b forge.Check) bool { return a.Workflow+"\x00"+a.Name < b.Workflow+"\x00"+b.Name }),
		cmpopts.EquateEmpty(),
	}
}

func TestForge_CommitVerdict_WhenARedCheckRunIsOnTheCommit_KeepsItUnlessItsRunIsOfAnotherEvent(t *testing.T) {
	build := forge.Check{Name: "build", Bucket: forge.BucketPass, Workflow: "CI"}
	tests := []struct {
		name string
		// run is the workflow run of the red check run's check suite; empty when it has none.
		run  string
		want []forge.Check
	}{
		{name: "scheduled run", run: workflowRunJSON(otherSuite, "Nightly", "schedule", "completed"), want: []forge.Check{build}},
		{name: "run started by hand", run: workflowRunJSON(otherSuite, "Release", "workflow_dispatch", "completed"), want: []forge.Check{build}},
		{name: "Dependabot run", run: workflowRunJSON(otherSuite, "Dependabot Updates", "dynamic", "completed"), want: []forge.Check{build}},
		{
			name: "run started by another run",
			run:  workflowRunJSON(otherSuite, "Deploy", "workflow_run", "completed"),
			want: []forge.Check{build, {Name: "scan", Bucket: forge.BucketFail, Workflow: "Deploy"}},
		},
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
			got := commitVerdict(t, tt.checkRuns, statusPage(), workflowRunsPage(workflowRunJSON(ciSuite, "CI", "push", "completed")))

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
		{name: "pending", state: "pending", want: forge.BucketPending},
		{name: "failure", state: "failure", want: forge.BucketFail},
		{name: "error", state: "error", want: forge.BucketFail},
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
		{name: "neutral", conclusion: "neutral", want: forge.BucketSkipped},
		{name: "canceled", conclusion: "cancelled", want: forge.BucketCanceled}, //nolint:misspell // GitHub's REST API spells the conclusion this way.
		{name: "failure", conclusion: "failure", want: forge.BucketFail},
		{name: "timed out", conclusion: "timed_out", want: forge.BucketFail},
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

func TestForge_CommitVerdict_WhenRunsAreOnTheCommit_ListsThePushAndWorkflowRunRunsWithTheirStatus(t *testing.T) {
	runs := workflowRunsPage(
		workflowRunJSON(ciSuite, "CI", "push", "queued"),
		workflowRunJSON(otherSuite, "Deploy", "workflow_run", "in_progress"),
		workflowRunJSON(13, "Nightly", "schedule", "in_progress"),
	)

	got := commitVerdict(t, checkRunsPage(), statusPage(), runs)

	want := []forge.Run{{Name: "CI", Status: forge.RunQueued}, {Name: "Deploy", Status: forge.RunInProgress}}
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

			assert.Check(t, is.DeepEqual(got, tt.want, sameChecks()))
		})
	}
}

// commitVerdictReads is each read of the commit verdict, with the other two answering empty pages.
func commitVerdictReads() []readCase {
	run := func(ctx context.Context, f *github.Forge) error {
		_, err := f.CommitVerdict(ctx, engine(), headSHA)
		return err
	}
	checkRuns := ghtest.Says(checkRunsPage(), checkRunsArgs()...)
	statuses := ghtest.Says(statusPage(), commitStatusArgs()...)
	runs := ghtest.Says(workflowRunsPage(), workflowRunsArgs()...)

	return []readCase{
		{name: "check runs", run: run, args: checkRunsArgs(), before: []ghtest.Call{statuses, runs}},
		{name: "statuses", run: run, args: commitStatusArgs(), before: []ghtest.Call{checkRuns, runs}},
		{name: "workflow runs", run: run, args: workflowRunsArgs(), before: []ghtest.Call{checkRuns, statuses}},
	}
}

func TestForge_CommitVerdict_WhenAReadExitsNonZero_ReturnsExitErrorNamingTheRead(t *testing.T) {
	for _, tt := range commitVerdictReads() {
		t.Run(tt.name, func(t *testing.T) {
			assertExitErrorNamesCall(t, tt)
		})
	}
}

func TestForge_CommitVerdict_WhenAReadPrintsMalformedJSON_ReturnsDecodeErrorNamingTheRead(t *testing.T) {
	for _, tt := range commitVerdictReads() {
		t.Run(tt.name, func(t *testing.T) {
			assertDecodeErrorNamesCall(t, tt)
		})
	}
}
