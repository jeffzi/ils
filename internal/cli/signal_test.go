package cli_test

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

func TestApp_Run_WhenQuitWhileTheSpinnerLineShows_DiesOfItWithTheCursorShown(t *testing.T) {
	t.Parallel()
	if !readsStopDisposition() {
		t.Skip("ils sets no signal disposition on this system; Go's own quit ends the run")
	}

	run := startStuck(t, stuckSpinning)
	run.awaitSpinning(t)

	assert.NilError(t, run.cmd.Process.Signal(syscall.SIGQUIT))
	_, err := run.end(t)

	diedOf(t, err, syscall.SIGQUIT)
	assert.Check(t, !run.stderr.cursorHidden(), "the cursor is still hidden when the run dies:\n%q", run.stderr.raw())
}

func TestApp_Run_WhenASignalLandsWhileTheSpinnerLineShows_PrintsTheResultWithTheDisplayEnded(t *testing.T) {
	t.Parallel()

	run := startStuck(t, stuckStopping)
	run.awaitSpinning(t)

	assert.NilError(t, run.cmd.Process.Signal(syscall.SIGINT))
	stdout, err := run.end(t)

	assert.Check(t, err, "the run did not end with the result of its landing command")
	assert.Check(t, is.Equal(stdout, "Stopped.\n"), "the run did not print its result")
	checkCleared(t, run)
}

func TestApp_Run_WhenStartedWithTheInterruptIgnored_GoesOnIgnoringIt(t *testing.T) {
	t.Parallel()

	run := startStuckIgnoring(t, stuckTelling, "INT")

	run.awaitLine(t, interruptIgnored)
}

func TestApp_Run_WhenASecondSignalLands_DiesOfItWithTheDisplayEnded(t *testing.T) {
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

				run.awaitSpinning(t)
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

			run := startStuck(t, tt.way)
			tt.armed(t, run)
			assert.NilError(t, run.cmd.Process.Signal(syscall.SIGINT))
			run.awaitLine(t, canceled)

			assert.NilError(t, run.cmd.Process.Signal(syscall.SIGINT))
			_, err := run.end(t)

			diedOf(t, err, syscall.SIGINT)
			checkCleared(t, run)
		})
	}
}

// checkCleared checks, without stopping the test, that stderr of run ends as a normal end leaves it.
func checkCleared(t *testing.T, run *stuckChild) {
	t.Helper()

	assert.Check(t, run.cleared(), "stderr does not end as a normal end leaves it:\n%q", run.stderr.raw())
}

// diedOf fails the test unless err, what waiting for a run returned, says the run died of sig.
func diedOf(t *testing.T, err error, sig syscall.Signal) {
	t.Helper()

	exit, ok := errors.AsType[*exec.ExitError](err)
	assert.Assert(t, ok, "wait = %v, want the process to die of %v", err, sig)
	status, ok := exit.Sys().(syscall.WaitStatus)
	assert.Assert(t, ok, "exit status %T is no wait status", exit.Sys())
	assert.Check(t, status.Signaled() && status.Signal() == sig, "exit status = %v, want death by %v", status, sig)
}
