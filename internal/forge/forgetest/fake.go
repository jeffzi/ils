// Package forgetest provides a scripted forge for the tests of packages that talk to one.
package forgetest

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jeffzi/ils/internal/forge"
)

// TB is the part of testing.TB the fake reports through.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
}

// Op names an operation of the forge.
type Op string

// The operations of the forge.
const (
	OpIdentity        Op = "Identity"
	OpFindChanges     Op = "FindChanges"
	OpOpenChange      Op = "OpenChange"
	OpHead            Op = "Head"
	OpTrunkProtection Op = "TrunkProtection"
	OpVerdict         Op = "Verdict"
	OpHasTrunkCI      Op = "HasTrunkCI"
	OpMessage         Op = "Message"
	OpLand            Op = "Land"
	OpDeleteBranch    Op = "DeleteBranch"
)

// ErrUnscripted is returned for a call the script has no answer for.
var ErrUnscripted = errors.New("forgetest: call not in the script")

// Answer is one scripted step of an operation: Err when it is set, Value otherwise.
//
// A held answer gives neither: the call returns only once its context has ended, with an
// error that wraps the context's error, so callers can match it with errors.Is against
// context.DeadlineExceeded or context.Canceled.
type Answer[T any] struct {
	Value T
	Err   error
	Hold  bool
}

// Script states the answers the fake gives.
//
// Each operation holds a sequence that advances one step per call, and whose last step
// repeats on further calls. A call to an operation with no step fails the test. Land and
// DeleteBranch answer only an error: a nil step is a call that succeeds.
type Script struct {
	// OnRead, when set, runs on each call that reads from the forge, which is every
	// operation but OpenChange, Land and DeleteBranch, before the call answers.
	OnRead func(Call)
	// OnLand, when set, runs on each Land call, with the landing it was given, before the
	// call answers.
	OnLand          func(Call)
	Identity        []Answer[forge.Repo]
	FindChanges     []Answer[[]forge.Change]
	OpenChange      []Answer[forge.ChangeRef]
	Head            []Answer[forge.Head]
	TrunkProtection []Answer[forge.Protection]
	Verdict         []Answer[forge.Verdict]
	HasTrunkCI      []Answer[bool]
	Message         []Answer[forge.Message]
	Land            []error
	DeleteBranch    []error
}

// Call is one recorded call with the arguments that its operation takes.
type Call struct {
	Op Op
	// Branch is the branch of FindChanges and DeleteBranch, and the trunk of TrunkProtection
	// and HasTrunkCI.
	Branch       string
	Change       forge.NewChange
	Repo         forge.Repo
	Landing      forge.Landing
	HeadQuery    forge.HeadQuery
	VerdictQuery forge.VerdictQuery
	// Number is the change of Message.
	Number int
}

// Fake is a landing.Forge that answers from a script and records its calls.
// It is not safe for concurrent use.
type Fake struct {
	tb     TB
	steps  map[Op]int
	script Script
	calls  []Call
}

// New returns a fake that answers from script and reports unexpected calls to tb.
func New(tb TB, script Script) *Fake {
	return &Fake{tb: tb, script: script, steps: map[Op]int{}}
}

// Calls returns every call made so far, in order.
func (f *Fake) Calls() []Call {
	return slices.Clone(f.calls)
}

// Identity answers from the script's Identity sequence.
func (f *Fake) Identity(ctx context.Context, repo forge.Repo) (forge.Repo, error) {
	f.tb.Helper()

	return answer(ctx, f, f.script.Identity, Call{Op: OpIdentity, Repo: repo})
}

// FindChanges answers from the script's FindChanges sequence.
func (f *Fake) FindChanges(ctx context.Context, repo forge.Repo, branch string) ([]forge.Change, error) {
	f.tb.Helper()

	return answer(ctx, f, f.script.FindChanges, Call{Op: OpFindChanges, Repo: repo, Branch: branch})
}

