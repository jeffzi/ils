package github

import (
	"cmp"
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
)

// runOfLink finds the Actions run a check's link belongs to.
var runOfLink = regexp.MustCompile(`/actions/runs/(\d+)`)

// runUnfinished matches what `gh run view --log-failed` prints when the run has not completed.
var runUnfinished = regexp.MustCompile(`run \d+ is still in progress; logs will be available when it is complete`)

// noChecksReported is what gh prints on stderr when a pull request has no checks.
const noChecksReported = "no checks reported"

// RunListLimit is the most runs `gh run list` returns for one commit.
const RunListLimit = "1000"

type pullCheck struct {
	Name     string `json:"name"`
	Bucket   string `json:"bucket"`
	Workflow string `json:"workflow"`
	Link     string `json:"link"`
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
		"--json", "name,bucket,workflow,link",
	}

	result, err := f.runner.Run(ctx, args...)
	if err != nil {
		exitErr, ok := errors.AsType[*child.ExitError](err)

		switch {
		case !ok:
			return nil, err
		case strings.TrimSpace(result.Stdout) != "":
		case strings.Contains(exitErr.Stderr, noChecksReported):
			return nil, nil
		default:
			return nil, err
		}
	}

	reported, err := decodeOutput[[]pullCheck](result.Stdout, args)
	if err != nil {
		return nil, err
	}

	checks := make([]forge.Check, len(reported))
	for i, check := range reported {
		checks[i] = forge.Check{
			Name: check.Name, Bucket: bucketOf(check.Bucket), Workflow: check.Workflow, Link: check.Link,
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
	listed, err := runDecoded[[]workflowRun](ctx, f.runner,
		"run", "list", "--repo", repo.Slug(), "--commit", sha, "--limit", RunListLimit,
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
func (f *Forge) failedLog(ctx context.Context, repo forge.Repo, verdict forge.Verdict) (forge.FailedLog, error) {
	failed, ok := verdict.Failed()
	if !ok {
		return forge.FailedLog{}, nil
	}

	match := runOfLink.FindStringSubmatch(failed.Link)
	if match == nil {
		return forge.FailedLog{Check: failed.Name, State: forge.FailedLogNoRun}, nil
	}

	result, err := f.runner.Run(ctx, "run", "view", "--repo", repo.Slug(), match[1], "--log-failed")
	if exitErr, exited := errors.AsType[*child.ExitError](err); exited {
		if runUnfinished.MatchString(exitErr.Stderr) {
			return forge.FailedLog{Check: failed.Name, State: forge.FailedLogInProgress}, nil
		}

		return forge.FailedLog{Check: failed.Name, State: forge.FailedLogUnreadable}, nil
	}

	if err != nil {
		return forge.FailedLog{}, err
	}

	return forge.FailedLog{Check: failed.Name, Text: result.Stdout, State: forge.FailedLogRead}, nil
}
