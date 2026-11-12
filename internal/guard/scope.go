//go:build unix

// cspell:ignore Arithm Arit Assgn -- the shell parser's names for its arithmetic nodes and its
// arithmetic assignment operators.

package guard

import (
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// ---------------------------------------------------------------------------
// Scope state and merging
// ---------------------------------------------------------------------------

// GitTarget is where a git invocation points git: the directory git works in once the
// invocation's own `-C` options apply, and the GIT_DIR and GIT_WORK_TREE it runs with once its own
// `--git-dir` and `--work-tree` apply, each made absolute against that directory as git does.
type GitTarget struct {
	// Dir is empty when the guard cannot follow it.
	Dir string
	// GitDir is empty when GIT_DIR is not set.
	GitDir string
	// WorkTree is empty when GIT_WORK_TREE is not set.
	WorkTree string
	// Unknown reports whether the guard cannot tell what GIT_DIR or GIT_WORK_TREE git runs with;
	// GitDir and WorkTree are then empty.
	Unknown bool
}

// scope is what the shell running a list carries from one command to the next.
type scope struct {
	// dir is the directory commands run in; empty when unknown.
	dir string
	// shell is the program name of the shell the commands run in: one of shellNames, or empty on
	// the command line itself, whose shell the guard cannot tell.
	shell string
	// redirects are the redirects in effect on the shell's own descriptors: those of each compound
	// command the walk is inside, outermost first.
	redirects []Redirect
	// functions holds, by name, the functions a call may run. Copies of a scope share it, so a
	// declaration replaces it rather than changing it in place.
	functions map[string]*function
	// head is the switch an earlier operand of the `&&` list being walked made, which every later
	// operand runs after; nil outside such a list.
	head *headClaim
	vars shellVars
	env  environment
	// physical reports whether the shell's working directory was last set by `cd -P` and no
	// absolute `cd` since, so a symlink may stand in the path dir spells where the shell has the
	// directory it resolves to.
	physical bool
}

func startScope(caller Caller, file *syntax.File) scope {
	sc := scope{dir: caller.Dir, vars: startVars(caller.Env, file)}
	for name, value := range caller.Env {
		sc.env.set(name, variable{value: value, set: true})
	}

	sc.env.forgetCDPath(file)

	return sc
}

// merge returns the scope that either a or b may be: what they agree on, and unknown elsewhere.
func merge(a, b scope) scope {
	if a.dir != b.dir {
		a.dir = ""
	}

	a.physical = a.physical || b.physical
	a.env.gitDir = a.env.gitDir.or(b.env.gitDir)
	a.env.workTree = a.env.workTree.or(b.env.workTree)
	a.env.cdPath = a.env.cdPath.or(b.env.cdPath)
	a.env.ghConfig = a.env.ghConfig.or(b.env.ghConfig)
	a.vars = a.vars.merge(b.vars)
	a.functions = mergeFunctions(a.functions, b.functions)
	if a.head != b.head {
		a.head = nil
	}

	return a
}

// runScope returns the scope a string the invocation inv has run starts in, run from sc: the
// environment inv runs with, and its own redirects in effect after those of the shell. For a string
// a shell of its own runs, the variables the guard follows are none, apart from whether the guard
// may read any of them (unread); script replaces them for eval.
func (sc *scope) runScope(inv Invocation) scope {
	inner := *sc
	inner.env = sc.commandEnv(inv)
	inner.vars = shellVars{unread: sc.vars.unread}
	inner.redirects = slices.Concat(sc.redirects, inv.Redirects)

	return inner
}

// moved returns the directory that going to w from dir leads to, empty when the guard cannot tell.
func moved(dir string, w Word) string {
	if !w.Literal {
		return ""
	}

	return absolute(dir, w.Text)
}

// changedTo returns the directory that git's `-C` w moves to from dir, empty when the guard cannot
// tell. git changes directory with chdir, which follows a symbolic link before a `..` after it steps
// up, so a w holding `..` is resolved on disk, and is unknown when it cannot be. A w without `..`
// keeps its spelling.
func changedTo(dir string, w Word) string {
	to := moved(dir, w)
	if to == "" || !stepsUp(w.Text) {
		return to
	}

	path := w.Text
	if !filepath.IsAbs(path) {
		// Joined as text: filepath.Join would drop each `..` with the name before it, link or not.
		path = dir + "/" + path
	}

	landed, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}

	return landed
}

