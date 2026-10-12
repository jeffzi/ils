package github_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
)

// No test that scripts `gh` is parallel: the fake `gh` is found through `GH_PATH`,
// which `go-gh` reads from the environment of the whole process.

const (
	engineHost = "git.example.com"
	engineSlug = engineHost + "/ada/engine"
	headSHA    = "4f2a9c1d7e3b5a6088c1d2e3f4a5b6c7d8e9f001"
	pullURL    = "https://git.example.com/ada/engine/pull/7"
	runLink    = "https://git.example.com/ada/engine/actions/runs/9001/job/42"
	statusLink = "https://ci.example.com/builds/15"
	serverDown = "gh: Server Error (HTTP 500)\n"

	rulesForBuildAndTest = `[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"build"},{"context":"test"}]}}]`
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

// newForge is the GitHub forge over a plain runner.
func newForge() *github.Forge {
	return github.NewForge(github.Runner{})
}

// ok scripts an exit 0 that prints stdout.
func ok(stdout string, args ...string) ghtest.Call {
	return answered(ghtest.Answer{Stdout: stdout}, args...)
}

// answered scripts one answer to an argv.
func answered(answer ghtest.Answer, args ...string) ghtest.Call {
	return ghtest.Call{Args: args, Answers: []ghtest.Answer{answer}}
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
	return forge.NewChange{Title: "feature", Base: "main", Head: "feature"}
}

func loginArgs() []string {
	return []string{"auth", "status", "--hostname", engineHost}
}

func repoViewArgs() []string {
	return []string{"repo", "view", engineSlug, "--json", "isFork,viewerPermission"}
}

func listArgs(state string) []string {
	return []string{
		"pr", "list", "--repo", engineSlug, "--head", "feature", "--state", state,
		"--json", "number,url,title,baseRefName,headRefOid,isDraft",
	}
}

func createArgs() []string {
	return []string{
		"pr", "create", "--repo", engineSlug, "--title", "feature", "--body", "",
		"--base", "main", "--head", "feature",
	}
}

func headArgs() []string {
	return []string{
		"pr", "view", "--repo", engineSlug, "7",
		"--json", "headRefOid,mergeable,mergeStateStatus,isDraft,reviewDecision",
	}
}

func rulesArgs() []string {
	return []string{"api", "--hostname", engineHost, "repos/ada/engine/rules/branches/main"}
}

func checksArgs() []string {
	return []string{"pr", "checks", "--repo", engineSlug, "7", "--json", "name,bucket,workflow,link,state"}
}

func runsArgs() []string {
	return []string{
		"run", "list", "--repo", engineSlug, "--commit", headSHA,
		"--json", "databaseId,status,event,name,workflowName",
	}
}

func logArgs() []string {
	return []string{"run", "view", "--repo", engineSlug, "9001", "--log-failed"}
}

// headJSON is `gh pr view`'s answer for a mergeable pull request in the given state.
func headJSON(status, review string) string {
	return pullJSON("MERGEABLE", status, review)
}

// pullJSON is `gh pr view`'s answer for a pull request that is not a draft.
func pullJSON(mergeable, status, review string) string {
	return `{"headRefOid":"` + headSHA + `","mergeable":"` + mergeable + `","mergeStateStatus":"` + status +
		`","isDraft":false,"reviewDecision":"` + review + `"}`
}

// headOf is the head commit in the given merge state, with GitHub's own status behind it.
func headOf(state forge.MergeState, raw string) forge.Head {
	return forge.Head{SHA: headSHA, MergeState: state, RawMergeState: raw}
}

// checkJSON is one entry of `gh pr checks`'s answer.
func checkJSON(name, bucket, link string) string {
	return `{"name":"` + name + `","bucket":"` + bucket + `","workflow":"CI","link":"` + link + `","state":"X"}`
}

// runJSON is one entry of `gh run list`'s answer.
func runJSON(status string) string {
	return `{"databaseId":9001,"status":"` + status + `","event":"pull_request","name":"Add the forge","workflowName":"CI"}`
}

func headQuery(required ...string) forge.HeadQuery {
	return forge.HeadQuery{Number: 7, Required: required}
}

func verdictQuery(required ...string) forge.VerdictQuery {
	return forge.VerdictQuery{SHA: headSHA, Number: 7, Required: required}
}

func operations() []operation {
	return []operation{
		{name: "Identity", run: func(ctx context.Context, f *github.Forge) error {
			_, err := f.Identity(ctx, engine())
			return err
		}},
		{name: "FindChanges", run: func(ctx context.Context, f *github.Forge) error {
			_, err := f.FindChanges(ctx, engine(), "feature")
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
			_, err := f.TrunkProtection(ctx, engine(), "main")
			return err
		}},
		{name: "Verdict", run: func(ctx context.Context, f *github.Forge) error {
			_, err := f.Verdict(ctx, engine(), verdictQuery())
			return err
		}},
	}
}

func readCases() []readCase {
	ops := operations()
	identity, find, head, protection, verdict := ops[0].run, ops[1].run, ops[3].run, ops[4].run, ops[5].run
	blocked := func(ctx context.Context, f *github.Forge) error {
		_, err := f.Head(ctx, engine(), headQuery("build", "test"))
		return err
	}

	return []readCase{
		{name: "repo view", run: identity, args: repoViewArgs(), before: []ghtest.Call{ok("", loginArgs()...)}},
		{name: "pr list of open ones", run: find, args: listArgs("open")},
		{name: "pr list of merged ones", run: find, args: listArgs("merged"), before: []ghtest.Call{ok("[]", listArgs("open")...)}},
		{name: "pr view", run: head, args: headArgs()},
		{
			name: "pr checks behind a blocked head", run: blocked, args: checksArgs(),
			before: []ghtest.Call{ok(headJSON("BLOCKED", ""), headArgs()...)},
		},
		{
			name: "run list behind a blocked head", run: blocked, args: runsArgs(),
			before: []ghtest.Call{ok(headJSON("BLOCKED", ""), headArgs()...), ok("[]", checksArgs()...)},
		},
		{name: "api rules", run: protection, args: rulesArgs()},
		{name: "pr checks", run: verdict, args: checksArgs()},
		{name: "run list", run: verdict, args: runsArgs(), before: []ghtest.Call{ok("[]", checksArgs()...)}},
	}
}

func TestForge_WhenGhMissing_ReturnsToolMissingError(t *testing.T) {
	for _, tt := range operations() {
		t.Run(tt.name, func(t *testing.T) {
			// Sets `GH_PATH` and `PATH` itself, so that no `gh` can be found.
			t.Setenv("GH_PATH", "")
			t.Setenv("PATH", t.TempDir())

			err := tt.run(t.Context(), newForge())

			missing, ok := errors.AsType[*forge.ToolMissingError](err)
			assert.Assert(t, ok, "%s error = %v, want *forge.ToolMissingError", tt.name, err)
			assert.Check(t, is.DeepEqual(missing, missingTool()))
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
			failing := answered(ghtest.Answer{Stderr: serverDown, Code: 1}, tt.args...)
			ghtest.Setup(t, append(tt.before, failing)...)

			err := tt.run(t.Context(), newForge())

			exitErr, ok := errors.AsType[*github.ExitError](err)
			assert.Assert(t, ok, "error = %v, want *github.ExitError", err)
			want := github.ExitError{Argv: received(tt.args)[0], Stderr: serverDown, Code: 1}
			assert.Check(t, is.DeepEqual(*exitErr, want))
		})
	}
}

func TestForge_WhenReadPrintsMalformedJSON_ReturnsDecodeErrorNamingCall(t *testing.T) {
	for _, tt := range readCases() {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, append(tt.before, ok(`[{"number":`, tt.args...))...)

			err := tt.run(t.Context(), newForge())

			decodeErr, ok := errors.AsType[*github.DecodeError](err)
			assert.Assert(t, ok, "error = %v, want *github.DecodeError", err)
			assert.Check(t, is.DeepEqual(decodeErr.Argv, received(tt.args)[0]))
		})
	}
}

