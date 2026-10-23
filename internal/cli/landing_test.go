package cli_test

import (
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// outdated is a first push with a gh that is logged out and prints line for its version.
func outdated(name, line string) scenario {
	calls := []ghtest.Call{
		ghtest.Says(line+"\n", ghtest.VersionArgs()...),
		login(ghtest.Answer{Stderr: "You are not logged into any GitHub hosts.\n", Code: 1}),
	}

	return scenario{name: name, script: only(calls...), want: exitRefused}
}

func TestApp_Run_WhenMergeRunsOnABranchWithNoPullRequest_RefusesNamingPush(t *testing.T) {
	s := newScene(t, noPullRequest())

	got := launch(t, s.app(), "merge")

	assert.Check(t, is.Equal(got.code, exitRefused))
	assert.Check(t, is.Equal(got.stdout,
		"Refused: the branch "+topic+" has no open pull request against the trunk "+trunk+"\nNext: ils push\n"))
}

func TestApp_Run_WhenPushEndsInAnyOutcome_JSONCarriesWhatTheTextShows(t *testing.T) {
	tests := []struct {
		outcome  string
		scenario scenario
	}{
		{outcome: "done", scenario: greenFirstPush()},
		{outcome: "refused", scenario: onTheTrunk()},
		{outcome: "broke", scenario: failedRead()},
		{outcome: "pending", scenario: pendingAtTheBound()},
		{outcome: "delivered-not-green", scenario: redWithLog()},
	}
	for _, tt := range tests {
		t.Run(tt.outcome, func(t *testing.T) {
			s := newScene(t, tt.scenario)

			text, encoded := both(t, s)

			assert.Check(t, is.Equal(text.code, tt.scenario.want))
			assert.Check(t, is.Equal(encoded.code, tt.scenario.want))

			got := resulttest.Decode(t, encoded.stdout)
			assert.Check(t, is.Equal(got.Outcome, tt.outcome))
			assert.Check(t, is.Equal(got.ExitCode, tt.scenario.want))

			resulttest.AssertCarried(t, text.stdout, got)
		})
	}
}

func TestApp_Run_WhenOriginNamesAnSSHAlias_AsksGhAboutTheHostItStandsFor(t *testing.T) {
	const alias = "github-personal"

	sc := greenFirstPush()
	sc.ssh = "Host " + alias + "\n  HostName " + host + "\n"
	sc.arrange = func(f *gittest.Fixture) { f.SetOriginURL("git@" + alias + ":" + owner + "/" + widgets + ".git") }
	s := newScene(t, sc)

	got := launch(t, s.app(), "push")

	assert.Check(t, is.Equal(got.code, exitDone), "ils push printed %q", got.stdout)
	assert.Check(t, is.Contains(s.fake.Calls(), append([]string{"gh"}, ghtest.LoginArgs(host)...)))
}

func TestApp_Run_WhenGhIsOlderThanTheMinimum_RefusesWithNothingPushedOrOpened(t *testing.T) {
	const (
		next   = "upgrade gh, then run ils push again"
		reason = "gh 1.0.0 is older than " + github.MinimumVersion +
			", the oldest version ils works with; upgrade it from https://cli.github.com"
	)

	s := newScene(t, outdated("an older release", "gh version 1.0.0"))

	text, encoded := both(t, s)

	want := ran{stdout: "Refused: " + reason + "\nNext: " + next + "\n", code: exitRefused}
	assert.Check(t, is.DeepEqual(text, want, sameRun()))
	assert.Check(t, is.Equal(encoded.code, exitRefused))
	got := resulttest.Decode(t, encoded.stdout)
	assert.Check(t, is.Equal(got.Outcome, "refused"))
	assert.Check(t, is.Equal(got.Reason, reason))
	assert.Check(t, is.Equal(got.Next, next))
	nothingPushed(t, s)
	assert.Check(t, !opensPullRequest(s), "gh was asked to create a pull request")
}

func TestApp_Run_WhenGhIsNotInstalled_RefusesNamingTheInstallPage(t *testing.T) {
	s := newScene(t, scenario{})
	t.Setenv("GH_PATH", filepath.Join(t.TempDir(), "gh"))

	text, encoded := both(t, s)

	want := ran{
		stdout: "Refused: gh is not installed; install it from https://cli.github.com\nNext: install gh, then run ils push again\n",
		code:   exitRefused,
	}
	assert.Check(t, is.DeepEqual(text, want, sameRun()))
	assert.Check(t, is.Equal(encoded.code, exitRefused))
	assert.Check(t, is.Equal(resulttest.Decode(t, encoded.stdout).Outcome, "refused"))
	nothingPushed(t, s)
}
