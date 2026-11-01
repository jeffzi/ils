//go:build unix

// cspell:ignore verif forc ccore noval -- deliberately truncated or Perl-negated option spellings git accepts.

package guard_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
)

// refusal is a command the guard refuses, with the text its reason must hold.
type refusal struct {
	name      string
	command   string
	wantNamed string
}

// repositoryCaller returns a caller working in a new repository, on a branch off its trunk, where
// the trunk rules judge nothing.
func repositoryCaller(t *testing.T) guard.Caller {
	t.Helper()

	f := offTrunk(t)

	return guard.Caller{Home: home, Dir: f.Dir(), Git: git.Runner{Env: f.Env()}}
}

// offTrunk returns a new repository whose trunk is main, with HEAD on a branch cut from it.
func offTrunk(t *testing.T) *gittest.Fixture {
	t.Helper()

	f := gittest.New(t, "main")
	f.Commits("topic")

	return f
}

// checkRefusals fails unless Judge refuses every command naming what its row wants named.
func checkRefusals(t *testing.T, tests []refusal) {
	t.Helper()

	caller := repositoryCaller(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := guard.Judge(t.Context(), tt.command, caller)

			assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome, reason %q", tt.command, got.Reason)
			assert.Check(t, is.Contains(got.Reason, tt.wantNamed), "Judge(%q) reason", tt.command)
		})
	}
}

func TestJudge_RefusesAGitOperationThatDiscardsWork(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "checkout of a path", command: "git checkout -- file", wantNamed: "git checkout"},
		{name: "checkout of a path at a commit", command: "git checkout HEAD -- file", wantNamed: "git checkout"},
		{name: "checkout of the tree", command: "git checkout .", wantNamed: "git checkout"},
		{name: "forced checkout", command: "git checkout -f branch", wantNamed: "git checkout"},
		{name: "checkout of a branch", command: "git checkout branch", wantNamed: "git checkout"},
		{name: "checkout creating a branch", command: "git checkout -b new-branch", wantNamed: "git checkout"},
		{name: "checkout after a chain", command: "git add . && git checkout -- .", wantNamed: "git checkout"},
		{name: "switch -f", command: "git switch -f branch", wantNamed: "git switch"},
		{name: "switch --force", command: "git switch --force branch", wantNamed: "git switch"},
		{name: "switch --discard-changes", command: "git switch --discard-changes branch", wantNamed: "git switch"},
		{name: "plain reset", command: "git reset", wantNamed: "git reset"},
		{name: "reset to a commit", command: "git reset HEAD", wantNamed: "git reset"},
		{name: "soft reset", command: "git reset --soft HEAD~1", wantNamed: "git reset"},
		{name: "hard reset", command: "git reset --hard", wantNamed: "git reset"},
		{name: "reset after a quoted message", command: `git commit -m "msg"; git reset --hard`, wantNamed: "git reset"},
		{name: "plain stash", command: "git stash", wantNamed: "git stash"},
		{name: "stash push", command: "git stash push", wantNamed: "git stash"},
		{name: "stash list", command: "git stash list", wantNamed: "git stash"},
		{name: "stash pop", command: "git stash pop", wantNamed: "git stash"},
		{name: "stash after a semicolon", command: "git status; git stash", wantNamed: "git stash"},
		{name: "quoted subcommand", command: "git 'stash'", wantNamed: "git stash"},
		{name: "clean -f", command: "git clean -f", wantNamed: "git clean"},
		{name: "clean --force", command: "git clean --force", wantNamed: "git clean"},
		{name: "clean bundle", command: "git clean -fd", wantNamed: "git clean"},
		{name: "clean bundle of three", command: "git clean -fdx", wantNamed: "git clean"},
		{name: "escaped command word", command: `\git clean -fd`, wantNamed: "git clean"},
		{name: "clean flags by brace expansion", command: "git clean -{f,d}", wantNamed: "git clean"},
		{name: "branch -D", command: "git branch -D feature", wantNamed: "git branch"},
		{name: "branch --delete --force", command: "git branch --delete --force feature", wantNamed: "git branch"},
		{name: "branch -d -f", command: "git branch -d -f feature", wantNamed: "git branch"},
		{name: "branch -df", command: "git branch -df feature", wantNamed: "git branch"},
		{name: "branch -fd", command: "git branch -fd feature", wantNamed: "git branch"},
		{name: "branch -D behind a -C value spelled like the subcommand", command: "git -C branch branch -D main", wantNamed: "git branch"},
		{name: "rm -f", command: "git rm -f file", wantNamed: "git rm"},
		{name: "rm --force", command: "git rm --force file", wantNamed: "git rm"},
		{name: "rm -rf", command: "git rm -rf dir/", wantNamed: "git rm"},
		{name: "rm -qf", command: "git rm -qf file", wantNamed: "git rm"},
		{name: "reflog expire", command: "git reflog expire", wantNamed: "git reflog expire"},
		{name: "reflog delete", command: "git reflog delete", wantNamed: "git reflog delete"},
		{name: "reflog drop", command: "git reflog drop", wantNamed: "git reflog drop"},
		{name: "prune", command: "git prune", wantNamed: "git prune"},
		{name: "gc pruning now", command: "git gc --prune=now", wantNamed: "git gc"},
		{name: "gc pruning by age", command: "git gc --prune=2.weeks.ago", wantNamed: "git gc"},
		{name: "apply -R", command: "git apply -R patch.diff", wantNamed: "git apply"},
		{name: "apply --reverse", command: "git apply --reverse patch.diff", wantNamed: "git apply"},
		{name: "add -f", command: "git add -f some-file", wantNamed: "git add"},
		{name: "add --force", command: "git add --force some-file", wantNamed: "git add"},
		{name: "add -f of the tree", command: "git add -f .", wantNamed: "git add"},
		{name: "add --force -A", command: "git add --force -A", wantNamed: "git add"},
		{name: "add bundle", command: "git add -fA", wantNamed: "git add"},
	})
}

