package guard

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeffzi/ils/internal/git"
)

// hooksPathKey is the config key that points git at the programs it runs before a commit or a
// push.
const hooksPathKey = "core.hooksPath"

// ---------------------------------------------------------------------------
// Operations that discard work
// ---------------------------------------------------------------------------

// discard is a git operation that destroys work, with the reason the guard refuses it for.
type discard struct {
	// refuses reports whether the options make the operation destroy work; nil refuses every
	// form of it.
	refuses func(o subcommandOptions) bool
	reason  string
}

// discards returns the git operations that destroy work, each by the subcommand and, for the
// operations of a subcommand that takes its own, the first word after it.
func discards() map[string]discard {
	return map[string]discard{
		"checkout": {reason: "git checkout can overwrite uncommitted changes; use git switch or git restore --staged"},
		"switch": {
			refuses: func(o subcommandOptions) bool { return o.sets('f', "--force") || o.sets(0, "--discard-changes") },
			reason:  "git switch --force discards uncommitted changes",
		},
		"reset": {reason: "git reset can discard uncommitted changes and move the branch; use git restore --staged"},
		"stash": {reason: "git stash takes uncommitted changes out of the working tree"},
		"clean": {
			refuses: func(o subcommandOptions) bool { return o.sets('f', "--force") },
			reason:  "git clean --force deletes untracked files",
		},
		"branch": {
			refuses: func(o subcommandOptions) bool {
				return o.sets('D', "") || o.sets('d', "--delete") && o.sets('f', "--force")
			},
			reason: "git branch -D deletes a branch even when its commits are not merged",
		},
		"restore": {
			refuses: func(o subcommandOptions) bool { return !o.names("--staged", "--source") || o.sets('W', "--worktree") },
			reason:  "git restore without --staged discards changes in the working tree",
		},
		"rm": {
			refuses: func(o subcommandOptions) bool { return o.sets('f', "--force") },
			reason:  "git rm --force deletes files even when they hold uncommitted changes",
		},
		"reflog expire": {reason: "git reflog expire deletes the reflog entries that recover lost commits"},
		"reflog delete": {reason: "git reflog delete deletes the reflog entries that recover lost commits"},
		"reflog drop":   {reason: "git reflog drop deletes the whole reflog of a ref, which recovers lost commits"},
		"prune":         {reason: "git prune deletes unreachable objects, which can hold lost commits"},
		"gc": {
			refuses: func(o subcommandOptions) bool { return o.attaches("--prune") },
			reason:  "git gc --prune deletes unreachable objects, which can hold lost commits",
		},
		"apply": {
			refuses: func(o subcommandOptions) bool { return o.sets('R', "--reverse") },
			reason:  "git apply --reverse undoes changes in the working tree",
		},
		"add": {
			refuses: func(o subcommandOptions) bool { return o.sets('f', "--force") },
			reason:  "git add --force stages files the ignore rules keep out",
		},
	}
}

// refuseDiscardingGit refuses a git invocation that destroys work: uncommitted changes, untracked
// files, unmerged branches, or the objects and reflog entries lost commits are recovered from. It
// refuses one whose operation, or the options that decide it, it cannot read.
func refuseDiscardingGit(_ context.Context, _ Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok {
		return Verdict{Outcome: Allow}, nil
	}

	table, name := discards(), ruleName(sub)
	op, found := discard{}, false
	if args := inv.Git.Args; len(args) > 0 {
		if !args[0].Literal && hasOperations(table, name) {
			return unreadableArgument(sub), nil
		}

		op, found = table[name+" "+args[0].Text]
	}

	if !found {
		op, found = table[name]
	}

	if !found {
		return Verdict{Outcome: Allow}, nil
	}

	if op.refuses != nil {
		options := readArgs(inv.Git.Args, optionsOf(sub))
		if options.unreadable {
			return unreadableArgument(sub), nil
		}

		if !op.refuses(options) {
			return Verdict{Outcome: Allow}, nil
		}
	}

	if name != sub {
		return refuse(fmt.Sprintf("git %s is git %s: %s", sub, name, op.reason)), nil
	}

	return refuse(op.reason), nil
}

