package github_test

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
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
