package forgetest_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
)

const (
	branch     = "feature"
	trunk      = "main"
	number     = 12
	oldCommit  = "1111111"
	newCommit  = "2222222"
	pullURL    = "https://example.com/acme/widgets/pull/12"
	lintCheck  = "lint"
	testCheck  = "test"
	workflow   = "ci"
	rawClean   = "CLEAN"
	hasTrunkCI = true
)

// repo is the repository every call is made against.
func repo() forge.Repo {
	return forge.Repo{Host: "example.com", Owner: "acme", Name: "widgets"}
}

// identity is the scripted answer of Identity: repo with push access.
func identity() forge.Repo {
	resolved := repo()
	resolved.CanPush = true

	return resolved
}

// requiredChecks returns a fresh list of the checks the fixtures require.
func requiredChecks() []string {
	return []string{lintCheck, testCheck}
}

// changes is the scripted answer of FindChanges: one open change on the feature branch.
func changes() []forge.Change {
	return []forge.Change{{
		Number:  number,
		URL:     pullURL,
		Title:   branch,
		Base:    trunk,
		HeadSHA: oldCommit,
		State:   forge.ChangeOpen,
	}}
}

// newChange is the argument of OpenChange: a change from the feature branch to trunk.
func newChange() forge.NewChange {
	return forge.NewChange{Title: branch, Base: trunk, Head: branch}
}

// opened is the scripted answer of OpenChange: the reference to the new change.
func opened() forge.ChangeRef {
	return forge.ChangeRef{Number: number, URL: pullURL}
}

// head is the scripted answer of Head: the new commit with a clean merge state.
func head() forge.Head {
	return forge.Head{SHA: newCommit, MergeState: forge.MergeClean, RawMergeState: rawClean}
}

// protection is the scripted answer of TrunkProtection: the required checks.
func protection() forge.Protection {
	return forge.Protection{RequiredChecks: requiredChecks()}
}

// headQuery is the argument of Head: the change number and the required checks.
func headQuery() forge.HeadQuery {
	return forge.HeadQuery{Number: number, Required: requiredChecks()}
}

// verdictQuery is the argument of Verdict: the change number, the new commit and the required checks.
func verdictQuery() forge.VerdictQuery {
	return forge.VerdictQuery{Number: number, SHA: newCommit, Required: requiredChecks()}
}

// verdict is the scripted answer of Verdict: a failed lint check and a missing test check.
func verdict() forge.Verdict {
	return forge.Verdict{
		Checks: []forge.Check{{
			Name:     lintCheck,
			Bucket:   forge.BucketFail,
			Workflow: workflow,
			Link:     "https://example.com/acme/widgets/runs/7",
			Required: true,
		}},
		MissingRequired: []string{testCheck},
		Runs:            []forge.Run{{Name: workflow, Status: forge.RunCompleted}},
		FailedLog:       forge.FailedLog{Check: lintCheck, Text: "lint: unused variable", State: forge.FailedLogRead},
	}
}

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
	call func(context.Context, *forgetest.Fake) (any, error)
	name string
	// made is the call the fake records for the operation.
	made forgetest.Call
}

func operations() []operation {
	return []operation{
		{name: "identity", made: forgetest.Call{Op: forgetest.OpIdentity, Repo: repo()}, want: identity(), call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.Identity(ctx, repo())
		}},
		{name: "find changes", made: forgetest.Call{Op: forgetest.OpFindChanges, Repo: repo(), Branch: branch}, want: changes(), call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.FindChanges(ctx, repo(), branch)
		}},
		{name: "open change", made: forgetest.Call{Op: forgetest.OpOpenChange, Repo: repo(), Change: newChange()}, want: opened(), call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.OpenChange(ctx, repo(), newChange())
		}},
		{name: "head", made: forgetest.Call{Op: forgetest.OpHead, Repo: repo(), Head: headQuery()}, want: head(), call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.Head(ctx, repo(), headQuery())
		}},
		{name: "trunk protection", made: forgetest.Call{Op: forgetest.OpTrunkProtection, Repo: repo(), Branch: trunk}, want: protection(), call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.TrunkProtection(ctx, repo(), trunk)
		}},
		{name: "verdict", made: forgetest.Call{Op: forgetest.OpVerdict, Repo: repo(), Query: verdictQuery()}, want: verdict(), call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.Verdict(ctx, repo(), verdictQuery())
		}},
		{name: "has trunk CI", made: forgetest.Call{Op: forgetest.OpHasTrunkCI, Repo: repo(), Branch: trunk}, want: hasTrunkCI, call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.HasTrunkCI(ctx, repo(), trunk)
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
		Identity:        steps(identity(), err),
		FindChanges:     steps(changes(), err),
		OpenChange:      steps(opened(), err),
		Head:            steps(head(), err),
		TrunkProtection: steps(protection(), err),
		Verdict:         steps(verdict(), err),
		HasTrunkCI:      steps(hasTrunkCI, err),
	}
}

