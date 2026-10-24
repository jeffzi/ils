package forgetest_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

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
	subject    = "Add the widget list (#12)"
	body       = "List every widget on the home page."
)

// repo is the repository every call is made against.
func repo() forge.Repo {
	return forge.Repo{Host: "example.com", Owner: "acme", Name: "widgets"}
}

// identity is the scripted answer of Identity.
func identity() forge.Repo {
	resolved := repo()
	resolved.CanPush = true

	return resolved
}

// requiredChecks returns a fresh list of the checks the fixtures require.
func requiredChecks() []string {
	return []string{lintCheck, testCheck}
}

// changes is the scripted answer of FindChanges.
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

// newChange is the argument of OpenChange.
func newChange() forge.NewChange {
	return forge.NewChange{Title: branch, Base: trunk, Head: branch}
}

// opened is the scripted answer of OpenChange.
func opened() forge.ChangeRef {
	return forge.ChangeRef{Number: number, URL: pullURL}
}

// head is the scripted answer of Head.
func head() forge.Head {
	return forge.Head{SHA: newCommit, MergeState: forge.MergeClean, RawMergeState: rawClean}
}

// protection is the scripted answer of TrunkProtection.
func protection() forge.Protection {
	return forge.Protection{RequiredChecks: requiredChecks()}
}

// headQuery is the argument of Head.
func headQuery() forge.HeadQuery {
	return forge.HeadQuery{Number: number, Required: requiredChecks()}
}

// verdictQuery is the argument of Verdict.
func verdictQuery() forge.VerdictQuery {
	return forge.VerdictQuery{Number: number, SHA: newCommit, Required: requiredChecks()}
}

// verdict is the scripted answer of Verdict.
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

// message is the scripted answer of Message.
func message() forge.Message {
	return forge.Message{
		Title:    "Add the widget list",
		Body:     body,
		Trailers: []string{"Co-authored-by: Ada <ada@example.com>"},
	}
}

