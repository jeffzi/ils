package forgetest_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/failtest"
	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
)

const (
	branch    = "feature"
	trunk     = "main"
	number    = 12
	oldCommit = "1111111"
	newCommit = "2222222"
	lintCheck = "lint"
	testCheck = "test"
	rawClean  = "CLEAN"
	subject   = "Add the widget list (#12)"
	body      = "List every widget on the home page."
)

// repo is the repository every call is made against.
func repo() forge.Repo {
	return forge.Repo{Host: "example.com", Owner: "acme", Name: "widgets"}
}

// requiredChecks returns a fresh list of the checks the fixtures require.
func requiredChecks() []string {
	return []string{lintCheck, testCheck}
}

// newChange is the argument of OpenChange.
func newChange() forge.NewChange {
	return forge.NewChange{Title: branch, Base: trunk, Head: branch}
}

// head is the scripted answer of Head.
func head() forge.Head {
	return forge.Head{SHA: newCommit, MergeState: forge.MergeClean, RawMergeState: rawClean}
}

// headQuery is the argument of Head.
func headQuery() forge.HeadQuery {
	return forge.HeadQuery{Number: number, Required: requiredChecks()}
}

// verdictQuery is the argument of Verdict.
func verdictQuery() forge.VerdictQuery {
	return forge.VerdictQuery{Number: number, SHA: newCommit, Required: requiredChecks()}
}

// landing is the argument of Land.
func landing() forge.Landing {
	return forge.Landing{Number: number, HeadSHA: newCommit, Subject: subject, Body: body}
}

// operation is one forge operation called with the fixture arguments.
type operation struct {
	call func(context.Context, *forgetest.Fake) (any, error)
	name string
	// made is the call the fake records for the operation.
	made forgetest.Call
}

// operations lists every forge operation, called with the fixture arguments.
func operations() []operation {
	return []operation{
		{name: "identity", made: forgetest.Call{Op: forgetest.OpIdentity, Repo: repo()}, call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.Identity(ctx, repo())
		}},
		{name: "find changes", made: forgetest.Call{Op: forgetest.OpFindChanges, Repo: repo(), Branch: branch}, call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.FindChanges(ctx, repo(), branch)
		}},
		{name: "open change", made: forgetest.Call{Op: forgetest.OpOpenChange, Repo: repo(), Change: newChange()}, call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.OpenChange(ctx, repo(), newChange())
		}},
		{name: "head", made: forgetest.Call{Op: forgetest.OpHead, Repo: repo(), HeadQuery: headQuery()}, call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.Head(ctx, repo(), headQuery())
		}},
		{name: "trunk protection", made: forgetest.Call{Op: forgetest.OpTrunkProtection, Repo: repo(), Branch: trunk}, call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.TrunkProtection(ctx, repo(), trunk)
		}},
		{name: "verdict", made: forgetest.Call{Op: forgetest.OpVerdict, Repo: repo(), VerdictQuery: verdictQuery()}, call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.Verdict(ctx, repo(), verdictQuery())
		}},
		{name: "has trunk CI", made: forgetest.Call{Op: forgetest.OpHasTrunkCI, Repo: repo(), Branch: trunk}, call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.HasTrunkCI(ctx, repo(), trunk)
		}},
		{name: "message", made: forgetest.Call{Op: forgetest.OpMessage, Repo: repo(), Number: number}, call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.Message(ctx, repo(), number)
		}},
		{name: "land", made: forgetest.Call{Op: forgetest.OpLand, Repo: repo(), Landing: landing()}, call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return nil, f.Land(ctx, repo(), landing())
		}},
		{name: "delete branch", made: forgetest.Call{Op: forgetest.OpDeleteBranch, Repo: repo(), Branch: branch}, call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return nil, f.DeleteBranch(ctx, repo(), branch)
		}},
	}
}

// script scripts every operation to answer once: head() for Head and the zero value for the rest.
func script() forgetest.Script {
	return forgetest.Script{
		Identity:        forgetest.Values(forge.Repo{}),
		FindChanges:     forgetest.Values([]forge.Change(nil)),
		OpenChange:      forgetest.Values(forge.ChangeRef{}),
		Head:            forgetest.Values(head()),
		TrunkProtection: forgetest.Values(forge.Protection{}),
		Verdict:         forgetest.Values(forge.Verdict{}),
		HasTrunkCI:      forgetest.Values(false),
		Message:         forgetest.Values(forge.Message{}),
		Land:            []error{nil},
		DeleteBranch:    []error{nil},
	}
}

