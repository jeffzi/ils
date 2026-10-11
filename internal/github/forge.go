package github

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jeffzi/ils/internal/forge"
)

// Forge is GitHub seen through the forge's operations, each one a call of gh.
type Forge struct {
	runner Runner
}

var _ forge.Forge = (*Forge)(nil)

// NewForge returns the GitHub forge that runs gh through runner.
func NewForge(runner Runner) *Forge {
	return &Forge{runner: runner}
}

// runOfLink finds the Actions run a check's link belongs to.
var runOfLink = regexp.MustCompile(`/actions/runs/(\d+)`)

// pullExists matches what `gh pr create` prints when the branch already has a pull request.
var pullExists = regexp.MustCompile(`a pull request for branch "[^"\n]*" into branch "[^"\n]*" already exists`)

type repoView struct {
	Permission string `json:"viewerPermission"`
	Fork       bool   `json:"isFork"`
}

type pull struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Base    string `json:"baseRefName"`
	HeadSHA string `json:"headRefOid"`
	Number  int    `json:"number"`
	Draft   bool   `json:"isDraft"`
}

type pullHead struct {
	SHA       string `json:"headRefOid"`
	Mergeable string `json:"mergeable"`
	Status    string `json:"mergeStateStatus"`
	Review    string `json:"reviewDecision"`
	Draft     bool   `json:"isDraft"`
}

type branchRule struct {
	Type       string `json:"type"`
	Parameters struct {
		Required []struct {
			Context string `json:"context"`
		} `json:"required_status_checks"`
	} `json:"parameters"`
}

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

// slug names the repository the way `--repo` and `gh repo view` take it.
func slug(repo forge.Repo) string {
	return repo.Host + "/" + repo.Owner + "/" + repo.Name
}

// Identity fills the fork and can-push flags of repo, after checking that gh is logged in to its host.
func (f *Forge) Identity(ctx context.Context, repo forge.Repo) (forge.Repo, error) {
	if err := f.runner.CheckLogin(ctx, repo.Host); err != nil {
		return forge.Repo{}, err
	}

	view, err := RunJSON[repoView](ctx, f.runner, "repo", "view", slug(repo), "--json", "isFork,viewerPermission")
	if err != nil {
		return forge.Repo{}, fmt.Errorf("read the repository %s: %w", slug(repo), err)
	}

	repo.Fork = view.Fork
	repo.CanPush = slices.Contains([]string{"WRITE", "MAINTAIN", "ADMIN"}, view.Permission)

	return repo, nil
}

// FindChanges returns the open pull requests whose head is branch, then the merged ones.
func (f *Forge) FindChanges(ctx context.Context, repo forge.Repo, branch string) ([]forge.Change, error) {
	var changes []forge.Change

	for _, state := range []forge.ChangeState{forge.ChangeOpen, forge.ChangeMerged} {
		pulls, err := RunJSON[[]pull](ctx, f.runner,
			"pr", "list", "--repo", slug(repo), "--head", branch, "--state", string(state),
			"--json", "number,url,title,baseRefName,headRefOid,isDraft")
		if err != nil {
			return nil, err
		}

		for _, p := range pulls {
			changes = append(changes, forge.Change{
				Number: p.Number, URL: p.URL, Title: p.Title, Base: p.Base,
				HeadSHA: p.HeadSHA, Draft: p.Draft, State: state,
			})
		}
	}

	return changes, nil
}

// OpenChange opens a pull request with an empty body, ready for review.
//
// It returns forge.ErrChangeExists when gh says the branch already has one.
func (f *Forge) OpenChange(ctx context.Context, repo forge.Repo, change forge.NewChange) (forge.ChangeRef, error) {
	result, err := f.runner.Run(ctx,
		"pr", "create", "--repo", slug(repo), "--title", change.Title, "--body", "",
		"--base", change.Base, "--head", change.Head)
	if exitErr, ok := errors.AsType[*ExitError](err); ok && pullExists.MatchString(exitErr.Stderr) {
		return forge.ChangeRef{}, fmt.Errorf("%w: %w", forge.ErrChangeExists, err)
	}

	if err != nil {
		return forge.ChangeRef{}, err
	}

	url := strings.TrimSpace(result.Stdout)

	_, after, found := strings.Cut(url, "/pull/")
	if !found {
		return forge.ChangeRef{}, fmt.Errorf("gh pr create: no pull request URL in its output %q", result.Stdout)
	}

	number, err := strconv.Atoi(after)
	if err != nil {
		return forge.ChangeRef{}, fmt.Errorf("gh pr create: no pull request number in %q: %w", url, err)
	}

	return forge.ChangeRef{Number: number, URL: url}, nil
}

// Head returns the pull request's head commit and whether GitHub can merge it.
//
// GitHub's BLOCKED does not say what blocks, so for one that no review explains
// Head also reads the checks on the head commit, when the query names required ones.
func (f *Forge) Head(ctx context.Context, repo forge.Repo, query forge.HeadQuery) (forge.Head, error) {
	view, err := RunJSON[pullHead](ctx, f.runner,
		"pr", "view", "--repo", slug(repo), strconv.Itoa(query.Number),
		"--json", "headRefOid,mergeable,mergeStateStatus,isDraft,reviewDecision")
	if err != nil {
		return forge.Head{}, err
	}

	facts := MergeFacts{Mergeable: view.Mergeable, Status: view.Status, Review: view.Review, Draft: view.Draft}
	if MergeStateOf(facts) == forge.MergeBlockedOther {
		verdictQuery := forge.VerdictQuery{SHA: view.SHA, Number: query.Number, Required: query.Required}

		facts.MissingCheck, err = f.missingCheck(ctx, repo, verdictQuery)
		if err != nil {
			return forge.Head{}, err
		}
	}

	return forge.Head{SHA: view.SHA, MergeState: MergeStateOf(facts), RawMergeState: view.Status}, nil
}

