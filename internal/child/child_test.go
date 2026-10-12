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
