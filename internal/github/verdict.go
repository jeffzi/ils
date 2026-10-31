package github

import (
	"cmp"
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
)

// runOfLink finds the Actions run a check's link belongs to, and the job of that run when the link
// names one.
var runOfLink = regexp.MustCompile(`/actions/runs/(\d+)(?:/job/(\d+))?`)

// runUnfinished matches what `gh run view --log-failed` prints when the run has not completed.
var runUnfinished = regexp.MustCompile(`run \d+ is still in progress; logs will be available when it is complete`)

// noChecksReported is what gh prints on stderr when a pull request has no checks.
const noChecksReported = "no checks reported"

// runListLimit is the most runs `gh run list` returns for one commit.
const runListLimit = "1000"

type pullCheck struct {
	// StartedAt is the zero time for a check GitHub has not started.
	StartedAt time.Time `json:"startedAt"`
	Name      string    `json:"name"`
	Bucket    string    `json:"bucket"`
	Workflow  string    `json:"workflow"`
	Link      string    `json:"link"`
}

type workflowRun struct {
	Status   string `json:"status"`
	Name     string `json:"name"`
	Workflow string `json:"workflowName"`
}

// Verdict returns every check of the pull request, the workflow runs on the
// commit, and the failed log of the check the verdict's Failed names.
//
// A check the query requires carries Required, so that the others are reported
// without deciding.
func (f *Forge) Verdict(ctx context.Context, repo forge.Repo, query forge.VerdictQuery) (forge.Verdict, error) {
	verdict, err := f.checksAndRuns(ctx, repo, query)
	if err != nil {
		return forge.Verdict{}, err
	}

	verdict.FailedLog, err = f.failedLog(ctx, repo, verdict)
	if err != nil {
		return forge.Verdict{}, err
	}

	return verdict, nil
}

// checksAndRuns is the verdict without its failed log.
func (f *Forge) checksAndRuns(ctx context.Context, repo forge.Repo, query forge.VerdictQuery) (forge.Verdict, error) {
	checks, err := f.checks(ctx, repo, query.Number)
	if err != nil {
		return forge.Verdict{}, err
	}

	runs, err := f.runs(ctx, repo, query.SHA)
	if err != nil {
		return forge.Verdict{}, err
	}

	verdict := forge.Verdict{Checks: checks, Runs: runs}
	if len(query.Required) == 0 {
		return verdict, nil
	}

	for i := range checks {
		checks[i].Required = slices.Contains(query.Required, checks[i].Name)
	}

	for _, name := range query.Required {
		reported := func(check forge.Check) bool { return check.Name == name }
		if !slices.ContainsFunc(checks, reported) {
			verdict.MissingRequired = append(verdict.MissingRequired, name)
		}
	}

	return verdict, nil
}

// checks ignores gh's exit code once it printed JSON: it exits 8 while a check is
// pending, 1 when one failed, and 0 for a canceled one. A pull request gh says has
// no checks reported yields no checks and no error.
func (f *Forge) checks(ctx context.Context, repo forge.Repo, number int) ([]forge.Check, error) {
	args := []string{
		"pr", "checks", "--repo", repo.Slug(), strconv.Itoa(number),
		"--json", "name,bucket,workflow,link,startedAt",
	}

	result, err := f.runner().Run(ctx, args...)
	exitErr, exited := errors.AsType[*child.ExitError](err)
	printedJSON := exited && strings.TrimSpace(result.Stdout) != ""

	if exited && !printedJSON && strings.Contains(exitErr.Stderr, noChecksReported) {
		return nil, nil
	}

	if err != nil && !printedJSON {
		return nil, err
	}

	reported, err := decodeOutput[[]pullCheck](result.Stdout, args)
	if err != nil {
		return nil, err
	}

	checks := make([]forge.Check, len(reported))
	for i, check := range reported {
		checks[i] = forge.Check{
			Name: check.Name, Bucket: bucketOf(check.Bucket), Workflow: check.Workflow, Link: check.Link,
			StartedAt: check.StartedAt,
		}
	}

	return checks, nil
}

// bucketOf returns BucketPending for a bucket it does not know, so that it never reads as a passed check.
func bucketOf(bucket string) forge.Bucket {
	switch bucket {
	case "pass":
		return forge.BucketPass
	case "fail":
		return forge.BucketFail
	case "skipping":
		return forge.BucketSkipped
	case "cancel":
		return forge.BucketCanceled
	default:
		return forge.BucketPending
	}
}

// runs names a run by its workflow name, falling back to the run name when the workflow name is empty.
func (f *Forge) runs(ctx context.Context, repo forge.Repo, sha string) ([]forge.Run, error) {
	listed, err := runDecoded[[]workflowRun](ctx, f.runner(),
		"run", "list", "--repo", repo.Slug(), "--commit", sha, "--limit", runListLimit,
		"--json", "status,name,workflowName")
	if err != nil {
		return nil, err
	}

	runs := make([]forge.Run, len(listed))
	for i, run := range listed {
		runs[i] = forge.Run{Name: cmp.Or(run.Workflow, run.Name), Status: runStatusOf(run.Status)}
	}

	return runs, nil
}

