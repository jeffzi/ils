package guard_test

import (
	"path/filepath"
	"slices"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
)

const (
	commitArgs    = "commit -m 'msg'"
	commitCommand = "git " + commitArgs
)

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
	// Not parallel: standingOnGitHub sets GH_PATH for the whole process.
	_, caller := standingOnGitHub(t, "main", "main")

	checkJudgedInTurn(t, caller, []judged{
		{name: "commit", command: commitCommand, want: guard.Refuse, wantNamed: onMain},
		{name: "rebase", command: "git rebase origin/main", want: guard.Refuse, wantNamed: onMain},
		{name: "pull", command: "git pull", want: guard.Refuse, wantNamed: onMain},
		{name: "pull whose last fast-forward option allows a merge", command: "git pull --ff-only --no-ff", want: guard.Refuse, wantNamed: onMain},
	})
}

func TestJudge_AllowsACommandThatCreatesNoCommitOnTheTrunk(t *testing.T) {
	// Not parallel: standingOnGitHub sets GH_PATH for the whole process.
	_, caller := standingOnGitHub(t, "main", "main")

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
	// Not parallel: standingOnGitHub sets GH_PATH for the whole process.
	_, caller := standingOnGitHub(t, "main", "main")

	checkJudgedInTurn(t, caller, []judged{
		{name: "repository other than origin", command: "git pull --ff-only . main", want: guard.Refuse},
		{name: "refspec other than the trunk", command: "git pull --ff-only origin topic", want: guard.Refuse, wantNamed: "git pull --ff-only"},
		{name: "non-literal refspec after --", command: `git pull --ff-only -- origin main"$b"`, want: guard.Refuse},
	})
}

func TestJudge_AllowsACommitCreatingSubcommandOffTheTrunk(t *testing.T) {
	// Not parallel: standingOnGitHub sets GH_PATH for the whole process.
	_, caller := standingOnGitHub(t, "main", "plan/x")

	checkAllowed(t, caller, []namedCommand{
		{name: "commit", command: commitCommand},
		{name: "rebase naming the current branch", command: "git rebase origin/main plan/x"},
	})
}

func TestJudge_AllowsACommitWhereThereIsNoOrigin(t *testing.T) {
	t.Parallel()

	noOrigin, caller := standingOn(t, "main", "main")
	noOrigin.RemoveOrigin()

	checkJudgedIn(t, caller, []placed{
		{name: "repository with no origin", dir: noOrigin.Dir(), command: commitCommand},
		{name: "outside any repository", dir: outsideDir(t), command: commitCommand},
	}, expectation{outcome: guard.Allow})
}

func TestJudge_WhenTheTrunkIsNotMain_AllowsACommitOnMain(t *testing.T) {
	// Not parallel: standingOnGitHub sets GH_PATH for the whole process.
	_, caller := standingOnGitHub(t, "develop", "main")

	checkJudgedRow(t, caller, judged{command: commitCommand, want: guard.Allow})
}

func TestJudge_RefusesACommitWhoseOwnTargetIsOnTheTrunk(t *testing.T) {
	// Not parallel: standingOnGitHub sets GH_PATH for the whole process.
	f, caller := standingOnGitHub(t, "main", "main")
	feature := addWorktree(t, f, filepath.Join(outsideDir(t), "feature"))
	trunkDir := gittest.ShellWord(f.Dir())
	trunkGitDir := gittest.ShellWord(filepath.Join(f.Dir(), ".git"))

	checkJudgedInInTurn(t, caller, []placed{
		{name: "-C naming the trunk checkout", dir: feature, command: "git -C " + trunkDir + " " + commitArgs},
		{
			name:    "--git-dir and --work-tree naming the trunk checkout",
			dir:     feature,
			command: "git --git-dir=" + trunkGitDir + " --work-tree=" + trunkDir + " " + commitArgs,
		},
		{name: "GIT_DIR exported by an earlier eval string", dir: feature, command: `eval "export GIT_DIR=` + trunkGitDir + `"; git ` + commitArgs},
	}, expectation{outcome: guard.Refuse, named: onMain})
}

func TestJudge_WhenAReadRunsWhereTheGuardCannotTell_AllowsIt(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkJudgedRow(t, caller, judged{command: "git switch main; git status", want: guard.Allow})
}

func TestJudge_RefusesARebaseOfTheTrunkFromAnotherBranch(t *testing.T) {
	// Not parallel: standingOnGitHub sets GH_PATH for the whole process.
	_, caller := standingOnGitHub(t, "main", "topic")

	checkJudgedInTurn(t, caller, []judged{
		{name: "trunk as the branch argument", command: "git rebase topic main", want: guard.Refuse, wantNamed: onMain},
		{name: "trunk as the branch argument after --", command: "git rebase -- origin/main main", want: guard.Refuse, wantNamed: onMain},
	})
}

