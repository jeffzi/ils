//go:build unix

// cspell:ignore forc noval -- deliberately truncated or Perl-negated option spellings git accepts.

package guard_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
)

// refusal is a command the guard refuses, with the text its reason must hold.
type refusal struct {
	name      string
	command   string
	wantNamed string
}

// checkRefusals fails unless Judge refuses every command naming what its row wants named.
func checkRefusals(t *testing.T, tests []refusal) {
	t.Helper()

	_, caller := standingOn(t, "main", "topic")
	rows := make([]judged, 0, len(tests))
	for _, tt := range tests {
		rows = append(rows, judged{name: tt.name, command: tt.command, wantNamed: tt.wantNamed, want: guard.Refuse})
	}

	checkJudged(t, caller, rows)
}

func TestJudge_RefusesAGitOperationThatDiscardsWork(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "checkout of a path", command: "git checkout -- file", wantNamed: "git checkout"},
		{name: "switch -f", command: "git switch -f branch", wantNamed: "git switch"},
		{name: "switch --force", command: "git switch --force branch", wantNamed: "git switch"},
		{name: "switch --discard-changes", command: "git switch --discard-changes branch", wantNamed: "git switch"},
		{name: "clean -f", command: "git clean -f", wantNamed: "git clean"},
		{name: "clean --force", command: "git clean --force", wantNamed: "git clean"},
		{name: "clean bundle", command: "git clean -fd", wantNamed: "git clean"},
		{name: "clean flags by brace expansion", command: "git clean -{f,d}", wantNamed: "git clean"},
		{name: "branch -D", command: "git branch -D feature", wantNamed: "git branch"},
		{name: "branch --delete --force", command: "git branch --delete --force feature", wantNamed: "git branch"},
		{name: "branch -d -f", command: "git branch -d -f feature", wantNamed: "git branch"},
		{name: "branch -D behind a -C value spelled like the subcommand", command: "git -C branch branch -D main", wantNamed: "git branch"},
		{name: "rm -f", command: "git rm -f file", wantNamed: "git rm"},
		{name: "rm --force", command: "git rm --force file", wantNamed: "git rm"},
		{name: "reflog expire", command: "git reflog expire", wantNamed: "git reflog expire"},
		{name: "gc pruning now", command: "git gc --prune=now", wantNamed: "git gc"},
		{name: "apply -R", command: "git apply -R patch.diff", wantNamed: "git apply"},
		{name: "apply --reverse", command: "git apply --reverse patch.diff", wantNamed: "git apply"},
		{name: "add -f", command: "git add -f some-file", wantNamed: "git add"},
		{name: "add --force", command: "git add --force some-file", wantNamed: "git add"},
	})
}

func TestJudge_RefusesARestoreThatDiscardsWork(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "restore of a path", command: "git restore file", wantNamed: "git restore"},
		{name: "restore --staged --worktree", command: "git restore --staged --worktree file", wantNamed: "git restore"},
		{name: "restore --staged -W", command: "git restore --staged -W file", wantNamed: "git restore"},
	})
}

func TestJudge_RefusesADiscardingGitOperationWhereverTheShellRunsIt(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "bash -c string", command: `bash -c "git reset --hard"`, wantNamed: "git reset"},
		{name: "eval string", command: `eval "git stash"`, wantNamed: "git stash"},
		{name: "heredoc fed to a shell", command: "bash <<'EOF'\ngit reset --hard\nEOF", wantNamed: "git reset"},
		{name: "quoted -C value", command: `git -C "$wt" checkout .`, wantNamed: "git checkout"},
	})
}

func TestJudge_RefusesAForcedStageLikeAForcedAdd(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "stage -f", command: "git stage -f x", wantNamed: "git stage"},
		{name: "stage --pathspec-from-file taking --", command: "git stage --pathspec-from-file -- -f", wantNamed: "git stage"},
	})
}

func TestJudge_WhenAGlobalOptionValueLooksLikeAnAllowingFlag_StillRefuses(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "-C value ending in --staged", command: "git -C x--staged restore .", wantNamed: "git restore"},
	})
}

