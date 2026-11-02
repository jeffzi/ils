//go:build unix

package guard

import (
	"fmt"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// shellNames are the shells whose `-c` string Parse reads.
func shellNames() []string {
	return []string{"bash", "sh", "zsh", "dash"}
}

// isShell reports whether the command word names a shell whose `-c` string Parse reads.
func isShell(command Word) bool {
	return slices.Contains(shellNames(), programName(command))
}

// scriptOf returns the string that the invocation of words runs as commands of its own, when it
// is `eval` or a shell given `-c` or a heredoc, and reports whether there is one. The mark names
// a string it runs that Parse cannot read.
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

// shellScript returns the script the shell named shell, given args, runs: its `-c` string, or the
// heredoc on its standard input. Any other script it would read is marked hidden.
func shellScript(stmt *syntax.Stmt, shell string, args []Word) (string, bool, MarkKind) {
	set := readShellOptions(shell, args)
	switch {
	case set.readsStartup:
		return "", false, HiddenScript
	case set.command && len(set.operands) == 0:
		return "", false, ""
	case set.command && !set.operands[0].Literal:
		return "", false, DynamicScript
	case set.command:
		return set.operands[0].Text, true, ""
	case len(set.operands) > 0 && !set.stdin:
		return "", false, HiddenScript
	default:
		return heredocScript(stmt)
	}
}

// bashLongOptions are the long options of bash, which it also reads with a single dash.
func bashLongOptions() []string {
	return []string{
		"debug", "debugger", "dump-po-strings", "dump-strings", "help", "init-file", "login", "noediting",
		"noprofile", "norc", "posix", "pretty-print", "protected", "rcfile", "restricted", "verbose",
		"version", "wordexp",
	}
}

// shellOptions is what the options of a shell set, with the operands after them.
type shellOptions struct {
	operands  []Word
	command   bool
	stdin     bool
	allexport bool
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
//
// A word that is not a literal ends the options and may turn on anything, allexport included.
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
			r.allexport = true
			r.operands = args[i:]
		case text == "-" || text == "--" || (text == "+" && shell == "zsh"):
			r.operands = args[i+1:]
		case text == "+":
			continue
		case len(text) < 2 || (text[0] != '-' && text[0] != '+'):
			r.operands = args[i:]
		case strings.HasPrefix(text, "--"):
			if shell == "zsh" && zshOptionName(strings.ReplaceAll(text[2:], "-", "_")) == "allexport" {
				r.allexport = true
			}

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

// zshOptionName returns name, the name of an option of zsh, the way zsh compares it: case and
// underscores ignored. Dashes are kept: the caller first turns the dashes of a `--name` argument
// into underscores, as zsh reads them there.
func zshOptionName(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", ""))
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

	return ok && slices.Contains(bashLongOptions(), name)
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
		case 'a':
			r.allexport = enable
		case 'o', 'O':
			takes, ends := r.option(letter, letters[j+1:], enable, args[min(i+taken+1, len(args)):])
			taken += takes
			if ends {
				return taken
			}
		}
	}

	return taken
}

// option reads letter, an `o` or `O` that rest follows in its bundle, and returns how many of
// next, the words after the values its bundle has taken, it takes, and whether it takes rest. Each
// `o` and `O` of bash, sh and dash takes the next word. The `o` of zsh takes rest when there is
// any, and the next word otherwise; its `O` takes nothing.
func (r *optionReader) option(letter rune, rest string, enable bool, next []Word) (int, bool) {
	switch {
	case r.shell == "zsh" && letter == 'O':
		return 0, false
	case r.shell == "zsh" && rest != "":
		r.allexport = setsAllexport(r.shell, Word{Text: rest, Literal: true}, enable, r.allexport)

		return 0, true
	case letter == 'o' && len(next) > 0:
		r.allexport = setsAllexport(r.shell, next[0], enable, r.allexport)
	}

	return 1, false
}

