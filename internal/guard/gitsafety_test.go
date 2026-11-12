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

// linkTo returns a symbolic link to target, made in a new directory that holds no repository.
func linkTo(t *testing.T, target string) string {
	t.Helper()

	link := filepath.Join(outsideDir(t), "link")
	assert.NilError(t, os.Symlink(target, link))

	return link
}

// subdirectory makes the directory sub inside dir, and returns its path.
func subdirectory(t *testing.T, dir string) string {
	t.Helper()

	sub := filepath.Join(dir, "sub")
	assert.NilError(t, os.MkdirAll(sub, 0o750))

	return sub
}

func TestJudge_RefusesAGitOperationThatDiscardsWork(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "switch -f", command: "git switch -f branch"},
		{name: "switch --discard-changes", command: "git switch --discard-changes branch"},
		{name: "clean -f", command: "git clean -f"},
		{name: "clean -i", command: "git clean -i"},
		{name: "clean -nf with the dry run turned off last", command: "git clean -nf --no-dry-run"},
		{name: "clean -n with interactive set last", command: "git clean -n -i"},
		{name: "branch -D", command: "git branch -D feature"},
		{name: "branch --delete --force", command: "git branch --delete --force feature"},
		{name: "reflog expire", command: "git reflog expire"},
		{name: "gc pruning now", command: "git gc --prune=now"},
		{name: "repack -a -d", command: "git repack -a -d"},
		{name: "repack -a -d with --cruft turned off again", command: "git repack -a -d --cruft --no-cruft"},
		{
			name:    "repack -a -d with --keep-unreachable turned off again",
			command: "git repack -a -d --keep-unreachable --no-keep-unreachable",
		},
		{
			name:      "repack -A -d expiring the objects it loosens",
			command:   "git repack -A -d --unpack-unreachable=now",
			wantNamed: "--unpack-unreachable",
		},
		{
			name:      "repack --cruft -d expiring the cruft",
			command:   "git repack --cruft -d --cruft-expiration=2.weeks.ago",
			wantNamed: "--cruft-expiration",
		},
		{name: "apply -R", command: "git apply -R patch.diff"},
		{name: "checkout-index bundle holding f", command: "git checkout-index -af"},
		{name: "read-tree --reset beside --index-output", command: "git read-tree --index-output=x.idx --reset HEAD"},
		{name: "read-tree -u -m beside --index-output", command: "git read-tree --index-output=x.idx -u -m HEAD"},
		{name: "read-tree of a tree", command: "git read-tree HEAD"},
		{name: "update-ref -d", command: "git update-ref -d refs/heads/keep"},
		{name: "update-ref --stdin", command: "git update-ref --stdin"},
		{name: "update-ref to the all-zero SHA-1 id", command: "git update-ref refs/heads/keep " + strings.Repeat("0", 40)},
		{name: "update-ref to the all-zero SHA-256 id", command: "git update-ref refs/heads/keep " + strings.Repeat("0", 64)},
		{name: "update-ref to a non-literal value", command: `git update-ref -- refs/heads/keep "$sha"`},
	})
}

func TestJudge_RefusesAConfigOverrideThatLetsGitDiscardWork(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "--config-env setting the clean force requirement", command: "git --config-env clean.requireForce=V clean -d"},
		{name: "-c lifting it under a dry run a later --no-dry-run undoes", command: "git -c clean.requireForce=false clean -n --no-dry-run -d"},
		{name: "--config-env setting the gc prune expiry", command: "git --config-env=gc.pruneExpire=V gc", wantNamed: "--config-env"},
	})
}

func TestJudge_RefusesABranchRenameOverAnExistingBranch(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "--move --force", command: "git branch --move --force topic main"},
		{name: "-M onto a non-literal name", command: `git branch -M topic -- "$b"`, wantNamed: "new name"},
		{name: "-M onto a previous branch git cannot resolve", command: "git branch -M topic @{-5}"},
	})
}

func TestJudge_RefusesABranchCopyOverAnExistingBranch(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "-C copying the current branch", command: "git branch -C main", wantNamed: "main"},
		{name: "--copy --force", command: "git branch --copy --force topic main", wantNamed: "main"},
	})
}

