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
	is "gotest.tools/v3/assert/cmp"
)

// goQuit opens what Go writes on stderr when a quit signal ends a program: the name of the signal,
// then the dump of every goroutine.
const goQuit = "SIGQUIT: quit\n"

// exitQuit is the exit code of a program that a quit signal ended as Go ends it.
const exitQuit = 2

// interrupt is a signal that interrupts a run of ils, named for the test case that sends it.
type interrupt struct {
	name   string
	signal syscall.Signal
}

func interrupts() []interrupt {
	return []interrupt{
		{name: "interrupt", signal: syscall.SIGINT},
		{name: "terminate", signal: syscall.SIGTERM},
	}
}

func TestApp_Run_WhenASignalEndsItWhileTheSpinnerLineShows_ShowsTheCursorAndDiesOfTheSignal(t *testing.T) {
	t.Parallel()

	for _, tt := range interrupts() {
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

func TestApp_Run_WhenCancelOnInterruptAndASignalLandsWhileTheSpinnerLineShows_PrintsTheResultWithTheDisplayEnded(t *testing.T) {
	t.Parallel()

	for _, tt := range interrupts() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			run := startStuck(t, stuckCancelOnInterrupt)
			run.await(t, "the spinner line showed", run.spinnerShows)
			run.awaitReady(t)

			assert.NilError(t, run.cmd.Process.Signal(tt.signal))
			stdout, err := run.end(t)

			assert.Check(t, err, "the run did not end with the result of its landing command")
			assert.Check(t, is.Equal(stdout, "Stopped.\n"), "the run did not print its result")
			assert.Check(t, run.cleared(), "stderr does not end as a normal end leaves it:\n%q", run.stderr.raw())
		})
	}
}

func TestApp_Run_WhenCancelOnInterruptAndASecondSignalLands_DiesOfItWithNothingPrintedAndTheDisplayEnded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		// armed waits until the run is where the first signal must land.
		armed func(t *testing.T, run *stuckChild)
		name  string
		way   string
	}{
		{
			name: "first signal during the step", way: stuckHolding,
			armed: func(t *testing.T, run *stuckChild) {
				t.Helper()

				run.await(t, "the spinner line showed", run.spinnerShows)
				run.awaitReady(t)
			},
		},
		{
			name: "first signal before the landing command runs", way: stuckHoldingEarly,
			armed: func(t *testing.T, run *stuckChild) {
				t.Helper()

				run.awaitLine(t, listening)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for _, sig := range interrupts() {
				t.Run(sig.name, func(t *testing.T) {
					t.Parallel()

					run := startStuck(t, tt.way)
					tt.armed(t, run)
					assert.NilError(t, run.cmd.Process.Signal(sig.signal))
					run.awaitLine(t, canceled)

					assert.NilError(t, run.cmd.Process.Signal(sig.signal))
					stdout, err := run.end(t)

					diedOf(t, err, sig.signal)
					assert.Check(t, stdout == "", "stdout = %q, want nothing after the second signal", stdout)
					assert.Check(t, run.cleared(), "stderr does not end as a normal end leaves it:\n%q", run.stderr.raw())
				})
			}
		})
	}
}
