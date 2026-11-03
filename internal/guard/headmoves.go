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
// holding no substitution, and no call of a function, which may switch nothing. A background job
// or pipe stage runs beside the commands after it, so a switch in one claims nothing.
func (w *walker) followSwitch(operand *syntax.Stmt, at int, functions map[string]*function, sc *scope) {
	if _, ok := operand.Cmd.(*syntax.CallExpr); !ok || sc.racing || operand.Negated || operand.Background ||
		operand.Coprocess || len(w.kept)-at != 1 {
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
	sub := inv.Git.Subcommand
	if inv.Tool != Git || len(inv.Wrappers) > 0 || len(inv.Marks) > 0 || inv.Git.NoSubcommand ||
		!sub.Literal || sub.Text != "switch" {
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
	var operands []Word
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
			operands = append(operands, arg)
		}
	}

	switch {
	case created && len(operands) <= 1:
		return name, true
	case !created && len(operands) == 1:
		return operands[0], true
	default:
		return Word{}, false
	}
}

// returned returns the invocations Parse returns, each reporting whether an invocation walked
// before it may move HEAD, and the branch a switch earlier in its `&&` list left HEAD on. It reads
// each git invocation once its aliases expand, so it runs after resolveGit. The walk order is the
// order the shell may run them in: the substitutions of a command come before it, a dry run of a
// body that may repeat before the body itself, and a dry run of a background job or pipe stage
// before the commands already walked.
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
// in a background job or pipe stage may come between any two commands, so no switch holds after
// one.
func markHeads(walked []Invocation) {
	moved, raced := false, false
	var last *headClaim
	lastGitDir := ""
	for i := range walked {
		inv := &walked[i]
		inv.HeadUnknown = moved
		if inv.follows != nil && inv.follows == last && !raced && lastGitDir != "" && inv.Git.GitDir == lastGitDir {
			inv.HeadUnknown, inv.head = false, last.branch
		}

		if movesHead(*inv) {
			moved, raced = true, raced || inv.racing
			last, lastGitDir = inv.sets, inv.Git.GitDir
		}
	}
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
		valued := optionsOf("branch").valuedLetters

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
func isBranchMove(arg Word, valued string) bool {
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
	valued := "St"
	if command == "install" {
		valued += "mog"
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