func TestJudge_RefusesABranchRenameOntoAPackedBranchInAnotherCase(t *testing.T) {
	t.Parallel()
	f, caller := standingOn(t, "main", "topic")
	f.Git("pack-refs", "--all")

	checkJudged(t, caller, []judged{
		{name: "-M renaming the current branch", command: "git branch -M MAIN", wantNamed: "MAIN", want: guard.Refuse},
	})
}

func TestJudge_AllowsAGitOperationThatLeavesWorkOnDisk(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "clean under -c keeping the force requirement", command: "git -c clean.requireForce=true clean -d"},
		{name: "clean --no-dry-run then -n under -c lifting the force requirement", command: "git -c clean.requireForce=false clean --no-dry-run -n -d"},
		{name: "clean bundle holding a dry run beside force", command: "git clean -nfd"},
		{name: "clean --dry-run after -f -d", command: "git clean -f -d --dry-run"},
		{name: "checkout-index without force", command: "git checkout-index -a"},
		{name: "update-ref setting a ref", command: "git update-ref refs/heads/new HEAD"},
		{name: "branch -M onto a new name", command: "git branch -M topic other"},
		{name: "read-tree --dry-run", command: "git read-tree --dry-run HEAD"},
		{name: "read-tree --index-output", command: "git read-tree --index-output=other.idx -m HEAD"},
		{name: "read-tree --prefix", command: "git read-tree --prefix=vendor/lib/ HEAD"},
		{name: "repack -a", command: "git repack -a"},
		{name: "repack -d", command: "git repack -d"},
		{name: "repack -a -A -d", command: "git repack -a -A -d"},
		{name: "repack -a -d --cruft", command: "git repack -a -d --cruft"},
		{name: "repack -a -d --keep-unreachable", command: "git repack -a -d --keep-unreachable"},
		{name: "repack -d with an expiry but no -A", command: "git repack -d --unpack-unreachable=now"},
		{name: "repack -d with a cruft expiry but no --cruft", command: "git repack -d --cruft-expiration=now"},
		{
			name:    "repack -A -d with the expiry turned off again",
			command: "git repack -A -d --unpack-unreachable=now --no-unpack-unreachable",
		},
	})
}

// liftingForce returns a repository whose local config lifts the force requirement of git clean.
func liftingForce(t *testing.T) *gittest.Fixture {
	t.Helper()

	f := gittest.New(t, "main")
	f.Git("config", "--local", "clean.requireForce", "false")

	return f
}

func TestJudge_RefusesACleanWhereTheRepositoryLiftsTheForceRequirement(t *testing.T) {
	t.Parallel()

	lifted := liftingForce(t)
	kept := gittest.New(t, "main")

	checkJudgedIn(t, callerIn(lifted, lifted.Dir()), []placed{
		{name: "in the repository", dir: lifted.Dir(), command: "git clean -d"},
		{name: "-C naming the repository", dir: kept.Dir(), command: "git -C " + gittest.ShellWord(lifted.Dir()) + " clean -d"},
	}, expectation{outcome: guard.Refuse})
}

func TestJudge_WhenTheCleanForceSettingCannotBeRead_RefusesNamingTheRead(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f, _ := standingOn(t, "main", "topic")
	const command = "git clean -d"

	checkJudgedRow(t, callerFailingOn(t, f, f.Dir(), "config"), judged{command: command, want: guard.Refuse, wantNamed: gittest.FailedSaid})
}

func TestJudge_RefusesARestoreThatDiscardsWork(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "restore of a path", command: "git restore file"},
		{name: "restore --staged --worktree", command: "git restore --staged --worktree file"},
	})
}

func TestJudge_RefusesAForcedStageLikeAForcedAdd(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "stage -f", command: "git stage -f x", wantNamed: "git stage"},
		{name: "stage --pathspec-from-file taking --", command: "git stage --pathspec-from-file -- -f", wantNamed: "git stage"},
	})
}

