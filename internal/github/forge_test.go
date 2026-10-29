package github_test

import (
	"context"
	"errors"
	"path"
	"slices"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/logtest"
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
	jobID      = "42"
	runLink    = "https://git.example.com/ada/engine/actions/runs/" + runID + "/job/" + jobID

	pullNumber    = 7
	featureBranch = "feature"
	// featureTitle names no branch, so an argv that put a branch where the title goes would differ.
	featureTitle = "feat: open the change"
	trunk        = "main"

	ciFile      = ".github/workflows/ci.yml"
	releaseFile = ".github/workflows/release.yml"
	// dependabotPath is where GitHub lists its own Dependabot workflow, which has no file.
	dependabotPath = "dynamic/dependabot/dependabot-updates"
)

// readCase is one `gh` read of an operation, with the calls that succeed before it.
type readCase struct {
	run    func(ctx context.Context, f *github.Forge) error
	name   string
	args   []string
	before []ghtest.Call
}

func engine() forge.Repo {
	return forge.Repo{Host: engineHost, Owner: "ada", Name: "engine"}
}

func newForge() *github.Forge {
	return &github.Forge{}
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

func featureChange() forge.NewChange {
	return forge.NewChange{Title: featureTitle, Base: trunk, Head: featureBranch}
}

func versionArgs() []string { return ghtest.VersionArgs() }

// versionAnswer is what `gh --version` prints when line is its first line.
func versionAnswer(line string) string {
	return line + "\nhttps://github.com/cli/cli/releases/tag/v2.0.0\n"
}

func loginArgs() []string { return ghtest.LoginArgs(engineHost) }

func repoViewArgs() []string { return ghtest.RepoViewArgs(engineSlug) }

func listArgs() []string { return ghtest.ListArgs(engineSlug, featureBranch) }

func createArgs() []string {
	return ghtest.CreateArgs(engineSlug, featureTitle, trunk, featureBranch)
}

func headArgs() []string { return ghtest.HeadArgs(engineSlug, pullNumber) }

func rulesArgs() []string { return ghtest.RulesArgs(engineHost, "ada/engine", trunk) }

func checksArgs() []string { return ghtest.ChecksArgs(engineSlug, pullNumber) }

func runsArgs() []string { return ghtest.RunsArgs(engineSlug, headSHA) }

func messageArgs() []string { return ghtest.MessageArgs(engineHost, "ada", "engine", pullNumber) }

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

func workflowFilesArgsAt(ref string) []string {
	return []string{"api", "--hostname", engineHost, "repos/ada/engine/contents/.github/workflows?ref=" + ref}
}

// workflowJSON is one entry of the workflow list's answer: the workflow at file, in state.
func workflowJSON(file, state string) string {
	return `{"id":1,"name":"` + path.Base(file) + `","path":"` + file + `","state":"` + state + `"}`
}

// workflowsPage is one page of the workflow list's answer, holding workflows.
func workflowsPage(workflows ...string) string {
	return `{"total_count":` + strconv.Itoa(len(workflows)) + `,"workflows":` + ghtest.JSONArray(workflows...) + `}`
}

// fileJSON is one entry of the directory read's answer: the file at file.
func fileJSON(file string) string {
	return `{"name":"` + path.Base(file) + `","path":"` + file + `","type":"file"}`
}

func activeCIList() string { return workflowsPage(workflowJSON(ciFile, "active")) }

func ciOnTrunkJSON() string { return ghtest.JSONArray(fileJSON(ciFile)) }

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

// draftJSON is `gh pr view`'s answer for a draft pull request at the head commit, with no review asked.
func draftJSON(mergeable, status string) string {
	return ghtest.HeadDraftJSON(headSHA, mergeable, status)
}

func headOf(state forge.MergeState) forge.Head {
	return forge.Head{SHA: headSHA, MergeState: state}
}

// runJSON is one entry of the run list's answer: a run of the head commit in status.
func runJSON(status string) string {
	return ghtest.RunJSON(status, "Add the forge")
}

func headQuery(required ...string) forge.HeadQuery {
	return forge.HeadQuery{Number: pullNumber, Required: required}
}

func verdictQuery(required ...string) forge.VerdictQuery {
	return forge.VerdictQuery{SHA: headSHA, Number: pullNumber, Required: required}
}

// operationsByName is every forge operation, each reduced to its error, keyed by its method name.
func operationsByName() map[string]func(context.Context, *github.Forge) error {
	return map[string]func(context.Context, *github.Forge) error{
		"Identity": func(ctx context.Context, f *github.Forge) error {
			_, err := f.Identity(ctx, engine())
			return err
		},
		"FindChanges": func(ctx context.Context, f *github.Forge) error {
			_, err := f.FindChanges(ctx, engine(), featureBranch)
			return err
		},
		"OpenChange": func(ctx context.Context, f *github.Forge) error {
			_, err := f.OpenChange(ctx, engine(), featureChange())
			return err
		},
		"Head": func(ctx context.Context, f *github.Forge) error {
			_, err := f.Head(ctx, engine(), headQuery())
			return err
		},
		"TrunkProtection": func(ctx context.Context, f *github.Forge) error {
			_, err := f.TrunkProtection(ctx, engine(), trunk)
			return err
		},
		"Verdict": func(ctx context.Context, f *github.Forge) error {
			_, err := f.Verdict(ctx, engine(), verdictQuery())
			return err
		},
		"HasTrunkCI": func(ctx context.Context, f *github.Forge) error {
			_, err := f.HasTrunkCI(ctx, engine(), trunk)
			return err
		},
		"Message": func(ctx context.Context, f *github.Forge) error {
			_, err := f.Message(ctx, engine(), pullNumber)
			return err
		},
		"Land": func(ctx context.Context, f *github.Forge) error {
			return f.Land(ctx, engine(), featureLanding())
		},
		"DeleteBranch": func(ctx context.Context, f *github.Forge) error {
			return f.DeleteBranch(ctx, engine(), featureBranch)
		},
	}
}

// readCases is every `gh` read an operation makes, with the calls that must succeed before it.
func readCases() []readCase {
	byName := operationsByName()

	identity, find, head, protection := byName["Identity"], byName["FindChanges"], byName["Head"], byName["TrunkProtection"]
	hasCI, message := byName["HasTrunkCI"], byName["Message"]
	blocked := func(ctx context.Context, f *github.Forge) error {
		_, err := f.Head(ctx, engine(), headQuery("build", "test"))
		return err
	}

	return []readCase{
		{name: "repo view", run: identity, args: repoViewArgs(), before: []ghtest.Call{ghtest.Says("", loginArgs()...)}},
		{name: "pr list", run: find, args: listArgs()},
		{name: "pr view", run: head, args: headArgs()},
		{
			name: "pr checks behind a blocked head", run: blocked, args: checksArgs(),
			before: []ghtest.Call{ghtest.Says(headJSON("BLOCKED", ""), headArgs()...)},
		},
		{name: "api rules", run: protection, args: rulesArgs()},
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

func TestForge_WhenCallExitsNonZero_ReturnsExitErrorNamingCall(t *testing.T) {
	// Each read left out returns its exit error through a line another test already reaches:
	// runDecoded's through "pr list", Identity's through the malformed "repo view",
	// and TrunkProtection's and HasTrunkCI's through their own tests of a refused read. Verdict's
	// reads have their own test in verdict_test.go.
	reads := casesWithout(readCases(), "repo view", "api rules", "api workflows", "api workflow files")
	for _, tt := range reads {
		t.Run(tt.name, func(t *testing.T) {
			assertExitErrorNamesCall(t, tt)
		})
	}
}

// assertExitErrorNamesCall runs the operation of c with c's own call exiting non-zero, and checks
// that the operation returns an exit error naming that call.
func assertExitErrorNamesCall(t *testing.T, c readCase) {
	t.Helper()
	ghtest.Setup(t, append(c.before, ghtest.Exits(ghtest.ServerError(), c.args...))...)

	err := c.run(t.Context(), newForge())

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "error = %v, want *child.ExitError", err)
	assert.Check(t, ghtest.Matches(received(c.args)[0], exitErr.Argv), "Argv = %q, want %q", exitErr.Argv, c.args)
}

// casesNamed is the cases of all whose name is among names, in the order of all.
func casesNamed(all []readCase, names ...string) []readCase {
	return slices.DeleteFunc(all, func(c readCase) bool { return !slices.Contains(names, c.name) })
}

// casesWithout is the cases of all whose name is not among names, in the order of all.
func casesWithout(all []readCase, names ...string) []readCase {
	return slices.DeleteFunc(all, func(c readCase) bool { return slices.Contains(names, c.name) })
}

func TestForge_WhenReadPrintsMalformedJSON_ReturnsDecodeErrorNamingCall(t *testing.T) {
	for _, tt := range casesNamed(readCases(), "repo view", "api rules") {
		t.Run(tt.name, func(t *testing.T) {
			assertDecodeErrorNamesCall(t, tt)
		})
	}
}

// assertDecodeErrorNamesCall runs the operation of c with c's own call printing malformed JSON,
// and checks that the operation returns a decode error naming that call.
func assertDecodeErrorNamesCall(t *testing.T, c readCase) {
	t.Helper()
	ghtest.Setup(t, append(c.before, ghtest.Says(`[{"number":`, c.args...))...)

	err := c.run(t.Context(), newForge())

	decodeErr, ok := errors.AsType[*github.DecodeError](err)
	assert.Assert(t, ok, "error = %v, want *github.DecodeError", err)
	assert.Check(t, ghtest.Matches(received(c.args)[0], decodeErr.Argv), "Argv = %q, want %q", decodeErr.Argv, c.args)
}

// loggedArgv is the argv of every call sink received, each led by `gh`, in order.
func loggedArgv(sink *logtest.Sink) [][]string {
	var logged [][]string
	for _, record := range sink.Records() {
		args, _ := record.Map()[child.KeyArgs].([]string)
		logged = append(logged, args)
	}

	return received(logged...)
}

// loggedCases is one `gh` call of the forge: every call goes through the same runner, so one case
// shows that the forge hands its logger to it.
func loggedCases() []readCase {
	return []readCase{
		{name: "pr merge", run: operationsByName()["Land"], args: ghtest.LandArgs(engineSlug, featureLanding())},
	}
}

func TestForge_WhenLoggerSet_LogsTheCall(t *testing.T) {
	for _, tt := range loggedCases() {
		t.Run(tt.name, func(t *testing.T) {
			assertLogsCall(t, tt)
		})
	}
}

// assertLogsCall runs the operation of c with a logger and checks that every call the fake
// received was logged, c's own among them.
func assertLogsCall(t *testing.T, c readCase) {
	t.Helper()
	fake := ghtest.Setup(t, append(c.before, ghtest.Says("", c.args...))...)
	logger, sink := logtest.New()

	_ = c.run(t.Context(), &github.Forge{Logger: logger})

	assert.Check(t, is.DeepEqual(loggedArgv(sink), fake.Calls()))
	want := received(c.args)[0]
	assert.Check(t, slices.ContainsFunc(fake.Calls(), func(argv []string) bool { return ghtest.Matches(want, argv) }),
		"calls = %q, want one matching %q", fake.Calls(), c.args)
}

func TestForge_Identity_WhenNotLoggedIn_ReturnsNotLoggedInError(t *testing.T) {
	ghtest.Setup(t, ghtest.Exits(ghtest.LoggedOut(), loginArgs()...))

	_, err := newForge().Identity(t.Context(), engine())

	_, ok := errors.AsType[*forge.NotLoggedInError](err)
	assert.Assert(t, ok, "Identity error = %v, want *forge.NotLoggedInError", err)
}

func TestForge_Identity_WhenGhIsOlderThanTheMinimum_ReturnsToolTooOldErrorBeforeTheLogin(t *testing.T) {
	const line = "gh version 1.0.0"
	ghtest.Setup(t, ghtest.Says(versionAnswer(line), versionArgs()...), ghtest.Exits(ghtest.LoggedOut(), loginArgs()...))

	_, err := newForge().Identity(t.Context(), engine())

	_, ok := errors.AsType[*child.ToolTooOldError](err)
	assert.Assert(t, ok, "Identity error = %v, want *child.ToolTooOldError", err)
}

func TestForge_Identity_WhenVersionReadExitsNonZero_ReturnsExitErrorNamingCall(t *testing.T) {
	const unknownFlag = "unknown flag\n"
	ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stderr: unknownFlag, Code: 1}, versionArgs()...))

	_, err := newForge().Identity(t.Context(), engine())

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Identity error = %v, want *child.ExitError", err)
	assert.Check(t, is.DeepEqual(exitErr.Argv, received(versionArgs())[0]))
}

