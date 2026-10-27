//go:build unix

package cli_test

import (
	"errors"
	"io"
	"os"
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

func TestApp_Run_WhenQuitWhileTheSpinnerLineShows_EndsAsGoEndsAQuitWithTheCursorShown(t *testing.T) {
	t.Parallel()

	run := startStuck(t, stuckSpinning)
	run.await(t, "the spinner line showed", run.spinnerShows)

	assert.NilError(t, run.cmd.Process.Signal(syscall.SIGQUIT))
	rest, err := io.ReadAll(run.pipe)
	assert.NilError(t, err)
	_ = run.cmd.Wait() // Go exits non-zero on a quit; the goroutine dump checked below is what proves it

	drawn, dump, quit := strings.Cut(string(rest), goQuit)
	_, writeErr := run.stderr.Write([]byte(drawn))
	assert.NilError(t, writeErr)
	assert.Check(t, !run.stderr.cursorHidden(), "the cursor is still hidden when Go dumps its goroutines:\n%q", run.stderr.raw())
	assert.Check(t, quit && strings.Contains(dump, "goroutine "), "stderr ends with no goroutine dump:\n%q", rest)
}

func TestApp_Run_WhenASignalLandsWhileTheSpinnerLineShows_PrintsTheResultWithTheDisplayEnded(t *testing.T) {
	t.Parallel()

	run := startStuck(t, stuckStopping)
	run.awaitSpinning(t)

	assert.NilError(t, run.cmd.Process.Signal(syscall.SIGINT))
	stdout, err := run.end(t)

	assert.Check(t, err, "the run did not end with the result of its landing command")
	assert.Check(t, is.Equal(stdout, "Stopped.\n"), "the run did not print its result")
	assert.Check(t, run.cleared(), "stderr does not end as a normal end leaves it:\n%q", run.stderr.raw())
}

func TestApp_Run_WhenStartedWithTheInterruptIgnored_GoesOnIgnoringIt(t *testing.T) {
	t.Parallel()

	run := startStuckIgnoringTheInterrupt(t, stuckStopping)
	run.awaitSpinning(t)

	assert.NilError(t, run.cmd.Process.Signal(syscall.SIGINT))
	assert.NilError(t, run.cmd.Process.Signal(syscall.SIGTERM))
	stdout, err := run.end(t)

	assert.Check(t, err, "the run did not end with the result of its landing command")
	assert.Check(t, is.Equal(stdout, "Stopped.\n"), "the interrupt counted as the first signal of the run")
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
			assert.Check(t, run.cleared(), "stderr does not end as a normal end leaves it:\n%q", run.stderr.raw())
		})
	}
}

// startStuckIgnoringTheInterrupt is startStuck, except that the run starts with the interrupt
// ignored, as a background job of a non-interactive shell does: the shell ignores it, and the
// ignore holds across its exec of the test binary.
func startStuckIgnoringTheInterrupt(t *testing.T, way string) *stuckChild {
	t.Helper()

	return startStuckBy(t, way, "sh", "-c", `trap "" INT; exec "$0"`, os.Args[0])
}

// end draws the rest of stderr onto the screen, reads the rest of stdout, and waits for the run to
// end; it returns the rest of stdout and what waiting returned.
func (c *stuckChild) end(t *testing.T) (stdout string, err error) {
	t.Helper()

	_, err = io.Copy(c.stderr, c.pipe)
	assert.NilError(t, err)
	rest, err := io.ReadAll(c.stdout)
	assert.NilError(t, err)

	return string(rest), c.cmd.Wait()
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