func TestJudge_RefusesSkippingGitsCommitChecks(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "commit --no-verify", command: "git commit --no-verify -m 'msg'", wantNamed: "--no-verify"},
		{name: "commit -n", command: "git commit -n -m 'msg'", wantNamed: "git commit"},
		{name: "difftool --no-verify", command: "git difftool --no-verify HEAD", wantNamed: "--no-verify"},
		{name: "prefix of send-email --novalidate", command: "git send-email --noval x.patch", wantNamed: "--noval"},
		{name: "-c in another case", command: "git -c Core.HooksPATH=x commit -m msg"},
		{name: "--config-env with a separate value", command: "git --config-env core.hooksPath=X commit -m msg"},
	})
}

func TestJudge_RefusesWritingGitHistoryOverAFile(t *testing.T) {
	t.Parallel()

	outside := gittest.ShellWord(filepath.Join(outsideDir(t), "x.md"))

	checkRefusals(t, []refusal{
		{name: "show truncating", command: "git show HEAD > output.txt", wantNamed: "git show"},
		{name: "show into a directory not yet made", command: "git show HEAD:README.md > absent/x.md", wantNamed: "git show"},
		{name: "show piped into tee", command: "git show HEAD:README.md | tee README.md", wantNamed: "git show"},
		{name: "cat-file piped into tee -a", command: "git cat-file -p HEAD:README.md | tee -a README.md", wantNamed: "git cat-file"},
		{name: "tee whose second file lands in the work tree", command: "git show HEAD:README.md | tee " + outside + " README.md", wantNamed: "git show"},
		{name: "relative target where the directory is unknown", command: `cd "$d" && git show HEAD:README.md > x.md`, wantNamed: "x.md from"},
		{name: "tee operand after --", command: "git show HEAD:README.md | tee -- -x", wantNamed: "git show"},
		{name: "tee operand -", command: "git show HEAD:README.md | tee -", wantNamed: "git show"},
		{
			name:      "tee named by a variable the command assigned",
			command:   "T=tee; git show HEAD:README.md | $T README.md",
			wantNamed: "git show",
		},
	})
}

func TestJudge_RefusesGitHistoryWrittenWhereTheGuardCannotRead(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "non-literal redirect target", command: `git show HEAD:README.md > "$out"`, wantNamed: "git show"},
		{name: "non-literal tee operand", command: `git show HEAD:README.md | tee "$out"`, wantNamed: "git show"},
	})
}

func TestJudge_AllowsGitHistoryWrittenOutsideAnyWorkTree(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")
	away := outsideDir(t)
	outside := gittest.ShellWord(filepath.Join(away, "x.md"))

	checkAllowed(t, caller, []namedCommand{
		{name: "show into a file outside", command: "git show HEAD:README.md > " + outside},
		{name: "show piped into tee of a file outside", command: "git show HEAD:README.md | tee -a " + outside},
		{name: "show piped into tee naming no file", command: "git show HEAD:README.md | tee"},
		{name: "tee of a file outside named by a variable", command: "F=" + outside + "; git show HEAD:README.md | tee $F"},
		{name: "relative target after a cd outside", command: "cd " + gittest.ShellWord(away) + " && git show HEAD:README.md > x.md"},
		{name: "tee further down the pipe", command: "git show HEAD:README.md | cat | tee README.md"},
	})
}

func TestJudge_WhenGitHistoryIsWrittenUpFromALinkIntoAWorkTree_RefusesNamingTheFile(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")
	link := linkTo(t, subdirectory(t, f.Dir()))
	top, err := filepath.EvalSymlinks(f.Dir())
	assert.NilError(t, err)

	checkJudgedRow(t, caller, judged{command: "cd " + gittest.ShellWord(link) + " && git show HEAD:README.md > ../x.md", want: guard.Refuse, wantNamed: "writes over " + filepath.Join(top, "x.md")})
}

func TestJudge_AllowsGitHistoryRedirectedAroundACdFromOutsideAnyWorkTree(t *testing.T) {
	t.Parallel()

	f, _ := standingOn(t, "main", "topic")
	caller := callerIn(f, filepath.Dir(f.Dir()))

	checkAllowed(t, caller, []namedCommand{
		{name: "subshell entering the repository", command: "(cd repo && git show HEAD:go.mod) > old.mod"},
	})
}