func TestJudge_RefusesARestoreThatDiscardsWork(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "restore of a path", command: "git restore file", wantNamed: "git restore"},
		{name: "restore of the tree", command: "git restore .", wantNamed: "git restore"},
		{name: "restore -W", command: "git restore -W file", wantNamed: "git restore"},
		{name: "restore --worktree", command: "git restore --worktree file", wantNamed: "git restore"},
		{name: "restore --staged --worktree", command: "git restore --staged --worktree file", wantNamed: "git restore"},
		{name: "restore --staged -W", command: "git restore --staged -W file", wantNamed: "git restore"},
		{name: "restore -S -W", command: "git restore -S -W file", wantNamed: "git restore"},
		{name: "restore -SW", command: "git restore -SW file", wantNamed: "git restore"},
	})
}

func TestJudge_RefusesADiscardingGitOperationWhereverTheShellRunsIt(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "subshell", command: "(git reset --hard)", wantNamed: "git reset"},
		{name: "bash -c string", command: `bash -c "git reset --hard"`, wantNamed: "git reset"},
		{name: "sh -c string", command: "sh -c 'git checkout .'", wantNamed: "git checkout"},
		{name: "eval string", command: `eval "git stash"`, wantNamed: "git stash"},
		{name: "heredoc fed to a shell", command: "bash <<'EOF'\ngit reset --hard\nEOF", wantNamed: "git reset"},
		{name: "fragment carrying the heredoc operator", command: "git reset --hard <<EOF\nnotes\nEOF", wantNamed: "git reset"},
		{
			name:      "fragment after the heredoc terminator",
			command:   "git commit -F - <<'EOF'\ngit reset --hard lands the branch\nEOF\ngit reset --hard",
			wantNamed: "git reset",
		},
		{name: "operator with no terminator line", command: "git commit -m 'see <<EOF for the shape'\ngit reset --hard", wantNamed: "git reset"},
		{name: "quoted -C value", command: `git -C "$wt" checkout .`, wantNamed: "git checkout"},
		{name: "-C value inside a loop", command: `for wt in a b; do git -C "$wt" checkout . 2>&1; done`, wantNamed: "git checkout"},
		{name: "quoted --git-dir value", command: `git --git-dir "$d" stash`, wantNamed: "git stash"},
	})
}

func TestJudge_RefusesAForcedStageLikeAForcedAdd(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "stage -f", command: "git stage -f x", wantNamed: "git stage"},
		{name: "stage --force", command: "git stage --force x", wantNamed: "git stage"},
		{name: "stage bundle", command: "git stage -fA", wantNamed: "git stage"},
		{name: "stage --pathspec-from-file taking --", command: "git stage --pathspec-from-file -- -f", wantNamed: "git stage"},
	})
}

