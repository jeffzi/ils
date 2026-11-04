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
		if !slices.ContainsFunc(all, func(other *definition) bool { return other.body == d.body }) {
			all = append(all, d)
		}
	}

	return &function{definitions: all}
}

// mergeFunctions returns the functions a call may run when the shell may hold the functions of a or
// those of b: under each name, the definitions of both.
func mergeFunctions(a, b map[string]*function) map[string]*function {
	if maps.Equal(a, b) {
		return a
	}

	merged := cloneOrEmpty(a)
	for name, fn := range b {
		merged[name] = merged[name].with(fn.definitions...)
	}

	return merged
}

// unmovedDir is the directory a dry run of a function body starts in. No shell word holds a NUL
// byte, so no `cd` reaches it: a body that ends anywhere else may move the shell.
const unmovedDir = "/\x00"

// functionBody walks body, the body of a function, where it is declared, as a body that runs any
// number of times. A call runs it with the variables of the shell where the call stands, which are
// not those here, so the walk reads no variable the body does not assign itself. Nor does a switch
// before the declaration hold in the body, which a call may run once HEAD moved again.
//
// The declaration runs nothing, and each call walks the body again where it stands (see call), so
// sc is left as it was. The parser reads `f() { …; } && g` as a declaration whose body is the whole
// list, while the rest of the list runs once, after the declaration, in the shell itself: the walk
// cannot keep the changes of the rest apart from those of the body, so for such a declaration sc
// is left as the shell may be after any number of runs of the whole list, the variables of sc
// aside.
func (w *walker) functionBody(body *syntax.Stmt, sc *scope) {
	declared := *sc
	w.repeated(&declared, func(s *scope) {
		outer, head := s.vars, s.head
		s.vars = shellVars{held: outer.held, unread: outer.unread, function: true}
		s.head = nil
		w.stmt(body, s)
		s.vars, s.head = outer, head
	})

	if declaredBody(body) != body {
		*sc = declared
	}
}

// declareFunction records in sc the function decl declares for the calls after it.
func (w *walker) declareFunction(decl *syntax.FuncDecl, sc *scope) {
	if decl.Name == nil {
		return
	}

	name := decl.Name.Value
	functions := cloneOrEmpty(sc.functions)
	functions[name] = &function{definitions: []*definition{{body: decl.Body}}}
	sc.functions = functions
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
// the variables the guard follows and the functions are as a body leaves them, unknown where two
// bodies leave them differently. A shell variable whose value a body may change is unknown after
// it. Each GIT_* variable off the allow list that a body sets, and each call a dry run of a body
// cannot follow past maxCalls, is marked where the call stands. Each body the call may run is
// walked from sc first (see runBodies). No switch before the call holds after it.
func (w *walker) call(inv Invocation, sc *scope) {
	definitions := calledDefinitions(inv, sc.functions)
	if len(definitions) == 0 {
		return
	}

	w.runBodies(inv, definitions, *sc)
	// A dry run walks no body where a call in it stands, so a HEAD move in one is not between the
	// commands around the call.
	sc.head = nil
	name := inv.Words[0].Text
	after, marks := w.dryRun(definitions[0], *sc, name)
	for _, d := range definitions[1:] {
		run, found := w.dryRun(d, *sc, name)
		after = merge(after, run)
		marks = append(marks, found...)
	}

	if after.dir != unmovedDir {
		sc.dir = ""
	}

	sc.physical = sc.physical || after.physical
	sc.env = after.env
	sc.vars = sc.vars.merge(after.vars)
	sc.functions = after.functions

	if len(marks) > 0 {
		w.keep(w.placed(Invocation{Marks: marks}, sc))
	}
}

// maxCalls is how many walks of the bodies of functions the command defines one Parse makes, dry
// runs included. A function that calls itself runs without end, and functions that each call the
// one before twice run two to the power of their count of calls. It also bounds how many bodies of
// `for` loops over a literal list one Parse follows, one per value.
const maxCalls = 256

// runBodies walks each of definitions, the bodies inv's call may run, where the call stands: from
// sc, the shell as it is there, behind the redirects of the call, and after any switch of the `&&`
// list the call is in, so each command of a body is judged in the directory, with the environment,
// and behind the redirects and HEAD of the call.
//
// What the walks keep is atCall. A dry run walks no body: its invocations are dropped, and dryRun
// finds what the call changes. A call whose walks would take the Parse past maxCalls is marked
// instead.
func (w *walker) runBodies(inv Invocation, definitions []*definition, sc scope) {
	if w.probing {
		return
	}

	if *w.bodies+len(definitions) > maxCalls {
		w.keep(w.placed(Invocation{Marks: []Mark{{Kind: DeepCall, Name: inv.Words[0].Text}}}, &sc))

		return
	}

	*w.bodies += len(definitions)
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
	sub.probing, sub.loopBodies, sub.bodies = true, w.loopBodies, w.bodies

	return sub
}

// dryRun returns the scope d's body leaves, run from sc in unmovedDir, and the carriedMarks of
// its invocations. The run is walked apart and dropped: the walk of the body where it is declared
// already holds its invocations, and the errors in reading them. A call of d from inside its own
// body moves to an unknown directory and may change any shell variable, and so does one past the
// maxCalls walks of the Parse, which is marked as a DeepCall of name, the function called.
func (w *walker) dryRun(d *definition, sc scope, name string) (scope, []Mark) {
	if d.probing || *w.bodies == maxCalls {
		sc.dir, sc.vars = "", sc.vars.forgetAll()
		if d.probing {
			return sc, nil
		}

		return sc, []Mark{{Kind: DeepCall, Name: name}}
	}

	*w.bodies++
	sc.dir, sc.physical, sc.vars.function = unmovedDir, false, true
	d.probing = true
	sub := w.probe()
	sub.stmt(d.body, &sc)
	d.probing = false

	return sc, carriedMarks(sub.walked)
}

// carriedMarks returns the marks of invocations that a dry run carries to the call it runs for:
// each GIT_* variable off the allow list a body sets, and each call past maxCalls.
func carriedMarks(invocations []Invocation) []Mark {
	var marks []Mark
	for _, inv := range invocations {
		for _, m := range inv.Marks {
			if m.Kind == GitVariable || m.Kind == DeepCall {
				marks = append(marks, m)
			}
		}
	}

	return marks
}