func TestForge_Identity_WhenNotLoggedIn_ReturnsNotLoggedInError(t *testing.T) {
	fake := ghtest.Setup(t, answered(ghtest.Answer{Stderr: "You are not logged into any GitHub hosts.\n", Code: 1}, loginArgs()...))

	_, err := newForge().Identity(t.Context(), engine())

	notLoggedIn, ok := errors.AsType[*forge.NotLoggedInError](err)
	assert.Assert(t, ok, "Identity error = %v, want *forge.NotLoggedInError", err)
	assert.Check(t, is.Equal(notLoggedIn.Host, engineHost))
	assertCalls(t, fake, received(loginArgs()))
}

func TestForge_Identity(t *testing.T) {
	tests := []struct {
		name string
		view string
		want forge.Repo
	}{
		{name: "write may push", view: `{"isFork":false,"viewerPermission":"WRITE"}`, want: forge.Repo{CanPush: true}},
		{name: "maintain may push", view: `{"isFork":false,"viewerPermission":"MAINTAIN"}`, want: forge.Repo{CanPush: true}},
		{name: "admin may push", view: `{"isFork":false,"viewerPermission":"ADMIN"}`, want: forge.Repo{CanPush: true}},
		{name: "read may not push", view: `{"isFork":false,"viewerPermission":"READ"}`, want: forge.Repo{}},
		{name: "triage may not push", view: `{"isFork":false,"viewerPermission":"TRIAGE"}`, want: forge.Repo{}},
		{name: "no permission may not push", view: `{"isFork":false,"viewerPermission":""}`, want: forge.Repo{}},
		{name: "fork that may push", view: `{"isFork":true,"viewerPermission":"ADMIN"}`, want: forge.Repo{Fork: true, CanPush: true}},
		{name: "fork that may not push", view: `{"isFork":true,"viewerPermission":"READ"}`, want: forge.Repo{Fork: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := ghtest.Setup(t, ok("Logged in\n", loginArgs()...), ok(tt.view, repoViewArgs()...))
			want := engine()
			want.Fork, want.CanPush = tt.want.Fork, tt.want.CanPush

			got, err := newForge().Identity(t.Context(), engine())
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, want))
			assertCalls(t, fake, received(loginArgs(), repoViewArgs()))
		})
	}
}