// callEach calls every operation of fake once with the fixture arguments, failing the test on any
// error.
func callEach(t *testing.T, fake *forgetest.Fake) {
	t.Helper()

	for _, op := range operations() {
		_, err := op.call(t.Context(), fake)
		assert.NilError(t, err, "%s", op.name)
	}
}

// callTimes calls call times times and returns its values in order, failing the test on any error.
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

func TestFake_WhenAnswerIsNotHeldAndTheContextEnded_ReturnsItsValue(t *testing.T) {
	t.Parallel()
	fake := forgetest.New(t, script())

	got, err := fake.Head(ended(t), repo(), headQuery())
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got, head()))
}

func TestFake_WhenOperationIsNotScripted_ReportsItAsUnscripted(t *testing.T) {
	t.Parallel()
	stub := failtest.New(t)
	fake := forgetest.New(stub, forgetest.Script{})

	_, err := fake.Head(t.Context(), repo(), headQuery())

	assert.Check(t, is.ErrorIs(err, forgetest.ErrUnscripted))
	assert.Assert(t, is.Len(stub.Messages(), 1))
	assert.Check(t, is.Contains(stub.Messages()[0], string(forgetest.OpHead)))
}

func TestFake_Calls_WhenOperationIsNotScripted_RecordsIt(t *testing.T) {
	t.Parallel()
	fake := forgetest.New(failtest.New(t), forgetest.Script{})

	_, _ = fake.Head(t.Context(), repo(), headQuery())

	assert.Check(t, is.DeepEqual(fake.Calls(), []forgetest.Call{{Op: forgetest.OpHead, Repo: repo(), HeadQuery: headQuery()}}))
}

func TestFake_Calls_WhenEveryOperationIsCalled_RecordsThemInOrder(t *testing.T) {
	t.Parallel()

	fake := forgetest.New(t, script())

	callEach(t, fake)

	var want []forgetest.Call
	for _, op := range operations() {
		want = append(want, op.made)
	}
	assert.Check(t, is.DeepEqual(fake.Calls(), want))
}

func TestFake_WhenCalledRepeatedly_AdvancesOneStepPerCall(t *testing.T) {
	t.Parallel()

	stale := forge.Head{SHA: oldCommit, MergeState: forge.MergeClean, RawMergeState: rawClean}
	unknown := forge.Head{SHA: newCommit, MergeState: forge.MergeUnknown, RawMergeState: "UNKNOWN"}
	clean := forge.Head{SHA: newCommit, MergeState: forge.MergeClean, RawMergeState: rawClean}
	fake := forgetest.New(t, forgetest.Script{Head: forgetest.Values(stale, unknown, clean)})

	got := callTimes(t, 3, func() (forge.Head, error) { return fake.Head(t.Context(), repo(), headQuery()) })

	assert.Check(t, is.DeepEqual(got, []forge.Head{stale, unknown, clean}))
}

func TestFake_WhenCalledPastTheLastStep_RepeatsTheLast(t *testing.T) {
	t.Parallel()

	stale := forge.Head{SHA: oldCommit, MergeState: forge.MergeClean, RawMergeState: rawClean}
	clean := forge.Head{SHA: newCommit, MergeState: forge.MergeClean, RawMergeState: rawClean}
	fake := forgetest.New(t, forgetest.Script{Head: forgetest.Values(stale, clean)})

	got := callTimes(t, 4, func() (forge.Head, error) { return fake.Head(t.Context(), repo(), headQuery()) })

	assert.Check(t, is.DeepEqual(got, []forge.Head{stale, clean, clean, clean}))
}

func TestFake_WhenOnReadIsSet_RunsItOncePerRead(t *testing.T) {
	t.Parallel()

	var seen []forgetest.Op

	scripted := script()
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
		forgetest.OpMessage,
	}
	assert.Check(t, is.DeepEqual(seen, want))
}

func TestFake_WhenOnReadIsSet_RunsItAfterRecordingTheCall(t *testing.T) {
	t.Parallel()

	var (
		recorded []int
		fake     *forgetest.Fake
	)

	scripted := script()
	scripted.OnRead = func(forgetest.Call) { recorded = append(recorded, len(fake.Calls())) }
	fake = forgetest.New(t, scripted)

	_, err := fake.Head(t.Context(), repo(), headQuery())
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(recorded, []int{1}))
}

