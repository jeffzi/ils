package guard_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
)

// commitCommand is a command the trunk rule judges.
const commitCommand = "git commit -m 'msg'"

// standingOn returns a new repository whose trunk is trunk, with HEAD on branch, and a caller
// working in it.
func standingOn(t *testing.T, trunk, branch string) (*gittest.Fixture, guard.Caller) {
	t.Helper()

	f := gittest.New(t, trunk)
	if branch != trunk {
		f.Commits(branch)
	}

	return f, callerIn(f, f.Dir())
}

// callerIn returns a caller working in dir whose git runs with the environment of f.
func callerIn(f *gittest.Fixture, dir string) guard.Caller {
	return guard.Caller{Home: home, Dir: dir, Git: git.Runner{Env: f.Env()}}
}

// cacheTrunk records branch as origin's default branch in the clone, as a clone or a plain fetch
// would, without asking origin.
func cacheTrunk(f *gittest.Fixture, branch string) {
	f.Git("remote", "set-head", "origin", branch)
}

// cutOff points the origin of f at a location that holds no repository, so it never answers.
func cutOff(f *gittest.Fixture) {
	f.SetOriginURL(filepath.ToSlash(f.MissingOriginDir()))
}

// moveOriginTrunk makes origin's default branch a new branch named branch, cut from from.
func moveOriginTrunk(f *gittest.Fixture, branch, from string) {
	f.OriginGit("branch", branch, from)
	f.OriginGit("symbolic-ref", "HEAD", "refs/heads/"+branch)
}

// lsRemotes returns how many of the calls recorder holds ran git ls-remote.
func lsRemotes(recorder *git.Recorder) int {
	count := 0
	for _, call := range recorder.Calls() {
		if slices.Contains(call, "ls-remote") {
			count++
		}
	}

	return count
}

// commitCreating returns a call of every subcommand the trunk rule refuses on the trunk.
func commitCreating() []keptWork {
	return []keptWork{
		{name: "commit", command: commitCommand},
		{name: "merge", command: "git merge plan/x"},
		{name: "cherry-pick", command: "git cherry-pick abc1234"},
		{name: "revert", command: "git revert abc1234"},
		{name: "am", command: "git am patch.mbox"},
		{name: "rebase", command: "git rebase origin/main"},
		{name: "pull", command: "git pull"},
		{name: "pull --rebase", command: "git pull --rebase"},
	}
}

func TestJudge_RefusesEveryPush(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "plain push", command: "git push", wantNamed: "git push"},
		{name: "push of a branch", command: "git push origin topic", wantNamed: "git push"},
		{name: "push setting the upstream", command: "git push -u origin HEAD", wantNamed: "git push"},
		{name: "send-pack", command: "git send-pack ../elsewhere.git main", wantNamed: "git send-pack"},
		{name: "http-push", command: "git http-push https://example.com/repo.git main", wantNamed: "git http-push"},
	})
}

func TestJudge_RefusesAPushWhereverItRuns(t *testing.T) {
	t.Parallel()

	_, onTrunk := standingOn(t, "main", "main")
	noOrigin, _ := standingOn(t, "main", "main")
	noOrigin.RemoveOrigin()

	checkRefusedIn(t, onTrunk, []placed{
		{name: "on the trunk", dir: onTrunk.Dir, command: "git push"},
		{name: "in a repository with no origin", dir: noOrigin.Dir(), command: "git push"},
		{name: "outside any repository", dir: outsideDir(t), command: "git push"},
	}, "git push")
}

func TestJudge_RefusesACommitCreatingSubcommandOnTheTrunk(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "main")
	for _, tt := range commitCreating() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := guard.Judge(t.Context(), tt.command, caller)

			assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome, reason %q", tt.command, got.Reason)
			assert.Check(t, is.Contains(got.Reason, "ils branch"), "Judge(%q) reason", tt.command)
		})
	}
}

func TestJudge_AllowsAFastForwardPullAndReadsOnTheTrunk(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "main")

	checkAllowed(t, caller, []keptWork{
		{name: "pull --ff-only", command: "git pull --ff-only"},
		{name: "fetch", command: "git fetch origin"},
		{name: "switch creating a branch", command: "git switch -c plan/x"},
		{name: "log", command: "git log --oneline -10"},
		{name: "status", command: "git status"},
	})
}

