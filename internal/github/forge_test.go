package github_test

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
)

// No test that scripts `gh` is parallel: the fake `gh` is found through `GH_PATH`,
// which `go-gh` reads from the environment of the whole process.

// ---------------------------------------------------------------------------
// Fixtures and helpers
// ---------------------------------------------------------------------------

const (
	engineHost = "git.example.com"
	engineSlug = engineHost + "/ada/engine"
	headSHA    = "4f2a9c1d7e3b5a6088c1d2e3f4a5b6c7d8e9f001"
	pullURL    = "https://git.example.com/ada/engine/pull/7"
	runID      = "9001"
	runLink    = "https://git.example.com/ada/engine/actions/runs/" + runID + "/job/42"
	statusLink = "https://ci.example.com/builds/15"
	serverDown = "gh: Server Error (HTTP 500)\n"
	program    = "gh"
	installURL = "https://cli.github.com"

	pullNumber    = 7
	featureBranch = "feature"
	trunk         = "main"

	ciFile      = ".github/workflows/ci.yml"
	releaseFile = ".github/workflows/release.yml"
	// dependabotPath is where GitHub lists its own Dependabot workflow, which has no file.
	dependabotPath = "dynamic/dependabot/dependabot-updates"
)

// operation is one call on the forge, reduced to its error.
type operation struct {
	run  func(ctx context.Context, f *github.Forge) error
	name string
}

// readCase is one `gh` read of an operation, with the calls that succeed before it.
type readCase struct {
	run    func(ctx context.Context, f *github.Forge) error
	name   string
	args   []string
	before []ghtest.Call
}

// engine is the repository every test asks about.
func engine() forge.Repo {
	return forge.Repo{Host: engineHost, Owner: "ada", Name: "engine"}
}

func newForge() *github.Forge {
	return github.NewForge(github.Runner{})
}

// received is the calls the fake is expected to record, each led by `gh`.
func received(calls ...[]string) [][]string {
	led := make([][]string, len(calls))
	for i, call := range calls {
		led[i] = append([]string{"gh"}, call...)
	}

	return led
}

// assertCalls fails the test unless the fake received exactly the want argv, in order.
func assertCalls(t *testing.T, fake *ghtest.Fake, want [][]string) {
	t.Helper()

	assert.Check(t, is.DeepEqual(fake.Calls(), want))
}

// featureChange is the change every OpenChange test asks for.
func featureChange() forge.NewChange {
	return forge.NewChange{Title: "feature", Base: trunk, Head: featureBranch}
}

func versionArgs() []string { return ghtest.VersionArgs() }

// versionAnswer is what `gh --version` prints when line is its first line.
func versionAnswer(line string) string {
	return line + "\nhttps://github.com/cli/cli/releases/tag/v2.0.0\n"
}

func loginArgs() []string { return ghtest.LoginArgs(engineHost) }

func repoViewArgs() []string { return ghtest.RepoViewArgs(engineSlug) }

func listArgs(state string) []string { return ghtest.ListArgs(engineSlug, featureBranch, state) }

func createArgs() []string {
	return ghtest.CreateArgs(engineSlug, featureBranch, trunk, featureBranch)
}

func headArgs() []string { return ghtest.HeadArgs(engineSlug, pullNumber) }

func rulesArgs() []string { return ghtest.RulesArgs(engineHost, "ada/engine", trunk) }

func checksArgs() []string { return ghtest.ChecksArgs(engineSlug, pullNumber) }

func runsArgs() []string { return ghtest.RunsArgs(engineSlug, headSHA) }

func logArgs() []string { return ghtest.LogArgs(engineSlug, runID) }

func workflowsArgs() []string {
	return []string{"api", "--hostname", engineHost, "--paginate", "repos/ada/engine/actions/workflows"}
}

func workflowFilesArgs() []string { return workflowFilesArgsAt(trunk) }

func workflowFilesArgsAt(ref string) []string {
	return []string{"api", "--hostname", engineHost, "repos/ada/engine/contents/.github/workflows?ref=" + ref}
}

// workflowJSON is one entry of the workflow list's answer: the workflow at file, in state.
func workflowJSON(file, state string) string {
	return `{"id":1,"name":"` + path.Base(file) + `","path":"` + file + `","state":"` + state + `"}`
}

// workflowsPage is one page of the workflow list's answer, holding workflows.
func workflowsPage(workflows ...string) string {
	return `{"total_count":` + strconv.Itoa(len(workflows)) + `,"workflows":` + jsonArray(workflows...) + `}`
}

// fileJSON is one entry of the directory read's answer: the file at file.
func fileJSON(file string) string {
	return `{"name":"` + path.Base(file) + `","path":"` + file + `","type":"file"}`
}

// activeCIList is the workflow list's answer holding the active CI workflow.
func activeCIList() string { return workflowsPage(workflowJSON(ciFile, "active")) }

// ciOnTrunkJSON is the directory read's answer holding the CI workflow's file.
func ciOnTrunkJSON() string { return jsonArray(fileJSON(ciFile)) }

// notFoundAnswer is what `gh api` says of a path GitHub does not find.
func notFoundAnswer() ghtest.Answer { return notFound404("Not Found") }

// notFound404 is what `gh api` says of a 404 GitHub explains with message.
func notFound404(message string) ghtest.Answer {
	return ghtest.Answer{
		Stdout: `{"message":"` + message + `","documentation_url":"https://docs.github.com/rest","status":"404"}`,
		Stderr: "gh: " + message + " (HTTP 404)\n",
		Code:   1,
	}
}

// headJSON is `gh pr view`'s answer for a mergeable pull request in the given state.
func headJSON(status, review string) string {
	return pullJSON("MERGEABLE", status, review)
}

// pullJSON is `gh pr view`'s answer for a pull request at the head commit that is not a draft.
func pullJSON(mergeable, status, review string) string {
	return ghtest.HeadJSON(headSHA, mergeable, status, review)
}

// headOf is the head commit in the given merge state, with GitHub's own status behind it.
func headOf(state forge.MergeState, raw string) forge.Head {
	return forge.Head{SHA: headSHA, MergeState: state, RawMergeState: raw}
}