func TestForge_Identity(t *testing.T) {
	tests := []struct {
		name string
		view string
		want forge.Repo
	}{
		{name: "write may push", view: ghtest.RepoViewJSON(false, "WRITE"), want: forge.Repo{CanPush: true, SquashAllowed: true}},
		{name: "fork that may not push", view: ghtest.RepoViewJSON(true, "READ"), want: forge.Repo{Fork: true, SquashAllowed: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Says("Logged in\n", loginArgs()...), ghtest.Says(tt.view, repoViewArgs()...))
			want := engine()
			want.Fork, want.CanPush, want.SquashAllowed = tt.want.Fork, tt.want.CanPush, tt.want.SquashAllowed

			got, err := newForge().Identity(t.Context(), engine())
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, want))
		})
	}
}

func TestForge_FindChanges(t *testing.T) {
	open := ghtest.Change{Number: 7, URL: pullURL, Title: "feature", Base: "main", SHA: headSHA, Draft: true, State: "OPEN"}
	closed := ghtest.Change{
		Number: 5, URL: "https://git.example.com/ada/engine/pull/5", Title: "feat: try the forge",
		Base: "main", SHA: "5e6f7a8", State: "CLOSED",
	}
	merged := ghtest.Change{
		Number: 3, URL: "https://git.example.com/ada/engine/pull/3", Title: "feat: add the forge",
		Base: "release", SHA: "0a1b2c3", State: "MERGED",
	}
	foreign := ghtest.Change{
		Number: 9, URL: "https://git.example.com/ada/engine/pull/9", Title: "feature",
		Base: "main", SHA: "0a1b2c3", State: "OPEN", CrossRepository: true,
	}
	openChange := forge.Change{Number: 7, URL: pullURL, Title: "feature", Base: "main", HeadSHA: headSHA, Draft: true, State: forge.ChangeOpen}
	tests := []struct {
		name   string
		answer string
		want   []forge.Change
	}{
		{
			name:   "every state is listed, each marked",
			answer: ghtest.JSONArray(ghtest.ChangeJSON(open), ghtest.ChangeJSON(closed), ghtest.ChangeJSON(merged)),
			want: []forge.Change{
				openChange,
				{
					Number: 5, URL: "https://git.example.com/ada/engine/pull/5", Title: "feat: try the forge",
					Base: "main", HeadSHA: "5e6f7a8", State: forge.ChangeClosed,
				},
				{
					Number: 3, URL: "https://git.example.com/ada/engine/pull/3", Title: "feat: add the forge",
					Base: "release", HeadSHA: "0a1b2c3", State: forge.ChangeMerged,
				},
			},
		},
		{
			name:   "pull request from another repository is left out",
			answer: ghtest.JSONArray(ghtest.ChangeJSON(foreign), ghtest.ChangeJSON(open)),
			want:   []forge.Change{openChange},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Says(tt.answer, listArgs()...))

			got, err := newForge().FindChanges(t.Context(), engine(), featureBranch)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, tt.want))
		})
	}
}

