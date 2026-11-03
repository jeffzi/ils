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

const (
	commitArgs    = "commit -m 'msg'"
	commitCommand = "git " + commitArgs
)

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

func callerIn(f *gittest.Fixture, dir string) guard.Caller {
	return callerWithEnv(dir, f.Env())
}

// recordingCaller returns a caller working in dir whose git runs with the environment of f, and
// the recorder that holds the git calls it makes.
func recordingCaller(f *gittest.Fixture, dir string) (guard.Caller, *git.Recorder) {
	recorder := &git.Recorder{}
	caller := callerIn(f, dir)
	caller.Git.Recorder = recorder

	return caller, recorder
}

func callerWithEnv(dir string, env []string) guard.Caller {
	return guard.Caller{Home: home, Dir: dir, Git: git.Runner{Env: env}}
}

// callerFailingOn returns a caller working in dir whose git runs with the environment of f and
// fails on the given subcommands. The fake git sets PATH for the whole process, so the test is not
// parallel.
func callerFailingOn(t *testing.T, f *gittest.Fixture, dir string, subcommands ...string) guard.Caller {
	t.Helper()

	return callerWithEnv(dir, append(f.Env(), gittest.FakeGit(t, gittest.Fake{FailOn: subcommands})...))
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

func TestJudge_RefusesACommitCreatingSubcommandOnTheTrunk(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "main")

	checkJudgedIn(t, caller, []placed{
		{name: "commit", dir: f.Dir(), command: commitCommand},
		{name: "rebase", dir: f.Dir(), command: "git rebase origin/main"},
		{name: "pull", dir: f.Dir(), command: "git pull"},
		{name: "pull whose last fast-forward option allows a merge", dir: f.Dir(), command: "git pull --ff-only --no-ff"},
	}, expectation{outcome: guard.Refuse, named: "main"})
}

func TestJudge_AllowsACommandThatCreatesNoCommitOnTheTrunk(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "main")

	checkAllowed(t, caller, []namedCommand{
		{name: "pull whose last fast-forward option is --ff-only", command: "git pull --no-ff --ff-only"},
		{name: "fast-forward pull naming origin and the trunk", command: "git pull --ff-only origin main"},
		{name: "fast-forward pull naming origin alone", command: "git pull --ff-only origin"},
		{name: "fast-forward pull naming the trunk by its full ref", command: "git pull --ff-only origin refs/heads/main"},
		{name: "fast-forward pull naming the trunk with a leading plus", command: "git pull --ff-only origin +main"},
		{
			name:    "fast-forward pull naming the trunk by its full ref with a leading plus",
			command: "git pull --ff-only origin +refs/heads/main",
		},
	})
}

func TestJudge_RefusesAFastForwardPullOfAnotherBranchOnTheTrunk(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "main")

	checkJudgedIn(t, caller, []placed{
		{name: "repository other than origin", dir: f.Dir(), command: "git pull --ff-only . main"},
		{name: "refspec other than the trunk", dir: f.Dir(), command: "git pull --ff-only origin topic"},
		{name: "non-literal repository", dir: f.Dir(), command: `git pull --ff-only "$r"`},
		{name: "non-literal refspec", dir: f.Dir(), command: `git pull --ff-only origin "$b"`},
	}, expectation{outcome: guard.Refuse, named: "git pull"})
}

func TestJudge_AllowsACommitCreatingSubcommandOffTheTrunk(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "plan/x")

	checkAllowed(t, caller, []namedCommand{
		{name: "commit", command: commitCommand},
		{name: "rebase naming the current branch", command: "git rebase origin/main plan/x"},
	})
}

func TestJudge_AllowsACommitWhereNoTrunkIsProtected(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "main")
	noOrigin, _ := standingOn(t, "main", "main")
	noOrigin.RemoveOrigin()

	checkJudgedIn(t, caller, []placed{
		{name: "repository with no origin", dir: noOrigin.Dir(), command: commitCommand},
		{name: "outside any repository", dir: outsideDir(t), command: commitCommand},
	}, expectation{outcome: guard.Allow})
}

func TestJudge_WhenTheTrunkIsNotMain_AllowsACommitOnMain(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "develop", "main")

	checkJudgedRow(t, caller, judged{command: commitCommand, want: guard.Allow})
}

