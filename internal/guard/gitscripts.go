//go:build unix

package guard

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/jeffzi/ils/internal/child"
)

// resolveGit asks git, for each git invocation the walk placed, which git dir it targets and what
// the aliases it names expand to, then walks the shell commands it has git run. Their invocations
// are placed right after it and read in turn, so an alias that expands to `rebase --exec` is
// followed like one written out. The caller's git runs each call, from the invocation's directory
// and with the GIT_DIR and GIT_WORK_TREE the guard modeled for it. An invocation of any other tool
// starts no git.
func (w *walker) resolveGit(ctx context.Context) error {
	for i := 0; i < len(w.walked); i++ {
		inv := &w.walked[i]
		if inv.Tool != Git {
			continue
		}

		if err := resolveInvocation(ctx, w.caller.Git, inv); err != nil {
			return fmt.Errorf("read the git invocation %q: %w", wordTexts(inv.Words), err)
		}

		if err := w.gitScripts(ctx, i); err != nil {
			return err
		}
	}

	return nil
}

// gitScripts walks the shell commands that the resolved git invocation at index i of the walk has
// git run, one level deeper, and places their invocations right after it: returned after it when
// it is returned, and walked only when it belongs to a dry run. It marks a command it cannot read
// on the invocation instead. Each command is walked from the directory git starts it in: the top
// of the invocation's work tree, read through the caller's git, for one git starts there; a
// directory the guard does not know for any other, and a repository it does not know as well for
// one git runs outside the invocation's repository.
func (w *walker) gitScripts(ctx context.Context, i int) error {
	inv := &w.walked[i]
	runs := slices.Concat(shellRuns(inv), readableRuns(inv))

	top, err := w.topFor(ctx, *inv, runs)
	if err != nil {
		return err
	}

	sub := newWalker(w.caller, inv.depth)
	sub.loopBodies = w.loopBodies
	for _, run := range runs {
		sc := gitScope(*inv)
		switch run.start {
		case startTop:
			sc.dir = top
		case startElsewhere:
			sc.dir, sc.env = "", unknownEnvironment()
		case startUnknown:
			sc.dir = ""
		}

		if _, err := sub.walkString(run.script.Text, &sc); err != nil {
			return fmt.Errorf("parse the string git runs: %w", err)
		}
	}

	w.splice(i, sub)

	return nil
}

// topFor returns the top of the work tree of the git invocation inv when one of runs starts there,
// which git moves to before it runs a subcommand that needs a work tree. It returns "" when none
// starts there or the guard cannot tell where the top is: a target it cannot follow, or a call git
// fails, as it does outside a work tree and in a bare repository. A failure to run git at all is an
// error.
func (w *walker) topFor(ctx context.Context, inv Invocation, runs []gitRun) (string, error) {
	if !slices.ContainsFunc(runs, func(run gitRun) bool { return run.start == startTop }) {
		return "", nil
	}

	options, known := targetOptions(inv)
	if !known {
		return "", nil
	}

	result, err := targetRunner(w.caller.Git, inv.Dir, inv.Target).Run(ctx, append(options, "rev-parse", "--show-toplevel")...)
	if _, failed := errors.AsType[*child.ExitError](err); failed {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("read the top of the work tree of %q: %w", wordTexts(inv.Words), err)
	}

	return filepath.FromSlash(strings.TrimSpace(result.Stdout)), nil
}

// shellRuns returns the commands of shellCommandsOf that the guard walks, and marks inv when one of
// them is not a literal or they would nest too deep.
func shellRuns(inv *Invocation) []gitRun {
	commands := shellCommandsOf(*inv)
	scripts := make([]Word, 0, len(commands))
	for _, run := range commands {
		scripts = append(scripts, run.script)
	}

	if len(runnableScripts(inv, inv.depth, scripts)) == 0 {
		return nil
	}

	var runs []gitRun
	for _, run := range commands {
		if run.script.Literal {
			runs = append(runs, run)
		}
	}

	return runs
}