func TestForge_FindChanges_WhenStateIsUnknown_ReturnsDecodeError(t *testing.T) {
	draft := ghtest.Change{Number: 7, URL: pullURL, Title: "feature", Base: "main", SHA: headSHA, State: "DRAFT"}
	ghtest.Setup(t, ghtest.Says(ghtest.JSONArray(ghtest.ChangeJSON(draft)), listArgs()...))

	_, err := newForge().FindChanges(t.Context(), engine(), featureBranch)

	decodeErr, ok := errors.AsType[*github.DecodeError](err)
	assert.Assert(t, ok, "FindChanges error = %v, want *github.DecodeError", err)
	assert.Check(t, ghtest.Matches(received(listArgs())[0], decodeErr.Argv), "Argv = %q, want %q", decodeErr.Argv, listArgs())
}

func TestForge_OpenChange_WhenCreated_ReturnsNumberAndURL(t *testing.T) {
	ghtest.Setup(t, ghtest.Says(pullURL+"\n", createArgs()...))

	got, err := newForge().OpenChange(t.Context(), engine(), featureChange())
	assert.NilError(t, err)

	want := forge.ChangeRef{Number: 7, URL: pullURL}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestForge_OpenChange_WhenOutputNamesNoPullRequest_ReturnsDecodeErrorNamingCall(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
	}{
		{name: "no pull request URL", stdout: "Creating pull request\n"},
		{name: "no pull request number", stdout: "https://git.example.com/ada/engine/pull/latest\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Says(tt.stdout, createArgs()...))

			_, err := newForge().OpenChange(t.Context(), engine(), featureChange())

			decodeErr, ok := errors.AsType[*github.DecodeError](err)
			assert.Assert(t, ok, "OpenChange error = %v, want *github.DecodeError", err)
			assert.Check(t, is.DeepEqual(decodeErr.Argv, received(createArgs())[0]))
		})
	}
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

