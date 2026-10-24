//go:build unix

package cli_test

import (
	"errors"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"gotest.tools/v3/assert"
)

// goQuit opens what Go writes on stderr when a quit signal ends a program: the name of the signal,
// then the dump of every goroutine.
const goQuit = "SIGQUIT: quit\n"

// exitQuit is the exit code of a program that a quit signal ended as Go ends it.
const exitQuit = 2

func TestApp_Run_WhenASignalEndsItWhileTheSpinnerLineShows_ShowsTheCursorAndDiesOfTheSignal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		signal syscall.Signal
	}{
		{name: "interrupt", signal: syscall.SIGINT},
		{name: "terminate", signal: syscall.SIGTERM},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			run := startStuck(t, stuckSpinning)
			run.await(t, "the spinner line showed", run.spinnerStarted)
			hiddenBefore := run.stderr.cursorHidden()

			assert.NilError(t, run.cmd.Process.Signal(tt.signal))
			_, err := io.Copy(run.stderr, run.pipe)
			assert.NilError(t, err)
			err = run.cmd.Wait()

			assert.Check(t, hiddenBefore, "the cursor showed beside the spinner line:\n%q", run.stderr.raw())
			assert.Check(t, !run.stderr.cursorHidden(), "the cursor is still hidden after the signal:\n%q", run.stderr.raw())
			diedOf(t, err, tt.signal)
		})
	}
}

func TestApp_Run_WhenQuitWhileTheSpinnerLineShows_ShowsTheCursorThenEndsAsGoEndsAQuit(t *testing.T) {
	t.Parallel()

	run := startStuck(t, stuckSpinning)
	run.await(t, "the spinner line showed", run.spinnerStarted)
	hiddenBefore := run.stderr.cursorHidden()

	assert.NilError(t, run.cmd.Process.Signal(syscall.SIGQUIT))
	rest, err := io.ReadAll(run.pipe)
	assert.NilError(t, err)
	err = run.cmd.Wait()

	drawn, dump, quit := strings.Cut(string(rest), goQuit)
	_, writeErr := run.stderr.Write([]byte(drawn))
	assert.NilError(t, writeErr)
	assert.Check(t, hiddenBefore, "the cursor showed beside the spinner line:\n%q", run.stderr.raw())
	assert.Check(t, !run.stderr.cursorHidden(), "the cursor is still hidden when Go dumps its goroutines:\n%q", run.stderr.raw())
	assert.Check(t, quit && strings.Contains(dump, "goroutine "), "stderr ends with no goroutine dump:\n%q", rest)
	exit, ok := errors.AsType[*exec.ExitError](err)
	assert.Assert(t, ok, "wait = %v, want exit code %d", err, exitQuit)
	assert.Check(t, exit.ExitCode() == exitQuit, "exit code = %d, want %d", exit.ExitCode(), exitQuit)
}
