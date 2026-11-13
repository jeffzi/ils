//go:build unix

package guard_test

import (
	"path/filepath"
	"slices"
	"strconv"
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
	// Not parallel: standingOnProtected sets GH_PATH for the whole process.
	_, caller := standingOnProtected(t, "main", "main")

	checkJudgedInTurn(t, caller, []judged{
		{name: "commit", command: commitCommand, want: guard.Refuse, wantNamed: "main"},
		{name: "rebase", command: "git rebase origin/main", want: guard.Refuse, wantNamed: "main"},
		{name: "pull", command: "git pull", want: guard.Refuse, wantNamed: "main"},
		{name: "pull whose last fast-forward option allows a merge", command: "git pull --ff-only --no-ff", want: guard.Refuse, wantNamed: "main"},
	})
}

func TestJudge_AllowsACommandThatCreatesNoCommitOnTheTrunk(t *testing.T) {
	// Not parallel: standingOnProtected sets GH_PATH for the whole process.
	_, caller := standingOnProtected(t, "main", "main")

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
	// Not parallel: standingOnProtected sets GH_PATH for the whole process.
	_, caller := standingOnProtected(t, "main", "main")

	checkJudgedInTurn(t, caller, []judged{
		{name: "repository other than origin", command: "git pull --ff-only . main", want: guard.Refuse},
		{name: "refspec other than the trunk", command: "git pull --ff-only origin topic", want: guard.Refuse, wantNamed: "git pull --ff-only"},
		{name: "non-literal refspec after --", command: `git pull --ff-only -- origin main"$b"`, want: guard.Refuse},
	})
}

func TestJudge_AllowsACommitCreatingSubcommandOffTheTrunk(t *testing.T) {
	// Not parallel: standingOnProtected sets GH_PATH for the whole process.
	_, caller := standingOnProtected(t, "main", "plan/x")

	checkAllowed(t, caller, []namedCommand{
		{name: "commit", command: commitCommand},
		{name: "rebase naming the current branch", command: "git rebase origin/main plan/x"},
	})
}

func TestJudge_AllowsACommitWhereNoTrunkIsProtected(t *testing.T) {
	t.Parallel()

	noOrigin, caller := standingOn(t, "main", "main")
	noOrigin.RemoveOrigin()

	checkJudgedIn(t, caller, []placed{
		{name: "repository with no origin", dir: noOrigin.Dir(), command: commitCommand},
		{name: "outside any repository", dir: outsideDir(t), command: commitCommand},
	}, expectation{outcome: guard.Allow})
}

func TestJudge_WhenTheTrunkIsNotMain_AllowsACommitOnMain(t *testing.T) {
	// Not parallel: standingOnProtected sets GH_PATH for the whole process.
	_, caller := standingOnProtected(t, "develop", "main")

	checkJudgedRow(t, caller, judged{command: commitCommand, want: guard.Allow})
}

func TestJudge_RefusesACommitWhoseOwnTargetIsOnTheTrunk(t *testing.T) {
	// Not parallel: standingOnProtected sets GH_PATH for the whole process.
	f, caller := standingOnProtected(t, "main", "main")
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
	}, expectation{outcome: guard.Refuse, named: "main"})
}

func TestJudge_RefusesATrunkJudgedInvocationThatRunsWhereItCannotTell(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "non-literal -C", command: `git -C "$d" ` + commitArgs, wantNamed: "git commit"},
	})
}

func TestJudge_WhenAReadRunsWhereTheGuardCannotTell_AllowsIt(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkJudgedRow(t, caller, judged{command: "git switch main; git status", want: guard.Allow})
}

func TestJudge_RefusesARebaseOfTheTrunkFromAnotherBranch(t *testing.T) {
	// Not parallel: standingOnProtected sets GH_PATH for the whole process.
	_, caller := standingOnProtected(t, "main", "topic")

	checkJudgedInTurn(t, caller, []judged{
		{name: "trunk as the branch argument", command: "git rebase topic main", want: guard.Refuse, wantNamed: "main"},
		{name: "trunk as the branch argument after --", command: "git rebase -- origin/main main", want: guard.Refuse, wantNamed: "main"},
	})
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
	// Not parallel: protectTrunk sets GH_PATH for the whole process.
	_, caller := standingOn(t, "develop", "main")
	onMain, _ := standingOn(t, "main", "main")
	protectTrunk(t, "main", onMain)
	command := "git commit -m one; git -C " + gittest.ShellWord(onMain.Dir()) + " commit -m two"

	checkJudgedRow(t, caller, judged{command: command, want: guard.Refuse, wantNamed: "main"})
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
	// originRepo is the owner/name of the GitHub repository origin's push URL names.
	originRepo = "ada/engine"
	// protectedTrunk is what only the refusal on a protected trunk says, never one whose protection
	// the guard could not read.
	protectedTrunk = "the protected trunk"
)