// runJSON is one entry of `gh run list`'s answer.
func runJSON(status string) string {
	return ghtest.RunJSON(status, "Add the forge")
}

func jsonArray(items ...string) string {
	return "[" + strings.Join(items, ",") + "]"
}

// noChecksAnswer is what `gh pr checks` says when the branch has no check.
func noChecksAnswer() ghtest.Answer {
	return ghtest.Answer{Stderr: "no checks reported on the 'feature' branch\n", Code: 1}
}

// loggedOutAnswer is what `gh auth status` says when no host is logged in.
func loggedOutAnswer() ghtest.Answer {
	return ghtest.Answer{Stderr: "You are not logged into any GitHub hosts.\n", Code: 1}
}

func headQuery(required ...string) forge.HeadQuery {
	return forge.HeadQuery{Number: pullNumber, Required: required}
}

func verdictQuery(required ...string) forge.VerdictQuery {
	return forge.VerdictQuery{SHA: headSHA, Number: pullNumber, Required: required}
}

func operations() []operation {
	return []operation{
		{name: "Identity", run: func(ctx context.Context, f *github.Forge) error {
			_, err := f.Identity(ctx, engine())
			return err
		}},
		{name: "FindChanges", run: func(ctx context.Context, f *github.Forge) error {
			_, err := f.FindChanges(ctx, engine(), featureBranch)
			return err
		}},
		{name: "OpenChange", run: func(ctx context.Context, f *github.Forge) error {
			_, err := f.OpenChange(ctx, engine(), featureChange())
			return err
		}},
		{name: "Head", run: func(ctx context.Context, f *github.Forge) error {
			_, err := f.Head(ctx, engine(), headQuery())
			return err
		}},
		{name: "TrunkProtection", run: func(ctx context.Context, f *github.Forge) error {
			_, err := f.TrunkProtection(ctx, engine(), trunk)
			return err
		}},
		{name: "Verdict", run: func(ctx context.Context, f *github.Forge) error {
			_, err := f.Verdict(ctx, engine(), verdictQuery())
			return err
		}},
		{name: "HasTrunkCI", run: func(ctx context.Context, f *github.Forge) error {
			_, err := f.HasTrunkCI(ctx, engine(), trunk)
			return err
		}},
	}
}

func readCases() []readCase {
	byName := map[string]func(context.Context, *github.Forge) error{}
	for _, op := range operations() {
		byName[op.name] = op.run
	}

	identity, find, head, protection, verdict := byName["Identity"], byName["FindChanges"], byName["Head"], byName["TrunkProtection"], byName["Verdict"]
	hasCI := byName["HasTrunkCI"]
	blocked := func(ctx context.Context, f *github.Forge) error {
		_, err := f.Head(ctx, engine(), headQuery("build", "test"))
		return err
	}

	return []readCase{
		{name: "repo view", run: identity, args: repoViewArgs(), before: []ghtest.Call{ghtest.Says("", loginArgs()...)}},
		{name: "pr list of open ones", run: find, args: listArgs("open")},
		{name: "pr list of merged ones", run: find, args: listArgs("merged"), before: []ghtest.Call{ghtest.Says("[]", listArgs("open")...)}},
		{name: "pr view", run: head, args: headArgs()},
		{
			name: "pr checks behind a blocked head", run: blocked, args: checksArgs(),
			before: []ghtest.Call{ghtest.Says(headJSON("BLOCKED", ""), headArgs()...)},
		},
		{
			name: "run list behind a blocked head", run: blocked, args: runsArgs(),
			before: []ghtest.Call{ghtest.Says(headJSON("BLOCKED", ""), headArgs()...), ghtest.Says("[]", checksArgs()...)},
		},
		{name: "api rules", run: protection, args: rulesArgs()},
		{name: "pr checks", run: verdict, args: checksArgs()},
		{name: "run list", run: verdict, args: runsArgs(), before: []ghtest.Call{ghtest.Says("[]", checksArgs()...)}},
		{name: "api workflows", run: hasCI, args: workflowsArgs()},
		{
			name: "api workflow files", run: hasCI, args: workflowFilesArgs(),
			before: []ghtest.Call{ghtest.Says(activeCIList(), workflowsArgs()...)},
		},
	}
}

// withoutGh sets `GH_PATH` to ghPath and empties `PATH` for the rest of the test, so that no `gh`
// can be found.
func withoutGh(t *testing.T, ghPath string) {
	t.Helper()

	t.Setenv("GH_PATH", ghPath)
	t.Setenv("PATH", t.TempDir())
}

// ---------------------------------------------------------------------------
// Forge operations
// ---------------------------------------------------------------------------

func TestForge_WhenGhMissing_ReturnsToolMissingError(t *testing.T) {
	for _, tt := range operations() {
		t.Run(tt.name, func(t *testing.T) {
			withoutGh(t, "")

			err := tt.run(t.Context(), newForge())

			_ = assertMissingTool(t, err, tt.name)
		})
	}
}

func TestForge_WhenContextCanceled_ReturnsContextError(t *testing.T) {
	for _, tt := range operations() {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			err := tt.run(ctx, newForge())

			assert.Check(t, is.ErrorIs(err, context.Canceled))
		})
	}
}

func TestForge_WhenReadExitsNonZero_ReturnsExitErrorNamingCall(t *testing.T) {
	for _, tt := range readCases() {
		t.Run(tt.name, func(t *testing.T) {
			failing := ghtest.Exits(ghtest.Answer{Stderr: serverDown, Code: 1}, tt.args...)
			ghtest.Setup(t, append(tt.before, failing)...)

			err := tt.run(t.Context(), newForge())

			exitErr, ok := errors.AsType[*child.ExitError](err)
			assert.Assert(t, ok, "error = %v, want *child.ExitError", err)
			want := child.ExitError{Argv: received(tt.args)[0], Stderr: serverDown, Code: 1}
			assert.Check(t, is.DeepEqual(*exitErr, want))
		})
	}
}

func TestForge_WhenReadPrintsMalformedJSON_ReturnsDecodeErrorNamingCall(t *testing.T) {
	for _, tt := range readCases() {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, append(tt.before, ghtest.Says(`[{"number":`, tt.args...))...)

			err := tt.run(t.Context(), newForge())

			decodeErr, ok := errors.AsType[*github.DecodeError](err)
			assert.Assert(t, ok, "error = %v, want *github.DecodeError", err)
			assert.Check(t, is.DeepEqual(decodeErr.Argv, received(tt.args)[0]))
		})
	}
}

