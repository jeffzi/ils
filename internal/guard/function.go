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
	// probing reports whether a dry run of the body is under way, so that a call of the function
	// from inside its own body is not run again.
	probing bool
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
	_, wrapper := wrapperOptionsOf(name)
	if wrapper || slices.Contains([]string{"cd", "pushd", "popd"}, name) {
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
	return walker{caller: w.caller, timed: map[*syntax.Stmt]bool{}, depth: w.depth, probing: true}
}

// dryRun returns the scope d's body leaves, run from sc in unmovedDir, and the GitVariable marks of
// its invocations. The run is walked apart and dropped: the walk of the body where it is declared
// already holds its invocations, and the errors in reading them. A call of d from inside its own
// body moves to an unknown directory, and may turn allexport on.
func (w *walker) dryRun(d *definition, sc scope) (scope, []Mark) {
	if d.probing {
		sc.dir, sc.allexport = "", true

		return sc, nil
	}

	d.probing = true
	sub := w.probe()
	sc.dir, sc.physical = unmovedDir, false
	sub.stmt(d.body, &sc)
	d.probing = false

	var marks []Mark
	for _, inv := range sub.walked {
		for _, m := range inv.Marks {
			if m.Kind == GitVariable {
				marks = append(marks, m)
			}
		}
	}

	return sc, marks
}