func TestForge_OpenChange_WhenGhFailsForAnotherReason_ReturnsExitError(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
	}{
		{name: "something else already exists", stderr: "error: remote ref already exists elsewhere\n"},
		{name: "a pull request exists for another reason", stderr: "pull request create failed: a pull request exists but is locked\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stderr: tt.stderr, Code: 1}, createArgs()...))

			_, err := newForge().OpenChange(t.Context(), engine(), featureChange())

			assert.Assert(t, !errors.Is(err, forge.ErrChangeExists), "OpenChange error = %v, want an error other than forge.ErrChangeExists", err)
			_, ok := errors.AsType[*child.ExitError](err)
			assert.Check(t, ok, "OpenChange error = %v, want *child.ExitError", err)
		})
	}
}

func TestForge_Head_WhenStateNeedsNoCheck_ReportsMergeStateFromThePullRequestAlone(t *testing.T) {
	checked := []string{"build", "test"}
	tests := []struct {
		name     string
		view     string
		want     forge.Head
		required []string
	}{
		{name: "conflicting wins over a clean status", view: pullJSON("CONFLICTING", "CLEAN", ""), want: headOf(forge.MergeConflicting), required: checked},
		{name: "conflicting wins over the draft flag", view: draftJSON("CONFLICTING", "DIRTY"), want: headOf(forge.MergeConflicting), required: checked},
		{name: "mergeable not worked out yet", view: pullJSON("UNKNOWN", "BLOCKED", ""), want: headOf(forge.MergeUnknown), required: checked},
		{name: "mergeable not worked out yet wins over the draft flag", view: draftJSON("UNKNOWN", "UNKNOWN"), want: headOf(forge.MergeUnknown), required: checked},
		{name: "clean", view: headJSON("CLEAN", "APPROVED"), want: headOf(forge.MergeClean), required: checked},
		{name: "behind the base", view: headJSON("BEHIND", ""), want: headOf(forge.MergeBehind), required: checked},
		{name: "dirty", view: headJSON("DIRTY", ""), want: headOf(forge.MergeConflicting), required: checked},
		{name: "draft status", view: headJSON("DRAFT", ""), want: headOf(forge.MergeDraft), required: checked},
		{name: "draft flag on a blocked status", view: draftJSON("MERGEABLE", "BLOCKED"), want: headOf(forge.MergeDraft), required: checked},
		{name: "blocked by a review", view: headJSON("BLOCKED", "REVIEW_REQUIRED"), want: headOf(forge.MergeBlockedByReview), required: checked},
		{name: "blocked with no required name", view: headJSON("BLOCKED", ""), want: headOf(forge.MergeBlockedOther)},
		{name: "status not worked out yet", view: headJSON("UNKNOWN", ""), want: headOf(forge.MergeUnknown), required: checked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Says(tt.view, headArgs()...))

			got, err := newForge().Head(t.Context(), engine(), headQuery(tt.required...))
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, tt.want))
		})
	}
}