// rulesArgs is the argv of the read of the ruleset rules that apply to trunk in originRepo on host.
func rulesArgs(host, trunk string) []string {
	return ghtest.RulesArgs(host, originRepo, trunk)
}

// branchArgs is the argv of the read of the branch trunk of originRepo on host, whose protected
// field says whether classic branch protection covers it.
func branchArgs(host, trunk string) []string {
	return []string{"api", "--hostname", host, "repos/" + originRepo + "/branches/" + trunk}
}

// protection is what gh answers the two reads of whether a trunk is protected.
type protection struct {
	rules  ghtest.Answer
	branch ghtest.Answer
}

// classicAnswer is the branch read's answer for a branch that classic protection covers or not.
func classicAnswer(protected bool) ghtest.Answer {
	return ghtest.Answer{Stdout: `{"name":"main","protected":` + strconv.FormatBool(protected) + `}`}
}

// rulesAnswer is the rules read's answer holding the rules given, each a JSON object.
func rulesAnswer(rules ...string) ghtest.Answer {
	return ghtest.Answer{Stdout: ghtest.JSONArray(rules...)}
}

// ruleProtected is the protection of a trunk one active ruleset rule applies to.
func ruleProtected() protection {
	return protection{rules: rulesAnswer(`{"type":"deletion","ruleset_id":7}`), branch: classicAnswer(false)}
}

// unprotected is the protection of a trunk no rule applies to and classic protection does not cover.
func unprotected() protection {
	return protection{rules: rulesAnswer(), branch: classicAnswer(false)}
}

// failing is the protection of a trunk both of whose reads fail with answer.
func failing(answer ghtest.Answer) protection {
	return protection{rules: answer, branch: answer}
}

// setGitHubPushURL sets origin's push URL in f to the repository ada/engine on host.
func setGitHubPushURL(f *gittest.Fixture, host string) {
	f.SetPushURL("https://" + host + "/" + originRepo + ".git")
}

// scriptProtection scripts, for the rest of the test, a gh logged in to host that answers read to
// the reads of whether trunk is protected in ada/engine there. It sets GH_PATH for the whole
// process, so the test is not parallel.
func scriptProtection(t *testing.T, host, trunk string, read protection) *ghtest.Fake {
	t.Helper()

	return ghtest.Setup(t,
		ghtest.Exits(read.rules, rulesArgs(host, trunk)...),
		ghtest.Exits(read.branch, branchArgs(host, trunk)...),
		ghtest.Exits(ghtest.LoggedIn(), ghtest.LoginArgs(host)...),
	)
}

// onGitHub sets origin's push URL in f to ada/engine on host and scripts a gh answering read for
// the trunk main there, as scriptProtection does.
func onGitHub(t *testing.T, f *gittest.Fixture, host string, read protection) *ghtest.Fake {
	t.Helper()

	setGitHubPushURL(f, host)

	return scriptProtection(t, host, "main", read)
}

// protectTrunk sets origin's push URL of every fixture to ada/engine on github.com and scripts a gh
// answering that a ruleset rule protects trunk there. It sets GH_PATH for the whole process, so the
// test is not parallel.
func protectTrunk(t *testing.T, trunk string, fixtures ...*gittest.Fixture) {
	t.Helper()

	for _, f := range fixtures {
		setGitHubPushURL(f, githubHost)
	}

	scriptProtection(t, githubHost, trunk, ruleProtected())
}

// standingOnProtected is standingOn in a repository whose push URL names originRepo on github.com,
// where a ruleset rule protects trunk. It sets GH_PATH for the whole process, so the test is not
// parallel.
func standingOnProtected(t *testing.T, trunk, branch string) (*gittest.Fixture, guard.Caller) {
	t.Helper()

	f, caller := standingOn(t, trunk, branch)
	protectTrunk(t, trunk, f)

	return f, caller
}