func TestForge_Identity_WhenNotLoggedIn_ReturnsNotLoggedInError(t *testing.T) {
	fake := ghtest.Setup(t, ghtest.Exits(loggedOutAnswer(), loginArgs()...))

	_, err := newForge().Identity(t.Context(), engine())

	notLoggedIn, ok := errors.AsType[*forge.NotLoggedInError](err)
	assert.Assert(t, ok, "Identity error = %v, want *forge.NotLoggedInError", err)
	assert.Check(t, is.Equal(notLoggedIn.Host, engineHost))
	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Identity error = %v, want it to wrap the *child.ExitError", err)
	assert.Check(t, is.Equal(exitErr.Code, 1))
	assertCalls(t, fake, received(versionArgs(), loginArgs()))
}

func TestForge_Identity_WhenGhIsOlderThanTheMinimum_ReturnsToolTooOldErrorBeforeTheLogin(t *testing.T) {
	tests := []struct {
		name string
		// line is the first line `gh --version` prints.
		line string
		// version is the version the error carries; a line with none carries none.
		version string
	}{
		{name: "one minor release older", line: "gh version 2.49.0 (2024-05-13)", version: "2.49.0"},
		{name: "pre-release of the minimum", line: "gh version 2.50.0-pre.1 (2024-05-20)", version: "2.50.0-pre.1"},
		{name: "development build", line: "gh version DEV"},
		{name: "empty first line"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := ghtest.Setup(t, ghtest.Says(versionAnswer(tt.line), versionArgs()...), ghtest.Exits(loggedOutAnswer(), loginArgs()...))

			_, err := newForge().Identity(t.Context(), engine())

			tooOld, ok := errors.AsType[*child.ToolTooOldError](err)
			assert.Assert(t, ok, "Identity error = %v, want *child.ToolTooOldError", err)
			want := child.ToolTooOldError{
				Tool: program, Version: tt.version, Line: tt.line, Minimum: github.MinimumVersion, InstallURL: installURL,
			}
			assert.Check(t, is.DeepEqual(*tooOld, want))
			assertCalls(t, fake, received(versionArgs()))
		})
	}
}

func TestForge_Identity_WhenGhIsAtOrAboveTheMinimum_GoesOnToTheLogin(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "next major release", line: "gh version 3.0.0 (2027-01-12)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := ghtest.Setup(t,
				ghtest.Says(versionAnswer(tt.line), versionArgs()...),
				ghtest.Says("Logged in\n", loginArgs()...),
				ghtest.Says(ghtest.RepoViewJSON(false, "WRITE"), repoViewArgs()...),
			)

			_, err := newForge().Identity(t.Context(), engine())

			assert.NilError(t, err)
			assertCalls(t, fake, received(versionArgs(), loginArgs(), repoViewArgs()))
		})
	}
}

func TestForge_Identity_WhenVersionReadExitsNonZero_ReturnsExitErrorNamingCall(t *testing.T) {
	const unknownFlag = "unknown flag\n"
	fake := ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stderr: unknownFlag, Code: 1}, versionArgs()...))

	_, err := newForge().Identity(t.Context(), engine())

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Identity error = %v, want *child.ExitError", err)
	want := child.ExitError{Argv: received(versionArgs())[0], Stderr: unknownFlag, Code: 1}
	assert.Check(t, is.DeepEqual(*exitErr, want))
	_, tooOld := errors.AsType[*child.ToolTooOldError](err)
	assert.Check(t, !tooOld, "Identity error = %v, want one that is no *child.ToolTooOldError", err)
	_, loggedOut := errors.AsType[*forge.NotLoggedInError](err)
	assert.Check(t, !loggedOut, "Identity error = %v, want one that is no *forge.NotLoggedInError", err)
	assertCalls(t, fake, received(versionArgs()))
}

func TestForge_Identity(t *testing.T) {
	tests := []struct {
		name string
		view string
		want forge.Repo
	}{
		{name: "write may push", view: ghtest.RepoViewJSON(false, "WRITE"), want: forge.Repo{CanPush: true}},
		{name: "maintain may push", view: ghtest.RepoViewJSON(false, "MAINTAIN"), want: forge.Repo{CanPush: true}},
		{name: "admin may push", view: ghtest.RepoViewJSON(false, "ADMIN"), want: forge.Repo{CanPush: true}},
		{name: "read may not push", view: ghtest.RepoViewJSON(false, "READ"), want: forge.Repo{}},
		{name: "triage may not push", view: ghtest.RepoViewJSON(false, "TRIAGE"), want: forge.Repo{}},
		{name: "no permission may not push", view: ghtest.RepoViewJSON(false, ""), want: forge.Repo{}},
		{name: "fork that may push", view: ghtest.RepoViewJSON(true, "ADMIN"), want: forge.Repo{Fork: true, CanPush: true}},
		{name: "fork that may not push", view: ghtest.RepoViewJSON(true, "READ"), want: forge.Repo{Fork: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := ghtest.Setup(t, ghtest.Says("Logged in\n", loginArgs()...), ghtest.Says(tt.view, repoViewArgs()...))
			want := engine()
			want.Fork, want.CanPush = tt.want.Fork, tt.want.CanPush

			got, err := newForge().Identity(t.Context(), engine())
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, want))
			assertCalls(t, fake, received(versionArgs(), loginArgs(), repoViewArgs()))
		})
	}
}

