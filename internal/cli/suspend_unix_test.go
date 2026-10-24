//go:build unix

package cli_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

// quiet is how long stderr of a stopped run stays silent before the tests take it to have said
// all it wrote before the stop: the bytes written before the stop are already in the pipe.
const quiet = 200 * time.Millisecond

// stuckChild is the test binary run as stuck runs, in a process group of its own so that a stop
// signal from the terminal reaches it.
type stuckChild struct {
	cmd    *exec.Cmd
	stderr *screen
	pipe   *os.File
	stdout *bufio.Reader
}

// startStuck starts the test binary as stuck runs it in the given way; the end of the test kills it
// unless the test waited for it.
func startStuck(t *testing.T, way string) *stuckChild {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0]) //nolint:gosec // argv is this test binary
	cmd.Env = append(os.Environ(), stuckRun+"="+way)
	cmd.Dir = t.TempDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	read, write, err := os.Pipe()
	assert.NilError(t, err)
	cmd.Stderr = write
	stdout, err := cmd.StdoutPipe()
	assert.NilError(t, err)
	assert.NilError(t, cmd.Start())
	assert.NilError(t, write.Close())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Wait() // it reports the kill the end of the test's context sent
		}
		assert.Check(t, read.Close())
	})

	return &stuckChild{cmd: cmd, stderr: newScreen(t, wide), pipe: read, stdout: bufio.NewReader(stdout)}
}

// await draws stderr onto the screen one byte at a time until done holds, and fails the test
// when stderr ends first; what names the state awaited.
func (c *stuckChild) await(t *testing.T, what string, done func() bool) {
	t.Helper()

	b := make([]byte, 1)
	for !done() {
		n, err := c.pipe.Read(b)
		assert.NilError(t, err, "stderr ended before %s:\n%q", what, c.stderr.raw())
		_, err = c.stderr.Write(b[:n])
		assert.NilError(t, err)
	}
}

// awaitReady waits for the landing command of the run to say on stdout that it runs.
func (c *stuckChild) awaitReady(t *testing.T) {
	t.Helper()

	line, err := c.stdout.ReadString('\n')
	assert.NilError(t, err)
	assert.Assert(t, line == ready+"\n", "stdout = %q, want %q", line, ready+"\n")
}

// drain draws onto the screen everything stderr holds, which is all a stopped run wrote.
func (c *stuckChild) drain(t *testing.T) {
	t.Helper()

	buf := make([]byte, 4096)
	for {
		assert.NilError(t, c.pipe.SetReadDeadline(time.Now().Add(quiet)))
		n, err := c.pipe.Read(buf)
		_, writeErr := c.stderr.Write(buf[:n])
		assert.NilError(t, writeErr)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			assert.NilError(t, c.pipe.SetReadDeadline(time.Time{}))

			return
		}
		assert.NilError(t, err, "stderr ended while the run was stopped:\n%q", c.stderr.raw())
	}
}

// spinnerShows reports whether the screen shows a line with the cursor hidden.
func (c *stuckChild) spinnerShows() bool {
	return c.stderr.cursorHidden() && len(c.stderr.lines()) > 0
}

// cleared reports whether the screen shows nothing and its cursor.
func (c *stuckChild) cleared() bool {
	return !c.stderr.cursorHidden() && len(c.stderr.lines()) == 0
}

