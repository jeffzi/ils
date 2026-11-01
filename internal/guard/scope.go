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
	env environment
	// redirects are the redirects in effect on the shell's own descriptors: those of each compound
	// command the walk is inside, outermost first, then those an `exec` with no command made.
	redirects []Redirect
}

// environment is what the guard knows of the variables it follows: those that point git at a
// repository, and CDPATH, which `cd` reads a relative path against.
type environment struct {
	gitDir   variable
	workTree variable
	cdPath   variable
}

// envChange is one step in building the environment a command runs with: an assignment, or what
// an `env -i` or `env -u NAME` removes.
type envChange struct {
	// unset is the name `env -u` removes; empty for any other change.
	unset      string
	assignment Assignment
	// clear reports whether the change is `env -i`, which removes every variable before it.
	clear bool
}

// variable is what the guard knows of one variable.
type variable struct {
	value   string
	set     bool
	unknown bool
}

// startScope returns the scope command file starts in, for caller.
func startScope(caller Caller, file *syntax.File) scope {
	sc := scope{dir: caller.Dir}
	for name, value := range caller.Env {
		sc.env.set(name, variable{value: value, set: true})
	}

	sc.env.forgetCDPath(file)

	return sc
}

// forgetCDPath makes CDPATH unknown in e when file assigns it anywhere. A function body is walked
// where it is declared rather than where it is called, so an assignment the walk has not reached
// in order may still be in effect when a `cd` runs.
func (e *environment) forgetCDPath(file *syntax.File) {
	if assigns(file, "CDPATH") {
		e.cdPath = variable{unknown: true}
	}
}

// assigns reports whether node assigns the variable name anywhere in it.
func assigns(node syntax.Node, name string) bool {
	found := false
	syntax.Walk(node, func(n syntax.Node) bool {
		if a, ok := n.(*syntax.Assign); ok && a.Name != nil && a.Name.Value == name {
			found = true
		}

		return !found
	})

	return found
}

// merge returns the scope that either a or b may be: what they agree on, and unknown elsewhere.
func merge(a, b scope) scope {
	if a.dir != b.dir {
		a.dir = ""
	}

	a.env.gitDir = a.env.gitDir.or(b.env.gitDir)
	a.env.workTree = a.env.workTree.or(b.env.workTree)
	a.env.cdPath = a.env.cdPath.or(b.env.cdPath)
	if _, toFile := (Invocation{Redirects: a.redirects}).Stdout(); !toFile {
		// Either side's stdout may be in effect, so the one that sends it to a file decides.
		a.redirects = b.redirects
	}

	return a
}

func (v variable) or(other variable) variable {
	if v != other {
		return variable{unknown: true}
	}

	return v
}

// set records v as the value of name, when name is a variable the guard follows.
func (e *environment) set(name string, v variable) {
	switch name {
	case "GIT_DIR":
		e.gitDir = v
	case "GIT_WORK_TREE":
		e.workTree = v
	case "CDPATH":
		e.cdPath = v
	}
}

// valueOf returns what the guard knows of a variable given the value w.
func valueOf(w Word) variable {
	if !w.Literal {
		return variable{unknown: true}
	}

	return variable{value: w.Text, set: true}
}

// resolve returns the path v holds, made absolute against dir, and false when the guard cannot
// tell it.
func (v variable) resolve(dir string) (string, bool) {
	switch {
	case v.unknown:
		return "", false
	case !v.set:
		return "", true
	}

	abs := absolute(dir, v.value)

	return abs, abs != ""
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
// stays in it. bash opens a compound command's redirects before it runs the body, so the
// substitutions in them start from the shell as it is before the body, and every command in the
// body runs with them.
func (w *walker) stmt(stmt *syntax.Stmt, sc *scope) {
	if stmt.Background || stmt.Coprocess {
		inner := *sc
		sc = &inner
	}

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

// ontoStdout reports whether the redirect r changes where stdout goes.
func ontoStdout(r Redirect) bool {
	if strings.HasPrefix(r.Op, "&>") {
		return true
	}

	fd := r.Fd
	if fd == "" && !strings.HasPrefix(r.Op, "<") {
		fd = "1"
	}

	return fd == "1"
}

// compound walks a command that holds other commands.
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
		w.substitutions(cmd.Loop, sc)
		w.repeated(sc, func(s *scope) { w.list(cmd.Do, s) })
	case *syntax.FuncDecl:
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
		w.stmt(cmd.X, sc)
		w.stmt(cmd.Y, sc)
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
		left, right := *sc, *sc
		w.stmt(cmd.X, &left)
		w.stmt(cmd.Y, &right)
		*sc = merge(merge(*sc, left), right)
	}
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
	file, err := read(script)
	if err != nil {
		w.err = fmt.Errorf("parse the string %q runs: %w", name, err)

		return
	}

	inner := sc.runScope(inv)
	w.nested(file, &inner)
	if w.err != nil {
		w.err = fmt.Errorf("parse the string %q runs: %w", name, w.err)

		return
	}

	if name != "eval" {
		return
	}

	// A dry run of the string from the shell's own environment leaves each variable as the string
	// sets it, or as the shell had it when the string does not touch it.
	after := *sc
	at := len(w.kept)
	w.nested(file, &after)
	w.kept = w.kept[:at]
	sc.dir = inner.dir
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
	unknown := variable{unknown: true}

	return scope{env: environment{gitDir: unknown, workTree: unknown, cdPath: unknown}}
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

	scripts, mark := literalScripts(values)
	if len(scripts) > 0 && w.depth == maxDepth {
		scripts, mark = nil, DeepScript
	}

	if mark != "" {
		inv.Marks = append(inv.Marks, Mark{Kind: mark})
	}

	return scripts
}