func TestForge_FindChanges_WhenBothStatesHaveChanges_ReturnsOpenThenMerged(t *testing.T) {
	open := ghtest.Change{Number: 7, URL: pullURL, Title: "feature", Base: "main", SHA: headSHA, Draft: true}
	merged := ghtest.Change{
		Number: 3, URL: "https://git.example.com/ada/engine/pull/3", Title: "feat: add the forge",
		Base: "release", SHA: "0a1b2c3",
	}
	ghtest.Setup(t,
		ghtest.Says(jsonArray(ghtest.ChangeJSON(open)), listArgs("open")...),
		ghtest.Says(jsonArray(ghtest.ChangeJSON(merged)), listArgs("merged")...),
	)

	got, err := newForge().FindChanges(t.Context(), engine(), featureBranch)
	assert.NilError(t, err)

	want := []forge.Change{
		{Number: 7, URL: pullURL, Title: "feature", Base: "main", HeadSHA: headSHA, Draft: true, State: forge.ChangeOpen},
		{
			Number: 3, URL: "https://git.example.com/ada/engine/pull/3", Title: "feat: add the forge",
			Base: "release", HeadSHA: "0a1b2c3", State: forge.ChangeMerged,
		},
	}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestForge_FindChanges_WhenPullRequestComesFromAnotherRepository_LeavesItOut(t *testing.T) {
	own := ghtest.Change{Number: 7, URL: pullURL, Title: "feature", Base: "main", SHA: headSHA}
	foreign := ghtest.Change{
		Number: 9, URL: "https://git.example.com/ada/engine/pull/9", Title: "feature",
		Base: "main", SHA: "0a1b2c3", CrossRepository: true,
	}
	listed := jsonArray(ghtest.ChangeJSON(foreign), ghtest.ChangeJSON(own))
	ghtest.Setup(t, ghtest.Says(listed, listArgs("open")...), ghtest.Says(jsonArray(ghtest.ChangeJSON(foreign)), listArgs("merged")...))

	got, err := newForge().FindChanges(t.Context(), engine(), featureBranch)
	assert.NilError(t, err)

	want := []forge.Change{{Number: 7, URL: pullURL, Title: "feature", Base: "main", HeadSHA: headSHA, State: forge.ChangeOpen}}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestForge_FindChanges_WhenBranchHasNone_ReturnsNoChanges(t *testing.T) {
	ghtest.Setup(t, ghtest.Says("[]", listArgs("open")...), ghtest.Says("[]", listArgs("merged")...))

	got, err := newForge().FindChanges(t.Context(), engine(), featureBranch)
	assert.NilError(t, err)

	assert.Check(t, is.Len(got, 0))
}

func TestForge_OpenChange_WhenCreated_ReturnsNumberAndURL(t *testing.T) {
	ghtest.Setup(t, ghtest.Says(pullURL+"\n", createArgs()...))

	got, err := newForge().OpenChange(t.Context(), engine(), featureChange())
	assert.NilError(t, err)

	want := forge.ChangeRef{Number: 7, URL: pullURL}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestForge_OpenChange_WhenPullRequestExists_ReturnsErrChangeExists(t *testing.T) {
	exists := ghtest.Answer{
		Stderr: "a pull request for branch \"feature\" into branch \"main\" already exists:\n" + pullURL + "\n",
		Code:   1,
	}
	ghtest.Setup(t, ghtest.Exits(exists, createArgs()...))

	_, err := newForge().OpenChange(t.Context(), engine(), featureChange())

	assert.Check(t, is.ErrorIs(err, forge.ErrChangeExists))
}

func TestForge_OpenChange_WhenGhFailsForAnotherReason_ReturnsExitErrorCarryingStderr(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
	}{
		{name: "no commits between the branches", stderr: "pull request create failed: GraphQL: No commits between main and feature (createPullRequest)\n"},
		{
			name: "several complaints at once",
			stderr: "pull request create failed: GraphQL: Head sha can't be blank, Base sha can't be blank, " +
				"No commits between main and x, Head ref must be a branch (createPullRequest)\n",
		},
		{name: "something else already exists", stderr: "error: remote ref already exists elsewhere\n"},
		{name: "a pull request exists for another reason", stderr: "pull request create failed: a pull request exists but is locked\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stderr: tt.stderr, Code: 1}, createArgs()...))

			_, err := newForge().OpenChange(t.Context(), engine(), featureChange())

			assert.Assert(t, !errors.Is(err, forge.ErrChangeExists), "OpenChange error = %v, want an error other than forge.ErrChangeExists", err)
			exitErr, ok := errors.AsType[*child.ExitError](err)
			assert.Assert(t, ok, "OpenChange error = %v, want *child.ExitError", err)
			assert.Check(t, is.Equal(exitErr.Stderr, tt.stderr))
		})
	}
}

func TestForge_OpenChange_WhenStdoutHoldsNoPullURL_ReturnsError(t *testing.T) {
	fake := ghtest.Setup(t, ghtest.Says("Creating pull request for feature into main\n", createArgs()...))

	got, err := newForge().OpenChange(t.Context(), engine(), featureChange())

	assert.Assert(t, err != nil, "OpenChange = %+v with a nil error, want an error", got)
	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Check(t, !ok, "OpenChange error = %#v, want one that comes from reading stdout", exitErr)
	assertCalls(t, fake, received(createArgs()))
}

func TestForge_OpenChange_WhenPullNumberIsNotANumber_ReturnsError(t *testing.T) {
	ghtest.Setup(t, ghtest.Says("https://git.example.com/ada/engine/pull/abc\n", createArgs()...))

	_, err := newForge().OpenChange(t.Context(), engine(), featureChange())

	assert.Check(t, is.ErrorIs(err, strconv.ErrSyntax))
}

func TestForge_Head_WhenStateNeedsNoCheck_AsksOnlyForThePullRequest(t *testing.T) {
	checked := []string{"build", "test"}
	tests := []struct {
		name     string
		view     string
		want     forge.Head
		required []string
	}{
		{name: "clean", view: headJSON("CLEAN", "APPROVED"), want: headOf(forge.MergeClean, "CLEAN"), required: checked},
		{name: "conflicting", view: pullJSON("CONFLICTING", "DIRTY", ""), want: headOf(forge.MergeConflicting, "DIRTY")},
		{name: "conflicting on a blocked status", view: pullJSON("CONFLICTING", "BLOCKED", ""), want: headOf(forge.MergeConflicting, "BLOCKED"), required: checked},
		{name: "mergeable not worked out yet", view: pullJSON("UNKNOWN", "BLOCKED", ""), want: headOf(forge.MergeUnknown, "BLOCKED"), required: checked},
		{
			name: "draft flag on a blocked status",
			view: `{"headRefOid":"` + headSHA + `","mergeable":"MERGEABLE","mergeStateStatus":"BLOCKED","isDraft":true,"reviewDecision":""}`,
			want: headOf(forge.MergeDraft, "BLOCKED"),
		},
		{name: "blocked by a review", view: headJSON("BLOCKED", "REVIEW_REQUIRED"), want: headOf(forge.MergeBlockedByReview, "BLOCKED"), required: checked},
		{name: "blocked by requested changes", view: headJSON("BLOCKED", "CHANGES_REQUESTED"), want: headOf(forge.MergeBlockedByReview, "BLOCKED"), required: checked},
		{name: "blocked with no required name", view: headJSON("BLOCKED", ""), want: headOf(forge.MergeBlockedOther, "BLOCKED")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := ghtest.Setup(t, ghtest.Says(tt.view, headArgs()...))

			got, err := newForge().Head(t.Context(), engine(), headQuery(tt.required...))
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, tt.want))
			assertCalls(t, fake, received(headArgs()))
		})
	}
}