func TestJudge_RefusesSkippingGitsCommitChecks(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "commit --no-verify", command: "git commit --no-verify -m 'msg'", wantNamed: "--no-verify"},
		{name: "commit -n", command: "git commit -n -m 'msg'", wantNamed: "git commit"},
		{name: "send-email --no-validate", command: "git send-email --no-validate x.patch", wantNamed: "--no-validate"},
		{name: "prefix of send-email --novalidate", command: "git send-email --noval x.patch", wantNamed: "--noval"},
		{name: "-c with a separate value", command: "git -c core.hooksPath=/dev/null commit -m msg", wantNamed: "core.hooksPath"},
		{name: "-c in another case", command: "git -c Core.HooksPATH=x commit -m msg", wantNamed: "core.hooksPath"},
		{name: "--config-env with a separate value", command: "git --config-env core.hooksPath=X commit -m msg", wantNamed: "core.hooksPath"},
	})
}

func TestJudge_RefusesWritingGitHistoryOverAFile(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "show truncating", command: "git show HEAD > output.txt", wantNamed: "git show"},
		{name: "cat-file truncating", command: "git cat-file -p HEAD:f > f", wantNamed: "git cat-file"},
	})
}

// namedCommand is a command, with the name of the test case that runs it.
type namedCommand struct {
	name    string
	command string
}

func keepingWork() []namedCommand {
	return []namedCommand{
		{name: "plain switch", command: "git switch branch"},
		{name: "clean -n", command: "git clean -n"},
		{name: "branch -d", command: "git branch -d feature"},
		{name: "prefix of branch --delete alone", command: "git branch --del feature"},
		{name: "restore --staged of a path", command: "git restore --staged file"},
		{name: "prefix of restore --staged", command: "git restore --stag file"},
		{name: "restore -S", command: "git restore -S file"},
		{name: "plain rm", command: "git rm file"},
		{name: "rm of a path spelled like a flag", command: "git rm -- -f"},
		{name: "--namespace value --force", command: "git --namespace --force rm file"},
		{name: "-c of another key", command: "git -c core.pager=cat commit -m msg"},
		{name: "plain gc", command: "git gc"},
		{name: "reflog", command: "git reflog"},
		{name: "reflog show", command: "git reflog show"},
		{name: "show with only stderr redirected", command: "git show HEAD:f 2> err"},
		{name: "plain apply", command: "git apply patch.diff"},
		{name: "commit", command: "git commit -m 'msg'"},
		{name: "send-email --validate", command: "git send-email --validate x.patch"},
		{name: "plain add", command: "git add ."},
		{name: "restore --staged after an attached --pathspec-from-file", command: "git restore --pathspec-from-file=x --staged f"},
		{name: "rebase", command: "git rebase main"},
		{name: "operation named in a commit message", command: `git commit -m "docs: use git switch; git checkout is on the deny list"`},
	}
}

func TestJudge_AllowsAGitOperationThatKeepsWork(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, keepingWork())
}

// checkAllowed fails unless Judge, run by caller, allows every command.
func checkAllowed(t *testing.T, caller guard.Caller, tests []namedCommand) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checkJudgedRow(t, caller, judged{command: tt.command, want: guard.Allow})
		})
	}
}

// placed is a command run from a directory.
type placed struct {
	name    string
	dir     string
	command string
}

// checkRefusedIn fails unless Judge, run by caller from each row's directory, refuses the row's
// command with a reason holding wantNamed.
func checkRefusedIn(t *testing.T, caller guard.Caller, tests []placed, wantNamed string) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := caller
			in.Dir = tt.dir

			checkJudgedRow(t, in, judged{command: tt.command, wantNamed: wantNamed, want: guard.Refuse})
		})
	}
}

// checkAllowedIn fails unless Judge, run by caller from each row's directory, allows the row's
// command.
func checkAllowedIn(t *testing.T, caller guard.Caller, tests []placed) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := caller
			in.Dir = tt.dir

			checkJudgedRow(t, in, judged{command: tt.command, want: guard.Allow})
		})
	}
}

// changeTracked changes a file f tracks in the work tree dir, and returns dir.
func changeTracked(t *testing.T, f *gittest.Fixture, dir string) string {
	t.Helper()

	tracked, _, _ := strings.Cut(f.Git("-C", dir, "ls-files"), "\n")
	gittest.WriteFile(t, dir, tracked, "changed\n")

	return dir
}

