package child_test

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
)

// exitOf runs a go command that exits non-zero and returns what exec reports for it.
func exitOf(t *testing.T) *exec.ExitError {
	t.Helper()

	err := exec.CommandContext(t.Context(), "go", "tool", "nonsense").Run()
	exitErr, exited := errors.AsType[*exec.ExitError](err)
	assert.Assert(t, exited, "go tool nonsense error = %v, want an *exec.ExitError", err)

	return exitErr
}

func TestExitError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{name: "stderr is trimmed and appended", stderr: "  fatal: no such ref\n", want: "git show-ref main: exit status 3: fatal: no such ref"},
		{name: "blank stderr is left out", stderr: " \n", want: "git show-ref main: exit status 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := &child.ExitError{Argv: []string{"git", "show-ref", "main"}, Code: 3, Stderr: tt.stderr}

			got := err.Error()

			assert.Check(t, is.Equal(got, tt.want))
		})
	}
}

func TestFailure_WhenProgramExitedNonZero_ReturnsExitError(t *testing.T) {
	t.Parallel()
	argv := []string{"go", "tool", "nonsense"}
	runErr := exitOf(t)

	err := child.Failure(t.Context(), argv, runErr, "no such tool\n")

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Failure error = %v, want *child.ExitError", err)
	assert.Check(t, is.DeepEqual(exitErr.Argv, argv))
	assert.Check(t, is.Equal(exitErr.Stderr, "no such tool\n"))
	assert.Check(t, is.Equal(exitErr.Code, runErr.ExitCode()))
}

func TestFailure_WhenContextEnded_ReturnsContextErrorWhateverTheExit(t *testing.T) {
	t.Parallel()
	runErr := exitOf(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := child.Failure(ctx, []string{"go", "tool", "nonsense"}, runErr, "")

	assert.Check(t, is.ErrorIs(err, context.Canceled))
	_, exited := errors.AsType[*child.ExitError](err)
	assert.Check(t, !exited, "Failure error = %v, want no *child.ExitError once the context ended", err)
	assert.Check(t, is.Equal(err.Error(), "go tool nonsense: context canceled"))
}

func TestFailure_WhenProgramNeverExited_WrapsRunErrorNamingCommand(t *testing.T) {
	t.Parallel()

	err := child.Failure(t.Context(), []string{"gh", "pr", "view"}, exec.ErrNotFound, "")

	assert.Check(t, is.ErrorIs(err, exec.ErrNotFound))
	assert.Check(t, is.Equal(err.Error(), "gh pr view: "+exec.ErrNotFound.Error()))
}

func TestToolTooOldError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  child.ToolTooOldError
		want string
	}{
		{
			name: "version read",
			err:  child.ToolTooOldError{Tool: "gh", Version: "2.49.0", Line: "gh version 2.49.0 (2024-05-13)", Minimum: "2.50.0"},
			want: "gh 2.49.0 is older than 2.50.0",
		},
		{
			name: "line with no version",
			err:  child.ToolTooOldError{Tool: "gh", Line: "gh version DEV", Minimum: "2.50.0"},
			want: `gh printed "gh version DEV", which names no version; 2.50.0 or newer is needed`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.err.Error()

			assert.Check(t, is.Equal(got, tt.want))
		})
	}
}

func TestToolMissingError_WhenToolNotFound_NamesToolAndInstallPage(t *testing.T) {
	t.Parallel()
	err := &child.ToolMissingError{Tool: "gh", InstallURL: "https://cli.github.com", Err: exec.ErrNotFound}

	got := err.Error()

	assert.Check(t, is.Equal(got, "gh is not installed: install it from https://cli.github.com"))
}

func TestToolMissingError_WhenLookupFailed_MatchesTheLookupError(t *testing.T) {
	t.Parallel()
	err := &child.ToolMissingError{Tool: "git", InstallURL: "https://git-scm.com/downloads", Err: exec.ErrNotFound}

	assert.Check(t, is.ErrorIs(err, exec.ErrNotFound))
}

func requirement() child.Requirement {
	return child.Requirement{Tool: "gh", Minimum: "2.50.0", InstallURL: "https://cli.github.com"}
}

func TestRequirement_Check_WhenVersionIsTheMinimumOrNewer_ReturnsNil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		version string
	}{
		{name: "the minimum", version: "2.50.0"},
		{name: "a minor number with more digits", version: "2.100.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := requirement().Check("gh version "+tt.version, tt.version)

			assert.NilError(t, err, "Check(%q)", tt.version)
		})
	}
}

func TestRequirement_Check_WhenVersionIsOlderOrUnreadable_ReturnsToolTooOldError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		line    string
		version string
		// a version that is not semver carries none
		wantVersion string
	}{
		{name: "an older minor release", line: "gh version 2.49.0", version: "2.49.0", wantVersion: "2.49.0"},
		{name: "a release candidate of the minimum", line: "gh version 2.50.0-rc.1", version: "2.50.0-rc.1", wantVersion: "2.50.0-rc.1"},
		{name: "a development build", line: "gh version DEV", version: "DEV"},
		{name: "no version at all", line: "", version: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := requirement().Check(tt.line, tt.version)

			tooOld, ok := errors.AsType[*child.ToolTooOldError](err)
			assert.Assert(t, ok, "Check(%q) error = %v, want *child.ToolTooOldError", tt.version, err)
			want := child.ToolTooOldError{
				Tool: "gh", Version: tt.wantVersion, Line: tt.line, Minimum: "2.50.0", InstallURL: "https://cli.github.com",
			}
			assert.Check(t, is.DeepEqual(*tooOld, want))
		})
	}
}