func TestForge_Head_WhenBlockedAndTrunkRequiresChecks_ReportsMissingCheckOnlyWhenNothingCanStillReportIt(t *testing.T) {
	passing := jsonArray(ghtest.CheckJSON("test", "pass", runLink))
	completed := jsonArray(runJSON("completed"))
	optionalPending := jsonArray(ghtest.CheckJSON("test", "pass", runLink), ghtest.CheckJSON("lint", "pending", runLink))
	noChecks := noChecksAnswer()
	tests := []struct {
		name   string
		runs   string
		want   forge.MergeState
		checks ghtest.Answer
	}{
		{name: "required name absent, runs done", checks: ghtest.Answer{Stdout: passing}, runs: completed, want: forge.MergeBlockedByMissingCheck},
		{name: "required name absent, no run", checks: ghtest.Answer{Stdout: passing}, runs: "[]", want: forge.MergeBlockedByMissingCheck},
		{name: "required name absent, run waits on a person", checks: ghtest.Answer{Stdout: passing}, runs: jsonArray(runJSON("waiting")), want: forge.MergeBlockedByMissingCheck},
		{name: "required name absent, run queued", checks: ghtest.Answer{Stdout: passing}, runs: jsonArray(runJSON("queued")), want: forge.MergeBlockedOther},
		{name: "required name absent, run in progress", checks: ghtest.Answer{Stdout: passing}, runs: jsonArray(runJSON("in_progress")), want: forge.MergeBlockedOther},
		{name: "required name absent, run pending", checks: ghtest.Answer{Stdout: passing}, runs: jsonArray(runJSON("pending")), want: forge.MergeBlockedOther},
		{name: "required name absent, run in a status this version does not know", checks: ghtest.Answer{Stdout: passing}, runs: jsonArray(runJSON("paused")), want: forge.MergeBlockedOther},
		{name: "required name absent, only an optional check running", checks: ghtest.Answer{Stdout: optionalPending, Code: 8}, runs: completed, want: forge.MergeBlockedByMissingCheck},
		{
			name:   "every required name absent, only an optional check running",
			checks: ghtest.Answer{Stdout: jsonArray(ghtest.CheckJSON("lint", "pending", runLink)), Code: 8},
			runs:   completed,
			want:   forge.MergeBlockedByMissingCheck,
		},
		{name: "no check reported, no run", checks: noChecks, runs: "[]", want: forge.MergeBlockedByMissingCheck},
		{
			name:   "required name absent, other required check still running",
			checks: ghtest.Answer{Stdout: jsonArray(ghtest.CheckJSON("test", "pending", runLink)), Code: 8},
			runs:   completed,
			want:   forge.MergeBlockedOther,
		},
		{
			name:   "every required name has a check",
			checks: ghtest.Answer{Stdout: jsonArray(ghtest.CheckJSON("build", "pass", runLink), ghtest.CheckJSON("test", "fail", runLink)), Code: 1},
			runs:   completed,
			want:   forge.MergeBlockedOther,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t,
				ghtest.Says(headJSON("BLOCKED", "APPROVED"), headArgs()...),
				ghtest.Exits(tt.checks, checksArgs()...),
				ghtest.Says(tt.runs, runsArgs()...),
			)

			got, err := newForge().Head(t.Context(), engine(), headQuery("build", "test"))
			assert.NilError(t, err)

			want := headOf(tt.want, "BLOCKED")
			assert.Check(t, is.DeepEqual(got, want))
		})
	}
}

func TestForge_TrunkProtection(t *testing.T) {
	rules := `[
		{"type":"pull_request","parameters":{"required_approving_review_count":1}},
		{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"build"},{"context":"test"}]}},
		{"type":"deletion"},
		{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"test"},{"context":"lint"}]}}
	]`
	tests := []struct {
		name   string
		want   []string
		answer ghtest.Answer
	}{
		{name: "contexts of every status-check rule, once each", answer: ghtest.Answer{Stdout: rules}, want: []string{"build", "test", "lint"}},
		{name: "no rule on the trunk", answer: ghtest.Answer{Stdout: "[]"}},
		{name: "rules without a status-check rule", answer: ghtest.Answer{Stdout: `[{"type":"deletion"}]`}},
		{
			name:   "free private plan answers 403",
			answer: ghtest.Answer{Stdout: `{"message":"Upgrade to GitHub Pro"}`, Stderr: "gh: Upgrade to GitHub Pro (HTTP 403)\n", Code: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Exits(tt.answer, rulesArgs()...))

			got, err := newForge().TrunkProtection(t.Context(), engine(), trunk)
			assert.NilError(t, err)

			want := forge.Protection{RequiredChecks: tt.want}
			assert.Check(t, is.DeepEqual(got, want, cmpopts.EquateEmpty()))
		})
	}
}

