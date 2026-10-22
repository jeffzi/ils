package github_test

import (
	"context"
	"errors"
	"path"
	"strconv"
	"strings"
	"testing"

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

// newForge is a forge that runs the fake `gh`.
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

// loginArgs is the argv of the login read on the engine's host.
func loginArgs() []string { return ghtest.LoginArgs(engineHost) }

func repoViewArgs() []string { return ghtest.RepoViewArgs(engineSlug) }

// listArgs is the argv of the list of the feature branch's pull requests in state.
func listArgs(state string) []string { return ghtest.ListArgs(engineSlug, featureBranch, state) }

func createArgs() []string {
	return ghtest.CreateArgs(engineSlug, featureBranch, trunk, featureBranch)
}

func headArgs() []string { return ghtest.HeadArgs(engineSlug, pullNumber) }

func rulesArgs() []string { return ghtest.RulesArgs(engineHost, "ada/engine", trunk) }

func checksArgs() []string { return ghtest.ChecksArgs(engineSlug, pullNumber) }

func runsArgs() []string { return ghtest.RunsArgs(engineSlug, headSHA) }

func logArgs() []string { return ghtest.LogArgs(engineSlug, runID) }

func messageArgs() []string { return ghtest.MessageArgs(engineHost, "ada", "engine", pullNumber) }

// featureLanding is the landing every Land test asks for.
func featureLanding() forge.Landing {
	return forge.Landing{Number: pullNumber, HeadSHA: headSHA, Subject: "feat: read the message (#7)", Body: "Lands the change."}
}

// pullRequestRule is one `pull_request` rule of the rules read's answer, from the ruleset with the
// id id, allowing methods, a JSON array.
func pullRequestRule(id int, methods string) string {
	return `{"type":"pull_request","ruleset_source_type":"Repository","ruleset_source":"ada/engine",` +
		`"ruleset_id":` + strconv.Itoa(id) +
		`,"parameters":{"required_approving_review_count":0,"allowed_merge_methods":` + methods + `}}`
}

func workflowsArgs() []string {
	return []string{"api", "--hostname", engineHost, "--paginate", "repos/ada/engine/actions/workflows"}
}

func workflowFilesArgs() []string { return workflowFilesArgsAt(trunk) }

// workflowFilesArgsAt is the argv of the read of the workflow directory at ref.
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

func runJSON(status string) string {
	return ghtest.RunJSON(status, "Add the forge")
}

// jsonArray is a JSON array of items, each already JSON.
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

// headQuery is the head read of the pull request, requiring the checks named required.
func headQuery(required ...string) forge.HeadQuery {
	return forge.HeadQuery{Number: pullNumber, Required: required}
}

// verdictQuery is the verdict read of the head commit, requiring the checks named required.
func verdictQuery(required ...string) forge.VerdictQuery {
	return forge.VerdictQuery{SHA: headSHA, Number: pullNumber, Required: required}
}

// operations is every forge operation, each reduced to its error.
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
		{name: "Message", run: func(ctx context.Context, f *github.Forge) error {
			_, err := f.Message(ctx, engine(), pullNumber)
			return err
		}},
		{name: "Land", run: func(ctx context.Context, f *github.Forge) error {
			return f.Land(ctx, engine(), featureLanding())
		}},
		{name: "DeleteBranch", run: func(ctx context.Context, f *github.Forge) error {
			return f.DeleteBranch(ctx, engine(), featureBranch)
		}},
	}
}

func operationsByName() map[string]func(context.Context, *github.Forge) error {
	byName := map[string]func(context.Context, *github.Forge) error{}
	for _, op := range operations() {
		byName[op.name] = op.run
	}

	return byName
}

// readCases is every `gh` read an operation makes, with the calls that must succeed before it.
func readCases() []readCase {
	byName := operationsByName()

	identity, find, head, protection, verdict := byName["Identity"], byName["FindChanges"], byName["Head"], byName["TrunkProtection"], byName["Verdict"]
	hasCI, message := byName["HasTrunkCI"], byName["Message"]
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
		{name: "api graphql message", run: message, args: messageArgs()},
	}
}