func TestJudge_WhenAGlobalOptionValueLooksLikeAnAllowingFlag_StillRefuses(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "-C value ending in --staged", command: "git -C x--staged restore .", wantNamed: "git restore"},
		{name: "-c value holding --staged", command: "git -c a.b=--staged restore .", wantNamed: "git restore"},
		{name: "--namespace value --staged", command: "git --namespace --staged restore .", wantNamed: "git restore"},
	})
}

func TestJudge_RefusesSkippingGitsCommitChecks(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "commit --no-verify", command: "git commit --no-verify -m 'msg'", wantNamed: "--no-verify"},
		{name: "push --no-verify", command: "git push --no-verify", wantNamed: "--no-verify"},
		{name: "commit -n", command: "git commit -n -m 'msg'", wantNamed: "git commit"},
		{name: "am -n", command: "git am -n x.patch", wantNamed: "git am"},
		{name: "send-email --no-validate", command: "git send-email --no-validate x.patch", wantNamed: "--no-validate"},
		{name: "send-email --novalidate", command: "git send-email --novalidate x.patch", wantNamed: "--novalidate"},
		{name: "prefix of send-email --novalidate", command: "git send-email --noval x.patch", wantNamed: "--noval"},
		{name: "-c with a separate value", command: "git -c core.hooksPath=/dev/null commit -m msg", wantNamed: "core.hooksPath"},
		{name: "-c with an attached value", command: "git -ccore.hooksPath=x commit -m msg", wantNamed: "core.hooksPath"},
		{name: "-c in another case", command: "git -c Core.HooksPATH=x commit -m msg", wantNamed: "core.hooksPath"},
		{name: "--config-env with a separate value", command: "git --config-env core.hooksPath=X commit -m msg", wantNamed: "core.hooksPath"},
		{name: "--config-env with an attached value", command: "git --config-env=core.hooksPath=X commit -m msg", wantNamed: "core.hooksPath"},
	})
}

func TestJudge_RefusesWritingGitHistoryOverAFile(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "show truncating", command: "git show HEAD > output.txt", wantNamed: "git show"},
		{name: "show appending", command: "git show HEAD:f >> f", wantNamed: "git show"},
		{name: "show with stdout and stderr", command: "git show HEAD:f &> f", wantNamed: "git show"},
		{name: "show clobbering", command: "git show HEAD:f >| f", wantNamed: "git show"},
		{name: "cat-file truncating", command: "git cat-file blob HEAD:README > out", wantNamed: "git cat-file"},
		{name: "show in a redirected group", command: "{ git show HEAD:f; } > f", wantNamed: "git show"},
		{name: "show in a redirected subshell", command: "(git show HEAD:f) > f", wantNamed: "git show"},
		{name: "show in a group inside a redirected subshell", command: "({ git show HEAD:f; }) > f", wantNamed: "git show"},
		{name: "show after exec redirected stdout", command: "exec > f; git show HEAD:f", wantNamed: "git show"},
		{name: "show after exec redirected stdout in a group", command: "{ exec > f; }; git show HEAD:f", wantNamed: "git show"},
		{name: "show with stdout copied onto a redirected stderr", command: "git show HEAD:f 2> f >&2", wantNamed: "git show"},
		{name: "show with fd 1 copied onto a redirected fd 2", command: "git show HEAD:f 2>f 1>&2", wantNamed: "git show"},
		{name: "show copying stdout onto stderr in a group redirected to a file", command: "{ git show HEAD:f >&2; } 2> f", wantNamed: "git show"},
	})
}

// keptWork is a git operation that keeps work, with the command that runs it.
type keptWork struct {
	name    string
	command string
}

