package child_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/logtest"
)

// exitErrorOf runs cmd and returns the *exec.ExitError it ends with; what names the command in the
// failure message.
func exitErrorOf(t *testing.T, what string, cmd *exec.Cmd) *exec.ExitError {
	t.Helper()

	err := cmd.Run()
	exitErr, exited := errors.AsType[*exec.ExitError](err)
	assert.Assert(t, exited, "%s error = %v, want an *exec.ExitError", what, err)

	return exitErr
}

// nonZeroExit runs a go command that exits non-zero and returns what exec reports for it.
func nonZeroExit(t *testing.T) *exec.ExitError {
	t.Helper()

	return exitErrorOf(t, "go tool nonsense", exec.CommandContext(t.Context(), "go", "tool", "nonsense"))
}

// killedBy runs a shell that kills itself with signal, named as kill names it, and returns what
// exec reports for it.
func killedBy(t *testing.T, signal string) *exec.ExitError {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("Windows has no signal a process can kill itself with")
	}

	//nolint:gosec // G204: the test names the shell and the signal; nothing here comes from outside the test.
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "kill -"+signal+" $$")

	return exitErrorOf(t, "sh killed by "+signal, cmd)
}

// interruptedChild returns what exec reports for a child that Ctrl-C ended: on Windows a console
// child that exits with STATUS_CONTROL_C_EXIT, elsewhere a shell that the interrupt signal killed.
func interruptedChild(t *testing.T) *exec.ExitError {
	t.Helper()

	if runtime.GOOS != "windows" {
		return killedBy(t, "INT")
	}

	// -1073741510 is STATUS_CONTROL_C_EXIT, 0xC000013A, as cmd reads a signed 32-bit exit code.
	cmd := exec.CommandContext(t.Context(), "cmd", "/c", "exit", "-1073741510")

	return exitErrorOf(t, "cmd exiting with STATUS_CONTROL_C_EXIT", cmd)
}

func TestExitError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		stderr  string
		wantEnd string
	}{
		{name: "stderr is trimmed and appended", stderr: "  fatal: no such ref\n", wantEnd: "3: fatal: no such ref"},
		{name: "blank stderr is left out", stderr: " \n", wantEnd: "3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := &child.ExitError{Argv: []string{"git", "show-ref", "main"}, Code: 3, Stderr: tt.stderr}

			got := err.Error()

			assert.Check(t, is.Contains(got, "git show-ref main"))
			assert.Check(t, strings.HasSuffix(got, tt.wantEnd), "Error() = %q, want it to end with %q", got, tt.wantEnd)
		})
	}
}

func TestFailure_WhenProgramExitedNonZero_ReturnsExitError(t *testing.T) {
	t.Parallel()
	argv := []string{"go", "tool", "nonsense"}
	runErr := nonZeroExit(t)

	err := child.Failure(t.Context(), argv, runErr, "no such tool\n")

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Failure error = %v, want *child.ExitError", err)
	assert.Check(t, is.DeepEqual(exitErr.Argv, argv))
	assert.Check(t, is.Equal(exitErr.Stderr, "no such tool\n"))
	assert.Check(t, is.Equal(exitErr.Code, runErr.ExitCode()))
}

// assertNoStderr fails the test when err carries the stderr the Failure tests print before the end.
func assertNoStderr(t *testing.T, err error) {
	t.Helper()

	assert.Check(t, !strings.Contains(err.Error(), "secret"), "Failure error = %q, want no stderr in it", err)
}

func TestFailure_WhenContextEnded_ReturnsContextErrorWhateverTheExit(t *testing.T) {
	t.Parallel()
	runErr := nonZeroExit(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := child.Failure(ctx, []string{"go", "tool", "nonsense"}, runErr, "secret printed before the end\n")

	assert.Check(t, is.ErrorIs(err, context.Canceled))
	assert.Check(t, is.ErrorContains(err, "go tool nonsense"))
	assertNoStderr(t, err)
}

func TestFailure_WhenProgramDiedOfTheInterruptBeforeContextEnded_ReturnsContextCanceled(t *testing.T) {
	t.Parallel()
	runErr := interruptedChild(t)

	err := child.Failure(t.Context(), []string{"git", "fetch"}, runErr, "secret printed before the interrupt\n")

	assert.Check(t, is.ErrorIs(err, context.Canceled))
	assert.Check(t, is.ErrorContains(err, "git fetch"))
	assertNoStderr(t, err)
}

func TestFailure_WhenProgramDiedOfTerminateAndContextEndsWithinGrace_ReturnsContextError(t *testing.T) {
	t.Parallel()
	runErr := killedBy(t, "TERM")

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
		defer cancel()

		err := child.Failure(ctx, []string{"git", "fetch"}, runErr, "secret printed before the signal\n")

		assert.Check(t, is.ErrorIs(err, context.DeadlineExceeded))
		assert.Check(t, is.ErrorContains(err, "git fetch"))
		assertNoStderr(t, err)
	})
}