// ---------------------------------------------------------------------------
// Forge operations
// ---------------------------------------------------------------------------

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
	const line = "gh version 2.49.0 (2024-05-13)"
	fake := ghtest.Setup(t, ghtest.Says(versionAnswer(line), versionArgs()...), ghtest.Exits(loggedOutAnswer(), loginArgs()...))

	_, err := newForge().Identity(t.Context(), engine())

	tooOld, ok := errors.AsType[*child.ToolTooOldError](err)
	assert.Assert(t, ok, "Identity error = %v, want *child.ToolTooOldError", err)
	want := child.ToolTooOldError{
		Tool: program, Version: "2.49.0", Line: line, Minimum: github.MinimumVersion, InstallURL: installURL,
	}
	assert.Check(t, is.DeepEqual(*tooOld, want))
	assertCalls(t, fake, received(versionArgs()))
}

func TestForge_Identity_WhenGhIsAtOrAboveTheMinimum_GoesOnToTheLogin(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "exactly the minimum", line: "gh version " + github.MinimumVersion + " (2024-05-29)"},
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
		{name: "write may push", view: ghtest.RepoViewJSON(false, "WRITE"), want: forge.Repo{CanPush: true, SquashAllowed: true}},
		{name: "maintain may push", view: ghtest.RepoViewJSON(false, "MAINTAIN"), want: forge.Repo{CanPush: true, SquashAllowed: true}},
		{name: "admin may push", view: ghtest.RepoViewJSON(false, "ADMIN"), want: forge.Repo{CanPush: true, SquashAllowed: true}},
		{name: "read may not push", view: ghtest.RepoViewJSON(false, "READ"), want: forge.Repo{SquashAllowed: true}},
		{name: "triage may not push", view: ghtest.RepoViewJSON(false, "TRIAGE"), want: forge.Repo{SquashAllowed: true}},
		{name: "no permission may not push", view: ghtest.RepoViewJSON(false, ""), want: forge.Repo{SquashAllowed: true}},
		{name: "fork that may push", view: ghtest.RepoViewJSON(true, "ADMIN"), want: forge.Repo{Fork: true, CanPush: true, SquashAllowed: true}},
		{name: "fork that may not push", view: ghtest.RepoViewJSON(true, "READ"), want: forge.Repo{Fork: true, SquashAllowed: true}},
		{name: "settings forbid squash merges", view: ghtest.RepoViewSquashJSON(false, "WRITE", false), want: forge.Repo{CanPush: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := ghtest.Setup(t, ghtest.Says("Logged in\n", loginArgs()...), ghtest.Says(tt.view, repoViewArgs()...))
			want := engine()
			want.Fork, want.CanPush, want.SquashAllowed = tt.want.Fork, tt.want.CanPush, tt.want.SquashAllowed

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
		{name: "conflicting on a blocked status", view: pullJSON("CONFLICTING", "BLOCKED", ""), want: headOf(forge.MergeConflicting, "BLOCKED"), required: checked},
		{name: "mergeable not worked out yet", view: pullJSON("UNKNOWN", "BLOCKED", ""), want: headOf(forge.MergeUnknown, "BLOCKED"), required: checked},
		{
			name:     "draft flag on a blocked status",
			view:     `{"headRefOid":"` + headSHA + `","mergeable":"MERGEABLE","mergeStateStatus":"BLOCKED","isDraft":true,"reviewDecision":""}`,
			want:     headOf(forge.MergeDraft, "BLOCKED"),
			required: checked,
		},
		{name: "blocked by a review", view: headJSON("BLOCKED", "REVIEW_REQUIRED"), want: headOf(forge.MergeBlockedByReview, "BLOCKED"), required: checked},
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
	statusChecks := `[
		{"type":"pull_request","parameters":{"required_approving_review_count":1}},
		{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"build"},{"context":"test"}]}},
		{"type":"deletion"},
		{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"test"},{"context":"lint"}]}}
	]`
	squash := forge.Protection{SquashAllowed: true}
	// forbiddenBy is the protection whose squash merges the rulesets with the ids ids forbid.
	forbiddenBy := func(ids ...int) forge.Protection { return forge.Protection{SquashForbiddenBy: ids} }
	// rules is the rules read's answer holding the rules given, each a JSON object.
	rules := func(given ...string) ghtest.Answer { return ghtest.Answer{Stdout: jsonArray(given...)} }
	tests := []struct {
		name   string
		want   forge.Protection
		answer ghtest.Answer
	}{
		{
			name:   "contexts of every status-check rule, once each",
			answer: ghtest.Answer{Stdout: statusChecks},
			want:   forge.Protection{RequiredChecks: []string{"build", "test", "lint"}, SquashAllowed: true},
		},
		{name: "no rule on the trunk", answer: rules(), want: squash},
		{name: "rules without a status-check or pull request rule", answer: rules(`{"type":"deletion"}`), want: squash},
		{
			name:   "free private plan answers 403",
			answer: ghtest.Answer{Stdout: `{"message":"Upgrade to GitHub Pro"}`, Stderr: "gh: Upgrade to GitHub Pro (HTTP 403)\n", Code: 1},
			want:   squash,
		},
		{name: "pull request rule listing squash", answer: rules(pullRequestRule(42, `["merge","squash","rebase"]`)), want: squash},
		{name: "pull request rule without squash", answer: rules(pullRequestRule(42, `["merge"]`)), want: forbiddenBy(42)},
		{name: "every pull request rule lists squash", answer: rules(pullRequestRule(42, `["squash"]`), pullRequestRule(51, `["squash","rebase"]`)), want: squash},
		{name: "one of two pull request rules without squash", answer: rules(pullRequestRule(42, `["squash"]`), pullRequestRule(51, `["rebase"]`)), want: forbiddenBy(51)},
		{name: "the rule without squash ahead of one with it", answer: rules(pullRequestRule(42, `["rebase"]`), pullRequestRule(51, `["squash"]`)), want: forbiddenBy(42)},
		{name: "two pull request rules without squash", answer: rules(pullRequestRule(42, `["merge"]`), pullRequestRule(51, `["rebase"]`)), want: forbiddenBy(42, 51)},
		{name: "pull request rule with an empty methods list", answer: rules(pullRequestRule(42, `[]`)), want: forbiddenBy(42)},
		{
			name:   "pull request rule with no allowed methods field",
			answer: rules(`{"type":"pull_request","ruleset_id":42,"parameters":{"required_approving_review_count":1}}`),
			want:   squash,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Exits(tt.answer, rulesArgs()...))

			got, err := newForge().TrunkProtection(t.Context(), engine(), trunk)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, tt.want, cmpopts.EquateEmpty()))
		})
	}
}

