// Package github drives the gh CLI, started from an argv and never through a shell.
package github

import (
	"cmp"
	"context"
	legacy "encoding/json"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	gh "github.com/cli/go-gh/v2"
	"golang.org/x/mod/semver"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
)

// Forge is GitHub seen through the operations landing asks of a forge, each one a call of gh.
type Forge struct {
	runner Runner
}

// NewForge returns the GitHub forge that runs gh through runner.
func NewForge(runner Runner) *Forge {
	return &Forge{runner: runner}
}

// runOfLink finds the Actions run a check's link belongs to.
var runOfLink = regexp.MustCompile(`/actions/runs/(\d+)`)

// pullExists matches what `gh pr create` prints when the branch already has a pull request.
var pullExists = regexp.MustCompile(`a pull request for branch "[^"\n]*" into branch "[^"\n]*" already exists`)

// freePlanRefusal is the start of the message GitHub gives for rules on a private repository of a free plan.
const freePlanRefusal = "Upgrade to GitHub Pro"

// forbiddenStatus is the status line gh prints on stderr when GitHub answers 403.
const forbiddenStatus = "HTTP 403"

// noChecksReported is what gh prints on stderr when a pull request has no checks.
const noChecksReported = "no checks reported"

// runListLimit is the most runs `gh run list` returns for one commit.
const runListLimit = "1000"

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
	// Fork is true when the head branch is in another repository.
	Fork bool `json:"isCrossRepository"`
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

// Identity fills the fork and can-push flags of repo, after checking that gh is no older than
// MinimumVersion and then that it is logged in to the host of repo.
func (f *Forge) Identity(ctx context.Context, repo forge.Repo) (forge.Repo, error) {
	if err := f.runner.checkVersion(ctx); err != nil {
		return forge.Repo{}, err
	}

	if err := f.runner.CheckLogin(ctx, repo.Host); err != nil {
		return forge.Repo{}, err
	}

	view, err := RunJSON[repoView](ctx, f.runner, "repo", "view", repo.Slug(), "--json", "isFork,viewerPermission")
	if err != nil {
		return forge.Repo{}, fmt.Errorf("read the repository %s: %w", repo.Slug(), err)
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
			"pr", "list", "--repo", repo.Slug(), "--head", branch, "--state", string(state),
			"--json", "number,url,title,baseRefName,headRefOid,isDraft,isCrossRepository")
		if err != nil {
			return nil, err
		}

		for _, p := range pulls {
			if p.Fork {
				continue
			}

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
		"pr", "create", "--repo", repo.Slug(), "--title", change.Title, "--body", "",
		"--base", change.Base, "--head", change.Head)
	if exitErr, ok := errors.AsType[*child.ExitError](err); ok && pullExists.MatchString(exitErr.Stderr) {
		return forge.ChangeRef{}, fmt.Errorf("%w: %w", forge.ErrChangeExists, err)
	}

	if err != nil {
		return forge.ChangeRef{}, err
	}

	url := strings.TrimSpace(result.Stdout)

	_, numberText, found := strings.Cut(url, "/pull/")
	if !found {
		return forge.ChangeRef{}, fmt.Errorf("gh pr create: no pull request URL in its output %q", result.Stdout)
	}

	number, err := strconv.Atoi(numberText)
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
		"pr", "view", "--repo", repo.Slug(), strconv.Itoa(query.Number),
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
	if err != nil {
		return false, err
	}

	if len(verdict.MissingRequired) == 0 {
		return false, nil
	}

	return len(verdict.Pending()) == 0 && !verdict.Running(), nil
}