// checkRefusedNaming fails unless Judge, run by caller, refuses command with a reason holding
// every one of named.
func checkRefusedNaming(t *testing.T, caller guard.Caller, command string, named ...string) {
	t.Helper()

	got := guard.Judge(t.Context(), command, caller)

	checkVerdict(t, judged{command: command, want: guard.Refuse}, got, named...)
}

// callsOf returns how many of calls, each led by gh, have the argv args.
func callsOf(calls [][]string, args []string) int {
	want := append([]string{github.Program}, args...)
	count := 0

	for _, call := range calls {
		if slices.Equal(call, want) {
			count++
		}
	}

	return count
}

func TestJudge_WhenOriginsTrunkIsProtected_RefusesSayingItIsProtected(t *testing.T) {
	tests := []struct {
		name string
		read protection
	}{
		{name: "classic protection covers it", read: protection{rules: rulesAnswer(), branch: classicAnswer(true)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: onGitHub sets GH_PATH for the whole process.
			f, caller := standingOn(t, "main", "main")
			onGitHub(t, f, githubHost, tt.read)

			checkRefusedNaming(t, caller, commitCommand, "main", protectedTrunk, "ils branch")
		})
	}
}

func TestJudge_WhenThePlanAllowsNoRulesAndNoClassicProtectionCoversTheTrunk_AllowsACommit(t *testing.T) {
	// Not parallel: onGitHub sets GH_PATH for the whole process.
	f, caller := standingOn(t, "main", "main")
	rulesUnavailable := ghtest.Answer{
		Stdout: `{"message":"Upgrade to GitHub Pro"}`, Stderr: "gh: Upgrade to GitHub Pro (HTTP 403)\n", Code: 1,
	}
	onGitHub(t, f, githubHost, protection{rules: rulesUnavailable, branch: classicAnswer(false)})

	checkJudgedRow(t, caller, judged{command: commitCommand, want: guard.Allow})
}

func TestJudge_WhenGhIsLoggedInToAnEnterpriseHost_RefusesACommitOnItsProtectedTrunk(t *testing.T) {
	// Not parallel: onGitHub sets GH_PATH for the whole process.
	f, caller := standingOn(t, "main", "main")
	onGitHub(t, f, enterpriseHost, ruleProtected())

	checkRefusedNaming(t, caller, commitCommand, "main", protectedTrunk, "ils branch")
}

// notLoggedIn is what a gh call answers when gh holds no login for the host it addresses.
func notLoggedIn() ghtest.Answer {
	return ghtest.Answer{Stderr: "To get started with GitHub CLI, please run:  gh auth login\n", Code: 4}
}

// loggedOutOf scripts, for the rest of the test, a gh with no login for host, whose protection reads
// of trunk there fail.
func loggedOutOf(t *testing.T, host, trunk string) {
	t.Helper()

	ghtest.Setup(t,
		ghtest.Exits(notLoggedIn(), rulesArgs(host, trunk)...),
		ghtest.Exits(notLoggedIn(), branchArgs(host, trunk)...),
		ghtest.Exits(ghtest.LoggedOut(), ghtest.LoginArgs(host)...),
	)
}

// withoutGh points GH_PATH, for the rest of the test, at a file that does not exist, so no gh starts.
func withoutGh(t *testing.T) {
	t.Helper()

	t.Setenv(github.PathVariable, filepath.Join(t.TempDir(), github.Program))
}

func TestJudge_WhenGhCannotReadAnEnterpriseHost_AllowsACommitOnTheTrunk(t *testing.T) {
	tests := []struct {
		setup func(t *testing.T)
		name  string
	}{
		{name: "gh missing", setup: withoutGh},
		{name: "gh not logged in to the host", setup: func(t *testing.T) { t.Helper(); loggedOutOf(t, enterpriseHost, "main") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: the setup sets GH_PATH for the whole process.
			f, caller := standingOn(t, "main", "main")
			setGitHubPushURL(f, enterpriseHost)
			tt.setup(t)

			checkJudgedRow(t, caller, judged{command: commitCommand, want: guard.Allow})
		})
	}
}