// hasOperations reports whether table holds an operation of sub named by the word after it.
func hasOperations(table map[string]discard, sub string) bool {
	for name := range table {
		if strings.HasPrefix(name, sub+" ") {
			return true
		}
	}

	return false
}

// unreadableArgument refuses the git subcommand sub, one of whose arguments outside a value slot
// is not a literal: it may hold an option the guard decides by.
func unreadableArgument(sub string) Verdict {
	reason := fmt.Sprintf("git %s: the argument cannot be read, and it may hold an option the guard checks for", sub)

	return refuse(reason)
}

// ---------------------------------------------------------------------------
// Skipped checks and history over a file
// ---------------------------------------------------------------------------

// hookRunners returns the subcommands that run checks, each with the long options of its that
// skip them. send-email reads its options with Perl's Getopt::Long, which also takes `--novalidate`
// for `--no-validate`.
func hookRunners() map[string][]string {
	verify := []string{"--no-verify"}

	return map[string][]string{
		"commit": verify, "push": verify, "merge": verify, "am": verify, "rebase": verify, "pull": verify,
		"send-email": {"--no-validate", "--novalidate"},
	}
}

// refuseSkippedChecks refuses a git invocation that skips the checks a repository runs before a
// commit or a push: `--no-verify` on any subcommand, the option hookRunners names for its
// subcommand, the short option subcommandOptionTable lists for `--no-verify`, or a global `-c` or
// `--config-env` option that sets hooksPathKey. It refuses a `-c` or `--config-env` value it cannot
// read, and an argument it cannot read of a subcommand that runs those checks.
func refuseSkippedChecks(_ context.Context, _ Caller, inv Invocation) (Verdict, error) {
	if inv.Tool != Git {
		return Verdict{Outcome: Allow}, nil
	}

	for _, o := range inv.Git.Options {
		if (o.Name == "-c" || o.Name == "--config-env") && !o.Value.Literal {
			reason := fmt.Sprintf("git %s: the value of %s cannot be read, and it may set %s", inv.Git.Subcommand.Text, o.Name, hooksPathKey)

			return refuse(reason), nil
		}

		if setsHooksPath(o) {
			reason := fmt.Sprintf("git %s %s replaces the checks set up to run before commits and pushes", o.Name, hooksPathKey)

			return refuse(reason), nil
		}
	}

	sub, ok := subcommand(inv)
	if !ok {
		return Verdict{Outcome: Allow}, nil
	}

	known := optionsOf(sub)
	options := readArgs(inv.Git.Args, known)
	skips, runsChecks := hookRunners()[sub]
	letter := known.short["no-verify"]
	skip := slices.IndexFunc(skips, func(long string) bool { return options.sets(0, long) })
	flag := ""
	switch {
	case options.sets(0, "--no-verify"):
		flag = "--no-verify"
	case skip >= 0:
		flag = skips[skip]
	case options.sets(letter, ""):
		flag = "-" + string(letter)
	case options.unreadable && runsChecks:
		return unreadableArgument(sub), nil
	default:
		return Verdict{Outcome: Allow}, nil
	}

	reason := fmt.Sprintf("git %s %s skips the checks set up to run before it", sub, flag)

	return refuse(reason), nil
}

// setsHooksPath reports whether the global option o sets hooksPathKey. git reads a `-c` value
// up to its first `=` and a `--config-env` value up to its last as the key, whose section and name
// it reads in any case.
func setsHooksPath(o GitOption) bool {
	var key string
	switch o.Name {
	case "-c":
		key, _, _ = strings.Cut(o.Value.Text, "=")
	case "--config-env":
		key, _, _ = strings.CutLast(o.Value.Text, "=")
	default:
		return false
	}

	return strings.EqualFold(key, hooksPathKey)
}

// refuseHistoryOverFile refuses `git show` or `git cat-file` with stdout redirected to a file,
// which writes a version from history over a file that may hold uncommitted changes.
func refuseHistoryOverFile(_ context.Context, _ Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok || sub != "show" && sub != "cat-file" {
		return Verdict{Outcome: Allow}, nil
	}

	target, toFile := inv.Stdout()
	if !toFile {
		return Verdict{Outcome: Allow}, nil
	}

	reason := fmt.Sprintf("git %s writes over %s, which may hold uncommitted changes", sub, target.Text)

	return refuse(reason), nil
}

