//go:build unix

package guard

import (
	"context"
	"fmt"
	"maps"
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

// cleanForceKey is the config key that, while true, keeps git clean from deleting without
// `--force`.
const cleanForceKey = "clean.requireForce"

// discard is a git operation that destroys work, with the reason the guard refuses it for. One
// with an empty reason, no refuses, and no judges destroys nothing: it is a read-only operation of
// a subcommand whose other operations discard, so the guard allows every form of it.
type discard struct {
	// refuses reports whether the options make the operation destroy work; nil refuses every
	// form of it, unless judges is set.
	refuses func(o subcommandOptions) bool
	// judges decides the forms refuses leaves allowed, by the invocation's global options or the
	// state of its repository: it returns the reason to refuse for, or an empty one to allow.
	judges func(ctx context.Context, caller Caller, inv Invocation, o subcommandOptions) (string, error)
	reason string
}

// discards returns the git operations that destroy work, each by the subcommand and, for the
// operations of a subcommand that takes its own, the first word after it.
func discards() map[string]discard {
	table := map[string]discard{
		"checkout": {reason: "git checkout can overwrite uncommitted changes; use git switch or git restore --staged"},
		"switch": {
			refuses: func(o subcommandOptions) bool { return forced(o) || o.sets(0, "--discard-changes") },
			reason:  "git switch --force discards uncommitted changes",
		},
		"reset":      {reason: "git reset can discard uncommitted changes and move the branch; use git restore --staged"},
		"stash":      {reason: "git stash takes uncommitted changes out of the working tree"},
		"stash list": {},
		"stash show": {},
		"clean": {
			refuses: func(o subcommandOptions) bool {
				// A dry run deletes nothing, --force or not, but --interactive still asks to delete.
				dryRun := isDryRun(o)

				return (forced(o) && !dryRun) || o.setsLast('i', "--interactive", "--no-interactive")
			},
			judges: judgeCleanWithoutForce,
			reason: "git clean --force or --interactive deletes untracked files",
		},
		"branch": {
			refuses: func(o subcommandOptions) bool {
				return o.sets('D', "") || (o.sets('d', "--delete") && forced(o))
			},
			judges: judgeForcedBranchOverwrite,
			reason: "git branch -D deletes a branch even when its commits are not merged",
		},
		"restore": {
			refuses: func(o subcommandOptions) bool {
				staged := o.sets('S', "") || o.names("--staged", "--source")

				return !staged || o.sets('W', "--worktree")
			},
			reason: "git restore without --staged discards changes in the working tree",
		},
		"rm": {
			refuses: forced,
			reason:  "git rm --force deletes files even when they hold uncommitted changes",
		},
		"mv": {
			refuses: forced,
			reason:  "git mv --force overwrites the file at the destination, uncommitted changes included",
		},
		"apply": {
			refuses: func(o subcommandOptions) bool { return o.sets('R', "--reverse") },
			reason:  "git apply --reverse undoes changes in the working tree",
		},
		"add": {
			refuses: forced,
			reason:  "git add --force stages files the ignore rules keep out",
		},
	}
	maps.Copy(table, recoveryDiscards())
	maps.Copy(table, plumbingDiscards())
	maps.Copy(table, submoduleDiscards())

	return table
}

func forced(o subcommandOptions) bool {
	return o.sets('f', "--force")
}

// isDryRun reports whether the options end on `-n` or `--dry-run`, not undone by a later
// `--no-dry-run`.
func isDryRun(o subcommandOptions) bool {
	return o.setsLast('n', "--dry-run", "--no-dry-run")
}

// recoveryDiscards returns the operations of discards that delete what lost commits are recovered
// from: the reflog entries that name them and the unreachable objects that hold them.
func recoveryDiscards() map[string]discard {
	return map[string]discard{
		"reflog expire": {reason: "git reflog expire deletes the reflog entries that recover lost commits"},
		"reflog delete": {reason: "git reflog delete deletes the reflog entries that recover lost commits"},
		"reflog drop":   {reason: "git reflog drop deletes the whole reflog of a ref, which recovers lost commits"},
		"prune":         {reason: "git prune deletes unreachable objects, which can hold lost commits"},
		"gc": {
			refuses: func(o subcommandOptions) bool { return o.attaches("--prune") },
			judges:  judgePruneExpiry,
			reason:  "git gc --prune deletes unreachable objects, which can hold lost commits",
		},
		"repack": {
			refuses: func(o subcommandOptions) bool {
				// -A, --cruft and --keep-unreachable each keep the unreachable objects -a -d would
				// delete, unless an expiry deletes them anyway: judgeRepackExpiry reads that.
				keeps := o.sets('A', "") || o.setsLast(0, "--cruft", "--no-cruft") ||
					o.setsLast('k', "--keep-unreachable", "--no-keep-unreachable")

				return o.sets('a', "") && o.sets('d', "") && !keeps
			},
			judges: judgeRepackExpiry,
			reason: "git repack -a -d deletes unreachable objects at once, which can hold lost commits",
		},
	}
}

// plumbingDiscards returns the operations of discards that git's plumbing commands, the low-level
// ones its other commands are built from, perform.
func plumbingDiscards() map[string]discard {
	return map[string]discard{
		"checkout-index": {
			refuses: forced,
			reason:  "git checkout-index --force overwrites files in the working tree, uncommitted changes included",
		},
		"read-tree": {
			refuses: func(o subcommandOptions) bool {
				// --prefix only adds the tree's entries under a directory the index does not hold yet,
				// and --index-output writes another file, so neither replaces what is staged.
				replacesIndex := !isDryRun(o) &&
					!o.sets(0, "--index-output") && !o.sets(0, "--prefix")

				return replacesIndex || o.sets(0, "--reset") || (o.sets('u', "") && o.sets('m', ""))
			},
			reason: "git read-tree replaces the index, and with --reset or -u -m the working tree, discarding their changes",
		},
		"update-ref": {
			refuses: func(o subcommandOptions) bool {
				deletes := o.sets('d', "") || o.sets(0, "--stdin")

				// A ref or new value that is not a literal, reachable only after `--`, may be the all-zero
				// id, or may split into the ref and the all-zero id.
				named := o.positional[:min(2, len(o.positional))]
				unreadable := slices.ContainsFunc(named, func(w Word) bool { return !w.Literal })

				return deletes || unreadable || (len(o.positional) > 1 && nullObjectID(o.positional[1].Text))
			},
			reason: "git update-ref -d, --stdin, or an all-zero new value deletes refs, and the reflog that recovers their commits",
		},
	}
}

// submoduleDiscards returns the operations of discards that git submodule performs on the work
// trees of the submodules.
func submoduleDiscards() map[string]discard {
	return map[string]discard{
		"submodule update": {
			refuses: forced,
			reason:  "git submodule update --force checks out the recorded commits over the submodules' uncommitted changes",
		},
		"submodule deinit": {
			refuses: forced,
			reason:  "git submodule deinit --force removes the submodules' work trees even when they hold uncommitted changes",
		},
	}
}

// nullObjectID reports whether value is the all-zero object id, SHA-1 or SHA-256, which git reads
// as a new value of `git update-ref` as the request to delete the ref.
func nullObjectID(value string) bool {
	return (len(value) == shortObjectIDLength || len(value) == longObjectIDLength) && strings.Trim(value, "0") == ""
}

// The lengths of an object id in hex digits: the SHA-1 id and the SHA-256 id.
const (
	shortObjectIDLength = 40
	longObjectIDLength  = 64
)

// refuseDiscardingGit refuses a git invocation that destroys work: uncommitted changes, untracked
// files, unmerged branches, or the objects and reflog entries lost commits are recovered from. It
// refuses one whose operation, or the options that decide it, it cannot read.
func refuseDiscardingGit(ctx context.Context, caller Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok {
		return Verdict{Outcome: Allow}, nil
	}

	table, name := discards(), ruleName(sub)
	op, found := discard{}, false
	if args := inv.Git.Args[operationAt(name, inv.Git.Args):]; len(args) > 0 {
		if !args[0].Literal && hasOperations(table, name) {
			return Verdict{}, &unreadArgumentError{sub: sub}
		}

		op, found = table[name+" "+args[0].Text]
	}

	if !found {
		op, found = table[name]
	}

	if !found {
		return Verdict{Outcome: Allow}, nil
	}

	return op.verdict(ctx, caller, inv, sub)
}

// verdict judges the git invocation inv of the subcommand sub, whose operation is d: it refuses
// every form when d has neither refuses nor judges, and otherwise the forms they refuse and those
// whose options it cannot read. An error is the failure of a read judges made.
func (d discard) verdict(ctx context.Context, caller Caller, inv Invocation, sub string) (Verdict, error) {
	reason := d.reason
	if d.refuses != nil || d.judges != nil {
		options := readArgs(inv.Git.Args, optionsOf(sub))
		switch {
		case options.unreadable:
			return Verdict{}, &unreadArgumentError{sub: sub}
		case d.refuses != nil && d.refuses(options):
		case d.judges != nil:
			judged, err := d.judges(ctx, caller, inv, options)
			if err != nil {
				return Verdict{}, err
			}

			reason = judged
		default:
			reason = ""
		}
	}

	if reason == "" {
		return Verdict{Outcome: Allow}, nil
	}

	if name := ruleName(sub); name != sub {
		return refuse(fmt.Sprintf("git %s is git %s: %s", sub, name, reason)), nil
	}

	return refuse(reason), nil
}

// judgeCleanWithoutForce refuses `git clean` without `--force` when the config git reads for the
// invocation, its `-c` and `--config-env` options included, sets cleanForceKey to false, which
// lets it delete untracked files anyway, or when the guard cannot read that config. A dry run
// deletes nothing, unless a later --no-dry-run undoes it.
func judgeCleanWithoutForce(ctx context.Context, caller Caller, inv Invocation, o subcommandOptions) (string, error) {
	if isDryRun(o) {
		return "", nil
	}

	targets, targetOK := targetOptions(inv)
	config, configOK := configOptions(inv)
	if !targetOK {
		return fmt.Sprintf("git clean: the guard cannot tell where it runs, so it cannot read %s, which lets it "+
			"delete untracked files without --force; %s", cleanForceKey, moveFirst), nil
	}

	if !configOK {
		return fmt.Sprintf("git clean: the guard cannot read %s, which lets it delete untracked files without --force", cleanForceKey), nil
	}

	// --default answers for an unset key, so git exits non-zero only on a failure.
	args := slices.Concat(targets, config, []string{"config", "--type=bool", "--default=true", "--get", cleanForceKey})

	result, err := targetRunner(caller.Git, inv.Dir, inv.Target).Run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("read %s for git clean: %w", cleanForceKey, err)
	}

	if strings.TrimSpace(result.Stdout) == "true" {
		return "", nil
	}

	return fmt.Sprintf("git clean deletes untracked files without --force: %s is false", cleanForceKey), nil
}

