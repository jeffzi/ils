// Package child names how a program started from an argv failed.
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

func commandLine(argv []string) string {
	return strings.Join(argv, " ")
}
