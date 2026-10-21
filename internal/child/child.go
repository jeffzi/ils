// Package child names how a program started from an argv failed, or why it is not fit to start.
package child

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/mod/semver"
)

// ExitError reports a call that exited non-zero.
type ExitError struct {
	Stderr string
	Argv   []string
	Code   int
}

// Error joins the argv, the exit status, and the trimmed stderr when there is any.
func (e *ExitError) Error() string {
	message := fmt.Sprintf("%s: exit status %d", CommandLine(e.Argv), e.Code)
	if stderr := strings.TrimSpace(e.Stderr); stderr != "" {
		message += ": " + stderr
	}

	return message
}

// Failure returns the error of a run of argv that failed with runErr and left stderr behind.
//
// When ctx ended before the program did, the error is the context's, whatever the
// program's exit was. A non-zero exit is an *ExitError. Anything else is runErr,
// named by the command.
func Failure(ctx context.Context, argv []string, runErr error, stderr string) error {
	command := CommandLine(argv)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s: %w", command, ctxErr)
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok && exitErr.Exited() {
		return &ExitError{Argv: argv, Code: exitErr.ExitCode(), Stderr: stderr}
	}

	return fmt.Errorf("%s: %w", command, runErr)
}

// ToolMissingError reports that a command-line tool ils drives is not installed.
type ToolMissingError struct {
	// Err is what looking the tool up failed with.
	Err  error
	Tool string
	// InstallURL is the page the tool is installed from on every operating system, never a
	// command, since no package manager is common to all of them.
	InstallURL string
}

// Error names the missing tool and the page to install it from.
func (e *ToolMissingError) Error() string {
	return fmt.Sprintf("%s is not installed: install it from %s", e.Tool, e.InstallURL)
}

// Unwrap returns what looking the tool up failed with.
func (e *ToolMissingError) Unwrap() error {
	return e.Err
}

// ToolTooOldError reports that the installed command-line tool is older than the oldest release
// ils works with, or prints no version to judge it by.
type ToolTooOldError struct {
	Tool string
	// Version is the installed version, empty when Line carries none that could be read.
	Version string
	// Line is the first line the tool prints when asked for its version.
	Line string
	// Minimum is the oldest version of the tool that ils works with.
	Minimum    string
	InstallURL string
}

// Error names the tool, what it says of its version, and the oldest version that is accepted.
func (e *ToolTooOldError) Error() string {
	if e.Version == "" {
		return fmt.Sprintf("%s printed %q, which names no version; %s or newer is needed", e.Tool, e.Line, e.Minimum)
	}

	return fmt.Sprintf("%s %s is older than %s", e.Tool, e.Version, e.Minimum)
}

// semverPrefix is what the semver package wants ahead of a version.
const semverPrefix = "v"

// ValidVersion reports whether version is valid semver without the leading v.
func ValidVersion(version string) bool {
	return semver.IsValid(semverPrefix + version)
}

// AtLeast reports whether version is minimum or newer, both as semver without the leading v. A
// version that is not valid semver is older than any valid one.
func AtLeast(version, minimum string) bool {
	return semver.Compare(semverPrefix+version, semverPrefix+minimum) >= 0
}

// Requirement is the oldest release of a tool that ils works with and where to get it.
type Requirement struct {
	Tool       string
	Minimum    string
	InstallURL string
}

// Check returns nil when version is valid semver without the leading v and is r.Minimum or
// newer. Otherwise it returns a *ToolTooOldError for r.Tool, whose Version is empty when version
// is not valid semver. line is the first line the tool printed for its version.
func (r Requirement) Check(line, version string) error {
	tooOld := &ToolTooOldError{Tool: r.Tool, Line: line, Minimum: r.Minimum, InstallURL: r.InstallURL}

	if !ValidVersion(version) {
		return tooOld
	}

	tooOld.Version = version
	if !AtLeast(version, r.Minimum) {
		return tooOld
	}

	return nil
}

// CheckOutput returns nil when the version that versionOf reads from the first line of stdout,
// the whole output of the tool's --version, is r.Minimum or newer. Otherwise it returns the
// *ToolTooOldError of Check for that line. Lines after the first are never read.
func (r Requirement) CheckOutput(stdout string, versionOf func(line string) string) error {
	line, _, _ := strings.Cut(stdout, "\n")

	return r.Check(line, versionOf(line))
}

// CommandLine returns argv joined by spaces, the way every error about a run of argv names the
// call.
func CommandLine(argv []string) string {
	return strings.Join(argv, " ")
}
