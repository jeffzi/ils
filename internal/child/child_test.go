package child_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
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

// exitOf runs a go command that exits non-zero and returns what exec reports for it.
func exitOf(t *testing.T) *exec.ExitError {
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

	err := child.Failure(ctx, []string{"go", "tool", "nonsense"}, runErr, "secret printed before the end\n")

	assert.Check(t, is.ErrorIs(err, context.Canceled))
	_, exited := errors.AsType[*child.ExitError](err)
	assert.Check(t, !exited, "Failure error = %v, want no *child.ExitError once the context ended", err)
	assert.Check(t, is.Equal(err.Error(), "go tool nonsense: context canceled"))
}

func TestFailure_WhenProgramDiedOfTheInterruptBeforeContextEnded_ReturnsContextCanceled(t *testing.T) {
	t.Parallel()
	runErr := interruptedChild(t)

	err := child.Failure(t.Context(), []string{"git", "fetch"}, runErr, "secret printed before the interrupt\n")

	assert.Check(t, is.ErrorIs(err, context.Canceled))
	assert.Check(t, is.Equal(err.Error(), "git fetch: context canceled"))
}

func TestFailure_WhenProgramDiedOfTerminateAndContextEndsWithinGrace_ReturnsContextError(t *testing.T) {
	t.Parallel()
	runErr := killedBy(t, "TERM")
	tests := []struct {
		end  func(context.Context) (context.Context, context.CancelFunc)
		want error
		name string
	}{
		{
			name: "canceled",
			end: func(ctx context.Context) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(ctx)
				time.AfterFunc(time.Second, cancel)

				return ctx, cancel
			},
			want: context.Canceled,
		},
		{
			name: "past its deadline",
			end: func(ctx context.Context) (context.Context, context.CancelFunc) {
				return context.WithTimeout(ctx, time.Second)
			},
			want: context.DeadlineExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := tt.end(t.Context())
				defer cancel()

				err := child.FailureWithin(time.Minute)(ctx, []string{"git", "fetch"}, runErr, "secret printed before the signal\n")

				assert.Check(t, is.ErrorIs(err, tt.want))
				assert.Check(t, is.Equal(err.Error(), "git fetch: "+tt.want.Error()))
			})
		})
	}
}

func TestFailure_WhenProgramDiedOfTerminateAndContextOutlivesGrace_WrapsRunErrorNamingCommand(t *testing.T) {
	t.Parallel()
	runErr := killedBy(t, "TERM")

	synctest.Test(t, func(t *testing.T) {
		err := child.FailureWithin(time.Second)(t.Context(), []string{"git", "fetch"}, runErr, "")

		assert.Check(t, is.Equal(err.Error(), "git fetch: "+runErr.Error()))
	})
}

func TestFailure_WhenProgramDidNotDieOfTerminate_ReturnsWithoutWaitingForContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		died func(*testing.T) *exec.ExitError
		name string
	}{
		{name: "non-zero exit", died: exitOf},
		{name: "interrupt", died: interruptedChild},
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
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()

				err := child.FailureWithin(time.Minute)(ctx, []string{"git", "fetch"}, runErr, "")

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
		want bool
	}{
		{name: "STATUS_CONTROL_C_EXIT of a console child that Ctrl-C ended", code: 0xC000013A, want: true},
		{name: "STATUS_CONTROL_C_EXIT as a 32-bit build reads it", code: -1073741510, want: true},
		{name: "an ordinary failure", code: 1, want: false},
		{name: "the code a shell reports for a child the interrupt killed", code: 130, want: false},
		{name: "a child a signal killed, which reports no code", code: -1, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := child.InterruptedExit(int(tt.code))

			assert.Check(t, is.Equal(got, tt.want), "InterruptedExit(%#x)", tt.code)
		})
	}
}

func TestFailure_WhenProgramDiedOfAnotherSignal_WrapsRunErrorNamingCommand(t *testing.T) {
	t.Parallel()
	runErr := killedBy(t, "TERM")

	err := child.Failure(t.Context(), []string{"git", "fetch"}, runErr, "")

	assert.Check(t, is.Equal(err.Error(), "git fetch: "+runErr.Error()))
}

