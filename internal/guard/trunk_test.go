//go:build unix

package guard_test

import (
	"path/filepath"
	"slices"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
)

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
	return callerWithEnv(dir, f.Env())
}

// callerWithEnv returns a caller working in dir whose git runs with env.
func callerWithEnv(dir string, env []string) guard.Caller {
	return guard.Caller{Home: home, Dir: dir, Git: git.Runner{Env: env}}
}

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
func commitCreating() []namedCommand {
	return []namedCommand{
		{name: "commit", command: commitCommand},
		{name: "rebase", command: "git rebase origin/main"},
		{name: "pull", command: "git pull"},
		{name: "pull whose last fast-forward option allows a merge", command: "git pull --ff-only --no-ff"},
	}
}

func TestJudge_RefusesACommitCreatingSubcommandOnTheTrunk(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "main")
	for _, tt := range commitCreating() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checkJudgedRow(t, caller, judged{command: tt.command, wantNamed: "ils branch", want: guard.Refuse})
		})
	}
}

func TestJudge_AllowsACommandThatCreatesNoCommitOnTheTrunk(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "main")

	checkAllowed(t, caller, []namedCommand{
		{name: "pull --ff-only", command: "git pull --ff-only"},
		{name: "pull whose last fast-forward option is --ff-only", command: "git pull --no-ff --ff-only"},
	})
}

func TestJudge_AllowsACommitCreatingSubcommandOffTheTrunk(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "plan/x")

	checkAllowed(t, caller, append(commitCreating(),
		namedCommand{name: "rebase naming the current branch", command: "git rebase origin/main plan/x"},
	))
}

func TestJudge_AllowsACommitWhereNoTrunkIsProtected(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "main")
	noOrigin, _ := standingOn(t, "main", "main")
	noOrigin.RemoveOrigin()

	checkAllowedIn(t, caller, []placed{
		{name: "repository with no origin", dir: noOrigin.Dir(), command: commitCommand},
		{name: "outside any repository", dir: outsideDir(t), command: commitCommand},
	})
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

			checkJudgedRow(t, caller, judged{command: commitCommand, wantNamed: gittest.FailedSaid, want: guard.Refuse})
		})
	}
}

func TestJudge_WhenOriginDoesNotAnswerAndNoTrunkIsCached_RefusesNamingTheCommand(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")
	f.CutOffOrigin()

	checkRefusedIn(t, caller, []placed{
		{name: "off the trunk", dir: f.Dir(), command: commitCommand},
	}, "git commit")
}

func TestJudge_WhenACommandRunsGitTwiceInOneGitDir_AsksOriginOnce(t *testing.T) {
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

func TestJudge_WhenACommandSpansTwoGitDirs_JudgesEachAgainstItsOwnTrunk(t *testing.T) {
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

	checkRefusedIn(t, caller, []placed{
		{name: "origin with a detached HEAD", dir: f.Dir(), command: commitCommand},
	}, "read the trunk")
}
