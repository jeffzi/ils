package github_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
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

func pendingChecksArgs() []string { return []string{"pr", "checks", "7"} }

func pendingChecks() ghtest.Call {
	return ghtest.Exits(ghtest.Answer{Stdout: "build\tpending\n", Stderr: "still running\n", Code: 8}, pendingChecksArgs()...)
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

func TestRunner_Run_WhenEnvGiven_RunsGhWithIt(t *testing.T) {
	// The fake reads its script from the environment: the environment captured after the first
	// Setup holds the script that answers "given", and the process now holds the one that answers
	// "process".
	ghtest.Setup(t, ghtest.Says("given\n", "alias", "list"))
	given := os.Environ()
	ghtest.Setup(t, ghtest.Says("process\n", "alias", "list"))

	got, err := github.Runner{Env: given}.Run(t.Context(), "alias", "list")
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got.Stdout, "given\n"))
}

func TestRunner_Run_WhenLoggerSet_LogsTheCall(t *testing.T) {
	args := pendingChecksArgs()
	ghtest.Setup(t, pendingChecks())
	logger, sink := logtest.New()

	_, err := github.Runner{Logger: logger}.Run(t.Context(), args...)

	assert.Check(t, is.DeepEqual(logtest.CallAttrs(t, sink.Records()), logtest.ExecAttrs(t, child.Exec{Tool: "gh", Args: args, Err: err})))
}

// prompt bounds how long a test waits for a call that must return soon after gh exited, and for
// gh's lingering copy to connect.
const prompt = 10 * time.Second

// runBounded runs args through runner and fails the test when the call has not returned within
// prompt, so a call that waits on a descendant's pipes fails instead of hanging until the test ends.
func runBounded(t *testing.T, runner github.Runner, args ...string) (github.Result, error) {
	t.Helper()

	type ran struct {
		err error
		out github.Result
	}

	done := make(chan ran, 1)

	go func() {
		out, err := runner.Run(t.Context(), args...)
		done <- ran{out: out, err: err}
	}()

	select {
	case got := <-done:
		return got.out, got.err
	case <-time.After(prompt):
		t.Fatalf("Run(%q) did not return within %v", args, prompt)

		return github.Result{}, nil
	}
}

// withSpawnerGh makes gh, for the rest of the test, the test binary acting as a spawner that prints
// said and leaves a copy of itself holding its output, which connects to addr once started.
func withSpawnerGh(t *testing.T, addr, said string) {
	t.Helper()

	self, err := os.Executable()
	assert.NilError(t, err)
	t.Setenv("GH_PATH", self)

	for _, entry := range gittest.Spawner(addr, said) {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
}

func TestRunner_Run_WhenGhExitsZeroWhileDescendantHoldsStdout_ReturnsWhatItPrinted(t *testing.T) {
	addr, lingering := gittest.SilentListener(t)
	said := "gh version " + github.MinimumVersion
	withSpawnerGh(t, addr, said)

	got, err := runBounded(t, github.Runner{}, "pr", "view")
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got.Stdout, said+"\n"))
	select {
	case <-lingering:
	case <-time.After(prompt):
		t.Error("gh's lingering copy never connected, so nothing held the pipes open")
	}
}
