//go:build unix

package guard

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/jeffzi/ils/internal/git"
)

// GitCall is how git reads the words of a git invocation: its global options, then the
// subcommand and the arguments after it.
type GitCall struct {
	// Options are the global options before the subcommand, in order.
	Options []GitOption
	// Args are the words after the subcommand, the words of each alias it expanded first.
	Args []Word
	// GitDir is the absolute git dir git finds for the invocation; empty when git finds no
	// repository, or when GitDirUnknown.
	GitDir string
	// missing names the global option that ends the words without the value it takes, which makes
	// git fail before it runs anything; empty when every option has its value.
	missing string
	// Subcommand is the command git runs once Parse has expanded the aliases it names. It is the
	// zero Word, which is not a literal, both when the subcommand word is not a literal and when
	// NoSubcommand.
	Subcommand Word
	// GitDirUnknown reports whether the guard cannot ask git for the git dir before the command
	// runs: the directory or a target option is not a literal or cannot be followed, or the
	// directory does not exist.
	GitDirUnknown bool
	// NoSubcommand reports whether git runs no subcommand: the words end after the global options,
	// or one of them makes git print and exit. It is false for a subcommand word that is not a
	// literal, so a rule that reads Subcommand without it still fails closed.
	NoSubcommand bool
}

// GitOption is one global option of git, named as written, such as `-C` or `--git-dir`, with its
// value; Value is zero for an option that takes none.
type GitOption struct {
	Name  string
	Value Word
}

// The kinds of mark Parse puts on a git invocation whose subcommand it cannot resolve.
const (
	// ShellAlias is an alias whose expansion starts with `!`, which git runs as a shell command.
	ShellAlias MarkKind = "git alias that runs a shell command"
	// UnknownSubcommand is a subcommand word that is neither one of git's commands nor an alias the
	// guard can read.
	UnknownSubcommand MarkKind = "git word that is neither a command nor an alias"
	// DeepAlias is an alias nested past the depth Parse expands.
	DeepAlias MarkKind = "git alias nested too deep"
	// UnreadableAlias is an alias Parse cannot read as git would: its value does not split into
	// words, or the invocation's `-c` or `--config-env` options give config the guard cannot follow.
	UnreadableAlias MarkKind = "git alias the guard cannot read"
	// MissingGitValue is a global option that ends the git invocation without the value it takes.
	MissingGitValue MarkKind = "git option missing its value"
)

// maxAliasDepth bounds the git config reads of one invocation and ends alias cycles: an alias
// chain that reaches it is marked DeepAlias instead of read further.
const maxAliasDepth = 3

// gitArity is how a global option of git takes its value.
type gitArity int

const (
	// gitFlag takes no value.
	gitFlag gitArity = iota + 1
	// gitValued takes the next word as its value, or, for a long option, a value attached with `=`.
	gitValued
	// gitAttached takes only a value attached with `=`; alone it prints and exits, as gitPrints
	// does.
	gitAttached
	// gitPrints makes git print information and exit, running no subcommand whatever words follow.
	gitPrints
)

// gitGlobalOptions is the table of git's global options, each with how it takes its value. An option
// missing here ends the option read and is taken as the subcommand, which is then marked.
func gitGlobalOptions() map[string]gitArity {
	return map[string]gitArity{
		"-C": gitValued, "-c": gitValued, "--git-dir": gitValued, "--work-tree": gitValued,
		"--namespace": gitValued, "--config-env": gitValued, "--attr-source": gitValued,
		"--exec-path": gitAttached, "--version": gitPrints, "--help": gitPrints,
		"--html-path": gitPrints, "--man-path": gitPrints, "--info-path": gitPrints,
		"-p": gitFlag, "-P": gitFlag, "--no-pager": gitFlag, "--paginate": gitFlag, "--bare": gitFlag,
		"--no-replace-objects": gitFlag, "--no-lazy-fetch": gitFlag, "--no-optional-locks": gitFlag,
		"--no-advice": gitFlag, "--literal-pathspecs": gitFlag, "--glob-pathspecs": gitFlag,
		"--noglob-pathspecs": gitFlag, "--icase-pathspecs": gitFlag,
	}
}

