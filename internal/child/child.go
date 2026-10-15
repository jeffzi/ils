// Package child names how a program started from an argv failed, or why it is not fit to start.
package child

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ExitError reports a call that exited non-zero.
type ExitError struct {
	Stderr string
	Argv   []string
	Code   int
}

// Error joins the argv, the exit status, and the trimmed stderr when there is any.
func (e *ExitError) Error() string {
	message := fmt.Sprintf("%s: exit status %d", commandLine(e.Argv), e.Code)
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
	command := commandLine(argv)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s: %w", command, ctxErr)
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok && exitErr.Exited() {
		return &ExitError{Argv: argv, Code: exitErr.ExitCode(), Stderr: stderr}
	}

	return fmt.Errorf("%s: %w", command, runErr)
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

func commandLine(argv []string) string {
	return strings.Join(argv, " ")
}