func keepingWork() []keptWork {
	return []keptWork{
		{name: "plain switch", command: "git switch branch"},
		{name: "switch creating a branch", command: "git switch -c new-branch"},
		{name: "clean -n", command: "git clean -n"},
		{name: "clean --dry-run", command: "git clean --dry-run"},
		{name: "branch -d", command: "git branch -d feature"},
		{name: "prefix of branch --delete alone", command: "git branch --del feature"},
		{name: "branch -a", command: "git branch -a"},
		{name: "creating a branch", command: "git branch new-branch"},
		{name: "restore --staged of a path", command: "git restore --staged file"},
		{name: "restore --staged of the tree", command: "git restore --staged ."},
		{name: "prefix of restore --staged", command: "git restore --stag file"},
		{name: "restore -S", command: "git restore -S file"},
		{name: "restore -S in a short-flag bundle", command: "git restore -qS file"},
		{name: "plain rm", command: "git rm file"},
		{name: "rm -r", command: "git rm -r dir/"},
		{name: "rm --cached", command: "git rm --cached file"},
		{name: "rm -n", command: "git rm -n file"},
		{name: "rm --dry-run", command: "git rm --dry-run file"},
		{name: "rm of a path spelled like a flag", command: "git rm -- -f"},
		{name: "--no-verify spelled as a path", command: "git log -- --no-verify"},
		{name: "--namespace value --force", command: "git --namespace --force rm file"},
		{name: "-c value -f", command: "git -c -f rm file"},
		{name: "-c pair holding -f", command: "git -c x.y=-f rm f"},
		{name: "-c of another key", command: "git -c core.pager=cat commit -m msg"},
		{name: "plain gc", command: "git gc"},
		{name: "reflog", command: "git reflog"},
		{name: "reflog show", command: "git reflog show"},
		{name: "show with only stderr redirected", command: "git show HEAD 2>errors.txt"},
		{name: "show in a group with only stderr redirected", command: "{ git show HEAD:f; } 2> err"},
		{name: "show sending stdout to stderr in a redirected group", command: "{ git show HEAD:f >&2; } > f"},
		{name: "show after exec redirected stdout in a subshell", command: "(exec > f); git show HEAD:f"},
		{name: "show before exec redirects stdout", command: "git show HEAD:f; exec > f"},
		{name: "show copying stdout onto stderr before stderr is redirected", command: "git show HEAD:f >&2 2> f"},
		{name: "plain apply", command: "git apply patch.diff"},
		{name: "commit", command: "git commit -m 'msg'"},
		{name: "commit --amend", command: "git commit --amend"},
		{name: "send-email --validate", command: "git send-email --validate x.patch"},
		{name: "plain add", command: "git add ."},
		{name: "plain stage", command: "git stage x"},
		{name: "restore --staged after an attached --pathspec-from-file", command: "git restore --pathspec-from-file=x --staged f"},
		{name: "status", command: "git status"},
		{name: "log", command: "git log --oneline -10"},
		{name: "diff", command: "git diff"},
		{name: "fetch", command: "git fetch origin"},
		{name: "pull", command: "git pull"},
		{name: "merge", command: "git merge main"},
		{name: "rebase", command: "git rebase main"},
		{name: "operation named in a commit message", command: `git commit -m "docs: use git switch; git checkout is on the deny list"`},
		{name: "operation named in a quoted echo", command: "echo 'never run this; git reset --hard'"},
		{name: "operation and separator in a commit message", command: `git commit -m "fix: handle a && b in parser; git clean -fd is now documented"`},
		{name: "operation named in a heredoc a commit reads", command: "git commit -F - <<'EOF'\ngit reset --hard lands the branch\nEOF"},
		{name: "operation named in a heredoc cat writes", command: "cat <<EOF > notes.txt\ngit reset --hard\nEOF"},
		{name: "operation named in echo words", command: "echo git reset is banned"},
		{name: "operation named in a search", command: "rg -n git reset docs/"},
	}
}

func TestJudge_AllowsAGitOperationThatKeepsWork(t *testing.T) {
	t.Parallel()

	checkAllowed(t, repositoryCaller(t), keepingWork())
}

// checkAllowed fails unless Judge, run by caller, allows every command.
func checkAllowed(t *testing.T, caller guard.Caller, tests []keptWork) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := guard.Judge(t.Context(), tt.command, caller)

			assert.Check(t, is.DeepEqual(got, guard.Verdict{Outcome: guard.Allow}), "Judge(%q)", tt.command)
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

			got := guard.Judge(t.Context(), tt.command, in)

			assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) in %s outcome, reason %q", tt.command, tt.dir, got.Reason)
			assert.Check(t, is.Contains(got.Reason, wantNamed), "Judge(%q) in %s reason", tt.command, tt.dir)
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

			got := guard.Judge(t.Context(), tt.command, in)

			assert.Check(t, is.DeepEqual(got, guard.Verdict{Outcome: guard.Allow}), "Judge(%q) in %s", tt.command, tt.dir)
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

	dirty := offTrunk(t)
	dirty.DirtyFile()
	clean := offTrunk(t)
	caller := guard.Caller{Home: home, Git: git.Runner{Env: dirty.Env()}}

	checkRefusedIn(t, caller, []placed{
		{name: "in the dirty tree", dir: dirty.Dir(), command: "git rebase main"},
		{name: "-C naming the dirty tree", dir: clean.Dir(), command: "git -C " + quotedPath(dirty.Dir()) + " rebase main"},
	}, "dirty")
}

