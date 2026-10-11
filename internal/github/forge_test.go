package github_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
)

// No test in this file is parallel: the fake `gh` is found through `GH_PATH`,
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

	if diff := cmp.Diff(want, fake.Calls()); diff != "" {
		t.Errorf("argv the fake gh received mismatch (-want +got):\n%s", diff)
	}
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
			if !ok {
				t.Fatalf("%s error = %v, want *forge.ToolMissingError", tt.name, err)
			}
			if diff := cmp.Diff(missingTool(), missing); diff != "" {
				t.Errorf("%s error mismatch (-want +got):\n%s", tt.name, diff)
			}
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

			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s error = %v, want context.Canceled", tt.name, err)
			}
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
			if !ok {
				t.Fatalf("error = %v, want *github.ExitError", err)
			}
			want := github.ExitError{Argv: received(tt.args)[0], Stderr: serverDown, Code: 1}
			if diff := cmp.Diff(want, *exitErr); diff != "" {
				t.Errorf("exit error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestForge_WhenReadPrintsMalformedJSON_ReturnsDecodeErrorNamingCall(t *testing.T) {
	for _, tt := range readCases() {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, append(tt.before, ok(`[{"number":`, tt.args...))...)

			err := tt.run(t.Context(), newForge())

			decodeErr, ok := errors.AsType[*github.DecodeError](err)
			if !ok {
				t.Fatalf("error = %v, want *github.DecodeError", err)
			}
			if diff := cmp.Diff(received(tt.args)[0], decodeErr.Argv); diff != "" {
				t.Errorf("decode error argv mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestForge_Identity_WhenNotLoggedIn_ReturnsNotLoggedInError(t *testing.T) {
	fake := ghtest.Setup(t, answered(ghtest.Answer{Stderr: "You are not logged into any GitHub hosts.\n", Code: 1}, loginArgs()...))

	_, err := newForge().Identity(t.Context(), engine())

	notLoggedIn, ok := errors.AsType[*forge.NotLoggedInError](err)
	if !ok {
		t.Fatalf("Identity error = %v, want *forge.NotLoggedInError", err)
	}
	if notLoggedIn.Host != engineHost {
		t.Errorf("Identity error host = %q, want %q", notLoggedIn.Host, engineHost)
	}
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
			if err != nil {
				t.Fatalf("Identity error = %v, want nil", err)
			}

			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("Identity mismatch (-want +got):\n%s", diff)
			}
			assertCalls(t, fake, received(loginArgs(), repoViewArgs()))
		})
	}
}

func TestForge_Identity_WhenRepoUnreadable_ErrorStartsByNamingRepository(t *testing.T) {
	notFound := ghtest.Answer{Stderr: "GraphQL: Could not resolve to a Repository\n", Code: 1}
	ghtest.Setup(t, ok("Logged in\n", loginArgs()...), answered(notFound, repoViewArgs()...))

	_, err := newForge().Identity(t.Context(), engine())

	if err == nil {
		t.Fatal("Identity error = nil, want an error naming the repository")
	}
	wantPrefix := "read the repository " + engineSlug + ": "
	if !strings.HasPrefix(err.Error(), wantPrefix) {
		t.Errorf("Identity error = %q, want it to start with %q", err.Error(), wantPrefix)
	}
}

