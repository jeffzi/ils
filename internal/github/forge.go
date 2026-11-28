package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jeffzi/ils/internal/forge"
)

// ---------------------------------------------------------------------------
// GitHub forge
// ---------------------------------------------------------------------------

// Forge is GitHub seen through the operations landing asks of a forge, each one a call of gh. Its
// zero value is ready to use and logs nothing.
type Forge struct {
	// Logger, when set, is the Runner.Logger of every gh call.
	Logger *slog.Logger
}

// runner builds a Runner carrying f.Logger on each call, so the zero Forge needs no setup.
func (f *Forge) runner() Runner {
	return Runner{Logger: f.Logger}
}

// workflowsDir is the directory GitHub Actions reads workflow files from. GitHub lists its own
// dynamic workflows (Dependabot, the dependency graph) under other paths, with no file behind them.
const workflowsDir = ".github/workflows"

type repoView struct {
	Permission string `json:"viewerPermission"`
	Visibility string `json:"visibility"`
	Fork       bool   `json:"isFork"`
	Squash     bool   `json:"squashMergeAllowed"`
}

type pull struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Base    string `json:"baseRefName"`
	HeadSHA string `json:"headRefOid"`
	State   string `json:"state"`
	Number  int    `json:"number"`
	Draft   bool   `json:"isDraft"`
	// Fork is true when the head branch is in another repository.
	Fork bool `json:"isCrossRepository"`
}