func TestFake_Land_WhenOnLandIsSet_RunsItWithTheLandingAfterRecordingTheCall(t *testing.T) {
	t.Parallel()

	var (
		seen     []forgetest.Call
		recorded []int
		fake     *forgetest.Fake
	)

	fake = forgetest.New(t, forgetest.Script{
		Land: []error{nil},
		OnLand: func(call forgetest.Call) {
			seen = append(seen, call)
			recorded = append(recorded, len(fake.Calls()))
		},
	})

	_ = fake.Land(t.Context(), repo(), landing())

	assert.Check(t, is.DeepEqual(seen, []forgetest.Call{{Op: forgetest.OpLand, Repo: repo(), Landing: landing()}}))
	assert.Check(t, is.DeepEqual(recorded, []int{1}), "calls recorded when the hook ran")
}

func TestFake_Land_WhenCalledTwice_AdvancesItsErrorSequence(t *testing.T) {
	t.Parallel()
	scripted := errors.New("the head moved")
	fake := forgetest.New(t, forgetest.Script{Land: []error{nil, scripted}})

	firstErr := fake.Land(t.Context(), repo(), landing())
	secondErr := fake.Land(t.Context(), repo(), landing())

	assert.Check(t, is.Nil(firstErr))
	assert.Check(t, is.ErrorIs(secondErr, scripted))
}

func TestFake_Land_WhenOnLandIsSet_RunsItOnAnUnscriptedLandingToo(t *testing.T) {
	t.Parallel()

	var seen []forgetest.Call

	stub := failtest.New(t)
	fake := forgetest.New(stub, forgetest.Script{OnLand: func(call forgetest.Call) { seen = append(seen, call) }})

	err := fake.Land(t.Context(), repo(), landing())

	assert.Check(t, is.ErrorIs(err, forgetest.ErrUnscripted))
	assert.Check(t, is.DeepEqual(seen, []forgetest.Call{{Op: forgetest.OpLand, Repo: repo(), Landing: landing()}}))
}

// hold scripts one held answer whose value would be value.
func hold[T any](value T) []forgetest.Answer[T] {
	return []forgetest.Answer[T]{{Value: value, Hold: true}}
}

// heldScript scripts Head to hold its read until the context ends.
func heldScript() forgetest.Script {
	return forgetest.Script{Head: hold(head())}
}

// wait is how long a read's context lives before it ends, when it does not end before the read.
const wait = 10 * time.Second

// ended returns a context canceled before it is returned.
func ended(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	return ctx
}

func TestFake_WhenAnswerIsHeld_ReturnsTheContextErrorOnceTheContextEnds(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var elapsed []time.Duration

		ctx, cancel := context.WithTimeout(t.Context(), wait)
		t.Cleanup(cancel)
		start := time.Now()
		scripted := heldScript()
		scripted.OnRead = func(forgetest.Call) {
			elapsed = append(elapsed, time.Since(start))
		}
		fake := forgetest.New(t, scripted)

		got, err := fake.Head(ctx, repo(), headQuery())

		assert.Check(t, is.ErrorIs(err, context.DeadlineExceeded))
		assert.Check(t, reflect.ValueOf(got).IsZero(), "value %v, want the zero value", got)
		assert.Check(t, is.DeepEqual(elapsed, []time.Duration{0}), "time held when the hook ran")
	})
}

func TestFake_WhenAnswerIsHeld_TakesOneStep(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		next := forge.Head{SHA: oldCommit, MergeState: forge.MergeClean, RawMergeState: rawClean}
		last := forge.Head{SHA: newCommit, MergeState: forge.MergeUnknown, RawMergeState: "UNKNOWN"}
		fake := forgetest.New(t, forgetest.Script{
			Head: []forgetest.Answer[forge.Head]{{Value: head(), Hold: true}, {Value: next}, {Value: last}},
		})

		_, heldErr := fake.Head(ended(t), repo(), headQuery())
		got, err := fake.Head(t.Context(), repo(), headQuery())

		assert.Check(t, is.ErrorIs(heldErr, context.Canceled))
		assert.NilError(t, err)
		assert.Check(t, is.DeepEqual(got, next))
	})
}