func TestForge_FindChanges_ReturnsOpenThenMergedChanges(t *testing.T) {
	open := `[{"number":7,"url":"` + pullURL + `","title":"feature","baseRefName":"main","headRefOid":"` + headSHA + `","isDraft":true}]`
	merged := `[{"number":3,"url":"https://git.example.com/ada/engine/pull/3","title":"feat: add the forge",` +
		`"baseRefName":"release","headRefOid":"0a1b2c3","isDraft":false}]`
	fake := ghtest.Setup(t, ok(open, listArgs("open")...), ok(merged, listArgs("merged")...))

	got, err := newForge().FindChanges(t.Context(), engine(), "feature")
	if err != nil {
		t.Fatalf("FindChanges error = %v, want nil", err)
	}

	want := []forge.Change{
		{Number: 7, URL: pullURL, Title: "feature", Base: "main", HeadSHA: headSHA, Draft: true, State: forge.ChangeOpen},
		{
			Number: 3, URL: "https://git.example.com/ada/engine/pull/3", Title: "feat: add the forge",
			Base: "release", HeadSHA: "0a1b2c3", State: forge.ChangeMerged,
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FindChanges mismatch (-want +got):\n%s", diff)
	}
	assertCalls(t, fake, received(listArgs("open"), listArgs("merged")))
}

func TestForge_FindChanges_WhenBranchHasNone_ReturnsNoChanges(t *testing.T) {
	ghtest.Setup(t, ok("[]", listArgs("open")...), ok("[]", listArgs("merged")...))

	got, err := newForge().FindChanges(t.Context(), engine(), "feature")
	if err != nil {
		t.Fatalf("FindChanges error = %v, want nil", err)
	}

	if len(got) != 0 {
		t.Errorf("FindChanges = %v, want no changes", got)
	}
}

func TestForge_OpenChange_WhenCreated_ReturnsNumberAndURL(t *testing.T) {
	fake := ghtest.Setup(t, ok(pullURL+"\n", createArgs()...))

	got, err := newForge().OpenChange(t.Context(), engine(), featureChange())
	if err != nil {
		t.Fatalf("OpenChange error = %v, want nil", err)
	}

	want := forge.ChangeRef{Number: 7, URL: pullURL}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("OpenChange mismatch (-want +got):\n%s", diff)
	}
	assertCalls(t, fake, received(createArgs()))
}

func TestForge_OpenChange_WhenPullRequestExists_ReturnsErrChangeExists(t *testing.T) {
	exists := ghtest.Answer{
		Stderr: "a pull request for branch \"feature\" into branch \"main\" already exists:\n" + pullURL + "\n",
		Code:   1,
	}
	ghtest.Setup(t, answered(exists, createArgs()...))

	_, err := newForge().OpenChange(t.Context(), engine(), featureChange())

	if !errors.Is(err, forge.ErrChangeExists) {
		t.Errorf("OpenChange error = %v, want forge.ErrChangeExists", err)
	}
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

			if errors.Is(err, forge.ErrChangeExists) {
				t.Fatalf("OpenChange error = %v, want an error other than forge.ErrChangeExists", err)
			}
			exitErr, ok := errors.AsType[*github.ExitError](err)
			if !ok {
				t.Fatalf("OpenChange error = %v, want *github.ExitError", err)
			}
			if exitErr.Stderr != tt.stderr {
				t.Errorf("OpenChange error stderr = %q, want %q", exitErr.Stderr, tt.stderr)
			}
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

			if err == nil {
				t.Fatalf("OpenChange = %+v with a nil error, want an error", got)
			}
			if err.Error() != tt.want {
				t.Errorf("OpenChange error = %q, want %q", err.Error(), tt.want)
			}
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
			if err != nil {
				t.Fatalf("Head error = %v, want nil", err)
			}

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Head mismatch (-want +got):\n%s", diff)
			}
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
			if err != nil {
				t.Fatalf("Head error = %v, want nil", err)
			}

			want := forge.Head{SHA: headSHA, MergeState: tt.want, RawMergeState: "BLOCKED"}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("Head mismatch (-want +got):\n%s", diff)
			}
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
			if err != nil {
				t.Fatalf("TrunkProtection error = %v, want nil", err)
			}

			want := forge.Protection{RequiredChecks: tt.want}
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("TrunkProtection mismatch (-want +got):\n%s", diff)
			}
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
			if err != nil {
				t.Fatalf("Verdict error = %v, want nil", err)
			}

			want := []forge.Check{{Name: "build", Bucket: tt.want, Workflow: "CI", Link: statusLink}}
			if diff := cmp.Diff(want, got.Checks); diff != "" {
				t.Errorf("Verdict checks mismatch (-want +got):\n%s", diff)
			}
			assertCalls(t, fake, received(checksArgs(), runsArgs()))
		})
	}
}

func TestForge_Verdict_WhenNoCheckReported_ReturnsZeroChecks(t *testing.T) {
	none := ghtest.Answer{Stderr: "no checks reported on the 'feature' branch\n", Code: 1}
	ghtest.Setup(t, answered(none, checksArgs()...), ok("[]", runsArgs()...))

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery())
	if err != nil {
		t.Fatalf("Verdict error = %v, want nil", err)
	}

	if diff := cmp.Diff(forge.Verdict{}, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Verdict mismatch (-want +got):\n%s", diff)
	}
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
	if err != nil {
		t.Fatalf("Verdict error = %v, want nil", err)
	}

	want := []forge.Run{
		{Name: "CI", Status: forge.RunQueued},
		{Name: "Lint", Status: forge.RunInProgress},
		{Name: "Preview", Status: forge.RunAwaitingPerson},
		{Name: "Docs", Status: forge.RunAwaitingPerson},
		{Name: "Audit", Status: forge.RunQueued},
		{Name: "Build", Status: forge.RunCompleted},
		{Name: "Scan", Status: forge.RunCompleted},
	}
	if diff := cmp.Diff(want, got.Runs); diff != "" {
		t.Errorf("Verdict runs mismatch (-want +got):\n%s", diff)
	}
}