// judgePruneExpiry refuses `git gc` whose `-c` or `--config-env` option sets gc.pruneExpire, which
// decides how old the unreachable objects it deletes must be.
func judgePruneExpiry(_ context.Context, _ Caller, inv Invocation, _ subcommandOptions) (string, error) {
	for _, o := range inv.Git.Options {
		if key, _, cut := configKey(o); cut && strings.EqualFold(key, "gc.pruneExpire") {
			return fmt.Sprintf("git %s gc.pruneExpire sets which unreachable objects git gc deletes, and they can hold lost commits", o.Name), nil
		}
	}

	return "", nil
}

// judgeRepackExpiry refuses `git repack -d` that keeps unreachable objects only to expire them: `-A`
// with `--unpack-unreachable`, which leaves the objects older than its expiry out of the loose
// ones it writes, and `--cruft` with `--cruft-expiration`, which leaves them out of the cruft
// pack. Either way -d then deletes the packs that held them. Any expiry is refused, as `git gc
// --prune=…` is: one in the past still deletes the lost commits older than it.
func judgeRepackExpiry(_ context.Context, _ Caller, _ Invocation, o subcommandOptions) (string, error) {
	if !o.sets('d', "") {
		return "", nil
	}

	var expiry string
	switch {
	case o.sets('A', "") && o.setsLast(0, "--unpack-unreachable", "--no-unpack-unreachable"):
		expiry = "-A --unpack-unreachable"
	case o.setsLast(0, "--cruft", "--no-cruft") && o.setsLast(0, "--cruft-expiration", "--no-cruft-expiration"):
		expiry = "--cruft --cruft-expiration"
	default:
		return "", nil
	}

	return fmt.Sprintf("git repack -d %s deletes the unreachable objects older than the expiry, which can hold lost commits", expiry), nil
}