// absolute returns p made absolute against dir, empty when p is relative and dir is unknown.
func absolute(dir, p string) string {
	switch {
	case filepath.IsAbs(p):
		return filepath.Clean(p)
	case dir == "":
		return ""
	default:
		return filepath.Join(dir, p)
	}
}

// opened returns the path the kernel opens for p from dir, empty when p is relative and dir is
// unknown. Unlike absolute, which follows `cd`, it reads p against the directory dir resolves to,
// so a `..` in p steps up from the target of a symbolic link in dir. A dir that cannot be resolved,
// such as one that does not exist, has p joined to it as written.
func opened(dir, p string) string {
	if filepath.IsAbs(p) || dir == "" {
		return absolute(dir, p)
	}

	if physical, err := filepath.EvalSymlinks(dir); err == nil {
		dir = physical
	}

	return filepath.Join(dir, p)
}

// stepsUp reports whether p has a `..` element; a path without one reaches the same directory
// whether or not links are followed.
func stepsUp(p string) bool {
	return slices.Contains(strings.Split(p, "/"), "..")
}

// ---------------------------------------------------------------------------
// Walking compound commands
// ---------------------------------------------------------------------------

// list walks stmts in order, each one starting where the one before left sc.
func (w *walker) list(stmts []*syntax.Stmt, sc *scope) {
	for _, stmt := range stmts {
		if w.err != nil {
			return
		}

		w.stmt(stmt, sc)
	}
}

// stmt walks one statement. One run in the background runs in a subshell, so what it changes
// stays in it, and beside the commands after it.
func (w *walker) stmt(stmt *syntax.Stmt, sc *scope) {
	if runsAsJob(stmt) {
		inner := *sc
		w.beside++
		w.stmtInPlace(stmt, &inner)
		w.beside--

		return
	}

	w.stmtInPlace(stmt, sc)
}

// runsAsJob reports whether the shell runs stmt beside the commands after it: in the background, or
// as a coprocess.
func runsAsJob(stmt *syntax.Stmt) bool {
	return stmt.Background || stmt.Coprocess
}

// stmtInPlace walks stmt in the shell that runs it. bash opens a compound command's redirects
// before it runs the body, so the substitutions in them start from the shell as it is before the
// body, and every command in the body runs with them.
func (w *walker) stmtInPlace(stmt *syntax.Stmt, sc *scope) {
	if w.simple(stmt, sc) {
		return
	}

	for _, r := range stmt.Redirs {
		w.substitutions(r, sc)
	}

	before := sc.redirects
	sc.redirects = slices.Concat(before, openedIn(redirectsOf(stmt, w.reading(sc)), sc.dir))
	w.compound(stmt.Cmd, sc)
	sc.redirects = before
}

func (w *walker) compound(cmd syntax.Command, sc *scope) {
	switch cmd := cmd.(type) {
	case *syntax.Block:
		w.list(cmd.Stmts, sc)
	case *syntax.Subshell:
		inner := *sc
		w.list(cmd.Stmts, &inner)
	case *syntax.TimeClause:
		if cmd.Stmt != nil {
			w.timed[cmd.Stmt] = true
			w.stmt(cmd.Stmt, sc)
		}
	case *syntax.BinaryCmd:
		w.binary(cmd, sc)
	case *syntax.IfClause:
		w.ifClause(cmd, sc)
	case *syntax.CaseClause:
		w.caseClause(cmd, sc)
	case *syntax.WhileClause:
		w.repeated(sc, func(s *scope) {
			w.list(cmd.Cond, s)
			w.list(cmd.Do, s)
		})
	case *syntax.ForClause:
		w.forClause(cmd, sc)
	case *syntax.FuncDecl:
		w.declareFunction(cmd, sc)
		w.functionBody(cmd.Body, sc)
	default:
		// Arithmetic may assign a variable, which substitutions makes unknown; `[[ … ]]` and the
		// other commands run nothing but the substitutions in them.
		w.substitutions(cmd, sc)
	}
}

