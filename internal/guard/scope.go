//go:build unix

package guard

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

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
	// redirects are the redirects in effect on the shell's own descriptors: those of each compound
	// command the walk is inside, outermost first, then those an `exec` with no command made.
	redirects []Redirect
	// functions holds, by name, the functions a call may run. Copies of a scope share it, so a
	// declaration replaces it rather than changing it in place.
	functions map[string]*function
	env       environment
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
	sc := scope{dir: caller.Dir}
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
	a.functions = mergeFunctions(a.functions, b.functions)
	if _, toFile := stdoutFile(a.redirects); !toFile {
		// Either side's stdout may be in effect, so the one that sends it to a file decides.
		a.redirects = b.redirects
	}

	return a
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

// list walks stmts in order, each one starting where the one before left sc. A background job and
// the statements after it are walked by job.
func (w *walker) list(stmts []*syntax.Stmt, sc *scope) {
	for i, stmt := range stmts {
		if w.err != nil {
			return
		}

		if stmt.Background || stmt.Coprocess {
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
	if stmt.Background || stmt.Coprocess {
		inner := *sc
		w.race(&inner, func(s *scope) { w.statement(stmt, s) })

		return
	}

	w.statement(stmt, sc)
}

// statement walks stmt as the shell runs it once it has placed any background job.
func (w *walker) statement(stmt *syntax.Stmt, sc *scope) {
	if w.simple(stmt, sc) {
		return
	}

	for _, r := range stmt.Redirs {
		w.substitutions(r, sc)
	}

	own := redirectsOf(stmt, w.caller.Home)
	before := sc.redirects
	sc.redirects = slices.Concat(before, own)
	w.compound(stmt.Cmd, sc)
	sc.redirects = restored(before, own, sc.redirects)
}

// restored returns the redirects in effect once a command whose own redirects are own ends, given
// those in effect before it and those at its end. bash restores each descriptor the command
// redirected, so what an `exec` inside it did to stdout holds after it only when the command
// leaves stdout alone.
func restored(before, own, after []Redirect) []Redirect {
	switch {
	case len(own) == 0:
		return after
	case slices.ContainsFunc(own, ontoStdout):
		return before
	default:
		return slices.Concat(before, after[len(before)+len(own):])
	}
}

// ontoStdout reports whether the redirect r changes where stdout goes: `&>` and any redirect of
// descriptor 1 do, an unnumbered input redirect does not.
func ontoStdout(r Redirect) bool {
	if strings.HasPrefix(r.Op, "&>") {
		return true
	}

	fd := r.Fd
	if fd == "" && !strings.HasPrefix(r.Op, "<") {
		fd = stdoutFd
	}

	return fd == stdoutFd
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
		w.loopVariable(cmd, sc)
		w.substitutions(cmd.Loop, sc)
		w.repeated(sc, func(s *scope) { w.list(cmd.Do, s) })
	case *syntax.FuncDecl:
		w.declareFunction(cmd, sc)
		// A function body runs each time the function is called, from any later command.
		w.repeated(sc, func(s *scope) { w.stmt(cmd.Body, s) })
	default:
		// Arithmetic, `[[ … ]]`, and the other commands run nothing but the substitutions in them.
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
// shell may be after any of them.
func (w *walker) andList(cmd *syntax.BinaryCmd, sc *scope) {
	var after []scope
	for _, operand := range andOperands(cmd) {
		w.stmt(operand, sc)
		after = append(after, *sc)
	}

	merged := after[0]
	for _, s := range after[1:] {
		merged = merge(merged, s)
	}

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
			left.Background || left.Coprocess || left.Negated {
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
// it changes holds after it.
func (w *walker) substitutions(node syntax.Node, sc *scope) {
	w.exportedDefaults(node, sc)
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

// script walks the string that the invocation inv runs, one level deeper, starting with the
// environment inv runs with. A shell runs it in a process of its own, so nothing it changes holds
// after it. `eval` runs it in the shell itself, so the directory it leaves holds after it, and so
// does what it does to the shell's variables, while inv's assignment prefix and `env` wrappers end
// with it.
func (w *walker) script(script string, inv Invocation, sc *scope) {
	name := inv.Words[0].Text
	inner := sc.runScope(inv)
	inner.allexport = inner.allexport || startsAllexport(inv.Words)
	file, err := w.walkString(script, &inner)
	if err != nil {
		w.err = fmt.Errorf("parse the string %q runs: %w", name, err)

		return
	}

	if name != "eval" {
		return
	}

	sc.functions = inner.functions
	sc.allexport = inner.allexport

	// A dry run of the string from the shell's own environment leaves each variable as the string
	// sets it, or as the shell had it when the string does not touch it.
	after := *sc
	at := len(w.kept)
	w.nested(file, &after)
	w.kept = w.kept[:at]
	sc.dir = inner.dir
	sc.physical = inner.physical
	sc.env = after.env
	sc.redirects = restored(sc.redirects, inv.Redirects, inner.redirects)
}

// runScope returns the scope a string the invocation inv has run starts in, run from sc: the
// environment inv runs with, and its own redirects in effect after those of the shell.
func (sc *scope) runScope(inv Invocation) scope {
	inner := *sc
	inner.env = sc.commandEnv(inv)
	inner.redirects = slices.Concat(sc.redirects, inv.Redirects)

	return inner
}

// laterScope returns the scope of a string a variable set for later commands holds: any later
// command may run it, from a directory and with git variables the guard cannot tell.
func laterScope() scope {
	return scope{env: unknownEnvironment()}
}

// commandVariables are the variables whose value git runs as a shell command: its own, and the
// EDITOR, VISUAL, and PAGER it falls back to when neither its variable nor its config is set.
func commandVariables() []string {
	return []string{"GIT_EDITOR", "GIT_SEQUENCE_EDITOR", "GIT_PAGER", "EDITOR", "VISUAL", "PAGER"}
}

// variableScripts returns the value of each of assignments that sets one of commandVariables, and
// marks inv instead when one is not a literal or would nest past maxDepth.
func (w *walker) variableScripts(inv *Invocation, assignments []Assignment) []string {
	var values []Word
	for _, a := range assignments {
		if slices.Contains(commandVariables(), a.Name) {
			values = append(values, a.Value)
		}
	}

	return runnableScripts(inv, w.depth, values)
}

// variableCommands walks scripts, the shell commands a variable git reads holds, one level deeper
// from sc.
func (w *walker) variableCommands(scripts []string, sc scope) {
	for _, script := range scripts {
		inner := sc
		if _, err := w.walkString(script, &inner); err != nil {
			w.err = fmt.Errorf("parse the string a git variable holds: %w", err)

			return
		}
	}
}

// nested walks the commands of file, a string run one level deeper, from sc.
func (w *walker) nested(file *syntax.File, sc *scope) {
	sc.env.forgetCDPath(file)
	w.depth++
	w.list(file.Stmts, sc)
	w.depth--
}

// walkString reads script, a string run one level deeper, and walks it from sc. It returns the file
// it read, and the error of reading or walking it.
func (w *walker) walkString(script string, sc *scope) (*syntax.File, error) {
	file, err := read(script)
	if err != nil {
		return nil, err
	}

	w.nested(file, sc)

	return file, w.err
}

// runnableScripts returns the text of each of values, the shell commands inv has something run at
// depth levels of nesting, that is walked: none when they would nest past maxDepth. It marks inv
// when one of them is not a literal or they would nest too deep.
func runnableScripts(inv *Invocation, depth int, values []Word) []string {
	scripts, mark := literalScripts(values)
	runs, mark := capDepth(depth, len(scripts) > 0, mark)
	if !runs {
		scripts = nil
	}

	if mark != "" {
		inv.Marks = append(inv.Marks, Mark{Kind: mark})
	}

	return scripts
}

// capDepth reports whether a string that runs at depth levels of nesting is walked, and the mark
// for it: runs is cleared and the mark is DeepScript for one that would nest past maxDepth.
func capDepth(depth int, runs bool, mark MarkKind) (bool, MarkKind) {
	if runs && depth == maxDepth {
		return false, DeepScript
	}

	return runs, mark
}

// placed returns inv with where it runs: its directory, where it points git, and where a gh
// invocation's gh reads its config. It leaves sc's functions as those callMarks reads.
func (w *walker) placed(inv Invocation, sc *scope) Invocation {
	w.functions = sc.functions
	inv.Dir = sc.dir
	inv.enclosing = sc.redirects
	if inv.Tool == Git {
		inv.Git = readGitCall(inv.Words)
		inv.Target = sc.targetOf(inv)
	}

	if inv.Tool == GH {
		inv.ghConfig = sc.commandEnv(inv).ghConfig
	}

	return inv
}

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

	if !inv.Words[0].Literal {
		return
	}

	switch inv.Words[0].Text {
	case "set":
		sc.allexport = allexportAfter(inv.Words[1:], sc.allexport)
	case "cd", "pushd":
		sc.cd(inv.Words[1:], sc.commandEnv(inv).cdPath)
	case "popd":
		sc.dir = ""
	case "unset":
		for _, name := range inv.Words[1:] {
			sc.env.set(name.Text, variable{unknown: true})
		}
	}
}

// execsNothing reports whether words, the words of a command, are an `exec` with nothing after its
// options, reached through any of the wrappers that run a builtin: one that applies its redirects
// to the shell itself.
func execsNothing(words []Word) bool {
	i := 0
	for i < len(words) && words[i].Literal && (words[i].Text == "command" || words[i].Text == "builtin") {
		options, _ := wrapperOptionsOf(words[i].Text)
		n, _ := skipOptions(words[i+1:], options)
		i += 1 + n
	}

	if i == len(words) || !words[i].Literal || words[i].Text != "exec" {
		return false
	}

	options, _ := wrapperOptionsOf("exec")
	n, _ := skipOptions(words[i+1:], options)

	return i+1+n == len(words)
}

// allexportAfter returns whether `set` given args leaves allexport on, given whether it was on
// before. An option the guard cannot read may turn it on.
func allexportAfter(args []Word, on bool) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !arg.Literal {
			return true
		}

		if len(arg.Text) < 2 || arg.Text == "--" || !strings.ContainsAny(arg.Text[:1], "-+") {
			return on
		}

		enable := arg.Text[0] == '-'
		for _, letter := range arg.Text[1:] {
			switch {
			case letter == 'a':
				on = enable
			case letter == 'o' && i+1 < len(args):
				i++
				on = setsAllexport(args[i], enable, on)
			}
		}
	}

	return on
}

// setsAllexport returns whether `set -o name`, or `set +o name` when enable is false, leaves
// allexport on, given whether it was on before.
func setsAllexport(name Word, enable, on bool) bool {
	switch {
	case !name.Literal:
		return on || enable
	case name.Text == "allexport":
		return enable
	default:
		return on
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
	case target.Literal && (target.Text == "" || strings.ContainsAny(target.Text[:1], "-+")),
		target.Literal && cdPath != (variable{}) && !filepath.IsAbs(target.Text),
		target.Literal && leavesThroughLinks(target.Text, physical, sc.physical):
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

// followedCDFlags are the option letters of `cd` and `pushd` the guard follows.
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

// keep records inv as walked, and as the next invocation Parse returns.
func (w *walker) keep(inv Invocation) {
	w.kept = append(w.kept, len(w.walked))
	w.walked = append(w.walked, inv)
}

// returned returns the invocations Parse returns, each reporting whether an invocation walked
// before it may move HEAD. It reads each git invocation once its aliases expand, so it runs after
// resolveGit. The walk order is the order the shell may run them in: the substitutions of a command
// come before it, a dry run of a body that may repeat before the body itself, and a dry run of a
// background job or pipe stage before the commands already walked.
func (w *walker) returned() []Invocation {
	moved := false
	for i := range w.walked {
		w.walked[i].HeadUnknown = moved
		moved = moved || movesHead(w.walked[i])
	}

	var invocations []Invocation
	for _, i := range w.kept {
		invocations = append(invocations, w.walked[i])
	}

	return invocations
}

// allowedGitVariables is the list of GIT_* variables a command may set: those that point git at a
// repository, which the guard follows, and those that change no rule it judges by.
func allowedGitVariables() []string {
	return []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_PAGER", "GIT_EDITOR", "GIT_SEQUENCE_EDITOR",
		"GIT_TERMINAL_PROMPT", "GIT_TRACE", "GIT_TRACE2", "GIT_ADVICE", "GIT_OPTIONAL_LOCKS",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE",
	}
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

// loopVariable marks a `for` loop whose variable is a GIT_* variable off the allow list while
// allexport may be on, which exports it to every git after it.
func (w *walker) loopVariable(c *syntax.ForClause, sc *scope) {
	iter, ok := c.Loop.(*syntax.WordIter)
	if !ok || !sc.allexport {
		return
	}

	if marks := gitVariableMarks([]string{iter.Name.Value}); len(marks) > 0 {
		w.keep(w.placed(Invocation{Marks: marks}, sc))
	}
}

// exportedDefaults marks each GIT_* variable off the allow list that a `${NAME:=word}` or
// `${NAME=word}` in node assigns while allexport may be on, which exports it. A command reached
// through a substitution, and a simple command that is not a declaration, marks its own.
func (w *walker) exportedDefaults(node syntax.Node, sc *scope) {
	if !sc.allexport {
		return
	}

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

	if marks := gitVariableMarks(names); len(marks) > 0 {
		w.keep(w.placed(Invocation{Marks: marks}, sc))
	}
}

// assignsDefault reports whether p is a `${NAME:=word}` or `${NAME=word}`, which assigns NAME.
func assignsDefault(p *syntax.ParamExp) bool {
	return p.Param != nil && p.Exp != nil && (p.Exp.Op == syntax.AssignUnset || p.Exp.Op == syntax.AssignUnsetOrNull)
}

// startsAllexport reports whether words, the words of `bash`, `sh`, `zsh`, or `dash` running a
// string, give the shell `-a` or `-o allexport`, which the string's commands run under. A long
// option is skipped, with the file `--rcfile` and `--init-file` read.
func startsAllexport(words []Word) bool {
	if len(words) == 0 || !words[0].Literal ||
		!slices.Contains([]string{"bash", "sh", "zsh", "dash"}, filepath.Base(words[0].Text)) {
		return false
	}

	var options []Word
	for i := 1; i < len(words); i++ {
		word := words[i]
		if !word.Literal || len(word.Text) <= 2 || !strings.HasPrefix(word.Text, "--") {
			options = append(options, word)

			continue
		}

		if word.Text == "--rcfile" || word.Text == "--init-file" {
			i++
		}
	}

	return allexportAfter(options, false)
}