// judgeForcedBranchOverwrite refuses `git branch -M` or `-C`, or `--move` or `--copy` with
// `--force`, onto a branch that exists, which it replaces along with its reflog, and one whose
// repository the guard cannot tell. The new name is the last of the one or two names the move or
// copy takes, expanded as git expands it (`@{-1}` names the branch checked out before). A name git
// cannot expand fails the read, so the guard refuses it.
//
// A branch exists when its name matches the new one in any letter case, wherever git stores it: on
// a filesystem that folds case, `refs/heads/Main` and `refs/heads/main` are one file, while an exact
// read of a ref packed into packed-refs finds no other case at all.
func judgeForcedBranchOverwrite(ctx context.Context, caller Caller, inv Invocation, o subcommandOptions) (string, error) {
	flag := ""
	switch {
	case o.sets('M', "") || (o.sets('m', "--move") && forced(o)):
		flag = "-M"
	case o.sets('C', "") || (o.sets('c', "--copy") && forced(o)):
		flag = "-C"
	}

	if flag == "" || len(o.positional) == 0 {
		return "", nil
	}

	last := o.positional[len(o.positional)-1]
	if !last.Literal {
		return fmt.Sprintf("git branch %s: the guard cannot read the new name, which may be a branch it replaces; write the new name as a literal", flag), nil
	}

	targets, ok := targetOptions(inv)
	if !ok {
		return fmt.Sprintf("git branch %s: the guard cannot tell which repository it runs in; %s", flag, moveFirst), nil
	}

	runner := targetRunner(caller.Git, inv.Dir, inv.Target)
	name := last.Text

	expanded, err := runner.Run(ctx, slices.Concat(targets, []string{"check-ref-format", "--branch", name})...)
	if err != nil {
		return "", fmt.Errorf("expand the branch name %s for git branch %s: %w", name, flag, err)
	}

	branch := strings.TrimSpace(expanded.Stdout)

	branches, err := git.Repo{Runner: runner}.LocalBranches(ctx, targets...)
	if err != nil {
		return "", fmt.Errorf("read whether git branch %s replaces %s: %w", flag, branch, err)
	}

	for _, existing := range branches {
		switch {
		case !strings.EqualFold(existing, branch):
		case existing == branch:
			return fmt.Sprintf("git branch %s replaces the existing branch %s and its reflog", flag, branch), nil
		default:
			return fmt.Sprintf("git branch %s onto %s can replace the existing branch %s and its reflog: "+
				"where the filesystem folds case, git reads both names as one", flag, branch, existing), nil
		}
	}

	return "", nil
}

