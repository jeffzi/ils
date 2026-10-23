package landing_test

import (
	"context"
	"errors"
	"fmt"
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

func TestPreconditions_WhenForgeIdentityFailsUntyped_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, fails(errors.New("HTTP 502")), false)
	before := s.refs()

	_, stop := landing.Preconditions(t.Context(), s.deps, pushRequest())

	got := stopped(t, stop, "a broke result")
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

			got := stopped(t, stop, "a broke result")
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

				got := stopped(t, stop, "a refusal")
				assertStoppedBy(t, got, result.Refused, cmd.req)

				text, _ := shown(t, got, s.gitDir)
				assert.Check(t, is.DeepEqual(text, overtimeText(cmd, tt.read)))

				assertRefsUnchanged(t, s, before)
				assertAsked(t, s, tt.ops)
			})
		}
	}
}