func TestForge_Identity_WhenRepoUnreadable_ErrorStartsByNamingRepository(t *testing.T) {
	notFound := ghtest.Answer{Stderr: "GraphQL: Could not resolve to a Repository\n", Code: 1}
	ghtest.Setup(t, ok("Logged in\n", loginArgs()...), answered(notFound, repoViewArgs()...))

	_, err := newForge().Identity(t.Context(), engine())

	assert.Assert(t, err != nil, "Identity error = nil, want an error naming the repository")
	wantPrefix := "read the repository " + engineSlug + ": "
	assert.Check(t, strings.HasPrefix(err.Error(), wantPrefix), "Identity error = %q, want it to start with %q", err.Error(), wantPrefix)
}

func TestForge_FindChanges_ReturnsOpenThenMergedChanges(t *testing.T) {
	open := `[{"number":7,"url":"` + pullURL + `","title":"feature","baseRefName":"main","headRefOid":"` + headSHA + `","isDraft":true}]`
	merged := `[{"number":3,"url":"https://git.example.com/ada/engine/pull/3","title":"feat: add the forge",` +
		`"baseRefName":"release","headRefOid":"0a1b2c3","isDraft":false}]`
	fake := ghtest.Setup(t, ok(open, listArgs("open")...), ok(merged, listArgs("merged")...))

	got, err := newForge().FindChanges(t.Context(), engine(), "feature")
	assert.NilError(t, err)

	want := []forge.Change{
		{Number: 7, URL: pullURL, Title: "feature", Base: "main", HeadSHA: headSHA, Draft: true, State: forge.ChangeOpen},
		{
			Number: 3, URL: "https://git.example.com/ada/engine/pull/3", Title: "feat: add the forge",
			Base: "release", HeadSHA: "0a1b2c3", State: forge.ChangeMerged,
		},
	}
	assert.Check(t, is.DeepEqual(got, want))
	assertCalls(t, fake, received(listArgs("open"), listArgs("merged")))
}