// addWorktree adds a linked worktree of f at dir on a new branch named for it, and returns dir.
func addWorktree(f *gittest.Fixture, dir string) string {
	f.Git("worktree", "add", "--quiet", "-b", filepath.Base(dir), dir)

	return dir
}

func TestJudge_RefusesARebaseOverUncommittedWork(t *testing.T) {
	t.Parallel()

	dirty, caller := standingOn(t, "main", "topic")
	dirty.DirtyFile()
	clean, _ := standingOn(t, "main", "topic")

	checkRefusedIn(t, caller, []placed{
		{name: "in the dirty tree", dir: dirty.Dir(), command: "git rebase main"},
		{name: "-C naming the dirty tree", dir: clean.Dir(), command: "git -C " + quotedPath(dirty.Dir()) + " rebase main"},
	}, "dirty")
}

func TestJudge_RefusesARebaseWhoseTargetIsUnknown(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "main")

	checkRefusedIn(t, caller, []placed{
		{name: "non-literal -C", dir: f.Dir(), command: `git -C "$d" rebase main`},
	}, "git rebase")
}

func TestJudge_AllowsARebaseThatCannotLoseWork(t *testing.T) {
	t.Parallel()

	dirty, caller := standingOn(t, "main", "topic")
	dirty.DirtyFile()
	clean, _ := standingOn(t, "main", "topic")

	checkAllowedIn(t, caller, []placed{
		{name: "-C naming a clean tree from a dirty one", dir: dirty.Dir(), command: "git -C " + quotedPath(clean.Dir()) + " rebase main"},
		{name: "outside any repository", dir: outsideDir(t), command: "git rebase main"},
	})
}

// worktrees is a repository with linked worktrees elsewhere, and two more that each hold a linked
// worktree named wt1 inside their work tree.
type worktrees struct {
	// repo holds the directories remove and worktree, named like the words of git worktree remove.
	repo string
	// clean and dirty are linked worktrees of repo, with a tracked file changed in dirty.
	clean, dirty string
	// held holds a dirty wt1, heldClean a clean one.
	held, heldClean string
	// caller works in repo.
	caller guard.Caller
}

// newWorktrees builds the repositories and worktrees the worktree removal tests read.
func newWorktrees(t *testing.T) worktrees {
	t.Helper()

	f := gittest.New(t, "main")
	outside := outsideDir(t)
	for _, name := range []string{"remove", "worktree"} {
		assert.NilError(t, os.Mkdir(filepath.Join(f.Dir(), name), 0o750))
	}
	held := gittest.New(t, "main")
	changeTracked(t, held, addWorktree(held, filepath.Join(held.Dir(), "wt1")))
	heldClean := gittest.New(t, "main")
	addWorktree(heldClean, filepath.Join(heldClean.Dir(), "wt1"))

	return worktrees{
		caller:    callerIn(f, f.Dir()),
		repo:      f.Dir(),
		clean:     addWorktree(f, filepath.Join(outside, "clean")),
		dirty:     changeTracked(t, f, addWorktree(f, filepath.Join(outside, "dirty"))),
		held:      held.Dir(),
		heldClean: heldClean.Dir(),
	}
}

func TestJudge_RefusesAForcedWorktreeRemovalWhateverThePath(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	clean := quotedPath(addWorktree(f, filepath.Join(outsideDir(t), "clean")))

	checkRefusedIn(t, callerIn(f, f.Dir()), []placed{
		{name: "--force", dir: f.Dir(), command: "git worktree remove --force " + clean},
		{name: "prefix of --force", dir: f.Dir(), command: "git worktree remove --forc " + clean},
		{name: "bundle holding f", dir: f.Dir(), command: "git worktree remove -vf " + clean},
	}, "git worktree remove")
}

func TestJudge_RefusesAWorktreeRemovalItCannotVerify(t *testing.T) {
	t.Parallel()

	w := newWorktrees(t)

	checkRefusedIn(t, w.caller, []placed{
		{name: "non-literal path", dir: w.repo, command: `git worktree remove "$wt"`},
		{name: "no path", dir: w.repo, command: "git worktree remove"},
		{name: "missing path", dir: w.repo, command: "git worktree remove " + quotedPath(filepath.Join(w.repo, "absent"))},
		{name: "path that would glob onto a dirty worktree", dir: w.held, command: "git worktree remove wt*"},
		{name: "relative path absent under -C though present where it runs", dir: w.heldClean, command: "git -C " + quotedPath(w.repo) + " worktree remove wt1"},
	}, "unverifiable")
}