// spinnerStarted reports whether the screen hides its cursor or shows a line, whichever the run
// drew first.
func (c *stuckChild) spinnerStarted() bool {
	return c.stderr.cursorHidden() || len(c.stderr.lines()) > 0
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

// suspend sends the run the stop signal of a terminal and returns its status once it stopped or
// ended, without reaping it.
func (c *stuckChild) suspend(t *testing.T) syscall.WaitStatus {
	t.Helper()

	assert.NilError(t, c.cmd.Process.Signal(syscall.SIGTSTP))
	var status syscall.WaitStatus
	_, err := syscall.Wait4(c.cmd.Process.Pid, &status, syscall.WUNTRACED, nil)
	assert.NilError(t, err)

	return status
}

// stopped reports whether status is that of a run that neither exited nor died of a signal.
func stopped(status syscall.WaitStatus) bool {
	return !status.Exited() && !status.Signaled()
}

func TestApp_Run_WhenSuspendedWhileTheSpinnerLineShows_ClearsTheLineAndShowsTheCursorBeforeItStops(t *testing.T) {
	t.Parallel()

	run := startStuck(t, stuckSpinning)
	run.await(t, "the spinner line showed", run.spinnerShows)

	status := run.suspend(t)

	run.drain(t)
	assert.Check(t, stopped(status), "status = %v, want the run stopped", status)
	assert.Check(t, len(run.stderr.lines()) == 0, "the spinner line still shows while the run is stopped:\n%q", run.stderr.raw())
	assert.Check(t, !run.stderr.cursorHidden(), "the cursor is still hidden while the run is stopped:\n%q", run.stderr.raw())
}

// suspendedSpinning starts a run whose spinner line shows, suspends it, and returns it once its
// stopped run shows nothing.
func suspendedSpinning(t *testing.T) *stuckChild {
	t.Helper()

	run := startStuck(t, stuckSpinning)
	run.await(t, "the spinner line showed", run.spinnerShows)
	status := run.suspend(t)
	assert.Assert(t, stopped(status), "status = %v, want the run stopped", status)
	run.drain(t)
	assert.Assert(t, run.cleared(), "the spinner line still shows while the run is stopped:\n%q", run.stderr.raw())

	return run
}

func TestApp_Run_WhenContinuedAfterASuspend_DrawsTheSpinnerLineAgain(t *testing.T) {
	t.Parallel()
	run := suspendedSpinning(t)

	assert.NilError(t, run.cmd.Process.Signal(syscall.SIGCONT))

	run.await(t, "the spinner line showed again", run.spinnerShows)
}

func TestApp_Run_WhenInterruptedAfterASuspendAndContinue_ShowsTheCursorAndDiesOfTheSignal(t *testing.T) {
	t.Parallel()
	run := suspendedSpinning(t)
	assert.NilError(t, run.cmd.Process.Signal(syscall.SIGCONT))
	run.await(t, "the spinner line showed again", run.spinnerShows)

	assert.NilError(t, run.cmd.Process.Signal(syscall.SIGINT))
	_, err := io.Copy(run.stderr, run.pipe)
	assert.NilError(t, err)
	err = run.cmd.Wait()

	assert.Check(t, !run.stderr.cursorHidden(), "the cursor is still hidden after the interrupt:\n%q", run.stderr.raw())
	diedOf(t, err, syscall.SIGINT)
}

func TestApp_Run_WhenSuspendedWithNoSpinnerLine_StopsWithNothingOnStderr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		way  string
	}{
		{name: "stderr is no terminal", way: stuckPiped},
		{name: "no step runs", way: stuckIdle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			run := startStuck(t, tt.way)
			run.awaitReady(t)

			status := run.suspend(t)

			run.drain(t)
			assert.Check(t, stopped(status), "status = %v, want the run stopped", status)
			assert.Check(t, run.stderr.raw() == "", "stderr = %q, want nothing", run.stderr.raw())
		})
	}
}

func TestApp_Run_WhenSuspendedAfterTheLandingCommandReturned_Stops(t *testing.T) {
	t.Parallel()

	run := startStuck(t, stuckReturned)
	run.awaitReady(t)

	status := run.suspend(t)

	assert.Check(t, stopped(status), "status = %v, want the run stopped", status)
}

func TestApp_Run_WhenASuspendCannotStopTheProcess_DrawsTheSpinnerLineAgain(t *testing.T) {
	t.Parallel()

	run := startStuck(t, stuckUnstoppable)
	run.await(t, "the spinner line showed", run.spinnerShows)

	assert.NilError(t, run.cmd.Process.Signal(syscall.SIGTSTP))

	run.await(t, "the spinner line cleared for the stop", run.cleared)
	run.await(t, "the spinner line showed again", run.spinnerShows)
}