// heredocScript returns the script on the standard input of stmt when it is a literal heredoc,
// and marks any other standard input as hidden.
func heredocScript(stmt *syntax.Stmt) (string, bool, MarkKind) {
	var input *syntax.Redirect
	for _, r := range stmt.Redirs {
		if (r.N == nil || r.N.Value == stdinFd) && isInput(r.Op) {
			input = r
		}
	}

	if input == nil || (input.Op != syntax.Hdoc && input.Op != syntax.DashHdoc) {
		return "", false, HiddenScript
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

// goCommandFlags are the flags of go whose value is a shell command go runs: `-toolexec` runs each
// compiler tool through it, and `-exec` each test binary or program go builds.
func goCommandFlags() []string {
	return []string{"-toolexec", "--toolexec", "-exec", "--exec"}
}

// goCommand is one flag of goCommandFlags in a go invocation, with its value.
type goCommand struct {
	flag  string
	value Word
}

// goCommandsOf returns each flag of goCommandFlags among words, the words of a go invocation, with
// its value: the rest of the word after `=`, or the next word. A word that only starts as one of
// the flags before an expansion has a value that is not a literal.
func goCommandsOf(words []Word) []goCommand {
	if len(words) == 0 || words[0].Text != "go" {
		return nil
	}

	var commands []goCommand
	args := words[1:]
	for i, arg := range args {
		flag, value, joined := strings.Cut(arg.Text, "=")
		if !slices.Contains(goCommandFlags(), flag) {
			continue
		}

		switch {
		case joined:
			commands = append(commands, goCommand{flag: flag, value: Word{Text: value, Literal: arg.Literal}})
		case arg.Literal && i+1 < len(args):
			commands = append(commands, goCommand{flag: flag, value: args[i+1]})
		}
	}

	return commands
}

// goCommands reads each command the go invocation inv runs through a flag of goCommandFlags, at
// depth levels of nesting, and returns those to walk. It marks inv, naming the flag, for a command
// that is not a literal, that does not parse, or that would nest past maxDepth.
func goCommands(inv *Invocation, depth int) []*syntax.File {
	var files []*syntax.File
	for _, c := range goCommandsOf(inv.Words) {
		form := "go " + c.flag
		switch {
		case !c.value.Literal:
			inv.Marks = append(inv.Marks, Mark{Kind: DynamicScript, Name: form})
		case depth == maxDepth:
			inv.Marks = append(inv.Marks, Mark{Kind: DeepScript, Name: form})
		default:
			file, err := read(c.value.Text)
			if err != nil {
				inv.Marks = append(inv.Marks, Mark{Kind: UnreadCommand, Name: form})

				continue
			}

			files = append(files, file)
		}
	}

	return files
}

// goSubcommand returns the subcommand word among args, the words after a go command word, past
// `-C dir`, the one global option go takes, which it reads only as its first argument. A first
// argument that is not a literal is returned as the subcommand word. It reports whether there is a
// subcommand word.
func goSubcommand(args []Word) (Word, bool) {
	if len(args) == 0 {
		return Word{}, false
	}

	flag, _, joined := strings.Cut(args[0].Text, "=")
	if args[0].Literal && (flag == "-C" || flag == "--C") {
		skip := min(2, len(args))
		if joined {
			skip = 1
		}

		args = args[skip:]
	}

	if len(args) == 0 {
		return Word{}, false
	}

	return args[0], true
}

// script walks the string that the invocation inv runs, one level deeper, starting with the
// environment inv runs with. A shell runs it in a process of its own, so nothing it changes holds
// after it. `eval` runs it in the shell itself, so the directory it leaves holds after it, and so
// does what it does to the shell's variables, while inv's assignment prefix and `env` wrappers end
// with it.
func (w *walker) script(script string, inv Invocation, sc *scope) {
	name := inv.Words[0].Text
	inner := sc.runScope(inv)
	if name != "eval" {
		inner.shell = programName(inv.Words[0])
	}

	inner.allexport = inner.allexport || startsAllexport(inv.Words)
	file, err := w.walkString(script, &inner)
	if err != nil {
		w.err = fmt.Errorf("parse the string %q runs: %w", name, err)

		return
	}

	if name != "eval" || !runsInShell(inv.Wrappers) {
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

// startsAllexport reports whether words, the words of `bash`, `sh`, `zsh`, or `dash` running a
// string, give the shell `-a` or `-o allexport`, which the string's commands run under.
func startsAllexport(words []Word) bool {
	if len(words) == 0 || !words[0].Literal || !isShell(words[0]) {
		return false
	}

	return readShellOptions(programName(words[0]), words[1:]).allexport
}