func TestJudge_RefusesACommitWhoseOwnTargetIsOnTheTrunk(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "main")
	feature := addWorktree(f, filepath.Join(outsideDir(t), "feature"))
	trunkDir := quotedPath(f.Dir())
	trunkGitDir := quotedPath(filepath.Join(f.Dir(), ".git"))

	checkJudgedIn(t, caller, []placed{
		{name: "-C naming the trunk checkout", dir: feature, command: "git -C " + trunkDir + " " + commitArgs},
		{
			name:    "--git-dir and --work-tree naming the trunk checkout",
			dir:     feature,
			command: "git --git-dir=" + trunkGitDir + " --work-tree=" + trunkDir + " " + commitArgs,
		},
	}, expectation{outcome: guard.Refuse, named: "main"})
}

func TestJudge_AllowsACommitWhoseOwnTargetIsOffTheTrunk(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "main")
	feature := addWorktree(f, filepath.Join(outsideDir(t), "feature"))

	checkJudgedRow(t, caller, judged{command: "git -C " + quotedPath(feature) + " " + commitArgs, want: guard.Allow})
}

func TestJudge_RefusesATrunkJudgedInvocationThatRunsWhereItCannotTell(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")

	checkJudgedIn(t, caller, []placed{
		{name: "after a switch", dir: f.Dir(), command: "git switch main && git " + commitArgs},
		{name: "after a cd to a non-literal", dir: f.Dir(), command: `cd "$d" && git ` + commitArgs},
		{name: "non-literal -C", dir: f.Dir(), command: `git -C "$d" ` + commitArgs},
	}, expectation{outcome: guard.Refuse, named: "git commit"})
}

func TestJudge_AllowsAReadThatRunsWhereItCannotTell(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkJudgedRow(t, caller, judged{command: "git switch main && git status", want: guard.Allow})
}

func TestJudge_RefusesARebaseOfTheTrunkFromAnotherBranch(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")

	checkJudgedIn(t, caller, []placed{
		{name: "trunk as the branch argument", dir: f.Dir(), command: "git rebase topic main"},
		{name: "trunk after --onto", dir: f.Dir(), command: "git rebase --onto topic topic main"},
		{name: "trunk as the branch argument after --", dir: f.Dir(), command: "git rebase -- origin/main main"},
	}, expectation{outcome: guard.Refuse, named: "main"})
}

func TestJudge_WhenTheRebasedBranchIsNotALiteral_RefusesNamingTheCommand(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkJudgedRow(t, caller, judged{command: `git rebase -- origin/main "$b"`, want: guard.Refuse, wantNamed: "git rebase"})
}

func TestJudge_WhenOriginsURLCannotBeRead_RefusesSayingWhatBroke(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f, _ := standingOn(t, "main", "topic")
	caller := callerFailingOn(t, f, f.Dir(), "remote")

	checkJudgedRow(t, caller, judged{command: commitCommand, wantNamed: gittest.FailedSaid, want: guard.Refuse})
}

func TestJudge_WhenOriginDoesNotAnswerAndNoTrunkIsCached_RefusesNamingTheCommand(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")
	f.CutOffOrigin()

	checkJudgedRow(t, caller, judged{command: commitCommand, want: guard.Refuse, wantNamed: "git commit"})
}

func TestJudge_WhenACommandRunsGitTwiceInOneGitDir_AsksOriginOnce(t *testing.T) {
	t.Parallel()

	f, _ := standingOn(t, "main", "topic")
	caller, recorder := recordingCaller(f, f.Dir())
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
			f, _ := standingOn(t, "main", "main")
			if tt.detached {
				f.Detach()
			}
			caller, recorder := recordingCaller(f, f.Dir())

			got := guard.Judge(t.Context(), tt.command, caller)

			assert.Check(t, is.DeepEqual(got, guard.Verdict{Outcome: guard.Allow}), "Judge(%q)", tt.command)
			assert.Check(t, is.Equal(lsRemotes(recorder), 0), "Judge(%q) ls-remote calls", tt.command)
		})
	}
}

func TestJudge_WhenACommandSpansTwoGitDirs_JudgesEachAgainstItsOwnTrunk(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "develop", "main")
	onMain, _ := standingOn(t, "main", "main")
	command := "git commit -m one; git -C " + quotedPath(onMain.Dir()) + " commit -m two"

	checkJudgedRow(t, caller, judged{command: command, want: guard.Refuse, wantNamed: "main"})
}

func TestJudge_WhenOriginNamesNoTrunk_RefusesSayingWhatBroke(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "main")
	f.DetachOriginHead()

	checkJudgedRow(t, caller, judged{command: commitCommand, want: guard.Refuse, wantNamed: "read the trunk"})
}