func TestJudge_RefusesRemovingADirtyWorktree(t *testing.T) {
	t.Parallel()

	w := newWorktrees(t)
	heldClean := filepath.Dir(w.heldClean)
	heldCleanName := quotedPath(filepath.Base(w.heldClean))

	checkRefusedIn(t, w.caller, []placed{
		{name: "changed tracked file", dir: w.repo, command: "git worktree remove " + quotedPath(w.dirty)},
		{name: "relative path under -C", dir: w.repo, command: "git -C " + quotedPath(w.held) + " worktree remove wt1"},
		{
			name:    "relative path under an absolute -C after a relative one",
			dir:     heldClean,
			command: "git -C " + heldCleanName + " -C " + quotedPath(w.held) + " worktree remove wt1",
		},
	}, "dirty")
}

func TestJudge_AllowsAWorktreeCommandThatKeepsWork(t *testing.T) {
	t.Parallel()

	w := newWorktrees(t)

	checkAllowedIn(t, w.caller, []placed{
		{name: "absolute path", dir: w.repo, command: "git worktree remove " + quotedPath(w.clean)},
		{name: "relative path", dir: w.heldClean, command: "git worktree remove wt1"},
		{name: "relative path under -C from a tree holding a dirty one", dir: w.held, command: "git -C " + quotedPath(w.heldClean) + " worktree remove wt1"},
		{name: "list", dir: w.repo, command: "git worktree list"},
	})
}

func TestJudge_RefusesAGitConfigWriteOfAKeyThatChangesWhatGitRuns(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "key and value", command: "git config alias.p push", wantNamed: "alias.p"},
		{name: "--global", command: "git config --global alias.p push", wantNamed: "alias.p"},
		{name: "--file with a separate value", command: "git config --file other.cfg include.path x", wantNamed: "include.path"},
		{name: "-f", command: "git config -f other.cfg alias.p push", wantNamed: "alias.p"},
		{name: "--type with an attached value", command: "git config --type=path include.path x", wantNamed: "include.path"},
		{name: "includeIf", command: "git config includeIf.gitdir:/srv/.path x", wantNamed: "includeIf.gitdir:/srv/.path"},
		{name: "section in another case", command: "git config ALIAS.p push", wantNamed: "ALIAS.p"},
		{name: "variable in another case", command: "git config core.HOOKSPath x", wantNamed: "core.HOOKSPath"},
		{name: "--add", command: "git config --add include.path x", wantNamed: "include.path"},
		{name: "set", command: "git config set core.hooksPath x", wantNamed: "core.hooksPath"},
		{name: "unset", command: "git config unset alias.p", wantNamed: "alias.p"},
		{name: "--rename-section out of a guarded section", command: "git config --rename-section remote.origin remote.other", wantNamed: "remote.origin"},
		{name: "--rename-section into a guarded section", command: "git config --rename-section team alias", wantNamed: "alias"},
		{name: "--remove-section", command: "git config --remove-section alias", wantNamed: "alias"},
		{name: "prefix of --remove-section", command: "git config --remove alias", wantNamed: "alias"},
		{name: "prefix of --type with a separate value", command: "git config --ty path include.path /x", wantNamed: "include.path"},
		{name: "diff driver command in another case", command: "git config DIFF.x.Command x", wantNamed: "DIFF.x.Command"},
		{name: "credential helper run by the shell", command: "git config credential.helper '!x'", wantNamed: "credential.helper"},
		{name: "credential helper of a URL given as a path", command: "git config credential.https://h.helper /bin/x", wantNamed: "credential.https://h.helper"},
		{name: "credential helper key in another case", command: "git config credential.HELPER '!x'", wantNamed: "credential.HELPER"},
		{name: "credential helper with ;", command: "git config credential.helper 'cache; touch x'", wantNamed: "credential.helper"},
	})
}