func TestForge_Head_WhenBlockedAndTrunkRequiresChecks_SaysWhetherARequiredCheckIsMissingOrRunning(t *testing.T) {
	passing := ghtest.JSONArray(ghtest.CheckJSON("test", "pass", runLink))
	completed := ghtest.JSONArray(runJSON("completed"))
	tests := []struct {
		name   string
		runs   string
		want   forge.MergeState
		checks ghtest.Answer
	}{
		{name: "required name absent, runs done", checks: ghtest.Answer{Stdout: passing}, runs: completed, want: forge.MergeBlockedByMissingCheck},
		{name: "required name absent, run queued", checks: ghtest.Answer{Stdout: passing}, runs: ghtest.JSONArray(runJSON("queued")), want: forge.MergeBlockedByRunningCheck},
		{
			name:   "required name absent, other required check still running",
			checks: ghtest.Answer{Stdout: ghtest.JSONArray(ghtest.CheckJSON("test", "pending", runLink)), Code: 8},
			runs:   completed,
			want:   forge.MergeBlockedByRunningCheck,
		},
		{
			name:   "every required name has a check",
			checks: ghtest.Answer{Stdout: ghtest.JSONArray(ghtest.CheckJSON("build", "pass", runLink), ghtest.CheckJSON("test", "fail", runLink)), Code: 1},
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

			want := headOf(tt.want)
			assert.Check(t, is.DeepEqual(got, want))
		})
	}
}

