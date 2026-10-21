// Package github drives the gh CLI, started from an argv and never through a shell.
package github

import (
	"context"
	legacy "encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
)

// ---------------------------------------------------------------------------
// GitHub forge
// ---------------------------------------------------------------------------

// Forge is GitHub seen through the operations landing asks of a forge, each one a call of gh.
type Forge struct {
	runner Runner
}

// NewForge returns the GitHub forge that runs gh through runner.
func NewForge(runner Runner) *Forge {
	return &Forge{runner: runner}
}

// pullExists matches what `gh pr create` prints when the branch already has a pull request.
var pullExists = regexp.MustCompile(`a pull request for branch "[^"\n]*" into branch "[^"\n]*" already exists`)

// freePlanRefusal is the start of the message GitHub gives for rules on a private repository of a free plan.
const freePlanRefusal = "Upgrade to GitHub Pro"

// forbiddenStatus is the status line gh prints on stderr when GitHub answers 403.
const forbiddenStatus = "HTTP 403"

// pathNotFound is what gh prints on stderr when GitHub answers 404 for a path that does not exist
// on a ref that does. A ref that does not exist also answers 404, with another message.
const pathNotFound = "Not Found (HTTP 404)"

// workflowsDir is the directory GitHub Actions reads workflow files from. GitHub lists its own
// dynamic workflows (Dependabot, the dependency graph) under other paths, with no file behind them.
const workflowsDir = ".github/workflows"

type repoView struct {
	Permission string `json:"viewerPermission"`
	Fork       bool   `json:"isFork"`
	Squash     bool   `json:"squashMergeAllowed"`
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
		// MergeMethods is nil when the rule names none, which allows every method.
		MergeMethods []string `json:"allowed_merge_methods"`
	} `json:"parameters"`
	RulesetID int `json:"ruleset_id"`
}

