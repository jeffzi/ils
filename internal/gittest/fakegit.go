package gittest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/jeffzi/ils/internal/quiet"
)

const (
	// FailedCode is the exit code of a call the fake git fails, the code of a failed git read.
	FailedCode = 128
	// FailedSaid is the line a call the fake git fails prints to stderr.
	FailedSaid = "fatal: this read was made to fail"
	// BrokenCode is the exit code of a fake git that could not do its own work: start the real git,
	// print the faked version, or stall as asked. It is none of the codes internal/git reads as an
	// answer (0, 1 and 2) and not FailedCode, the code of a failed git read, so a broken fake never
	// passes for git's reply.
	BrokenCode = 125

	// realGitEnv holds the path of the real git; its presence makes the test binary act as git.
	realGitEnv = "ILS_GITTEST_REAL_GIT"
	// versionEnv holds what `git --version` prints.
	versionEnv = "ILS_GITTEST_VERSION"
	// failOnEnv holds the words a failing call starts with, separated by wordsSep.
	failOnEnv = "ILS_GITTEST_FAIL_ON"
	// stallOnEnv holds the words a stalling call starts with, separated by wordsSep.
	stallOnEnv = "ILS_GITTEST_STALL_ON"
	// stallAtEnv holds the address a stalling call connects to.
	stallAtEnv = "ILS_GITTEST_STALL_AT"
	// wordsSep separates the words a variable holds; no git argument holds a line break.
	wordsSep = "\n"
)

// Fake says how the git FakeGit puts on PATH answers. A call it does not fake runs the real git.
type Fake struct {
	// Version, when not empty, is what `git --version` prints, followed by a line break.
	Version string
	// StallAt is the address a call StallOn names connects to.
	StallAt string
	// FailOn, when not empty, makes a call whose arguments start with these words exit with
	// FailedCode and print FailedSaid to stderr.
	FailOn []string
	// StallOn, when not empty, makes a call whose arguments start with these words connect to the
	// address StallAt and wait there until it is killed. A test that hands in the address of a
	// SilentListener learns when the call is under way, and can end its context then.
	StallOn []string
}

// Main makes the test binary act as git when FakeGit arranged for it, and reports whether it did.
//
// A package's TestMain calls it before anything else and, when served is true, exits with code
// without running a test.
func Main() (code int, served bool) {
	realGit := os.Getenv(realGitEnv)
	if realGit == "" {
		return 0, false
	}

	return serveGit(context.Background(), realGit, os.Args[1:]), true
}

// FakeGit puts the test binary first on PATH under git's name for the rest of the test, acting as
// git as fake says once the package's TestMain calls Main.
//
// It sets the process environment, so the test cannot be parallel, and a git runner that inherits
// that environment reaches the fake. It returns the variables the fake reads, for a runner whose
// environment is given whole: such a runner appends them to it.
func FakeGit(tb testing.TB, fake Fake) []string {
	tb.Helper()

	realGit, err := exec.LookPath("git")
	if err != nil {
		tb.Fatalf("gittest: find the real git: %v", err)
	}

	var settings []string

	set := func(key, value string) {
		tb.Setenv(key, value)

		settings = append(settings, key+"="+value)
	}

	set(realGitEnv, realGit)
	set("GORACE", quiet.Setting())

	if fake.Version != "" {
		set(versionEnv, fake.Version)
	}

	if len(fake.FailOn) > 0 {
		set(failOnEnv, strings.Join(fake.FailOn, wordsSep))
	}

	if len(fake.StallOn) > 0 {
		set(stallOnEnv, strings.Join(fake.StallOn, wordsSep))
		set(stallAtEnv, fake.StallAt)
	}

	LinkAs(tb, "git")

	return settings
}

// LinkAs puts the test binary first on PATH under the name of tool for the rest of the test, so
// that a call of tool runs the test binary, whose TestMain decides how it answers.
//
// It sets the process environment, so the test cannot be parallel.
func LinkAs(tb testing.TB, tool string) {
	tb.Helper()

	self, err := os.Executable()
	if err != nil {
		tb.Fatalf("gittest: find the test binary: %v", err)
	}

	name := tool
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	dir := tb.TempDir()
	if err := os.Link(self, filepath.Join(dir, name)); err != nil {
		tb.Fatalf("gittest: link the test binary as %s: %v", tool, err)
	}

	tb.Setenv("PATH", firstOnPath(dir, os.Getenv("PATH")))
}

// firstOnPath returns the search path path with dir put first. An empty path gives dir alone: a
// trailing list separator would add an empty entry, which puts the current directory on the search
// path.
func firstOnPath(dir, path string) string {
	if path == "" {
		return dir
	}

	return dir + string(os.PathListSeparator) + path
}

// serveGit answers one call of the fake git with args and returns its exit code.
func serveGit(ctx context.Context, realGit string, args []string) int {
	if version, ok := os.LookupEnv(versionEnv); ok && slices.Equal(args, []string{"--version"}) {
		if _, err := fmt.Fprintln(os.Stdout, version); err != nil {
			return broken(fmt.Errorf("print the faked version: %w", err))
		}

		return 0
	}

	if startsWith(args, failOnEnv) {
		if _, err := fmt.Fprintln(os.Stderr, FailedSaid); err != nil {
			return broken(fmt.Errorf("print the failure: %w", err))
		}

		return FailedCode
	}

	if startsWith(args, stallOnEnv) {
		return stall(ctx, os.Getenv(stallAtEnv))
	}

	return runRealGit(ctx, realGit, args)
}

// startsWith reports whether args start with the words the variable key holds, and false when it
// holds none.
func startsWith(args []string, key string) bool {
	words := os.Getenv(key)
	if words == "" {
		return false
	}

	prefix := strings.Split(words, wordsSep)

	return len(args) >= len(prefix) && slices.Equal(args[:len(prefix)], prefix)
}

// stall connects to addr and waits for the server to close the connection, which a SilentListener
// does only once the test ends: the caller kills the fake git before then. It returns BrokenCode,
// since a stalled call that ends on its own did not do what the test asked of it.
func stall(ctx context.Context, addr string) int {
	var dialer net.Dialer

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return broken(fmt.Errorf("connect to %s to stall: %w", addr, err))
	}

	// The server never writes, so the copy returns only once it closes the connection.
	_, copyErr := io.Copy(io.Discard, conn)
	closeErr := conn.Close()

	return broken(errors.Join(fmt.Errorf("the server at %s closed the stalled call", addr), copyErr, closeErr))
}

// runRealGit runs the real git with args, the environment, and the standard streams, and returns
// its exit code, or BrokenCode when it cannot start it.
func runRealGit(ctx context.Context, realGit string, args []string) int {
	//nolint:gosec // G204: the fake names the real git and passes on the arguments it was given.
	cmd := exec.CommandContext(ctx, realGit, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err == nil {
		return 0
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode()
	}

	return broken(fmt.Errorf("start the real git: %w", err))
}

// broken prints why the fake git could not do its own work to stderr and returns BrokenCode.
func broken(err error) int {
	_, _ = fmt.Fprintf(os.Stderr, "gittest: %v\n", err)

	return BrokenCode
}
