package forgetest_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
)

var _ forge.Forge = (*forgetest.Fake)(nil)

const (
	branch    = "feature"
	trunk     = "main"
	number    = 12
	oldCommit = "1111111"
	newCommit = "2222222"
	pullURL   = "https://example.com/acme/widgets/pull/12"
)

var (
	repo     = forge.Repo{Host: "example.com", Owner: "acme", Name: "widgets"}
	identity = forge.Repo{Host: "example.com", Owner: "acme", Name: "widgets", CanPush: true}
	changes  = []forge.Change{{
		Number:  number,
		URL:     pullURL,
		Title:   branch,
		Base:    trunk,
		HeadSHA: oldCommit,
		State:   forge.ChangeOpen,
	}}
	newChange  = forge.NewChange{Title: branch, Base: trunk, Head: branch}
	opened     = forge.ChangeRef{Number: number, URL: pullURL}
	head       = forge.Head{SHA: newCommit, MergeState: forge.MergeClean, RawMergeState: "CLEAN"}
	protection = forge.Protection{RequiredChecks: []string{"lint", "test"}}
	headQuery  = forge.HeadQuery{Number: number, Required: []string{"lint", "test"}}
	query      = forge.VerdictQuery{Number: number, SHA: newCommit, Required: []string{"lint", "test"}}
	verdict    = forge.Verdict{
		Checks: []forge.Check{{
			Name:     "lint",
			Bucket:   forge.BucketFail,
			Workflow: "ci",
			Link:     "https://example.com/acme/widgets/runs/7",
			Required: true,
		}},
		MissingRequired: []string{"test"},
		Runs:            []forge.Run{{Name: "ci", Status: forge.RunCompleted}},
		FailedLog:       forge.FailedLog{Check: "lint", Text: "lint: unused variable", State: forge.FailedLogRead},
	}
)

// recorder stands in for the testing.TB the fake reports to, keeping each failure.
type recorder struct {
	failures []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// operation is one forge operation called with the fixture arguments, and the scripted value.
type operation struct {
	want any
	call func(context.Context, forge.Forge) (any, error)
	name string
	// made is the call the fake records for the operation.
	made forgetest.Call
}

// operations lists every operation of the forge seam.
func operations() []operation {
	return []operation{
		{name: "identity", made: forgetest.Call{Op: forgetest.OpIdentity, Repo: repo}, want: identity, call: func(ctx context.Context, f forge.Forge) (any, error) {
			return f.Identity(ctx, repo)
		}},
		{name: "find changes", made: forgetest.Call{Op: forgetest.OpFindChanges, Repo: repo, Branch: branch}, want: changes, call: func(ctx context.Context, f forge.Forge) (any, error) {
			return f.FindChanges(ctx, repo, branch)
		}},
		{name: "open change", made: forgetest.Call{Op: forgetest.OpOpenChange, Repo: repo, Change: newChange}, want: opened, call: func(ctx context.Context, f forge.Forge) (any, error) {
			return f.OpenChange(ctx, repo, newChange)
		}},
		{name: "head", made: forgetest.Call{Op: forgetest.OpHead, Repo: repo, Head: headQuery}, want: head, call: func(ctx context.Context, f forge.Forge) (any, error) {
			return f.Head(ctx, repo, headQuery)
		}},
		{name: "trunk protection", made: forgetest.Call{Op: forgetest.OpTrunkProtection, Repo: repo, Branch: trunk}, want: protection, call: func(ctx context.Context, f forge.Forge) (any, error) {
			return f.TrunkProtection(ctx, repo, trunk)
		}},
		{name: "verdict", made: forgetest.Call{Op: forgetest.OpVerdict, Repo: repo, Query: query}, want: verdict, call: func(ctx context.Context, f forge.Forge) (any, error) {
			return f.Verdict(ctx, repo, query)
		}},
	}
}

// steps is a one-step sequence answering with err when it is set, and with value otherwise.
func steps[T any](value T, err error) []forgetest.Answer[T] {
	if err != nil {
		return []forgetest.Answer[T]{{Err: err}}
	}

	return []forgetest.Answer[T]{{Value: value}}
}

// script answers every operation with err when it is set, and with the fixture values otherwise.
func script(err error) forgetest.Script {
	return forgetest.Script{
		Identity:        steps(identity, err),
		FindChanges:     steps(changes, err),
		OpenChange:      steps(opened, err),
		Head:            steps(head, err),
		TrunkProtection: steps(protection, err),
		Verdict:         steps(verdict, err),
	}
}

// callEach calls every operation of the forge seam on the fake and fails on the first error.
func callEach(t *testing.T, fake *forgetest.Fake) {
	t.Helper()

	for _, op := range operations() {
		if _, err := op.call(t.Context(), fake); err != nil {
			t.Fatalf("%s error = %v, want nil", op.name, err)
		}
	}
}

// read calls the operation times times and returns the values it answered.
func read[T any](t *testing.T, times int, call func() (T, error)) []T {
	t.Helper()

	values := make([]T, 0, times)

	for range times {
		value, err := call()
		if err != nil {
			t.Fatalf("read error = %v, want nil", err)
		}

		values = append(values, value)
	}

	return values
}

func TestFake_AnswersScriptedValue(t *testing.T) {
	t.Parallel()

	for _, tt := range operations() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := forgetest.New(t, script(nil))

			got, err := tt.call(t.Context(), fake)
			if err != nil {
				t.Fatalf("%s error = %v, want nil", tt.name, err)
			}

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("%s answer mismatch (-want +got):\n%s", tt.name, diff)
			}
		})
	}
}