// binary walks an `&&` or `||` list, or a pipe.
func (w *walker) binary(cmd *syntax.BinaryCmd, sc *scope) {
	switch cmd.Op {
	case syntax.AndStmt:
		w.andList(cmd, sc)
	case syntax.OrStmt:
		// The right side runs only once the left side failed, which may be before or after any
		// change it makes.
		before := *sc
		w.stmt(cmd.X, sc)
		other := merge(before, *sc)
		w.stmt(cmd.Y, &other)
		*sc = merge(*sc, other)
	default:
		// bash runs each side of a pipe in a subshell, and zsh runs the last one in the shell
		// itself, so a change either side makes may or may not hold after it.
		// The parser nests `a | b | c` to the left, so cmd.Y is a single stage, and the stage before
		// it is cmd.X, or the right side of cmd.X when that is a pipe too.
		before := cmd.X
		if inner, ok := cmd.X.Cmd.(*syntax.BinaryCmd); ok && (inner.Op == syntax.Pipe || inner.Op == syntax.PipeAll) {
			before = inner.Y
		}

		w.pipes[before] = cmd.Y
		left, right := *sc, *sc
		w.beside++
		w.stmt(cmd.X, &left)
		w.stmt(cmd.Y, &right)
		w.beside--
		*sc = merge(merge(*sc, left), right)
	}
}

// andList walks an `&&` list. Each operand runs only once every one before it succeeded, so it
// sees the changes of those before it, and the list may stop after any operand: sc is left as the
// shell may be after any of them. A switch one operand makes holds for the operands after it, and
// not past the list, which may have stopped before it.
func (w *walker) andList(cmd *syntax.BinaryCmd, sc *scope) {
	head := sc.head
	var after []scope
	for _, operand := range andOperands(cmd) {
		at, functions := len(w.kept), sc.functions
		w.stmt(operand, sc)
		after = append(after, *sc)
		w.followSwitch(operand, at, functions, sc)
	}

	merged := after[0]
	for _, s := range after[1:] {
		merged = merge(merged, s)
	}

	merged.head = head
	*sc = merged
}

// andOperands returns the statements an `&&` list joins, in the order they run, reading the
// left-nested lists `a && b && c` parses to as one. A nested list that carries a redirect or runs
// in the background, or whose result is negated, is an operand of its own.
func andOperands(cmd *syntax.BinaryCmd) []*syntax.Stmt {
	operands := []*syntax.Stmt{cmd.Y}
	left := cmd.X
	for {
		inner, ok := left.Cmd.(*syntax.BinaryCmd)
		if !ok || inner.Op != syntax.AndStmt || len(left.Redirs) > 0 ||
			runsAsJob(left) || left.Negated {
			break
		}

		operands = append(operands, inner.Y)
		left = inner.X
	}

	operands = append(operands, left)
	slices.Reverse(operands)

	return operands
}

// ifClause walks an `if`, `elif`, or `else` and the branches after it, leaving sc as any of them
// may.
func (w *walker) ifClause(c *syntax.IfClause, sc *scope) {
	if len(c.Cond) == 0 {
		w.list(c.Then, sc)

		return
	}

	w.list(c.Cond, sc)
	then, other := *sc, *sc
	w.list(c.Then, &then)
	if c.Else != nil {
		w.ifClause(c.Else, &other)
	}

	*sc = merge(then, other)
}

// caseClause walks a `case`, leaving sc as any arm, or none, may.
func (w *walker) caseClause(c *syntax.CaseClause, sc *scope) {
	w.substitutions(c.Word, sc)
	end := *sc
	for _, item := range c.Items {
		for _, pattern := range item.Patterns {
			w.substitutions(pattern, sc)
		}

		arm := *sc
		w.list(item.Stmts, &arm)
		end = merge(end, arm)
	}

	*sc = end
}

