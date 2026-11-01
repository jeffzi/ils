//go:build unix

package guard

import (
	"slices"
	"strings"
)

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
		// A second operand is the ref HEAD is set to.
		options := readArgs(args, optionsOf("symbolic-ref"))

		return options.unreadable || len(options.positional) > 1
	case "branch":
		return slices.ContainsFunc(args, isBranchMove)
	case "rebase":
		_, named, unreadable := rebaseBranch(args)

		return named || unreadable
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

		return len(text) >= len("--mo") && strings.HasPrefix(move, text)
	default:
		bundle, _, _ := strings.Cut(text[1:], "u")

		return strings.ContainsAny(bundle, "mM")
	}
}
