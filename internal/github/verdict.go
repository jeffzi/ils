package github

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
)

// runOfLink finds the Actions run a check's link belongs to, and the job of that run when the link
// names one.
var runOfLink = regexp.MustCompile(`/actions/runs/(\d+)(?:/job/(\d+))?`)

// Verdict returns every check on the commit the query names, whatever event started the run
// behind it, the workflow runs on the commit, and the failed log of the check the verdict's Failed
// names. A commit with no check yet has a verdict with no check and no error.
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
	verdict, err := f.commitChecks(ctx, repo, query.SHA, anyEvent)
	if err != nil {
		return forge.Verdict{}, err
	}

	checks := verdict.Checks
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
// A canceled check never gets a log, so none is read for it and the failed log
// is the zero value: reading one would find it empty and wait for it to the
// time limit. A check with no Actions run behind it has no log. The run's status is read before
// its log: a run that has not completed holds its log back, even when the check's own job has
// completed, so the log is in progress; a status this version does not know counts as not
// completed. A status that cannot be read, a gh that cannot give the log of a completed run, or a
// read of either that the time limit or an interrupt cuts short, leaves the log unreadable: the
// verdict is red either way. A link that names no job leaves the log unreadable without a read:
// the run's failed log holds every failed job of the run, and would be shown as this check's.
//
// A failed log that is empty falls back to the log of the job the check's link
// names: for a few seconds after a run completes, GitHub serves a log archive
// without its step files, `gh run view --log-failed` prints nothing for it and
// exits 0, and gh caches that archive, so every later read of the run prints
// nothing too. When that job log is empty or unreadable as well, the log is
// still in progress, so that the wait keeps polling.
func (f *Forge) failedLog(ctx context.Context, repo forge.Repo, verdict forge.Verdict) (forge.FailedLog, error) {
	failed, ok := verdict.Failed()
	if !ok || failed.Bucket == forge.BucketCanceled {
		return forge.FailedLog{}, nil
	}

	match := runOfLink.FindStringSubmatch(failed.Link)
	if match == nil {
		return forge.FailedLog{State: forge.FailedLogNoRun}, nil
	}

	runID, jobID := match[1], match[2]
	if jobID == "" {
		return forge.FailedLog{State: forge.FailedLogUnreadable}, nil
	}

	completed, err := f.runCompleted(ctx, repo, runID)
	if err != nil {
		return cutLog(err)
	}

	if !completed {
		return forge.FailedLog{State: forge.FailedLogInProgress}, nil
	}

	result, err := f.runner().Run(ctx, "run", "view", "--repo", repo.Slug(), runID, "--log-failed", "--job", jobID)
	if err != nil {
		return cutLog(err)
	}

	text := result.Stdout
	if strings.TrimSpace(text) == "" {
		text, err = f.jobLog(ctx, repo, jobID)
		if err != nil {
			return cutLog(err)
		}
	}

	if strings.TrimSpace(text) == "" {
		return forge.FailedLog{State: forge.FailedLogInProgress}, nil
	}

	return forge.FailedLog{Text: text, State: forge.FailedLogRead}, nil
}

// runCompleted reads whether the Actions run numbered run has completed. It returns the errors of
// Runner.Read, and a *DecodeError when the answer is not a run.
func (f *Forge) runCompleted(ctx context.Context, repo forge.Repo, run string) (bool, error) {
	got, err := readDecoded[actionsRun](ctx, f.runner(), repoAPIArgs(repo, "actions/runs/"+run)...)
	if err != nil {
		return false, err
	}

	return runStatusOf(got.Status) == forge.RunCompleted, nil
}

// cutLog returns the log of the failed check as unreadable when err is gh exiting non-zero, the
// time limit or an interrupt ending the read of it: the check failed whatever its log says, so the
// verdict stays red without it. Any other err is returned.
func cutLog(err error) (forge.FailedLog, error) {
	if _, exited := errors.AsType[*child.ExitError](err); exited || child.EndedEarly(err) {
		return forge.FailedLog{State: forge.FailedLogUnreadable}, nil
	}

	return forge.FailedLog{}, err
}