func TestJudge_RefusesAGitConfigWriteOfASectionThatHoldsAGuardedKey(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "rename into core", command: "git config --rename-section team core", wantNamed: "core"},
		{name: "rename into sequence", command: "git config rename-section team sequence", wantNamed: "sequence"},
		{name: "rename into core in another case", command: "git config --rename-section team CORE", wantNamed: "CORE"},
		{name: "rename into a filter driver", command: "git config --rename-section team filter.x", wantNamed: "filter.x"},
		{name: "rename into the credentials of a URL", command: "git config --rename-section team credential.https://h", wantNamed: "credential.https://h"},
	})
}

func TestJudge_AllowsAGitConfigCallThatLeavesGuardedKeys(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "--get", command: "git config --get alias.p"},
		{name: "get", command: "git config get remote.origin.url"},
		{name: "key alone", command: "git config alias.p"},
		{name: "--list", command: "git config --list"},
		{name: "write of another key", command: "git config user.name Someone"},
		{name: "write of another core key", command: "git config core.autocrlf true"},
		{name: "filter driver key that runs nothing", command: "git config filter.lfs.required true"},
		{name: "merge driver name", command: "git config merge.x.name x"},
		{name: "diff key that runs nothing", command: "git config diff.renames true"},
		{name: "removal of a diff driver", command: "git config --remove-section diff.x"},
		{name: "credential helper with plain arguments", command: "git config credential.helper 'cache --timeout 3600'"},
		{name: "--unset of a credential helper whatever its value-pattern", command: "git config --unset credential.helper 'a|b'"},
		{name: "--unset-all of a credential helper whatever its value-pattern", command: "git config --unset-all credential.helper '!.*'"},
	})
}

func TestJudge_RefusesOpeningTheGitConfigEditor(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "--edit", command: "git config --edit", wantNamed: "core.hooksPath"},
		{name: "-e", command: "git config -e", wantNamed: "core.hooksPath"},
		{name: "edit", command: "git config edit", wantNamed: "core.hooksPath"},
	})
}

func TestJudge_RefusesRewritingTheOriginRemote(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "remove", command: "git remote remove origin", wantNamed: "origin"},
		{name: "set-url --push", command: "git remote set-url --push origin https://example.com/x.git", wantNamed: "origin"},
		{name: "rename after -v", command: "git remote -v rename origin other", wantNamed: "origin"},
		{name: "set-url after --verbose", command: "git remote --verbose set-url origin https://example.com/x.git", wantNamed: "origin"},
	})
}

func TestJudge_AllowsARemoteCallThatLeavesOriginAsItIs(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "remove", command: "git remote remove upstream"},
		{name: "add", command: "git remote add upstream https://example.com/x.git"},
		{name: "listing", command: "git remote -v"},
	})
}

func TestJudge_RefusesANonLiteralArgumentAFlagCouldHideIn(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "rm", command: `git rm "$f"`, wantNamed: "git rm"},
		{name: "commit", command: `git commit "$opt" -m msg`, wantNamed: "git commit"},
		{name: "reflog", command: `git reflog "$sub"`, wantNamed: "git reflog"},
		{name: "remote", command: `git remote remove "$r"`, wantNamed: "git remote"},
		{name: "config", command: `git config "$k" x`, wantNamed: "git config"},
		{name: "long option holding a variable", command: `git restore "--st$x" f`, wantNamed: "git restore"},
		{name: "-c value", command: `git -c "$v" commit -m msg`, wantNamed: "git commit"},
	})
}

func TestJudge_WhenAWorkTreeReadFails_RefusesSayingWhatBroke(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f, _ := standingOn(t, "main", "topic")
	clean := addWorktree(f, filepath.Join(outsideDir(t), "clean"))
	env := append(f.Env(), gittest.FakeGit(t, gittest.Fake{FailOn: []string{"status"}})...)
	caller := callerWithEnv(f.Dir(), env)
	tests := []namedCommand{
		{name: "rebase", command: "git rebase main"},
		{name: "worktree remove", command: "git worktree remove " + quotedPath(clean)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkJudgedRow(t, caller, judged{command: tt.command, wantNamed: gittest.FailedSaid, want: guard.Refuse})
		})
	}
}
