//go:build unix

package guard

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/jeffzi/ils/internal/commandline"
	"github.com/jeffzi/ils/internal/landing"
)

// isLookup reports whether words open with `command -v` or `command -V`, which look a name up
// instead of running it.
func isLookup(words []Word) bool {
	return len(words) > 1 && words[0].Text == "command" && words[1].Literal &&
		(words[1].Text == "-v" || words[1].Text == "-V")
}

// dataConsumers is the table of command words that take their arguments as data, never as a
// command to run. `command -v` and `command -V` are the lookups isLookup adds.
func dataConsumers() map[string]bool {
	return setOf(
		// Shell built-ins and file tools.
		"cd", "pushd", "popd", "test", "[", "mkdir", "cp", "mv", "rm", "ln", "touch", "chmod", "tee",
		"file", "stat", "du", "realpath", "readlink", "dirname", "basename",
		// Readers and filters.
		"echo", "printf", "cat", "head", "tail", "less", "grep", "egrep", "fgrep", "ls", "wc",
		"sort", "uniq", "cut", "tr", "jq", "diff", "rg",
		// Shell script checkers and formatters, which name a shell dialect without running it.
		"shellcheck", "shfmt",
		// Lookups.
		"which", "type", "whence", "where",
		// Toolchains and package managers.
		"go", "brew", "apt", "apt-get", "dnf", "yum", "pacman", "apk", "port",
	)
}