// gitCommands is the table of git 2.55's main commands, which git runs as they are whatever alias
// shares their name. Commands that only contrib or a platform's packaging installs are left out.
func gitCommands() map[string]bool {
	names := []string{
		"add", "am", "annotate", "apply", "archimport", "archive", "backfill", "bisect", "blame",
		"branch", "bugreport", "bundle", "cat-file", "check-attr", "check-ignore", "check-mailmap",
		"check-ref-format", "checkout", "checkout--worker", "checkout-index", "cherry", "cherry-pick",
		"clean", "clone", "column", "commit", "commit-graph", "commit-tree", "config", "count-objects",
		"credential", "credential-cache", "credential-cache--daemon", "credential-store",
		"cvsexportcommit", "cvsimport", "cvsserver", "daemon", "describe", "diagnose", "diff",
		"diff-files", "diff-index", "diff-pairs", "diff-tree", "difftool", "difftool--helper",
		"fast-export", "fast-import", "fetch", "fetch-pack", "filter-branch", "fmt-merge-msg",
		"for-each-ref", "for-each-repo", "format-patch", "format-rev", "fsck", "fsck-objects",
		"fsmonitor--daemon", "gc", "get-tar-commit-id", "grep", "hash-object", "help", "history",
		"hook", "http-backend", "http-fetch", "http-push", "imap-send", "index-pack", "init",
		"init-db", "instaweb", "interpret-trailers", "last-modified", "log", "ls-files", "ls-remote",
		"ls-tree", "mailinfo", "mailsplit", "maintenance", "merge", "merge-base", "merge-file",
		"merge-index", "merge-octopus", "merge-one-file", "merge-ours", "merge-recursive",
		"merge-recursive-ours", "merge-recursive-theirs", "merge-resolve", "merge-subtree",
		"merge-tree", "mergetool", "mktag", "mktree", "multi-pack-index", "mv", "name-rev", "notes",
		"p4", "pack-objects", "pack-redundant", "pack-refs", "patch-id", "pickaxe", "prune",
		"prune-packed", "pull", "push", "quiltimport", "range-diff", "read-tree", "rebase",
		"receive-pack", "reflog", "refs", "remote", "remote-ext", "remote-fd", "remote-ftp",
		"remote-ftps", "remote-http", "remote-https", "repack", "replace", "replay", "repo",
		"request-pull", "rerere", "reset", "restore", "rev-list", "rev-parse", "revert", "rm",
		"send-email", "send-pack", "sh-i18n--envsubst", "shell", "shortlog", "show", "show-branch",
		"show-index", "show-ref", "sparse-checkout", "stage", "stash", "status", "stripspace",
		"submodule", "submodule--helper", "switch", "symbolic-ref", "tag", "unpack-file",
		"unpack-objects", "update-index", "update-ref", "update-server-info", "upload-archive",
		"upload-archive--writer", "upload-pack", "url-parse", "var", "verify-commit", "verify-pack",
		"verify-tag", "version", "web--browse", "whatchanged", "worktree", "write-tree",
	}
	table := make(map[string]bool, len(names))
	for _, name := range names {
		table[name] = true
	}

	return table
}

// readGitCall returns how git reads the git invocation words, before any alias expands.
func readGitCall(words []Word) GitCall {
	options, at, missing := gitOptions(words)
	call := GitCall{Options: options, NoSubcommand: at >= len(words), missing: missing}
	if !call.NoSubcommand {
		call.Subcommand = words[at]
		call.Args = words[at+1:]
	}

	return call
}

// gitOptions returns the global options opening the git invocation words, in order, and the index
// in words of the subcommand after them: the first word that is not one of git's global options or
// the value of one, whatever it is spelled like. After an option that makes git print and exit, the
// index is len(words): git runs no subcommand. So it is after an option that takes a value and ends
// the words, which gitOptions returns as missing.
//
// Only a long option takes a value attached with `=`: git reads `-C=x` as an option it does not
// know, so gitOptions returns it as the subcommand.
func gitOptions(words []Word) (options []GitOption, at int, missing string) {
	table := gitGlobalOptions()
	i := 1
	for i < len(words) {
		w := words[i]
		name, value, attached := strings.Cut(w.Text, "=")
		arity := table[name]
		switch {
		case attached && strings.HasPrefix(name, "--") && (arity == gitValued || arity == gitAttached):
			options = append(options, GitOption{Name: name, Value: Word{Text: value, Literal: w.Literal}})
			i++
		case !w.Literal || attached:
			return options, i, ""
		case arity == gitFlag:
			options = append(options, GitOption{Name: name})
			i++
		case arity == gitAttached || arity == gitPrints:
			return append(options, GitOption{Name: name}), len(words), ""
		case arity == gitValued && i+1 < len(words):
			options = append(options, GitOption{Name: name, Value: words[i+1]})
			i += 2
		case arity == gitValued:
			return options, len(words), name
		default:
			return options, i, ""
		}
	}

	return options, i, ""
}

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

		if err := w.gitScripts(i); err != nil {
			return err
		}
	}

	return nil
}

