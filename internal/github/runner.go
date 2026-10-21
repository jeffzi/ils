package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"

	gh "github.com/cli/go-gh/v2"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
)

// Program is the name gh is run under, and the first word of every argv a call is named by.
const Program = "gh"

// Argv returns the full argv of a call of gh with args.
func Argv(args []string) []string {
	return append([]string{Program}, args...)
}

const installURL = "https://cli.github.com"

// MinimumVersion is the oldest release of gh that ils works with.
const MinimumVersion = "2.50.0"

// versionPrefix is what the first line of `gh --version` reads ahead of the version.
const versionPrefix = "gh version "

// Runner runs gh, found through GH_PATH and then PATH.
type Runner struct{}

// Result is what a call printed.
type Result struct {
	Stdout string
	Stderr string
}

// DecodeError reports a call whose stdout was not the JSON its caller expected.
type DecodeError struct {
	Err  error
	Argv []string
}

// Error names the command whose output could not be decoded.
func (e *DecodeError) Error() string {
	return fmt.Sprintf("%s: decode output: %v", child.CommandLine(e.Argv), e.Err)
}

// Unwrap returns Err, so that errors.Is and errors.As reach the decoder's error.
func (e *DecodeError) Unwrap() error {
	return e.Err
}

// Run runs gh with args and returns what it printed.
//
// A gh that cannot be found is a *child.ToolMissingError. A non-zero exit is a
// *child.ExitError, returned together with the Result, since gh prints to stdout
// before some of its non-zero exits. When ctx ends before gh does, the error is
// the context's and the Result is empty, so a half-written stdout is never read.
func (Runner) Run(ctx context.Context, args ...string) (Result, error) {
	stdout, stderr, err := gh.ExecContext(ctx, args...)
	if isMissing(err) {
		return Result{}, &child.ToolMissingError{Tool: Program, InstallURL: installURL, Err: err}
	}

	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}

	failure := child.Failure(ctx, Argv(args), err, result.Stderr)
	if _, exited := errors.AsType[*child.ExitError](failure); exited {
		return result, failure
	}

	return Result{}, failure
}

// isMissing reports whether err says that no gh could be started: none on PATH,
// or a GH_PATH that names no file.
func isMissing(err error) bool {
	if _, ok := errors.AsType[*exec.Error](err); ok {
		return true
	}

	return errors.Is(err, fs.ErrNotExist)
}

// checkVersion returns nil when gh is MinimumVersion or newer.
//
// A gh that is older, or whose first line of `gh --version` carries no version that can be
// read, is a *child.ToolTooOldError; a pre-release of MinimumVersion is older than it.
func (r Runner) checkVersion(ctx context.Context) error {
	result, err := r.Run(ctx, "--version")
	if err != nil {
		return err
	}

	requirement := child.Requirement{Tool: Program, Minimum: MinimumVersion, InstallURL: installURL}

	return requirement.CheckOutput(result.Stdout, VersionOf)
}

// VersionOf returns the version that a first line of `gh --version` names, as semver without the
// leading v, or "" when it names none or does not start with "gh version ". A pre-release keeps
// its suffix, so that it reads as older than its release.
func VersionOf(line string) string {
	rest, ok := strings.CutPrefix(line, versionPrefix)
	if !ok {
		return ""
	}

	version, _, _ := strings.Cut(rest, " ")
	if !child.ValidVersion(version) {
		return ""
	}

	return version
}

// CheckLogin reports whether gh is logged in to host.
//
// A gh that cannot be found is a *child.ToolMissingError and a gh that is not
// logged in to host is a *forge.NotLoggedInError.
func (r Runner) CheckLogin(ctx context.Context, host string) error {
	_, err := r.Run(ctx, "auth", "status", "--hostname", host)
	if _, ok := errors.AsType[*child.ExitError](err); ok {
		return &forge.NotLoggedInError{Host: host, Err: err}
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

func decodeOutput[T any](stdout string, args []string) (T, error) {
	var value T
	if err := json.Unmarshal([]byte(stdout), &value); err != nil {
		var zero T

		return zero, &DecodeError{Argv: Argv(args), Err: err}
	}

	return value, nil
}
