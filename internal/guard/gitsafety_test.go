//go:build unix

// cspell:ignore forc noval -- deliberately truncated or Perl-negated option spellings git accepts.

package guard_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

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
		{name: "switch -f", command: "git switch -f branch"},
		{name: "switch --discard-changes", command: "git switch --discard-changes branch"},
		{name: "clean -f", command: "git clean -f"},
		{name: "clean flags by brace expansion", command: "git clean -{f,d}"},
		{name: "clean -i", command: "git clean -i"},
		{name: "branch -D", command: "git branch -D feature"},
		{name: "branch --delete --force", command: "git branch --delete --force feature"},
		{name: "rm -f", command: "git rm -f file"},
		{name: "reflog expire", command: "git reflog expire"},
		{name: "gc pruning now", command: "git gc --prune=now"},
		{name: "apply -R", command: "git apply -R patch.diff"},
		{name: "checkout-index bundle holding f", command: "git checkout-index -af"},
		{name: "read-tree --reset beside --index-output", command: "git read-tree --index-output=x.idx --reset HEAD"},
		{name: "read-tree -u -m beside --index-output", command: "git read-tree --index-output=x.idx -u -m HEAD"},
		{name: "read-tree of a tree", command: "git read-tree HEAD"},
		{name: "update-ref -d", command: "git update-ref -d refs/heads/keep"},
		{name: "update-ref --stdin", command: "git update-ref --stdin"},
		{name: "update-ref to the all-zero SHA-1 id", command: "git update-ref refs/heads/keep " + strings.Repeat("0", 40)},
		{name: "update-ref to the all-zero SHA-256 id", command: "git update-ref refs/heads/keep " + strings.Repeat("0", 64)},
		{name: "update-ref to a non-literal value", command: `git update-ref refs/heads/keep "$sha"`},
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
		{name: "-M onto a non-literal name", command: `git branch -M topic "$b"`},
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
		{name: "checkout-index without force", command: "git checkout-index -a"},
		{name: "update-ref setting a ref", command: "git update-ref refs/heads/new HEAD"},
		{name: "branch -M onto a new name", command: "git branch -M topic other"},
		{name: "read-tree --dry-run", command: "git read-tree --dry-run HEAD"},
		{name: "read-tree --index-output", command: "git read-tree --index-output=other.idx -m HEAD"},
		{name: "read-tree --prefix", command: "git read-tree --prefix=vendor/lib/ HEAD"},
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
		{name: "-C naming the repository", dir: kept.Dir(), command: "git -C " + quotedPath(lifted.Dir()) + " clean -d"},
	}, expectation{outcome: guard.Refuse})
}

func TestJudge_WhenTheCleanForceSettingCannotBeRead_RefusesNamingTheRead(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f, _ := standingOn(t, "main", "topic")
	const command = "git clean -d"

	got := guard.Judge(t.Context(), command, callerFailingOn(t, f, f.Dir(), "config"))

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Contains(got.Reason, gittest.FailedSaid), "Judge(%q) reason", command)
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
		{name: "prefix of send-email --novalidate", command: "git send-email --noval x.patch", wantNamed: "--noval"},
		{name: "-c in another case", command: "git -c Core.HooksPATH=x commit -m msg"},
		{name: "--config-env with a separate value", command: "git --config-env core.hooksPath=X commit -m msg"},
	})
}

func TestJudge_RefusesWritingGitHistoryOverAFile(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "show truncating", command: "git show HEAD > output.txt", wantNamed: "git show"},
	})
}

// namedCommand is a command, with the name of the test case that runs it.
type namedCommand struct {
	name    string
	command string
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
		{name: "rebase", command: "git rebase main"},
	})
}

// checkAllowed fails unless Judge, run by caller, allows every command.
func checkAllowed(t *testing.T, caller guard.Caller, tests []namedCommand) {
	t.Helper()

	rows := make([]judged, 0, len(tests))
	for _, tt := range tests {
		rows = append(rows, judged{name: tt.name, command: tt.command, want: guard.Allow})
	}

	checkJudged(t, caller, rows)
}