func TestJudge_RefusesARebaseWhoseTargetIsUnknown(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	caller := guard.Caller{Home: home, Git: git.Runner{Env: f.Env()}}

	checkRefusedIn(t, caller, []placed{
		{name: "non-literal -C", dir: f.Dir(), command: `git -C "$d" rebase main`},
		{name: "after a cd to a non-literal", dir: f.Dir(), command: `cd "$d" && git rebase main`},
	}, "git rebase")
}

func TestJudge_AllowsARebaseThatCannotLoseWork(t *testing.T) {
	t.Parallel()

	dirty := offTrunk(t)
	dirty.DirtyFile()
	clean := offTrunk(t)
	caller := guard.Caller{Home: home, Git: git.Runner{Env: dirty.Env()}}

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
	// clean, dirty and untracked are linked worktrees of repo, with a tracked file changed in dirty
	// and an untracked file in untracked.
	clean, dirty, untracked string
	// file is a regular file outside any repository.
	file string
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
	untracked := addWorktree(f, filepath.Join(outside, "untracked"))
	gittest.WriteFile(t, untracked, "notes.txt", "draft\n")
	gittest.WriteFile(t, outside, "plain.txt", "text\n")
	held := gittest.New(t, "main")
	changeTracked(t, held, addWorktree(held, filepath.Join(held.Dir(), "wt1")))
	heldClean := gittest.New(t, "main")
	addWorktree(heldClean, filepath.Join(heldClean.Dir(), "wt1"))

	return worktrees{
		caller:    guard.Caller{Home: home, Dir: f.Dir(), Git: git.Runner{Env: f.Env()}},
		repo:      f.Dir(),
		clean:     addWorktree(f, filepath.Join(outside, "clean")),
		dirty:     changeTracked(t, f, addWorktree(f, filepath.Join(outside, "dirty"))),
		untracked: untracked,
		file:      filepath.Join(outside, "plain.txt"),
		held:      held.Dir(),
		heldClean: heldClean.Dir(),
	}
}

func TestJudge_RefusesAForcedWorktreeRemovalWhateverThePath(t *testing.T) {
	t.Parallel()

	w := newWorktrees(t)
	clean := quotedPath(w.clean)

	checkRefusedIn(t, w.caller, []placed{
		{name: "--force", dir: w.repo, command: "git worktree remove --force " + clean},
		{name: "prefix of --force", dir: w.repo, command: "git worktree remove --forc " + clean},
		{name: "-f", dir: w.repo, command: "git worktree remove -f " + clean},
		{name: "bundle holding f", dir: w.repo, command: "git worktree remove -vf " + clean},
		{
			name:    "--force in a loop over non-literal paths",
			dir:     w.repo,
			command: `for wt in a b; do git worktree unlock "$wt" 2>/dev/null; git worktree remove --force "$wt"; done`,
		},
	}, "git worktree remove")
}

func TestJudge_RefusesAWorktreeRemovalItCannotVerify(t *testing.T) {
	t.Parallel()

	w := newWorktrees(t)

	checkRefusedIn(t, w.caller, []placed{
		{name: "non-literal path", dir: w.repo, command: `git worktree remove "$wt"`},
		{name: "no path", dir: w.repo, command: "git worktree remove"},
		{name: "no path behind a -C value spelled remove", dir: w.repo, command: "git -C remove worktree remove"},
		{name: "missing path", dir: w.repo, command: "git worktree remove " + quotedPath(filepath.Join(w.repo, "absent"))},
		{name: "path that would glob onto a dirty worktree", dir: w.held, command: "git worktree remove wt*"},
		{name: "regular file", dir: w.repo, command: "git worktree remove " + quotedPath(w.file)},
		{name: "relative path absent under -C though present where it runs", dir: w.heldClean, command: "git -C " + quotedPath(w.repo) + " worktree remove wt1"},
	}, "unverifiable")
}