func TestFailure_WhenProgramDiedOfTerminateAndContextOutlivesGrace_ReturnsKilledErrorNamingCommand(t *testing.T) {
	t.Parallel()
	runErr := killedBy(t, "TERM")

	synctest.Test(t, func(t *testing.T) {
		err := child.Failure(t.Context(), []string{"git", "fetch"}, runErr, "")

		_, killed := errors.AsType[*child.KilledError](err)
		assert.Check(t, killed, "Failure error = %v, want *child.KilledError", err)
		assert.Check(t, is.ErrorContains(err, "git fetch"))
	})
}

func TestFailure_WhenProgramDidNotDieOfTerminate_ReturnsWithoutWaitingForContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		died func(*testing.T) *exec.ExitError
		name string
	}{
		{name: "non-zero exit", died: nonZeroExit},
		{name: "kill", died: func(t *testing.T) *exec.ExitError {
			t.Helper()

			return killedBy(t, "KILL")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runErr := tt.died(t)

			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
				defer cancel()

				err := child.Failure(ctx, []string{"git", "fetch"}, runErr, "")

				assert.Check(t, !errors.Is(err, context.DeadlineExceeded), "Failure error = %v, want one returned before the context's deadline", err)
			})
		})
	}
}

func TestInterruptedExit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code int64
	}{
		{name: "STATUS_CONTROL_C_EXIT of a console child that Ctrl-C ended", code: 0xC000013A},
		{name: "STATUS_CONTROL_C_EXIT as a 32-bit build reads it", code: -1073741510},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := child.InterruptedExit(int(tt.code))

			assert.Check(t, got, "InterruptedExit(%#x) = false, want true", tt.code)
		})
	}
}

func TestFailure_WhenProgramNeverExited_WrapsRunErrorNamingCommand(t *testing.T) {
	t.Parallel()

	err := child.Failure(t.Context(), []string{"gh", "pr", "view"}, exec.ErrNotFound, "")

	assert.Check(t, is.ErrorIs(err, exec.ErrNotFound))
	assert.Check(t, is.ErrorContains(err, "gh pr view"))
}

// printedBoth returns what a run printed on both outputs, for the tests of Ended.
func printedBoth() child.Output {
	return child.Output{Stdout: "partial answer\n", Stderr: "no such tool\n"}
}

func TestEnded_WhenRunSucceeded_ReturnsWhatItPrinted(t *testing.T) {
	t.Parallel()

	got, err := child.Ended(t.Context(), []string{"go", "version"}, nil, printedBoth())
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got, printedBoth()))
}

func TestEnded_WhenProgramExitedNonZero_KeepsOnlyStdout(t *testing.T) {
	t.Parallel()
	runErr := nonZeroExit(t)

	got, err := child.Ended(t.Context(), []string{"go", "tool", "nonsense"}, runErr, printedBoth())

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Ended error = %v, want *child.ExitError", err)
	assert.Check(t, is.Equal(exitErr.Stderr, printedBoth().Stderr))
	assert.Check(t, is.DeepEqual(got, child.Output{Stdout: printedBoth().Stdout}))
}

