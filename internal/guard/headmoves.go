//go:build unix

package guard

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// race walks body, a background job or a pipe, which runs beside the commands around it: no order
// holds between them, so what it does to HEAD is unknown to every invocation of the command. A dry
// run of body is placed first in the walk, ahead of the invocations already walked, so that each
// one after it reports HEAD unknown when body may move HEAD. Inside a dry run or another race,
// body is walked once.
//
// Placing the dry run shifts every index into the walk that w.kept holds. An index held anywhere
// else across a walk that may reach a race goes stale, so none may be.
func (w *walker) race(sc *scope, body func(*scope)) {
	if w.probing || sc.racing {
		body(sc)

		return
	}

	w.probing = true
	walked, kept := len(w.walked), len(w.kept)
	dry := *sc
	dry.racing = true
	body(&dry)
	w.kept = w.kept[:kept]
	w.probing = false

	ahead := len(w.walked) - walked
	w.walked = moveBehind(w.walked, 0, walked)
	for k := range w.kept {
		w.kept[k] += ahead
	}

	sc.racing = true
	body(sc)
	sc.racing = false
}

// job walks stmt, a background job or coprocess, and then rest, the statements after it in its
// list, from sc. The job runs beside every command of rest, so it is walked after them, from the
// shell as it is where the job starts, and each of its invocations reports HEAD unknown when one
// of theirs may move HEAD. Its invocations are still returned ahead of theirs.
func (w *walker) job(stmt *syntax.Stmt, rest []*syntax.Stmt, sc *scope) {
	inner := *sc
	at := len(w.kept)
	w.list(rest, sc)
	if w.err != nil {
		return
	}

	after := len(w.kept)
	w.race(&inner, func(s *scope) { w.stmtInPlace(stmt, s) })
	w.kept = moveBehind(w.kept, at, after)
}