// repeated walks a body that runs any number of times, each run starting where the one before
// left the shell. A dry run of the body first finds what it changes, so that the walk starts from
// where any run may start. Inside a dry run, a body is walked once.
func (w *walker) repeated(sc *scope, body func(*scope)) {
	entry := *sc
	if !w.probing {
		w.probing = true
		at := len(w.kept)
		body(&entry)
		w.kept = w.kept[:at]
		w.probing = false
		entry = merge(*sc, entry)
	}

	body(&entry)
	*sc = merge(*sc, entry)
}

// substitutions walks the commands node runs, each in a subshell of its own: command and process
// substitutions, and the statements of a command that holds them. Each starts from sc, and nothing
// it changes holds after it. The variables an arithmetic expression in node assigns, which the
// shell makes itself, become unknown in sc.
func (w *walker) substitutions(node syntax.Node, sc *scope) {
	sc.assignInExpansions(node)
	syntax.Walk(node, func(n syntax.Node) bool {
		var stmts []*syntax.Stmt
		switch n := n.(type) {
		case *syntax.CmdSubst:
			stmts = n.Stmts
		case *syntax.ProcSubst:
			stmts = n.Stmts
		case *syntax.Stmt:
			if syntax.Node(n) == node {
				return true
			}

			stmts = []*syntax.Stmt{n}
		default:
			return w.err == nil
		}

		inner := *sc
		w.list(stmts, &inner)

		return false
	})
}

// placed returns inv with where it runs: its directory, which its own redirects are opened from,
// where it points git, and where a gh invocation's gh reads its config.
func (w *walker) placed(inv Invocation, sc *scope) Invocation {
	inv.Dir = sc.dir
	inv.Redirects = openedIn(inv.Redirects, sc.dir)
	inv.enclosing = sc.redirects
	inv.follows = sc.head
	if inv.Tool == Git {
		inv.Git = readGitCall(inv.Words)
		inv.Target = sc.targetOf(inv)
	}

	if inv.Tool == GH {
		inv.ghConfig = sc.commandEnv(inv).ghConfig
	}

	return inv
}

// ---------------------------------------------------------------------------
// Directory and option tracking
// ---------------------------------------------------------------------------

func (sc *scope) apply(inv Invocation) {
	if len(inv.Words) == 0 {
		// A bare assignment sets a shell variable, which reaches git only once it is exported, as it
		// may already be.
		for _, a := range inv.Assignments {
			sc.env.set(a.Name, variable{unknown: true})
		}

		return
	}

	if !inv.Words[0].Literal || !runsInShell(inv.Wrappers) {
		return
	}

	switch inv.Words[0].Text {
	case "cd", "pushd":
		sc.cd(inv.Words[1:], sc.commandEnv(inv).cdPath)
	case "popd":
		sc.dir = ""
	case "source", ".":
		// The file runs in this shell and may move it, or set or declare any variable.
		sc.dir, sc.env, sc.vars = "", unknownEnvironment(), shellVars{unread: true}
	case "unset":
		sc.assignUnknown(wordTexts(inv.Words[1:]))
	}
}

// cd moves sc.dir the way `cd` or `pushd` given args and run with cdPath as CDPATH does, or
// forgets it when the guard cannot tell where they go: back to an earlier directory, through the
// directory stack, through a CDPATH that may be set, or to a directory that is not a literal.
func (sc *scope) cd(args []Word, cdPath variable) {
	operands, ok := cdOperands(args)
	if !ok || len(operands) != 1 {
		sc.dir = ""

		return
	}

	target := operands[0]
	physical := slices.ContainsFunc(args[:len(args)-len(operands)], func(o Word) bool {
		return strings.ContainsRune(o.Text, 'P')
	})
	switch {
	case !target.Literal,
		target.Text == "" || startsWithSign(target.Text),
		cdPath != (variable{}) && !filepath.IsAbs(target.Text),
		leavesThroughLinks(target.Text, physical, sc.physical):
		sc.dir = ""
	default:
		sc.dir = moved(sc.dir, target)
	}

	switch {
	case physical:
		sc.physical = true
	case filepath.IsAbs(target.Text):
		sc.physical = false
	}
}

