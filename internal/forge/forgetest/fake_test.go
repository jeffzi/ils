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
	subject   = "Add the widget list (#12)"
	body      = "List every widget on the home page."
)

// repo is the repository every call is made against.
func repo() forge.Repo {
	return forge.Repo{Host: "example.com", Owner: "acme", Name: "widgets"}
}

// cleanHead is a clean answer of Head for the new commit.
func cleanHead() forge.Head {
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

// headQuery asks for the head of the pull request, requiring the lint and test checks.
func headQuery() forge.HeadQuery {
	return forge.HeadQuery{Number: number, Required: []string{"lint", "test"}}
}

// landing lands the new commit of the pull request with its subject and body.
func landing() forge.Landing {
	return forge.Landing{Number: number, HeadSHA: newCommit, Subject: subject, Body: body}
}

func TestFake_WhenOperationIsNotScripted_ReportsItAsUnscripted(t *testing.T) {
	t.Parallel()
	stub := failtest.New(t)
	fake := forgetest.New(stub, forgetest.Script{})

	_, err := fake.Identity(t.Context(), repo())

	assert.Check(t, is.ErrorIs(err, forgetest.ErrUnscripted))
	assert.Check(t, stub.Failed(), "the fake did not fail the test")
	assert.Assert(t, is.Len(stub.Messages(), 1))
	assert.Check(t, is.Contains(stub.Messages()[0], string(forgetest.OpIdentity)))
}

func TestFake_Calls_WhenOperationIsNotScripted_RecordsIt(t *testing.T) {
	t.Parallel()
	fake := forgetest.New(failtest.New(t), forgetest.Script{})

	_, _ = fake.Head(t.Context(), repo(), headQuery())

	assert.Check(t, is.DeepEqual(fake.Calls(), []forgetest.Call{{Op: forgetest.OpHead, Repo: repo(), HeadQuery: headQuery()}}))
}

func TestFake_WhenCalledPastTheLastStep_RepeatsTheLast(t *testing.T) {
	t.Parallel()

	stale, unknown, clean := staleHead(), unknownHead(), cleanHead()
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

func TestFake_WhenOnReadIsSet_RunsItOnARead(t *testing.T) {
	t.Parallel()

	var seen []forgetest.Op

	fake := forgetest.New(t, forgetest.Script{
		Head:   forgetest.Values(cleanHead()),
		OnRead: func(call forgetest.Call) { seen = append(seen, call.Op) },
	})

	_, err := fake.Head(t.Context(), repo(), headQuery())
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(seen, []forgetest.Op{forgetest.OpHead}))
}

func TestFake_WhenOnReadIsSet_SkipsItOnAWrite(t *testing.T) {
	t.Parallel()

	var seen []forgetest.Op

	fake := forgetest.New(t, forgetest.Script{
		Land:   []error{nil},
		OnRead: func(call forgetest.Call) { seen = append(seen, call.Op) },
	})

	err := fake.Land(t.Context(), repo(), landing())
	assert.NilError(t, err)

	assert.Check(t, is.Len(seen, 0))
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
	return forgetest.Script{Head: []forgetest.Answer[forge.Head]{{Value: cleanHead(), Hold: true}}}
}

// wait is how long a read's context lives before it ends, when it does not end before the read.
const wait = 10 * time.Second

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
			Head: []forgetest.Answer[forge.Head]{{Value: cleanHead(), Hold: true}, {Value: next}, {Value: last}},
		})
		_, heldErr := fake.Head(ended(t), repo(), headQuery())
		assert.Assert(t, is.ErrorIs(heldErr, context.Canceled), "the answer was not held")

		got, err := fake.Head(t.Context(), repo(), headQuery())
		assert.NilError(t, err)

		assert.Check(t, is.DeepEqual(got, next))
	})
}
