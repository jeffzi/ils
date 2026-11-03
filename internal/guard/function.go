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

// definition is one declaration of a function: its body, which the walk reads where it is
// declared and again where each call of it stands, and what a call of it changes.
type definition struct {
	body *syntax.Stmt
	// pastRuns are the dry runs of the body so far, which a later call reuses instead of walking the
	// body again: without them, functions that each call the one before twice cost a walk per call
	// path, two to the power of their count.
	pastRuns []pastRun
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
		a.allexport == b.allexport && a.shell == b.shell && slices.Equal(a.redirects, b.redirects) &&
		maps.Equal(a.functions, b.functions) && a.vars.equal(b.vars)
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

// same reports whether d and other run the same body.
func (d *definition) same(other *definition) bool {
	return d.body == other.body
}

// mergeFunctions returns the functions a call may run when the shell may hold the functions of a or
// those of b: under each name, the definitions of both.
func mergeFunctions(a, b map[string]*function) map[string]*function {
	if maps.Equal(a, b) {
		return a
	}

	merged := cloneFunctions(a)
	for name, fn := range b {
		merged[name] = merged[name].with(fn.definitions...)
	}

	return merged
}

// cloneFunctions returns a copy of m, an empty map when m is nil.
func cloneFunctions(m map[string]*function) map[string]*function {
	cloned := maps.Clone(m)
	if cloned == nil {
		cloned = map[string]*function{}
	}

	return cloned
}

// unmovedDir is the directory a dry run of a function body starts in. No shell word holds a NUL
// byte, so no `cd` reaches it: a body that ends anywhere else may move the shell.
const unmovedDir = "/\x00"

// declarationBuiltins are the builtins the parser reads as declarations rather than as calls, so a
// function of the same name runs in their place without the walk of a call seeing it.
func declarationBuiltins() []string {
	return []string{"export", "declare", "local", "readonly", "typeset", "let"}
}

// functionBody walks body, the body of a function, where it is declared. A call runs it with the
// variables of the shell where the call stands, which are not those here, so the walk reads no
// variable the body does not assign itself, and leaves the variables of sc as they were. Nor does
// a switch before the declaration hold in the body, which a call may run once HEAD moved again.
func (w *walker) functionBody(body *syntax.Stmt, sc *scope) {
	outer, head := sc.vars, sc.head
	sc.vars = shellVars{held: outer.held, unread: outer.unread, function: true}
	sc.head = nil
	w.stmt(body, sc)
	sc.vars, sc.head = outer, head
}

// declareFunction records in sc the function decl declares for the calls after it, and marks a
// declaration that takes the name of a command the guard reads: `cd`, `pushd`, `popd`, a wrapper it
// sees through, or a declaration builtin.
func (w *walker) declareFunction(decl *syntax.FuncDecl, sc *scope) {
	if decl.Name == nil {
		return
	}

	name := decl.Name.Value
	functions := cloneFunctions(sc.functions)
	functions[name] = &function{definitions: []*definition{{body: decl.Body}}}
	sc.functions = functions
	if readsCommand(name) || slices.Contains(declarationBuiltins(), name) {
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

// call changes sc the way inv's call of a function the command declares does, run from any of the
// definitions it may run: the directory after it is unknown when a body may move the shell, and
// allexport, the variables the guard follows, and the functions are as a body leaves them, unknown
// where two bodies leave them differently. A shell variable whose value a body may change is
// unknown after it. A GIT_* variable off the allow list that a body sets while allexport may be on
// is marked where the call stands. Each body the call may run is walked from sc first (see
// runBodies). No switch before the call holds after it.
func (w *walker) call(inv Invocation, sc *scope) {
	definitions := calledDefinitions(inv, sc.functions)
	if len(definitions) == 0 {
		return
	}

	w.runBodies(inv, definitions, *sc)
	// A dry run walks no body where a call in it stands, so a HEAD move in one is not between the
	// commands around the call.
	sc.head = nil
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
	sc.vars = sc.vars.merge(after.vars)
	sc.functions = after.functions
	if !slices.ContainsFunc(inv.Redirects, ontoStdout) {
		// An `exec` in a body holds after the call, unless the call redirected stdout: bash then
		// restores the descriptors it redirected (see restored).
		sc.redirects = after.redirects
	}

	if len(marks) > 0 {
		w.keep(w.placed(Invocation{Marks: marks}, sc))
	}
}

// maxCalls is how many calls of functions the command defines one walk follows into the body they
// run. A function that calls itself runs without end, and functions that each call the one before
// twice run two to the power of their count of calls. It also bounds how many bodies of `for`
// loops over a literal list one walk follows, one per value.
const maxCalls = 256

// runBodies walks each of definitions, the bodies inv's call may run, where the call stands: from
// sc, the shell as it is there, behind the redirects of the call, and after any switch of the `&&`
// list the call is in, so each command of a body is judged in the directory, with the environment,
// and behind the redirects and HEAD of the call.
//
// What the walks keep is atCall. A dry run walks no body: its invocations are dropped, and dryRun
// finds what the call changes. A call past maxCalls is marked instead.
func (w *walker) runBodies(inv Invocation, definitions []*definition, sc scope) {
	if w.probing {
		return
	}

	if w.calls == maxCalls {
		w.keep(w.placed(Invocation{Marks: []Mark{{Kind: DeepCall, Name: inv.Words[0].Text}}}, &sc))

		return
	}

	w.calls++
	w.calling++
	sc.redirects = slices.Concat(sc.redirects, inv.Redirects)
	sc.vars.function = true
	for _, d := range definitions {
		body := sc
		w.stmt(declaredBody(d.body), &body)
	}

	w.calling--
}

// declaredBody returns the commands a call runs of body, the body the parser reads for a function
// declaration. The parser reads `f() { …; } && g` as a declaration whose body is the whole list,
// while bash and zsh end the body at its compound command and run the rest of the list once, after
// the declaration: the body a call runs is the first operand of that list.
func declaredBody(body *syntax.Stmt) *syntax.Stmt {
	for {
		list, ok := body.Cmd.(*syntax.BinaryCmd)
		if !ok {
			return body
		}

		body = list.X
	}
}

// probe returns a walker that reads at w's depth and drops what it finds.
func (w *walker) probe() walker {
	sub := newWalker(w.caller, w.depth)
	sub.probing, sub.dryRuns, sub.loopBodies = true, w.dryRuns, w.loopBodies

	return sub
}

// dryRun returns the scope d's body leaves, run from sc in unmovedDir, and the GitVariable marks of
// its invocations. The run is walked apart and dropped: the walk of the body where it is declared
// already holds its invocations, and the errors in reading them. A call of d from inside its own
// body moves to an unknown directory, may turn allexport on, and may change any shell variable.
//
// A run that starts from the same scope at the same depth as an earlier run of d, while every
// definition that run ran is not under way and every one it found under way still is, leaves what
// the earlier run left, which dryRun returns without walking the body again.
func (w *walker) dryRun(d *definition, sc scope) (scope, []Mark) {
	runs := w.dryRuns
	if d.probing {
		runs.noteUnderway(d)
		sc.dir, sc.allexport, sc.vars = "", true, sc.vars.forgetAll()

		return sc, nil
	}

	sc.dir, sc.physical, sc.vars.function = unmovedDir, false, true
	for _, past := range d.pastRuns {
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

	// The caller appends to the marks it is handed, which must not reach the copy kept here.
	marks := slices.Clip(variableMarksOf(sub.walked))
	d.pastRuns = append(d.pastRuns, pastRun{from: from, depth: w.depth, left: sc, marks: marks, reads: reads})
	runs.note(d, reads)

	return sc, marks
}

func variableMarksOf(invocations []Invocation) []Mark {
	var marks []Mark
	for _, inv := range invocations {
		for _, m := range inv.Marks {
			if m.Kind == GitVariable {
				marks = append(marks, m)
			}
		}
	}

	return marks
}