func TestJudge_AllowsACommitCreatingSubcommandOffTheTrunk(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "plan/x")

	checkAllowed(t, caller, append(commitCreating(),
		keptWork{name: "rebase naming the current branch", command: "git rebase origin/main plan/x"},
	))
}

func TestJudge_AllowsACommitWhereNoTrunkIsProtected(t *testing.T) {
	t.Parallel()

	detached, caller := standingOn(t, "main", "main")
	detached.Detach()
	noOrigin, _ := standingOn(t, "main", "main")
	noOrigin.RemoveOrigin()

	checkAllowedIn(t, caller, []placed{
		{name: "detached HEAD", dir: detached.Dir(), command: commitCommand},
		{name: "repository with no origin", dir: noOrigin.Dir(), command: commitCommand},
		{name: "outside any repository", dir: outsideDir(t), command: commitCommand},
	})
}

func TestJudge_WhenTheTrunkIsNotMain_RefusesACommitOnTheTrunk(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "develop", "develop")

	checkRefusedIn(t, caller, []placed{
		{name: "on develop", dir: f.Dir(), command: commitCommand},
	}, "ils branch")
}

func TestJudge_WhenTheTrunkIsNotMain_AllowsACommitOnMain(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "develop", "main")

	checkAllowedIn(t, caller, []placed{
		{name: "on main", dir: f.Dir(), command: commitCommand},
	})
}

func TestJudge_RefusesACommitWhoseOwnTargetIsOnTheTrunk(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "main")
	feature := addWorktree(f, filepath.Join(outsideDir(t), "feature"))
	trunkDir := quotedPath(f.Dir())
	trunkGitDir := quotedPath(filepath.Join(f.Dir(), ".git"))

	checkRefusedIn(t, caller, []placed{
		{name: "-C naming the trunk checkout", dir: feature, command: "git -C " + trunkDir + " commit -m 'msg'"},
		{
			name:    "--git-dir and --work-tree naming the trunk checkout",
			dir:     feature,
			command: "git --git-dir=" + trunkGitDir + " --work-tree=" + trunkDir + " commit -m 'msg'",
		},
	}, "ils branch")
}

func TestJudge_AllowsACommitWhoseOwnTargetIsOffTheTrunk(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "main")
	feature := addWorktree(f, filepath.Join(outsideDir(t), "feature"))

	checkAllowedIn(t, caller, []placed{
		{name: "-C naming a feature worktree", dir: f.Dir(), command: "git -C " + quotedPath(feature) + " commit -m 'msg'"},
	})
}

func TestJudge_RefusesATrunkJudgedInvocationThatRunsWhereItCannotTell(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")

	checkRefusedIn(t, caller, []placed{
		{name: "after a switch", dir: f.Dir(), command: "git switch main && git commit -m 'msg'"},
		{name: "after a cd to a non-literal", dir: f.Dir(), command: `cd "$d" && git commit -m 'msg'`},
		{name: "non-literal -C", dir: f.Dir(), command: `git -C "$d" commit -m 'msg'`},
	}, "git commit")
}

func TestJudge_AllowsAReadThatRunsWhereItCannotTell(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")

	checkAllowedIn(t, caller, []placed{
		{name: "after a switch", dir: f.Dir(), command: "git switch main && git status"},
		{name: "after a cd to a non-literal", dir: f.Dir(), command: `cd "$d" && git log`},
	})
}

func TestJudge_RefusesARebaseOfTheTrunkFromAnotherBranch(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")

	checkRefusedIn(t, caller, []placed{
		{name: "trunk as the branch argument", dir: f.Dir(), command: "git rebase topic main"},
		{name: "trunk after --onto", dir: f.Dir(), command: "git rebase --onto topic topic main"},
	}, "ils branch")
}

func TestJudge_WhenOriginOrHeadCannotBeRead_RefusesSayingWhatBroke(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	tests := []struct {
		name   string
		failOn []string
	}{
		{name: "origin URL", failOn: []string{"remote"}},
		{name: "HEAD", failOn: []string{"symbolic-ref"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, caller := standingOn(t, "main", "topic")
			caller.Git.Env = append(f.Env(), gittest.FakeGit(t, gittest.Fake{FailOn: tt.failOn})...)
			const command = "git commit -m 'msg'"

			got := guard.Judge(t.Context(), command, caller)

			assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome, reason %q", command, got.Reason)
			assert.Check(t, is.Contains(got.Reason, gittest.FailedSaid), "Judge(%q) reason", command)
		})
	}
}