// gitScripts walks the shell commands that the resolved git invocation at index i of the walk has
// git run, one level deeper, and places their invocations right after it: returned after it when
// it is returned, and walked only when it belongs to a dry run. It marks a command it cannot read
// on the invocation instead.
func (w *walker) gitScripts(i int) error {
	inv := &w.walked[i]
	scripts, mark := shellCommandsOf(*inv)
	if len(scripts) > 0 && inv.depth == maxDepth {
		scripts, mark = nil, DeepScript
	}

	if mark != "" {
		inv.Marks = append(inv.Marks, Mark{Kind: mark})
	}

	sub := walker{caller: w.caller, timed: map[*syntax.Stmt]bool{}, depth: inv.depth}
	for _, script := range scripts {
		file, err := read(script)
		if err != nil {
			return fmt.Errorf("parse the string git runs: %w", err)
		}

		sc := gitScope(*inv)
		sub.nested(file, &sc)
		if sub.err != nil {
			return fmt.Errorf("parse the string git runs: %w", sub.err)
		}
	}

	w.splice(i, sub)

	return nil
}

// splice places the invocations sub walked right after the one at index i of the walk, and those
// sub keeps right after it among the kept ones when it is kept itself.
func (w *walker) splice(i int, sub walker) {
	n := len(sub.walked)
	if n == 0 {
		return
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
	unknown := variable{unknown: true}
	sc := scope{
		dir:       inv.Target.Dir,
		env:       environment{gitDir: unknown, workTree: unknown, cdPath: unknown},
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
// aliases expand: the value of each config key git runs through the shell, given by `-c` or
// `--config-env`, and each `rebase --exec`. It returns DynamicScript as well when one of them is
// not a literal.
func shellCommandsOf(inv Invocation) ([]string, MarkKind) {
	values := configCommands(inv)
	if sub := inv.Git.Subcommand; sub.Literal && sub.Text == "rebase" {
		values = append(values, rebaseCommands(inv.Git.Args)...)
	}

	return literalScripts(values)
}

// literalScripts returns the text of each of values, the shell commands something runs, that is a
// literal, and DynamicScript as well when one of them is not.
func literalScripts(values []Word) ([]string, MarkKind) {
	var scripts []string
	var mark MarkKind
	for _, v := range values {
		if !v.Literal {
			mark = DynamicScript

			continue
		}

		scripts = append(scripts, v.Text)
	}

	return scripts, mark
}

// configCommands returns the value of each `-c` and `--config-env` option of the git invocation inv
// whose key git runs through the shell. The value is not a literal when the guard cannot read it.
// An option whose key the guard cannot read is left to the rules that judge the subcommand.
func configCommands(inv Invocation) []Word {
	var values []Word
	for _, o := range inv.Git.Options {
		switch o.Name {
		case "-c":
			key, value, cut := strings.Cut(o.Value.Text, "=")
			if cut && runsShell(key) {
				values = append(values, Word{Text: value, Literal: o.Value.Literal})
			}
		case "--config-env":
			key, name, cut := strings.CutLast(o.Value.Text, "=")
			if !cut || !runsShell(key) {
				continue
			}

			value, ok := setValue(inv, name)
			values = append(values, Word{Text: value, Literal: ok && o.Value.Literal})
		}
	}

	return values
}

// shellKeys are the config keys outside the `pager` section whose value git runs as a shell
// command.
func shellKeys() []string {
	return []string{"core.editor", "core.pager", "sequence.editor", "core.sshCommand"}
}

// runsShell reports whether git runs the value of the config key through the shell. Section and
// variable names are case-insensitive.
func runsShell(key string) bool {
	for _, name := range shellKeys() {
		if strings.EqualFold(key, name) {
			return true
		}
	}

	const pager = "pager."

	return len(key) > len(pager) && strings.EqualFold(key[:len(pager)], pager)
}

// rebaseCommands returns the value of each `--exec` or `-x` among the arguments of `git rebase`:
// separate or attached, a long option spelled as any prefix of `--exec` git could read as it, and
// `-x` anywhere in a short-flag bundle, whose rest is then its value.
func rebaseCommands(args []Word) []Word {
	var values []Word
	for i := 0; i < len(args); i++ {
		text := args[i].Text
		var value string
		var attached bool
		switch {
		case text == "--":
			return values
		case strings.HasPrefix(text, "--"):
			var name string
			name, value, attached = strings.Cut(text, "=")
			const exec = "--exec"
			if len(name) < len("--e") || len(name) > len(exec) || name != exec[:len(name)] {
				continue
			}
		case strings.HasPrefix(text, "-") && strings.Contains(text[1:], "x"):
			_, value, _ = strings.Cut(text[1:], "x")
			attached = value != "" || !args[i].Literal
		default:
			continue
		}

		switch {
		case attached:
			values = append(values, Word{Text: value, Literal: args[i].Literal})
		case i+1 < len(args):
			i++
			values = append(values, args[i])
		}
	}

	return values
}

// resolveInvocation records on the git invocation inv the git dir it targets and its subcommand
// once its aliases expand, or marks what stops the expansion.
func resolveInvocation(ctx context.Context, runner git.Runner, inv *Invocation) error {
	if inv.Git.missing != "" {
		inv.Git.GitDirUnknown = true
		inv.Marks = append(inv.Marks, Mark{Kind: MissingGitValue, Name: inv.Git.missing})

		return nil
	}

	options, known := targetOptions(*inv)
	repo := git.Repo{Runner: targetRunner(runner, *inv)}
	if known {
		gitDir, _, err := repo.FindGitDir(ctx, options...)
		if err != nil {
			return err
		}

		inv.Git.GitDir = gitDir
	} else {
		inv.Git.GitDirUnknown = true
	}

	if inv.Git.NoSubcommand {
		return nil
	}

	mark, err := expandAliases(ctx, repo, options, inv)
	if err != nil {
		return err
	}

	if mark != (Mark{}) {
		inv.Marks = append(inv.Marks, mark)
	}

	return nil
}

// targetOptions returns the global options of the git invocation inv that decide where git looks
// for a repository, as git is to be given them, and false when the guard cannot ask git where it
// looks: a directory or one of those options it cannot follow, or a directory that does not exist.
func targetOptions(inv Invocation) ([]string, bool) {
	if inv.Target.Unknown || !isDirectory(inv.Dir) || !isDirectory(inv.Target.Dir) {
		return nil, false
	}

	var options []string
	for _, o := range inv.Git.Options {
		switch o.Name {
		case "-C", "--git-dir", "--work-tree":
			if !o.Value.Literal {
				return nil, false
			}

			options = append(options, o.Name, o.Value.Text)
		case "--bare":
			options = append(options, o.Name)
		}
	}

	return options, true
}

// isDirectory reports whether dir is known and names a directory.
func isDirectory(dir string) bool {
	if dir == "" {
		return false
	}

	info, err := os.Stat(dir)

	return err == nil && info.IsDir()
}

// targetRunner returns runner moved to the directory the git invocation inv runs in, with the
// GIT_DIR and GIT_WORK_TREE the guard modeled for it in place of its own.
func targetRunner(runner git.Runner, inv Invocation) git.Runner {
	env := slices.Clone(runner.Env)
	if env == nil {
		env = os.Environ()
	}

	env = slices.DeleteFunc(env, func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")

		return git.SameKey(name, "GIT_DIR") || git.SameKey(name, "GIT_WORK_TREE")
	})
	if inv.Target.GitDir != "" {
		env = append(env, "GIT_DIR="+inv.Target.GitDir)
	}

	if inv.Target.WorkTree != "" {
		env = append(env, "GIT_WORK_TREE="+inv.Target.WorkTree)
	}

	runner.Dir = inv.Dir
	runner.Env = env

	return runner
}

// expandAliases puts in place of the subcommand of the git invocation inv the words of the alias
// it names, read with the global options in inv's target and inv's own config options, until a
// word is one of git's commands. It returns the mark that stops the expansion instead, leaving
// inv's subcommand as it was: an alias that runs a shell command, a word that is neither a command
// nor an alias the guard can read, an alias it cannot read as git would, or an alias nested deeper
// than maxAliasDepth.
func expandAliases(ctx context.Context, repo git.Repo, options []string, inv *Invocation) (Mark, error) {
	commands := gitCommands()
	config, readable := configOptions(*inv)
	options = append(slices.Clone(options), config...)
	sub, args := inv.Git.Subcommand, inv.Git.Args
	for depth := 0; ; depth++ {
		switch {
		case sub.Literal && commands[sub.Text]:
			inv.Git.Subcommand, inv.Git.Args = sub, args

			return Mark{}, nil
		case !sub.Literal || inv.Git.GitDirUnknown:
			return Mark{Kind: UnknownSubcommand, Name: sub.Text}, nil
		case depth == maxAliasDepth:
			return Mark{Kind: DeepAlias, Name: sub.Text}, nil
		case !isAliasName(sub.Text):
			// git config exits 1 both for a key that is not set and for one it rejects, so only a
			// name git accepts is read, and an exit of 1 means no alias is set.
			return Mark{Kind: UnknownSubcommand, Name: sub.Text}, nil
		case !readable:
			return Mark{Kind: UnreadableAlias, Name: sub.Text}, nil
		}

		expansion, set, err := repo.Alias(ctx, sub.Text, options...)
		if err != nil {
			return Mark{}, err
		}

		if !set {
			return Mark{Kind: UnknownSubcommand, Name: sub.Text}, nil
		}

		if strings.HasPrefix(expansion, "!") {
			return Mark{Kind: ShellAlias, Name: sub.Text}, nil
		}

		words, ok := splitAlias(expansion)
		if !ok {
			return Mark{Kind: UnreadableAlias, Name: sub.Text}, nil
		}

		expanded := make([]Word, 0, len(words)-1+len(args))
		for _, word := range words[1:] {
			expanded = append(expanded, Word{Text: word, Literal: true})
		}

		sub, args = Word{Text: words[0], Literal: true}, append(expanded, args...)
	}
}

// configOptions returns the `-c` and `--config-env` options of the git invocation inv as `-c`
// options git is to be given to read config as inv would, each `--config-env` taking its value
// from the variables inv sets. It reports false when one of them cannot be followed: a value that
// is not a literal, or a `--config-env` whose variable inv does not set to a literal.
func configOptions(inv Invocation) ([]string, bool) {
	var options []string
	for _, o := range inv.Git.Options {
		switch o.Name {
		case "-c":
			if !o.Value.Literal {
				return nil, false
			}

			options = append(options, "-c", o.Value.Text)
		case "--config-env":
			// git splits the value at its last `=`: a config key may hold one, a variable name not.
			key, variable, cut := strings.CutLast(o.Value.Text, "=")
			if !o.Value.Literal || !cut {
				return nil, false
			}

			value, ok := setValue(inv, variable)
			if !ok {
				return nil, false
			}

			options = append(options, "-c", key+"="+value)
		}
	}

	return options, true
}

// setValue returns the literal value the invocation inv itself gives the variable name, through its
// assignment prefix or an `env` wrapper, and false when it gives none the guard can read. The
// environment inv inherits is not followed for any variable but those the scope models.
func setValue(inv Invocation, name string) (string, bool) {
	for _, c := range slices.Backward(inv.env) {
		switch {
		case c.clear || git.SameKey(c.unset, name):
			return "", false
		case git.SameKey(c.assignment.Name, name):
			return c.assignment.Value.Text, c.assignment.Value.Literal
		}
	}

	return "", false
}

// isAliasName reports whether git accepts `alias.<name>` as a config key: the part after the last
// dot, or all of name, starts with a letter and holds only letters, digits and `-`; the part before
// it, a subsection, holds no line break.
func isAliasName(name string) bool {
	subsection, variable, dotted := strings.CutLast(name, ".")
	if !dotted {
		subsection, variable = "", name
	}

	if variable == "" || !isASCIILetter(variable[0]) || strings.Contains(subsection, "\n") {
		return false
	}

	for i := range len(variable) {
		c := variable[i]
		if !isASCIILetter(c) && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}

	return true
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// splitAlias splits the expansion of an alias into words as git does: at each run of whitespace
// outside quotes, with what single quotes hold taken as it is, and a backslash anywhere else
// standing for the character after it. It reports false where git refuses the expansion: a quote
// left open, or a backslash at the end.
func splitAlias(expansion string) ([]string, bool) {
	var words []string
	var word strings.Builder
	var quote byte
	for i := 0; i < len(expansion); i++ {
		c := expansion[i]
		switch {
		case quote == 0 && isAliasSpace(c):
			words = append(words, word.String())
			word.Reset()
			for i+1 < len(expansion) && isAliasSpace(expansion[i+1]) {
				i++
			}
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case c == quote:
			quote = 0
		case c == '\\' && quote != '\'':
			i++
			if i == len(expansion) {
				return nil, false
			}

			word.WriteByte(expansion[i])
		default:
			word.WriteByte(c)
		}
	}

	if quote != 0 {
		return nil, false
	}

	return append(words, word.String()), true
}

// isAliasSpace reports whether c is one of the whitespace characters git splits an alias at.
func isAliasSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func wordTexts(words []Word) []string {
	texts := make([]string, 0, len(words))
	for _, w := range words {
		texts = append(texts, w.Text)
	}

	return texts
}