func TestJudge_WhenTheProtectionOfAGitHubTrunkCannotBeRead_RefusesSayingWhy(t *testing.T) {
	tests := []struct {
		setup func(t *testing.T)
		name  string
		named []string
	}{
		{name: "gh missing", setup: withoutGh, named: []string{"gh", "missing", "ils branch"}},
		{
			name:  "gh not logged in to github.com",
			setup: func(t *testing.T) { t.Helper(); loggedOutOf(t, githubHost, "main") },
			named: []string{"! gh auth login --hostname github.com", "ils branch"},
		},
		{
			name:  "server error",
			setup: func(t *testing.T) { t.Helper(); scriptProtection(t, githubHost, "main", failing(ghtest.ServerError())) },
			named: []string{"HTTP 500", "ils branch"},
		},
		{
			name: "classic protection read fails",
			setup: func(t *testing.T) {
				t.Helper()
				scriptProtection(t, githubHost, "main", protection{rules: rulesAnswer(), branch: ghtest.ServerError()})
			},
			named: []string{"branch protection", "HTTP 500", "ils branch"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: the setup sets GH_PATH for the whole process.
			f, caller := standingOn(t, "main", "main")
			setGitHubPushURL(f, githubHost)
			tt.setup(t)

			checkRefusedNaming(t, caller, commitCommand, tt.named...)
		})
	}
}

func TestJudge_WhenTheGhConfigCannotBeTold_RefusesACommitOnTheTrunkNamingTheVariable(t *testing.T) {
	// Not parallel: onGitHub sets GH_PATH for the whole process.
	f, caller := standingOn(t, "main", "main")
	onGitHub(t, f, githubHost, unprotected())
	tests := []struct {
		name     string
		command  string
		variable string
	}{
		{name: "config dir not a literal", command: `GH_CONFIG_DIR="$d" ` + commitCommand, variable: "GH_CONFIG_DIR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkRefusedNaming(t, caller, tt.command, tt.variable, "ils branch")
		})
	}
}

func TestJudge_WhenOriginsSSHHostCannotBeResolved_RefusesACommitOnTheTrunk(t *testing.T) {
	// Not parallel: scriptProtection sets GH_PATH for the whole process.
	f, caller := standingOn(t, "main", "main")
	f.SetPushURL("git@github.com:" + originRepo + ".git")
	f.RemoveSSHConfig()
	scriptProtection(t, githubHost, "main", unprotected())

	checkRefusedNaming(t, caller, commitCommand, "ssh")
}

func TestJudge_WhenACommandCommitsTwiceOnTheTrunk_ReadsEachProtectionOnce(t *testing.T) {
	// Not parallel: onGitHub sets GH_PATH for the whole process.
	f, caller := standingOn(t, "main", "main")
	fake := onGitHub(t, f, githubHost, unprotected())
	const command = "git commit -m one; git commit -m two"

	got := guard.Judge(t.Context(), command, caller)

	calls := fake.Calls()
	assert.Check(t, is.Equal(got.Outcome, guard.Allow), "Judge(%q) outcome, reason %q", command, got.Reason)
	assert.Check(t, is.Equal(callsOf(calls, rulesArgs(githubHost, "main")), 1), "Judge(%q) gh calls %v", command, calls)
	assert.Check(t, is.Equal(callsOf(calls, branchArgs(githubHost, "main")), 1), "Judge(%q) gh calls %v", command, calls)
}

func TestJudge_WhenTheProtectionReadSucceeds_ChecksNoLogin(t *testing.T) {
	// Not parallel: onGitHub sets GH_PATH for the whole process.
	f, caller := standingOn(t, "main", "main")
	fake := onGitHub(t, f, githubHost, unprotected())

	got := guard.Judge(t.Context(), commitCommand, caller)

	assert.Check(t, is.DeepEqual(got, guard.Verdict{Outcome: guard.Allow}), "Judge(%q)", commitCommand)
	assert.Check(t, is.Equal(callsOf(fake.Calls(), ghtest.LoginArgs(githubHost)), 0), "Judge(%q) login checks", commitCommand)
}

