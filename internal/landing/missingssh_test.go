package landing_test

import (
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

// httpsURL names Acme/Widgets over HTTPS, which git reads without asking ssh.
const httpsURL = "https://github.com/acme/widgets.git"

// missingProgram is an ssh command that no PATH holds.
const missingProgram = "no-such-ssh"

// sshCommandVar is the environment variable the fixture sets to an ssh command of its own.
const sshCommandVar = "GIT_SSH_COMMAND"

// without returns a copy of env with every entry of key removed.
func without(env []string, key string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")

		return strings.EqualFold(name, key)
	})
}

// commandMissing is the whole text of the refusal of an ssh command, configured by setting, that
// runs missingProgram.
func commandMissing(setting string) string {
	return "Refused: " + missingProgram + ", the ssh command " + setting + " names, is not found\n" +
		"Next: fix " + setting + ", then run ils push again\n"
}

// sshMissing is a refusal of a push whose ssh command, or ssh itself, is not installed.
type sshMissing struct {
	arrange func(*gittest.Fixture)
	// env receives the scene's runner environment and returns the one the runner is given. A row
	// removes or replaces GIT_SSH_COMMAND or PATH in it to set up the case.
	env  func(t *testing.T, env []string) []string
	name string
	want string
}

// sshMissingRefusals are the refusals of a push whose ssh command, or ssh itself, is not installed.
func sshMissingRefusals() []sshMissing {
	return []sshMissing{
		{
			name:    "GIT_SSH_COMMAND runs a missing program for the push URL",
			arrange: func(f *gittest.Fixture) { f.SetOriginURL(httpsURL) },
			env: func(_ *testing.T, env []string) []string {
				return append(without(env, sshCommandVar), sshCommandVar+"="+missingProgram)
			},
			want: commandMissing(sshCommandVar),
		},
		{
			name: "core.sshCommand runs a missing program for the fetch URL",
			arrange: func(f *gittest.Fixture) {
				f.SetPushURL(httpsURL)
				f.Git("config", "--local", "core.sshCommand", missingProgram)
			},
			env: func(_ *testing.T, env []string) []string {
				return without(env, sshCommandVar)
			},
			want: commandMissing("core.sshCommand"),
		},
		{
			name: "GIT_SSH runs a missing program for both URLs",
			env: func(_ *testing.T, env []string) []string {
				return append(without(env, sshCommandVar), "GIT_SSH="+missingProgram)
			},
			want: commandMissing("GIT_SSH"),
		},
		{
			name: "plain ssh is not installed",
			env: func(t *testing.T, env []string) []string {
				t.Helper()

				return append(without(without(env, sshCommandVar), "PATH"), "PATH="+t.TempDir())
			},
			want: "Refused: ssh is not installed; install it from https://www.openssh.com\n" +
				"Next: install ssh, then run ils push again\n",
		},
	}
}

func TestPreconditions_WhenSSHIsMissing_RefusesNamingWhatToFix(t *testing.T) {
	t.Parallel()
	tests := sshMissingRefusals()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, tt.arrange, forgetest.Script{}, false)
			s.deps.Repo.Runner.Env = tt.env(t, s.deps.Repo.Runner.Env)
			before := s.refs()

			_, stop := landing.Preconditions(t.Context(), s.deps, pushRequest())

			got := stopped(t, stop, "a refusal")
			assertStopped(t, got, result.Refused)

			text, _ := shown(t, got, s.gitDir)
			assert.Check(t, is.DeepEqual(text, tt.want))

			assertRefsUnchanged(t, s, before)
			assertAsked(t, s, nil)
		})
	}
}
