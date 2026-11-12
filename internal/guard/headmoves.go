//go:build unix

package guard

import (
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// headClaim is a `git switch` an operand of an `&&` list makes: every later operand runs only once
// it succeeded, so HEAD is on branch there until something else moves it. Each switch walked has a
// claim of its own, so an invocation follows the switch whose claim it holds, wherever the walk
// moves either of them.
type headClaim struct {
	branch string
}

// followSwitch reads operand, an operand of an `&&` list whose invocations the walk kept from index
// at of w.kept on, run where functions are the functions a call may run, and has the later
// operands of the list run after its switch when it is a `git switch` switchedBranch reads. operand
// must be that switch alone, with a result that is its own: not negated, not in the background,
// holding no substitution, and no call of a function, which may switch nothing.
func (w *walker) followSwitch(operand *syntax.Stmt, at int, functions map[string][]*definition, sc *scope) {
	if _, ok := operand.Cmd.(*syntax.CallExpr); !ok || operand.Negated || runsAsJob(operand) ||
		len(w.kept)-at != 1 {
		return
	}

	inv := &w.walked[w.kept[at]]
	branch, ok := switchedBranch(*inv)
	if !ok || functions[inv.Words[0].Text] != nil {
		return
	}

	inv.sets = &headClaim{branch: branch}
	sc.head = inv.sets
}

// switchedBranch returns the branch the git invocation inv leaves HEAD on when it succeeds, and
// false unless inv is `git switch` written out, with no wrapper or mark, given a literal branch, or
// `-c`, `--create`, `-C` or `--force-create` with a literal name and at most a start point. Any
// other option may detach HEAD or name the branch another way, and `-` or a name holding `@{` is a
// branch git looks up in the reflog, so each leaves the branch unknown.
func switchedBranch(inv Invocation) (string, bool) {
	sub, ok := subcommand(inv)
	if !ok || sub != "switch" || len(inv.Wrappers) > 0 || len(inv.Marks) > 0 {
		return "", false
	}

	name, ok := switchName(inv.Git.Args)
	if !ok || !name.Literal || strings.HasPrefix(name.Text, "-") || strings.Contains(name.Text, "@{") {
		return "", false
	}

	return name.Text, true
}

// switchName returns the branch the arguments args of `git switch` name: the value of its one
// create option, or else its one operand. ok is false for any other option, a word that is not a
// literal, or operands past those.
func switchName(args []Word) (name Word, ok bool) {
	var positional []Word
	created := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case !arg.Literal:
			return Word{}, false
		case !created && i+1 < len(args) && slices.Contains([]string{"-c", "--create", "-C", "--force-create"}, arg.Text):
			created, name = true, args[i+1]
			i++
		case strings.HasPrefix(arg.Text, "-"):
			return Word{}, false
		default:
			positional = append(positional, arg)
		}
	}

	switch {
	case created && len(positional) <= 1:
		return name, true
	case !created && len(positional) == 1:
		return positional[0], true
	default:
		return Word{}, false
	}
}

// returned returns the invocations parse returns, those walked at a call site (atCall) included,
// each reporting whether an invocation walked before it may move HEAD, and the branch a switch
// earlier in its `&&` list left HEAD on. It reads each git invocation once its aliases expand, so
// it runs after resolveGit. The walk order is the order the shell may run them in: the
// substitutions of a command come before it, and a dry run of a body that may repeat before the
// body itself.
func (w *walker) returned() []Invocation {
	markHeads(w.walked)

	var invocations []Invocation
	for _, i := range w.kept {
		invocations = append(invocations, w.walked[i])
	}

	return invocations
}

// markHeads marks each invocation of walked, in walk order, with where HEAD is when it runs. HEAD
// is unknown once an invocation before it may move HEAD, and on the branch of the switch it
// follows when that switch is the last such invocation and points git at the same git dir. A move
// in a background job or pipe stage is not followed: it may come between any two commands, and
// no cooperative command moves HEAD there.
func markHeads(walked []Invocation) {
	moved := false
	var last *headClaim
	lastGitDir := ""
	for i := range walked {
		inv := &walked[i]
		inv.HeadUnknown = moved
		onClaimedBranch := inv.follows != nil && inv.follows == last && lastGitDir != "" && inv.Git.GitDir == lastGitDir
		if onClaimedBranch {
			inv.HeadUnknown, inv.head = false, last.branch
		}

		if !inv.beside && movesHead(*inv) {
			moved = true
			last, lastGitDir = inv.sets, inv.Git.GitDir
		}
	}
}

// movesHead reports whether the invocation inv may move HEAD, as a git invocation read by the
// subcommand its aliases expand to, or as `gh`. A git invocation whose subcommand Parse could not
// resolve may run any command, so it may move HEAD. A command other than git that writes the HEAD
// file of a git dir is not followed.
func movesHead(inv Invocation) bool {
	if inv.Tool == GH {
		return ghMovesHead(inv.Words[1:])
	}

	if inv.Tool != Git {
		return false
	}

	if slices.ContainsFunc(inv.Marks, func(m Mark) bool {
		return m.Kind == ShellAlias || m.Kind == UnknownSubcommand || m.Kind == DeepAlias ||
			m.Kind == UnreadableAlias
	}) {
		return true
	}

	sub, ok := subcommand(inv)
	if !ok {
		return false
	}

	args := inv.Git.Args
	switch sub {
	case "switch", "checkout", "bisect":
		// Every checkout counts, the one that restores files and leaves HEAD where it is too:
		// telling them apart means reading its operands as git does.
		return true
	case "symbolic-ref":
		// A second operand is the ref HEAD is set to.
		options := readArgs(args, optionsOf("symbolic-ref"))

		return options.unreadable || len(options.positional) > 1
	case "branch":
		return isBranchMove(args)
	case "rebase":
		_, named, unreadable := rebaseBranch(args)

		return named || unreadable
	default:
		return false
	}
}

// ghMovesHead reports whether gh given args may move HEAD: `gh pr checkout` by any name gh gives
// it, and a first command word that is none of gh's commands, since an alias or an extension may
// check out a branch. It finds the command words the way Cobra does, past the values of flags; one
// that is not a literal may be any of them.
func ghMovesHead(args []Word) bool {
	at := commandWords(args, ghSwitches())
	if len(at) == 0 {
		return false
	}

	first := args[at[0]]
	if !inTable(ghCommands(), first) {
		return true
	}

	if first.Text != "pr" || len(at) < 2 {
		return false
	}

	sub := args[at[1]]

	return !sub.Literal || ghSubcommand("pr", sub.Text) == "checkout"
}

// isBranchMove reports whether `git branch` given args may rename or move a branch: its options,
// read the way git reads them, hold `-m`, `-M` or `--move`, or hold a word the guard cannot read.
// A long option that starts several options, one of which takes a value, moves nothing: git
// refuses the command over it, so the word after it does not matter.
func isBranchMove(args []Word) bool {
	if slices.ContainsFunc(args, func(w Word) bool { return !w.Literal && !w.Glob }) {
		return true
	}

	known := optionsOf("branch")
	options := readArgs(args, known)
	if slices.ContainsFunc(options.long, func(l longOption) bool {
		_, _, ambiguous := known.resolve(l.name)

		return ambiguous
	}) {
		return false
	}

	return options.unreadable || options.sets('m', "--move") || options.sets('M', "")
}
