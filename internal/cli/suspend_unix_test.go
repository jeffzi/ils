//go:build unix

package cli_test

import (
	"bufio"
	"context"
	"errors"
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

	return startStuckBy(t, way, os.Args[0])
}

// startStuckBy is startStuck run through argv, whose run ends in an exec of the test binary.
func startStuckBy(t *testing.T, way string, argv ...string) *stuckChild {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv ends in this test binary
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

	c.awaitLine(t, ready)
}

// awaitSpinning waits until the spinner line of the run shows and its landing command says on
// stdout that it runs.
func (c *stuckChild) awaitSpinning(t *testing.T) {
	t.Helper()

	c.await(t, "the spinner line showed", c.spinnerShows)
	c.awaitReady(t)
}

// awaitLine reads the next line of stdout, and fails the test unless it is want.
func (c *stuckChild) awaitLine(t *testing.T, want string) {
	t.Helper()

	line, err := c.stdout.ReadString('\n')
	assert.NilError(t, err, "stdout ended before %q", want)
	assert.Assert(t, line == want+"\n", "stdout = %q, want %q", line, want+"\n")
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

// suspendedSpinning starts a stuckHolding run, suspends it once its spinner line shows and its
// landing command runs, and returns it once its stopped run shows nothing.
func suspendedSpinning(t *testing.T) *stuckChild {
	t.Helper()

	run := startStuck(t, stuckHolding)
	run.awaitSpinning(t)
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

func TestApp_Run_WhenSuspendedWithNoSpinnerLine_StopsWithNothingOnStderr(t *testing.T) {
	t.Parallel()
	run := startStuck(t, stuckIdle)
	run.awaitReady(t)

	status := run.suspend(t)

	run.drain(t)
	assert.Check(t, stopped(status), "status = %v, want the run stopped", status)
	assert.Check(t, run.stderr.raw() == "", "stderr = %q, want nothing", run.stderr.raw())
}

func TestApp_Run_WhenSuspendedAfterTheLandingCommandReturned_Stops(t *testing.T) {
	t.Parallel()

	run := startStuck(t, stuckReturned)
	run.awaitReady(t)

	status := run.suspend(t)

	assert.Check(t, stopped(status), "status = %v, want the run stopped", status)
}