func TestForge_Verdict_WhenRunStatusIsNotKnown_CarriesTheRunAsInProgress(t *testing.T) {
	ghtest.Setup(t, ok("[]", checksArgs()...), ok("["+runJSON("paused")+"]", runsArgs()...))

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery())
	if err != nil {
		t.Fatalf("Verdict error = %v, want nil", err)
	}

	want := []forge.Run{{Name: "CI", Status: forge.RunInProgress}}
	if diff := cmp.Diff(want, got.Runs); diff != "" {
		t.Errorf("Verdict runs mismatch (-want +got):\n%s", diff)
	}
}

func TestForge_Verdict_WhenQueryNamesRequiredChecks_KeepsThemAndListsTheMissing(t *testing.T) {
	checks := "[" + checkJSON("lint", "fail", runLink) + "," + checkJSON("test", "pass", statusLink) + "]"
	fake := ghtest.Setup(t, answered(ghtest.Answer{Stdout: checks, Code: 1}, checksArgs()...), ok("[]", runsArgs()...))

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery("build", "test", "deploy"))
	if err != nil {
		t.Fatalf("Verdict error = %v, want nil", err)
	}

	want := forge.Verdict{
		Checks:          []forge.Check{{Name: "test", Bucket: forge.BucketPass, Workflow: "CI", Link: statusLink, Required: true}},
		MissingRequired: []string{"build", "deploy"},
	}
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Verdict mismatch (-want +got):\n%s", diff)
	}
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
			if err != nil {
				t.Fatalf("Verdict error = %v, want nil", err)
			}

			if diff := cmp.Diff(tt.want, got.FailedLog); diff != "" {
				t.Errorf("Verdict failed log mismatch (-want +got):\n%s", diff)
			}
			if got.State() != tt.state {
				t.Errorf("Verdict state = %q, want %q", got.State(), tt.state)
			}
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
	if err != nil {
		t.Fatalf("Verdict error = %v, want nil", err)
	}

	failed, _ := got.Failed()
	want := forge.FailedLog{Check: "scan", State: forge.FailedLogNoRun}
	if diff := cmp.Diff(want, got.FailedLog); diff != "" {
		t.Errorf("Verdict failed log mismatch (-want +got):\n%s", diff)
	}
	if failed.Name != got.FailedLog.Check {
		t.Errorf("Verdict.Failed() names %q, the failed log names %q, want the same check", failed.Name, got.FailedLog.Check)
	}
	assertCalls(t, fake, received(checksArgs(), runsArgs()))
}

func TestForge_Verdict_WhenFailingCheckIsNotRequired_ReadsNoLog(t *testing.T) {
	checks := "[" + checkJSON("lint", "fail", runLink) + "," + checkJSON("build", "pass", runLink) + "]"
	fake := ghtest.Setup(t, answered(ghtest.Answer{Stdout: checks, Code: 1}, checksArgs()...), ok("[]", runsArgs()...))

	got, err := newForge().Verdict(t.Context(), engine(), verdictQuery("build"))
	if err != nil {
		t.Fatalf("Verdict error = %v, want nil", err)
	}

	if diff := cmp.Diff(forge.FailedLog{}, got.FailedLog); diff != "" {
		t.Errorf("Verdict failed log mismatch (-want +got):\n%s", diff)
	}
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
		if err := op.run(t.Context(), target); err != nil {
			t.Fatalf("%s error = %v, want nil", op.name, err)
		}
	}

	calls := fake.Calls()

	if len(calls) < len(operations()) {
		t.Fatalf("the fake gh received %d calls, want at least one per operation", len(calls))
	}
	for _, argv := range calls {
		if !namesRepository(argv) {
			t.Errorf("gh call %q does not name the repository %s in its form", argv, engineSlug)
		}
	}
}
