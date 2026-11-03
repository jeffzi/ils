//go:build unix

package guard

import (
	"slices"
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
// non-literal word where go reads `-C`: either may be `run`, `generate`, or `-C=dir`. The commands
// that the flags in goCommandFlags hand to go are read by goCommands.
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

// marksOf returns what Parse cannot read in the invocation inv of call, apart from the string it
// runs. A `~` in a word expands to home.
func marksOf(call *syntax.CallExpr, stmt *syntax.Stmt, inv Invocation, home string) []Mark {
	words := slices.Clone(call.Args)
	for _, a := range call.Assigns {
		words = append(words, a.Value)
	}

	var marks []Mark
	if hasExtGlob(words, stmt) {
		marks = append(marks, Mark{Kind: ExtendedGlob})
	}

	if len(inv.Words) == 0 {
		return marks
	}

	command := inv.Words[0]
	switch {
	case !command.Literal:
		marks = append(marks, Mark{Kind: DynamicCommand})
	case inv.Tool != NoTool, isDataConsumer(inv.Words), isShell(command), command.Text == "eval":
	case command.Text == "source" || command.Text == ".":
		if len(inv.Words) > 1 {
			marks = append(marks, Mark{Kind: HiddenScript})
		}
	default:
		if name, ok := handedByCall(call, inv.Words, inv.Dir, home); ok {
			marks = append(marks, Mark{Kind: ToolArgument, Name: name})
		}
	}

	return marks
}

// handedByCall returns the tool that the command call, a command the guard does not know whose
// words after any wrapper are words, may run from its arguments, and reports whether there is one.
// It reads the arguments as handedBy does, except that a bare shell word followed by `-c` and a
// literal string hands over only what the string hands over (`xargs -I{} sh -c 'ls {}'`, `docker
// run img sh -c 'make test'`). The string runs where the command puts it, so a git call in it is
// never judged as if it ran here: holding a tool word, or a command that hands one over in turn,
// is enough to hand one over. dir is where a relative path is read against, and a `~` expands to
// home.
func handedByCall(call *syntax.CallExpr, words []Word, dir, home string) (string, bool) {
	args := argsOf(call, words)
	if !isInterpreter(words[0]) {
		words, args = withoutShellStrings(words, args)
	}

	return handedBy(words, args, dir, home)
}

// argsOf returns the arguments of call that words, its words after any wrapper, were read from.
func argsOf(call *syntax.CallExpr, words []Word) []*syntax.Word {
	return call.Args[len(call.Args)-len(words):]
}

// withoutShellStrings returns words and args, the raw word each of words was read from, without
// each bare shell word that is followed by `-c` and a literal string, and without that `-c`. The
// string stays, to be read as any other argument. A shell word with no `-c`, or whose string is not
// a literal, stays as well, and so does a path to a shell.
func withoutShellStrings(words []Word, args []*syntax.Word) ([]Word, []*syntax.Word) {
	keptWords := []Word{words[0]}
	keptArgs := []*syntax.Word{args[0]}
	for i := 1; i < len(words); i++ {
		shell := words[i]
		if shell.Literal && slices.Contains(shellNames(), shell.Text) && i+2 < len(words) &&
			words[i+1].Literal && words[i+1].Text == "-c" && words[i+2].Literal {
			i += 2
		}

		keptWords = append(keptWords, words[i])
		keptArgs = append(keptArgs, args[i])
	}

	return keptWords, keptArgs
}

// handedBy returns the tool that a command the guard does not know, whose words are words read
// from args, may run from its arguments, and reports whether there is one. An argument hands over
// a tool when it is a tool word itself, or when it reads as a shell script one of whose commands
// is a tool word or hands one over in turn: `su -c 'cd /repo && git push'`, `ssh host 'sudo git
// push'`. The arguments of an interpreter are code in its own language, never read as a shell
// script, so only a tool word among them hands one over. An ils word followed by an ils command
// that changes nothing hands over nothing. dir is where a relative path is read against, and a `~`
// expands to home.
func handedBy(words []Word, args []*syntax.Word, dir, home string) (string, bool) {
	readsScripts := !isInterpreter(words[0])
	for i, w := range words[1:] {
		if isToolWord(w, dir) {
			if toolOf(w) == ILS && changesNothing(words[i+2:]) {
				continue
			}

			return w.Text, true
		}

		if !readsScripts {
			continue
		}

		if name, ok := scriptTool(scriptText(args[i+1], home), home); ok {
			return name, true
		}
	}

	return "", false
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

// scriptText returns the text a command receives as the argument raw, with each expansion the
// shell makes in it standing as expansionWord. A command that runs a string (`su -c "git push $b"`)
// runs it with the expansion already made, so what it holds stays a word of the script.
func scriptText(raw *syntax.Word, home string) string {
	parts := make([]syntax.WordPart, 0, len(raw.Parts))
	for _, part := range raw.Parts {
		switch p := part.(type) {
		case *syntax.Lit, *syntax.SglQuoted:
			parts = append(parts, p)
		case *syntax.DblQuoted:
			inner := make([]syntax.WordPart, 0, len(p.Parts))
			for _, q := range p.Parts {
				if _, ok := q.(*syntax.Lit); !ok {
					q = &syntax.Lit{Value: expansionWord}
				}

				inner = append(inner, q)
			}

			parts = append(parts, &syntax.DblQuoted{Parts: inner, Dollar: p.Dollar})
		default:
			parts = append(parts, &syntax.Lit{Value: expansionWord})
		}
	}

	return readWord(&syntax.Word{Parts: parts}, home).Text
}

// shellOperatorChars are the characters that make text more than words: a newline, a list or pipe
// operator, a redirect, a substitution, or a subshell.
const shellOperatorChars = "\n;&|()<>`$"

// shellWordBreaks are the characters that split text into several shell words or make it more than
// words: a blank, or one of shellOperatorChars.
const shellWordBreaks = " \t" + shellOperatorChars

// stringRunners are the command words that run a string or a file as shell commands, besides the
// shells.
func stringRunners() []string {
	return []string{"eval", "source", "."}
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

		inv := invocationOf(stmt, call, home)
		command := inv.Words[0]
		switch {
		case !command.Literal, isDataConsumer(inv.Words):
		case inv.Tool == ILS && changesNothing(inv.Words[1:]):
		case inv.Tool != NoTool, isShell(command), slices.Contains(stringRunners(), command.Text):
			name = command.Text
		default:
			name, _ = handedBy(inv.Words, argsOf(call, inv.Words), "", home)
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

	return sub.Literal && slices.Contains(readOnlyILSCommands(), sub.Text)
}

// readOnlyILSCommands are the commands of ils that change nothing. `help` is Cobra's own command,
// which ils never declares.
func readOnlyILSCommands() []string {
	return []string{
		landing.StatusCommand, commandline.ParseCommand, commandline.GuardCommand, commandline.HookCommand, "help",
	}
}
