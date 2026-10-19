// Package git makes typed calls over the git CLI, started from an argv and never through a shell.
package git

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/jeffzi/ils/internal/child"
)

const (
	program = "git"
	// installURL is the page git is installed from on every operating system.
	installURL = "https://git-scm.com/downloads"

	// pipeGrace is how long a call waits for git's output pipes to close once git itself is gone. A
	// process git started, such as ssh or a credential helper, can outlive it holding those pipes.
	pipeGrace = time.Second
)

// Recorder keeps the argv of every call made by the runners it is attached to.
type Recorder struct {
	calls [][]string
	mu    sync.Mutex
}

// Calls returns a copy of every recorded argv, in the order the calls started.
func (r *Recorder) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()

	calls := make([][]string, len(r.calls))
	for i, argv := range r.calls {
		calls[i] = slices.Clone(argv)
	}

	return calls
}

func (r *Recorder) record(argv []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, slices.Clone(argv))
}

// Runner runs git in one directory with the environment its caller chose.
type Runner struct {
	// Recorder, when set, receives the argv of every call.
	Recorder *Recorder
	Dir      string
	// Env is the child's whole environment; nil inherits this process's.
	Env []string
}

// Result is what a call that exited zero left behind.
type Result struct {
	Stdout string
	Stderr string
}

// Run runs git with args and returns its output once it has exited zero.
//
// A git missing from PATH is a *child.ToolMissingError and a non-zero exit is a
// *child.ExitError. When ctx ends before git does, the error is the context's and
// the Result is empty, so a half-written stdout is never read.
//
// Run does not wait for a process git started and left behind: once git has
// exited or been killed, output pipes still held open are closed after a short
// grace, so a context that ended never turns into a hang.
func (r Runner) Run(ctx context.Context, args ...string) (Result, error) {
	inv := invocation{
		program:    program,
		installURL: installURL,
		args:       args,
		dir:        r.Dir,
		env:        r.Env,
		waitDelay:  pipeGrace,
	}
	if r.Recorder != nil {
		inv.record = r.Recorder.record
	}

	return inv.run(ctx)
}

// invocation is one run of a tool as a child process, started from an argv and never through a
// shell.
type invocation struct {
	// record, when set, receives the argv once the tool is found and before it starts.
	record     func(argv []string)
	program    string
	installURL string
	dir        string
	args       []string
	env        []string
	// waitDelay is how long to wait for the output pipes to close once the tool is gone; zero waits
	// for them for as long as it takes.
	waitDelay time.Duration
}

// run runs the tool and returns its output once it has exited zero.
//
// A tool missing from PATH is a *child.ToolMissingError and a failed run is the error of
// child.Failure. With a waitDelay, a process the tool left holding the output pipes does not fail
// a run that exited zero.
func (inv invocation) run(ctx context.Context) (Result, error) {
	path, err := exec.LookPath(inv.program)
	if err != nil {
		return Result{}, &child.ToolMissingError{Tool: inv.program, InstallURL: inv.installURL, Err: err}
	}

	argv := append([]string{inv.program}, inv.args...)
	if inv.record != nil {
		inv.record(argv)
	}

	var stdout, stderr bytes.Buffer

	//nolint:gosec // G204: running a tool with the caller's argv is this package's purpose; no shell is involved.
	cmd := exec.CommandContext(ctx, path, inv.args...)
	cmd.Dir = inv.dir
	cmd.Env = inv.env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = inv.waitDelay

	// ErrWaitDelay means the tool exited zero and a process it started still holds the pipes: the
	// tool's own output is complete, so the call succeeded.
	runErr := cmd.Run()
	if runErr == nil || errors.Is(runErr, exec.ErrWaitDelay) {
		return Result{Stdout: stdout.String(), Stderr: stderr.String()}, nil
	}

	return Result{}, child.Failure(ctx, argv, runErr, stderr.String())
}

// exited reports whether err is git exiting with code.
func exited(err error, code int) bool {
	exitErr, ok := errors.AsType[*child.ExitError](err)

	return ok && exitErr.Code == code
}
