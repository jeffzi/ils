package cli_test

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
)

// No test that builds a scene is parallel: the fake `gh` is found through `GH_PATH`, which
// `go-gh` reads from the environment of the whole process.

const (
	trunk     = "develop"
	topic     = "tidy-readme"
	host      = "github.com"
	owner     = "acme"
	widgets   = "widgets"
	slug      = host + "/" + owner + "/" + widgets
	originURL = "ssh://git@" + slug + ".git"

	// opened is the number of the open pull request.
	opened = 12

	pullURL = "https://" + slug + "/pull/12"
	// runID is the workflow run that stands behind a check, and runLink is that check's link.
	runID   = "9001"
	runLink = "https://" + slug + "/actions/runs/" + runID + "/job/42"

	// shortLeft is less than one poll of the checks, so that a wait for running checks ends
	// as pending right after its first read.
	shortLeft = 4900 * time.Millisecond
)

// scenario is one state of the repository and of GitHub that a landing command is run in.
type scenario struct {
	// arrange changes the repository, which stands on the topic branch with one commit.
	arrange func(f *gittest.Fixture)
	// script returns what gh answers, read once arrange has run.
	script func(f *gittest.Fixture) []ghtest.Call
	name   string
	// ssh is the ssh config ils reads; empty names no host.
	ssh string
	// left is the time left under the bound at launch; zero leaves the whole bound.
	left time.Duration
	want int
}

// scene is one repository and one scripted gh, with a record of what git was asked.
type scene struct {
	fixture  *gittest.Fixture
	recorder *git.Recorder
	fake     *ghtest.Fake
	// env reaches origin, whose URL names GitHub over ssh, through the stand-in for ssh.
	env  []string
	left time.Duration
}

// newScene returns a repository on the topic branch whose origin names acme/widgets on GitHub,
// once sc has arranged it, and a gh that answers from sc's script.
func newScene(t *testing.T, sc scenario) *scene {
	t.Helper()

	f, env := gittest.NewServed(t, trunk, topic, originURL)
	f.SetSSHConfig(sc.ssh)

	if sc.arrange != nil {
		sc.arrange(f)
	}

	var script []ghtest.Call
	if sc.script != nil {
		script = sc.script(f)
	}

	return &scene{
		fixture:  f,
		recorder: &git.Recorder{},
		fake:     ghtest.Setup(t, script...),
		env:      env,
		left:     sc.left,
	}
}

// app returns the cli over the scene's repository, launched so that the scene's time is left.
func (s *scene) app() cli.App {
	app := over(git.Runner{Dir: s.fixture.Dir(), Env: s.env, Recorder: s.recorder})

	if s.left > 0 {
		app.Launch = time.Now().Add(s.left - bound)
	}

	return app
}

func tipOf(f *gittest.Fixture) string {
	return f.Git("rev-parse", "HEAD")
}

func login(answer ghtest.Answer) ghtest.Call {
	return ghtest.Exits(answer, ghtest.LoginArgs(host)...)
}

// identity scripts a logged-in gh and a repository that is a fork or not, with permission.
func identity(fork bool, permission string) []ghtest.Call {
	return []ghtest.Call{
		login(ghtest.Answer{Stdout: "Logged in\n"}),
		ghtest.Says(ghtest.RepoViewJSON(fork, permission), ghtest.RepoViewArgs(slug)...),
	}
}

// allowed scripts a logged-in gh and a repository the user can push to.
func allowed() []ghtest.Call {
	return identity(false, "WRITE")
}

// listed scripts the answer to the list of the topic branch's pull requests in state.
func listed(state, body string) ghtest.Call {
	return ghtest.Exits(ghtest.Answer{Stdout: body}, ghtest.ListArgs(slug, topic, state)...)
}

// changeJSON is one pull request of the topic branch as `gh pr list` prints it.
func changeJSON(number int, title, base, sha string) string {
	url := "https://" + slug + "/pull/" + strconv.Itoa(number)

	return "[" + ghtest.ChangeJSON(ghtest.Change{Number: number, URL: url, Title: title, Base: base, SHA: sha}) + "]"
}