// variableCommands walks scripts, the shell commands a variable git reads holds, one level deeper
// from sc.
func (w *walker) variableCommands(scripts []string, sc scope) {
	for _, script := range scripts {
		file, err := read(script)
		if err != nil {
			w.err = fmt.Errorf("parse the string a git variable holds: %w", err)

			return
		}

		inner := sc
		w.nested(file, &inner)
		if w.err != nil {
			w.err = fmt.Errorf("parse the string a git variable holds: %w", w.err)

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

// placed returns inv with where it runs: its directory, and where it points git.
func (w *walker) placed(inv Invocation, sc *scope) Invocation {
	inv.Dir = sc.dir
	inv.enclosing = sc.redirects
	if inv.Tool == Git {
		inv.Git = readGitCall(inv.Words)
		inv.Target = sc.targetOf(inv)
	}

	return inv
}

// apply changes sc the way the invocation inv changes the shell that runs it.
func (sc *scope) apply(inv Invocation) {
	if len(inv.Words) == 0 {
		// A bare assignment sets a shell variable, which reaches git only once it is exported, as it
		// may already be.
		for _, a := range inv.Assignments {
			sc.env.set(a.Name, variable{unknown: true})
		}

		return
	}

	if !inv.Words[0].Literal {
		return
	}

	switch inv.Words[0].Text {
	case "exec":
		// `exec` with no command applies its redirects to the shell itself.
		if len(inv.Words) == 1 {
			sc.redirects = slices.Concat(sc.redirects, inv.Redirects)
		}
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
	switch {
	case target.Literal && (target.Text == "" || strings.ContainsAny(target.Text[:1], "-+")),
		target.Literal && cdPath != (variable{}) && !filepath.IsAbs(target.Text):
		sc.dir = ""
	default:
		sc.dir = moved(sc.dir, target)
	}
}

// cdOperands returns the operands of `cd` or `pushd` after its options, and false when an option
// is one the guard does not follow.
func cdOperands(args []Word) ([]Word, bool) {
	for i, arg := range args {
		switch {
		case !arg.Literal || len(arg.Text) < 2 || arg.Text[0] != '-':
			return args[i:], true
		case arg.Text == "--":
			return args[i+1:], true
		case strings.Trim(arg.Text[1:], "LPe@qs") != "":
			return nil, false
		}
	}

	return nil, true
}

// declare changes sc the way the declaration builtin decl does. Only `export NAME=value` with no
// option sets a git variable the guard follows; any other declaration of one makes it unknown.
func (sc *scope) declare(decl *syntax.DeclClause, home string) {
	plain := decl.Variant.Value == "export"
	for _, a := range decl.Args {
		if a.Name == nil {
			plain = false
		}
	}

	for _, a := range decl.Args {
		if a.Name == nil {
			continue
		}

		v := variable{unknown: true}
		if plain && !a.Naked && !a.Append {
			v = valueOf(assignmentOf(a, home).Value)
		}

		sc.env.set(a.Name.Value, v)
	}
}

// declared returns the assignments the declaration decl makes: each `NAME=value` among its
// arguments.
func declared(decl *syntax.DeclClause, home string) []Assignment {
	var assignments []Assignment
	for _, a := range decl.Args {
		if a.Name != nil && !a.Naked {
			assignments = append(assignments, assignmentOf(a, home))
		}
	}

	return assignments
}

// commandEnv returns the environment the invocation inv runs with, run from sc: the shell's, then
// inv's changes in the order bash makes them, its assignment prefix first and then each `env`
// wrapper's. What `env -i` or `env -u` removes is unknown rather than unset.
func (sc *scope) commandEnv(inv Invocation) environment {
	env := sc.env
	for _, c := range inv.env {
		switch {
		case c.clear:
			unknown := variable{unknown: true}
			env = environment{gitDir: unknown, workTree: unknown, cdPath: unknown}
		case c.unset != "":
			env.set(c.unset, variable{unknown: true})
		default:
			env.set(c.assignment.Name, valueOf(c.assignment.Value))
		}
	}

	return env
}

// targetOf returns where the git invocation inv points git, run from sc.
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
// come before it, and a dry run of a body that may repeat before the body itself.
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

// movesHead reports whether the invocation inv may move HEAD, reading a git invocation by the
// subcommand its aliases expand to. A git invocation whose subcommand Parse could not resolve may
// run any command, so it may move HEAD.
func movesHead(inv Invocation) bool {
	words := inv.Words
	switch inv.Tool {
	case GH:
		return ghMovesHead(words[1:])
	case Git:
	default:
		return false
	}

	if slices.ContainsFunc(inv.Marks, func(m Mark) bool {
		return m.Kind == ShellAlias || m.Kind == UnknownSubcommand || m.Kind == DeepAlias ||
			m.Kind == UnreadableAlias
	}) {
		return true
	}

	if !inv.Git.Subcommand.Literal {
		return false
	}

	args := inv.Git.Args
	switch inv.Git.Subcommand.Text {
	case "switch", "checkout", "bisect":
		// Every checkout counts, the one that restores files and leaves HEAD where it is too:
		// telling them apart means reading its operands as git does.
		return true
	case "symbolic-ref":
		return positionals(args, func(text string) bool { return text == "-m" }) > 1
	case "branch":
		return slices.ContainsFunc(args, isBranchMove)
	case "rebase":
		// The last of two operands is a branch rebase switches to first; with `--root` there is no
		// upstream operand, so one operand is that branch.
		branchAt := 2
		if slices.ContainsFunc(args, func(arg Word) bool { return arg.Literal && arg.Text == "--root" }) {
			branchAt = 1
		}

		return positionals(args, takesRebaseValue) >= branchAt
	default:
		return false
	}
}

// ghMovesHead reports whether gh given args may move HEAD: `gh pr checkout`, and a first command
// word that is none of gh's commands, since an alias or an extension may check out a branch. It
// finds the command words the way Cobra does, past the values of flags; one that is not a literal
// may be any of them.
func ghMovesHead(args []Word) bool {
	at := commandWords(args, ghSwitches())
	if len(at) == 0 {
		return false
	}

	first := args[at[0]]
	if !first.Literal || !ghCommands()[first.Text] {
		return true
	}

	return first.Text == "pr" && len(at) > 1 && (!args[at[1]].Literal || args[at[1]].Text == "checkout")
}

// isBranchMove reports whether arg is an option `git branch` reads as `--move`: `m` or `M` in a
// short-flag bundle ahead of any `u`, whose rest is the value of `-u`, or a prefix of `--move` git
// reads as it.
func isBranchMove(arg Word) bool {
	text := arg.Text
	switch {
	case !arg.Literal || !strings.HasPrefix(text, "-"):
		return false
	case strings.HasPrefix(text, "--"):
		const move = "--move"

		return len(text) >= len("--mo") && len(text) <= len(move) && text == move[:len(text)]
	default:
		bundle, _, _ := strings.Cut(text[1:], "u")

		return strings.ContainsAny(bundle, "mM")
	}
}

// positionals returns how many of args are operands rather than options, given which options take
// the next word as their value. A word that is not a literal counts as an operand.
func positionals(args []Word, takesValue func(string) bool) int {
	n := 0
	for i := 0; i < len(args); i++ {
		text := args[i].Text
		switch {
		case !args[i].Literal || !strings.HasPrefix(text, "-") || text == "-":
			n++
		case text == "--":
			return n + len(args) - i - 1
		case takesValue(text):
			i++
		}
	}

	return n
}

func takesRebaseValue(text string) bool {
	switch text {
	case "--onto", "-s", "--strategy", "-X", "--strategy-option", "-x", "--exec":
		return true
	default:
		return false
	}
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
