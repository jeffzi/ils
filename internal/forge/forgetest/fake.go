// Package forgetest provides a scripted forge for the tests of packages that talk to one.
package forgetest

import (
	"context"
	"errors"

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
)

// ErrUnscripted is returned for a call the script has no answer for.
var ErrUnscripted = errors.New("forgetest: call not in the script")

// Answer is one scripted step of an operation: Err when it is set, Value otherwise.
type Answer[T any] struct {
	Value T
	Err   error
}

// Script states the answers the fake gives.
//
// Each operation holds a sequence that advances one step per call, and whose last step
// repeats on further calls. A call to an operation with no step fails the test.
type Script struct {
	// OnRead, when set, runs on each call that reads from the forge, which is every
	// operation but OpenChange, before the call answers.
	OnRead          func(Call)
	Identity        []Answer[forge.Repo]
	FindChanges     []Answer[[]forge.Change]
	OpenChange      []Answer[forge.ChangeRef]
	Head            []Answer[forge.Head]
	TrunkProtection []Answer[forge.Protection]
	Verdict         []Answer[forge.Verdict]
}

// Call is one recorded call with the arguments that its operation takes.
type Call struct {
	Op Op
	// Branch is the branch of FindChanges and the trunk of TrunkProtection.
	Branch string
	Change forge.NewChange
	Repo   forge.Repo
	// Head is the query of Head.
	Head forge.HeadQuery
	// Query is the query of Verdict.
	Query forge.VerdictQuery
}

// Fake is a forge.Forge that answers from a script and records its calls.
// It is not safe for concurrent use.
type Fake struct {
	tb     TB
	steps  map[Op]int
	script Script
	calls  []Call
}

var _ forge.Forge = (*Fake)(nil)

// New returns a fake that answers from script and reports unexpected calls to tb.
func New(tb TB, script Script) *Fake {
	return &Fake{tb: tb, script: script, steps: map[Op]int{}}
}

// Calls returns every call made so far, in order.
func (f *Fake) Calls() []Call {
	return append([]Call(nil), f.calls...)
}

// Identity answers from the script's Identity sequence.
func (f *Fake) Identity(_ context.Context, repo forge.Repo) (forge.Repo, error) {
	f.tb.Helper()

	return answer(f, f.script.Identity, Call{Op: OpIdentity, Repo: repo})
}

// FindChanges answers from the script's FindChanges sequence.
func (f *Fake) FindChanges(_ context.Context, repo forge.Repo, branch string) ([]forge.Change, error) {
	f.tb.Helper()

	return answer(f, f.script.FindChanges, Call{Op: OpFindChanges, Repo: repo, Branch: branch})
}

// OpenChange answers from the script's OpenChange sequence.
func (f *Fake) OpenChange(_ context.Context, repo forge.Repo, change forge.NewChange) (forge.ChangeRef, error) {
	f.tb.Helper()

	return answer(f, f.script.OpenChange, Call{Op: OpOpenChange, Repo: repo, Change: change})
}

// Head answers from the script's Head sequence.
func (f *Fake) Head(_ context.Context, repo forge.Repo, query forge.HeadQuery) (forge.Head, error) {
	f.tb.Helper()

	return answer(f, f.script.Head, Call{Op: OpHead, Repo: repo, Head: query})
}

// TrunkProtection answers from the script's TrunkProtection sequence.
func (f *Fake) TrunkProtection(_ context.Context, repo forge.Repo, trunk string) (forge.Protection, error) {
	f.tb.Helper()

	return answer(f, f.script.TrunkProtection, Call{Op: OpTrunkProtection, Repo: repo, Branch: trunk})
}

// Verdict answers from the script's Verdict sequence.
func (f *Fake) Verdict(_ context.Context, repo forge.Repo, query forge.VerdictQuery) (forge.Verdict, error) {
	f.tb.Helper()

	return answer(f, f.script.Verdict, Call{Op: OpVerdict, Repo: repo, Query: query})
}

func answer[T any](f *Fake, sequence []Answer[T], call Call) (T, error) {
	f.tb.Helper()

	var zero T

	f.calls = append(f.calls, call)

	if call.Op != OpOpenChange && f.script.OnRead != nil {
		f.script.OnRead(call)
	}

	if len(sequence) == 0 {
		f.tb.Errorf("forgetest: unexpected call %+v: the script has no answer for %s", call, call.Op)

		return zero, ErrUnscripted
	}

	step := min(f.steps[call.Op], len(sequence)-1)
	f.steps[call.Op] = step + 1

	if err := sequence[step].Err; err != nil {
		return zero, err
	}

	return sequence[step].Value, nil
}
