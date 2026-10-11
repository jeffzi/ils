// Package git makes typed calls over the git CLI, started from an argv and never through a shell.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	program = "git"

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
	// Dir is the working directory of every call.
	Dir string
	// Env is the child's whole environment; nil inherits this process's.
	Env []string
}

// Result is what a call that exited zero left behind.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// NotFoundError reports a program that is missing from PATH.
type NotFoundError struct {
	Err  error
	Name string
}

// Error names the program that is missing from PATH.
func (e *NotFoundError) Error() string {
	return e.Name + " not found on PATH"
}

// Unwrap returns the error exec.LookPath reported.
func (e *NotFoundError) Unwrap() error {
	return e.Err
}

// ExitError reports a call that exited non-zero.
type ExitError struct {
	Stderr string
	Argv   []string
	Code   int
}

// Error joins the argv, the exit status, and git's trimmed stderr when there is any.
func (e *ExitError) Error() string {
	message := fmt.Sprintf("%s: exit status %d", strings.Join(e.Argv, " "), e.Code)
	if stderr := strings.TrimSpace(e.Stderr); stderr != "" {
		message += ": " + stderr
	}

	return message
}

// Run runs git with args and returns its output once it has exited zero.
//
// A git missing from PATH is a *NotFoundError and a non-zero exit is an
// *ExitError. When ctx ends before git does, the error is the context's and
// the Result is empty, so a half-written stdout is never read.
//
// Run does not wait for a process git started and left behind: once git has
// exited or been killed, output pipes still held open are closed after a short
// grace, so a context that ended never turns into a hang.
func (r Runner) Run(ctx context.Context, args ...string) (Result, error) {
	path, err := exec.LookPath(program)
	if err != nil {
		return Result{}, &NotFoundError{Name: program, Err: err}
	}

	argv := append([]string{program}, args...)
	if r.Recorder != nil {
		r.Recorder.record(argv)
	}

	var stdout, stderr bytes.Buffer

	//nolint:gosec // G204: running git with the caller's argv is this package's purpose; no shell is involved.
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = r.Dir
	cmd.Env = r.Env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = pipeGrace

	// ErrWaitDelay means git exited zero and a process it started still holds the pipes: git's own
	// output is complete, so the call succeeded.
	runErr := cmd.Run()
	if runErr == nil || errors.Is(runErr, exec.ErrWaitDelay) {
		return Result{Stdout: stdout.String(), Stderr: stderr.String()}, nil
	}

	command := strings.Join(argv, " ")
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, fmt.Errorf("%s: %w", command, ctxErr)
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
		return Result{}, &ExitError{Argv: argv, Code: exitErr.ExitCode(), Stderr: stderr.String()}
	}

	return Result{}, fmt.Errorf("%s: %w", command, runErr)
}