func TestForge_TrunkProtection(t *testing.T) {
	statusChecks := `[
		{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"build"},{"context":"test"}]}},
		{"type":"deletion"},
		{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"test"},{"context":"lint"}]}}
	]`
	squash := forge.Protection{Rules: forge.RulesProtect, SquashAllowed: true}
	// forbiddenBy is the protection whose squash merges the rulesets with the ids ids forbid.
	forbiddenBy := func(ids ...int) forge.Protection {
		return forge.Protection{Rules: forge.RulesProtect, SquashForbiddenBy: ids}
	}
	// rules is the rules read's answer holding the rules given, each a JSON object.
	rules := func(given ...string) ghtest.Answer { return ghtest.Answer{Stdout: ghtest.JSONArray(given...)} }
	tests := []struct {
		name   string
		want   forge.Protection
		answer ghtest.Answer
	}{
		{
			name:   "contexts of every status-check rule, once each",
			answer: ghtest.Answer{Stdout: statusChecks},
			want:   forge.Protection{Rules: forge.RulesProtect, RequiredChecks: []string{"build", "test", "lint"}, SquashAllowed: true},
		},
		{
			name: "rules spread over two pages",
			answer: ghtest.Answer{Stdout: rules(`{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"build"}]}}`).Stdout +
				rules(pullRequestRule(51, `["rebase"]`)).Stdout},
			want: forge.Protection{Rules: forge.RulesProtect, RequiredChecks: []string{"build"}, SquashForbiddenBy: []int{51}},
		},
		{
			name:   "free private plan answers 403",
			answer: ghtest.Answer{Stdout: `{"message":"Upgrade to GitHub Pro"}`, Stderr: "gh: Upgrade to GitHub Pro (HTTP 403)\n", Code: 1},
			want:   forge.Protection{Rules: forge.RulesUnavailable, SquashAllowed: true},
		},
		{name: "no rule applies", answer: rules(), want: forge.Protection{Rules: forge.RulesNone, SquashAllowed: true}},
		{name: "pull request rule listing squash", answer: rules(pullRequestRule(42, `["merge","squash","rebase"]`)), want: squash},
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
			assert.Check(t, is.DeepEqual(exitErr.Argv, received(rulesArgs())[0]))
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
		{name: "slash keeps separating segments", branch: "release/v1", path: "repos/ada/engine/rules/branches/release/v1"},
		{name: "hash is escaped, not a fragment", branch: "release#1", path: "repos/ada/engine/rules/branches/release%231"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"api", "--hostname", engineHost, "--paginate", tt.path}
			fake := ghtest.Setup(t, ghtest.Says(rules, args...))

			_, err := newForge().TrunkProtection(t.Context(), engine(), tt.branch)
			assert.NilError(t, err)

			assertCalls(t, fake, received(args))
		})
	}
}