func callEach(t *testing.T, fake *forgetest.Fake) {
	t.Helper()

	for _, op := range operations() {
		_, err := op.call(t.Context(), fake)
		assert.NilError(t, err, "%s", op.name)
	}
}

func callTimes[T any](t *testing.T, times int, call func() (T, error)) []T {
	t.Helper()

	values := make([]T, 0, times)

	for range times {
		value, err := call()
		assert.NilError(t, err)

		values = append(values, value)
	}

	return values
}

func TestFake_WhenOperationIsScripted_ReturnsItsValue(t *testing.T) {
	t.Parallel()

	for _, tt := range operations() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := forgetest.New(t, script(nil))

			got, err := tt.call(t.Context(), fake)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, tt.want))
		})
	}
}

func TestFake_WhenAnswerIsError_ReturnsItUnchanged(t *testing.T) {
	t.Parallel()

	for _, tt := range operations() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scripted := &forge.ToolMissingError{Tool: "gh", InstallURL: "https://example.com/install"}
			fake := forgetest.New(t, script(scripted))

			_, err := tt.call(t.Context(), fake)

			got, ok := errors.AsType[*forge.ToolMissingError](err)
			assert.Check(t, ok, "error = %v, want a *forge.ToolMissingError", err)
			assert.Check(t, is.Equal(got, scripted))
		})
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

			assert.Check(t, is.ErrorIs(err, forgetest.ErrUnscripted))

			assert.Assert(t, is.Len(stub.failures, 1))
			assert.Check(t, is.Contains(stub.failures[0], string(tt.made.Op)))

			assert.Check(t, is.DeepEqual(fake.Calls(), []forgetest.Call{tt.made}))
		})
	}
}

func TestFake_Calls_WhenEveryOperationIsCalled_RecordsThemInOrder(t *testing.T) {
	t.Parallel()

	fake := forgetest.New(t, script(nil))

	callEach(t, fake)

	var got []forgetest.Op
	for _, call := range fake.Calls() {
		got = append(got, call.Op)
	}

	want := []forgetest.Op{
		forgetest.OpIdentity,
		forgetest.OpFindChanges,
		forgetest.OpOpenChange,
		forgetest.OpHead,
		forgetest.OpTrunkProtection,
		forgetest.OpVerdict,
		forgetest.OpHasTrunkCI,
	}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestFake_Head_WhenCalledPastTheLastStep_AdvancesOneStepPerCallThenRepeatsTheLast(t *testing.T) {
	t.Parallel()

	stale := forge.Head{SHA: oldCommit, MergeState: forge.MergeClean, RawMergeState: rawClean}
	unknown := forge.Head{SHA: newCommit, MergeState: forge.MergeUnknown, RawMergeState: "UNKNOWN"}
	clean := forge.Head{SHA: newCommit, MergeState: forge.MergeClean, RawMergeState: rawClean}
	fake := forgetest.New(t, forgetest.Script{
		Head: []forgetest.Answer[forge.Head]{{Value: stale}, {Value: stale}, {Value: unknown}, {Value: clean}},
	})

	got := callTimes(t, 6, func() (forge.Head, error) { return fake.Head(t.Context(), repo(), headQuery()) })

	want := []forge.Head{stale, stale, unknown, clean, clean, clean}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestFake_WhenOnReadIsSet_RunsItOncePerRead(t *testing.T) {
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
		forgetest.OpHasTrunkCI,
	}
	assert.Check(t, is.DeepEqual(seen, want))
}
