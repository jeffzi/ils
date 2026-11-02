//go:build unix

package guard

import (
	"maps"
	"slices"

	"mvdan.cc/sh/v3/syntax"
)

// function is what the guard knows of a function the command may have declared under one name:
// each declaration a call of it may run.
type function struct {
	definitions []*definition
}

// definition is one declaration of a function: its body, which the walk reads once where it is
// declared, and what a call of it changes that this read does not show.
type definition struct {
	body *syntax.Stmt
	// redirects are those in effect where the function is declared, which the walk reads every
	// command of its body with.
	redirects []Redirect
	// runs are the dry runs of the body so far, which a later call reuses instead of walking the
	// body again: without them, functions that each call the one before twice cost a walk per call
	// path, two to the power of their count.
	runs []pastRun
	// probing reports whether a dry run of the body is under way, so that a call of the function
	// from inside its own body is not run again.
	probing bool
}

// pastRun is one dry run of a definition's body: the scope it started from at a depth, what it
// left and found, and the definitions its result rests on.
type pastRun struct {
	reads dryRunReads
	marks []Mark
	from  scope
	left  scope
	depth int
}

// dryRunReads is what a dry run's result rests on besides the scope it starts from: the
// definitions it ran, which must not be under way for the result to hold again, and those it found
// under way and so did not run again, which must be.
type dryRunReads struct {
	ran      map[*definition]bool
	underway map[*definition]bool
}

// holds reports whether a dry run that read r would run again as it did, given the definitions
// under way now.
func (r dryRunReads) holds() bool {
	for d := range r.ran {
		if d.probing {
			return false
		}
	}

	for d := range r.underway {
		if !d.probing {
			return false
		}
	}

	return true
}

// dryRuns is what the walkers of one Parse share about dry runs of function bodies: what each run
// under way has read so far, innermost last.
type dryRuns struct {
	stack []dryRunReads
}

// note records in the innermost run under way, if any, that it ran d, whose result rests on reads.
func (runs *dryRuns) note(d *definition, reads dryRunReads) {
	if len(runs.stack) == 0 {
		return
	}

	top := runs.stack[len(runs.stack)-1]
	top.ran[d] = true
	maps.Copy(top.ran, reads.ran)
	maps.Copy(top.underway, reads.underway)
}

// noteUnderway records in the innermost run under way, if any, that it found d under way.
func (runs *dryRuns) noteUnderway(d *definition) {
	if len(runs.stack) > 0 {
		runs.stack[len(runs.stack)-1].underway[d] = true
	}
}

// sameScope reports whether a and b are the same scope to a dry run of a function body.
func sameScope(a, b scope) bool {
	return a.dir == b.dir && a.env == b.env && a.physical == b.physical && a.racing == b.racing &&
		a.allexport == b.allexport && slices.Equal(a.redirects, b.redirects) &&
		maps.Equal(a.functions, b.functions)
}

// with returns the function that runs any of fn's definitions or of defs. A nil fn has none.
func (fn *function) with(defs ...*definition) *function {
	var all []*definition
	if fn != nil {
		all = slices.Clone(fn.definitions)
	}

	for _, d := range defs {
		if !slices.ContainsFunc(all, d.same) {
			all = append(all, d)
		}
	}

	return &function{definitions: all}
}

// same reports whether d and other run the same body with the same redirects.
func (d *definition) same(other *definition) bool {
	return d.body == other.body && slices.Equal(d.redirects, other.redirects)
}

// mergeFunctions returns the functions a call may run when the shell may hold the functions of a or
// those of b: under each name, the definitions of both.
func mergeFunctions(a, b map[string]*function) map[string]*function {
	if maps.Equal(a, b) {
		return a
	}

	merged := maps.Clone(a)
	if merged == nil {
		merged = map[string]*function{}
	}

	for name, fn := range b {
		merged[name] = merged[name].with(fn.definitions...)
	}

	return merged
}

// unmovedDir is the directory a dry run of a function body starts in. No shell word holds a NUL
// byte, so no `cd` reaches it: a body that ends anywhere else may move the shell.
const unmovedDir = "/\x00"

// declareFunction records in sc the function decl declares for the calls after it, and marks a
// declaration that takes the name of a command the guard reads: `cd`, `pushd`, `popd`, or a
// wrapper it sees through.
func (w *walker) declareFunction(decl *syntax.FuncDecl, sc *scope) {
	if decl.Name == nil {
		return
	}

	name := decl.Name.Value
	functions := maps.Clone(sc.functions)
	if functions == nil {
		functions = map[string]*function{}
	}

	functions[name] = &function{definitions: []*definition{{body: decl.Body, redirects: sc.redirects}}}
	sc.functions = functions
	if readsCommand(name) {
		w.keep(w.placed(Invocation{Marks: []Mark{{Kind: ShadowingFunction, Name: name}}}, sc))
	}
}