func TestFake_WhenAnswerIsError_ReturnsItUnchanged(t *testing.T) {
	t.Parallel()

	for _, tt := range operations() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scripted := &forge.ToolMissingError{Tool: "gh", InstallHint: "https://example.com/install"}
			fake := forgetest.New(t, script(scripted))

			_, err := tt.call(t.Context(), fake)

			got, ok := errors.AsType[*forge.ToolMissingError](err)
			if !ok || got != scripted {
				t.Errorf("%s error = %v, want the scripted %v", tt.name, err, scripted)
			}
		})
	}
}

func TestFake_Identity_WhenNotLoggedIn_ErrorCarriesHost(t *testing.T) {
	t.Parallel()

	fake := forgetest.New(t, script(fmt.Errorf("identity: %w", &forge.NotLoggedInError{Host: "example.com"})))

	_, err := fake.Identity(t.Context(), repo)

	got, ok := errors.AsType[*forge.NotLoggedInError](err)
	if !ok {
		t.Fatalf("Identity error = %v, want a *forge.NotLoggedInError", err)
	}

	if got.Host != "example.com" {
		t.Errorf("NotLoggedInError.Host = %q, want %q", got.Host, "example.com")
	}
}

func TestFake_OpenChange_WhenChangeExists_ErrorMatchesSentinel(t *testing.T) {
	t.Parallel()

	fake := forgetest.New(t, script(fmt.Errorf("open: %w", forge.ErrChangeExists)))

	_, err := fake.OpenChange(t.Context(), repo, newChange)

	if !errors.Is(err, forge.ErrChangeExists) {
		t.Errorf("OpenChange error = %v, want forge.ErrChangeExists", err)
	}
}

func TestFake_WhenOperationIsNotScripted_FailsTheTestNamingTheOperation(t *testing.T) {
	t.Parallel()

	for _, tt := range operations() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stub := &recorder{}
			fake := forgetest.New(stub, forgetest.Script{})

			_, err := tt.call(t.Context(), fake)

			if !errors.Is(err, forgetest.ErrUnscripted) {
				t.Errorf("%s error = %v, want forgetest.ErrUnscripted", tt.name, err)
			}

			wantFailures := []string{fmt.Sprintf("forgetest: unexpected call %+v: the script has no answer for %s", tt.made, tt.made.Op)}
			if diff := cmp.Diff(wantFailures, stub.failures); diff != "" {
				t.Errorf("%s failures reported mismatch (-want +got):\n%s", tt.name, diff)
			}

			if diff := cmp.Diff([]forgetest.Call{tt.made}, fake.Calls()); diff != "" {
				t.Errorf("%s recorded calls mismatch (-want +got):\n%s", tt.name, diff)
			}
		})
	}
}