const followedCDFlags = "LPe@qs"

// cdOperands returns the operands of `cd` or `pushd` after its options, and false when an option
// is one the guard does not follow.
func cdOperands(args []Word) ([]Word, bool) {
	for i, arg := range args {
		switch {
		case !arg.Literal || len(arg.Text) < 2 || arg.Text[0] != '-':
			return args[i:], true
		case arg.Text == "--":
			return args[i+1:], true
		case strings.Trim(arg.Text[1:], followedCDFlags) != "":
			return nil, false
		}
	}

	return nil, true
}

// leavesThroughLinks reports whether a `cd` to the literal target may land elsewhere than the
// guard's text-only resolution of it, because a symlink on the way is followed: `..` after a
// symlink goes to the link's parent when the `cd` is physical (`-P`), or when it is relative and
// follows a physical one, whose directory the shell holds with the links resolved.
func leavesThroughLinks(target string, physical, afterPhysical bool) bool {
	if !stepsUp(target) {
		return false
	}

	return physical || (afterPhysical && !filepath.IsAbs(target))
}

func (sc *scope) targetOf(inv Invocation) GitTarget {
	if inv.Git.missing != "" {
		return GitTarget{Unknown: true}
	}

	env := sc.commandEnv(inv)
	dir := sc.dir
	for _, o := range inv.Git.Options {
		switch o.Name {
		case "-C":
			dir = changedTo(dir, o.Value)
		case "--git-dir":
			env.gitDir = valueOf(o.Value)
		case "--work-tree":
			env.workTree = valueOf(o.Value)
		}
	}

	// git reads a relative GIT_DIR or GIT_WORK_TREE against the directory it is in once every `-C`
	// has applied, wherever the option that set it stands.
	gitDir, knownDir := env.gitDir.resolve(dir)
	workTree, knownTree := env.workTree.resolve(dir)
	if !knownDir || !knownTree {
		return GitTarget{Dir: dir, Unknown: true}
	}

	return GitTarget{Dir: dir, GitDir: gitDir, WorkTree: workTree}
}

// keep records inv as walked, and as the next invocation Parse returns, atCall inside a walk of a
// function body where a call of it stands.
func (w *walker) keep(inv Invocation) {
	inv.atCall = inv.atCall || w.calling > 0
	inv.beside = inv.beside || w.beside > 0
	w.kept = append(w.kept, len(w.walked))
	w.walked = append(w.walked, inv)
}

// ---------------------------------------------------------------------------
// Loops and variable assignment
// ---------------------------------------------------------------------------

// forClause walks a `for` loop. When the walk reads a value for each word of the loop's list (see
// loopValues), it walks the body once per value, the variable holding it as a literal assignment
// would leave it; otherwise the variable is unknown in the body. After the loop the variable is
// unknown either way, since a `break` may end the loop before its last value.
func (w *walker) forClause(c *syntax.ForClause, sc *scope) {
	iter, _ := c.Loop.(*syntax.WordIter)
	values, each := w.loopValues(iter, sc)
	if iter != nil {
		sc.assignUnknown([]string{iter.Name.Value})
	}

	w.substitutions(c.Loop, sc)
	w.repeated(sc, func(s *scope) {
		if !each {
			w.list(c.Do, s)

			return
		}

		for _, value := range values {
			s.vars = s.vars.assigned(&syntax.Assign{Name: iter.Name}, Word{Text: value, Literal: true})
			w.list(c.Do, s)
		}
	})
}