func TestJudge_RefusesGitHistoryRedirectedAroundACdOntoAWorkTreeFile(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")
	parent := filepath.Dir(f.Dir())
	away := gittest.ShellWord(outsideDir(t))

	checkJudgedIn(t, caller, []placed{
		{name: "subshell redirected into the repository it enters", dir: parent, command: "(cd repo && git show HEAD:go.mod) > repo/go.mod"},
		{name: "subshell leaving the work tree it is redirected in", command: "(cd " + away + " && git -C " + gittest.ShellWord(f.Dir()) + " show HEAD:go.mod) > go.mod"},
		{name: "redirect on git show inside the subshell", dir: parent, command: "(cd repo && git show HEAD:go.mod > go.mod)"},
	}, expectation{outcome: guard.Refuse, named: "writes over " + filepath.Join(f.Dir(), "go.mod")})
}

func TestJudge_WhenWhetherTheTargetIsInAWorkTreeCannotBeRead_RefusesNamingTheRead(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f, _ := standingOn(t, "main", "topic")
	command := "git show HEAD:README.md > " + gittest.ShellWord(filepath.Join(outsideDir(t), "x.md"))

	checkJudgedRow(t, callerFailingOn(t, f, f.Dir(), "rev-parse", "--show-toplevel"), judged{command: command, want: guard.Refuse, wantNamed: gittest.FailedSaid})
}

func TestJudge_AllowsAGitOperationThatKeepsWork(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "plain switch", command: "git switch branch"},
		{name: "branch -d", command: "git branch -d feature"},
		{name: "restore -S", command: "git restore -S file"},
		{name: "rm of a path spelled like a flag", command: "git rm -- -f"},
		{name: "-c of another key", command: "git -c core.pager=cat commit -m msg"},
		{name: "plain gc", command: "git gc"},
		{name: "show writing to the terminal", command: "git show HEAD:f"},
		{name: "plain apply", command: "git apply patch.diff"},
		{name: "send-email --validate", command: "git send-email --validate x.patch"},
		{name: "restore --staged after an attached --pathspec-from-file", command: "git restore --pathspec-from-file=x --staged f"},
	})
}

func TestJudge_RefusesEveryStashButAListOrAShow(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "bare stash", command: "git stash"},
		{name: "push", command: "git stash push -m msg"},
		{name: "subcommand the guard cannot read", command: `git stash "$x"`},
	})
}

// changeTracked changes a file f tracks in the work tree dir, and returns dir.
func changeTracked(t *testing.T, f *gittest.Fixture, dir string) string {
	t.Helper()

	tracked, _, _ := strings.Cut(f.Git("-C", dir, "ls-files"), "\n")
	gittest.WriteFile(t, dir, tracked, "changed\n")

	return dir
}

// addWorktree adds a linked worktree of f at dir on a new branch named for it, and returns dir.
func addWorktree(t *testing.T, f *gittest.Fixture, dir string) string {
	t.Helper()

	f.Git("worktree", "add", "--quiet", "-b", filepath.Base(dir), dir)

	return dir
}

func TestJudge_RefusesARebaseOverUncommittedWork(t *testing.T) {
	t.Parallel()

	dirty, caller := standingOn(t, "main", "topic")
	dirty.DirtyFile()
	clean, _ := standingOn(t, "main", "topic")

	checkJudgedIn(t, caller, []placed{
		{name: "in the dirty tree", dir: dirty.Dir(), command: "git rebase main"},
		{name: "-C naming the dirty tree", dir: clean.Dir(), command: "git -C " + gittest.ShellWord(dirty.Dir()) + " rebase main"},
	}, expectation{outcome: guard.Refuse, named: "dirty"})
}

func TestJudge_RefusesARebaseWhoseTargetIsUnknown(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "main")

	checkJudgedIn(t, caller, []placed{
		{name: "non-literal -C", dir: f.Dir(), command: `git -C "$d" rebase main`},
	}, expectation{outcome: guard.Refuse, named: "which repository"})
}

