package github_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/logtest"
)

// No test that scripts `gh` is parallel: the fake `gh` is found through `GH_PATH`,
// which `go-gh` reads from the environment of the whole process.

func TestVersionOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "release with a date", line: "gh version 2.50.0 (2024-05-29)", want: "2.50.0"},
		{name: "development build", line: "gh version DEV"},
		{name: "no gh version prefix", line: "2.50.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := github.VersionOf(tt.line)

			assert.Check(t, is.Equal(got, tt.want), "VersionOf(%q)", tt.line)
		})
	}
}

// withoutGh sets `GH_PATH` to ghPath and empties `PATH` for the rest of the test, so that no `gh`
// can be found.
func withoutGh(t *testing.T, ghPath string) {
	t.Helper()

	t.Setenv("GH_PATH", ghPath)
	t.Setenv("PATH", t.TempDir())
}

// pendingChecksArgs is the argv of a read that exits 8 printing a pending check.
func pendingChecksArgs() []string { return []string{"pr", "checks", "7"} }

// pendingChecks scripts the read of pendingChecksArgs.
func pendingChecks() ghtest.Call {
	return ghtest.Exits(ghtest.Answer{Stdout: "build\tpending\n", Stderr: "still running\n", Code: 8}, pendingChecksArgs()...)
}

func TestRunner_Run_WhenGhExitsNonZero_ReturnsExitErrorNamingCall(t *testing.T) {
	ghtest.Setup(t, pendingChecks())

	_, err := github.Runner{}.Run(t.Context(), pendingChecksArgs()...)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Run(pr checks) error = %v, want *child.ExitError", err)
	wantErr := child.ExitError{
		Argv:   []string{"gh", "pr", "checks", "7"},
		Stderr: "still running\n",
	}
	assert.Check(t, is.DeepEqual(*exitErr, wantErr, cmpopts.IgnoreFields(child.ExitError{}, "Code")))
}

func TestRunner_Run_WhenGhExitsNonZero_KeepsStdout(t *testing.T) {
	ghtest.Setup(t, pendingChecks())

	got, _ := github.Runner{}.Run(t.Context(), pendingChecksArgs()...)

	assert.Check(t, is.Equal(got.Stdout, "build\tpending\n"))
}

func TestRunner_Run_WhenContextIsCanceledWhileGhRuns_ReturnsContextError(t *testing.T) {
	args := []string{"pr", "checks", "7"}
	fake := ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "pass\n", Delay: 30 * time.Second}, args...))
	cancel, ran := fake.RunHeld(func(ctx context.Context) error {
		_, err := github.Runner{}.Run(ctx, args...)

		return err
	})

	cancel()
	err := <-ran

	assert.Check(t, is.ErrorIs(err, context.Canceled))
}

func TestRunner_Run_WhenGhMissing_ReturnsToolMissingError(t *testing.T) {
	tests := []struct {
		name   string
		ghPath string
	}{
		{name: "GH_PATH names no file", ghPath: filepath.Join(t.TempDir(), "gh")},
		{name: "no GH_PATH and no gh on PATH", ghPath: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withoutGh(t, tt.ghPath)

			_, err := github.Runner{}.Run(t.Context(), "pr", "view")

			missing, ok := errors.AsType[*child.ToolMissingError](err)
			assert.Assert(t, ok, "Run(pr view) error = %v, want *child.ToolMissingError", err)
			assert.Check(t, missing.Err != nil, "Run(pr view) error carries no cause, want what starting gh failed with")
		})
	}
}

func TestRunner_CheckLogin_WhenNotLoggedIn_ReturnsNotLoggedInError(t *testing.T) {
	ghtest.Setup(t, ghtest.Exits(ghtest.LoggedOut(), ghtest.LoginArgs(engineHost)...))

	err := github.Runner{}.CheckLogin(t.Context(), engineHost)

	notLoggedIn, ok := errors.AsType[*forge.NotLoggedInError](err)
	assert.Assert(t, ok, "CheckLogin error = %v, want *forge.NotLoggedInError", err)
	assert.Check(t, is.Equal(notLoggedIn.Host, engineHost))
	_, ok = errors.AsType[*child.ExitError](err)
	assert.Check(t, ok, "CheckLogin error = %v, want it to wrap the *child.ExitError", err)
}

func TestRunner_Run_WhenLoggerSet_LogsTheCall(t *testing.T) {
	args := pendingChecksArgs()
	ghtest.Setup(t, pendingChecks())
	logger, sink := logtest.New()

	_, err := github.Runner{Logger: logger}.Run(t.Context(), args...)

	record := logtest.Only(t, sink.Records())
	assert.Check(t, is.DeepEqual(record.Without(child.KeyDuration), logtest.ExecAttrs(t, child.Exec{Tool: "gh", Args: args, Err: err})))
}