// readableRuns returns the commands the git invocation inv has git run from its arguments that the
// guard can walk, and marks each other one on inv, naming its form.
func readableRuns(inv *Invocation) []gitRun {
	var runs []gitRun
	for _, run := range argumentRuns(*inv) {
		before := len(inv.Marks)
		for _, script := range runnableScripts(inv, inv.depth, []Word{run.script}) {
			run.script = Word{Text: script, Literal: true}
			runs = append(runs, run)
		}

		for k := before; k < len(inv.Marks); k++ {
			inv.Marks[k].Name = run.form
		}
	}

	return runs
}

// splice places the invocations sub walked right after the one at index i of the walk, and those
// sub keeps right after it among the kept ones when it is kept itself. Each is atCall when sub
// walked it at a call site or when the one at index i is.
func (w *walker) splice(i int, sub walker) {
	n := len(sub.walked)
	if n == 0 {
		return
	}

	for k := range sub.walked {
		sub.walked[k].atCall = sub.walked[k].atCall || w.walked[i].atCall
	}

	w.walked = slices.Insert(w.walked, i+1, sub.walked...)
	at := -1
	for k, index := range w.kept {
		switch {
		case index == i:
			at = k
		case index > i:
			w.kept[k] += n
		}
	}

	if at < 0 {
		return
	}

	kept := make([]int, 0, len(sub.kept))
	for _, index := range sub.kept {
		kept = append(kept, i+1+index)
	}

	w.kept = slices.Insert(w.kept, at+1, kept...)
}

// gitScope returns the scope a shell command run by the git invocation inv starts in: the directory
// git works in, the GIT_DIR and GIT_WORK_TREE git runs with, and the redirects git runs with.
// CDPATH is not followed into git's environment, so it is unknown.
func gitScope(inv Invocation) scope {
	sc := scope{
		dir:       inv.Target.Dir,
		env:       unknownEnvironment(),
		redirects: slices.Concat(inv.enclosing, inv.Redirects),
	}
	if !inv.Target.Unknown {
		sc.env.gitDir = pathVariable(inv.Target.GitDir)
		sc.env.workTree = pathVariable(inv.Target.WorkTree)
	}

	return sc
}

// pathVariable returns the variable that holds path, unset when path is empty.
func pathVariable(path string) variable {
	if path == "" {
		return variable{}
	}

	return variable{value: path, set: true}
}

// shellCommandsOf returns the shell commands the git invocation inv has git run, read once its
// aliases expand: the editor given by `-c` or `--config-env`, and each `rebase --exec`. git starts
// a rebase's commands at the top of the work tree, rebase being a command that needs one. An editor
// starts in a directory the guard does not know.
func shellCommandsOf(inv Invocation) []gitRun {
	var runs []gitRun
	for _, command := range configCommands(inv) {
		runs = append(runs, gitRun{script: command})
	}

	if sub, ok := subcommand(inv); ok && sub == "rebase" {
		runs = append(runs, topRuns(inv.Git.Args, "rebase", "--exec")...)
	}

	return runs
}