// operationAt returns the index in args of the word that names the operation of the subcommand
// sub, or len(args) when no word does: the first word, after the options git's submodule script
// reads before its operation.
func operationAt(sub string, args []Word) int {
	if sub != "submodule" {
		return 0
	}

	leading := []string{"-q", "--quiet", "--cached"}
	at := slices.IndexFunc(args, func(w Word) bool { return !w.Literal || !slices.Contains(leading, w.Text) })
	if at < 0 {
		return len(args)
	}

	return at
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

// unreadArgumentError is what a rule returns for a git invocation of the subcommand sub, one of
// whose arguments outside a value slot is not a literal: it may hold an option the guard decides
// by. judgeInvocation refuses the invocation with the error's text and the spelling of the
// argument that the guard allows.
type unreadArgumentError struct {
	sub string
}

// Error names the git subcommand and says that an argument it cannot read may hold an option the
// guard checks for.
func (e *unreadArgumentError) Error() string {
	return fmt.Sprintf("git %s: the argument cannot be read, and it may hold an option the guard checks for", e.sub)
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
// subcommand, `-n` on `am` and `commit`, or a global `-c` or `--config-env` option that sets
// hooksPathKey. It refuses a `-c` or `--config-env` option whose key
// it cannot read, and an argument it cannot read of a subcommand that runs those checks.
func refuseSkippedChecks(_ context.Context, _ Caller, inv Invocation) (Verdict, error) {
	if inv.Tool != Git {
		return Verdict{Outcome: Allow}, nil
	}

	command := "git"
	if inv.Git.Subcommand.Text != "" {
		command += " " + inv.Git.Subcommand.Text
	}

	for _, o := range inv.Git.Options {
		if configKeyUnread(o) {
			reason := fmt.Sprintf("%s: the key of %s cannot be read, and it may be %s; write the %s key as a literal",
				command, o.Name, hooksPathKey, o.Name)

			return refuse(reason), nil
		}

		// configKey reads a `-c` key up to the value's first `=` and a `--config-env` key up to its
		// last, and git reads a key's section and name in any case.
		if key, _, _ := configKey(o); strings.EqualFold(key, hooksPathKey) {
			reason := fmt.Sprintf("git %s %s replaces the checks set up to run before commits and pushes", o.Name, hooksPathKey)

			return refuse(reason), nil
		}
	}

	sub, ok := subcommand(inv)
	if !ok {
		return Verdict{Outcome: Allow}, nil
	}

	options := readArgs(inv.Git.Args, optionsOf(sub))
	skips, runsChecks := hookRunners()[sub]
	// am and commit spell --no-verify as -n too; elsewhere -n means something else.
	var letter byte
	if sub == "am" || sub == "commit" {
		letter = 'n'
	}

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
		return Verdict{}, &unreadArgumentError{sub: sub}
	default:
		return Verdict{Outcome: Allow}, nil
	}

	reason := fmt.Sprintf("git %s %s skips the checks set up to run before it", sub, flag)

	return refuse(reason), nil
}

// configKeyUnread reports whether the guard cannot read the key of the global `-c` or
// `--config-env` option o. git reads a `-c` key up to the first `=` of the value, so the key is read
// when the text before the first part the guard cannot read holds an `=`. A `--config-env` key runs
// up to the last `=`, which a part the guard cannot read may hold, so its key is read only when the
// whole value is.
func configKeyUnread(o GitOption) bool {
	switch o.Name {
	case "-c":
		return !o.Value.Literal && !strings.Contains(o.Value.Text, "=")
	case "--config-env":
		return !o.Value.Literal
	default:
		return false
	}
}

// refuseHistoryOverFile refuses `git show` or `git cat-file` whose output lands in a file of a git
// work tree, which writes a version from history over a file that may hold uncommitted changes.
// The output lands in the file stdout is redirected to, or else in each file a `tee` that reads it
// straight from the pipe names. A file outside every work tree, `/dev/null` among them, has no
// changes to lose; one the guard cannot read is refused.
func refuseHistoryOverFile(ctx context.Context, caller Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok || (sub != "show" && sub != "cat-file") {
		return Verdict{Outcome: Allow}, nil
	}

	from, targets := historyTargets(inv)
	for _, target := range targets {
		path := target.Text
		switch {
		case !target.Literal:
			return refuse(fmt.Sprintf("git %s writes to a file the guard cannot read; name the file as a literal path", sub)), nil
		case !filepath.IsAbs(path) && from == "":
			reason := fmt.Sprintf("git %s writes to %s from a directory the guard cannot tell; %s", sub, path, moveFirst)

			return refuse(reason), nil
		case !filepath.IsAbs(path):
			path = opened(from, path)
		}

		dir := nearestDirectory(path)
		_, inside, err := git.Repo{Runner: targetRunner(caller.Git, dir, GitTarget{})}.TopLevel(ctx)
		if err != nil {
			return Verdict{}, fmt.Errorf("read whether %s lies in a git work tree: %w", dir, err)
		}

		if inside {
			return refuse(fmt.Sprintf("git %s writes over %s, which may hold uncommitted changes", sub, path)), nil
		}
	}

	return Verdict{Outcome: Allow}, nil
}

// historyTargets returns the files the output of inv lands in, and the directory a relative one is
// opened from: the file its stdout is redirected to, from where the shell opened that redirect,
// which for one around a `cd` is before the `cd` runs; or else each operand of a `tee` that reads
// it straight from the pipe, from the directory inv runs in. A word that is not a literal may be
// either an option or a file, so it is returned as a file.
func historyTargets(inv Invocation) (string, []Word) {
	if opened, toFile := inv.stdoutFile(); toFile {
		return opened.Dir, []Word{opened.Target}
	}

	if len(inv.pipedInto) == 0 || !inv.pipedInto[0].Literal || programName(inv.pipedInto[0]) != "tee" {
		return inv.Dir, nil
	}

	return inv.Dir, operands(inv.pipedInto[1:])
}

// nearestDirectory returns the nearest directory that exists among those holding path, which the
// shell creates path in.
func nearestDirectory(path string) string {
	dir := filepath.Dir(path)
	for !isDirectory(dir) {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}

		dir = parent
	}

	return dir
}

