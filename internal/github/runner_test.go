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
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
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
		{name: "release without a date", line: "gh version 2.100.3", want: "2.100.3"},
		{name: "pre-release keeps its suffix", line: "gh version 2.50.0-rc.1", want: "2.50.0-rc.1"},
		{name: "development build", line: "gh version DEV"},
		{name: "no gh version prefix", line: "2.50.0"},
		{name: "empty line"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := github.VersionOf(tt.line)

			assert.Check(t, is.Equal(got, tt.want), "VersionOf(%q)", tt.line)
		})
	}
}

// missingTool is the error every call returns when `gh` is not installed.
func missingTool() *child.ToolMissingError {
	return &child.ToolMissingError{Tool: program, InstallURL: installURL}
}

// assertMissingTool fails the test unless err is the missingTool error, ignoring its cause, and
// returns that error.
func assertMissingTool(t *testing.T, err error, label string) *child.ToolMissingError {
	t.Helper()

	missing, ok := errors.AsType[*child.ToolMissingError](err)
	assert.Assert(t, ok, "%s error = %v, want *child.ToolMissingError", label, err)
	assert.Check(t, is.DeepEqual(missing, missingTool(), cmpopts.IgnoreFields(child.ToolMissingError{}, "Err")))

	return missing
}

func TestRunner_Run_WhenGhExitsZero_ReturnsOutput(t *testing.T) {
	args := []string{"pr", "view", "--json", "number"}
	ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "{\"number\":7}\n", Stderr: "notice\n"}, args...))

	got, err := github.Runner{}.Run(t.Context(), args...)
	assert.NilError(t, err)

	want := github.Result{Stdout: "{\"number\":7}\n", Stderr: "notice\n"}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestRunner_Run_WhenArgHoldsShellSyntax_PassesItVerbatim(t *testing.T) {
	value := "first && second; $HOME `third` > fourth"
	args := []string{"api", value}
	fake := ghtest.Setup(t, ghtest.Says("matched\n", args...))

	_, err := github.Runner{}.Run(t.Context(), args...)
	assert.NilError(t, err)

	assertCalls(t, fake, [][]string{{"gh", "api", value}})
}

func TestRunner_Run_WhenGhExitsNonZero_ReturnsExitErrorAndStdout(t *testing.T) {
	args := []string{"pr", "checks", "7"}
	ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "build\tpending\n", Stderr: "still running\n", Code: 8}, args...))

	got, err := github.Runner{}.Run(t.Context(), args...)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Run(pr checks) error = %v, want *child.ExitError", err)
	wantErr := child.ExitError{
		Argv:   []string{"gh", "pr", "checks", "7"},
		Stderr: "still running\n",
		Code:   8,
	}
	assert.Check(t, is.DeepEqual(*exitErr, wantErr))
	assert.Check(t, is.Equal(got.Stdout, "build\tpending\n"))
}

func TestRunner_Run_WhenContextEndsWhileGhRuns_ReturnsContextError(t *testing.T) {
	args := []string{"pr", "checks", "7"}
	ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "pass\n", Delay: 30 * time.Second}, args...))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	_, err := github.Runner{}.Run(ctx, args...)

	assert.Check(t, is.ErrorIs(err, context.DeadlineExceeded))
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

			missing := assertMissingTool(t, err, "Run(pr view)")
			assert.Check(t, missing.Err != nil, "Run(pr view) error carries no cause, want what starting gh failed with")
		})
	}
}