func TestJudge_RefusesRemovingADirtyWorktree(t *testing.T) {
	t.Parallel()

	w := newWorktrees(t)
	held, heldClean := filepath.Dir(w.held), filepath.Dir(w.heldClean)
	heldName, heldCleanName := quotedPath(filepath.Base(w.held)), quotedPath(filepath.Base(w.heldClean))

	checkRefusedIn(t, w.caller, []placed{
		{name: "changed tracked file", dir: w.repo, command: "git worktree remove " + quotedPath(w.dirty)},
		{name: "untracked file", dir: w.repo, command: "git worktree remove " + quotedPath(w.untracked)},
		{name: "behind a -C value spelled worktree", dir: w.repo, command: "git -C worktree worktree remove " + quotedPath(w.dirty)},
		{name: "relative path under -C", dir: w.repo, command: "git -C " + quotedPath(w.held) + " worktree remove wt1"},
		{name: "relative path under a relative -C", dir: held, command: "git -C " + heldName + " worktree remove wt1"},
		{
			name:    "relative path under chained -C values",
			dir:     w.repo,
			command: "git -C " + quotedPath(held) + " -C " + heldName + " worktree remove wt1",
		},
		{
			name:    "relative path under an absolute -C after a relative one",
			dir:     heldClean,
			command: "git -C " + heldCleanName + " -C " + quotedPath(w.held) + " worktree remove wt1",
		},
		{name: "relative path after a cd", dir: held, command: "cd " + heldName + " && git worktree remove wt1"},
	}, "dirty")
}

func TestJudge_AllowsAWorktreeCommandThatKeepsWork(t *testing.T) {
	t.Parallel()

	w := newWorktrees(t)

	checkAllowedIn(t, w.caller, []placed{
		{name: "absolute path", dir: w.repo, command: "git worktree remove " + quotedPath(w.clean)},
		{name: "-C naming the repository", dir: w.held, command: "git -C " + quotedPath(w.repo) + " worktree remove " + quotedPath(w.clean)},
		{name: "relative path", dir: w.heldClean, command: "git worktree remove wt1"},
		{name: "relative path under -C from a tree holding a dirty one", dir: w.held, command: "git -C " + quotedPath(w.heldClean) + " worktree remove wt1"},
		{name: "list", dir: w.repo, command: "git worktree list"},
		{name: "add", dir: w.repo, command: "git worktree add " + quotedPath(filepath.Join(filepath.Dir(w.clean), "added"))},
		{name: "unlock of a non-literal path", dir: w.repo, command: `git worktree unlock "$wt"`},
		{name: "prune", dir: w.repo, command: "git worktree prune"},
	})
}