func TestJudge_WhenNoInvocationIsOnTheTrunk_RunsNoGh(t *testing.T) {
	tests := []struct {
		arrange func(f *gittest.Fixture)
		name    string
		branch  string
	}{
		{name: "HEAD off the trunk", branch: "topic", arrange: func(*gittest.Fixture) {}},
		{name: "no origin", branch: "main", arrange: (*gittest.Fixture).RemoveOrigin},
		{name: "detached HEAD", branch: "main", arrange: (*gittest.Fixture).Detach},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: onGitHub sets GH_PATH for the whole process.
			f, caller := standingOn(t, "main", tt.branch)
			fake := onGitHub(t, f, githubHost, ruleProtected())
			tt.arrange(f)

			got := guard.Judge(t.Context(), commitCommand, caller)

			assert.Check(t, is.DeepEqual(got, guard.Verdict{Outcome: guard.Allow}), "Judge(%q)", commitCommand)
			assert.Check(t, is.Len(fake.Calls(), 0), "Judge(%q) gh calls", commitCommand)
		})
	}
}

// protectionRoutes routes the reads of whether main is protected in originRepo on github.com, and
// the login check there, to the files rules, branch and login.
func protectionRoutes() map[string][]string {
	return map[string][]string{
		"rules":  rulesArgs(githubHost, "main"),
		"branch": branchArgs(githubHost, "main"),
		"login":  ghtest.LoginArgs(githubHost),
	}
}

func TestJudge_ReadsTheProtectionWithTheGhConfigTheCommandSets(t *testing.T) {
	unprotectedConfig := map[string]ghtest.Answer{
		"rules": rulesAnswer(), "branch": classicAnswer(false), "login": ghtest.LoggedIn(),
	}
	tests := []struct {
		process map[string]ghtest.Answer
		command map[string]ghtest.Answer
		name    string
		named   string
	}{
		{
			name:    "the protection read",
			process: unprotectedConfig,
			command: map[string]ghtest.Answer{"rules": ruleProtected().rules, "login": ghtest.LoggedIn()},
			named:   protectedTrunk,
		},
		{
			name:    "the login check after a failed read",
			process: map[string]ghtest.Answer{"login": ghtest.LoggedIn()},
			command: map[string]ghtest.Answer{"login": ghtest.LoggedOut()},
			named:   "! gh auth login",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: ghReadingConfig sets GH_PATH and the gh config location for the whole process.
			f, caller := standingOn(t, "main", "main")
			setGitHubPushURL(f, githubHost)
			ghReadingConfig(t, protectionRoutes(), tt.process)
			command := "GH_CONFIG_DIR=" + gittest.ShellWord(configHolding(t, "", tt.command)) + " " + commitCommand

			checkRefusedNaming(t, caller, command, tt.named)
		})
	}
}

func TestJudge_WhenOriginsPushURLCannotBeReadOnTheTrunk_RefusesSayingWhatBroke(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f, _ := standingOn(t, "main", "main")
	caller := callerFailingOn(t, f, f.Dir(), "remote", "get-url", "--push")

	checkJudgedRow(t, caller, judged{command: commitCommand, wantNamed: gittest.FailedSaid, want: guard.Refuse})
}

func TestJudge_WhenOnlyTheFetchURLNamesAGitHubRepository_AllowsACommitOnTheTrunk(t *testing.T) {
	// Not parallel: scriptProtection sets GH_PATH for the whole process.
	f, _ := standingOn(t, "main", "main")
	f.SetOriginURL("git@github.com:" + originRepo + ".git")
	f.SetPushURL(filepath.ToSlash(f.OriginDir()))
	caller := callerWithEnv(f.Dir(), f.ServedEnv(f.OriginDir()))
	fake := scriptProtection(t, githubHost, "main", ruleProtected())

	got := guard.Judge(t.Context(), commitCommand, caller)

	assert.Check(t, is.DeepEqual(got, guard.Verdict{Outcome: guard.Allow}), "Judge(%q)", commitCommand)
	assert.Check(t, is.Len(fake.Calls(), 0), "Judge(%q) gh calls", commitCommand)
}

func TestJudge_WhenTheGitDirCannotBeTold_RefusesACommit(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "main")
	missing := gittest.ShellWord(filepath.Join(t.TempDir(), "missing"))

	checkJudgedRow(t, caller, judged{
		command: "git -C " + missing + " " + commitArgs, want: guard.Refuse, wantNamed: "cannot tell where the command runs",
	})
}