func TestForge_HasTrunkCI(t *testing.T) {
	ci := workflowJSON(ciFile, "active")
	dependabot := workflowJSON(dependabotPath, "active")
	autoSubmission := workflowJSON("dynamic/dependency-graph/auto-submission", "active")
	ciOnTrunk := ghtest.Answer{Stdout: ciOnTrunkJSON()}
	releaseOnTrunk := ghtest.Answer{Stdout: ghtest.JSONArray(fileJSON(releaseFile))}
	// filesFail is a trunk read that fails: a row scripting it answers without reading the trunk.
	filesFail := ghtest.ServerError()
	tests := []struct {
		name      string
		workflows string
		files     ghtest.Answer
		want      bool
	}{
		{name: "active workflow whose file is on the trunk", workflows: workflowsPage(dependabot, ci), files: ciOnTrunk, want: true},
		{name: "active workflow on the second page", workflows: workflowsPage(dependabot) + workflowsPage(ci), files: ciOnTrunk, want: true},
		{name: "workflow disabled by hand", workflows: workflowsPage(workflowJSON(ciFile, "disabled_manually")), files: ciOnTrunk},
		{name: "only dynamic workflows", workflows: workflowsPage(dependabot, autoSubmission), files: filesFail},
		{name: "active workflow whose file is not on the trunk", workflows: workflowsPage(ci), files: releaseOnTrunk},
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

	_, err := newForge().HasTrunkCI(t.Context(), engine(), trunk)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "HasTrunkCI error = %v, want *child.ExitError", err)
	assert.Check(t, is.DeepEqual(exitErr.Argv, received(workflowFilesArgs())[0]))
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
	escaped := workflowFilesArgsAt("release%2F1.0%2B2")
	fake := ghtest.Setup(t, ghtest.Says(activeCIList(), workflowsArgs()...), ghtest.Says(ciOnTrunkJSON(), escaped...))

	_, err := newForge().HasTrunkCI(t.Context(), engine(), release)
	assert.NilError(t, err)

	assertCalls(t, fake, received(workflowsArgs(), escaped))
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
		{name: "last paragraph mixes prose with a trailer", mergeBody: "Lands the change.\n\nThanks to Grace.\n" + liveSuite},
		{name: "token holding a space", mergeBody: "Lands the change.\n\nThanks to: Grace Hopper"},
		{name: "token with no value", mergeBody: "Lands the change.\n\nReviewed-by: "},
		{name: "no space after the colon", mergeBody: "Lands the change.\n\nReviewed-by:Grace Hopper"},
		{name: "body text ending in a newline", mergeBody: liveSuite + "\n", want: []string{liveSuite}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Says(ghtest.MessageJSON(title, pullBody, tt.mergeBody), messageArgs()...))

			got, err := newForge().Message(t.Context(), engine(), pullNumber)
			assert.NilError(t, err)

			want := forge.Message{Title: title, Body: pullBody, Trailers: tt.want}
			assert.Check(t, is.DeepEqual(got, want, cmpopts.EquateEmpty()))
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
			assert.Check(t, ghtest.Matches(received(messageArgs())[0], decodeErr.Argv), "Argv = %q, want %q", decodeErr.Argv, messageArgs())
		})
	}
}

func TestForge_DeleteBranch_WhenNameHoldsAHash_EscapesIt(t *testing.T) {
	args := ghtest.DeleteBranchArgs(engineHost, "ada/engine", "fix%231")
	fake := ghtest.Setup(t, ghtest.Says("", args...))

	err := newForge().DeleteBranch(t.Context(), engine(), "fix#1")
	assert.NilError(t, err)

	assertCalls(t, fake, received(args))
}