// isDataConsumer reports whether words are the invocation of a data consumer. `go run` and `go
// generate` are not one, past any global option of go (`go -C dir run`): they start a program the
// command does not hold, the one go run builds or those go generate finds in source files, with
// their arguments. Nor is a go command whose subcommand word is not a literal, which covers a
// non-literal word where go reads `-C`: either may be `run`, `generate`, or `-C=dir`.
func isDataConsumer(words []Word) bool {
	if isLookup(words) {
		return true
	}

	if words[0].Text == "go" {
		if sub, ok := goSubcommand(words[1:]); ok && (!sub.Literal || sub.Text == "run" || sub.Text == "generate") {
			return false
		}
	}

	return dataConsumers()[words[0].Text]
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

// handedByCall returns the tool that the command call, a command the guard does not know whose
// words after any wrapper are words, may run from its arguments, and reports whether there is one.
// It reads the arguments as handedBy does, once each bare shell word that runs a script the guard
// can see is read as that script run directly (runDirectly): `xargs -I{} sh -c 'ls {}'` hands
// over only what its string hands over, and `timeout 60 bash x.sh` only what `timeout 60 ./x.sh`
// does. The string runs where the command puts it, so a git call in it is never judged as if it
// ran here: holding a tool word, or a command that hands one over in turn, is enough to hand one
// over. dir is where a relative path is read against, and ex reads the arguments.
func handedByCall(call *syntax.CallExpr, words []Word, dir string, ex expander) (string, bool) {
	args := argsOf(call, words)
	if !isInterpreter(words[0]) {
		words, args = runDirectly(words, args)
	}

	return handedBy(words, args, dir, ex)
}

// argsOf returns the arguments of call that words, its words after any wrapper, were read from.
func argsOf(call *syntax.CallExpr, words []Word) []*syntax.Word {
	return call.Args[len(call.Args)-len(words):]
}

// runDirectly returns words and args, the raw word each of words was read from, with each bare
// shell word before the first interpreter word read as the script it runs, run directly:
//
//   - A shell followed by `-c` and a literal string loses the shell and the `-c`. The string stays,
//     to be read as any other argument. A bare shell word right after the string goes too: it is
//     the zero argument (`$0`), the name the shell gives itself, as in `xargs sh -c 'ls "$1"' sh`.
//   - A shell given a script file loses the shell and its options. The file and the arguments the
//     script gets stay, read as those of `./x.sh` in the same place.
//
// A shell word with neither, whose string is not a literal, or whose file holds a script the
// command does not (`/dev/stdin`, `<(cat x)`) stays, and so does a path to a shell. Words from the
// first interpreter word on are its own: a shell word there is one of its arguments.
//
// Only a shell word as the zero argument goes. Any other zero argument, and every word after it,
// stays a word handed to the command: the string can run them (`sh -c '"$@"' sh git push`), and
// the guard does not read what the string does with its parameters.
func runDirectly(words []Word, args []*syntax.Word) ([]Word, []*syntax.Word) {
	keptWords := []Word{words[0]}
	keptArgs := []*syntax.Word{args[0]}
	for i := 1; i < len(words); i++ {
		if isInterpreter(words[i]) {
			return append(keptWords, words[i:]...), append(keptArgs, args[i:]...)
		}

		ranString := false
		if isBareShell(words[i]) {
			ranString = runsString(words[i:])
			i += shellWords(words[i:], args[i:])
		}

		keptWords = append(keptWords, words[i])
		keptArgs = append(keptArgs, args[i])

		if ranString && i+1 < len(words) && isBareShell(words[i+1]) {
			i++
		}
	}

	return keptWords, keptArgs
}

// isBareShell reports whether w is the plain name of a shell whose `-c` string Parse reads, not a
// path to one.
func isBareShell(w Word) bool {
	return inTable(shellNames(), w)
}

// runsString reports whether words, a shell word and the words after it, give the shell `-c` and a
// literal string.
func runsString(words []Word) bool {
	return len(words) > 2 && words[1].Literal && words[1].Text == "-c" && words[2].Literal
}

// shellWords returns how many of words, a bare shell word and the words after it read from args,
// go before the script it runs: the shell and `-c` before a literal string, or the shell and its
// options before a script file that holds the script. It returns zero when the shell runs neither.
func shellWords(words []Word, args []*syntax.Word) int {
	if runsString(words) {
		return 2
	}

	file, ok := scriptFileWords(words)
	if !ok {
		return 0
	}

	skip := len(words) - len(file)
	if hidesScript(file[0], args[skip]) {
		return 0
	}

	return skip
}

// handedBy returns the tool that a command the guard does not know, whose words are words read
// from args, may run from its arguments, and reports whether there is one. An argument hands over
// a tool when it is a tool word itself, or when it reads as a shell script one of whose commands
// is a tool word or hands one over in turn: `su -c 'cd /repo && git push'`, `ssh host 'sudo git
// push'`. The arguments of an interpreter, whether it is the command or an argument of it
// (`xargs sed …`), are code in its own language, never read as a shell script, so only a tool
// word among them hands one over. A command that hands its words to a shell (shellHandovers) has
// every argument read as a script, interpreter word or not: that shell reads them as one. An ils
// word followed by an ils command that changes nothing hands over nothing. dir is where a
// relative path is read against, and ex reads the arguments; a script among them runs in a shell
// of its own.
func handedBy(words []Word, args []*syntax.Word, dir string, ex expander) (string, bool) {
	toShell := words[0].Literal && shellHandovers()[programName(words[0])]
	readsScripts := !isInterpreter(words[0])
	for i, w := range words[1:] {
		if isToolWord(w, dir) {
			if toolOf(w) == ILS && changesNothing(words[i+2:]) {
				continue
			}

			return w.Text, true
		}

		if !toShell && isInterpreter(w) {
			readsScripts = false
		}

		if !readsScripts {
			continue
		}

		if name, ok := scriptTool(scriptText(args[i+1], ex), ex.home); ok {
			return name, true
		}
	}

	return "", false
}

// isToolWord reports whether w names git, gh, ils, a shell, `eval`, or `source`: the bare name, or
// a path whose basename is the name and that is an executable regular file, a relative path read
// against dir. A path whose stat fails for any reason other than its absence counts as one, and so
// does a relative path when dir is empty, since the guard cannot rule it out.
//
// `.` is not counted: as an argument it names the current directory far more often (`find .`,
// `docker build .`) than it runs a script.
func isToolWord(w Word, dir string) bool {
	program := programName(w)
	runner := toolOf(w) != NoTool || isShell(w) || program == "eval" || program == "source"
	if !w.Literal || !runner {
		return false
	}

	if !strings.Contains(w.Text, "/") {
		return true
	}

	name := w.Text
	if !filepath.IsAbs(name) {
		if dir == "" {
			return true
		}

		name = opened(dir, name)
	}

	info, err := os.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}

	if err != nil {
		return true
	}

	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// shellHandovers are the commands that hand their words to a shell, which reads them all as one
// script.
func shellHandovers() map[string]bool {
	return setOf("ssh", "su")
}

// interpreters are the command words that run code in a language other than shell from their
// arguments.
func interpreters() map[string]bool {
	return setOf("sed", "awk", "perl", "python", "python3", "node", "ruby")
}

// isInterpreter reports whether command is the command word of an interpreter, `python3.<minor>`
// included.
func isInterpreter(command Word) bool {
	if !command.Literal {
		return false
	}

	if minor, ok := strings.CutPrefix(command.Text, "python3."); ok {
		return minor != "" && strings.Trim(minor, "0123456789") == ""
	}

	return interpreters()[command.Text]
}

// expansionWord stands for an expansion in the text of an argument read as a script: one plain word
// that names no tool.
const expansionWord = "_"

// scriptText returns the text a command receives as the argument raw, read by ex, with each
// expansion the shell makes in it standing as its value when ex knows it, and as expansionWord
// otherwise. A command that runs a string (`su -c "git push $b"`) runs it with the expansion
// already made, so what it holds stays a word of the script.
func scriptText(raw *syntax.Word, ex expander) string {
	parts := make([]syntax.WordPart, 0, len(raw.Parts))
	for _, part := range raw.Parts {
		switch p := part.(type) {
		case *syntax.Lit, *syntax.SglQuoted:
			parts = append(parts, p)
		case *syntax.DblQuoted:
			inner := make([]syntax.WordPart, 0, len(p.Parts))
			for _, q := range p.Parts {
				if _, known := knownExpansion(q, ex.vars); !known {
					q = &syntax.Lit{Value: expansionWord}
				}

				inner = append(inner, q)
			}

			parts = append(parts, &syntax.DblQuoted{Parts: inner, Dollar: p.Dollar})
		default:
			value, known := knownExpansion(p, ex.vars)
			if !known {
				value = expansionWord
			}

			parts = append(parts, &syntax.SglQuoted{Value: value})
		}
	}

	return readWord(&syntax.Word{Parts: parts}, ex).Text
}

// knownExpansion returns the value part, a part of a word, stands for when it is a literal or an
// expansion of a variable vars knows, and reports whether it is one of those.
func knownExpansion(part syntax.WordPart, vars shellVars) (string, bool) {
	switch p := part.(type) {
	case *syntax.Lit:
		return p.Value, true
	case *syntax.ParamExp:
		return vars.expansion(p)
	default:
		return "", false
	}
}

// shellOperatorChars are the characters that make text more than words: a newline, a list or pipe
// operator, a redirect, a substitution, or a subshell.
const shellOperatorChars = "\n;&|()<>`$"

// shellWordBreaks are the characters that split text into several shell words or make it more than
// words: a blank, or one of shellOperatorChars.
const shellWordBreaks = " \t" + shellOperatorChars

// stringRunners are the command words that run a string or a file as shell commands, besides the
// shells.
func stringRunners() map[string]bool {
	return setOf("eval", "source", ".")
}

// scriptTool reads text as a shell script and returns the tool one of its commands is, or hands
// over as handedBy reads it, and reports whether there is one. Text with no blank and no shell
// operator is one word, which handedBy has already judged. Text that does not parse is no
// script: it is data, whatever command it goes to. A command word that is not a literal is passed
// over: the guard cannot tell what a variable handed to another command holds. A relative path in
// the script is read as unknown, since the command that runs it may run it anywhere.
func scriptTool(text, home string) (string, bool) {
	if !strings.ContainsAny(text, shellWordBreaks) {
		return "", false
	}

	file, err := read(text)
	if err != nil {
		return "", false
	}

	ex := expander{home: home}
	var name string
	syntax.Walk(file, func(node syntax.Node) bool {
		if name != "" {
			return false
		}

		stmt, ok := node.(*syntax.Stmt)
		if !ok {
			return true
		}

		call, ok := stmt.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}

		inv := invocationOf(stmt, call, ex)
		command := inv.Words[0]
		switch {
		case !command.Literal, isDataConsumer(inv.Words):
		case inv.Tool == ILS && changesNothing(inv.Words[1:]):
		case inv.Tool != NoTool, isShell(command), stringRunners()[command.Text]:
			name = command.Text
		default:
			name, _ = handedBy(inv.Words, argsOf(call, inv.Words), "", ex)
		}

		return name == ""
	})

	return name, name != ""
}

// changesNothing reports whether args, the words after an ils word, name an ils command that
// changes nothing, as Cobra finds the command. A command word that is not a literal, or that is
// no command of ils, may become any command once another command fills it in (`find -exec ils
// {}`), so it changes something.
func changesNothing(args []Word) bool {
	at := commandWords(args, ilsSwitches())
	if len(at) == 0 {
		return false
	}

	sub := args[at[0]]

	return inTable(readOnlyILSCommands(), sub)
}

// readOnlyILSCommands are the commands of ils that change nothing. `help` is Cobra's own command,
// which ils never declares.
func readOnlyILSCommands() map[string]bool {
	return setOf(
		landing.StatusCommand, commandline.ParseCommand, commandline.GuardCommand, commandline.HookCommand, "help",
	)
}