func TestFake_Calls_RecordsEveryCallInOrder(t *testing.T) {
	t.Parallel()

	fake := forgetest.New(t, script(nil))

	callEach(t, fake)

	want := []forgetest.Call{
		{Op: forgetest.OpIdentity, Repo: repo},
		{Op: forgetest.OpFindChanges, Repo: repo, Branch: branch},
		{Op: forgetest.OpOpenChange, Repo: repo, Change: newChange},
		{Op: forgetest.OpHead, Repo: repo, Head: headQuery},
		{Op: forgetest.OpTrunkProtection, Repo: repo, Branch: trunk},
		{Op: forgetest.OpVerdict, Repo: repo, Query: query},
	}
	if diff := cmp.Diff(want, fake.Calls()); diff != "" {
		t.Errorf("Calls() mismatch (-want +got):\n%s", diff)
	}
}

func TestFake_Head_AdvancesOneStepPerReadThenRepeatsTheLast(t *testing.T) {
	t.Parallel()

	stale := forge.Head{SHA: oldCommit, MergeState: forge.MergeClean, RawMergeState: "CLEAN"}
	unknown := forge.Head{SHA: newCommit, MergeState: forge.MergeUnknown, RawMergeState: "UNKNOWN"}
	clean := forge.Head{SHA: newCommit, MergeState: forge.MergeClean, RawMergeState: "CLEAN"}
	fake := forgetest.New(t, forgetest.Script{
		Head: []forgetest.Answer[forge.Head]{{Value: stale}, {Value: stale}, {Value: unknown}, {Value: clean}},
	})

	got := read(t, 6, func() (forge.Head, error) { return fake.Head(t.Context(), repo, headQuery) })

	want := []forge.Head{stale, stale, unknown, clean, clean, clean}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Head reads mismatch (-want +got):\n%s", diff)
	}
}

func TestFake_Verdict_AdvancesOneStepPerReadThenRepeatsTheLast(t *testing.T) {
	t.Parallel()

	pending := forge.Verdict{
		Checks: []forge.Check{{Name: "test", Bucket: forge.BucketPending, Workflow: "ci", Required: true}},
		Runs:   []forge.Run{{Name: "ci", Status: forge.RunInProgress}},
	}
	green := forge.Verdict{
		Checks: []forge.Check{{Name: "test", Bucket: forge.BucketPass, Workflow: "ci", Required: true}},
		Runs:   []forge.Run{{Name: "ci", Status: forge.RunCompleted}},
	}
	fake := forgetest.New(t, forgetest.Script{
		Verdict: []forgetest.Answer[forge.Verdict]{{Value: pending}, {Value: green}},
	})

	got := read(t, 3, func() (forge.Verdict, error) { return fake.Verdict(t.Context(), repo, query) })

	want := []forge.Verdict{pending, green, green}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Verdict reads mismatch (-want +got):\n%s", diff)
	}
}

func TestFake_OnRead_RunsOncePerRead(t *testing.T) {
	t.Parallel()

	var seen []forgetest.Op

	scripted := script(nil)
	scripted.OnRead = func(call forgetest.Call) { seen = append(seen, call.Op) }
	fake := forgetest.New(t, scripted)

	callEach(t, fake)

	want := []forgetest.Op{
		forgetest.OpIdentity,
		forgetest.OpFindChanges,
		forgetest.OpHead,
		forgetest.OpTrunkProtection,
		forgetest.OpVerdict,
	}
	if diff := cmp.Diff(want, seen); diff != "" {
		t.Errorf("OnRead calls mismatch (-want +got):\n%s", diff)
	}
}