// ---------------------------------------------------------------------------
// Rebase, worktree, config, and remote rules
// ---------------------------------------------------------------------------

// refuseRebaseOverChanges refuses `git rebase` in a work tree that holds uncommitted or
// untracked changes, and one whose repository the guard cannot tell. Outside any repository there
// is nothing for it to lose.
func refuseRebaseOverChanges(ctx context.Context, caller Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok || sub != "rebase" {
		return Verdict{Outcome: Allow}, nil
	}

	if inv.Git.GitDirUnknown {
		return refuse("git rebase: the guard cannot tell which repository it runs in"), nil
	}

	if inv.Git.GitDir == "" {
		return Verdict{Outcome: Allow}, nil
	}

	options, _ := targetOptions(inv)
	dirty, err := git.Repo{Runner: targetRunner(caller.Git, inv)}.Dirty(ctx, options...)
	if err != nil {
		return Verdict{}, fmt.Errorf("read whether git rebase runs over changes: %w", err)
	}

	if dirty {
		return refuse("git rebase (dirty): the working tree holds uncommitted or untracked changes"), nil
	}

	return Verdict{Outcome: Allow}, nil
}

// refuseRiskyWorktreeRemoval refuses `git worktree remove` with `--force`, of a path it cannot
// verify, and of a worktree that holds uncommitted or untracked changes. It reads a relative path
// from the directory git runs in once its `-C` options apply, as git does.
func refuseRiskyWorktreeRemoval(ctx context.Context, caller Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok || sub != "worktree" || len(inv.Git.Args) == 0 {
		return Verdict{Outcome: Allow}, nil
	}

	if !inv.Git.Args[0].Literal {
		return unreadableArgument(sub), nil
	}

	if inv.Git.Args[0].Text != "remove" {
		return Verdict{Outcome: Allow}, nil
	}

	options := readArgs(inv.Git.Args[1:], knownOptions{})
	if options.sets('f', "--force") {
		return refuse("git worktree remove --force deletes the worktree's uncommitted and untracked changes"), nil
	}

	path, why := worktreePath(inv, options)
	if why != "" {
		return refuse("git worktree remove (unverifiable path): " + why), nil
	}

	dirty, err := git.Repo{Runner: targetRunner(caller.Git, Invocation{Dir: path})}.Dirty(ctx)
	if err != nil {
		return Verdict{}, fmt.Errorf("read whether the worktree %s holds changes: %w", path, err)
	}

	if dirty {
		reason := fmt.Sprintf("git worktree remove (dirty): %s holds uncommitted or untracked changes", path)

		return refuse(reason), nil
	}

	return Verdict{Outcome: Allow}, nil
}

// worktreePath returns the absolute path of the worktree that `git worktree remove` with the
// arguments options removes, or why the guard cannot verify it.
func worktreePath(inv Invocation, options subcommandOptions) (path, why string) {
	switch {
	case options.unreadable:
		return "", "an argument cannot be read"
	case len(options.positional) != 1:
		return "", "it names no single worktree"
	case !options.positional[0].Literal:
		return "", "the path cannot be read"
	}

	path = options.positional[0].Text
	if !filepath.IsAbs(path) {
		if inv.Target.Dir == "" {
			return "", "the directory git runs in cannot be followed"
		}

		path = filepath.Join(inv.Target.Dir, path)
	}

	if !isDirectory(path) {
		return "", path + " is not a directory"
	}

	return path, ""
}

// guardedSections returns the config sections that make git run programs or reach another
// repository.
func guardedSections() []string {
	return []string{"alias", "include", "includeIf", "remote"}
}