// placed is a command run from a directory.
type placed struct {
	name    string
	dir     string
	command string
}

// expectation is the outcome Judge must give, with a reason holding named.
type expectation struct {
	named   string
	outcome guard.Outcome
}

// checkJudgedIn fails unless Judge, run by caller from each row's directory, gives the row's
// command the outcome want states.
func checkJudgedIn(t *testing.T, caller guard.Caller, tests []placed, want expectation) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := caller
			in.Dir = tt.dir

			checkJudgedRow(t, in, judged{command: tt.command, wantNamed: want.named, want: want.outcome})
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
		{name: "-C naming the dirty tree", dir: clean.Dir(), command: "git -C " + quotedPath(dirty.Dir()) + " rebase main"},
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
		{name: "-C naming a clean tree from a dirty one", dir: dirty.Dir(), command: "git -C " + quotedPath(clean.Dir()) + " rebase main"},
		{name: "outside any repository", dir: outsideDir(t), command: "git rebase main"},
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
	clean := quotedPath(addWorktree(t, f, filepath.Join(outsideDir(t), "clean")))

	checkJudgedIn(t, callerIn(f, f.Dir()), []placed{
		{name: "prefix of --force", dir: f.Dir(), command: "git worktree remove --forc " + clean},
	}, expectation{outcome: guard.Refuse})
}

func TestJudge_RefusesAWorktreeRemovalItCannotVerify(t *testing.T) {
	t.Parallel()

	w := newWorktrees(t)

	checkJudgedIn(t, w.caller, []placed{
		{name: "non-literal path", dir: w.repo, command: `git worktree remove "$wt"`},
		{name: "no path", dir: w.repo, command: "git worktree remove"},
		{name: "missing path", dir: w.repo, command: "git worktree remove " + quotedPath(filepath.Join(w.repo, "absent"))},
		{name: "relative path absent under -C though present where it runs", dir: w.heldClean, command: "git -C " + quotedPath(w.repo) + " worktree remove wt1"},
	}, expectation{outcome: guard.Refuse, named: "unverifiable"})
}

func TestJudge_RefusesRemovingADirtyWorktree(t *testing.T) {
	t.Parallel()

	w := newWorktrees(t)
	heldClean := filepath.Dir(w.heldClean)
	heldCleanName := quotedPath(filepath.Base(w.heldClean))

	checkJudgedIn(t, w.caller, []placed{
		{name: "changed tracked file", dir: w.repo, command: "git worktree remove " + quotedPath(w.dirty)},
		{
			name:    "relative path under an absolute -C after a relative one",
			dir:     heldClean,
			command: "git -C " + heldCleanName + " -C " + quotedPath(w.held) + " worktree remove wt1",
		},
	}, expectation{outcome: guard.Refuse, named: "dirty"})
}

func TestJudge_AllowsAWorktreeCommandThatKeepsWork(t *testing.T) {
	t.Parallel()

	w := newWorktrees(t)

	checkJudgedIn(t, w.caller, []placed{
		{name: "absolute path", dir: w.repo, command: "git worktree remove " + quotedPath(w.clean)},
		{name: "relative path under -C from a tree holding a dirty one", dir: w.held, command: "git -C " + quotedPath(w.heldClean) + " worktree remove wt1"},
		{name: "list", dir: w.repo, command: "git worktree list"},
	}, expectation{outcome: guard.Allow})
}