func TestEnded_WhenContextEndedAfterNonZeroExit_ReturnsNoOutput(t *testing.T) {
	t.Parallel()
	runErr := nonZeroExit(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got, err := child.Ended(ctx, []string{"go", "tool", "nonsense"}, runErr, printedBoth())

	assert.Check(t, is.ErrorIs(err, context.Canceled))
	assert.Check(t, is.DeepEqual(got, child.Output{}))
}

func TestExec_Log(t *testing.T) {
	t.Parallel()
	const took = 5 * time.Millisecond
	args := []string{"pr", "view"}
	tests := []struct {
		err  error
		want map[string]any
		name string
	}{
		{
			name: "run that exited zero",
			want: map[string]any{child.KeyTool: "gh", child.KeyArgs: args, child.KeyExit: int64(0), child.KeyDuration: took},
		},
		{
			name: "run that exited non-zero",
			err:  &child.ExitError{Argv: []string{"gh", "pr", "view"}, Code: 3, Stderr: "secret printed before the exit\n"},
			want: map[string]any{child.KeyTool: "gh", child.KeyArgs: args, child.KeyExit: int64(3), child.KeyDuration: took},
		},
		{
			name: "run that ended before an exit",
			err:  fmt.Errorf("gh pr view: %w", exec.ErrNotFound),
			want: map[string]any{
				child.KeyTool: "gh", child.KeyArgs: args, child.KeyError: "gh pr view: " + exec.ErrNotFound.Error(),
				child.KeyDuration: took,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			run := child.Exec{Tool: "gh", Args: args, Err: tt.err, Took: took}

			record := logtest.Only(t, logtest.RecordsOf(func(logger *slog.Logger) { run.Log(t.Context(), logger) }))

			assert.Check(t, is.DeepEqual(record.Map(), tt.want))
		})
	}
}

func TestExec_Log_WhenNoLogger_LogsNothingToDefaultLogger(t *testing.T) {
	// Not parallel: this replaces the process's default logger while it runs.
	logger, sink := logtest.New()
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })
	run := child.Exec{Tool: "gh", Args: []string{"pr", "view"}, Err: errors.New("failed"), Took: time.Millisecond}

	run.Log(t.Context(), nil)

	assert.Check(t, is.Len(sink.Records(), 0))
}

func TestToolTooOldError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		err   child.ToolTooOldError
		parts []string
	}{
		{
			name:  "version read",
			err:   child.ToolTooOldError{Tool: "gh", Version: "2.49.0", Line: "gh version 2.49.0 (2024-05-13)", Minimum: "2.50.0"},
			parts: []string{"gh 2.49.0", "2.50.0"},
		},
		{
			name:  "line with no version",
			err:   child.ToolTooOldError{Tool: "gh", Line: "gh version DEV", Minimum: "2.50.0"},
			parts: []string{"gh version DEV", "2.50.0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.err.Error()

			for _, part := range tt.parts {
				assert.Check(t, is.Contains(got, part))
			}
		})
	}
}

func TestToolMissingError_Error_WhenToolIsMissing_NamesWhereToInstallIt(t *testing.T) {
	t.Parallel()
	err := &child.ToolMissingError{Tool: "gh", InstallURL: "https://cli.github.com", Err: exec.ErrNotFound}

	got := err.Error()

	assert.Check(t, is.Contains(got, "gh"))
	assert.Check(t, is.Contains(got, "https://cli.github.com"))
}

// requirement is the gh requirement every Check and CheckOutput test is made against.
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
		{name: "a newer release", version: "2.60.0"},
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
		{name: "a development build", line: "gh version DEV", version: "DEV"},
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

// versionOf reads the version of the gh lines the output tests print, and none from any other line.
func versionOf(line string) string {
	versions := map[string]string{
		"gh version 2.49.0": "2.49.0",
		"gh version 2.50.0": "2.50.0",
		"gh version 2.60.0": "2.60.0",
	}

	return versions[line]
}

func TestRequirement_CheckOutput_WhenFirstLineIsTheMinimumOrNewer_ReturnsNil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		stdout string
	}{
		{name: "one line with no newline", stdout: "gh version 2.50.0"},
		{name: "a later line names an older version", stdout: "gh version 2.60.0\ngh version 2.49.0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := requirement().CheckOutput(tt.stdout, versionOf)

			assert.NilError(t, err, "CheckOutput(%q)", tt.stdout)
		})
	}
}

func TestRequirement_CheckOutput_WhenFirstLineIsOlder_ReturnsToolTooOldErrorForThatLine(t *testing.T) {
	t.Parallel()
	stdout := "gh version 2.49.0\ngh version 2.60.0\n"

	err := requirement().CheckOutput(stdout, versionOf)

	tooOld, ok := errors.AsType[*child.ToolTooOldError](err)
	assert.Assert(t, ok, "CheckOutput(%q) error = %v, want *child.ToolTooOldError", stdout, err)
	assert.Check(t, is.Equal(tooOld.Line, "gh version 2.49.0"))
	assert.Check(t, is.Equal(tooOld.Version, "2.49.0"))
}
