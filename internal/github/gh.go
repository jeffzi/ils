// Package github drives the gh CLI, started from an argv and never through a shell.
package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"slices"
	"strings"
	"sync"

	gh "github.com/cli/go-gh/v2"

	"github.com/jeffzi/ils/internal/forge"
)

const (
	program     = "gh"
	installHint = "brew install gh"
)

// Recorder keeps the argv of every call made by the runners it is attached to.
type Recorder struct {
	calls [][]string
	mu    sync.Mutex
}

// Calls returns a copy of every recorded argv, each starting with gh, in the order the calls ended.
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

// Runner runs gh, found through GH_PATH and then PATH.
type Runner struct {
	// Recorder, when set, receives the argv of every call that reached gh.
	Recorder *Recorder
}

// Result is what a call printed.
type Result struct {
	Stdout string
	Stderr string
}

// ExitError reports a call that exited non-zero.
type ExitError struct {
	Stderr string
	Argv   []string
	Code   int
}

// Error names the command, its exit code, and what it printed on stderr.
func (e *ExitError) Error() string {
	message := fmt.Sprintf("%s: exit status %d", strings.Join(e.Argv, " "), e.Code)
	if stderr := strings.TrimSpace(e.Stderr); stderr != "" {
		message += ": " + stderr
	}

	return message
}

// DecodeError reports a call whose stdout was not the JSON its caller expected.
type DecodeError struct {
	Err  error
	Argv []string
}

// Error names the command whose output could not be decoded.
func (e *DecodeError) Error() string {
	return fmt.Sprintf("%s: decode output: %v", strings.Join(e.Argv, " "), e.Err)
}

// Unwrap returns the decoder's error.
func (e *DecodeError) Unwrap() error {
	return e.Err
}

// Run runs gh with args and returns what it printed.
//
// A gh that cannot be found is a *forge.ToolMissingError. A non-zero exit is an
// *ExitError, returned together with the Result, since gh prints to stdout
// before some of its non-zero exits. When ctx ends before gh does, the error is
// the context's and the Result is empty, so a half-written stdout is never read.
func (r Runner) Run(ctx context.Context, args ...string) (Result, error) {
	argv := append([]string{program}, args...)

	stdout, stderr, err := gh.ExecContext(ctx, args...)
	if isMissing(err) {
		return Result{}, &forge.ToolMissingError{Tool: program, InstallHint: installHint}
	}

	if r.Recorder != nil {
		r.Recorder.record(argv)
	}

	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}

	command := strings.Join(argv, " ")
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, fmt.Errorf("%s: %w", command, ctxErr)
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return result, &ExitError{Argv: argv, Code: exitErr.ExitCode(), Stderr: result.Stderr}
	}

	return Result{}, fmt.Errorf("%s: %w", command, err)
}

// isMissing reports whether err says that no gh could be started: none on PATH,
// or a GH_PATH that names no file.
func isMissing(err error) bool {
	if _, ok := errors.AsType[*exec.Error](err); ok {
		return true
	}

	return errors.Is(err, fs.ErrNotExist)
}

// CheckLogin reports whether gh is logged in to host.
//
// A gh that cannot be found is a *forge.ToolMissingError and a gh that is not
// logged in to host is a *forge.NotLoggedInError.
func (r Runner) CheckLogin(ctx context.Context, host string) error {
	_, err := r.Run(ctx, "auth", "status", "--hostname", host)
	if _, ok := errors.AsType[*ExitError](err); ok {
		return &forge.NotLoggedInError{Host: host}
	}

	return err
}

// RunJSON runs gh with args and decodes its stdout into a T.
//
// It returns the errors of Runner.Run, and a *DecodeError when stdout is not
// the JSON of a T.
func RunJSON[T any](ctx context.Context, r Runner, args ...string) (T, error) {
	result, err := r.Run(ctx, args...)
	if err != nil {
		var zero T

		return zero, err
	}

	return decodeOutput[T](result.Stdout, args)
}

// decodeOutput decodes the stdout of gh run with args into a T, or returns a *DecodeError.
func decodeOutput[T any](stdout string, args []string) (T, error) {
	var value T
	if err := json.Unmarshal([]byte(stdout), &value); err != nil {
		var zero T

		return zero, &DecodeError{Argv: append([]string{program}, args...), Err: err}
	}

	return value, nil
}