// ---------------------------------------------------------------------------
// Rebase, worktree, config, and remote rules
// ---------------------------------------------------------------------------

// refuseRebaseOverChanges refuses `git rebase` in a work tree that holds uncommitted or
// untracked changes, and one whose repository the guard cannot tell. Outside any repository there
// is nothing for it to lose, and an action keepsWorkTree names leaves the work tree as it is.
func refuseRebaseOverChanges(ctx context.Context, caller Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok || sub != "rebase" {
		return Verdict{Outcome: Allow}, nil
	}

	unknownRepository := refuse("git rebase: the guard cannot tell which repository it runs in; " + moveFirst)
	if inv.Git.GitDirUnknown {
		return unknownRepository, nil
	}

	if inv.Git.GitDir == "" || keepsWorkTree(readArgs(inv.Git.Args, optionsOf(sub))) {
		return Verdict{Outcome: Allow}, nil
	}

	options, known := targetOptions(inv)
	if !known {
		return unknownRepository, nil
	}

	dirty, err := git.Repo{Runner: targetRunner(caller.Git, inv.Dir, inv.Target)}.Dirty(ctx, options...)
	if err != nil {
		return Verdict{}, fmt.Errorf("read whether git rebase runs over changes: %w", err)
	}

	if dirty {
		return refuse("git rebase (dirty): the working tree holds uncommitted or untracked changes"), nil
	}

	return Verdict{Outcome: Allow}, nil
}