func TestForge_TrunkProtection_ReadsRulesOfExactlyTheTrunk(t *testing.T) {
	rules := `[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"build"}]}}]`
	tests := []struct {
		name   string
		branch string
		path   string
	}{
		{name: "plain name", branch: "main", path: "repos/ada/engine/rules/branches/main"},
		{name: "slash keeps separating segments", branch: "release/v1", path: "repos/ada/engine/rules/branches/release/v1"},
		{name: "hash is escaped, not a fragment", branch: "release#1", path: "repos/ada/engine/rules/branches/release%231"},
		{name: "percent is escaped, not an escape", branch: "100%", path: "repos/ada/engine/rules/branches/100%25"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"api", "--hostname", engineHost, "--paginate", tt.path}
			fake := ghtest.Setup(t, ghtest.Says(rules, args...))

			got, err := newForge().TrunkProtection(t.Context(), engine(), tt.branch)
			assert.NilError(t, err)

			assert.Check(t, is.Contains(fake.Calls(), received(args)[0]))
			assert.Check(t, is.DeepEqual(got, forge.Protection{RequiredChecks: []string{"build"}}))
		})
	}
}

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

func TestForge_Verdict_FailedLog(t *testing.T) {
	const logText = "build\tRun tests\t--- FAIL: TestForge (0.01s)\nbuild\tRun tests\tFAIL\n"

	logged := ghtest.Answer{Stdout: logText}
	gone := ghtest.Answer{Stderr: "run " + runID + " not found\n", Code: 1}
	// unfinished is what gh run view --log-failed says of a run that has not completed.
	unfinished := ghtest.Answer{Stderr: "run " + runID + " is still in progress; logs will be available when it is complete\n", Code: 1}
	read := forge.FailedLog{Check: "build", Text: logText, State: forge.FailedLogRead}
	noRun := forge.FailedLog{Check: "scan", State: forge.FailedLogNoRun}
	unreadable := forge.FailedLog{Check: "build", State: forge.FailedLogUnreadable}
	inProgress := forge.FailedLog{Check: "build", State: forge.FailedLogInProgress}
	withLog := received(checksArgs(), runsArgs(), logArgs())
	noLog := received(checksArgs(), runsArgs())
	tests := []struct {
		name   string
		checks string
		// wantCalls is every argv gh receives: the log is read only for a failing check of a run.
		wantCalls [][]string
		want      forge.FailedLog
		// required names the checks the query requires; none makes every check decide.
		required []string
		log      ghtest.Answer
	}{
		{name: "failing check of a run", checks: ghtest.CheckJSON("build", "fail", runLink), log: logged, want: read, wantCalls: withLog},
		{name: "canceled check of a run", checks: ghtest.CheckJSON("build", "cancel", runLink), log: logged, want: read, wantCalls: withLog},
		{name: "passing check ahead of the failing one", checks: ghtest.CheckJSON("lint", "pass", statusLink) + "," + ghtest.CheckJSON("build", "fail", runLink), log: logged, want: read, wantCalls: withLog},
		{name: "failing status ahead of a failing check of a run", checks: ghtest.CheckJSON("scan", "fail", statusLink) + "," + ghtest.CheckJSON("build", "fail", runLink), log: logged, want: noRun, wantCalls: noLog},
		{name: "failing status with no run", checks: ghtest.CheckJSON("scan", "fail", statusLink), log: logged, want: noRun, wantCalls: noLog},
		{name: "run view fails", checks: ghtest.CheckJSON("build", "fail", runLink), log: gone, want: unreadable, wantCalls: withLog},
		{name: "run of the failing check still in progress", checks: ghtest.CheckJSON("build", "fail", runLink), log: unfinished, want: inProgress, wantCalls: withLog},
		{name: "check of a run still pending", checks: ghtest.CheckJSON("build", "pending", runLink), log: logged, wantCalls: noLog},
		{name: "every check passed", checks: ghtest.CheckJSON("build", "pass", runLink), log: logged, wantCalls: noLog},
		{
			name:     "first required failing check has no run",
			checks:   ghtest.CheckJSON("lint", "fail", runLink) + "," + ghtest.CheckJSON("scan", "fail", statusLink) + "," + ghtest.CheckJSON("build", "fail", runLink),
			required: []string{"scan", "build"}, log: logged, want: noRun, wantCalls: noLog,
		},
		{
			name:     "failing check is not required",
			checks:   ghtest.CheckJSON("lint", "fail", runLink) + "," + ghtest.CheckJSON("build", "pass", runLink),
			required: []string{"build"}, log: logged, wantCalls: noLog,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := ghtest.Setup(t, ghtest.Says(jsonArray(tt.checks), checksArgs()...), ghtest.Says("[]", runsArgs()...), ghtest.Exits(tt.log, logArgs()...))

			got, err := newForge().Verdict(t.Context(), engine(), verdictQuery(tt.required...))
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got.FailedLog, tt.want))
			assertCalls(t, fake, tt.wantCalls)
		})
	}
}

func TestForge_HasTrunkCI(t *testing.T) {
	ci := workflowJSON(ciFile, "active")
	dependabot := workflowJSON(dependabotPath, "active")
	autoSubmission := workflowJSON("dynamic/dependency-graph/auto-submission", "active")
	ciOnTrunk := ghtest.Answer{Stdout: ciOnTrunkJSON()}
	releaseOnTrunk := ghtest.Answer{Stdout: jsonArray(fileJSON(releaseFile))}
	// filesFail is a trunk read that fails: a row scripting it answers without reading the trunk.
	filesFail := ghtest.Answer{Stderr: serverDown, Code: 1}
	tests := []struct {
		name      string
		workflows string
		files     ghtest.Answer
		want      bool
	}{
		{name: "active workflow whose file is on the trunk", workflows: workflowsPage(dependabot, ci), files: ciOnTrunk, want: true},
		{name: "active workflow on the second page", workflows: workflowsPage(dependabot) + workflowsPage(ci), files: ciOnTrunk, want: true},
		{name: "workflow disabled by hand", workflows: workflowsPage(workflowJSON(ciFile, "disabled_manually")), files: ciOnTrunk},
		{name: "workflow disabled for inactivity", workflows: workflowsPage(workflowJSON(ciFile, "disabled_inactivity")), files: ciOnTrunk},
		{name: "only dynamic workflows", workflows: workflowsPage(dependabot, autoSubmission), files: filesFail},
		{name: "active workflow whose file is not on the trunk", workflows: workflowsPage(ci), files: releaseOnTrunk},
		{name: "no workflow", workflows: workflowsPage(), files: ciOnTrunk},
		{name: "trunk without a workflows directory", workflows: workflowsPage(ci), files: notFoundAnswer()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Says(tt.workflows, workflowsArgs()...), ghtest.Exits(tt.files, workflowFilesArgs()...))

			got, err := newForge().HasTrunkCI(t.Context(), engine(), trunk)
			assert.NilError(t, err)

			assert.Check(t, is.Equal(got, tt.want))
		})
	}
}

