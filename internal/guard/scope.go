//go:build unix

package guard

import (
	"maps"
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
	// command the walk is inside, outermost first, then those an `exec` with no command made.
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
	// racing reports whether the walk is inside a background job or a stage of a pipe, which runs
	// beside the commands around it.
	racing bool
	// allexport reports whether `set -a` may be in effect, under which the shell exports every
	// variable it assigns.
	allexport bool
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
	a.allexport = a.allexport || b.allexport
	a.env.gitDir = a.env.gitDir.or(b.env.gitDir)
	a.env.workTree = a.env.workTree.or(b.env.workTree)
	a.env.cdPath = a.env.cdPath.or(b.env.cdPath)
	a.env.ghConfig = a.env.ghConfig.or(b.env.ghConfig)
	a.vars = a.vars.merge(b.vars)
	a.functions = mergeFunctions(a.functions, b.functions)
	a.redirects = mergeRedirects(a.redirects, b.redirects)
	if a.head != b.head {
		a.head = nil
	}

	return a
}

// mergeRedirects returns the redirects in effect when either a or b may be. The shell is then in
// a's state or b's, so a descriptor is on a file exactly when a or b leaves it on one: an `exec` on
// either side holds. Both begin with the redirects they share, which the result keeps in front,
// followed by a's own, then b's own, then the redirects that settle each descriptor the order of
// the two left on the wrong file, or on none: where both leave it on a file, on a's.
func mergeRedirects(a, b []Redirect) []Redirect {
	if slices.Equal(a, b) {
		return a
	}

	shared := 0
	for shared < min(len(a), len(b)) && a[shared] == b[shared] {
		shared++
	}

	merged := slices.Concat(a, b[shared:])
	wanted := filesOf(b)
	maps.Copy(wanted, filesOf(a))

	return slices.Concat(merged, settled(filesOf(merged), wanted))
}