func TestForge_FindChanges_WhenBranchHasNone_ReturnsNoChanges(t *testing.T) {
	ghtest.Setup(t, ok("[]", listArgs("open")...), ok("[]", listArgs("merged")...))

	got, err := newForge().FindChanges(t.Context(), engine(), "feature")
	assert.NilError(t, err)

	assert.Check(t, is.Len(got, 0))
}

func TestForge_OpenChange_WhenCreated_ReturnsNumberAndURL(t *testing.T) {
	fake := ghtest.Setup(t, ok(pullURL+"\n", createArgs()...))

	got, err := newForge().OpenChange(t.Context(), engine(), featureChange())
	assert.NilError(t, err)

	want := forge.ChangeRef{Number: 7, URL: pullURL}
	assert.Check(t, is.DeepEqual(got, want))
	assertCalls(t, fake, received(createArgs()))
}

func TestForge_OpenChange_WhenPullRequestExists_ReturnsErrChangeExists(t *testing.T) {
	exists := ghtest.Answer{
		Stderr: "a pull request for branch \"feature\" into branch \"main\" already exists:\n" + pullURL + "\n",
		Code:   1,
	}
	ghtest.Setup(t, answered(exists, createArgs()...))

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
			ghtest.Setup(t, answered(ghtest.Answer{Stderr: tt.stderr, Code: 1}, createArgs()...))

			_, err := newForge().OpenChange(t.Context(), engine(), featureChange())

			assert.Assert(t, !errors.Is(err, forge.ErrChangeExists), "OpenChange error = %v, want an error other than forge.ErrChangeExists", err)
			exitErr, ok := errors.AsType[*github.ExitError](err)
			assert.Assert(t, ok, "OpenChange error = %v, want *github.ExitError", err)
			assert.Check(t, is.Equal(exitErr.Stderr, tt.stderr))
		})
	}
}

func TestForge_OpenChange_WhenStdoutHoldsNoUsablePullURL_ReturnsError(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{
			name:   "no pull URL",
			stdout: "Creating pull request for feature into main\n",
			want:   `gh pr create: no pull request URL in its output "Creating pull request for feature into main\n"`,
		},
		{
			name:   "pull number is not a number",
			stdout: "https://git.example.com/ada/engine/pull/abc\n",
			want:   `gh pr create: no pull request number in "https://git.example.com/ada/engine/pull/abc": strconv.Atoi: parsing "abc": invalid syntax`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ok(tt.stdout, createArgs()...))

			got, err := newForge().OpenChange(t.Context(), engine(), featureChange())

			assert.Assert(t, err != nil, "OpenChange = %+v with a nil error, want an error", got)
			assert.Check(t, is.Equal(err.Error(), tt.want))
		})
	}
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
			fake := ghtest.Setup(t, ok(tt.view, headArgs()...))

			got, err := newForge().Head(t.Context(), engine(), headQuery(tt.required...))
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, tt.want))
			assertCalls(t, fake, received(headArgs()))
		})
	}
}