func TestFailure_WhenProgramNeverExited_WrapsRunErrorNamingCommand(t *testing.T) {
	t.Parallel()

	err := child.Failure(t.Context(), []string{"gh", "pr", "view"}, exec.ErrNotFound, "")

	assert.Check(t, is.ErrorIs(err, exec.ErrNotFound))
	assert.Check(t, is.Equal(err.Error(), "gh pr view: "+exec.ErrNotFound.Error()))
}

func TestExec_Log(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		want map[string]any
		name string
	}{
		{
			name: "run that exited zero",
			want: map[string]any{"tool": "gh", "args": []string{"pr", "view"}, "exit": int64(0)},
		},
		{
			name: "run that exited non-zero",
			err:  &child.ExitError{Argv: []string{"gh", "pr", "view"}, Code: 3, Stderr: "secret printed before the exit\n"},
			want: map[string]any{"tool": "gh", "args": []string{"pr", "view"}, "exit": int64(3)},
		},
		{
			name: "run that ended before an exit",
			err:  fmt.Errorf("gh pr view: %w", exec.ErrNotFound),
			want: map[string]any{"tool": "gh", "args": []string{"pr", "view"}, "error": "gh pr view: " + exec.ErrNotFound.Error()},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger, sink := logtest.New()
			run := child.Exec{Tool: "gh", Args: []string{"pr", "view"}, Err: tt.err, Took: 5 * time.Millisecond}

			run.Log(t.Context(), logger)

			records := sink.Records()
			assert.Assert(t, is.Len(records, 1))
			record := records[0]
			assert.Check(t, is.Equal(record.Message, "exec"))
			assert.Check(t, is.Equal(record.Level, slog.LevelDebug))
			got := record.Map()
			assert.Check(t, is.Equal(got["duration"], 5*time.Millisecond))
			delete(got, "duration")
			assert.Check(t, is.DeepEqual(got, tt.want))
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

func TestToolMissingError_Error_NamesToolAndInstallPage(t *testing.T) {
	t.Parallel()
	err := &child.ToolMissingError{Tool: "gh", InstallURL: "https://cli.github.com", Err: exec.ErrNotFound}

	got := err.Error()

	assert.Check(t, is.Equal(got, "gh is not installed: install it from https://cli.github.com"))
}

func TestToolMissingError_Unwrap_WhenLookupFailed_MatchesTheLookupError(t *testing.T) {
	t.Parallel()
	err := &child.ToolMissingError{Tool: "git", InstallURL: "https://git-scm.com/downloads", Err: exec.ErrNotFound}

	assert.Check(t, is.ErrorIs(err, exec.ErrNotFound))
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
		{name: "later lines name no version", stdout: "gh version 2.50.0\nhttps://github.com/cli/cli/releases/tag/v2.50.0\n"},
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

func TestRequirement_CheckOutput_WhenFirstLineIsOlderOrUnreadable_ReturnsToolTooOldError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		stdout      string
		wantLine    string
		wantVersion string
	}{
		{name: "a later line names a newer version", stdout: "gh version 2.49.0\ngh version 2.60.0\n", wantLine: "gh version 2.49.0", wantVersion: "2.49.0"},
		{name: "a first line the parser reads no version from", stdout: "gh version DEV\ngh version 2.60.0\n", wantLine: "gh version DEV"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := requirement().CheckOutput(tt.stdout, versionOf)

			tooOld, ok := errors.AsType[*child.ToolTooOldError](err)
			assert.Assert(t, ok, "CheckOutput(%q) error = %v, want *child.ToolTooOldError", tt.stdout, err)
			want := child.ToolTooOldError{
				Tool: "gh", Version: tt.wantVersion, Line: tt.wantLine, Minimum: "2.50.0", InstallURL: "https://cli.github.com",
			}
			assert.Check(t, is.DeepEqual(*tooOld, want))
		})
	}
}
