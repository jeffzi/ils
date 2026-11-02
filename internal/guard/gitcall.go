//go:build unix

package guard

import (
	"context"
	"os"
	"slices"
	"strings"

	"github.com/jeffzi/ils/internal/git"
)

// ---------------------------------------------------------------------------
// Reading the words of a git invocation
// ---------------------------------------------------------------------------

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
	// OlderCommandAlias is an alias named like one of git's commands that an older git ils supports
	// may not have, which that git runs in place of the command.
	OlderCommandAlias MarkKind = "git alias of a command an older git lacks"
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

// gitCommands is the table of git 2.55's main commands, which a git that has them runs as they are
// whatever alias shares their name; everyGitBuiltins are those every git ils supports has. Commands
// that only contrib or a platform's packaging installs are left out.
func gitCommands() map[string]bool {
	return setOf(
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
	)
}

// everyGitBuiltins is the part of gitCommands that git.MinimumVersion builds in, and so every git
// ils supports: git runs a builtin whatever alias shares its name. git runs any other command only
// when its program is installed, and an alias of its name when it is not: a command newer than the
// oldest git, or one that packaging ships on its own, such as send-email.
func everyGitBuiltins() map[string]bool {
	return setOf(
		"add", "am", "annotate", "apply", "archive", "blame", "branch", "bugreport", "bundle",
		"cat-file", "check-attr", "check-ignore", "check-mailmap", "check-ref-format", "checkout",
		"checkout--worker", "checkout-index", "cherry", "cherry-pick", "clean", "clone", "column",
		"commit", "commit-graph", "commit-tree", "config", "count-objects", "credential",
		"credential-cache", "credential-cache--daemon", "credential-store", "describe", "diff",
		"diff-files", "diff-index", "diff-tree", "difftool", "fast-export", "fast-import", "fetch",
		"fetch-pack", "fmt-merge-msg", "for-each-ref", "for-each-repo", "format-patch", "fsck",
		"fsck-objects", "gc", "get-tar-commit-id", "grep", "hash-object", "help", "index-pack", "init",
		"init-db", "interpret-trailers", "log", "ls-files", "ls-remote", "ls-tree", "mailinfo",
		"mailsplit", "maintenance", "merge", "merge-base", "merge-file", "merge-index", "merge-ours",
		"merge-recursive", "merge-recursive-ours", "merge-recursive-theirs", "merge-subtree",
		"merge-tree", "mktag", "mktree", "multi-pack-index", "mv", "name-rev", "notes", "pack-objects",
		"pack-redundant", "pack-refs", "patch-id", "pickaxe", "prune", "prune-packed", "pull", "push",
		"range-diff", "read-tree", "rebase", "receive-pack", "reflog", "remote", "remote-ext",
		"remote-fd", "repack", "replace", "rerere", "reset", "restore", "rev-list", "rev-parse",
		"revert", "rm", "send-pack", "shortlog", "show", "show-branch", "show-index", "show-ref",
		"sparse-checkout", "stage", "stash", "status", "stripspace", "submodule--helper", "switch",
		"symbolic-ref", "tag", "unpack-file", "unpack-objects", "update-index", "update-ref",
		"update-server-info", "upload-archive", "upload-archive--writer", "upload-pack", "var",
		"verify-commit", "verify-pack", "verify-tag", "version", "whatchanged", "worktree",
		"write-tree",
	)
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

// ---------------------------------------------------------------------------
// The git dir and aliases of an invocation
// ---------------------------------------------------------------------------

// resolveInvocation records on the git invocation inv the git dir it targets and its subcommand
// once its aliases expand, or marks what stops the expansion.
func resolveInvocation(ctx context.Context, runner git.Runner, inv *Invocation) error {
	if inv.Git.missing != "" {
		inv.Git.GitDirUnknown = true
		inv.Marks = append(inv.Marks, Mark{Kind: MissingGitValue, Name: inv.Git.missing})

		return nil
	}

	options, known := targetOptions(*inv)
	repo := git.Repo{Runner: targetRunner(runner, inv.Dir, inv.Target)}
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

// targetRunner returns runner moved to dir, with the GIT_DIR and GIT_WORK_TREE of target in place
// of its own; an empty field of target leaves that variable unset.
func targetRunner(runner git.Runner, dir string, target GitTarget) git.Runner {
	env := git.WithoutKeys(runner.Environ(), "GIT_DIR", "GIT_WORK_TREE")
	if target.GitDir != "" {
		env = append(env, "GIT_DIR="+target.GitDir)
	}

	if target.WorkTree != "" {
		env = append(env, "GIT_WORK_TREE="+target.WorkTree)
	}

	runner.Dir = dir
	runner.Env = env

	return runner
}

// expandAliases puts in place of the subcommand of the git invocation inv the words of the alias
// it names, read with the global options in inv's target and inv's own config options, until a
// word is one of git's commands and no alias of its name can run in its place. It returns the mark
// that stops the expansion instead, leaving inv's subcommand as it was: an alias that runs a shell
// command, a word that is neither a command nor an alias the guard can read, an alias it cannot
// read as git would, an alias nested deeper than maxAliasDepth, or an alias of a command an older
// git lacks.
func expandAliases(ctx context.Context, repo git.Repo, options []string, inv *Invocation) (Mark, error) {
	commands, builtins := gitCommands(), everyGitBuiltins()
	config, access := aliasConfig(*inv)
	options = append(slices.Clone(options), config...)
	sub, args := inv.Git.Subcommand, inv.Git.Args
	for depth := 0; ; depth++ {
		command := inTable(commands, sub)
		if inTable(builtins, sub) {
			inv.Git.Subcommand, inv.Git.Args = sub, args

			return Mark{}, nil
		}

		if mark := unreadAlias(sub, command, depth, access); mark != (Mark{}) {
			return mark, nil
		}

		expansion, set, err := repo.Alias(ctx, sub.Text, options...)
		if err != nil {
			return Mark{}, err
		}

		switch {
		case !set && command:
			inv.Git.Subcommand, inv.Git.Args = sub, args

			return Mark{}, nil
		case !set:
			return Mark{Kind: UnknownSubcommand, Name: sub.Text}, nil
		case command:
			return Mark{Kind: OlderCommandAlias, Name: sub.Text}, nil
		}

		if strings.HasPrefix(expansion, "!") {
			return Mark{Kind: ShellAlias, Name: sub.Text}, nil
		}

		words, ok := git.SplitCommandLine(expansion)
		if !ok {
			return Mark{Kind: UnreadableAlias, Name: sub.Text}, nil
		}

		sub, args = Word{Text: words[0], Literal: true}, append(literalWords(words[1:]), args...)
	}
}

// aliasAccess is how far the guard can read the aliases of a git invocation as git would.
type aliasAccess int

const (
	aliasesReadable aliasAccess = iota
	// aliasesUnreadable is an invocation whose `-c` or `--config-env` options the guard cannot follow.
	aliasesUnreadable
	// aliasesTargetUnknown is an invocation whose repository, and so whose config, the guard does not
	// know.
	aliasesTargetUnknown
)

// inTable reports whether the word w is a literal that names an entry of table.
func inTable(table map[string]bool, w Word) bool {
	return w.Literal && table[w.Text]
}

// aliasConfig returns the config options of the git invocation inv as configOptions does, and how
// far the guard can read its aliases with them.
func aliasConfig(inv Invocation) ([]string, aliasAccess) {
	config, readable := configOptions(inv)
	switch {
	case inv.Git.GitDirUnknown:
		return config, aliasesTargetUnknown
	case !readable:
		return config, aliasesUnreadable
	}

	return config, aliasesReadable
}

// unreadAlias returns the mark that stops the expansion of the git subcommand word sub at depth
// before its alias is read, and the zero Mark when the alias is to be read. command reports whether
// sub is one of git's commands, which an alias can replace only on a git that lacks it.
func unreadAlias(sub Word, command bool, depth int, access aliasAccess) Mark {
	switch {
	case !sub.Literal || access == aliasesTargetUnknown && !command:
		return Mark{Kind: UnknownSubcommand, Name: sub.Text}
	case depth == maxAliasDepth:
		return Mark{Kind: DeepAlias, Name: sub.Text}
	case !isAliasName(sub.Text):
		// git config exits 1 both for a key that is not set and for one it rejects, so only a
		// name git accepts is read, and an exit of 1 means no alias is set.
		return Mark{Kind: UnknownSubcommand, Name: sub.Text}
	case access != aliasesReadable:
		return Mark{Kind: UnreadableAlias, Name: sub.Text}
	}

	return Mark{}
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
			key, varName, cut := configKey(o)
			if !o.Value.Literal || !cut {
				return nil, false
			}

			value, ok := setValue(inv, varName)
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
	subsection, key, dotted := strings.CutLast(name, ".")
	if !dotted {
		subsection, key = "", name
	}

	if key == "" || !isASCIILetter(key[0]) || strings.Contains(subsection, "\n") {
		return false
	}

	for i := range len(key) {
		c := key[i]
		if !isASCIILetter(c) && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}

	return true
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