func TestJudge_AllowsARebaseThatCannotLoseWork(t *testing.T) {
	t.Parallel()

	dirty, caller := standingOn(t, "main", "topic")
	dirty.DirtyFile()
	clean, _ := standingOn(t, "main", "topic")

	checkJudgedIn(t, caller, []placed{
		{name: "-C naming a clean tree from a dirty one", dir: dirty.Dir(), command: "git -C " + gittest.ShellWord(clean.Dir()) + " rebase main"},
		{name: "outside any repository", dir: outsideDir(t), command: "git rebase main"},
		{name: "--continue in a dirty tree", dir: dirty.Dir(), command: "git rebase --continue"},
	}, expectation{outcome: guard.Allow})
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
	changeTracked(t, held, addWorktree(t, held, filepath.Join(held.Dir(), "wt1")))
	heldClean := gittest.New(t, "main")
	addWorktree(t, heldClean, filepath.Join(heldClean.Dir(), "wt1"))

	return worktrees{
		caller:    callerIn(f, f.Dir()),
		repo:      f.Dir(),
		clean:     addWorktree(t, f, filepath.Join(outside, "clean")),
		dirty:     changeTracked(t, f, addWorktree(t, f, filepath.Join(outside, "dirty"))),
		held:      held.Dir(),
		heldClean: heldClean.Dir(),
	}
}

func TestJudge_RefusesAForcedWorktreeRemovalWhateverThePath(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	clean := gittest.ShellWord(addWorktree(t, f, filepath.Join(outsideDir(t), "clean")))

	checkJudgedIn(t, callerIn(f, f.Dir()), []placed{
		{name: "prefix of --force", dir: f.Dir(), command: "git worktree remove --forc " + clean},
	}, expectation{outcome: guard.Refuse})
}

func TestJudge_RefusesAWorktreeRemovalItCannotVerify(t *testing.T) {
	t.Parallel()

	w := newWorktrees(t)

	checkJudgedIn(t, w.caller, []placed{
		{name: "non-literal path after --", dir: w.repo, command: `git worktree remove -- "$wt"`},
		{name: "no path", dir: w.repo, command: "git worktree remove"},
		{name: "two paths", dir: w.repo, command: "git worktree remove " + gittest.ShellWord(w.clean) + " " + gittest.ShellWord(w.dirty)},
		{name: "relative path where the directory is unknown", dir: w.repo, command: `cd "$d" && git worktree remove wt1`},
		{name: "missing path", dir: w.repo, command: "git worktree remove " + gittest.ShellWord(filepath.Join(w.repo, "absent"))},
		{name: "relative path absent under -C though present where it runs", dir: w.heldClean, command: "git -C " + gittest.ShellWord(w.repo) + " worktree remove wt1"},
	}, expectation{outcome: guard.Refuse, named: "unverifiable"})
}

func TestJudge_WhenAWorktreeRemovalHoldsAnUnreadableWordWhereAnOptionCanStand_RefusesNamingASpellingTheGuardAllows(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	clean := gittest.ShellWord(addWorktree(t, f, filepath.Join(outsideDir(t), "clean")))

	checkJudgedIn(t, callerIn(f, f.Dir()), []placed{
		{name: "before the path", dir: f.Dir(), command: `git worktree remove "$opt" ` + clean},
	}, expectation{outcome: guard.Refuse, named: "write the argument as a literal"})
}

func TestJudge_RefusesRemovingADirtyWorktree(t *testing.T) {
	t.Parallel()

	w := newWorktrees(t)
	heldClean := filepath.Dir(w.heldClean)
	heldCleanName := gittest.ShellWord(filepath.Base(w.heldClean))
	intoHeld := linkTo(t, subdirectory(t, w.held))

	checkJudgedIn(t, w.caller, []placed{
		{name: "changed tracked file", dir: w.repo, command: "git worktree remove " + gittest.ShellWord(w.dirty)},
		{name: "relative path up from a link into a tree holding a dirty one", dir: w.repo, command: "cd " + gittest.ShellWord(intoHeld) + " && git worktree remove ../wt1"},
		{
			name:    "relative path under an absolute -C after a relative one",
			dir:     heldClean,
			command: "git -C " + heldCleanName + " -C " + gittest.ShellWord(w.held) + " worktree remove wt1",
		},
	}, expectation{outcome: guard.Refuse, named: "dirty"})
}