func TestJudge_WhenTheRebasedBranchIsNotALiteral_RefusesNamingTheCommand(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkJudgedRow(t, caller, judged{command: `git rebase -- origin/main "$b"`, want: guard.Refuse, wantNamed: "git rebase"})
}

func TestJudge_WhenOriginsURLCannotBeRead_RefusesSayingWhatBroke(t *testing.T) {
	t.Parallel()

	f, _ := standingOn(t, "main", "topic")
	caller := callerFailingOn(f, f.Dir(), "remote")

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

	checkJudgedRow(t, caller, judged{command: command, want: guard.Allow})

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

			checkJudgedRow(t, caller, judged{command: tt.command, want: guard.Allow})

			assert.Check(t, is.Equal(lsRemotes(recorder), 0), "Judge(%q) ls-remote calls", tt.command)
		})
	}
}

func TestJudge_WhenACommandSpansTwoGitDirs_JudgesEachAgainstItsOwnTrunk(t *testing.T) {
	// Not parallel: onGitHubCom sets GH_PATH for the whole process.
	_, caller := standingOn(t, "develop", "main")
	trunk, _ := standingOn(t, "main", "main")
	onGitHubCom(t, trunk)
	command := "git commit -m one; git -C " + gittest.ShellWord(trunk.Dir()) + " commit -m two"

	checkJudgedRow(t, caller, judged{command: command, want: guard.Refuse, wantNamed: onMain})
}

func TestJudge_WhenOriginNamesNoTrunk_RefusesSayingWhatBroke(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "main")
	f.DetachOriginHead()

	checkJudgedRow(t, caller, judged{command: commitCommand, want: guard.Refuse, wantNamed: git.ErrNoSymref.Error()})
}

const (
	githubHost     = "github.com"
	enterpriseHost = "git.example.com"
	originRepo     = "ada/engine"
	onMain         = "on main, the default branch"
	loginCheckSaid = "dial tcp: lookup " + enterpriseHost + ": no such host"
)

func setGitHubPushURL(f *gittest.Fixture, host string) {
	f.SetPushURL("https://" + host + "/" + originRepo + ".git")
}

// withoutGh points GH_PATH, for the rest of the test, at a file that does not exist, so no gh starts.
func withoutGh(t *testing.T) {
	t.Helper()

	t.Setenv(github.PathVariable, filepath.Join(t.TempDir(), github.Program))
}

// onGitHubCom sets origin's push URL of every fixture to ada/engine on github.com, with no gh to
// start. It sets GH_PATH for the whole process, so the test is not parallel.
func onGitHubCom(t *testing.T, fixtures ...*gittest.Fixture) {
	t.Helper()

	for _, f := range fixtures {
		setGitHubPushURL(f, githubHost)
	}

	withoutGh(t)
}

// standingOnGitHub is standingOn in a repository whose push URL names originRepo on github.com,
// with no gh to start. It sets GH_PATH for the whole process, so the test is not parallel.
func standingOnGitHub(t *testing.T, trunk, branch string) (*gittest.Fixture, guard.Caller) {
	t.Helper()

	f, caller := standingOn(t, trunk, branch)
	onGitHubCom(t, f)

	return f, caller
}

// loggedInTo scripts, for the rest of the test, the gh that caller runs as one whose active
// account on host passes its login check.
func loggedInTo(t *testing.T, caller *guard.Caller, host string) *ghtest.Fake {
	t.Helper()

	return answeringGh(t, caller, ghtest.Exits(ghtest.LoggedIn(host), ghtest.LoginArgs(host)...))
}

// loggedOutOf scripts, for the rest of the test, the gh that caller runs as one with no login for
// host.
func loggedOutOf(t *testing.T, caller *guard.Caller, host string) {
	t.Helper()

	answeringGh(t, caller, ghtest.Exits(ghtest.LoggedOut(), ghtest.LoginArgs(host)...))
}

// loginFailingOn scripts, for the rest of the test, the gh that caller runs as one whose active
// account on host fails its login check, saying loginCheckSaid.
func loginFailingOn(t *testing.T, caller *guard.Caller, host string) {
	t.Helper()

	account := ghtest.Account{State: "error", Error: loginCheckSaid, Active: true}
	answeringGh(t, caller, ghtest.Exits(ghtest.LoginStatus(host, account), ghtest.LoginArgs(host)...))
}

// onTrunkPushingTo returns a caller working on the trunk of a repository whose push URL names
// originRepo on host.
func onTrunkPushingTo(t *testing.T, host string) guard.Caller {
	t.Helper()

	f, caller := standingOn(t, "main", "main")
	setGitHubPushURL(f, host)

	return caller
}

func TestJudge_WhenGhIsLoggedInToAHostOtherThanGitHubCom_RefusesACommitOnTheTrunk(t *testing.T) {
	t.Parallel()
	caller := onTrunkPushingTo(t, enterpriseHost)
	loggedInTo(t, &caller, enterpriseHost)

	checkJudgedRow(t, caller, judged{command: commitCommand, want: guard.Refuse, wantNamed: onMain})
}