// TrunkProtection returns the status checks the rules on trunk require.
//
// A free private repository answers 403 with an upgrade message to the read,
// which counts as no required check. Any other 403 is an error.
func (f *Forge) TrunkProtection(ctx context.Context, repo forge.Repo, trunk string) (forge.Protection, error) {
	path := fmt.Sprintf("repos/%s/%s/rules/branches/%s", repo.Owner, repo.Name, trunk)
	args := []string{"api", "--hostname", repo.Host, "--paginate", path}

	result, err := f.runner.Run(ctx, args...)
	if exitErr, ok := errors.AsType[*child.ExitError](err); ok &&
		strings.Contains(exitErr.Stderr, forbiddenStatus) && strings.Contains(exitErr.Stderr, freePlanRefusal) {
		return forge.Protection{}, nil
	}

	if err != nil {
		return forge.Protection{}, err
	}

	rules, err := decodePages[branchRule](result.Stdout, args)
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
	listed, err := RunJSON[[]workflowRun](ctx, f.runner,
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

	result, err := f.runner.Run(ctx, "run", "view", "--repo", repo.Slug(), match[1], "--log-failed")
	if _, exited := errors.AsType[*child.ExitError](err); exited {
		return forge.FailedLog{Check: failed.Name, State: forge.FailedLogUnreadable}, nil
	}

	if err != nil {
		return forge.FailedLog{}, err
	}

	return forge.FailedLog{Check: failed.Name, Text: result.Stdout, State: forge.FailedLogRead}, nil
}

// MergeFacts is what GitHub says about whether a pull request can merge.
type MergeFacts struct {
	// Mergeable is GitHub's `mergeable`: MERGEABLE, CONFLICTING, or UNKNOWN.
	Mergeable string
	// Status is GitHub's `mergeStateStatus`.
	Status string
	// Review is GitHub's `reviewDecision`; it is empty when no review is asked for.
	Review string
	Draft  bool
	// MissingCheck reports that a required check has no check on the head commit
	// and that nothing still running could report it.
	MissingCheck bool
}

// MergeStateOf maps GitHub's values to the merge state a command switches on.
//
// A value of `mergeStateStatus` it does not know is forge.MergeUnknown.
func MergeStateOf(facts MergeFacts) forge.MergeState {
	switch facts.Mergeable {
	case "CONFLICTING":
		return forge.MergeConflicting
	case "UNKNOWN":
		return forge.MergeUnknown
	}

	// GitHub reports a draft as BLOCKED as often as DRAFT, so the flag decides
	// ahead of every status a draft can still carry. Only `mergeable` goes first:
	// a draft that conflicts still needs the trunk merged in.
	if facts.Draft || facts.Status == "DRAFT" {
		return forge.MergeDraft
	}

	switch facts.Status {
	case "BEHIND":
		return forge.MergeBehind
	case "DIRTY":
		return forge.MergeConflicting
	case "CLEAN", "HAS_HOOKS", "UNSTABLE":
		return forge.MergeClean
	case "BLOCKED":
		return blockedState(facts)
	default:
		return forge.MergeUnknown
	}
}

// blockedState says what blocks a pull request whose status is BLOCKED.
func blockedState(facts MergeFacts) forge.MergeState {
	switch {
	case facts.Review == "REVIEW_REQUIRED" || facts.Review == "CHANGES_REQUESTED":
		return forge.MergeBlockedByReview
	case facts.MissingCheck:
		return forge.MergeBlockedByMissingCheck
	default:
		return forge.MergeBlockedOther
	}
}

// Program is the name gh is run under, and the first word of every argv a call is named by.
const Program = "gh"

func argv(args []string) []string {
	return append([]string{Program}, args...)
}

const installURL = "https://cli.github.com"

// MinimumVersion is the oldest release of gh that ils works with.
const MinimumVersion = "2.50.0"

// versionPrefix is what the first line of `gh --version` reads ahead of the version.
const versionPrefix = "gh version "

// semverPrefix is what the semver package wants ahead of a version.
const semverPrefix = "v"

// Runner runs gh, found through GH_PATH and then PATH.
type Runner struct{}

// Result is what a call printed.
type Result struct {
	Stdout string
	Stderr string
}

// DecodeError reports a call whose stdout was not the JSON its caller expected.
type DecodeError struct {
	Err  error
	Argv []string
}

// Error names the command whose output could not be decoded.
func (e *DecodeError) Error() string {
	return fmt.Sprintf("%s: decode output: %v", strings.Join(e.Argv, " "), e.Err)
}

// Unwrap returns Err, so that errors.Is and errors.As reach the decoder's error.
func (e *DecodeError) Unwrap() error {
	return e.Err
}

// Run runs gh with args and returns what it printed.
//
// A gh that cannot be found is a *forge.ToolMissingError. A non-zero exit is a
// *child.ExitError, returned together with the Result, since gh prints to stdout
// before some of its non-zero exits. When ctx ends before gh does, the error is
// the context's and the Result is empty, so a half-written stdout is never read.
func (Runner) Run(ctx context.Context, args ...string) (Result, error) {
	stdout, stderr, err := gh.ExecContext(ctx, args...)
	if isMissing(err) {
		return Result{}, &forge.ToolMissingError{Tool: Program, InstallURL: installURL, Err: err}
	}

	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}

	failure := child.Failure(ctx, argv(args), err, result.Stderr)
	if _, exited := errors.AsType[*child.ExitError](failure); exited {
		return result, failure
	}

	return Result{}, failure
}