// keepsWorkTree reports whether the readable `git rebase` options o set an action that discards
// nothing in the work tree: --continue commits what is staged, and the others edit or show the
// rebase's state, or drop it while leaving HEAD and the work tree where they are. git refuses an
// action beside another, so one of these set means no other runs.
func keepsWorkTree(o subcommandOptions) bool {
	keeping := []string{"--continue", "--edit-todo", "--show-current-patch", "--quit"}

	return !o.unreadable && slices.ContainsFunc(keeping, func(action string) bool { return o.sets(0, action) })
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
		return Verdict{}, &unreadArgumentError{sub: sub}
	}

	if inv.Git.Args[0].Text != "remove" {
		return Verdict{Outcome: Allow}, nil
	}

	options := readArgs(inv.Git.Args[1:], knownOptions{})
	if forced(options) {
		return refuse("git worktree remove --force deletes the worktree's uncommitted and untracked changes"), nil
	}

	if options.unreadable {
		return Verdict{}, &unreadArgumentError{sub: sub}
	}

	path, why := worktreePath(inv, options)
	if why != "" {
		return refuse("git worktree remove (unverifiable path): " + why), nil
	}

	dirty, err := git.Repo{Runner: targetRunner(caller.Git, path, GitTarget{})}.Dirty(ctx)
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
	case len(options.positional) != 1:
		return "", "it names no single worktree"
	case !options.positional[0].Literal:
		return "", "the path cannot be read; write the path as a literal"
	}

	path = options.positional[0].Text
	if !filepath.IsAbs(path) {
		if inv.Target.Dir == "" {
			return "", "the directory git runs in cannot be followed; " + moveFirst
		}

		path = opened(inv.Target.Dir, path)
	}

	if !isDirectory(path) {
		return "", path + " is not a directory"
	}

	return path, ""
}