func TestJudge_RefusesAGitConfigWriteOfAKeyThatChangesWhatGitRuns(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "key and value", command: "git config alias.p push", wantNamed: "alias.p"},
		{name: "--global", command: "git config --global alias.p push", wantNamed: "alias.p"},
		{name: "--local", command: "git config --local core.hooksPath /dev/null", wantNamed: "core.hooksPath"},
		{name: "--file with a separate value", command: "git config --file other.cfg include.path x", wantNamed: "include.path"},
		{name: "-f", command: "git config -f other.cfg alias.p push", wantNamed: "alias.p"},
		{name: "--type with an attached value", command: "git config --type=path include.path x", wantNamed: "include.path"},
		{name: "includeIf", command: "git config includeIf.gitdir:/srv/.path x", wantNamed: "includeIf.gitdir:/srv/.path"},
		{name: "remote", command: "git config remote.origin.url x", wantNamed: "remote.origin.url"},
		{name: "section in another case", command: "git config ALIAS.p push", wantNamed: "ALIAS.p"},
		{name: "variable in another case", command: "git config core.HOOKSPath x", wantNamed: "core.HOOKSPath"},
		{name: "--add", command: "git config --add include.path x", wantNamed: "include.path"},
		{name: "--replace-all", command: "git config --replace-all remote.origin.fetch x", wantNamed: "remote.origin.fetch"},
		{name: "set", command: "git config set core.hooksPath x", wantNamed: "core.hooksPath"},
		{name: "set with a scope", command: "git config set --global alias.p push", wantNamed: "alias.p"},
		{name: "--unset", command: "git config --unset remote.origin.url", wantNamed: "remote.origin.url"},
		{name: "unset", command: "git config unset alias.p", wantNamed: "alias.p"},
		{name: "--rename-section out of a guarded section", command: "git config --rename-section remote.origin remote.other", wantNamed: "remote.origin"},
		{name: "--rename-section into a guarded section", command: "git config --rename-section team alias", wantNamed: "alias"},
		{name: "--remove-section", command: "git config --remove-section alias", wantNamed: "alias"},
		{name: "prefix of --remove-section", command: "git config --remove alias", wantNamed: "alias"},
		{name: "prefix of --rename-section into a guarded section", command: "git config --rename team alias", wantNamed: "alias"},
		{name: "prefix of --type with a separate value", command: "git config --ty path include.path /x", wantNamed: "include.path"},
		{name: "core.editor", command: "git config core.editor vim", wantNamed: "core.editor"},
		{name: "core.pager", command: "git config --global core.pager less", wantNamed: "core.pager"},
		{name: "sequence.editor", command: "git config set sequence.editor vim", wantNamed: "sequence.editor"},
		{name: "core.sshCommand", command: "git config core.sshCommand ssh", wantNamed: "core.sshCommand"},
		{name: "core.sshCommand in another case", command: "git config Core.SshCommand ssh", wantNamed: "Core.SshCommand"},
		{name: "pager of a command", command: "git config pager.log less", wantNamed: "pager.log"},
		{name: "pager section in another case", command: "git config PAGER.log less", wantNamed: "PAGER.log"},
		{name: "--unset of a pager", command: "git config --unset pager.diff", wantNamed: "pager.diff"},
		{name: "--remove-section of the pager section", command: "git config --remove-section pager", wantNamed: "pager"},
	})
}

func TestJudge_RefusesAGitConfigWriteOfASectionThatHoldsAGuardedKey(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "rename into core", command: "git config --rename-section team core", wantNamed: "core"},
		{name: "rename out of core", command: "git config --rename-section core team", wantNamed: "core"},
		{name: "rename into sequence", command: "git config rename-section team sequence", wantNamed: "sequence"},
		{name: "remove core", command: "git config --remove-section core", wantNamed: "core"},
		{name: "rename into core in another case", command: "git config --rename-section team CORE", wantNamed: "CORE"},
	})
}

func TestJudge_AllowsAGitConfigCallThatLeavesGuardedKeys(t *testing.T) {
	t.Parallel()

	checkAllowed(t, repositoryCaller(t), []keptWork{
		{name: "--get", command: "git config --get alias.p"},
		{name: "get", command: "git config get remote.origin.url"},
		{name: "key alone", command: "git config alias.p"},
		{name: "--get-regexp", command: "git config --get-regexp alias"},
		{name: "prefix of --get-all", command: "git config --get-a alias.x"},
		{name: "--list", command: "git config --list"},
		{name: "list", command: "git config list"},
		{name: "write of another key", command: "git config user.name Someone"},
		{name: "--unset of another key", command: "git config --unset user.name"},
		{name: "--get of an editor", command: "git config --get core.editor"},
		{name: "get of a pager", command: "git config get pager.log"},
		{name: "write of another core key", command: "git config core.autocrlf true"},
	})
}

func TestJudge_RefusesOpeningTheGitConfigEditor(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "--edit", command: "git config --edit", wantNamed: "core.hooksPath"},
		{name: "-e", command: "git config -e", wantNamed: "core.hooksPath"},
		{name: "-e after a scope", command: "git config --global -e", wantNamed: "core.hooksPath"},
		{name: "edit", command: "git config edit", wantNamed: "core.hooksPath"},
		{name: "edit with a scope", command: "git config edit --global", wantNamed: "core.hooksPath"},
	})
}

func TestJudge_WhenOutsideAnyRepository_RefusesAGitConfigWriteOfAGuardedKey(t *testing.T) {
	t.Parallel()
	const command = "git config --global alias.p push"
	f := gittest.New(t, "main")

	got := guard.Judge(t.Context(), command, guard.Caller{Home: home, Dir: outsideDir(t), Git: git.Runner{Env: f.Env()}})

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome, reason %q", command, got.Reason)
	assert.Check(t, is.Contains(got.Reason, "alias.p"), "Judge(%q) reason", command)
}

