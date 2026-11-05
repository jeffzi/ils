//go:build unix

package guard

import (
	"fmt"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// ---------------------------------------------------------------------------
// Shell script extraction
// ---------------------------------------------------------------------------

// shellNames are the shells whose `-c` string Parse reads.
func shellNames() map[string]bool {
	return setOf("bash", "sh", "zsh", "dash")
}

// isShell reports whether the command word names a shell whose `-c` string Parse reads.
func isShell(command Word) bool {
	return shellNames()[programName(command)]
}

// scriptOf returns the string that the invocation of stmt, whose words after any wrapper are words,
// runs as commands of its own, when it is `eval` or a shell given `-c` or a heredoc, and reports
// whether there is one. The mark names a string it runs that Parse cannot read.
func scriptOf(stmt *syntax.Stmt, words []Word) (string, bool, MarkKind) {
	switch {
	case len(words) == 0 || !words[0].Literal:
		return "", false, ""
	case words[0].Text == "eval":
		return evalScript(words[1:])
	case isShell(words[0]):
		return shellScript(stmt, programName(words[0]), words[1:])
	default:
		return "", false, ""
	}
}

func evalScript(args []Word) (string, bool, MarkKind) {
	if len(args) == 0 {
		return "", false, ""
	}

	if slices.ContainsFunc(args, func(arg Word) bool { return !arg.Literal }) {
		return "", false, DynamicScript
	}

	return strings.Join(wordTexts(args), " "), true, ""
}

// shellScript returns the script the shell named shell runs, given args: its `-c` string, or the
// heredoc on its standard input. A script file, a startup file, or any other standard input holds a
// script the command does not, which the guard does not read, just as it does not read `./x.sh`.
func shellScript(stmt *syntax.Stmt, shell string, args []Word) (string, bool, MarkKind) {
	set := readShellOptions(shell, args)
	switch {
	case set.command && len(set.operands) == 0:
		return "", false, ""
	case set.command && !set.operands[0].Literal:
		return "", false, DynamicScript
	case set.command:
		return set.operands[0].Text, true, ""
	case len(set.operands) > 0 && !set.stdin:
		return "", false, ""
	default:
		return heredocScript(stmt)
	}
}

// scriptFileWords returns the words of the shell invocation words from its script file on, the
// file and the arguments the script gets, and reports whether the shell runs a script file. A
// shell given `-c`, reading standard input, or first reading a startup file runs none.
func scriptFileWords(words []Word) ([]Word, bool) {
	set := readShellOptions(programName(words[0]), words[1:])
	if set.readsStartup || set.command || set.stdin || len(set.operands) == 0 {
		return nil, false
	}

	return set.operands, true
}

// hidesScript reports whether the script file file, read from the raw word raw, holds a script
// the command does not: a process substitution, or a name for standard input (`/dev/stdin`,
// `/dev/fd/N`, `/proc/self/fd/N`).
func hidesScript(file Word, raw *syntax.Word) bool {
	if substitutes(raw) {
		return true
	}

	return file.Literal && (file.Text == "/dev/stdin" || strings.HasPrefix(file.Text, "/dev/fd/") ||
		strings.HasPrefix(file.Text, "/proc/self/fd/"))
}

// substitutes reports whether w holds a process substitution, whose file is a script the command
// writes to a pipe.
func substitutes(w *syntax.Word) bool {
	return slices.ContainsFunc(w.Parts, func(part syntax.WordPart) bool {
		_, ok := part.(*syntax.ProcSubst)

		return ok
	})
}

// ---------------------------------------------------------------------------
// Shell option parsing
// ---------------------------------------------------------------------------

// bashLongOptions are the long options of bash, which it also reads with a single dash.
func bashLongOptions() map[string]bool {
	return setOf(
		"debug", "debugger", "dump-po-strings", "dump-strings", "help", "init-file", "login", "noediting",
		"noprofile", "norc", "posix", "pretty-print", "protected", "rcfile", "restricted", "verbose",
		"version", "wordexp",
	)
}

// shellOptions is what the options of a shell set, with the operands after them.
type shellOptions struct {
	operands []Word
	command  bool
	stdin    bool
	// readsStartup reports that the shell first reads a startup file the options name: bash given
	// `--rcfile` or `--init-file` and `-i`. The `-i` of sh does not read it.
	readsStartup bool
}

// optionReader reads the options of the shell named shell, one word at a time.
type optionReader struct {
	shell string
	shellOptions

	interactive bool
}

// readShellOptions reads args, the arguments of the shell named shell, the way that shell does.
//
//   - bash and sh first read a run of long options, each written with one dash or two, and the
//     file `rcfile` and `init-file` name, even one starting with a dash. The first word that is
//     not a long option ends the run, and every option after it is a bundle of short options.
//   - zsh and dash read only bundles. A word starting with two dashes is a long option of zsh at
//     any position; to bash and dash past their leading run it is an error, so nothing runs.
//   - A lone `-` ends the options of every one of them, like `--`: with `-c`, the word after it is
//     the command string. A lone `+` does too in zsh; bash, sh and dash skip it and read on.
func readShellOptions(shell string, args []Word) shellOptions {
	r := optionReader{shell: shell}
	startup, i := false, 0
	if shell == "bash" || shell == "sh" {
		i, startup = bashLongRun(args)
	}

	for ; i < len(args); i++ {
		text := args[i].Text
		switch {
		case !args[i].Literal:
			r.operands = args[i:]
		case text == "-" || text == "--" || (text == "+" && shell == "zsh"):
			r.operands = args[i+1:]
		case text == "+":
			continue
		case len(text) < 2 || !startsWithSign(text):
			r.operands = args[i:]
		case strings.HasPrefix(text, "--"):
			continue
		default:
			i += r.bundle(args, i)

			continue
		}

		break
	}

	r.readsStartup = shell == "bash" && startup && r.interactive

	return r.shellOptions
}

// bashLongRun returns how many of args, the arguments of bash or sh, the run of long options
// leading them takes, and whether the run names a startup file.
func bashLongRun(args []Word) (int, bool) {
	startup, i := false, 0
	for i < len(args) && isBashLongOption(args[i]) {
		name := strings.TrimLeft(args[i].Text, "-")
		i++
		if name == "rcfile" || name == "init-file" {
			startup = true
			i++
		}
	}

	return min(i, len(args)), startup
}

// isBashLongOption reports whether w is a long option of bash: any word of two dashes and a name,
// or one dash before the name of a long option.
func isBashLongOption(w Word) bool {
	if !w.Literal {
		return false
	}

	if name, ok := strings.CutPrefix(w.Text, "--"); ok {
		return name != ""
	}

	name, ok := strings.CutPrefix(w.Text, "-")

	return ok && bashLongOptions()[name]
}

// bundle reads the bundle of short options args[i] and returns how many words after it the bundle
// takes as values.
func (r *optionReader) bundle(args []Word, i int) int {
	letters := args[i].Text[1:]
	enable := args[i].Text[0] == '-'
	taken := 0
	for j, letter := range letters {
		switch letter {
		case 'c':
			r.command = r.command || enable
		case 's':
			r.stdin = r.stdin || enable
		case 'i':
			r.interactive = true
		case 'O':
			// The `O` of bash, sh and dash takes the next word; that of zsh takes nothing.
			if r.shell != "zsh" {
				taken++
			}
		case 'o':
			// The `o` of zsh takes the rest of its bundle as the option name when there is any;
			// every other `o` takes the next word.
			if r.shell == "zsh" && j+1 < len(letters) {
				return taken
			}

			taken++
		}
	}

	return taken
}

// heredocScript returns the script on the standard input of stmt when it is a literal heredoc;
// any other standard input holds no script the command holds.
func heredocScript(stmt *syntax.Stmt) (string, bool, MarkKind) {
	var input *syntax.Redirect
	for _, r := range stmt.Redirs {
		if (r.N == nil || r.N.Value == stdinFd) && isInput(r.Op) {
			input = r
		}
	}

	if input == nil || (input.Op != syntax.Hdoc && input.Op != syntax.DashHdoc) {
		return "", false, ""
	}

	if input.Hdoc == nil {
		return "", true, ""
	}

	var body strings.Builder
	for _, part := range input.Hdoc.Parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			return "", false, DynamicScript
		}

		body.WriteString(lit.Value)
	}

	if quoted(input.Word) {
		return body.String(), true, ""
	}

	return unescapeHeredoc(body.String()), true, ""
}