func TestJudge_RefusesAGitConfigWriteOfAKeyThatChangesWhatGitRuns(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "-f", command: "git config -f other.cfg alias.p push", wantNamed: "alias.p"},
		{name: "--type with an attached value", command: "git config --type=path include.path x", wantNamed: "include.path"},
		{name: "section in another case", command: "git config ALIAS.p push", wantNamed: "ALIAS.p"},
		{name: "variable in another case", command: "git config core.HOOKSPath x", wantNamed: "core.HOOKSPath"},
		{name: "--add", command: "git config --add include.path x", wantNamed: "include.path"},
		{name: "set", command: "git config set core.hooksPath x", wantNamed: "core.hooksPath"},
		{name: "--rename-section into a guarded section", command: "git config --rename-section team alias", wantNamed: "alias"},
		{name: "prefix of --remove-section", command: "git config --remove alias", wantNamed: "alias"},
		{name: "prefix of --type with a separate value", command: "git config --ty path include.path /x", wantNamed: "include.path"},
		{name: "credential helper of a URL given as a path", command: "git config credential.https://h.helper /bin/x", wantNamed: "credential.https://h.helper"},
		{name: "credential helper key in another case", command: "git config credential.HELPER '!x'", wantNamed: "credential.HELPER"},
		{name: "credential helper with ;", command: "git config credential.helper 'cache; touch x'", wantNamed: "credential.helper"},
		{name: "mergetool command in another case", command: "git config MergeTool.x.CMD 'git push'", wantNamed: "MergeTool.x.CMD"},
		{name: "difftool path", command: "git config difftool.x.path /bin/x", wantNamed: "difftool.x.path"},
		{name: "mergetool path in another case", command: "git config MergeTool.x.PATH /bin/x", wantNamed: "MergeTool.x.PATH"},
	})
}

func TestJudge_RefusesAGitConfigWriteOfASectionThatHoldsAGuardedKey(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "rename into sequence", command: "git config rename-section team sequence", wantNamed: "sequence"},
		{name: "rename into core in another case", command: "git config --rename-section team CORE", wantNamed: "CORE"},
		{name: "rename into the credentials of a URL", command: "git config --rename-section team credential.https://h", wantNamed: "credential.https://h"},
		{name: "rename into a difftool", command: "git config --rename-section team difftool.x", wantNamed: "difftool.x"},
	})
}

func TestJudge_AllowsAGitConfigCallThatLeavesGuardedKeys(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "--get", command: "git config --get alias.p"},
		{name: "get", command: "git config get remote.origin.url"},
		{name: "key alone", command: "git config alias.p"},
		{name: "merge driver name", command: "git config merge.x.name x"},
		{name: "removal of a diff driver", command: "git config --remove-section diff.x"},
		{name: "credential helper with plain arguments", command: "git config credential.helper 'cache --timeout 3600'"},
		{name: "--unset of a credential helper whatever its value-pattern", command: "git config --unset credential.helper 'a|b'"},
		{name: "--unset-all of a credential helper whatever its value-pattern", command: "git config --unset-all credential.helper '!.*'"},
	})
}

func TestJudge_RefusesOpeningTheGitConfigEditor(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "--edit", command: "git config --edit"},
		{name: "edit", command: "git config edit"},
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
		{name: "commit", command: `git commit "$opt" -m msg`, wantNamed: "git commit"},
		{name: "reflog", command: `git reflog "$sub"`, wantNamed: "git reflog"},
		{name: "remote", command: `git remote remove "$r"`, wantNamed: "git remote"},
		{name: "config", command: `git config "$k" x`, wantNamed: "git config"},
		{name: "long option holding a variable", command: `git restore "--st$x" f`, wantNamed: "git restore"},
		{name: "-c value", command: `git -c "$v" commit -m msg`, wantNamed: "git commit"},
		{name: "-c value with no subcommand", command: `git -c "$k"`, wantNamed: "git:"},
		{name: "--config-env value", command: `git --config-env "$k" status`, wantNamed: "git status"},
		{name: "--config-env value with no subcommand", command: `git --config-env "$k"`, wantNamed: "git:"},
	})
}

func TestJudge_WhenAWorkTreeReadFails_RefusesSayingWhatBroke(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f, _ := standingOn(t, "main", "topic")
	clean := addWorktree(t, f, filepath.Join(outsideDir(t), "clean"))
	caller := callerFailingOn(t, f, f.Dir(), "status")

	checkJudgedInTurn(t, caller, []judged{
		{name: "rebase", command: "git rebase main", want: guard.Refuse, wantNamed: gittest.FailedSaid},
		{name: "worktree remove", command: "git worktree remove " + quotedPath(clean), want: guard.Refuse, wantNamed: gittest.FailedSaid},
	})
}