// runnableScripts returns the text of each of values, the shell commands inv has something run at
// depth levels of nesting, that is walked: none when they would nest past maxDepth. It marks inv
// when one of them is not a literal or they would nest too deep.
func runnableScripts(inv *Invocation, depth int, values []Word) []string {
	var scripts []string
	var mark MarkKind
	for _, v := range values {
		if !v.Literal {
			mark = DynamicScript

			continue
		}

		scripts = append(scripts, v.Text)
	}

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

// configCommands returns the shell command of each `-c` and `--config-env` option of the git
// invocation inv that sets `core.editor` or `sequence.editor`, the editors git runs through the
// shell. The command is not a literal when the `-c` value is not one, and never for a
// `--config-env`, whose value the guard does not read. An option whose key the guard cannot read
// is left to the rules that judge the subcommand.
func configCommands(inv Invocation) []Word {
	editors := []string{"core.editor", "sequence.editor"}
	var scripts []Word
	for _, o := range inv.Git.Options {
		key, rest, cut := configKey(o)
		if !cut || !containsFold(editors, key) {
			continue
		}

		scripts = append(scripts, Word{Text: rest, Literal: o.Name == "-c" && o.Value.Literal})
	}

	return scripts
}

func containsFold(names []string, s string) bool {
	return slices.ContainsFunc(names, func(name string) bool { return strings.EqualFold(name, s) })
}

// couldBeXOption reports whether a word that starts with text, the rest unknown, could be `-x` or
// the long option long once the rest is known, where text alone does not already name one: an
// empty text, a short-flag bundle holding none of valued, the short options that take a value
// with `x` among them, or a prefix of long with no value.
func couldBeXOption(text, long, valued string) bool {
	switch {
	case strings.HasPrefix(text, "--"):
		return !strings.Contains(text, "=") && strings.HasPrefix(long, text)
	case strings.HasPrefix(text, "-"):
		return !strings.ContainsAny(text[1:], valued)
	default:
		return text == ""
	}
}

// startDir is where git starts a command that a git invocation has it run.
type startDir int

const (
	// startUnknown is a directory of the invocation's repository the guard does not know.
	startUnknown startDir = iota
	// startTop is the top of the invocation's work tree, where git moves before it runs a
	// subcommand that needs one.
	startTop
	// startElsewhere is outside the invocation's directory and repository: in each submodule, in
	// each repository a config key lists, or in filter-branch's scratch tree.
	startElsewhere
)

// gitRun is a command that a git invocation has git run from its arguments or its config.
type gitRun struct {
	// form names the argument as the invocation gives it, such as `bisect run` or `--tree-filter`.
	form string
	// script is the shell command git runs; not a literal when the guard cannot read it.
	script Word
	start  startDir
}

// argumentRuns returns the commands the git invocation inv has git run from its arguments, once its
// aliases expand, other than `rebase --exec`.
func argumentRuns(inv Invocation) []gitRun {
	runs := map[string]func([]Word) []gitRun{
		"bisect":        bisectRuns,
		"submodule":     submoduleRuns,
		"filter-branch": filterBranchRuns,
		"for-each-repo": forEachRepoRuns,
	}
	sub, ok := subcommand(inv)
	if read, known := runs[sub]; ok && known {
		return read(inv.Git.Args)
	}

	return nil
}

// topRuns returns the value of each `-x` or long option among args, the arguments of the git
// subcommand sub, the commands git runs at the top of the work tree: separate or attached, the long
// option spelled as any prefix of long git could read as it, and `-x` in a short-flag bundle ahead
// of any other of the subcommand's short options that take a value, whose rest is then its value.
// Each run's form is the option as written. A word the guard cannot read that git could read as
// one of these options is a run the guard cannot read either, whose form is sub.
func topRuns(args []Word, sub, long string) []gitRun {
	valued := optionsOf(sub).valuedLetters
	x := valuedOption{letter: 'x', valued: valued, long: long, shortest: len("--e")}

	var runs []gitRun
	for _, v := range x.values(args, func(text string) bool { return couldBeXOption(text, long, valued) }) {
		if v.unread {
			runs = append(runs, gitRun{form: sub, start: startTop})

			continue
		}

		runs = append(runs, gitRun{form: v.form, script: v.value, start: startTop})
	}

	return runs
}

// argvScript returns the shell command that runs words as one command, each word one argument; not
// a literal when one of words is not, or holds what the shell cannot quote.
func argvScript(words []Word) Word {
	quoted := make([]string, 0, len(words))
	for _, w := range words {
		if !w.Literal {
			return Word{}
		}

		q, err := syntax.Quote(w.Text, syntax.LangBash)
		if err != nil {
			return Word{}
		}

		quoted = append(quoted, q)
	}

	return Word{Text: strings.Join(quoted, " "), Literal: true}
}

// bisectRuns returns the command of `git bisect run` among the arguments of `git bisect`, which git
// runs with each word one argument, at the top of the work tree. A newer git reads a leading `--reset-when-found` as its option;
// an older one runs it as the command's name, which runs nothing the guard judges.
func bisectRuns(args []Word) []gitRun {
	switch {
	case len(args) == 0:
		return nil
	case !args[0].Literal:
		return []gitRun{{form: "bisect"}}
	case args[0].Text != "run":
		return nil
	}

	words := args[1:]
	if len(words) > 0 && words[0].Literal {
		if option, _, _ := strings.Cut(words[0].Text, "="); option == "--reset-when-found" {
			words = words[1:]
		}
	}

	if len(words) == 0 {
		return nil
	}

	return []gitRun{{form: "bisect run", script: argvScript(words), start: startTop}}
}

// submoduleRuns returns the command of `git submodule foreach` among the arguments of `git
// submodule`: the first word after the options of both, which git runs as a shell command given
// the words after it as its arguments.
func submoduleRuns(args []Word) []gitRun {
	for i := 0; i < len(args); i++ {
		switch w := args[i]; {
		case !w.Literal:
			return []gitRun{{form: "submodule", start: startElsewhere}}
		case w.Text == "foreach":
			return foreachRun(args[i+1:])
		case w.Text == "-b" || w.Text == "--branch":
			i++
		case w.Text == "--" || !strings.HasPrefix(w.Text, "-"):
			return nil
		}
	}

	return nil
}

// foreachRun returns the command `git submodule foreach` runs given the words args after it.
func foreachRun(args []Word) []gitRun {
	at := slices.IndexFunc(args, func(w Word) bool { return !w.Literal || !strings.HasPrefix(w.Text, "-") })
	switch {
	case at < 0:
		return nil
	case !args[at].Literal:
		return []gitRun{{form: "submodule foreach", start: startElsewhere}}
	}

	script := args[at]
	if rest := args[at+1:]; len(rest) > 0 {
		quoted := argvScript(rest)
		script = Word{Text: script.Text + " " + quoted.Text, Literal: quoted.Literal}
	}

	return []gitRun{{form: "submodule foreach", script: script, start: startElsewhere}}
}

// filterBranchRuns returns the value of each filter among the arguments of `git filter-branch`, and
// of `--setup`, all of which it runs as shell commands. filter-branch reads only exact option names,
// each but a few flags taking the next word as its value, and stops at the first other word.
func filterBranchRuns(args []Word) []gitRun {
	scripts := setOf(
		"--setup", "--env-filter", "--tree-filter", "--index-filter", "--parent-filter",
		"--msg-filter", "--commit-filter", "--tag-name-filter",
	)
	flags := setOf("-f", "--force", "--remap-to-ancestor", "--prune-empty")
	var runs []gitRun
	for i := 0; i < len(args); i++ {
		w := args[i]
		switch {
		case !w.Literal:
			return append(runs, gitRun{form: "filter-branch", start: startElsewhere})
		case flags[w.Text]:
			continue
		case w.Text == "--" || !strings.HasPrefix(w.Text, "-") || i+1 == len(args):
			return runs
		}

		i++
		if scripts[w.Text] {
			runs = append(runs, gitRun{form: w.Text, script: args[i], start: startElsewhere})
		}
	}

	return runs
}

// forEachRepoRuns returns the git command `git for-each-repo` runs in each repository it lists: git
// given the words after its options, each word one argument.
func forEachRepoRuns(args []Word) []gitRun {
	const config = "--config"
	for i := 0; i < len(args); i++ {
		w := args[i]
		name, _, attached := strings.Cut(w.Text, "=")
		switch {
		case !w.Literal:
			return []gitRun{{form: "for-each-repo", start: startElsewhere}}
		case w.Text == "--":
			return forEachRepoRun(args[i+1:])
		case !strings.HasPrefix(w.Text, "-"):
			return forEachRepoRun(args[i:])
		case !attached && abbreviates(name, config, len("--c")):
			i++
		}
	}

	return nil
}

// forEachRepoRun returns the git command `git for-each-repo` runs given the words argv.
func forEachRepoRun(argv []Word) []gitRun {
	if len(argv) == 0 {
		return nil
	}

	script := argvScript(argv)
	if script.Literal {
		script.Text = "git " + script.Text
	}

	return []gitRun{{form: "for-each-repo", script: script, start: startElsewhere}}
}