type pullMessage struct {
	Data struct {
		Repository *struct {
			PullRequest *pullMessageView `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

type pullMessageView struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	MergeBody string `json:"viewerMergeBodyText"`
}

// errNoPullRequest is why an answer to the message read that names no pull request cannot be read.
var errNoPullRequest = errors.New("the answer holds no pull request")

// MessageQuery is the GraphQL query that reads a pull request's title, its body, and the body
// GitHub would compose for the viewer's squash merge. The repository and number are variables,
// never spliced into the query.
const MessageQuery = "query($owner: String!, $name: String!, $number: Int!) { " +
	"repository(owner: $owner, name: $name) { " +
	"pullRequest(number: $number) { title body viewerMergeBodyText(mergeType: SQUASH) } } }"

// trailerLine matches a line of the `Token: value` form, whose token is letters, digits and hyphens.
var trailerLine = regexp.MustCompile(`^[A-Za-z0-9-]+: \S`)

type workflowsPage struct {
	Workflows []struct {
		Path  string `json:"path"`
		State string `json:"state"`
	} `json:"workflows"`
}

type repoFile struct {
	Path string `json:"path"`
}

// Identity fills the fork, can-push and squash-allowed flags of repo, after checking that gh is no older than
// MinimumVersion and then that it is logged in to the host of repo.
func (f *Forge) Identity(ctx context.Context, repo forge.Repo) (forge.Repo, error) {
	if err := f.runner.checkVersion(ctx); err != nil {
		return forge.Repo{}, err
	}

	if err := f.runner.CheckLogin(ctx, repo.Host); err != nil {
		return forge.Repo{}, err
	}

	view, err := RunJSON[repoView](ctx, f.runner, "repo", "view", repo.Slug(), "--json", "isFork,viewerPermission,squashMergeAllowed")
	if err != nil {
		return forge.Repo{}, err
	}

	repo.Fork = view.Fork
	repo.CanPush = slices.Contains([]string{"WRITE", "MAINTAIN", "ADMIN"}, view.Permission)
	repo.SquashAllowed = view.Squash

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

	link := strings.TrimSpace(result.Stdout)

	_, numberText, found := strings.Cut(link, "/pull/")
	if !found {
		return forge.ChangeRef{}, fmt.Errorf("gh pr create: no pull request URL in its output %q", result.Stdout)
	}

	number, err := strconv.Atoi(numberText)
	if err != nil {
		return forge.ChangeRef{}, fmt.Errorf("gh pr create: no pull request number in %q: %w", link, err)
	}

	return forge.ChangeRef{Number: number, URL: link}, nil
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

// TrunkProtection returns the status checks the rules on trunk require, and whether they allow a
// squash merge: they do unless a pull_request rule names merge methods without squash, whose
// ruleset it then names.
//
// A free private repository answers 403 with an upgrade message to the read,
// which counts as no rule. Any other 403 is an error.
func (f *Forge) TrunkProtection(ctx context.Context, repo forge.Repo, trunk string) (forge.Protection, error) {
	args := paginatedAPIArgs(repo, "rules/branches/"+branchPath(trunk))

	result, err := f.runner.Run(ctx, args...)
	if exitErr, ok := errors.AsType[*child.ExitError](err); ok &&
		strings.Contains(exitErr.Stderr, forbiddenStatus) && strings.Contains(exitErr.Stderr, freePlanRefusal) {
		return forge.Protection{SquashAllowed: true}, nil
	}

	if err != nil {
		return forge.Protection{}, err
	}

	rules, err := decodePages[branchRule](result.Stdout, args)
	if err != nil {
		return forge.Protection{}, err
	}

	return protectionOf(rules), nil
}

// protectionOf folds the rules on a branch into the checks they require, each named once, and
// whether every pull_request rule allows a squash merge, with the rulesets of those that do not.
func protectionOf(rules []branchRule) forge.Protection {
	protection := forge.Protection{SquashAllowed: true}

	for _, rule := range rules {
		switch rule.Type {
		case "pull_request":
			if rule.Parameters.MergeMethods != nil && !slices.Contains(rule.Parameters.MergeMethods, "squash") {
				protection.SquashAllowed = false
				protection.SquashForbiddenBy = append(protection.SquashForbiddenBy, rule.RulesetID)
			}
		case "required_status_checks":
			for _, check := range rule.Parameters.Required {
				if !slices.Contains(protection.RequiredChecks, check.Context) {
					protection.RequiredChecks = append(protection.RequiredChecks, check.Context)
				}
			}
		}
	}

	return protection
}

// branchPath returns branch as a REST path.
//
// Each segment is escaped on its own: `#`, `%` and `?` would otherwise cut or corrupt the
// path, while `/` is kept literal so that a name like release/v1 keeps its slash in the path.
func branchPath(branch string) string {
	segments := strings.Split(branch, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}

	return strings.Join(segments, "/")
}

// HasTrunkCI reports whether a workflow that GitHub runs, one in the active state,
// has its file under .github/workflows on trunk.
//
// A trunk with no .github/workflows directory has no CI; any other failure of a read is an error.
func (f *Forge) HasTrunkCI(ctx context.Context, repo forge.Repo, trunk string) (bool, error) {
	active, err := f.activeWorkflows(ctx, repo)
	if err != nil || len(active) == 0 {
		return false, err
	}

	files, err := f.workflowFiles(ctx, repo, trunk)
	if err != nil {
		return false, err
	}

	isActiveWorkflow := func(file repoFile) bool { return slices.Contains(active, file.Path) }

	return slices.ContainsFunc(files, isActiveWorkflow), nil
}

// Message returns pull request number's title and body, and the trailer block GitHub would end
// its squash commit with when the logged-in user merges it.
//
// The block is the last paragraph of the body GitHub composes, taken only when every line of it
// is a trailer. Under every message setting GitHub puts its trailers there; a last paragraph with
// any other line, such as a commit's subject, means GitHub added no trailer.
//
// An answer that cannot be decoded, or that holds no pull request, is a *DecodeError.
func (f *Forge) Message(ctx context.Context, repo forge.Repo, number int) (forge.Message, error) {
	args := []string{
		"api", "graphql", "--hostname", repo.Host,
		"-F", "number=" + strconv.Itoa(number), "-f", "owner=" + repo.Owner, "-f", "name=" + repo.Name,
		"-f", "query=" + MessageQuery,
	}

	answer, err := RunJSON[pullMessage](ctx, f.runner, args...)
	if err != nil {
		return forge.Message{}, err
	}

	if answer.Data.Repository == nil || answer.Data.Repository.PullRequest == nil {
		return forge.Message{}, &DecodeError{Argv: Argv(args), Err: errNoPullRequest}
	}

	view := answer.Data.Repository.PullRequest

	return forge.Message{Title: view.Title, Body: view.Body, Trailers: trailersOf(view.MergeBody)}, nil
}

// trailersOf returns the lines of the last paragraph of body when every one is a trailer, and
// nil otherwise.
func trailersOf(body string) []string {
	paragraphs := strings.Split(strings.TrimRight(body, "\n"), "\n\n")
	lines := strings.Split(paragraphs[len(paragraphs)-1], "\n")

	for _, line := range lines {
		if !trailerLine.MatchString(line) {
			return nil
		}
	}

	return lines
}

// Land squash-merges the pull request with exactly the subject and body of landing, even an empty
// body, and leaves its branch in place. GitHub refuses the merge once the head is no longer
// landing.HeadSHA.
func (f *Forge) Land(ctx context.Context, repo forge.Repo, landing forge.Landing) error {
	_, err := f.runner.Run(ctx,
		"pr", "merge", strconv.Itoa(landing.Number), "--repo", repo.Slug(), "--squash",
		"--match-head-commit", landing.HeadSHA, "--subject", landing.Subject, "--body", landing.Body)

	return err
}

// DeleteBranch removes branch from the repository on GitHub.
func (f *Forge) DeleteBranch(ctx context.Context, repo forge.Repo, branch string) error {
	_, err := f.runner.Run(ctx,
		"api", "--hostname", repo.Host, "-X", "DELETE", repoAPIPath(repo, "git/refs/heads/"+branchPath(branch)))

	return err
}

// repoAPIPath returns the REST path of rest under the repository, relative to the API root.
func repoAPIPath(repo forge.Repo, rest string) string {
	return fmt.Sprintf("repos/%s/%s/%s", repo.Owner, repo.Name, rest)
}

// paginatedAPIArgs returns the gh arguments that read rest under the repository, following every page.
func paginatedAPIArgs(repo forge.Repo, rest string) []string {
	return []string{"api", "--hostname", repo.Host, "--paginate", repoAPIPath(repo, rest)}
}

// decodePages decodes the JSON arrays that `gh api --paginate` prints, one per
// page and concatenated, into one slice; a single array decodes as itself.
func decodePages[T any](stdout string, args []string) ([]T, error) {
	pages, err := decodeEachPage[[]T](stdout, args)
	if err != nil {
		return nil, err
	}

	return slices.Concat(pages...), nil
}

// decodeEachPage decodes the JSON values that `gh api --paginate` prints, one per
// page and concatenated, each into a P; stdout holding no value is a *DecodeError.
func decodeEachPage[P any](stdout string, args []string) ([]P, error) {
	decoder := legacy.NewDecoder(strings.NewReader(stdout))

	var pages []P

	for {
		var raw legacy.RawMessage

		err := decoder.Decode(&raw)
		if errors.Is(err, io.EOF) {
			if len(pages) > 0 {
				return pages, nil
			}

			err = io.ErrUnexpectedEOF
		}

		if err != nil {
			return nil, &DecodeError{Argv: Argv(args), Err: err}
		}

		page, err := decodeOutput[P](string(raw), args)
		if err != nil {
			return nil, err
		}

		pages = append(pages, page)
	}
}

// activeWorkflows returns the paths of the active workflows that have a file under .github/workflows.
func (f *Forge) activeWorkflows(ctx context.Context, repo forge.Repo) ([]string, error) {
	args := paginatedAPIArgs(repo, "actions/workflows")

	result, err := f.runner.Run(ctx, args...)
	if err != nil {
		return nil, err
	}

	pages, err := decodeEachPage[workflowsPage](result.Stdout, args)
	if err != nil {
		return nil, err
	}

	var active []string

	for _, page := range pages {
		for _, workflow := range page.Workflows {
			if workflow.State == "active" && strings.HasPrefix(workflow.Path, workflowsDir+"/") {
				active = append(active, workflow.Path)
			}
		}
	}

	return active, nil
}

// workflowFiles returns the entries of the .github/workflows directory on trunk; a trunk
// without the directory, which GitHub answers 404 Not Found for, has none. A trunk that does not
// exist is an error.
func (f *Forge) workflowFiles(ctx context.Context, repo forge.Repo, trunk string) ([]repoFile, error) {
	path := repoAPIPath(repo, "contents/"+workflowsDir+"?ref="+url.QueryEscape(trunk))
	args := []string{"api", "--hostname", repo.Host, path}

	result, err := f.runner.Run(ctx, args...)
	if exitErr, ok := errors.AsType[*child.ExitError](err); ok && strings.Contains(exitErr.Stderr, pathNotFound) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return decodeOutput[[]repoFile](result.Stdout, args)
}

// ---------------------------------------------------------------------------
// Merge state
// ---------------------------------------------------------------------------

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