func TestForge_HasTrunkCI_WhenWorkflowListAnswers404_ReturnsExitError(t *testing.T) {
	ghtest.Setup(t, ghtest.Exits(notFoundAnswer(), workflowsArgs()...))

	_, err := newForge().HasTrunkCI(t.Context(), engine(), trunk)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "HasTrunkCI error = %v, want *child.ExitError", err)
	assert.Check(t, is.DeepEqual(exitErr.Argv, received(workflowsArgs())[0]))
}

func TestForge_HasTrunkCI_WhenTrunkRefIsMissing_ReturnsExitError(t *testing.T) {
	noCommit := notFound404("No commit found for the ref " + trunk)
	ghtest.Setup(t, ghtest.Says(activeCIList(), workflowsArgs()...), ghtest.Exits(noCommit, workflowFilesArgs()...))

	got, err := newForge().HasTrunkCI(t.Context(), engine(), trunk)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "HasTrunkCI error = %v, want *child.ExitError", err)
	assert.Check(t, is.DeepEqual(exitErr.Argv, received(workflowFilesArgs())[0]))
	assert.Check(t, is.Equal(exitErr.Stderr, noCommit.Stderr))
	assert.Check(t, !got, "HasTrunkCI = CI alongside an error, want false")
}

func TestForge_HasTrunkCI_WhenWorkflowListPrintsNothing_ReturnsDecodeError(t *testing.T) {
	ghtest.Setup(t, ghtest.Says("", workflowsArgs()...))

	_, err := newForge().HasTrunkCI(t.Context(), engine(), trunk)

	decodeErr, ok := errors.AsType[*github.DecodeError](err)
	assert.Assert(t, ok, "HasTrunkCI error = %v, want *github.DecodeError", err)
	assert.Check(t, is.DeepEqual(decodeErr.Argv, received(workflowsArgs())[0]))
}

func TestForge_HasTrunkCI_WhenTrunkNameHoldsQuerySyntax_SendsItEscaped(t *testing.T) {
	const release = "release/1.0+2"
	ghtest.Setup(t, ghtest.Says(activeCIList(), workflowsArgs()...), ghtest.Says(ciOnTrunkJSON(), workflowFilesArgsAt("release%2F1.0%2B2")...))

	got, err := newForge().HasTrunkCI(t.Context(), engine(), release)
	assert.NilError(t, err)

	assert.Check(t, got)
}

// ---------------------------------------------------------------------------
// Merge state mapping
// ---------------------------------------------------------------------------