func TestForge_Head_WhenBlockedAndTrunkRequiresChecks(t *testing.T) {
	passing := "[" + checkJSON("test", "pass", runLink) + "]"
	optionalPending := "[" + checkJSON("test", "pass", runLink) + "," + checkJSON("lint", "pending", runLink) + "]"
	noChecks := ghtest.Answer{Stderr: "no checks reported on the 'feature' branch\n", Code: 1}
	tests := []struct {
		name   string
		runs   string
		want   forge.MergeState
		checks ghtest.Answer
	}{
		{name: "required name absent, runs done", checks: ghtest.Answer{Stdout: passing}, runs: "[" + runJSON("completed") + "]", want: forge.MergeBlockedByMissingCheck},
		{name: "required name absent, no run", checks: ghtest.Answer{Stdout: passing}, runs: "[]", want: forge.MergeBlockedByMissingCheck},
		{name: "required name absent, run waits on a person", checks: ghtest.Answer{Stdout: passing}, runs: "[" + runJSON("waiting") + "]", want: forge.MergeBlockedByMissingCheck},
		{name: "required name absent, run queued", checks: ghtest.Answer{Stdout: passing}, runs: "[" + runJSON("queued") + "]", want: forge.MergeBlockedOther},
		{name: "required name absent, run in progress", checks: ghtest.Answer{Stdout: passing}, runs: "[" + runJSON("in_progress") + "]", want: forge.MergeBlockedOther},
		{name: "required name absent, run pending", checks: ghtest.Answer{Stdout: passing}, runs: "[" + runJSON("pending") + "]", want: forge.MergeBlockedOther},
		{name: "required name absent, run in a status this version does not know", checks: ghtest.Answer{Stdout: passing}, runs: "[" + runJSON("paused") + "]", want: forge.MergeBlockedOther},
		{name: "required name absent, only an optional check running", checks: ghtest.Answer{Stdout: optionalPending, Code: 8}, runs: "[" + runJSON("completed") + "]", want: forge.MergeBlockedByMissingCheck},
		{
			name:   "every required name absent, only an optional check running",
			checks: ghtest.Answer{Stdout: "[" + checkJSON("lint", "pending", runLink) + "]", Code: 8},
			runs:   "[" + runJSON("completed") + "]",
			want:   forge.MergeBlockedByMissingCheck,
		},
		{name: "no check reported, no run", checks: noChecks, runs: "[]", want: forge.MergeBlockedByMissingCheck},
		{
			name:   "required name absent, other required check still running",
			checks: ghtest.Answer{Stdout: "[" + checkJSON("test", "pending", runLink) + "]", Code: 8},
			runs:   "[" + runJSON("completed") + "]",
			want:   forge.MergeBlockedOther,
		},
		{
			name:   "every required name has a check",
			checks: ghtest.Answer{Stdout: "[" + checkJSON("build", "pass", runLink) + "," + checkJSON("test", "fail", runLink) + "]", Code: 1},
			runs:   "[" + runJSON("completed") + "]",
			want:   forge.MergeBlockedOther,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := ghtest.Setup(t,
				ok(headJSON("BLOCKED", "APPROVED"), headArgs()...),
				answered(tt.checks, checksArgs()...),
				ok(tt.runs, runsArgs()...),
			)

			got, err := newForge().Head(t.Context(), engine(), headQuery("build", "test"))
			assert.NilError(t, err)

			want := forge.Head{SHA: headSHA, MergeState: tt.want, RawMergeState: "BLOCKED"}
			assert.Check(t, is.DeepEqual(got, want))
			assertCalls(t, fake, received(headArgs(), checksArgs(), runsArgs()))
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
			fake := ghtest.Setup(t, answered(tt.answer, rulesArgs()...))

			got, err := newForge().TrunkProtection(t.Context(), engine(), "main")
			assert.NilError(t, err)

			want := forge.Protection{RequiredChecks: tt.want}
			assert.Check(t, is.DeepEqual(got, want, cmpopts.EquateEmpty()))
			assertCalls(t, fake, received(rulesArgs()))
		})
	}
}

func TestForge_Verdict_ReadsEachBucketWhateverGhExitsWith(t *testing.T) {
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
			checks := ghtest.Answer{Stdout: "[" + checkJSON("build", tt.bucket, statusLink) + "]", Stderr: "notice\n", Code: tt.code}
			fake := ghtest.Setup(t, answered(checks, checksArgs()...), ok("[]", runsArgs()...))

			got, err := newForge().Verdict(t.Context(), engine(), verdictQuery())
			assert.NilError(t, err)

			want := []forge.Check{{Name: "build", Bucket: tt.want, Workflow: "CI", Link: statusLink}}
			assert.Check(t, is.DeepEqual(got.Checks, want))
			assertCalls(t, fake, received(checksArgs(), runsArgs()))
		})
	}
}

func TestForge_Verdict_WhenNoCheckReported_ReturnsZeroChecks(t *testing.T) {
	none := ghtest.Answer{Stderr: "no checks reported on the 'feature' branch\n", Code: 1}
	ghtest.Setup(t, answered(none, checksArgs()...), ok("[]", runsArgs()...))

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery())
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got, forge.Verdict{}, cmpopts.EquateEmpty()))
}