// runStatusOf returns RunInProgress for a status it does not know, so that the
// run keeps a wait alive and never ends one.
func runStatusOf(status string) forge.RunStatus {
	switch status {
	case "queued", "pending":
		return forge.RunQueued
	case "waiting", "requested":
		return forge.RunAwaitingPerson
	case "completed":
		return forge.RunCompleted
	default:
		return forge.RunInProgress
	}
}

// failedLog reads the failed log of the check the verdict's Failed names, so
// that the named check and the log always belong together.
//
// A check with no Actions run behind it has no log, a run that has not completed
// holds its log back, and a gh that cannot give the log for any other reason
// leaves it unreadable: the verdict is red either way.
//
// A failed log that is empty falls back to the log of the job the check's link
// names: for a few seconds after a run completes, GitHub serves a log archive
// without its step files, `gh run view --log-failed` prints nothing for it and
// exits 0, and gh caches that archive, so every later read of the run prints
// nothing too. When that job log is empty or unreadable as well, the log is
// still in progress, so that the wait keeps polling.
func (f *Forge) failedLog(ctx context.Context, repo forge.Repo, verdict forge.Verdict) (forge.FailedLog, error) {
	failed, ok := verdict.Failed()
	if !ok {
		return forge.FailedLog{}, nil
	}

	match := runOfLink.FindStringSubmatch(failed.Link)
	if match == nil {
		return forge.FailedLog{Check: failed.Name, State: forge.FailedLogNoRun}, nil
	}

	runID, jobID := match[1], match[2]

	result, err := f.runner().Run(ctx, "run", "view", "--repo", repo.Slug(), runID, "--log-failed")
	if exitErr, exited := errors.AsType[*child.ExitError](err); exited {
		if runUnfinished.MatchString(exitErr.Stderr) {
			return forge.FailedLog{Check: failed.Name, State: forge.FailedLogInProgress}, nil
		}

		return forge.FailedLog{Check: failed.Name, State: forge.FailedLogUnreadable}, nil
	}

	if err != nil {
		return forge.FailedLog{}, err
	}

	text := result.Stdout
	if strings.TrimSpace(text) == "" {
		text, err = f.jobLog(ctx, repo, jobID)
		if err != nil {
			return forge.FailedLog{}, err
		}
	}

	if strings.TrimSpace(text) == "" {
		return forge.FailedLog{Check: failed.Name, State: forge.FailedLogInProgress}, nil
	}

	return forge.FailedLog{Check: failed.Name, Text: text, State: forge.FailedLogRead}, nil
}

// jobLog reads the log of the Actions job numbered job. It returns an empty log, and no error,
// when job is empty or gh exits non-zero: either way failedLog has no log to give yet.
func (f *Forge) jobLog(ctx context.Context, repo forge.Repo, job string) (string, error) {
	if job == "" {
		return "", nil
	}

	result, err := f.runner().Run(ctx, repoAPIArgs(repo, "actions/jobs/"+job+"/logs")...)
	if _, exited := errors.AsType[*child.ExitError](err); exited {
		return "", nil
	}

	if err != nil {
		return "", err
	}

	return result.Stdout, nil
}

type commitCheckRun struct {
	// StartedAt is the zero time for a check run GitHub has not started.
	StartedAt  time.Time `json:"started_at"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	Link       string    `json:"html_url"`
	CheckSuite struct {
		ID int64 `json:"id"`
	} `json:"check_suite"`
	ID int64 `json:"id"`
}

type checkRunsPage struct {
	CheckRuns []commitCheckRun `json:"check_runs"`
}

// commitStatus is one status of the REST read of a commit's combined status, which holds the
// latest status of each context.
type commitStatus struct {
	CreatedAt time.Time `json:"created_at"`
	Context   string    `json:"context"`
	State     string    `json:"state"`
	Link      string    `json:"target_url"`
}

type combinedStatusPage struct {
	Statuses []commitStatus `json:"statuses"`
}

// actionsRun is one run of the REST read of the Actions runs on a commit; its check suite holds the
// check runs of its jobs.
type actionsRun struct {
	Name       string `json:"name"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	CheckSuite int64  `json:"check_suite_id"`
}

type actionsRunsPage struct {
	Runs []actionsRun `json:"workflow_runs"`
}