// guardedSections returns the config sections that make git reach another repository or rewrite
// the URL it pushes to: the `url.<base>` keys replace the start of a URL.
func guardedSections() []string {
	return []string{"remote", "url"}
}

// refuseGuardedConfigWrite refuses `git config` writing, unsetting, renaming or removing
// hooksPathKey, the section that holds it, or a section of guardedSections, in any scope. Reads are
// allowed, and so is a write of any other key or opening the config in an editor.
func refuseGuardedConfigWrite(_ context.Context, _ Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok || sub != "config" {
		return Verdict{Outcome: Allow}, nil
	}

	options := readArgs(inv.Git.Args, optionsOf(sub))
	if options.opaque() {
		return Verdict{}, &unreadArgumentError{sub: sub}
	}

	action, words := configAction(options)
	count := 0
	switch action {
	case "set", "unset", "remove-section":
		count = 1
	case "rename-section":
		count = 2
	}

	for _, name := range words[:min(len(words), count)] {
		if guardedConfigName(name) {
			return refuse(fmt.Sprintf("git config writing %s changes what git runs or where it pushes", name)), nil
		}
	}

	return Verdict{Outcome: Allow}, nil
}

// configAction returns what `git config` with the arguments options does, by the name of its
// subcommand form: get, set, unset, rename-section, remove-section or edit, with the words after
// it. An option that adds or replaces a value counts as set, and one that removes values as unset.
// Opening the editor wins over any other action. Two words and no action set the first.
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
		case l.name == "--replace-all" || l.name == "--add":
			action = "set"
		case l.name == "--unset" || l.name == "--unset-all":
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
// guardedSections, hooksPathKey, or the section that holds hooksPathKey, which a rename or a removal
// of the whole section writes. git reads the section and the variable name in any case.
func guardedConfigName(name string) bool {
	section, _, _ := strings.Cut(name, ".")
	hooksSection, _, _ := strings.Cut(hooksPathKey, ".")

	return containsFold(guardedSections(), section) || strings.EqualFold(name, hooksPathKey) ||
		strings.EqualFold(name, hooksSection)
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
		return Verdict{}, &unreadArgumentError{sub: sub}
	}

	if len(options.positional) == 0 || options.positional[0].Text != git.OriginRemote {
		return Verdict{Outcome: Allow}, nil
	}

	reason := fmt.Sprintf("git remote %s origin changes the remote branches are pushed to and fetched from", op.Text)

	return refuse(reason), nil
}

// remoteVerbosity reports whether word is the one option `git remote` takes before its own
// subcommand: -v, bundled or not, or --verbose, --no-verbose or a start of either.
func remoteVerbosity(word string) bool {
	if name, ok := strings.CutPrefix(word, "--"); ok {
		startsLong := func(long string) bool { return strings.HasPrefix(long, name) }

		return name != "" && slices.ContainsFunc([]string{"verbose", "no-verbose"}, startsLong)
	}

	letters, ok := strings.CutPrefix(word, "-")

	return ok && letters != "" && strings.Trim(letters, "v") == ""
}