func TestJudge_AsksOriginForTheTrunkBeforeTheCachedOne(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "main")
	cacheTrunk(f, "main")
	moveOriginTrunk(f, "develop", "main")

	checkAllowedIn(t, caller, []placed{
		{name: "on the trunk the clone last fetched", dir: f.Dir(), command: commitCommand},
	})
}

func TestJudge_WhenOriginDoesNotAnswer_RefusesACommitOnTheCachedTrunk(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "main")
	cacheTrunk(f, "main")
	cutOff(f)

	checkRefusedIn(t, caller, []placed{
		{name: "on the cached trunk", dir: f.Dir(), command: commitCommand},
	}, "ils branch")
}

func TestJudge_WhenOriginDoesNotAnswer_AllowsACommitOffTheCachedTrunk(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")
	cacheTrunk(f, "main")
	cutOff(f)

	checkAllowedIn(t, caller, []placed{
		{name: "off the cached trunk", dir: f.Dir(), command: commitCommand},
	})
}

func TestJudge_WhenOriginDoesNotAnswerAndNoTrunkIsCached_RefusesNamingAFetch(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")
	cutOff(f)

	checkRefusedIn(t, caller, []placed{
		{name: "off the trunk", dir: f.Dir(), command: commitCommand},
	}, "git fetch origin")
}

func TestJudge_WhenOriginStallsPastTheDeadline_JudgesAgainstTheCachedTrunk(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f, caller := standingOn(t, "main", "topic")
	cacheTrunk(f, "main")
	// Origin's live answer makes topic the trunk, which a call that was not cut would refuse on.
	f.Push("topic")
	f.OriginGit("symbolic-ref", "HEAD", "refs/heads/topic")
	addr, connected := gittest.SilentListener(t)
	caller.Git.Env = append(f.Env(), gittest.FakeGit(t, gittest.Fake{StallOn: []string{"ls-remote"}, StallAt: addr})...)
	ctx := gittest.Ending(t.Context(), connected, context.DeadlineExceeded)
	const command = "git commit -m 'msg'"

	got := guard.Judge(ctx, command, caller)

	assert.Check(t, is.DeepEqual(got, guard.Verdict{Outcome: guard.Allow}), "Judge(%q)", command)
}

func TestJudge_AsksOriginOncePerCommandAndGitDir(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")
	recorder := &git.Recorder{}
	caller.Git.Recorder = recorder
	const command = "git commit -m one && git commit -m two"

	got := guard.Judge(t.Context(), command, caller)

	assert.Check(t, is.DeepEqual(got, guard.Verdict{Outcome: guard.Allow}), "Judge(%q) in %s", command, f.Dir())
	assert.Check(t, is.Equal(lsRemotes(recorder), 1), "Judge(%q) ls-remote calls", command)
}

func TestJudge_WhenNoInvocationNeedsTheTrunk_AsksOriginNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		command  string
		detached bool
	}{
		{name: "reads on the trunk", command: "git status; git log --oneline -10"},
		{name: "commit on a detached HEAD", command: commitCommand, detached: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, caller := standingOn(t, "main", "main")
			if tt.detached {
				f.Detach()
			}
			recorder := &git.Recorder{}
			caller.Git.Recorder = recorder

			got := guard.Judge(t.Context(), tt.command, caller)

			assert.Check(t, is.DeepEqual(got, guard.Verdict{Outcome: guard.Allow}), "Judge(%q)", tt.command)
			assert.Check(t, is.Equal(lsRemotes(recorder), 0), "Judge(%q) ls-remote calls", tt.command)
		})
	}
}

func TestJudge_JudgesEachGitDirAgainstItsOwnTrunk(t *testing.T) {
	t.Parallel()

	offDevelop, caller := standingOn(t, "develop", "main")
	onMain, _ := standingOn(t, "main", "main")
	command := "git commit -m one; git -C " + quotedPath(onMain.Dir()) + " commit -m two"

	checkRefusedIn(t, caller, []placed{
		{name: "second repository on its trunk", dir: offDevelop.Dir(), command: command},
	}, "ils branch")
}

func TestJudge_WhenOriginNamesNoTrunk_RefusesSayingWhatBroke(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "main")
	f.DetachOriginHead()
	const command = "git commit -m 'msg'"

	got := guard.Judge(t.Context(), command, caller)

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome, reason %q", command, got.Reason)
	assert.Check(t, is.Contains(got.Reason, "read the trunk"), "Judge(%q) reason", command)
}