// changeStateOf returns the change state of the pull request state gh names, and false for a state
// gh does not document.
func changeStateOf(state string) (forge.ChangeState, bool) {
	switch state {
	case "OPEN":
		return forge.ChangeOpen, true
	case "CLOSED":
		return forge.ChangeClosed, true
	case "MERGED":
		return forge.ChangeMerged, true
	default:
		return "", false
	}
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

// messageQuery is the GraphQL query that reads a pull request's title, its body, and the body
// GitHub would compose for the viewer's squash merge. The repository and number are variables,
// never spliced into the query.
const messageQuery = "query($owner: String!, $name: String!, $number: Int!) { " +
	"repository(owner: $owner, name: $name) { " +
	"pullRequest(number: $number) { title body viewerMergeBodyText(mergeType: SQUASH) } } }"

// trailerLine matches a line of the `Token: value` form, whose token is letters, digits and hyphens.
var trailerLine = regexp.MustCompile(`^[A-Za-z0-9-]+: \S`)

type workflowEntry struct {
	Path  string `json:"path"`
	State string `json:"state"`
}

type workflowsPage struct {
	Workflows []workflowEntry `json:"workflows"`
}

type repoFile struct {
	Path string `json:"path"`
}

// Identity fills the visibility and the fork, can-push and squash-allowed flags of repo, after
// checking that gh is no older than MinimumVersion and then that it is logged in to the host of
// repo. A visibility that is not one GitHub gives is a *DecodeError.
func (f *Forge) Identity(ctx context.Context, repo forge.Repo) (forge.Repo, error) {
	if err := f.runner().checkVersion(ctx); err != nil {
		return forge.Repo{}, err
	}

	if err := f.runner().CheckLogin(ctx, repo.Host); err != nil {
		return forge.Repo{}, err
	}

	args := []string{"repo", "view", repo.Slug(), "--json", "isFork,viewerPermission,squashMergeAllowed,visibility"}

	view, err := readDecoded[repoView](ctx, f.runner(), args...)
	if err != nil {
		return forge.Repo{}, err
	}

	visibility, err := visibilityOf(view.Visibility, args)
	if err != nil {
		return forge.Repo{}, err
	}

	repo.Visibility = visibility
	repo.Fork = view.Fork
	repo.CanPush = slices.Contains([]string{"WRITE", "MAINTAIN", "ADMIN"}, view.Permission)
	repo.SquashAllowed = view.Squash

	return repo, nil
}

// PublicRepo reports whether repo is public on GitHub, as the gh login r uses for the host of repo
// sees it, after checking that login: a private or an internal repository is not public.
//
// It returns the errors of CheckLogin, so a host gh holds no login for is a
// *forge.NotLoggedInError, and those of Run. A reply that names no visibility GitHub gives is a
// *DecodeError.
func (r Runner) PublicRepo(ctx context.Context, repo forge.Repo) (bool, error) {
	if err := r.CheckLogin(ctx, repo.Host); err != nil {
		return false, err
	}

	args := []string{"repo", "view", repo.Slug(), "--json", "visibility"}
	view, err := runDecoded[repoView](ctx, r, args...)
	if err != nil {
		return false, err
	}

	visibility, err := visibilityOf(view.Visibility, args)
	if err != nil {
		return false, err
	}

	return visibility == forge.VisibilityPublic, nil
}

// visibilityOf returns the visibility GitHub names, read with args; a name GitHub does not give is
// a *DecodeError.
func visibilityOf(name string, args []string) (forge.Visibility, error) {
	switch name {
	case "PUBLIC":
		return forge.VisibilityPublic, nil
	case "PRIVATE":
		return forge.VisibilityPrivate, nil
	case "INTERNAL":
		return forge.VisibilityInternal, nil
	default:
		return "", &DecodeError{Argv: Argv(args), Err: fmt.Errorf("visibility %q is not one GitHub gives", name)}
	}
}

// changesLimit is the most pull requests FindChanges asks gh for, far past what one branch ever
// has, since gh lists only 30 when no limit is given.
const changesLimit = "1000"

// FindChanges returns the pull requests whose head is branch, open, closed and merged, in the
// order gh lists them. A pull request whose head is in another repository is left out; one in a
// state gh does not document is a *DecodeError.
func (f *Forge) FindChanges(ctx context.Context, repo forge.Repo, branch string) ([]forge.Change, error) {
	args := []string{
		"pr", "list", "--repo", repo.Slug(), "--head", branch, "--state", "all", "--limit", changesLimit,
		"--json", "number,url,title,baseRefName,headRefOid,isDraft,isCrossRepository,state",
	}

	pulls, err := readDecoded[[]pull](ctx, f.runner(), args...)
	if err != nil {
		return nil, err
	}

	var changes []forge.Change

	for _, p := range pulls {
		if p.Fork {
			continue
		}

		state, ok := changeStateOf(p.State)
		if !ok {
			return nil, &DecodeError{Argv: Argv(args), Err: fmt.Errorf("pull request #%d: unknown state %q", p.Number, p.State)}
		}

		changes = append(changes, forge.Change{
			Number: p.Number, URL: p.URL, Title: p.Title, Base: p.Base,
			HeadSHA: p.HeadSHA, Draft: p.Draft, State: state,
		})
	}

	return changes, nil
}

// OpenChange opens a pull request with an empty body, ready for review.
//
// When the open fails, it reads the branch's pull requests again: if one is open from the branch
// into the base, the failure comes back wrapped in forge.ErrChangeExists, and otherwise as gh
// returned it. Output that names no pull request is a *DecodeError.
func (f *Forge) OpenChange(ctx context.Context, repo forge.Repo, change forge.NewChange) (forge.ChangeRef, error) {
	args := []string{
		"pr", "create", "--repo", repo.Slug(), "--title", change.Title, "--body", "",
		"--base", change.Base, "--head", change.Head,
	}

	result, err := f.runner().Run(ctx, args...)
	if err != nil {
		if f.changeOpen(ctx, repo, change) {
			return forge.ChangeRef{}, fmt.Errorf("%w: %w", forge.ErrChangeExists, err)
		}

		return forge.ChangeRef{}, err
	}

	link := strings.TrimSpace(result.Stdout)

	_, numberText, found := strings.Cut(link, "/pull/")
	if !found {
		return forge.ChangeRef{}, &DecodeError{Argv: Argv(args), Err: fmt.Errorf("no pull request URL in %q", result.Stdout)}
	}

	number, err := strconv.Atoi(numberText)
	if err != nil {
		return forge.ChangeRef{}, &DecodeError{Argv: Argv(args), Err: fmt.Errorf("no pull request number in %q: %w", link, err)}
	}

	return forge.ChangeRef{Number: number, URL: link}, nil
}

// changeOpen reports whether a pull request is open from change.Head into change.Base. A read of
// the pull requests that fails reports none: the open's own failure is then the one to return.
func (f *Forge) changeOpen(ctx context.Context, repo forge.Repo, change forge.NewChange) bool {
	changes, err := f.FindChanges(ctx, repo, change.Head)
	if err != nil {
		return false
	}

	return slices.ContainsFunc(changes, func(c forge.Change) bool {
		return c.State == forge.ChangeOpen && c.Base == change.Base
	})
}

// Head returns the pull request's head commit and whether GitHub can merge it.
//
// GitHub's BLOCKED does not say what blocks, so for one that no review explains
// Head also reads the checks on the head commit, when the query names required ones.
func (f *Forge) Head(ctx context.Context, repo forge.Repo, query forge.HeadQuery) (forge.Head, error) {
	view, err := readDecoded[pullHead](ctx, f.runner(),
		"pr", "view", "--repo", repo.Slug(), strconv.Itoa(query.Number),
		"--json", "headRefOid,mergeable,mergeStateStatus,isDraft,reviewDecision")
	if err != nil {
		return forge.Head{}, err
	}

	facts := mergeFacts{Mergeable: view.Mergeable, Status: view.Status, Review: view.Review, Draft: view.Draft}
	if mergeStateOf(facts) == forge.MergeBlockedOther {
		verdictQuery := forge.VerdictQuery{SHA: view.SHA, Number: query.Number, Required: query.Required}

		facts.MissingCheck, facts.RunningCheck, err = f.requiredChecks(ctx, repo, verdictQuery)
		if err != nil {
			return forge.Head{}, err
		}
	}

	return forge.Head{SHA: view.SHA, MergeState: mergeStateOf(facts)}, nil
}

// requiredChecks reports whether a check the query requires has no check on the commit while
// nothing still running could report it (missing), and whether one is still running or could
// still be reported by a run in progress (running). At most one of the two is true.
func (f *Forge) requiredChecks(ctx context.Context, repo forge.Repo, query forge.VerdictQuery) (missing, running bool, err error) {
	if len(query.Required) == 0 {
		return false, false, nil
	}

	verdict, err := f.checksAndRuns(ctx, repo, query)
	if err != nil {
		return false, false, err
	}

	absent := len(verdict.MissingRequired) > 0
	running = len(verdict.Pending()) > 0 || (absent && verdict.Running())

	return absent && !running, running, nil
}

// TrunkProtection returns whether rules apply to trunk, the status checks they require, whether
// they allow a squash merge, and whether they require a merge queue. They allow a squash merge
// unless a pull_request rule names merge methods without squash, whose ruleset it then names.
//
// A repository that is not public answers 403 to the read when its plan does not allow rules: none
// applies then, and a squash merge is allowed. See planRefusal for the 403 that is an error.
func (f *Forge) TrunkProtection(ctx context.Context, repo forge.Repo, trunk string) (forge.Protection, error) {
	args := repoAPIArgs(repo, "rules/branches/"+branchPath(trunk), "--paginate")

	reply, err := f.runner().Read(ctx, args...)
	if err != nil {
		if planRefusal(repo, reply) {
			return forge.Protection{Rules: forge.RulesUnavailable, SquashAllowed: true}, nil
		}

		return forge.Protection{}, err
	}

	pages, err := decodeEachPage[[]branchRule](reply.Stdout, withHeaders(args))
	if err != nil {
		return forge.Protection{}, err
	}

	return protectionOf(slices.Concat(pages...)), nil
}

// planRefusal reports whether reply, a failed read of the rules on a branch of repo, is the 403 of
// a plan that does not allow rules. Rules need a public repository or a paid plan, so a public
// repository's 403 is never the plan's. Neither is one that carries a header naming another cause:
// X-GitHub-SSO for a token not authorized for SAML single sign-on, Retry-After for a secondary rate
// limit, and X-RateLimit-Remaining at 0 for a primary one.
func planRefusal(repo forge.Repo, reply Reply) bool {
	if reply.Status != http.StatusForbidden || repo.Visibility == forge.VisibilityPublic {
		return false
	}

	for _, name := range []string{"X-GitHub-SSO", "Retry-After"} {
		if len(reply.Header.Values(name)) > 0 {
			return false
		}
	}

	return reply.Header.Get("X-RateLimit-Remaining") != "0"
}

// protectionOf folds the rules on a branch into whether any applies, the checks they require, each
// named once, whether every pull_request rule allows a squash merge, with the rulesets of those
// that do not, and whether a merge_queue rule applies. Rules of any other type change nothing.
func protectionOf(rules []branchRule) forge.Protection {
	protection := forge.Protection{Rules: forge.RulesNone, SquashAllowed: true}
	if len(rules) > 0 {
		protection.Rules = forge.RulesProtect
	}

	for _, rule := range rules {
		switch rule.Type {
		case "pull_request":
			if rule.Parameters.MergeMethods != nil && !slices.Contains(rule.Parameters.MergeMethods, "squash") {
				protection.SquashAllowed = false
				protection.SquashForbiddenBy = append(protection.SquashForbiddenBy, rule.RulesetID)
			}
		case "merge_queue":
			protection.MergeQueue = true
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

// branchPath escapes each segment on its own: `#`, `%` and `?` would otherwise cut or corrupt the
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
// A trunk with no .github/workflows directory has no CI; any other failure of a read, a trunk
// GitHub does not find among them, is an error.
func (f *Forge) HasTrunkCI(ctx context.Context, repo forge.Repo, trunk string) (bool, error) {
	active, err := f.activeWorkflows(ctx, repo)
	if err != nil {
		return false, err
	}

	if len(active) == 0 {
		return false, nil
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
// The block is the last paragraph of what GitHub appends after the pull request body in the body
// it composes, taken only when every line of it is a trailer. Under every message setting GitHub
// puts its trailers there; a last paragraph with any other line, such as a commit's subject, means
// GitHub added no trailer. A trailer line the pull request body already holds is not returned:
// the squash body already carries it.
//
// An answer that cannot be decoded, or that holds no pull request, is a *DecodeError.
func (f *Forge) Message(ctx context.Context, repo forge.Repo, number int) (forge.Message, error) {
	args := withHeaders([]string{
		"api", "graphql", "--hostname", repo.Host,
		"-F", "number=" + strconv.Itoa(number), "-f", "owner=" + repo.Owner, "-f", "name=" + repo.Name,
		"-f", "query=" + messageQuery,
	})

	answer, err := readDecoded[pullMessage](ctx, f.runner(), args...)
	if err != nil {
		return forge.Message{}, err
	}

	if answer.Data.Repository == nil || answer.Data.Repository.PullRequest == nil {
		return forge.Message{}, &DecodeError{Argv: Argv(args), Err: errors.New("the answer holds no pull request")}
	}

	view := answer.Data.Repository.PullRequest

	return forge.Message{Title: view.Title, Body: view.Body, Trailers: trailersOf(view.Body, view.MergeBody)}, nil
}

// trailersOf returns the lines of the last paragraph of what GitHub appended to body in
// mergeBody when every one is a trailer, and nil otherwise. A mergeBody that does not start with
// body, ending at a line, is all GitHub's own; the two are compared with LF line ends and without
// the whitespace that closes body, which GitHub's copy of it may keep or drop. A trailer that
// body already holds as one of its lines, compared with LF line ends and without the spaces and
// tabs that close a line, is never returned, whether or not mergeBody starts with body: the
// squash body already carries body, and would carry it twice.
func trailersOf(body, mergeBody string) []string {
	appended := strings.ReplaceAll(mergeBody, "\r\n", "\n")
	own := strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), " \t\n")
	rest, cut := strings.CutPrefix(appended, own)
	rest = strings.TrimLeft(rest, " \t")

	endsAtLine := rest == "" || strings.HasPrefix(rest, "\n")
	if cut && endsAtLine {
		// The line break that closes body's last line is body's own, and any blank line after it
		// only separates body from what GitHub appended: kept, they would open the lines GitHub
		// appended with an empty one.
		appended = strings.TrimLeft(rest, "\n")
	}

	paragraphs := strings.Split(strings.TrimRight(appended, "\n"), "\n\n")
	lines := strings.Split(paragraphs[len(paragraphs)-1], "\n")

	for _, line := range lines {
		if !trailerLine.MatchString(line) {
			return nil
		}
	}

	lineKey := func(line string) string { return strings.TrimRight(line, " \t") }

	held := make(map[string]bool)
	for line := range strings.SplitSeq(own, "\n") {
		held[lineKey(line)] = true
	}

	return slices.DeleteFunc(lines, func(line string) bool { return held[lineKey(line)] })
}

// Land squash-merges the pull request with exactly the subject and body of landing, even an empty
// body, and leaves its branch in place. GitHub refuses the merge once the head is no longer
// landing.HeadSHA.
func (f *Forge) Land(ctx context.Context, repo forge.Repo, landing forge.Landing) error {
	_, err := f.runner().Run(ctx,
		"pr", "merge", strconv.Itoa(landing.Number), "--repo", repo.Slug(), "--squash",
		"--match-head-commit", landing.HeadSHA, "--subject", landing.Subject, "--body", landing.Body)

	return err
}

// DeleteBranch removes branch from the repository on GitHub.
func (f *Forge) DeleteBranch(ctx context.Context, repo forge.Repo, branch string) error {
	_, err := f.runner().Run(ctx, repoAPIArgs(repo, "git/refs/heads/"+branchPath(branch), "-X", "DELETE")...)

	return err
}

// releaseQuery is the GraphQL query that reads the URL of the release of a tag, a draft included.
// The repository and tag are variables, never spliced into the query.
const releaseQuery = "query($owner: String!, $name: String!, $tagName: String!) { " +
	"repository(owner: $owner, name: $name) { release(tagName: $tagName) { url } } }"

// releaseAnswer is the answer to releaseQuery; Release is nil when the tag has no release.
type releaseAnswer struct {
	Data struct {
		Repository struct {
			Release *struct {
				URL string `json:"url"`
			} `json:"release"`
		} `json:"repository"`
	} `json:"data"`
}

// FindRelease returns the URL of the GitHub release of tag, a draft included, and found false with
// no error when GitHub has no release for tag. An answer that cannot be decoded is a *DecodeError.
func (f *Forge) FindRelease(ctx context.Context, repo forge.Repo, tag string) (link string, found bool, err error) {
	args := []string{
		"api", "graphql", "--hostname", repo.Host,
		"-f", "owner=" + repo.Owner, "-f", "name=" + repo.Name, "-f", "tagName=" + tag,
		"-f", "query=" + releaseQuery,
	}

	answer, err := readDecoded[releaseAnswer](ctx, f.runner(), args...)
	if err != nil {
		return "", false, err
	}

	release := answer.Data.Repository.Release
	if release == nil {
		return "", false, nil
	}

	return release.URL, true, nil
}

// PublishRelease creates the GitHub release of release.Tag titled with the tag, with
// release.Notes as its body, marked as a pre-release when release.Prerelease is set, and returns
// its URL. `--verify-tag` makes gh refuse a tag origin does not hold, rather than create it at the
// trunk's tip.
//
// The notes reach gh through a temporary file, since a Runner has no stdin; it is removed before
// PublishRelease returns, and a failure to remove it is an error even once the release exists:
// the release's URL then comes back with that error, so a caller can tell a created release from
// one gh did not create.
func (f *Forge) PublishRelease(ctx context.Context, repo forge.Repo, release forge.NewRelease) (link string, err error) {
	notes, err := writeNotes(release.Notes)
	if err != nil {
		return "", fmt.Errorf("write the notes of %s: %w", release.Tag, err)
	}

	defer func() {
		if removeErr := os.Remove(notes); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove the notes of %s: %w", release.Tag, removeErr))
		}
	}()

	args := []string{"release", "create", release.Tag, "--repo", repo.Slug(), "--verify-tag", "--title", release.Tag, "--notes-file", notes}
	if release.Prerelease {
		args = append(args, "--prerelease")
	}

	result, err := f.runner().Run(ctx, args...)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(result.Stdout), nil
}

// writeNotes writes notes to a new temporary file and returns its path; the caller removes it.
func writeNotes(notes string) (path string, err error) {
	file, err := os.CreateTemp("", "ils-release-notes-*.md")
	if err != nil {
		return "", err
	}

	if _, err := file.WriteString(notes); err != nil {
		return "", errors.Join(err, file.Close(), os.Remove(file.Name()))
	}

	if err := file.Close(); err != nil {
		return "", errors.Join(err, os.Remove(file.Name()))
	}

	return file.Name(), nil
}

// repoAPIArgs returns the gh arguments of a call of rest under the repository, with flags before
// its REST path, which is relative to the API root.
func repoAPIArgs(repo forge.Repo, rest string, flags ...string) []string {
	args := append([]string{"api", "--hostname", repo.Host}, flags...)

	return append(args, "repos/"+repo.FullName()+"/"+rest)
}

// activeWorkflows returns the paths of the active workflows that have a file under .github/workflows.
func (f *Forge) activeWorkflows(ctx context.Context, repo forge.Repo) ([]string, error) {
	pages, err := readEachPage[workflowsPage](ctx, f.runner(), repoAPIArgs(repo, "actions/workflows", "--paginate"))
	if err != nil {
		return nil, err
	}

	var active []string

	for _, page := range pages {
		for _, workflow := range page.Workflows {
			if workflow.State != "active" || !strings.HasPrefix(workflow.Path, workflowsDir+"/") {
				continue
			}

			active = append(active, workflow.Path)
		}
	}

	return active, nil
}

type branchView struct {
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// workflowFiles returns the entries of the .github/workflows directory on trunk. It reads trunk
// first, so that a trunk that does not exist, which GitHub also answers 404 for, is an error, and
// then reads the directory at the commit trunk stands on: a 404 there means no directory, and so no
// entry.
func (f *Forge) workflowFiles(ctx context.Context, repo forge.Repo, trunk string) ([]repoFile, error) {
	branchArgs := repoAPIArgs(repo, "branches/"+branchPath(trunk))

	branch, err := readDecoded[branchView](ctx, f.runner(), branchArgs...)
	if err != nil {
		return nil, err
	}

	if branch.Commit.SHA == "" {
		return nil, &DecodeError{Argv: Argv(withHeaders(branchArgs)), Err: errors.New("the branch holds no commit")}
	}

	args := repoAPIArgs(repo, "contents/"+workflowsDir+"?ref="+url.QueryEscape(branch.Commit.SHA))

	reply, err := f.runner().Read(ctx, args...)
	if err != nil {
		if reply.Status == http.StatusNotFound {
			return nil, nil
		}

		return nil, err
	}

	return decodeOutput[[]repoFile](reply.Stdout, withHeaders(args))
}

// ---------------------------------------------------------------------------
// Merge state
// ---------------------------------------------------------------------------

// mergeFacts is GitHub's merge fields of a pull request, plus the two flags that Head derives from
// the checks of the head commit.
type mergeFacts struct {
	// Mergeable is GitHub's `mergeable`: MERGEABLE, CONFLICTING, or UNKNOWN.
	Mergeable string
	Status    string
	// Review is GitHub's `reviewDecision`; it is empty when no review is asked for.
	Review string
	Draft  bool
	// MissingCheck reports that a required check has no check on the head commit
	// and that nothing still running could report it.
	MissingCheck bool
	// RunningCheck reports that a required check is still running, or has no check
	// yet while a run in progress could still report it.
	RunningCheck bool
}

// mergeStateOf maps GitHub's values to the merge state a command switches on.
//
// A value of `mergeStateStatus` it does not know is forge.MergeUnknown.
func mergeStateOf(facts mergeFacts) forge.MergeState {
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
func blockedState(facts mergeFacts) forge.MergeState {
	switch {
	case facts.Review == "REVIEW_REQUIRED" || facts.Review == "CHANGES_REQUESTED":
		return forge.MergeBlockedByReview
	case facts.MissingCheck:
		return forge.MergeBlockedByMissingCheck
	case facts.RunningCheck:
		return forge.MergeBlockedByRunningCheck
	default:
		return forge.MergeBlockedOther
	}
}