func TestJudge_WhenGhHoldsNoLoginForAHostOtherThanGitHubCom_AllowsACommitOnTheTrunk(t *testing.T) {
	tests := []struct {
		setup func(t *testing.T, caller *guard.Caller)
		name  string
	}{
		{name: "gh missing", setup: func(t *testing.T, _ *guard.Caller) { t.Helper(); withoutGh(t) }},
		{name: "gh not logged in to the host", setup: func(t *testing.T, caller *guard.Caller) { t.Helper(); loggedOutOf(t, caller, enterpriseHost) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: withoutGh sets GH_PATH for the whole process.
			caller := onTrunkPushingTo(t, enterpriseHost)
			tt.setup(t, &caller)

			checkJudgedRow(t, caller, judged{command: commitCommand, want: guard.Allow})
		})
	}
}

func TestJudge_WhenGhsLoginCheckFailsOnAHostOtherThanGitHubCom_RefusesSayingWhy(t *testing.T) {
	t.Parallel()
	caller := onTrunkPushingTo(t, enterpriseHost)
	loginFailingOn(t, &caller, enterpriseHost)

	checkJudgedRow(t, caller, judged{command: commitCommand, want: guard.Refuse, wantNamed: loginCheckSaid})
}

func TestJudge_WhenTheGhConfigCannotBeTold_RefusesACommitOnTheTrunk(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		host  string
		named string
	}{
		{name: "host other than github.com", host: enterpriseHost, named: "GH_CONFIG_DIR"},
		{name: "github.com, which needs no gh", host: githubHost, named: onMain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			caller := onTrunkPushingTo(t, tt.host)
			loggedInTo(t, &caller, tt.host)

			checkJudgedRow(t, caller, judged{command: `GH_CONFIG_DIR="$d" ` + commitCommand, want: guard.Refuse, wantNamed: tt.named})
		})
	}
}

func TestJudge_WhenTheCommandSetsTheGhConfig_ChecksTheLoginToAHostOtherThanGitHubComWithIt(t *testing.T) {
	// Not parallel: ghReadingConfig sets GH_PATH and the gh config location for the whole process.
	caller := onTrunkPushingTo(t, enterpriseHost)
	routes := map[string][]string{"login": ghtest.LoginArgs(enterpriseHost)}
	// The process's own gh is logged out; the config the command sets is logged in.
	ghReadingConfig(t, routes, map[string]ghtest.Answer{"login": ghtest.LoggedOut()})
	config := configHolding(t, "", map[string]ghtest.Answer{"login": ghtest.LoggedIn(enterpriseHost)})
	command := "GH_CONFIG_DIR=" + gittest.ShellWord(config) + " " + commitCommand

	checkJudgedRow(t, caller, judged{command: command, want: guard.Refuse, wantNamed: onMain})
}

func TestJudge_WhenOriginsSSHHostCannotBeResolved_RefusesACommitOnTheTrunk(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "main")
	f.SetPushURL("git@github.com:" + originRepo + ".git")
	f.RemoveSSHConfig()

	checkJudgedRow(t, caller, judged{command: commitCommand, want: guard.Refuse, wantNamed: "ssh"})
}

func TestJudge_WhenHEADIsOffTheTrunk_RunsNoGh(t *testing.T) {
	t.Parallel()
	caller := pushingTo(t, enterpriseHost)
	fake := loggedInTo(t, &caller, enterpriseHost)

	checkJudgedRow(t, caller, judged{command: commitCommand, want: guard.Allow})

	assert.Check(t, is.Len(fake.Calls(), 0), "Judge(%q) gh calls", commitCommand)
}

func TestJudge_WhenOriginsPushURLCannotBeReadOnTheTrunk_RefusesSayingWhatBroke(t *testing.T) {
	t.Parallel()

	f, _ := standingOn(t, "main", "main")
	caller := callerFailingOn(f, f.Dir(), "remote", "get-url", "--push")

	checkJudgedRow(t, caller, judged{command: commitCommand, wantNamed: gittest.FailedSaid, want: guard.Refuse})
}

func TestJudge_WhenOnlyTheFetchURLNamesAGitHubRepository_AllowsACommitOnTheTrunk(t *testing.T) {
	t.Parallel()

	f, _ := standingOn(t, "main", "main")
	f.SetOriginURL("git@github.com:" + originRepo + ".git")
	// A local path names no repository on GitHub.
	f.SetPushURL(f.OriginDir())
	caller := callerWithEnv(f.Dir(), f.ServedEnv(f.OriginDir()))

	checkJudgedRow(t, caller, judged{command: commitCommand, want: guard.Allow})
}

func TestJudge_WhenTheGitDirCannotBeTold_RefusesACommit(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "main")
	missing := gittest.ShellWord(filepath.Join(t.TempDir(), "missing"))

	checkJudgedRow(t, caller, judged{
		command: "git -C " + missing + " " + commitArgs, want: guard.Refuse, wantNamed: "cannot tell where the command runs",
	})
}