func TestForge_Verdict_ReadsTheStatusOfEachRunOnTheCommit(t *testing.T) {
	runs := `[
		{"databaseId":1,"status":"queued","event":"pull_request","name":"Add the forge","workflowName":"CI"},
		{"databaseId":2,"status":"in_progress","event":"pull_request","name":"Add the forge","workflowName":"Lint"},
		{"databaseId":3,"status":"waiting","event":"pull_request","name":"Add the forge","workflowName":"Preview"},
		{"databaseId":4,"status":"requested","event":"pull_request","name":"Add the forge","workflowName":"Docs"},
		{"databaseId":5,"status":"pending","event":"pull_request","name":"Add the forge","workflowName":"Audit"},
		{"databaseId":6,"status":"completed","event":"push","name":"Add the forge","workflowName":"Build"},
		{"databaseId":7,"status":"completed","event":"dynamic","name":"Scan","workflowName":""}
	]`
	ghtest.Setup(t, ok("[]", checksArgs()...), ok(runs, runsArgs()...))

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
	}
	assert.Check(t, is.DeepEqual(got.Runs, want))
}

func TestForge_Verdict_WhenRunStatusIsNotKnown_CarriesTheRunAsInProgress(t *testing.T) {
	ghtest.Setup(t, ok("[]", checksArgs()...), ok("["+runJSON("paused")+"]", runsArgs()...))

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery())
	assert.NilError(t, err)

	want := []forge.Run{{Name: "CI", Status: forge.RunInProgress}}
	assert.Check(t, is.DeepEqual(got.Runs, want))
}

func TestForge_Verdict_WhenQueryNamesRequiredChecks_CarriesEveryCheckAndListsTheMissing(t *testing.T) {
	checks := "[" + checkJSON("lint", "fail", runLink) + "," + checkJSON("test", "pass", statusLink) + "]"
	fake := ghtest.Setup(t, answered(ghtest.Answer{Stdout: checks, Code: 1}, checksArgs()...), ok("[]", runsArgs()...))

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
	assertCalls(t, fake, received(checksArgs(), runsArgs()))
}

func TestForge_Verdict_FailedLog(t *testing.T) {
	const logText = "build\tRun tests\t--- FAIL: TestForge (0.01s)\nbuild\tRun tests\tFAIL\n"

	logged := ghtest.Answer{Stdout: logText}
	gone := ghtest.Answer{Stderr: "run 9001 not found\n", Code: 1}
	read := forge.FailedLog{Check: "build", Text: logText, State: forge.FailedLogRead}
	noRun := forge.FailedLog{Check: "scan", State: forge.FailedLogNoRun}
	unreadable := forge.FailedLog{Check: "build", State: forge.FailedLogUnreadable}
	tests := []struct {
		name     string
		checks   string
		want     forge.FailedLog
		state    forge.VerdictState
		log      ghtest.Answer
		wantCall bool
	}{
		{name: "failing check of a run", checks: checkJSON("build", "fail", runLink), log: logged, want: read, state: forge.VerdictRed, wantCall: true},
		{name: "canceled check of a run", checks: checkJSON("build", "cancel", runLink), log: logged, want: read, state: forge.VerdictRed, wantCall: true},
		{name: "passing check ahead of the failing one", checks: checkJSON("lint", "pass", statusLink) + "," + checkJSON("build", "fail", runLink), log: logged, want: read, state: forge.VerdictRed, wantCall: true},
		{name: "failing status ahead of a failing check of a run", checks: checkJSON("scan", "fail", statusLink) + "," + checkJSON("build", "fail", runLink), log: logged, want: noRun, state: forge.VerdictRed},
		{name: "failing status with no run", checks: checkJSON("scan", "fail", statusLink), log: logged, want: noRun, state: forge.VerdictRed},
		{name: "run view fails", checks: checkJSON("build", "fail", runLink), log: gone, want: unreadable, state: forge.VerdictRed, wantCall: true},
		{name: "check of a run still pending", checks: checkJSON("build", "pending", runLink), log: logged, state: forge.VerdictPending},
		{name: "every check passed", checks: checkJSON("build", "pass", runLink), log: logged, state: forge.VerdictGreen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := ghtest.Setup(t, ok("["+tt.checks+"]", checksArgs()...), ok("[]", runsArgs()...), answered(tt.log, logArgs()...))

			got, err := newForge().Verdict(t.Context(), engine(), verdictQuery())
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got.FailedLog, tt.want))
			assert.Check(t, is.Equal(got.State(), tt.state))
			wantCalls := received(checksArgs(), runsArgs())
			if tt.wantCall {
				wantCalls = received(checksArgs(), runsArgs(), logArgs())
			}
			assertCalls(t, fake, wantCalls)
		})
	}
}