// movesHead reports whether the invocation inv may move HEAD: by writing the HEAD file of a git
// dir, or as a git invocation read by the subcommand its aliases expand to. A git invocation whose
// subcommand Parse could not resolve may run any command, so it may move HEAD.
func movesHead(inv Invocation) bool {
	if writesHead(inv) {
		return true
	}

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
		valued := []byte(optionsOf("branch").valuedLetters)

		return slices.ContainsFunc(args, func(arg Word) bool { return isBranchMove(arg, valued) })
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
// short-flag bundle ahead of any of valued, the short options that take a value, whose rest is
// then that value, or a prefix of `--move` git reads as it.
func isBranchMove(arg Word, valued []byte) bool {
	text := arg.Text
	switch {
	case !arg.Literal || !strings.HasPrefix(text, "-"):
		return false
	case strings.HasPrefix(text, "--"):
		const move = "--move"

		return abbreviates(text, move, len("--mo"))
	default:
		_, move, _ := shortValue(arg, 'm', valued)
		_, forced, _ := shortValue(arg, 'M', valued)

		return move || forced
	}
}

// writesHead reports whether inv may write the HEAD file of a git dir: through one of the redirects
// in effect where it runs, or as a command other than git, gh, or ils given a word that may name
// that file, unless it is one known to write no file. `cp` and `install` write only what they copy
// into, and `mv` and `ln` may also write it into a git dir they are given. A path that is not a
// literal may name that file.
func writesHead(inv Invocation) bool {
	for _, r := range slices.Concat(inv.enclosing, inv.Redirects) {
		if writesFile(r) && mayBeHead(inv.Dir, r.Target) {
			return true
		}
	}

	if len(inv.Words) == 0 || inv.Tool != NoTool {
		return false
	}

	command, args := "", inv.Words[1:]
	if inv.Words[0].Literal {
		command = programName(inv.Words[0])
	}

	intoGitDir := func(w Word) bool { return mayBeGitDir(inv.Dir, w) }
	switch {
	case writesNoFile()[command]:
		return false
	case command == "cp" || command == "install":
		return slices.ContainsFunc(copyDestinations(command, args), func(w Word) bool {
			return mayBeHead(inv.Dir, w) || intoGitDir(w)
		})
	case (command == "mv" || command == "ln") && slices.ContainsFunc(copyDestinations(command, args), intoGitDir):
		return true
	default:
		return slices.ContainsFunc(args, func(w Word) bool { return namesHead(inv.Dir, w) })
	}
}

// writesNoFile returns the commands known to write no file whatever words they are given: those
// that only read the files they name, those that only print, and the declaration builtins. A
// command that has an option to write a file, such as `less -o` or `sort -o`, is not one of them.
func writesNoFile() map[string]bool {
	return setOf(
		"cat", "head", "tail", "more", "grep", "rg", "wc", "file", "stat", "ls", "diff", "cmp",
		"test", "[", "echo", "printf", "export", "declare", "local", "readonly", "typeset", "let",
	)
}

// namesHead reports whether the word w, read from dir, may name the HEAD file of a git dir, as a
// path or as the value of a `name=value` word such as `of=` or `--output=`.
func namesHead(dir string, w Word) bool {
	if mayBeHead(dir, w) {
		return true
	}

	_, value, valued := strings.Cut(w.Text, "=")

	return valued && mayBeHead(dir, Word{Text: value, Literal: true})
}

// writesFile reports whether the redirect r opens its target for writing.
func writesFile(r Redirect) bool {
	switch r.Op {
	case "<", "<<", "<<-", "<<<":
		return false
	case ">&", "<&":
		return !isDescriptor(r.Target)
	default:
		return true
	}
}

// operands returns the words of args that are not options: each one after `--`, and each other one
// that does not start with `-`. A word that is not a literal may be either, so it is one.
func operands(args []Word) []Word {
	var found []Word
	for i, arg := range args {
		switch {
		case arg.Literal && arg.Text == "--":
			return append(found, args[i+1:]...)
		case !arg.Literal || !strings.HasPrefix(arg.Text, "-") || arg.Text == "-":
			found = append(found, arg)
		}
	}

	return found
}

// copyDestinations returns the paths command, `cp`, `mv`, `ln` or `install`, given args may write
// into: the last operand, and the directory a `-t` option or a prefix of `--target-directory`
// names, read from a short-flag bundle as the rest of it or as the next word. `-t` counts only
// ahead of the command's other short options that take a value, whose value is the rest of the
// bundle. A word that is not a literal may be an option naming one, so it is one.
func copyDestinations(command string, args []Word) []Word {
	valued := []byte{'S', 't'}
	if command == "install" {
		valued = append(valued, 'm', 'o', 'g')
	}

	var found []Word
	if all := operands(args); len(all) > 0 {
		found = append(found, all[len(all)-1])
	}

	for i, arg := range args {
		text := arg.Text
		value, named := "", false
		switch {
		case !arg.Literal:
			found = append(found, arg)

			continue
		case text == "--":
			return found
		case strings.HasPrefix(text, "--"):
			const target = "--target-directory"
			name, attached, _ := strings.Cut(text, "=")
			named = abbreviates(name, target, len("--t"))
			value = attached
		case strings.HasPrefix(text, "-"):
			var short Word
			short, named, _ = shortValue(arg, 't', valued)
			value = short.Text
		}

		switch {
		case named && value != "":
			found = append(found, Word{Text: value, Literal: true})
		case named && i+1 < len(args):
			found = append(found, args[i+1])
		}
	}

	return found
}

// mayBeHead reports whether the path w, read from dir, may name the HEAD file of a git dir: it is
// not a literal, or its last element is HEAD, in any case since the filesystem may fold it, in a
// directory the guard cannot tell from a git dir.
func mayBeHead(dir string, w Word) bool {
	if !w.Literal {
		return true
	}

	parent, base := ".", w.Text
	if before, after, found := strings.CutLast(w.Text, "/"); found {
		parent, base = before, after
		if parent == "" {
			parent = "/"
		}
	}

	if !strings.EqualFold(base, "HEAD") {
		return false
	}

	path, known := resolved(dir, parent)

	return !known || gitDirAt(path, true)
}

// mayBeGitDir reports whether the path w, read from dir, may name a git dir, which a file copied
// into it may land in as its HEAD.
func mayBeGitDir(dir string, w Word) bool {
	if !w.Literal {
		return true
	}

	path, known := resolved(dir, w.Text)

	return !known || gitDirAt(path, false)
}

// resolved returns path read from dir, and false when it is relative and dir is unknown. It
// leaves `..` to the filesystem, which reads it past a symlink as the shell does.
func resolved(dir, path string) (string, bool) {
	switch {
	case filepath.IsAbs(path):
		return path, true
	case dir == "":
		return "", false
	default:
		return dir + "/" + path, true
	}
}

// gitDirAt reports whether path may be a git dir: a directory holding an entry git requires of
// one (objects, refs, or the commondir of a worktree's git dir), or one the guard cannot read.
// A path that does not exist yet may be one when missing is set, since an earlier command may
// create it.
func gitDirAt(path string, missing bool) bool {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return missing
	case err != nil:
		return true
	case !info.IsDir():
		return false
	}

	for _, entry := range []string{"objects", "refs", "commondir"} {
		if _, err := os.Lstat(path + "/" + entry); !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}

	return false
}