// calledDefinitions returns the definitions among functions that inv's call may run. Only `time`
// and zsh's modifiers run a function behind them; every other wrapper runs a program or a builtin.
func calledDefinitions(inv Invocation, functions map[string]*function) []*definition {
	if len(inv.Words) == 0 || !inv.Words[0].Literal {
		return nil
	}

	for _, wrapper := range inv.Wrappers {
		if !slices.Contains([]string{"time", "noglob", "nocorrect"}, wrapper) {
			return nil
		}
	}

	fn := functions[inv.Words[0].Text]
	if fn == nil {
		return nil
	}

	return fn.definitions
}

// callMarks marks inv when it calls a function the command declares with redirects other than
// those of a declaration it may run, which the walk of that body does not see. It reads the
// functions of the scope inv was placed from.
func (w *walker) callMarks(inv Invocation) []Mark {
	definitions := calledDefinitions(inv, w.functions)
	elsewhere := func(d *definition) bool { return !slices.Equal(inv.enclosing, d.redirects) }
	if len(definitions) == 0 || (len(inv.Redirects) == 0 && !slices.ContainsFunc(definitions, elsewhere)) {
		return nil
	}

	return []Mark{{Kind: RedirectedFunction, Name: inv.Words[0].Text}}
}

// call changes sc the way inv's call of a function the command declares does, run from any of the
// definitions it may run: the directory after it is unknown when a body may move the shell, and
// allexport, the variables the guard follows, and the functions are as a body leaves them, unknown
// where two bodies leave them differently. A GIT_* variable off the allow list that a body sets
// while allexport may be on is marked where the call stands.
func (w *walker) call(inv Invocation, sc *scope) {
	definitions := calledDefinitions(inv, sc.functions)
	if len(definitions) == 0 {
		return
	}

	after, marks := w.dryRun(definitions[0], *sc)
	for _, d := range definitions[1:] {
		run, found := w.dryRun(d, *sc)
		after = merge(after, run)
		marks = append(marks, found...)
	}

	if after.dir != unmovedDir {
		sc.dir = ""
	}

	sc.physical = sc.physical || after.physical
	sc.allexport = after.allexport
	sc.env = after.env
	sc.functions = after.functions
	if len(marks) > 0 {
		w.keep(w.placed(Invocation{Marks: marks}, sc))
	}
}

// probe returns a walker that reads at w's depth and drops what it finds.
func (w *walker) probe() walker {
	return walker{caller: w.caller, timed: map[*syntax.Stmt]bool{}, depth: w.depth, probing: true, dryRuns: w.runs()}
}

// runs returns the dry runs w shares with the walkers it probes with.
func (w *walker) runs() *dryRuns {
	if w.dryRuns == nil {
		w.dryRuns = &dryRuns{}
	}

	return w.dryRuns
}

// dryRun returns the scope d's body leaves, run from sc in unmovedDir, and the GitVariable marks of
// its invocations. The run is walked apart and dropped: the walk of the body where it is declared
// already holds its invocations, and the errors in reading them. A call of d from inside its own
// body moves to an unknown directory, and may turn allexport on.
//
// A run that starts from the same scope at the same depth as an earlier run of d, while every
// definition that run ran is not under way and every one it found under way still is, leaves what
// the earlier run left, which dryRun returns without walking the body again.
func (w *walker) dryRun(d *definition, sc scope) (scope, []Mark) {
	runs := w.runs()
	if d.probing {
		runs.noteUnderway(d)
		sc.dir, sc.allexport = "", true

		return sc, nil
	}

	sc.dir, sc.physical = unmovedDir, false
	for _, past := range d.runs {
		if past.depth == w.depth && sameScope(past.from, sc) && past.reads.holds() {
			runs.note(d, past.reads)

			return past.left, past.marks
		}
	}

	reads := dryRunReads{ran: map[*definition]bool{}, underway: map[*definition]bool{}}
	runs.stack = append(runs.stack, reads)
	d.probing = true
	sub := w.probe()
	from := sc
	sub.stmt(d.body, &sc)
	d.probing = false
	runs.stack = runs.stack[:len(runs.stack)-1]
	delete(reads.underway, d)

	var marks []Mark
	for _, inv := range sub.walked {
		for _, m := range inv.Marks {
			if m.Kind == GitVariable {
				marks = append(marks, m)
			}
		}
	}

	// The caller appends to the marks it is handed, which must not reach the copy kept here.
	marks = slices.Clip(marks)
	d.runs = append(d.runs, pastRun{from: from, depth: w.depth, left: sc, marks: marks, reads: reads})
	runs.note(d, reads)

	return sc, marks
}