func TestJudge_RefusesRewritingTheOriginRemote(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "remove", command: "git remote remove origin", wantNamed: "origin"},
		{name: "rm", command: "git remote rm origin", wantNamed: "origin"},
		{name: "rename", command: "git remote rename origin upstream", wantNamed: "origin"},
		{name: "set-url", command: "git remote set-url origin https://example.com/x.git", wantNamed: "origin"},
		{name: "set-url --push", command: "git remote set-url --push origin https://example.com/x.git", wantNamed: "origin"},
		{name: "set-head", command: "git remote set-head origin -a", wantNamed: "origin"},
		{name: "rename after -v", command: "git remote -v rename origin other", wantNamed: "origin"},
		{name: "set-url after --verbose", command: "git remote --verbose set-url origin https://example.com/x.git", wantNamed: "origin"},
		{name: "remove after -v", command: "git remote -v remove origin", wantNamed: "origin"},
	})
}

func TestJudge_AllowsARemoteCallThatLeavesOriginAsItIs(t *testing.T) {
	t.Parallel()

	checkAllowed(t, repositoryCaller(t), []keptWork{
		{name: "remove", command: "git remote remove upstream"},
		{name: "rename", command: "git remote rename upstream mirror"},
		{name: "set-url", command: "git remote set-url upstream https://example.com/x.git"},
		{name: "set-head", command: "git remote set-head upstream -a"},
		{name: "add", command: "git remote add upstream https://example.com/x.git"},
		{name: "listing", command: "git remote -v"},
		{name: "show after -v", command: "git remote -v show origin"},
	})
}

func TestJudge_RefusesANonLiteralArgumentAFlagCouldHideIn(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "rm", command: `git rm "$f"`, wantNamed: "git rm"},
		{name: "commit", command: `git commit "$opt" -m msg`, wantNamed: "git commit"},
		{name: "push", command: `git push origin "$flag"`, wantNamed: "git push"},
		{name: "clean", command: `git clean "$opt"`, wantNamed: "git clean"},
		{name: "branch", command: `git branch -d "$b"`, wantNamed: "git branch"},
		{name: "switch", command: `git switch "$b"`, wantNamed: "git switch"},
		{name: "add", command: `git add "$f"`, wantNamed: "git add"},
		{name: "stage", command: `git stage "$f"`, wantNamed: "git stage"},
		{name: "am", command: `git am "$opt" x.patch`, wantNamed: "git am"},
		{name: "merge", command: `git merge "$opt" main`, wantNamed: "git merge"},
		{name: "pull", command: `git pull "$opt"`, wantNamed: "git pull"},
		{name: "rebase", command: `git rebase "$opt" main`, wantNamed: "git rebase"},
		{name: "send-email", command: `git send-email "$opt" x.patch`, wantNamed: "git send-email"},
		{name: "gc", command: `git gc "$opt"`, wantNamed: "git gc"},
		{name: "apply", command: `git apply "$opt" patch.diff`, wantNamed: "git apply"},
		{name: "reflog", command: `git reflog "$sub"`, wantNamed: "git reflog"},
		{name: "remote", command: `git remote remove "$r"`, wantNamed: "git remote"},
		{name: "config", command: `git config "$k" x`, wantNamed: "git config"},
		{name: "long option holding a variable", command: `git restore "--st$x" f`, wantNamed: "git restore"},
		{name: "-c value", command: `git -c "$v" commit -m msg`, wantNamed: "git commit"},
		{name: "--config-env value", command: `git --config-env "$v" commit -m msg`, wantNamed: "git commit"},
	})
}

func TestJudge_WhenAWorkTreeReadFails_RefusesSayingWhatBroke(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := offTrunk(t)
	clean := addWorktree(f, filepath.Join(outsideDir(t), "clean"))
	env := append(f.Env(), gittest.FakeGit(t, gittest.Fake{FailOn: []string{"status"}})...)
	caller := guard.Caller{Home: home, Dir: f.Dir(), Git: git.Runner{Env: env}}
	tests := []keptWork{
		{name: "rebase", command: "git rebase main"},
		{name: "worktree remove", command: "git worktree remove " + quotedPath(clean)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := guard.Judge(t.Context(), tt.command, caller)

			assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome, reason %q", tt.command, got.Reason)
			assert.Check(t, is.Contains(got.Reason, gittest.FailedSaid), "Judge(%q) reason", tt.command)
		})
	}
}