// refuseGuardedConfigWrite refuses `git config` writing, unsetting, renaming or removing a key
// or section that changes what git runs or where it pushes: those of guardedSections and
// hooksPathKey, in any scope. It refuses opening the config in an editor too, which can write
// any key. Reads are allowed, and so is a write of any other key.
func refuseGuardedConfigWrite(_ context.Context, _ Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok || sub != "config" {
		return Verdict{Outcome: Allow}, nil
	}

	options := readArgs(inv.Git.Args, optionsOf(sub))
	if options.opaque() {
		return unreadableArgument(sub), nil
	}

	action, words := configAction(options)
	names := words[:min(len(words), 1)]
	switch action {
	case "edit":
		reason := fmt.Sprintf("git config opening an editor can write any key, those the guard protects included: the sections %s and %s",
			strings.Join(guardedSections(), ", "), hooksPathKey)

		return refuse(reason), nil
	case "set", "unset", "remove-section":
	case "rename-section":
		names = words[:min(len(words), 2)]
	default:
		names = nil
	}

	for _, name := range names {
		if guardedConfigName(name) {
			reason := fmt.Sprintf("git config writing %s changes what git runs or where it pushes", name)

			return refuse(reason), nil
		}
	}

	return Verdict{Outcome: Allow}, nil
}

// configAction returns what `git config` with the arguments options does, by the name of its
// subcommand form: get, set, unset, rename-section, remove-section or edit, with the words after
// it. An option that adds or replaces a value writes the key as unset does. Opening the editor
// wins over any other action. Two words and no action set the first.
func configAction(options subcommandOptions) (action string, words []string) {
	words = wordTexts(options.positional)
	if len(words) > 0 && slices.Contains([]string{"list", "get", "set", "unset", "rename-section", "remove-section", "edit"}, words[0]) {
		action, words = words[0], words[1:]
	}

	if strings.ContainsRune(options.letters, 'l') {
		action = "get"
	}

	for _, l := range options.long {
		switch {
		case strings.HasPrefix(l.name, "--get") || l.name == "--list":
			action = "get"
		case slices.Contains([]string{"--replace-all", "--add", "--unset", "--unset-all"}, l.name):
			action = "unset"
		case l.name == "--rename-section" || l.name == "--remove-section":
			action = strings.TrimPrefix(l.name, "--")
		}
	}

	if options.sets('e', "--edit") {
		action = "edit"
	}

	if action == "" && len(words) > 1 {
		action = "set"
	}

	return action, words
}

// guardedConfigName reports whether name, a key or a whole section, is guarded: in a section of
// guardedSections, or hooksPathKey. git reads the section and the variable name in any case.
func guardedConfigName(name string) bool {
	section, _, _ := strings.Cut(name, ".")

	inSection := slices.ContainsFunc(guardedSections(), func(s string) bool { return strings.EqualFold(s, section) })

	return inSection || strings.EqualFold(name, hooksPathKey)
}

// refuseOriginRewrite refuses a `git remote` operation that removes or renames origin, or points it elsewhere.
func refuseOriginRewrite(_ context.Context, _ Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok || sub != "remote" {
		return Verdict{Outcome: Allow}, nil
	}

	args := inv.Git.Args
	for len(args) > 0 && args[0].Literal && remoteVerbosity(args[0].Text) {
		args = args[1:]
	}

	if len(args) == 0 {
		return Verdict{Outcome: Allow}, nil
	}

	op := args[0]
	rewrites := slices.Contains([]string{"remove", "rm", "rename", "set-url", "set-head"}, op.Text)
	if op.Literal && !rewrites {
		return Verdict{Outcome: Allow}, nil
	}

	options := readArgs(args[1:], knownOptions{})
	if !op.Literal || options.opaque() {
		return unreadableArgument(sub), nil
	}

	if len(options.positional) == 0 || options.positional[0].Text != "origin" {
		return Verdict{Outcome: Allow}, nil
	}

	reason := fmt.Sprintf("git remote %s origin changes the remote branches are pushed to and fetched from", op.Text)

	return refuse(reason), nil
}

// remoteVerbosity reports whether word is the one option `git remote` takes before its own
// subcommand: -v, bundled or not, or --verbose, --no-verbose or a start of either.
func remoteVerbosity(word string) bool {
	if name, ok := strings.CutPrefix(word, "--"); ok {
		return name != "" && slices.ContainsFunc([]string{"verbose", "no-verbose"}, func(long string) bool { return strings.HasPrefix(long, name) })
	}

	letters, ok := strings.CutPrefix(word, "-")

	return ok && letters != "" && strings.Trim(letters, "v") == ""
}
