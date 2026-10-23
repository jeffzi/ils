package landing_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

// ---------------------------------------------------------------------------
// Missing ssh
// ---------------------------------------------------------------------------

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

			assert.Assert(t, stop != nil, "Preconditions() stop = nil, want a refusal")
			got := *stop
			assertStopped(t, got, result.Refused)

			text, _ := shown(t, got, s.gitDir)
			assert.Check(t, is.DeepEqual(text, tt.want))

			assertRefsUnchanged(t, s, before)
			assertAsked(t, s, nil)
		})
	}
}

// ---------------------------------------------------------------------------
// Failed and unfinished reads of the preconditions
// ---------------------------------------------------------------------------

func TestPreconditions_WhenForgeIdentityFailsUntyped_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, fails(errors.New("HTTP 502")), false)
	before := s.refs()

	_, stop := landing.Preconditions(t.Context(), s.deps, pushRequest())

	assert.Assert(t, stop != nil, "Preconditions() stop = nil, want a broke result")
	got := *stop
	assertBrokeRead(t, got, readIdentity, "HTTP 502")

	text, _ := shown(t, got, s.gitDir)
	assert.Check(t, is.DeepEqual(text, unreadText(pushCommand(), readIdentity, "HTTP 502")))

	assertRefsUnchanged(t, s, before)
	assertAsked(t, s, identityRead())

	assert.Check(t, !s.gitRan("ls-remote"), "origin was asked for its trunk after the identity read failed, want no later read")
}

func TestPreconditions_WhenDirectoryIsNotARepository_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()

	for _, cmd := range landingCommands() {
		t.Run(cmd.name, func(t *testing.T) {
			t.Parallel()
			deps := landing.Deps{
				Repo:  git.Repo{Runner: git.Runner{Dir: t.TempDir(), Env: gittest.Env(t)}},
				Forge: forgetest.New(t, forgetest.Script{}),
			}

			_, stop := landing.Preconditions(t.Context(), deps, cmd.req)

			assert.Assert(t, stop != nil, "Preconditions() stop = nil, want a broke result")
			got := *stop
			assertBrokeReadBy(t, got, cmd, readStanding)
		})
	}
}

// unfinished is a deadline that ends during one read of the preconditions.
type unfinished struct {
	// arrange returns the context to run under and the forge's script.
	arrange func(t *testing.T) (context.Context, forgetest.Script)
	name    string
	read    string
	// ops are the forge operations made before the refusal.
	ops []forgetest.Op
}

func unfinishedReads() []unfinished {
	return []unfinished{
		{
			name: "before the first read",
			read: readGitVersion,
			arrange: func(t *testing.T) (context.Context, forgetest.Script) {
				t.Helper()

				ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
				t.Cleanup(cancel)

				return ctx, forgetest.Script{}
			},
		},
		{
			name: "during the forge's identity read",
			read: readIdentity,
			ops:  identityRead(),
			arrange: func(t *testing.T) (context.Context, forgetest.Script) {
				t.Helper()

				return t.Context(), fails(fmt.Errorf("gh repo view: %w", context.DeadlineExceeded))
			},
		},
		{
			name: "between the identity read and the trunk read",
			read: readTrunk,
			ops:  identityRead(),
			arrange: func(t *testing.T) (context.Context, forgetest.Script) {
				t.Helper()

				ended := make(chan struct{})
				script := allowed()
				script.OnRead = func(forgetest.Call) { close(ended) }

				return gittest.Ending(t.Context(), ended, context.DeadlineExceeded), script
			},
		},
	}
}

func TestPreconditions_WhenDeadlineEndsDuringRead_RefusesNamingTheRead(t *testing.T) {
	t.Parallel()

	for _, cmd := range landingCommands() {
		for _, tt := range unfinishedReads() {
			t.Run(cmd.name+" "+tt.name, func(t *testing.T) {
				t.Parallel()
				ctx, script := tt.arrange(t)
				s := newScene(t, nil, script, false)
				before := s.refs()

				_, stop := landing.Preconditions(ctx, s.deps, cmd.req)

				assert.Assert(t, stop != nil, "Preconditions() stop = nil, want a refusal")
				got := *stop
				assertStoppedBy(t, got, result.Refused, cmd.req)

				text, _ := shown(t, got, s.gitDir)
				assert.Check(t, is.DeepEqual(text, overtimeText(cmd, tt.read)))

				assertRefsUnchanged(t, s, before)
				assertAsked(t, s, tt.ops)
			})
		}
	}
}