func TestMergeStateOf(t *testing.T) {
	t.Parallel()

	const mergeable = "MERGEABLE"

	tests := []struct {
		name  string
		want  forge.MergeState
		facts github.MergeFacts
	}{
		{name: "conflicting wins over a clean status", facts: github.MergeFacts{Mergeable: "CONFLICTING", Status: "CLEAN"}, want: forge.MergeConflicting},
		{name: "conflicting wins over a blocked status", facts: github.MergeFacts{Mergeable: "CONFLICTING", Status: "BLOCKED"}, want: forge.MergeConflicting},
		{name: "mergeable not worked out yet", facts: github.MergeFacts{Mergeable: "UNKNOWN", Status: "CLEAN"}, want: forge.MergeUnknown},
		{name: "clean", facts: github.MergeFacts{Mergeable: mergeable, Status: "CLEAN"}, want: forge.MergeClean},
		{name: "clean with hooks", facts: github.MergeFacts{Mergeable: mergeable, Status: "HAS_HOOKS"}, want: forge.MergeClean},
		{name: "unstable still lands", facts: github.MergeFacts{Mergeable: mergeable, Status: "UNSTABLE"}, want: forge.MergeClean},
		{name: "behind the base", facts: github.MergeFacts{Mergeable: mergeable, Status: "BEHIND"}, want: forge.MergeBehind},
		{name: "dirty", facts: github.MergeFacts{Mergeable: mergeable, Status: "DIRTY"}, want: forge.MergeConflicting},
		{name: "draft status", facts: github.MergeFacts{Mergeable: mergeable, Status: "DRAFT"}, want: forge.MergeDraft},
		{name: "draft flag on a blocked status", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED", Draft: true}, want: forge.MergeDraft},
		{name: "draft flag on a behind status", facts: github.MergeFacts{Mergeable: mergeable, Status: "BEHIND", Draft: true}, want: forge.MergeDraft},
		{name: "draft flag on a dirty status", facts: github.MergeFacts{Mergeable: mergeable, Status: "DIRTY", Draft: true}, want: forge.MergeDraft},
		{name: "draft flag on a clean status", facts: github.MergeFacts{Mergeable: mergeable, Status: "CLEAN", Draft: true}, want: forge.MergeDraft},
		{name: "draft flag on a clean status with hooks", facts: github.MergeFacts{Mergeable: mergeable, Status: "HAS_HOOKS", Draft: true}, want: forge.MergeDraft},
		{name: "draft flag on an unstable status", facts: github.MergeFacts{Mergeable: mergeable, Status: "UNSTABLE", Draft: true}, want: forge.MergeDraft},
		{name: "draft flag on a status not worked out yet", facts: github.MergeFacts{Mergeable: mergeable, Status: "UNKNOWN", Draft: true}, want: forge.MergeDraft},
		{name: "conflicting wins over the draft flag", facts: github.MergeFacts{Mergeable: "CONFLICTING", Status: "DIRTY", Draft: true}, want: forge.MergeConflicting},
		{name: "conflicting wins over a draft status", facts: github.MergeFacts{Mergeable: "CONFLICTING", Status: "DRAFT", Draft: true}, want: forge.MergeConflicting},
		{name: "mergeable not worked out yet wins over the draft flag", facts: github.MergeFacts{Mergeable: "UNKNOWN", Status: "UNKNOWN", Draft: true}, want: forge.MergeUnknown},
		{name: "mergeable not worked out yet on a blocked status", facts: github.MergeFacts{Mergeable: "UNKNOWN", Status: "BLOCKED"}, want: forge.MergeUnknown},
		{name: "mergeable not worked out yet on a behind status", facts: github.MergeFacts{Mergeable: "UNKNOWN", Status: "BEHIND"}, want: forge.MergeUnknown},
		{name: "mergeable not worked out yet on a dirty status", facts: github.MergeFacts{Mergeable: "UNKNOWN", Status: "DIRTY"}, want: forge.MergeUnknown},
		{name: "mergeable not worked out yet on a draft status", facts: github.MergeFacts{Mergeable: "UNKNOWN", Status: "DRAFT", Draft: true}, want: forge.MergeUnknown},
		{name: "blocked, review required", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED", Review: "REVIEW_REQUIRED"}, want: forge.MergeBlockedByReview},
		{name: "blocked, changes requested", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED", Review: "CHANGES_REQUESTED"}, want: forge.MergeBlockedByReview},
		{name: "blocked, review required and a check missing", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED", Review: "REVIEW_REQUIRED", MissingCheck: true}, want: forge.MergeBlockedByReview},
		{name: "blocked, approved and a check missing", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED", Review: "APPROVED", MissingCheck: true}, want: forge.MergeBlockedByMissingCheck},
		{name: "blocked, no review asked and a check missing", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED", MissingCheck: true}, want: forge.MergeBlockedByMissingCheck},
		{name: "blocked, approved and no check missing", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED", Review: "APPROVED"}, want: forge.MergeBlockedOther},
		{name: "blocked, no review asked and no check missing", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED"}, want: forge.MergeBlockedOther},
		{name: "status not worked out yet", facts: github.MergeFacts{Mergeable: mergeable, Status: "UNKNOWN"}, want: forge.MergeUnknown},
		{name: "status this version does not know", facts: github.MergeFacts{Mergeable: mergeable, Status: "QUEUED_FOR_MERGE"}, want: forge.MergeUnknown},
		{name: "no status at all", facts: github.MergeFacts{Mergeable: mergeable}, want: forge.MergeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := github.MergeStateOf(tt.facts)

			assert.Check(t, is.Equal(got, tt.want), "MergeStateOf(%+v)", tt.facts)
		})
	}
}

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

func TestVersionOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "release with a date", line: "gh version 2.50.0 (2024-05-29)", want: "2.50.0"},
		{name: "release without a date", line: "gh version 2.100.3", want: "2.100.3"},
		{name: "pre-release keeps its suffix", line: "gh version 2.50.0-rc.1", want: "2.50.0-rc.1"},
		{name: "development build", line: "gh version DEV"},
		{name: "no gh version prefix", line: "2.50.0"},
		{name: "empty line"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := github.VersionOf(tt.line)

			assert.Check(t, is.Equal(got, tt.want), "VersionOf(%q)", tt.line)
		})
	}
}

// missingTool is the error every call returns when `gh` is not installed.
func missingTool() *child.ToolMissingError {
	return &child.ToolMissingError{Tool: program, InstallURL: installURL}
}

// assertMissingTool fails the test unless err is the missingTool error, ignoring its cause, and
// returns that error.
func assertMissingTool(t *testing.T, err error, label string) *child.ToolMissingError {
	t.Helper()

	missing, ok := errors.AsType[*child.ToolMissingError](err)
	assert.Assert(t, ok, "%s error = %v, want *child.ToolMissingError", label, err)
	assert.Check(t, is.DeepEqual(missing, missingTool(), cmpopts.IgnoreFields(child.ToolMissingError{}, "Err")))

	return missing
}

func TestRunner_Run_WhenGhExitsZero_ReturnsOutput(t *testing.T) {
	args := []string{"pr", "view", "--json", "number"}
	ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "{\"number\":7}\n", Stderr: "notice\n"}, args...))

	got, err := github.Runner{}.Run(t.Context(), args...)
	assert.NilError(t, err)

	want := github.Result{Stdout: "{\"number\":7}\n", Stderr: "notice\n"}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestRunner_Run_WhenArgHoldsShellSyntax_PassesItVerbatim(t *testing.T) {
	value := "first && second; $HOME `third` > fourth"
	args := []string{"api", value}
	fake := ghtest.Setup(t, ghtest.Says("matched\n", args...))

	_, err := github.Runner{}.Run(t.Context(), args...)
	assert.NilError(t, err)

	assertCalls(t, fake, [][]string{{"gh", "api", value}})
}

func TestRunner_Run_WhenGhExitsNonZero_ReturnsExitErrorAndStdout(t *testing.T) {
	args := []string{"pr", "checks", "7"}
	ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "build\tpending\n", Stderr: "still running\n", Code: 8}, args...))

	got, err := github.Runner{}.Run(t.Context(), args...)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Run(pr checks) error = %v, want *child.ExitError", err)
	wantErr := child.ExitError{
		Argv:   []string{"gh", "pr", "checks", "7"},
		Stderr: "still running\n",
		Code:   8,
	}
	assert.Check(t, is.DeepEqual(*exitErr, wantErr))
	assert.Check(t, is.Equal(got.Stdout, "build\tpending\n"))
}

func TestRunner_Run_WhenContextEndsWhileGhRuns_ReturnsContextError(t *testing.T) {
	args := []string{"pr", "checks", "7"}
	ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "pass\n", Delay: 30 * time.Second}, args...))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	_, err := github.Runner{}.Run(ctx, args...)

	assert.Check(t, is.ErrorIs(err, context.DeadlineExceeded))
}

func TestRunner_Run_WhenGhMissing_ReturnsToolMissingError(t *testing.T) {
	tests := []struct {
		name   string
		ghPath string
	}{
		{name: "GH_PATH names no file", ghPath: filepath.Join(t.TempDir(), "gh")},
		{name: "no GH_PATH and no gh on PATH", ghPath: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withoutGh(t, tt.ghPath)

			_, err := github.Runner{}.Run(t.Context(), "pr", "view")

			missing := assertMissingTool(t, err, "Run(pr view)")
			assert.Check(t, missing.Err != nil, "Run(pr view) error carries no cause, want what starting gh failed with")
		})
	}
}