// jobLog reads the log of the Actions job numbered job. It returns an empty log, and no error,
// when gh exits non-zero: failedLog has no log to give yet.
func (f *Forge) jobLog(ctx context.Context, repo forge.Repo, job string) (string, error) {
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

// actionsRun is one Actions run as the REST API gives it; its check suite holds the check runs of
// its jobs.
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
// Only a push run judges the commit. Every other event, and one this version does not know, judges
// something else that happens to stand on the same commit, as a scheduled, manual or Dependabot
// run does. A run another run started (`workflow_run`) is one of them: GitHub files it under the
// tip of the default branch whatever started the run it follows, and nothing in the run says what
// that was.
func (f *Forge) CommitVerdict(ctx context.Context, repo forge.Repo, sha string) (forge.Verdict, error) {
	return f.commitChecks(ctx, repo, sha, fromPush)
}

func fromPush(event string) bool { return event == "push" }

func anyEvent(string) bool { return true }

// commitChecks returns every commit status of the commit sha, and every check run but those of an
// Actions run whose event judges says does not judge the commit, with the Actions runs it says do.
// Its FailedLog is always the zero value.
//
// A check run is keyed by its workflow, the event of its run and its name: when one is reported
// more than once, the most recent one stands for it.
func (f *Forge) commitChecks(ctx context.Context, repo forge.Repo, sha string, judges func(event string) bool) (forge.Verdict, error) {
	// The check runs are read before the Actions runs: a run created between the two reads then
	// shows only as a run still going, while read the other way round its check runs would have no
	// run behind them and would decide the verdict as checks of no workflow.
	checkPages, err := readEachPage[checkRunsPage](ctx, f.runner(), repoAPIArgs(repo, "commits/"+sha+"/check-runs", "--paginate"))
	if err != nil {
		return forge.Verdict{}, err
	}

	runPages, err := readEachPage[actionsRunsPage](ctx, f.runner(), repoAPIArgs(repo, "actions/runs?head_sha="+sha, "--paginate"))
	if err != nil {
		return forge.Verdict{}, err
	}

	statusPages, err := readEachPage[combinedStatusPage](ctx, f.runner(), repoAPIArgs(repo, "commits/"+sha+"/status", "--paginate"))
	if err != nil {
		return forge.Verdict{}, err
	}

	suites := suitesOf(runPages, judges)
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

// suiteRun is the Actions run behind a check suite.
type suiteRun struct {
	workflow string
	event    string
}

// commitSuites sorts the check suites of a commit's Actions runs by whether their run judges the
// commit.
type commitSuites struct {
	// runOf holds the run of each check suite whose run judges the commit.
	runOf map[int64]suiteRun
	// elsewhere holds the check suites of the runs that judge something else.
	elsewhere map[int64]bool
	// runs are the runs that judge the commit.
	runs []forge.Run
}

// suitesOf sorts the check suites of the runs of pages by whether judges says the event of their
// run judges the commit.
func suitesOf(pages []actionsRunsPage, judges func(event string) bool) commitSuites {
	suites := commitSuites{runOf: map[int64]suiteRun{}, elsewhere: map[int64]bool{}}

	for _, page := range pages {
		for _, run := range page.Runs {
			if !judges(run.Event) {
				suites.elsewhere[run.CheckSuite] = true

				continue
			}

			suites.runOf[run.CheckSuite] = suiteRun{workflow: run.Name, event: run.Event}
			suites.runs = append(suites.runs, forge.Run{Name: run.Name, Status: runStatusOf(run.Status)})
		}
	}

	return suites
}

// latestCheckRuns returns a check for each workflow, event and name, from the check run of it
// GitHub created last, leaving out the check runs of suites that judge something else. A check run
// with no Actions run behind it, such as one a GitHub App reports, has no workflow and no event.
func latestCheckRuns(pages []checkRunsPage, suites commitSuites) []forge.Check {
	var kept []commitCheckRun

	at := map[checkKey]int{}

	for _, page := range pages {
		for _, run := range page.CheckRuns {
			if suites.elsewhere[run.CheckSuite.ID] {
				continue
			}

			key := checkKey{run: suites.runOf[run.CheckSuite.ID], name: run.Name}

			i, seen := at[key]
			if !seen {
				at[key] = len(kept)
				kept = append(kept, run)

				continue
			}

			if run.ID > kept[i].ID {
				kept[i] = run
			}
		}
	}

	checks := make([]forge.Check, len(kept))
	for i, run := range kept {
		checks[i] = forge.Check{
			Name: run.Name, Bucket: checkRunBucketOf(run.Status, run.Conclusion),
			Workflow: suites.runOf[run.CheckSuite.ID].workflow, Link: run.Link, StartedAt: run.StartedAt,
		}
	}

	return checks
}

// checkKey is what makes two check runs the same check.
type checkKey struct {
	run  suiteRun
	name string
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