func TestForge_Verdict_WhenRed_FailedLogBelongsToTheCheckFailedNames(t *testing.T) {
	checks := "[" + checkJSON("lint", "fail", runLink) + "," + checkJSON("scan", "fail", statusLink) + "," + checkJSON("build", "fail", runLink) + "]"
	fake := ghtest.Setup(t, answered(ghtest.Answer{Stdout: checks, Code: 1}, checksArgs()...), ok("[]", runsArgs()...), ok("log\n", logArgs()...))

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery("scan", "build"))
	assert.NilError(t, err)

	failed, _ := got.Failed()
	want := forge.FailedLog{Check: "scan", State: forge.FailedLogNoRun}
	assert.Check(t, is.DeepEqual(got.FailedLog, want))
	assert.Check(t, is.Equal(failed.Name, got.FailedLog.Check))
	assertCalls(t, fake, received(checksArgs(), runsArgs()))
}

func TestForge_Verdict_WhenFailingCheckIsNotRequired_ReadsNoLog(t *testing.T) {
	checks := "[" + checkJSON("lint", "fail", runLink) + "," + checkJSON("build", "pass", runLink) + "]"
	fake := ghtest.Setup(t, answered(ghtest.Answer{Stdout: checks, Code: 1}, checksArgs()...), ok("[]", runsArgs()...))

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery("build"))
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got.FailedLog, forge.FailedLog{}))
	assertCalls(t, fake, received(checksArgs(), runsArgs()))
}

// follows reports whether value is the argument right after flag.
func follows(argv []string, flag, value string) bool {
	at := slices.Index(argv, flag)

	return at >= 0 && at+1 < len(argv) && argv[at+1] == value
}

// namesRepository reports whether an argv names the repository in the form its `gh` command takes.
func namesRepository(argv []string) bool {
	switch argv[1] {
	case "auth":
		return follows(argv, "--hostname", engineHost)
	case "repo":
		return argv[3] == engineSlug
	case "api":
		inPath := func(arg string) bool { return strings.HasPrefix(arg, "repos/ada/engine/") }

		return follows(argv, "--hostname", engineHost) && slices.ContainsFunc(argv, inPath)
	case "pr":
		numbered := argv[2] != "view" && argv[2] != "checks"

		return follows(argv, "--repo", engineSlug) && (numbered || follows(argv, engineSlug, "7"))
	case "run":
		return follows(argv, "--repo", engineSlug)
	default:
		return false
	}
}

func TestForge_NamesTheRepositoryOnEveryCall(t *testing.T) {
	checks := ghtest.Answer{Stdout: "[" + checkJSON("test", "fail", runLink) + "]", Code: 1}
	fake := ghtest.Setup(t,
		ok("Logged in\n", loginArgs()...),
		ok(`{"isFork":false,"viewerPermission":"ADMIN"}`, repoViewArgs()...),
		ok("[]", listArgs("open")...),
		ok("[]", listArgs("merged")...),
		ok(pullURL+"\n", createArgs()...),
		ok(headJSON("BLOCKED", ""), headArgs()...),
		ok(rulesForBuildAndTest, rulesArgs()...),
		answered(checks, checksArgs()...),
		ok("[]", runsArgs()...),
		ok("log\n", logArgs()...),
	)
	target := newForge()
	for _, op := range operations() {
		assert.NilError(t, op.run(t.Context(), target), "%s", op.name)
	}

	calls := fake.Calls()

	assert.Assert(t, len(calls) >= len(operations()), "the fake gh received %d calls, want at least one per operation", len(calls))
	for _, argv := range calls {
		assert.Check(t, namesRepository(argv), "gh call %q does not name the repository %s in its form", argv, engineSlug)
	}
}

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
