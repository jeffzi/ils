package github_test

import (
	"testing"

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
		{name: "pending exits 8", bucket: "pending", code: 8, want: forge.BucketPending},
		{name: "skipping", bucket: "skipping", code: 0, want: forge.BucketSkipped},
		{name: "cancel exits 0", bucket: "cancel", code: 0, want: forge.BucketCanceled},
		{name: "bucket this version does not know", bucket: "stale", code: 0, want: forge.BucketPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks := ghtest.Answer{Stdout: jsonArray(ghtest.CheckJSON("build", tt.bucket, statusLink)), Stderr: "notice\n", Code: tt.code}
			ghtest.Setup(t, ghtest.Exits(checks, checksArgs()...), ghtest.Says("[]", runsArgs()...))

			got, err := newForge().Verdict(t.Context(), engine(), verdictQuery())
			assert.NilError(t, err)

			want := []forge.Check{{Name: "build", Bucket: tt.want, Workflow: "CI", Link: statusLink}}
			assert.Check(t, is.DeepEqual(got.Checks, want))
		})
	}
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
		{"status":"requested","name":"Add the forge","workflowName":"Docs"},
		{"status":"pending","name":"Add the forge","workflowName":"Audit"},
		{"status":"completed","name":"Add the forge","workflowName":"Build"},
		{"status":"completed","name":"Scan","workflowName":""},
		{"status":"paused","name":"Add the forge","workflowName":"Hold"}
	]`
	ghtest.Setup(t, ghtest.Says("[]", checksArgs()...), ghtest.Says(runs, runsArgs()...))

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery())
	assert.NilError(t, err)

	want := []forge.Run{
		{Name: "CI", Status: forge.RunQueued},
		{Name: "Lint", Status: forge.RunInProgress},
		{Name: "Preview", Status: forge.RunAwaitingPerson},
		{Name: "Docs", Status: forge.RunAwaitingPerson},
		{Name: "Audit", Status: forge.RunQueued},
		{Name: "Build", Status: forge.RunCompleted},
		{Name: "Scan", Status: forge.RunCompleted},
		{Name: "Hold", Status: forge.RunInProgress},
	}
	assert.Check(t, is.DeepEqual(got.Runs, want))
}

func TestForge_Verdict_WhenQueryNamesRequiredChecks_CarriesEveryCheckAndListsTheMissing(t *testing.T) {
	checks := jsonArray(ghtest.CheckJSON("lint", "fail", runLink), ghtest.CheckJSON("test", "pass", statusLink))
	ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: checks, Code: 1}, checksArgs()...), ghtest.Says("[]", runsArgs()...))

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery("build", "test", "deploy"))
	assert.NilError(t, err)

	want := forge.Verdict{
		Checks: []forge.Check{
			{Name: "lint", Bucket: forge.BucketFail, Workflow: "CI", Link: runLink},
			{Name: "test", Bucket: forge.BucketPass, Workflow: "CI", Link: statusLink, Required: true},
		},
		MissingRequired: []string{"build", "deploy"},
	}
	assert.Check(t, is.DeepEqual(got, want, cmpopts.EquateEmpty()))
}

// jobLogArgs is the argv of the read of the log of the job numbered job, the one runLink names.
func jobLogArgs(job string) []string {
	return []string{"api", "--hostname", engineHost, "repos/ada/engine/actions/jobs/" + job + "/logs"}
}

// failedLogRow is a verdict whose checks name the log to read, the answers gh gives to the reads of
// that log, and the failed log the forge returns.
type failedLogRow struct {
	name   string
	checks string
	// wantCalls is every argv gh receives: the log is read only for a failing check of a run.
	wantCalls [][]string
	want      forge.FailedLog
	// required names the checks the query requires; none makes every check decide.
	required []string
	log      ghtest.Answer
	// jobLog is the answer to the read of the log of the job runLink names.
	jobLog ghtest.Answer
}

func failedLogRows() []failedLogRow {
	const (
		logText = "build\tRun tests\t--- FAIL: TestForge (0.01s)\nbuild\tRun tests\tFAIL\n"
		jobText = "2026-10-22T10:00:00.0000000Z --- FAIL: TestForge (0.01s)\n"
		// runOnlyLink has no /job/ segment, so failedLog has no job log to fall back to.
		runOnlyLink = "https://git.example.com/ada/engine/actions/runs/" + runID
		// jobID is the number of the job runLink names.
		jobID = "42"
	)

	failing := ghtest.CheckJSON("build", "fail", runLink)
	empty := ghtest.Answer{}
	logged := ghtest.Answer{Stdout: logText}
	// blank is output of only whitespace, which counts as no log, like empty output.
	blank := ghtest.Answer{Stdout: " \n\t\n"}
	jobLogged := ghtest.Answer{Stdout: jobText}
	jobGone := ghtest.Answer{Stderr: "gh: Not Found (HTTP 404)\n", Code: 1}
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
	withJobLog := received(checksArgs(), runsArgs(), logArgs(), jobLogArgs(jobID))

	return []failedLogRow{
		{name: "failing check of a run", checks: failing, log: logged, want: read, wantCalls: withLog},
		{name: "canceled check of a run", checks: ghtest.CheckJSON("build", "cancel", runLink), log: logged, want: read, wantCalls: withLog},
		{name: "passing check ahead of the failing one", checks: ghtest.CheckJSON("lint", "pass", statusLink) + "," + failing, log: logged, want: read, wantCalls: withLog},
		{name: "failing status ahead of a failing check of a run", checks: ghtest.CheckJSON("scan", "fail", statusLink) + "," + failing, log: logged, want: noRun, wantCalls: noLog},
		{name: "failing status with no run", checks: ghtest.CheckJSON("scan", "fail", statusLink), log: logged, want: noRun, wantCalls: noLog},
		{name: "run view fails", checks: failing, log: gone, want: unreadable, wantCalls: withLog},
		{name: "run of the failing check still in progress", checks: failing, log: unfinished, want: inProgress, wantCalls: withLog},
		{name: "failed log empty reads the job log", checks: failing, log: empty, jobLog: jobLogged, want: jobRead, wantCalls: withJobLog},
		{name: "failed log only whitespace reads the job log", checks: failing, log: blank, jobLog: jobLogged, want: jobRead, wantCalls: withJobLog},
		{name: "failed log and job log both empty", checks: failing, log: empty, jobLog: empty, want: inProgress, wantCalls: withJobLog},
		{name: "failed log empty and job log only whitespace", checks: failing, log: empty, jobLog: blank, want: inProgress, wantCalls: withJobLog},
		{name: "failed log empty and job log read fails", checks: failing, log: empty, jobLog: jobGone, want: inProgress, wantCalls: withJobLog},
		{name: "failed log empty and link names no job", checks: ghtest.CheckJSON("build", "fail", runOnlyLink), log: empty, jobLog: jobLogged, want: inProgress, wantCalls: withLog},
		{name: "check of a run still pending", checks: ghtest.CheckJSON("build", "pending", runLink), log: logged, wantCalls: noLog},
		{name: "every check passed", checks: ghtest.CheckJSON("build", "pass", runLink), log: logged, wantCalls: noLog},
		{
			name:     "first required failing check has no run",
			checks:   ghtest.CheckJSON("lint", "fail", runLink) + "," + ghtest.CheckJSON("scan", "fail", statusLink) + "," + failing,
			required: []string{"scan", "build"}, log: logged, want: noRun, wantCalls: noLog,
		},
		{
			name:     "failing check is not required",
			checks:   ghtest.CheckJSON("lint", "fail", runLink) + "," + ghtest.CheckJSON("build", "pass", runLink),
			required: []string{"build"}, log: logged, wantCalls: noLog,
		},
	}
}

func TestForge_Verdict(t *testing.T) {
	for _, tt := range failedLogRows() {
		t.Run(tt.name, func(t *testing.T) {
			fake := ghtest.Setup(t, ghtest.Says(jsonArray(tt.checks), checksArgs()...), ghtest.Says("[]", runsArgs()...), ghtest.Exits(tt.log, logArgs()...), ghtest.Exits(tt.jobLog, jobLogArgs("42")...))

			got, err := newForge().Verdict(t.Context(), engine(), verdictQuery(tt.required...))
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got.FailedLog, tt.want))
			assertCalls(t, fake, tt.wantCalls)
		})
	}
}
