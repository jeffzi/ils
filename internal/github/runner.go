package github

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
)

// Program is the name gh is run under, and the first word of every argv a call is named by.
const Program = "gh"

// Argv returns the full argv of a call of gh with args.
func Argv(args []string) []string {
	return child.Argv(Program, args)
}

const installURL = "https://cli.github.com"

// MinimumVersion is the oldest release of gh that ils works with. Every older release fails
// `gh pr edit` against GitHub, even with no project flag, because it still reads the Projects
// (classic) fields that GitHub's GraphQL API now rejects.
const MinimumVersion = "2.73.0"

// versionPrefix is what the first line of `gh --version` reads ahead of the version.
const versionPrefix = "gh version "

// Runner runs gh, found through GH_PATH and then PATH. Its zero value logs nothing.
type Runner struct {
	// Logger, when set, receives one debug record per call once it ends: the record child.Exec.Log
	// writes.
	Logger *slog.Logger
	// Env is gh's whole environment; nil inherits this process's. gh itself is found through this
	// process's GH_PATH and PATH whatever Env holds.
	Env []string
}

// Environ returns a copy of the environment gh runs with: the runner's own Env, or the environment
// of this process when it has none.
func (r Runner) Environ() []string {
	return child.Environ(r.Env)
}

// Result is what a call left behind, as child.Output holds it.
type Result = child.Output

// DecodeError reports a call whose stdout was not what its caller expected.
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
// *child.ExitError, returned together with gh's stdout, since gh prints to stdout
// before some of its non-zero exits; its stderr is the ExitError's. When ctx ends
// before gh does, the error is the context's and the Result is empty, so a
// half-written stdout is never read.
//
// Run does not wait for a process gh started and left behind: once gh has exited or been killed,
// output pipes still held open are closed after a short grace, as child.Program.Run does.
func (r Runner) Run(ctx context.Context, args ...string) (Result, error) {
	program := child.Program{
		LookPath: ghPath, Logger: r.Logger, Tool: Program, InstallURL: installURL, Args: args, Env: r.Env,
		StartWantingAFileIsMissing: true,
	}

	return program.Run(ctx)
}

// PathVariable is the environment variable that names the gh to start ahead of the one on PATH.
const PathVariable = "GH_PATH"

// ghPath returns the gh to start, whatever name it is asked for: the one PathVariable names, or else
// the gh on PATH, found as git is. A gh found only through a relative PATH entry is not found.
func ghPath(string) (string, error) {
	if path := os.Getenv(PathVariable); path != "" {
		return path, nil
	}

	path, err := exec.LookPath(Program)
	if err != nil {
		return "", fmt.Errorf("find %s: %w", Program, err)
	}

	return path, nil
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

// runDecoded runs gh with args and decodes its stdout into a T.
//
// It returns the errors of Runner.Run, and a *DecodeError when stdout is not
// the JSON of a T.
func runDecoded[T any](ctx context.Context, r Runner, args ...string) (T, error) {
	result, err := r.Run(ctx, args...)
	if err != nil {
		var zero T

		return zero, err
	}

	return decodeOutput[T](result.Stdout, args)
}

// readEachPage runs gh with args and decodes the pages of its stdout with decodeEachPage.
//
// It returns the errors of Runner.Run, and a *DecodeError when stdout holds no page or a page
// that is not the JSON of a P.
func readEachPage[P any](ctx context.Context, r Runner, args []string) ([]P, error) {
	result, err := r.Run(ctx, args...)
	if err != nil {
		return nil, err
	}

	return decodeEachPage[P](result.Stdout, args)
}

func decodeOutput[T any](stdout string, args []string) (T, error) {
	var value T
	if err := json.Unmarshal([]byte(stdout), &value); err != nil {
		var zero T

		return zero, &DecodeError{Argv: Argv(args), Err: err}
	}

	return value, nil
}

// decodeEachPage decodes the JSON values that `gh api --paginate` prints, one per page and
// concatenated, each into a P; stdout holding no value is a *DecodeError.
func decodeEachPage[P any](stdout string, args []string) ([]P, error) {
	decoder := jsontext.NewDecoder(strings.NewReader(stdout))

	var pages []P

	for {
		raw, err := decoder.ReadValue()
		if errors.Is(err, io.EOF) {
			if len(pages) > 0 {
				return pages, nil
			}

			err = io.ErrUnexpectedEOF
		}

		if err != nil {
			return nil, &DecodeError{Argv: Argv(args), Err: err}
		}

		page, err := decodeOutput[P](string(raw), args)
		if err != nil {
			return nil, err
		}

		pages = append(pages, page)
	}
}
