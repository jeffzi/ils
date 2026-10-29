package cli_test

import (
	"fmt"
	"slices"
	"testing"

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

	// runID is the workflow run that stands behind a check, and runLink is that check's link.
	runID   = "9001"
	runLink = "https://" + slug + "/actions/runs/" + runID + "/job/42"
)

// pullURL is the address of the open pull request.
func pullURL() string { return fmt.Sprintf("https://%s/pull/%d", slug, opened) }

// scenario is one state of the repository and of GitHub that a landing command is run in.
type scenario struct {
	// script returns what gh answers, read from the repository on the topic branch with one commit.
	script func(f *gittest.Fixture) []ghtest.Call
}

// scene is one repository and one scripted gh, with a record of what git was asked.
type scene struct {
	fixture  *gittest.Fixture
	recorder *git.Recorder
	fake     *ghtest.Fake
	// env reaches origin, whose URL names GitHub over ssh, through the stand-in for ssh.
	env []string
}

// newScene returns a repository on the topic branch whose origin names acme/widgets on GitHub,
// and a gh that answers from sc's script.
func newScene(t *testing.T, sc scenario) *scene {
	t.Helper()

	f, env := gittest.NewServed(t, trunk, topic, originURL)

	var script []ghtest.Call
	if sc.script != nil {
		script = sc.script(f)
	}

	return &scene{
		fixture:  f,
		recorder: &git.Recorder{},
		fake:     ghtest.Setup(t, script...),
		env:      env,
	}
}

// app returns the cli over the scene's repository.
func (s *scene) app() cli.App {
	return over(git.Runner{Dir: s.fixture.Dir(), Env: s.env, Recorder: s.recorder})
}

func tipOf(f *gittest.Fixture) string {
	return f.Git("rev-parse", "HEAD")
}

// allowed scripts a logged-in gh and a repository the user can push to.
func allowed() []ghtest.Call {
	return []ghtest.Call{
		ghtest.Exits(ghtest.Answer{Stdout: "Logged in\n"}, ghtest.LoginArgs(host)...),
		ghtest.Says(ghtest.RepoViewJSON(false, "WRITE"), ghtest.RepoViewArgs(slug)...),
	}
}

// found scripts body as the list of the topic branch's pull requests.
func found(body string) []ghtest.Call {
	return append(allowed(), ghtest.Exits(ghtest.Answer{Stdout: body}, ghtest.ListArgs(slug, topic)...))
}

// greenFirstPush scripts a branch with no pull request yet that GitHub then shows mergeable,
// its one check passed, on a trunk with no rule.
func greenFirstPush() scenario {
	script := func(f *gittest.Fixture) []ghtest.Call {
		sha := tipOf(f)
		run := ghtest.JSONArray(ghtest.RunJSON("completed", "Tidy"))
		checks := ghtest.JSONArray(ghtest.CheckJSON("build", "pass", runLink))

		return slices.Concat(found("[]"), []ghtest.Call{
			ghtest.Says(pullURL()+"\n", ghtest.CreateArgs(slug, topic, trunk, topic)...),
			ghtest.Says("[]", ghtest.RulesArgs(host, owner+"/"+widgets, trunk)...),
			ghtest.Says(ghtest.HeadJSON(sha, "MERGEABLE", "CLEAN", ""), ghtest.HeadArgs(slug, opened)...),
			ghtest.Says(checks, ghtest.ChecksArgs(slug, opened)...),
			ghtest.Says(run, ghtest.RunsArgs(slug, sha)...),
		})
	}

	return scenario{script: script}
}