// loopValues returns the values iter, the list of a `for` loop, gives its variable, read where the
// loop starts in sc, and reports whether the walk follows the body once for each: every word of
// the list reads as a literal, and the walk has bodies left for all of them, which it then
// spends. Outside a dry run, repeated walks the body twice for each value, a dry run and then the
// walk that keeps what it finds, so each value spends two bodies there. A loop with no list runs
// over the positional parameters, which the guard cannot read.
func (w *walker) loopValues(iter *syntax.WordIter, sc *scope) ([]string, bool) {
	if iter == nil || !iter.InPos.IsValid() {
		return nil, false
	}

	bodies := len(iter.Items)
	if !w.probing {
		bodies *= 2
	}

	if *w.loopBodies+bodies > maxCalls {
		return nil, false
	}

	ex := w.reading(sc)
	values := make([]string, 0, len(iter.Items))
	for _, item := range iter.Items {
		word := readWord(item, ex)
		if !word.Literal {
			return nil, false
		}

		values = append(values, word.Text)
	}

	*w.loopBodies += bodies

	return values, true
}

// assignUnknown makes each of names a variable the guard cannot tell the value of in sc's
// environment, for variables the shell assigns without an assignment.
func (sc *scope) assignUnknown(names []string) {
	for _, name := range names {
		sc.env.set(name, variable{unknown: true})
	}

	sc.vars = sc.vars.forget(names...)
}

// assignInExpansions makes unknown in sc each variable node assigns in an expansion the shell
// makes itself: a `${NAME:=word}` or `${NAME=word}`, and an arithmetic expression, as in `((n =
// 1))`, `let n++`, or `$((n += 2))`. A command or process substitution, and a statement other than
// node, run in a shell of their own or are walked apart, so what they assign is left out. A simple
// command that is not a declaration assigns its own defaults.
func (sc *scope) assignInExpansions(node syntax.Node) {
	readsDefaults := true
	if stmt, ok := node.(*syntax.Stmt); ok {
		_, simple := stmt.Cmd.(*syntax.CallExpr)
		readsDefaults = !simple
	}

	var defaults, arithmetic []string
	syntax.Walk(node, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			return false
		case *syntax.Stmt:
			return syntax.Node(n) == node
		case *syntax.ParamExp:
			if readsDefaults && assignsDefault(n) {
				defaults = append(defaults, n.Param.Value)
			}
		}

		if name, ok := arithmeticTarget(n); ok {
			arithmetic = append(arithmetic, name)
		}

		return true
	})

	sc.assignUnknown(defaults)
	sc.vars = sc.vars.forget(arithmetic...)
}

// arithmeticTarget returns the variable n assigns when it is an arithmetic assignment, increment,
// or decrement of a variable, and false for any other node.
func arithmeticTarget(n syntax.Node) (string, bool) {
	var target syntax.ArithmExpr
	switch n := n.(type) {
	case *syntax.BinaryArithm:
		if assignsArithmetic(n.Op) {
			target = n.X
		}
	case *syntax.UnaryArithm:
		if n.Op == syntax.Inc || n.Op == syntax.Dec {
			target = n.X
		}
	}

	w, ok := target.(*syntax.Word)
	if !ok {
		return "", false
	}

	name, _, _ := strings.Cut(w.Lit(), "[")

	return name, true
}

func assignsArithmetic(op syntax.BinAritOperator) bool {
	switch op {
	case syntax.Assgn, syntax.AddAssgn, syntax.SubAssgn, syntax.MulAssgn, syntax.QuoAssgn,
		syntax.RemAssgn, syntax.AndAssgn, syntax.OrAssgn, syntax.XorAssgn, syntax.ShlAssgn,
		syntax.ShrAssgn, syntax.AndBoolAssgn, syntax.OrBoolAssgn, syntax.XorBoolAssgn, syntax.PowAssgn:
		return true
	default:
		return false
	}
}

// assignsDefault reports whether p is a `${NAME:=word}` or `${NAME=word}`, which assigns NAME.
func assignsDefault(p *syntax.ParamExp) bool {
	return p.Param != nil && p.Exp != nil && (p.Exp.Op == syntax.AssignUnset || p.Exp.Op == syntax.AssignUnsetOrNull)
}