func TestJudge_AllowsAWorktreeCommandThatKeepsWork(t *testing.T) {
	t.Parallel()

	w := newWorktrees(t)

	checkJudgedIn(t, w.caller, []placed{
		{name: "absolute path", dir: w.repo, command: "git worktree remove " + gittest.ShellWord(w.clean)},
		{name: "relative path under -C from a tree holding a dirty one", dir: w.held, command: "git -C " + gittest.ShellWord(w.heldClean) + " worktree remove wt1"},
		{name: "list", dir: w.repo, command: "git worktree list"},
	}, expectation{outcome: guard.Allow})
}

func TestJudge_RefusesAGitConfigWriteOfTheHooksPathOrARemote(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "-f", command: "git config -f other.cfg remote.origin.url x", wantNamed: "remote.origin.url"},
		{name: "--type with an attached value", command: "git config --type=path core.hooksPath x", wantNamed: "core.hooksPath"},
		{name: "section in another case", command: "git config URL.x.insteadOf y", wantNamed: "URL.x.insteadOf"},
		{name: "variable in another case", command: "git config core.HOOKSPath x", wantNamed: "core.HOOKSPath"},
		{name: "set", command: "git config set core.hooksPath x", wantNamed: "core.hooksPath"},
		{name: "prefix of --remove-section", command: "git config --remove remote.origin", wantNamed: "remote.origin"},
		{name: "prefix of --type with a separate value", command: "git config --ty path core.hooksPath /x", wantNamed: "core.hooksPath"},
		{name: "rename into core in another case", command: "git config --rename-section team CORE", wantNamed: "CORE"},
		{name: "--unset", command: "git config --unset core.hooksPath", wantNamed: "core.hooksPath"},
		{name: "--unset-all", command: "git config --unset-all core.hooksPath", wantNamed: "core.hooksPath"},
	})
}

func TestJudge_AllowsAGitConfigCallThatLeavesTheHooksPathAndRemotes(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "--get-all with a value-pattern", command: "git config --get-all remote.origin.url x"},
		{name: "--edit given two words", command: "git config --edit core.hooksPath x"},
		{name: "core.editor", command: "git config core.editor vim"},
	})
}

func TestJudge_RefusesRewritingTheOriginRemote(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "rename after -v", command: "git remote -v rename origin other"},
		{name: "set-url after --verbose", command: "git remote --verbose set-url origin https://example.com/x.git"},
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
		{name: "reflog", command: `git reflog "$sub"`, wantNamed: "git reflog"},
		{name: "remote", command: `git remote remove "$r"`, wantNamed: "git remote"},
		{name: "config", command: `git config "$k" x`, wantNamed: "git config"},
		{name: "long option holding a variable", command: `git restore "--st$x" f`, wantNamed: "git restore"},
		{name: "-c value", command: `git -c "$v" commit -m msg`, wantNamed: "git commit"},
		{name: "-c value with no subcommand", command: `git -c "$k"`, wantNamed: "git:"},
		{name: "--config-env value", command: `git --config-env "$k" status`, wantNamed: "git status"},
	})
}

func TestJudge_WhenAWorkTreeReadFails_RefusesSayingWhatBroke(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f, _ := standingOn(t, "main", "topic")
	clean := addWorktree(t, f, filepath.Join(outsideDir(t), "clean"))
	caller := callerFailingOn(t, f, f.Dir(), "status")

	checkJudgedInTurn(t, caller, []judged{
		{name: "rebase", command: "git rebase main", want: guard.Refuse, wantNamed: gittest.FailedSaid},
		{name: "worktree remove", command: "git worktree remove " + gittest.ShellWord(clean), want: guard.Refuse, wantNamed: gittest.FailedSaid},
	})
}