func isInput(op syntax.RedirOperator) bool {
	switch op {
	case syntax.RdrIn, syntax.RdrInOut, syntax.DplIn, syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		return true
	default:
		return false
	}
}

// quoted reports whether the heredoc delimiter w has any quoting, which leaves its body as written.
func quoted(w *syntax.Word) bool {
	if len(w.Parts) != 1 {
		return true
	}

	lit, ok := w.Parts[0].(*syntax.Lit)

	return !ok || strings.Contains(lit.Value, `\`)
}

// unescapeHeredoc removes the backslashes the shell removes from the body of an unquoted heredoc:
// before `$`, a backtick, a backslash, and a newline.
func unescapeHeredoc(body string) string {
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' && i+1 < len(body) && strings.IndexByte("$`\\\n", body[i+1]) >= 0 {
			i++
			if body[i] == '\n' {
				continue
			}
		}

		b.WriteByte(body[i])
	}

	return b.String()
}

// ---------------------------------------------------------------------------
// Walking nested strings
// ---------------------------------------------------------------------------

// script walks the string that the invocation inv runs, one level deeper, starting with the
// environment inv runs with. A shell runs it in a process of its own, so nothing it changes holds
// after it. `eval` runs it in the shell itself, so the directory it leaves holds after it, and so
// does what it does to the shell's variables, while inv's assignment prefix and `env` wrappers end
// with it.
func (w *walker) script(script string, inv Invocation, sc *scope) {
	name := inv.Words[0].Text
	inner := sc.runScope(inv)
	// eval reads the variables of the shell, except those of its assignment prefix, which the guard
	// does not follow and so reads unknown.
	vars := sc.vars
	for _, a := range inv.Assignments {
		vars = vars.forget(a.Name)
	}

	if name == "eval" {
		inner.vars = vars
	} else {
		inner.shell = programName(inv.Words[0])
	}

	file, err := w.walkString(script, &inner)
	if err != nil {
		w.err = fmt.Errorf("parse the string %q runs: %w", name, err)

		return
	}

	if name != "eval" || !runsInShell(inv.Wrappers) {
		return
	}

	sc.functions = inner.functions

	// A dry run of the string from the shell's own environment leaves each variable as the string
	// sets it, or as the shell had it when the string does not touch it, other than those of the
	// assignment prefix.
	after := *sc
	after.vars = vars
	at := len(w.kept)
	w.nested(file, &after)
	w.kept = w.kept[:at]
	sc.dir = inner.dir
	sc.physical = inner.physical
	sc.env = after.env
	sc.vars = after.vars
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

// commandVariables are the editor variables whose value git runs as a shell command: its own, and
// the EDITOR and VISUAL it falls back to when neither its variable nor its config is set. The pager
// variables are left out: like `core.pager`, they are data.
func commandVariables() []string {
	return []string{"GIT_EDITOR", "GIT_SEQUENCE_EDITOR", "EDITOR", "VISUAL"}
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
	sc.vars = sc.vars.watched(file)
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
