// Package child names what a program started from an argv left behind, how it failed, or why it
// is not fit to start.
package child

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

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
// program's exit was. A program that died of an interrupt is reported as
// context.Canceled even while ctx is still live: the terminal sends Ctrl-C to the
// whole process group, so the program can die of it before ils has canceled its
// own context, and either order is the same interrupt. On Windows, which reports no
// signals, that program is one that exited with STATUS_CONTROL_C_EXIT.
//
// A program that died of SIGTERM while ctx was still live is given terminateGrace for ctx to
// end: a SIGTERM sent to the whole process group reaches the program and ils together, and Go
// delivers the signal to ils asynchronously, so the program can be reaped before ils cancels its
// context. When ctx ends within the grace period, the error is the context's; otherwise the
// SIGTERM was the program's alone and is reported like any other signal. Unix only: Windows
// reports no signals.
//
// A non-zero exit is an *ExitError, and a program a signal ended is a *KilledError. Anything else
// is runErr, named by the command.
func Failure(ctx context.Context, argv []string, runErr error, stderr string) error {
	command := CommandLine(argv)
	named := func(err error) error { return fmt.Errorf("%s: %w", command, err) }

	if ctxErr := ctx.Err(); ctxErr != nil {
		return named(ctxErr)
	}

	exitErr, ok := errors.AsType[*exec.ExitError](runErr)
	if ok && interrupted(exitErr) {
		return named(context.Canceled)
	}

	if ok && killedBy(exitErr, syscall.SIGTERM) {
		if ctxErr := endedWithin(ctx, terminateGrace); ctxErr != nil {
			return named(ctxErr)
		}
	}

	if ok && exitErr.Exited() {
		return &ExitError{Argv: argv, Code: exitErr.ExitCode(), Stderr: stderr}
	}

	if ok {
		return &KilledError{Argv: argv, Err: runErr}
	}

	return named(runErr)
}

// KilledError reports a program that started and never exited: a signal ended it, after it may
// have done part of its work.
type KilledError struct {
	// Err is the *exec.ExitError the run ended with.
	Err  error
	Argv []string
}

// Error names the command and the signal that ended it.
func (e *KilledError) Error() string {
	return CommandLine(e.Argv) + ": " + e.Err.Error()
}

// Unwrap returns the error the run ended with.
func (e *KilledError) Unwrap() error {
	return e.Err
}

// Output is what a run left behind: both outputs of a run that exited zero, and only the stdout of
// one that exited non-zero, whose stderr its *ExitError holds.
type Output struct {
	Stdout string
	Stderr string
}

// Ended returns what a run of argv that printed both outputs of printed and ended with runErr
// leaves to its caller: printed when runErr is nil, and otherwise the error of Failure, with the
// stdout when that error is an *ExitError and an empty Output when it is not, so that a
// half-written stdout is never read.
func Ended(ctx context.Context, argv []string, runErr error, printed Output) (Output, error) {
	if runErr == nil {
		return printed, nil
	}

	err := Failure(ctx, argv, runErr, printed.Stderr)
	if _, exitedNonZero := errors.AsType[*ExitError](err); exitedNonZero {
		return Output{Stdout: printed.Stdout}, err
	}

	return Output{}, err
}

// terminateGrace bounds how long Failure waits for the context of a program that died of SIGTERM
// to end. It only has to cover the scheduling of ils's own signal goroutine.
const terminateGrace = 100 * time.Millisecond

// interrupted reports whether the program exitErr reports on was ended by Ctrl-C: killed by
// SIGINT, or, on Windows, which reports no signals, exited with STATUS_CONTROL_C_EXIT.
func interrupted(exitErr *exec.ExitError) bool {
	if killedBy(exitErr, syscall.SIGINT) {
		return true
	}

	return interruptedExit(exitErr.ExitCode())
}

// killedBy reports whether the program exitErr reports on was killed by signal.
func killedBy(exitErr *exec.ExitError, signal syscall.Signal) bool {
	status, ok := exitErr.Sys().(syscall.WaitStatus)

	return ok && status.Signaled() && status.Signal() == signal
}

// endedWithin waits up to wait for ctx to end and returns its error, or nil when ctx is still
// live once wait has passed.
func endedWithin(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// statusControlCExit is STATUS_CONTROL_C_EXIT, the exit code Windows gives a console program that
// Ctrl-C ended. No Unix exit code reaches it: those run from 0 to 255.
const statusControlCExit = 0xC000013A

// interruptedExit reports whether code, as exec.ExitError.ExitCode returns it, is
// STATUS_CONTROL_C_EXIT. Windows keeps the code as a uint32 and ExitCode converts it to int, so a
// 32-bit build sees it negative; only its low 32 bits are compared.
func interruptedExit(code int) bool {
	return int64(code)&0xFFFFFFFF == statusControlCExit
}

// Exec is one finished run of a tool, as a debug log records it.
type Exec struct {
	// Err is what the run ended with: nil, a *ExitError, or the failure that ended it before an
	// exit.
	Err  error
	Tool string
	// Args is the argv after the tool.
	Args []string
	Took time.Duration
}

// The message and the attribute keys of the debug record Exec.Log writes, for a handler that
// reads it back.
const (
	ExecMessage = "exec"
	// KeyTool is the key of the tool, a string.
	KeyTool = "tool"
	// KeyArgs is the key of the argv after the tool, a []string.
	KeyArgs = "args"
	// KeyExit is the key of the exit code, an int, present when the tool ran to an exit.
	KeyExit = "exit"
	// KeyError is the key of the failure, a string, present instead of KeyExit when the tool did
	// not run to an exit.
	KeyError = "error"
	// KeyDuration is the key of how long the run took, a time.Duration.
	KeyDuration = "duration"
)

// Log sends logger, when it is not nil, the debug record of e: the message ExecMessage and the
// attributes KeyTool; KeyArgs; KeyExit, the exit code, when the tool ran to an exit, or else
// KeyError, the failure; and KeyDuration. What the tool printed is never logged, since it may hold
// secrets.
func (e Exec) Log(ctx context.Context, logger *slog.Logger) {
	if logger == nil {
		return
	}

	outcome := slog.Int(KeyExit, 0)
	if exitErr, exited := errors.AsType[*ExitError](e.Err); exited {
		outcome.Value = slog.IntValue(exitErr.Code)
	} else if e.Err != nil {
		outcome = slog.String(KeyError, e.Err.Error())
	}

	logger.LogAttrs(ctx, slog.LevelDebug, ExecMessage,
		slog.String(KeyTool, e.Tool), slog.Any(KeyArgs, slices.Clone(e.Args)), outcome, slog.Duration(KeyDuration, e.Took))
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

// CheckOutput returns nil when the version that versionOf reads from the VersionLine of stdout,
// the whole output of the tool's --version, is r.Minimum or newer. Otherwise it returns the
// *ToolTooOldError of Check for that line.
func (r Requirement) CheckOutput(stdout string, versionOf func(line string) string) error {
	line := VersionLine(stdout)

	return r.Check(line, versionOf(line))
}

// VersionLine returns the first line of stdout, the output of a tool's --version: the only line a
// version is read from.
func VersionLine(stdout string) string {
	line, _, _ := strings.Cut(stdout, "\n")

	return line
}

// Argv returns the full argv of a call of tool with args.
func Argv(tool string, args []string) []string {
	return append([]string{tool}, args...)
}

// CommandLine returns argv joined by spaces, the way every error about a run of argv and the debug
// trace name the call.
func CommandLine(argv []string) string {
	return strings.Join(argv, " ")
}
