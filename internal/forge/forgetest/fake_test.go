package forgetest_test

import (
	"context"
	"errors"
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
	number    = 12
	oldCommit = "1111111"
	newCommit = "2222222"
	lintCheck = "lint"
	testCheck = "test"
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

func head() forge.Head {
	return forge.Head{SHA: newCommit, MergeState: forge.MergeClean}
}

// staleHead is a clean answer of Head for the previous commit.
func staleHead() forge.Head {
	return forge.Head{SHA: oldCommit, MergeState: forge.MergeClean}
}

// unknownHead is an answer of Head whose merge state is not yet known.
func unknownHead() forge.Head {
	return forge.Head{SHA: newCommit, MergeState: forge.MergeUnknown}
}

func headQuery() forge.HeadQuery {
	return forge.HeadQuery{Number: number, Required: requiredChecks()}
}

func landing() forge.Landing {
	return forge.Landing{Number: number, HeadSHA: newCommit, Subject: subject, Body: body}
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

func TestFake_WhenCalledPastTheLastStep_RepeatsTheLast(t *testing.T) {
	t.Parallel()

	stale, unknown, clean := staleHead(), unknownHead(), head()
	fake := forgetest.New(t, forgetest.Script{Head: forgetest.Values(stale, unknown, clean)})

	const calls = 4 // one past the three scripted steps
	got := make([]forge.Head, 0, calls)
	for range calls {
		h, err := fake.Head(t.Context(), repo(), headQuery())
		assert.NilError(t, err)

		got = append(got, h)
	}

	assert.Check(t, is.DeepEqual(got, []forge.Head{stale, unknown, clean, clean}))
}

func TestFake_WhenOnReadIsSet_RunsItOncePerRead(t *testing.T) {
	t.Parallel()

	var seen []forgetest.Op

	fake := forgetest.New(t, forgetest.Script{
		Head:   forgetest.Values(head()),
		Land:   []error{nil},
		OnRead: func(call forgetest.Call) { seen = append(seen, call.Op) },
	})

	_, headErr := fake.Head(t.Context(), repo(), headQuery())
	landErr := fake.Land(t.Context(), repo(), landing())
	assert.NilError(t, headErr)
	assert.NilError(t, landErr)

	assert.Check(t, is.DeepEqual(seen, []forgetest.Op{forgetest.OpHead}))
}

func TestFake_Land_WhenCalledTwice_AdvancesItsErrorSequence(t *testing.T) {
	t.Parallel()
	headMoved := errors.New("the head moved")
	fake := forgetest.New(t, forgetest.Script{Land: []error{nil, headMoved}})

	firstErr := fake.Land(t.Context(), repo(), landing())
	secondErr := fake.Land(t.Context(), repo(), landing())

	assert.Check(t, is.Nil(firstErr))
	assert.Check(t, is.ErrorIs(secondErr, headMoved))
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

// heldScript scripts Head to hold its read until the context ends.
func heldScript() forgetest.Script {
	return forgetest.Script{Head: []forgetest.Answer[forge.Head]{{Value: head(), Hold: true}}}
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
		ctx, cancel := context.WithTimeout(t.Context(), wait)
		t.Cleanup(cancel)
		fake := forgetest.New(t, heldScript())

		got, err := fake.Head(ctx, repo(), headQuery())

		assert.Check(t, is.ErrorIs(err, context.DeadlineExceeded))
		assert.Check(t, is.DeepEqual(got, forge.Head{}))
	})
}

func TestFake_WhenAnswerIsHeld_RunsOnReadBeforeHolding(t *testing.T) {
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

		_, err := fake.Head(ctx, repo(), headQuery())
		assert.Assert(t, is.ErrorIs(err, context.DeadlineExceeded), "the answer was not held")

		assert.Check(t, is.DeepEqual(elapsed, []time.Duration{0}), "time held when the hook ran")
	})
}

func TestFake_WhenAnswerIsHeld_TakesOneStep(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		next := staleHead()
		last := unknownHead()
		fake := forgetest.New(t, forgetest.Script{
			Head: []forgetest.Answer[forge.Head]{{Value: head(), Hold: true}, {Value: next}, {Value: last}},
		})

		_, heldErr := fake.Head(ended(t), repo(), headQuery())
		got, err := fake.Head(t.Context(), repo(), headQuery())
		assert.NilError(t, err)

		assert.Check(t, is.ErrorIs(heldErr, context.Canceled))
		assert.Check(t, is.DeepEqual(got, next))
	})
}