// moved returns the directory that going to w from dir leads to, empty when the guard cannot tell.
func moved(dir string, w Word) string {
	if !w.Literal {
		return ""
	}

	return absolute(dir, w.Text)
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

// ---------------------------------------------------------------------------
// Walking compound commands
// ---------------------------------------------------------------------------

// list walks stmts in order, each one starting where the one before left sc. A background job and
// the statements after it are walked by job.
func (w *walker) list(stmts []*syntax.Stmt, sc *scope) {
	for i, stmt := range stmts {
		if w.err != nil {
			return
		}

		if runsAsJob(stmt) {
			w.job(stmt, stmts[i+1:], sc)

			return
		}

		w.stmt(stmt, sc)
	}
}

// stmt walks one statement. One run in the background runs in a subshell, so what it changes
// stays in it. bash opens a compound command's redirects before it runs the body, so the
// substitutions in them start from the shell as it is before the body, and every command in the
// body runs with them.
func (w *walker) stmt(stmt *syntax.Stmt, sc *scope) {
	if runsAsJob(stmt) {
		inner := *sc
		w.race(&inner, func(s *scope) { w.stmtInPlace(stmt, s) })

		return
	}

	w.stmtInPlace(stmt, sc)
}

// runsAsJob reports whether the shell runs stmt beside the commands after it: in the background, or
// as a coprocess.
func runsAsJob(stmt *syntax.Stmt) bool {
	return stmt.Background || stmt.Coprocess
}

// stmtInPlace walks stmt as the shell runs it once it has placed any background job.
func (w *walker) stmtInPlace(stmt *syntax.Stmt, sc *scope) {
	if w.simple(stmt, sc) {
		return
	}

	for _, r := range stmt.Redirs {
		w.substitutions(r, sc)
	}

	own := redirectsOf(stmt, w.reading(sc))
	before := sc.redirects
	sc.redirects = slices.Concat(before, own)
	w.compound(stmt.Cmd, sc)
	sc.redirects = restored(before, own, sc.redirects)
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
		w.race(sc, func(s *scope) {
			left, right := *s, *s
			w.stmt(cmd.X, &left)
			w.stmt(cmd.Y, &right)
			*s = merge(merge(*s, left), right)
		})
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
	w.exportedDefaults(node, sc)
	sc.vars = sc.vars.forget(arithmeticNames(node)...)
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

// placed returns inv with where it runs: its directory, where it points git, and where a gh
// invocation's gh reads its config.
func (w *walker) placed(inv Invocation, sc *scope) Invocation {
	inv.Dir = sc.dir
	inv.enclosing = sc.redirects
	inv.follows, inv.racing = sc.head, sc.racing
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

	if execsNothing(inv.Words) {
		sc.redirects = slices.Concat(sc.redirects, inv.Redirects)

		return
	}

	if !inv.Words[0].Literal || !runsInShell(inv.Wrappers) {
		return
	}

	switch inv.Words[0].Text {
	case "set":
		sc.allexport = allexportAfter(sc.shell, inv.Words[1:], sc.allexport)
	case "setopt", "unsetopt":
		sc.allexport = allexportAfterZshOptions(inv.Words[0].Text == "setopt", inv.Words[1:], sc.allexport)
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

// execsNothing reports whether words, the words of a command, are an `exec` with nothing after its
// options, reached through any of the wrappers that run a builtin: one that applies its redirects
// to the shell itself.
func execsNothing(words []Word) bool {
	i := 0
	for i < len(words) && words[i].Literal && (words[i].Text == "command" || words[i].Text == "builtin") {
		i = pastOptions(words, i)
	}

	if i == len(words) || !words[i].Literal || words[i].Text != "exec" {
		return false
	}

	return pastOptions(words, i) == len(words)
}

// pastOptions returns the index of the word after the options of the wrapper at index i of words.
func pastOptions(words []Word, i int) int {
	options, _ := wrapperOptionsOf(words[i].Text)
	n, _ := skipOptions(words[i+1:], options)

	return i + 1 + n
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
// follows a physical one, whose directory the shell holds with the links resolved. A target
// without `..` reaches the same directory either way.
func leavesThroughLinks(target string, physical, afterPhysical bool) bool {
	if !slices.Contains(strings.Split(target, "/"), "..") {
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
			dir = moved(dir, o.Value)
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
	w.kept = append(w.kept, len(w.walked))
	w.walked = append(w.walked, inv)
}

// ---------------------------------------------------------------------------
// GIT_* variable marks
// ---------------------------------------------------------------------------

// allowedGitVariables is the list of GIT_* variables a command may set: those that point git at a
// repository, which the guard follows, the GIT_* variables among commandVariables, whose scripts it
// reads, and those that change no rule it judges by.
func allowedGitVariables() []string {
	followed := slices.DeleteFunc(commandVariables(), func(name string) bool { return !strings.HasPrefix(name, "GIT_") })

	return append(followed,
		gitDirVar, gitWorkTreeVar,
		"GIT_TERMINAL_PROMPT", "GIT_TRACE", "GIT_TRACE2", "GIT_ADVICE", "GIT_OPTIONAL_LOCKS",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE",
	)
}

// gitVariableMarks returns a GitVariable mark for each of the variable names that is a GIT_*
// variable off the allow list.
func gitVariableMarks(names []string) []Mark {
	allowed := allowedGitVariables()
	var marks []Mark
	for _, name := range names {
		if strings.HasPrefix(name, "GIT_") && !slices.Contains(allowed, name) {
			marks = append(marks, Mark{Kind: GitVariable, Name: name})
		}
	}

	return marks
}

// forClause walks a `for` loop. When the walk reads a value for each word of the loop's list (see
// loopValues), it walks the body once per value, the variable holding it as a literal assignment
// would leave it; otherwise the variable is unknown in the body. After the loop the variable is
// unknown either way, since a `break` may end the loop before its last value.
func (w *walker) forClause(c *syntax.ForClause, sc *scope) {
	iter, _ := c.Loop.(*syntax.WordIter)
	values, each := w.loopValues(iter, sc)
	w.loopVariable(iter, sc)
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

// loopVariable makes the variable of the `for` loop over iter unknown in sc, whatever it is, and
// marks the loop when that variable is a GIT_* variable off the allow list while allexport may be
// on, which exports it to every git after it. A nil iter, a loop that is not over words, has none.
func (w *walker) loopVariable(iter *syntax.WordIter, sc *scope) {
	if iter == nil {
		return
	}

	w.assignUnknownExported([]string{iter.Name.Value}, sc)
}

// assignUnknown makes each of names a variable the guard cannot tell the value of in sc's
// environment, for variables the shell assigns without an assignment.
func (sc *scope) assignUnknown(names []string) {
	for _, name := range names {
		sc.env.set(name, variable{unknown: true})
	}

	sc.vars = sc.vars.forget(names...)
}

// assignUnknownExported makes each of names unknown in sc, and, while allexport may be on, which
// exports them, keeps an invocation placed at sc that holds the GitVariable mark of each of names
// that is a GIT_* variable off the allow list; it keeps none when no name is.
func (w *walker) assignUnknownExported(names []string, sc *scope) {
	sc.assignUnknown(names)
	if !sc.allexport {
		return
	}

	if marks := gitVariableMarks(names); len(marks) > 0 {
		w.keep(w.placed(Invocation{Marks: marks}, sc))
	}
}

// exportedDefaults makes each variable a `${NAME:=word}` or `${NAME=word}` in node assigns unknown
// in sc, whichever variable it is, and marks each GIT_* variable off the allow list among them
// while allexport may be on, which exports it. A command reached through a substitution, and a
// simple command that is not a declaration, marks its own.
func (w *walker) exportedDefaults(node syntax.Node, sc *scope) {
	if stmt, ok := node.(*syntax.Stmt); ok {
		if _, call := stmt.Cmd.(*syntax.CallExpr); call {
			return
		}
	}

	var names []string
	syntax.Walk(node, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			return false
		case *syntax.Stmt:
			return syntax.Node(n) == node
		case *syntax.ParamExp:
			if assignsDefault(n) {
				names = append(names, n.Param.Value)
			}
		}

		return true
	})

	w.assignUnknownExported(names, sc)
}

// assignsDefault reports whether p is a `${NAME:=word}` or `${NAME=word}`, which assigns NAME.
func assignsDefault(p *syntax.ParamExp) bool {
	return p.Param != nil && p.Exp != nil && (p.Exp.Op == syntax.AssignUnset || p.Exp.Op == syntax.AssignUnsetOrNull)
}
