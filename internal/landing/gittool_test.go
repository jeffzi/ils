package landing_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

// noGit returns the whole text of the refusal of a git that is missing from PATH, for the command
// named command.
func noGit(command string) string {
	return "Refused: git is not installed; install it from https://git-scm.com/downloads\n" +
		"Next: install git, then run ils " + command + " again\n"
}

// hideGit empties PATH for the whole process until the test ends, so that the next git call finds
// no git. It cannot run in a parallel test.
func hideGit(t *testing.T) {
	t.Helper()

	t.Setenv("PATH", t.TempDir())
}

// hidingGitAt returns script with a hook that hides git once the forge is asked for op, so that
// the git read after that call is the first to find no git.
func hidingGitAt(t *testing.T, script forgetest.Script, op forgetest.Op) forgetest.Script {
	t.Helper()

	script.OnRead = func(call forgetest.Call) {
		if call.Op == op {
			hideGit(t)
		}
	}

	return script
}

func TestPreconditions_WhenGitMissingFromPath_RefusesWithAnInstallHint(t *testing.T) {
	// Not parallel: each row empties PATH for the whole process while it runs.
	tests := []struct {
		// arrange returns the forge's script, having hidden git or set the script up to.
		arrange func(t *testing.T) forgetest.Script
		name    string
		ops     []forgetest.Op
	}{
		{
			name: "at the first read",
			arrange: func(t *testing.T) forgetest.Script {
				t.Helper()
				hideGit(t)

				return forgetest.Script{}
			},
		},
		{
			name: "at the read of origin's trunk", ops: identityRead(),
			arrange: func(t *testing.T) forgetest.Script {
				t.Helper()

				return hidingGitAt(t, allowed(), forgetest.OpIdentity)
			},
		},
	}
	for _, cmd := range landingCommands() {
		for _, tt := range tests {
			t.Run(cmd.name+" "+tt.name, func(t *testing.T) {
				s := newScene(t, nil, forgetest.Script{}, false)
				s.useForge(t, tt.arrange(t))

				_, stop := landing.Preconditions(t.Context(), s.deps, cmd.req)

				assert.Assert(t, stop != nil, "Preconditions() stop = nil, want a refusal")
				got := *stop
				assertStoppedBy(t, got, result.Refused, cmd.req)

				text, _ := shown(t, got, s.gitDir)
				assert.Check(t, is.DeepEqual(text, noGit(cmd.name)))

				assertAsked(t, s, tt.ops)
			})
		}
	}
}

func TestPreconditions_WhenGitIsTooOld_RefusesBeforeAnyOtherRead(t *testing.T) {
	// Not parallel: fakeGit puts a git that prints its own version first on PATH with t.Setenv.
	const tooOld = "Refused: git 1.0.0 is older than " + git.MinimumVersion + ", the oldest version ils works with; " +
		"upgrade it from https://git-scm.com/downloads\n"

	for _, cmd := range landingCommands() {
		t.Run(cmd.name, func(t *testing.T) {
			s := newScene(t, nil, forgetest.Script{}, false)
			fakeGit(t, s, gittest.Fake{Version: "git version 1.0.0"})
			before := s.refs()

			_, stop := landing.Preconditions(t.Context(), s.deps, cmd.req)

			assert.Assert(t, stop != nil, "Preconditions() stop = nil, want a refusal")
			got := *stop
			assertStoppedBy(t, got, result.Refused, cmd.req)

			text, _ := shown(t, got, s.gitDir)
			assert.Check(t, is.DeepEqual(text, tooOld+"Next: upgrade git, then run ils "+cmd.name+" again\n"))

			assertRefsUnchanged(t, s, before)
			assertAsked(t, s, nil)

			assert.Check(t, is.DeepEqual(s.recorder.Calls(), [][]string{{"git", "--version"}}))
		})
	}
}

func TestPreconditions_WhenGitVersionFails_BrokeNamingTheRead(t *testing.T) {
	// Not parallel: failGit puts a git that fails first on PATH with t.Setenv.
	s := newScene(t, nil, forgetest.Script{}, false)
	failGit(t, s, []string{"--version"})

	_, stop := landing.Preconditions(t.Context(), s.deps, pushRequest())

	assert.Assert(t, stop != nil, "Preconditions() stop = nil, want a broke result")
	got := *stop
	assertBrokeRead(t, got, readGitVersion, gittest.FailedSaid)
	assertAsked(t, s, nil)
}