// isMissing reports whether err says that no gh could be started: none on PATH,
// or a GH_PATH that names no file.
func isMissing(err error) bool {
	if _, ok := errors.AsType[*exec.Error](err); ok {
		return true
	}

	return errors.Is(err, fs.ErrNotExist)
}

// checkVersion reports whether gh is MinimumVersion or newer.
//
// A gh that is older, or whose first line of `gh --version` carries no version that can be
// read, is a *forge.ToolTooOldError; a pre-release of MinimumVersion is older than it.
func (r Runner) checkVersion(ctx context.Context) error {
	result, err := r.Run(ctx, "--version")
	if err != nil {
		return err
	}

	line, _, _ := strings.Cut(result.Stdout, "\n")
	tooOld := &forge.ToolTooOldError{Tool: Program, Line: line, Minimum: MinimumVersion, InstallURL: installURL}

	rest, _ := strings.CutPrefix(line, versionPrefix)

	version, _, _ := strings.Cut(rest, " ")
	canonical := semverPrefix + version
	if !semver.IsValid(canonical) {
		return tooOld
	}

	tooOld.Version = version
	if semver.Compare(canonical, semverPrefix+MinimumVersion) < 0 {
		return tooOld
	}

	return nil
}

// CheckLogin reports whether gh is logged in to host.
//
// A gh that cannot be found is a *forge.ToolMissingError and a gh that is not
// logged in to host is a *forge.NotLoggedInError.
func (r Runner) CheckLogin(ctx context.Context, host string) error {
	_, err := r.Run(ctx, "auth", "status", "--hostname", host)
	if _, ok := errors.AsType[*child.ExitError](err); ok {
		return &forge.NotLoggedInError{Host: host, Err: err}
	}

	return err
}

// RunJSON runs gh with args and decodes its stdout into a T.
//
// It returns the errors of Runner.Run, and a *DecodeError when stdout is not
// the JSON of a T.
func RunJSON[T any](ctx context.Context, r Runner, args ...string) (T, error) {
	result, err := r.Run(ctx, args...)
	if err != nil {
		var zero T

		return zero, err
	}

	return decodeOutput[T](result.Stdout, args)
}

func decodeOutput[T any](stdout string, args []string) (T, error) {
	var value T
	if err := json.Unmarshal([]byte(stdout), &value); err != nil {
		var zero T

		return zero, &DecodeError{Argv: argv(args), Err: err}
	}

	return value, nil
}

// decodePages decodes the JSON arrays that `gh api --paginate` prints, one per
// page and concatenated, into one slice; a single array decodes as itself.
func decodePages[T any](stdout string, args []string) ([]T, error) {
	decoder := legacy.NewDecoder(strings.NewReader(stdout))

	var all []T

	for pages := 0; ; pages++ {
		var raw legacy.RawMessage

		err := decoder.Decode(&raw)
		if errors.Is(err, io.EOF) {
			if pages > 0 {
				return all, nil
			}

			err = io.ErrUnexpectedEOF
		}

		if err != nil {
			return nil, &DecodeError{Argv: argv(args), Err: err}
		}

		page, err := decodeOutput[[]T](string(raw), args)
		if err != nil {
			return nil, err
		}

		all = append(all, page...)
	}
}