// missingCheck reports whether a check the query requires has no check on the
// commit while nothing still running could report it.
func (f *Forge) missingCheck(ctx context.Context, repo forge.Repo, query forge.VerdictQuery) (bool, error) {
	if len(query.Required) == 0 {
		return false, nil
	}

	verdict, err := f.checksAndRuns(ctx, repo, query)
	if err != nil || len(verdict.MissingRequired) == 0 {
		return false, err
	}

	running := slices.ContainsFunc(verdict.Runs, func(run forge.Run) bool {
		return run.Status == forge.RunQueued || run.Status == forge.RunInProgress
	})

	return len(verdict.Pending()) == 0 && !running, nil
}

// TrunkProtection returns the status checks the rules on trunk require.
//
// A free private repository answers 403 to the read, which counts as no required check.
func (f *Forge) TrunkProtection(ctx context.Context, repo forge.Repo, trunk string) (forge.Protection, error) {
	path := fmt.Sprintf("repos/%s/%s/rules/branches/%s", repo.Owner, repo.Name, trunk)

	rules, err := RunJSON[[]branchRule](ctx, f.runner, "api", "--hostname", repo.Host, path)
	if exitErr, ok := errors.AsType[*ExitError](err); ok && strings.Contains(exitErr.Stderr, "HTTP 403") {
		return forge.Protection{}, nil
	}

	if err != nil {
		return forge.Protection{}, err
	}

	var required []string

	for _, rule := range rules {
		if rule.Type != "required_status_checks" {
			continue
		}

		for _, check := range rule.Parameters.Required {
			if !slices.Contains(required, check.Context) {
				required = append(required, check.Context)
			}
		}
	}

	return forge.Protection{RequiredChecks: required}, nil
}

// Verdict returns the deciding checks of the pull request, the workflow runs on
// the commit, and the failed log of the check the verdict's Failed names.
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

	verdict.Checks = nil

	for _, check := range checks {
		if slices.Contains(query.Required, check.Name) {
			check.Required = true
			verdict.Checks = append(verdict.Checks, check)
		}
	}

	for _, name := range query.Required {
		reported := func(check forge.Check) bool { return check.Name == name }
		if !slices.ContainsFunc(checks, reported) {
			verdict.MissingRequired = append(verdict.MissingRequired, name)
		}
	}

	return verdict, nil
}

// checks reads every check of the pull request.
//
// gh's exit code is ignored once it printed JSON: it exits 8 while a check is
// pending, 1 when one failed, and 0 for a canceled one.
func (f *Forge) checks(ctx context.Context, repo forge.Repo, number int) ([]forge.Check, error) {
	args := []string{
		"pr", "checks", "--repo", slug(repo), strconv.Itoa(number),
		"--json", "name,bucket,workflow,link,state",
	}

	result, err := f.runner.Run(ctx, args...)
	if err != nil {
		exitErr, ok := errors.AsType[*ExitError](err)

		switch {
		case !ok:
			return nil, err
		case strings.TrimSpace(result.Stdout) != "":
		case strings.Contains(exitErr.Stderr, "no checks reported"):
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

// bucketOf maps gh's bucket to the forge's.
//
// A bucket it does not know is pending, so that it never reads as a passed check.
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

// runs reads the workflow runs on the commit.
func (f *Forge) runs(ctx context.Context, repo forge.Repo, sha string) ([]forge.Run, error) {
	listed, err := RunJSON[[]workflowRun](ctx, f.runner,
		"run", "list", "--repo", slug(repo), "--commit", sha,
		"--json", "databaseId,status,event,name,workflowName")
	if err != nil {
		return nil, err
	}

	runs := make([]forge.Run, len(listed))
	for i, run := range listed {
		runs[i] = forge.Run{Name: cmp.Or(run.Workflow, run.Name), Status: runStatusOf(run.Status)}
	}

	return runs, nil
}

// runStatusOf maps the status of an Actions run to the forge's.
//
// A status it does not know is in progress, so that the run keeps a wait alive
// and never ends one.
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
// A check with no Actions run behind it has no log, and a gh that cannot give
// the log leaves it unreadable: the verdict is red either way.
func (f *Forge) failedLog(ctx context.Context, repo forge.Repo, verdict forge.Verdict) (forge.FailedLog, error) {
	failed, ok := verdict.Failed()
	if !ok {
		return forge.FailedLog{}, nil
	}

	match := runOfLink.FindStringSubmatch(failed.Link)
	if match == nil {
		return forge.FailedLog{Check: failed.Name, State: forge.FailedLogNoRun}, nil
	}

	result, err := f.runner.Run(ctx, "run", "view", "--repo", slug(repo), match[1], "--log-failed")
	if _, exited := errors.AsType[*ExitError](err); exited {
		return forge.FailedLog{Check: failed.Name, State: forge.FailedLogUnreadable}, nil
	}

	if err != nil {
		return forge.FailedLog{}, err
	}

	return forge.FailedLog{Check: failed.Name, Text: result.Stdout, State: forge.FailedLogRead}, nil
}