func TestForge_TrunkProtection_WhenRefusalIsNotTheFreePlans_ReturnsExitError(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
	}{
		{name: "403 for another reason", stderr: "gh: Resource not accessible by integration (HTTP 403)\n"},
		{name: "upgrade wording under another status", stderr: "gh: Upgrade to GitHub Pro (HTTP 404)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stderr: tt.stderr, Code: 1}, rulesArgs()...))

			_, err := newForge().TrunkProtection(t.Context(), engine(), trunk)

			exitErr, ok := errors.AsType[*child.ExitError](err)
			assert.Assert(t, ok, "error = %v, want *child.ExitError", err)
			assert.Check(t, is.Equal(exitErr.Stderr, tt.stderr))
		})
	}
}

func TestForge_TrunkProtection_WhenTrunkNameHoldsPathSyntax_ReadsExactlyItsRules(t *testing.T) {
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
			assert.Check(t, is.DeepEqual(got, forge.Protection{RequiredChecks: []string{"build"}, SquashAllowed: true}))
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

func TestForge_Message(t *testing.T) {
	const (
		title     = "feat: read the message"
		pullBody  = "Lands the change.\n\nCo-authored-by: Grace Hopper <grace@example.com>"
		signedOff = "Signed-off-by: Ada Lovelace <ada@example.com>"
		liveSuite = "Co-authored-by: ils live suite <ils-live@example.com>"
	)
	tests := []struct {
		name string
		// mergeBody is the body GitHub would compose for a squash merge by the logged-in user.
		mergeBody string
		want      []string
	}{
		{
			name:      "commit messages setting",
			mergeBody: "* Add the forge\n\n* Read the message\n\n---------\n\n" + signedOff + "\n" + liveSuite,
			want:      []string{signedOff, liveSuite},
		},
		{
			name:      "pull request body setting, the body's own trailer left out",
			mergeBody: pullBody + "\n\n---------\n\n" + liveSuite,
			want:      []string{liveSuite},
		},
		{name: "blank setting", mergeBody: liveSuite, want: []string{liveSuite}},
		{name: "empty body text"},
		{name: "commit messages with no trailer anywhere", mergeBody: "* Add the forge\n\n* fix: read the message"},
		{name: "last paragraph mixes prose with a trailer", mergeBody: "Lands the change.\n\nThanks to Grace.\n" + liveSuite},
		{name: "token holding a space", mergeBody: "Lands the change.\n\nThanks to: Grace Hopper"},
		{name: "token holding an underscore", mergeBody: "Lands the change.\n\nReviewed_by: Grace Hopper"},
		{name: "token with no value", mergeBody: "Lands the change.\n\nReviewed-by:"},
		{name: "no space after the colon", mergeBody: "Lands the change.\n\nReviewed-by:Grace Hopper"},
		{name: "body text ending in a newline", mergeBody: liveSuite + "\n", want: []string{liveSuite}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := ghtest.Setup(t, ghtest.Says(ghtest.MessageJSON(title, pullBody, tt.mergeBody), messageArgs()...))

			got, err := newForge().Message(t.Context(), engine(), pullNumber)
			assert.NilError(t, err)

			want := forge.Message{Title: title, Body: pullBody, Trailers: tt.want}
			assert.Check(t, is.DeepEqual(got, want, cmpopts.EquateEmpty()))
			assertCalls(t, fake, received(messageArgs()))
		})
	}
}