// landing is the argument of Land.
func landing() forge.Landing {
	return forge.Landing{Number: number, HeadSHA: newCommit, Subject: subject, Body: body}
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

// operations lists every forge operation, called with the fixture arguments.
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
		{name: "head", made: forgetest.Call{Op: forgetest.OpHead, Repo: repo(), HeadQuery: headQuery()}, want: head(), call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.Head(ctx, repo(), headQuery())
		}},
		{name: "trunk protection", made: forgetest.Call{Op: forgetest.OpTrunkProtection, Repo: repo(), Branch: trunk}, want: protection(), call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.TrunkProtection(ctx, repo(), trunk)
		}},
		{name: "verdict", made: forgetest.Call{Op: forgetest.OpVerdict, Repo: repo(), VerdictQuery: verdictQuery()}, want: verdict(), call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.Verdict(ctx, repo(), verdictQuery())
		}},
		{name: "has trunk CI", made: forgetest.Call{Op: forgetest.OpHasTrunkCI, Repo: repo(), Branch: trunk}, want: hasTrunkCI, call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
			return f.HasTrunkCI(ctx, repo(), trunk)
		}},
		{name: "message", made: forgetest.Call{Op: forgetest.OpMessage, Repo: repo(), Number: number}, want: message(), call: func(ctx context.Context, f *forgetest.Fake) (any, error) {
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

// steps scripts one answer: the error when err is set, value otherwise.
func steps[T any](value T, err error) []forgetest.Answer[T] {
	if err != nil {
		return []forgetest.Answer[T]{{Err: err}}
	}

	return []forgetest.Answer[T]{{Value: value}}
}

// script scripts every operation to answer its fixture value, or err when err is set.
func script(err error) forgetest.Script {
	return forgetest.Script{
		Identity:        steps(identity(), err),
		FindChanges:     steps(changes(), err),
		OpenChange:      steps(opened(), err),
		Head:            steps(head(), err),
		TrunkProtection: steps(protection(), err),
		Verdict:         steps(verdict(), err),
		HasTrunkCI:      steps(hasTrunkCI, err),
		Message:         steps(message(), err),
		Land:            []error{err},
		DeleteBranch:    []error{err},
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

// readContext is a context an answer that is not held is read under.
type readContext struct {
	ctx  func(t *testing.T) context.Context
	name string
}

// readContexts are the contexts an answer that is not held leaves as it is: the test's own, and
// one that ended before the read.
func readContexts() []readContext {
	return []readContext{
		{name: "live", ctx: func(t *testing.T) context.Context { return t.Context() }},
		{name: "ended", ctx: ended},
	}
}

func TestFake_WhenOperationIsScripted_ReturnsItsValue(t *testing.T) {
	t.Parallel()

	for _, tt := range operations() {
		for _, rc := range readContexts() {
			t.Run(tt.name+"/"+rc.name, func(t *testing.T) {
				t.Parallel()

				fake := forgetest.New(t, script(nil))

				got, err := tt.call(rc.ctx(t), fake)
				assert.NilError(t, err)

				assert.Check(t, is.DeepEqual(got, tt.want))
			})
		}
	}
}

func TestFake_WhenAnswerIsError_ReturnsItUnchanged(t *testing.T) {
	t.Parallel()

	for _, tt := range operations() {
		for _, rc := range readContexts() {
			t.Run(tt.name+"/"+rc.name, func(t *testing.T) {
				t.Parallel()

				scripted := errors.New("gh is not installed")
				fake := forgetest.New(t, script(scripted))

				_, err := tt.call(rc.ctx(t), fake)

				assert.Check(t, is.Equal(err, scripted))
			})
		}
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

	var want []forgetest.Call
	for _, op := range operations() {
		want = append(want, op.made)
	}
	assert.Check(t, is.DeepEqual(fake.Calls(), want))
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
		forgetest.OpMessage,
	}
	assert.Check(t, is.DeepEqual(seen, want))
}

func TestFake_WhenOnLandIsSet_RunsItWithEachLandingBeforeItReturns(t *testing.T) {
	t.Parallel()

	first := landing()
	second := landing()
	second.HeadSHA = oldCommit
	second.Body = ""

	scripted := errors.New("the head moved")

	var (
		seen     []forgetest.Call
		recorded []int
		fake     *forgetest.Fake
	)

	fake = forgetest.New(t, forgetest.Script{
		Land: []error{nil, scripted},
		OnLand: func(call forgetest.Call) {
			seen = append(seen, call)
			recorded = append(recorded, len(fake.Calls()))
		},
	})

	firstErr := fake.Land(t.Context(), repo(), first)
	secondErr := fake.Land(t.Context(), repo(), second)

	assert.Check(t, is.Nil(firstErr))
	assert.Check(t, is.Equal(secondErr, scripted))

	want := []forgetest.Call{
		{Op: forgetest.OpLand, Repo: repo(), Landing: first},
		{Op: forgetest.OpLand, Repo: repo(), Landing: second},
	}
	assert.Check(t, is.DeepEqual(seen, want))
	assert.Check(t, is.DeepEqual(recorded, []int{1, 2}), "calls recorded when the hook ran")
}

// readOps lists the read operations, called with the fixture arguments.
func readOps() []operation {
	var picked []operation

	for _, op := range operations() {
		switch op.made.Op {
		case forgetest.OpOpenChange, forgetest.OpLand, forgetest.OpDeleteBranch:
		default:
			picked = append(picked, op)
		}
	}

	return picked
}

// hold scripts one held answer whose value would be value.
func hold[T any](value T) []forgetest.Answer[T] {
	return []forgetest.Answer[T]{{Value: value, Hold: true}}
}

// heldScript scripts every read operation to hold its read until the context ends.
func heldScript() forgetest.Script {
	return forgetest.Script{
		Identity:        hold(identity()),
		FindChanges:     hold(changes()),
		Head:            hold(head()),
		TrunkProtection: hold(protection()),
		Verdict:         hold(verdict()),
		HasTrunkCI:      hold(hasTrunkCI),
		Message:         hold(message()),
	}
}

// ending is a way a read's context ends: after wait, or before the read starts.
type ending struct {
	start func(t *testing.T, wait time.Duration) context.Context
	want  error
	name  string
	// atOnce is whether the context ended before the read starts, so that the read returns at once
	// in place of after wait.
	atOnce bool
}

// endings are the ways a read's context ends: its deadline after wait, a cancellation after wait,
// and a cancellation before the read starts.
func endings() []ending {
	return []ending{
		{name: "deadline", want: context.DeadlineExceeded, start: func(t *testing.T, wait time.Duration) context.Context {
			t.Helper()

			ctx, cancel := context.WithTimeout(t.Context(), wait)
			t.Cleanup(cancel)

			return ctx
		}},
		{name: "cancellation", want: context.Canceled, start: func(t *testing.T, wait time.Duration) context.Context {
			t.Helper()

			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)

			time.AfterFunc(wait, cancel)

			return ctx
		}},
		{name: "already ended", want: context.Canceled, atOnce: true, start: func(t *testing.T, _ time.Duration) context.Context {
			t.Helper()

			return ended(t)
		}},
	}
}

// ended returns a context canceled before it is returned.
func ended(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	return ctx
}

func TestFake_WhenAnswerIsHeld_ReturnsTheContextErrorOnceTheContextEnds(t *testing.T) {
	t.Parallel()

	const wait = 10 * time.Second

	for _, op := range readOps() {
		for _, end := range endings() {
			t.Run(op.name+"/"+end.name, func(t *testing.T) {
				t.Parallel()

				synctest.Test(t, func(t *testing.T) {
					fake := forgetest.New(t, heldScript())
					ctx := end.start(t, wait)
					start := time.Now()

					got, err := op.call(ctx, fake)

					lasted := wait
					if end.atOnce {
						lasted = 0
					}

					assert.Check(t, is.ErrorIs(err, end.want))
					assert.Check(t, is.Equal(time.Since(start), lasted), "time the read lasted")
					assert.Check(t, reflect.ValueOf(got).IsZero(), "value %v, want the zero value", got)
				})
			})
		}
	}
}

func TestFake_WhenAnswerIsHeld_DoesItsBookkeepingBeforeHolding(t *testing.T) {
	t.Parallel()

	var seen []forgetest.Call

	scripted := heldScript()
	scripted.OnRead = func(call forgetest.Call) { seen = append(seen, call) }
	fake := forgetest.New(t, scripted)

	_, err := fake.Head(ended(t), repo(), headQuery())

	assert.Check(t, is.ErrorIs(err, context.Canceled))

	want := []forgetest.Call{{Op: forgetest.OpHead, Repo: repo(), HeadQuery: headQuery()}}
	assert.Check(t, is.DeepEqual(fake.Calls(), want))
	assert.Check(t, is.DeepEqual(seen, want))
}

func TestFake_Head_WhenAnswerIsHeld_TakesOneStep(t *testing.T) {
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

func TestFake_WhenOnLandIsSet_RunsItOnAnUnscriptedLandingToo(t *testing.T) {
	t.Parallel()

	var seen []forgetest.Call

	stub := &recorder{}
	fake := forgetest.New(stub, forgetest.Script{OnLand: func(call forgetest.Call) { seen = append(seen, call) }})

	err := fake.Land(t.Context(), repo(), landing())

	assert.Check(t, is.ErrorIs(err, forgetest.ErrUnscripted))
	assert.Check(t, is.DeepEqual(seen, []forgetest.Call{{Op: forgetest.OpLand, Repo: repo(), Landing: landing()}}))
}