// CommitVerdict returns the verdict of the commit sha on its own, with no pull request involved:
// every commit status, and every check run but those of an Actions run that judges something else
// than the commit, with the Actions runs that judge it. Its FailedLog is always the zero value.
//
// A check run is keyed by its workflow and name: when one is reported more than once, the most
// recent one stands for it.
func (f *Forge) CommitVerdict(ctx context.Context, repo forge.Repo, sha string) (forge.Verdict, error) {
	runPages, err := readEachPage[actionsRunsPage](ctx, f.runner(), paginatedAPIArgs(repo, "actions/runs?head_sha="+sha))
	if err != nil {
		return forge.Verdict{}, err
	}

	checkPages, err := readEachPage[checkRunsPage](ctx, f.runner(), paginatedAPIArgs(repo, "commits/"+sha+"/check-runs"))
	if err != nil {
		return forge.Verdict{}, err
	}

	statusPages, err := readEachPage[combinedStatusPage](ctx, f.runner(), paginatedAPIArgs(repo, "commits/"+sha+"/status"))
	if err != nil {
		return forge.Verdict{}, err
	}

	suites := suitesOf(runPages)
	verdict := forge.Verdict{Checks: latestCheckRuns(checkPages, suites), Runs: suites.runs}

	for _, page := range statusPages {
		for _, status := range page.Statuses {
			verdict.Checks = append(verdict.Checks, forge.Check{
				Name: status.Context, Bucket: statusBucketOf(status.State), Link: status.Link, StartedAt: status.CreatedAt,
			})
		}
	}

	return verdict, nil
}

// commitSuites sorts the check suites of a commit's Actions runs by whether their run judges the
// commit.
type commitSuites struct {
	// workflowOf names the workflow of each check suite whose run judges the commit.
	workflowOf map[int64]string
	// elsewhere holds the check suites of the runs that judge something else.
	elsewhere map[int64]bool
	// runs are the runs that judge the commit.
	runs []forge.Run
}

// suitesOf judges each run by its event: a push judges the commit. Every other event, and one this
// version does not know, judges something else that happens to stand on the same commit, as a
// scheduled, manual or Dependabot run does.
//
// A run another run started (`workflow_run`) is one of them: GitHub files it under the tip of the
// default branch whatever started the run it follows, and nothing in the run says what that was.
func suitesOf(pages []actionsRunsPage) commitSuites {
	suites := commitSuites{workflowOf: map[int64]string{}, elsewhere: map[int64]bool{}}

	for _, page := range pages {
		for _, run := range page.Runs {
			if run.Event != "push" {
				suites.elsewhere[run.CheckSuite] = true

				continue
			}

			suites.workflowOf[run.CheckSuite] = run.Name
			suites.runs = append(suites.runs, forge.Run{Name: run.Name, Status: runStatusOf(run.Status)})
		}
	}

	return suites
}

// checkKey is what makes two check runs the same check.
type checkKey struct {
	workflow string
	name     string
}

// latestCheckRuns returns a check for each workflow and name, from the check run of it GitHub
// created last, leaving out the check runs of suites that judge something else. A check run with
// no Actions run behind it, such as one a GitHub App reports, has no workflow.
func latestCheckRuns(pages []checkRunsPage, suites commitSuites) []forge.Check {
	var kept []commitCheckRun

	at := map[checkKey]int{}

	for _, page := range pages {
		for _, run := range page.CheckRuns {
			if suites.elsewhere[run.CheckSuite.ID] {
				continue
			}

			key := checkKey{workflow: suites.workflowOf[run.CheckSuite.ID], name: run.Name}
			if i, seen := at[key]; seen {
				if run.ID > kept[i].ID {
					kept[i] = run
				}

				continue
			}

			at[key] = len(kept)
			kept = append(kept, run)
		}
	}

	checks := make([]forge.Check, len(kept))
	for i, run := range kept {
		checks[i] = forge.Check{
			Name: run.Name, Bucket: checkRunBucketOf(run.Status, run.Conclusion),
			Workflow: suites.workflowOf[run.CheckSuite.ID], Link: run.Link, StartedAt: run.StartedAt,
		}
	}

	return checks
}

// checkRunBucketOf returns BucketPending for a check run not completed, and for a conclusion it
// does not know, so that it never reads as a passed check.
func checkRunBucketOf(status, conclusion string) forge.Bucket {
	if status != "completed" {
		return forge.BucketPending
	}

	switch conclusion {
	case "success":
		return forge.BucketPass
	case "skipped", "neutral":
		return forge.BucketSkipped
	case "cancelled": //nolint:misspell // GitHub's REST API spells the conclusion this way.
		return forge.BucketCanceled
	case "failure", "timed_out", "action_required", "stale":
		return forge.BucketFail
	default:
		return forge.BucketPending
	}
}

// statusBucketOf returns BucketPending for a state it does not know, so that it never reads as a
// passed check.
func statusBucketOf(state string) forge.Bucket {
	switch state {
	case "success":
		return forge.BucketPass
	case "failure", "error":
		return forge.BucketFail
	default:
		return forge.BucketPending
	}
}