// found scripts the pull requests of the topic branch: the open ones, then the merged ones.
func found(open, landed string) []ghtest.Call {
	return append(allowed(), listed("open", open), listed("merged", landed))
}

// headJSON is `gh pr view`'s answer for the pull request at sha, with no review decision.
func headJSON(sha, mergeable, status string) string {
	return ghtest.HeadJSON(sha, mergeable, status, "")
}

// checkJSON is `gh pr checks`'s answer with one check of workflow run 9001.
func checkJSON(name, bucket string) string {
	return "[" + ghtest.CheckJSON(name, bucket, runLink) + "]"
}

// verdict is what GitHub says of pull request 12 once it is open.
type verdict struct {
	// rules is the answer to the read of the trunk's rules.
	rules string
	// head is the answer to `gh pr view`.
	head string
	// run is the status of the one workflow run on the commit.
	run string
	// checks is the answer to `gh pr checks`.
	checks ghtest.Answer
}

// passing is a mergeable pull request at sha whose one check passed, on a trunk with no rule.
func passing(sha string) verdict {
	return verdict{
		rules: "[]", head: headJSON(sha, "MERGEABLE", "CLEAN"), run: "completed",
		checks: ghtest.Answer{Stdout: checkJSON("build", "pass")},
	}
}

// calls scripts the reads of v for the commit sha.
func (v verdict) calls(sha string) []ghtest.Call {
	run := "[" + ghtest.RunJSON(v.run, "Tidy") + "]"

	return []ghtest.Call{
		ghtest.Says(v.rules, ghtest.RulesArgs(host, owner+"/"+widgets, trunk)...),
		ghtest.Says(v.head, ghtest.HeadArgs(slug, opened)...),
		ghtest.Exits(v.checks, ghtest.ChecksArgs(slug, opened)...),
		ghtest.Says(run, ghtest.RunsArgs(slug, sha)...),
		ghtest.Says("build\tRun tests\tFAIL\n", ghtest.LogArgs(slug, runID)...),
	}
}

// firstPush scripts a branch with no pull request yet, and then the verdict judge gives.
func firstPush(judge func(sha string) verdict) func(*gittest.Fixture) []ghtest.Call {
	return func(f *gittest.Fixture) []ghtest.Call {
		sha := tipOf(f)
		opening := ghtest.Says(pullURL+"\n", ghtest.CreateArgs(slug, topic, trunk, topic)...)

		return slices.Concat(found("[]", "[]"), []ghtest.Call{opening}, judge(sha).calls(sha))
	}
}

// laterPush scripts a branch whose pull request 12 is open at its tip, and then the verdict
// judge gives.
func laterPush(judge func(sha string) verdict) func(*gittest.Fixture) []ghtest.Call {
	return func(f *gittest.Fixture) []ghtest.Call {
		sha := tipOf(f)

		return slices.Concat(found(changeJSON(opened, topic, trunk, sha), "[]"), judge(sha).calls(sha))
	}
}

// only scripts the same answers whatever the repository holds.
func only(calls ...ghtest.Call) func(*gittest.Fixture) []ghtest.Call {
	return func(*gittest.Fixture) []ghtest.Call { return calls }
}

func greenFirstPush() scenario {
	return scenario{name: "green on a first push", script: firstPush(passing), want: exitDone}
}

func redWithLog() scenario {
	red := func(sha string) verdict {
		v := passing(sha)
		v.head = headJSON(sha, "MERGEABLE", "UNSTABLE")
		v.checks = ghtest.Answer{Stdout: checkJSON("build", "fail"), Code: 1}

		return v
	}

	return scenario{name: "red with a failed log", script: firstPush(red), want: exitDelivered}
}

func pendingAtTheBound() scenario {
	running := func(sha string) verdict {
		v := passing(sha)
		v.run = "in_progress"
		v.checks = ghtest.Answer{Stdout: checkJSON("build", "pending"), Code: 8}

		return v
	}

	return scenario{
		name: "checks still running at the bound", script: laterPush(running), left: shortLeft, want: exitPending,
		arrange: func(f *gittest.Fixture) { f.Publish(topic) },
	}
}

func noPullRequest() scenario {
	return scenario{name: "no pull request", script: only(found("[]", "[]")...), want: exitRefused}
}