// OpenChange answers from the script's OpenChange sequence.
func (f *Fake) OpenChange(ctx context.Context, repo forge.Repo, change forge.NewChange) (forge.ChangeRef, error) {
	f.tb.Helper()

	return answer(ctx, f, f.script.OpenChange, Call{Op: OpOpenChange, Repo: repo, Change: change})
}

// Head answers from the script's Head sequence.
func (f *Fake) Head(ctx context.Context, repo forge.Repo, query forge.HeadQuery) (forge.Head, error) {
	f.tb.Helper()

	return answer(ctx, f, f.script.Head, Call{Op: OpHead, Repo: repo, HeadQuery: query})
}

// TrunkProtection answers from the script's TrunkProtection sequence.
func (f *Fake) TrunkProtection(ctx context.Context, repo forge.Repo, trunk string) (forge.Protection, error) {
	f.tb.Helper()

	return answer(ctx, f, f.script.TrunkProtection, Call{Op: OpTrunkProtection, Repo: repo, Branch: trunk})
}

// Verdict answers from the script's Verdict sequence.
func (f *Fake) Verdict(ctx context.Context, repo forge.Repo, query forge.VerdictQuery) (forge.Verdict, error) {
	f.tb.Helper()

	return answer(ctx, f, f.script.Verdict, Call{Op: OpVerdict, Repo: repo, VerdictQuery: query})
}

// HasTrunkCI answers from the script's HasTrunkCI sequence.
func (f *Fake) HasTrunkCI(ctx context.Context, repo forge.Repo, trunk string) (bool, error) {
	f.tb.Helper()

	return answer(ctx, f, f.script.HasTrunkCI, Call{Op: OpHasTrunkCI, Repo: repo, Branch: trunk})
}

// Message answers from the script's Message sequence.
func (f *Fake) Message(ctx context.Context, repo forge.Repo, number int) (forge.Message, error) {
	f.tb.Helper()

	return answer(ctx, f, f.script.Message, Call{Op: OpMessage, Repo: repo, Number: number})
}

// Land runs the script's OnLand hook, then answers from its Land sequence.
func (f *Fake) Land(ctx context.Context, repo forge.Repo, landing forge.Landing) error {
	f.tb.Helper()

	return answerError(ctx, f, f.script.Land, Call{Op: OpLand, Repo: repo, Landing: landing})
}

// DeleteBranch answers from the script's DeleteBranch sequence.
func (f *Fake) DeleteBranch(ctx context.Context, repo forge.Repo, branch string) error {
	f.tb.Helper()

	return answerError(ctx, f, f.script.DeleteBranch, Call{Op: OpDeleteBranch, Repo: repo, Branch: branch})
}

func answerError(ctx context.Context, f *Fake, sequence []error, call Call) error {
	f.tb.Helper()

	answers := make([]Answer[struct{}], len(sequence))
	for i, err := range sequence {
		answers[i].Err = err
	}

	_, err := answer(ctx, f, answers, call)

	return err
}

func answer[T any](ctx context.Context, f *Fake, sequence []Answer[T], call Call) (T, error) {
	f.tb.Helper()

	var zero T

	f.calls = append(f.calls, call)

	if reads(call.Op) && f.script.OnRead != nil {
		f.script.OnRead(call)
	}

	if call.Op == OpLand && f.script.OnLand != nil {
		f.script.OnLand(call)
	}

	if len(sequence) == 0 {
		f.tb.Errorf("forgetest: unexpected call %+v: the script has no answer for %s", call, call.Op)

		return zero, ErrUnscripted
	}

	index := min(f.steps[call.Op], len(sequence)-1)
	f.steps[call.Op] = index + 1
	step := sequence[index]

	if step.Hold {
		<-ctx.Done()

		return zero, fmt.Errorf("forgetest: held %s: %w", call.Op, ctx.Err())
	}

	if step.Err != nil {
		return zero, step.Err
	}

	return step.Value, nil
}

// reads reports whether op only reads what the forge holds.
func reads(op Op) bool {
	switch op {
	case OpOpenChange, OpLand, OpDeleteBranch:
		return false
	default:
		return true
	}
}