func TestForge_Message_WhenAnswerHoldsNoPullRequest_ReturnsDecodeError(t *testing.T) {
	tests := []struct {
		name   string
		answer string
	}{
		{name: "no pull request", answer: `{"data":{"repository":{"pullRequest":null}}}`},
		{name: "no repository", answer: `{"data":{"repository":null}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Says(tt.answer, messageArgs()...))

			_, err := newForge().Message(t.Context(), engine(), pullNumber)

			decodeErr, ok := errors.AsType[*github.DecodeError](err)
			assert.Assert(t, ok, "error = %v, want *github.DecodeError", err)
			assert.Check(t, is.DeepEqual(decodeErr.Argv, received(messageArgs())[0]))
			assert.Check(t, is.ErrorIs(err, github.ErrNoPullRequest))
		})
	}
}

func TestForge_Land(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "body with a trailer block", body: "Lands the change.\n\nCo-authored-by: ils live suite <ils-live@example.com>"},
		{name: "empty body still sent", body: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			landing := featureLanding()
			landing.Body = tt.body
			args := []string{
				"pr", "merge", "7", "--repo", engineSlug, "--squash", "--match-head-commit", headSHA,
				"--subject", landing.Subject, "--body", tt.body,
			}
			fake := ghtest.Setup(t, ghtest.Says("", args...))

			err := newForge().Land(t.Context(), engine(), landing)
			assert.NilError(t, err)

			assertCalls(t, fake, received(args))
		})
	}
}

func TestForge_DeleteBranch(t *testing.T) {
	tests := []struct {
		name   string
		branch string
		path   string
	}{
		{name: "plain name", branch: "feature", path: "repos/ada/engine/git/refs/heads/feature"},
		{name: "slash keeps separating segments", branch: "team/fix", path: "repos/ada/engine/git/refs/heads/team/fix"},
		{name: "hash is escaped, not a fragment", branch: "fix#1", path: "repos/ada/engine/git/refs/heads/fix%231"},
		{name: "percent is escaped, not an escape", branch: "100%", path: "repos/ada/engine/git/refs/heads/100%25"},
		{name: "question mark is escaped, not a query", branch: "fix?1", path: "repos/ada/engine/git/refs/heads/fix%3F1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"api", "--hostname", engineHost, "-X", "DELETE", tt.path}
			fake := ghtest.Setup(t, ghtest.Says("", args...))

			err := newForge().DeleteBranch(t.Context(), engine(), tt.branch)
			assert.NilError(t, err)

			assertCalls(t, fake, received(args))
		})
	}
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
