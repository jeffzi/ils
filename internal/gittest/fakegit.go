package gittest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const (
	// FailedCode is the exit code of a call the fake git fails, the code of a failed git read.
	FailedCode = 128
	// FailedSaid is the line a call the fake git fails prints to stderr.
	FailedSaid = "fatal: this read was made to fail"

	// realGitEnv holds the path of the real git; its presence makes the test binary act as git.
	realGitEnv = "ILS_GITTEST_REAL_GIT"
	// versionEnv holds what `git --version` prints.
	versionEnv = "ILS_GITTEST_VERSION"
	// failOnEnv holds the words a failing call starts with, separated by failOnSep; no git
	// argument holds a line break.
	failOnEnv = "ILS_GITTEST_FAIL_ON"
	failOnSep = "\n"
)

// Fake says how the git FakeGit puts on PATH answers. A call it does not fake runs the real git.
type Fake struct {
	// Version, when not empty, is what `git --version` prints, followed by a line break.
	Version string
	// FailOn, when not empty, makes a call whose arguments start with these words exit with
	// FailedCode and print FailedSaid to stderr.
	FailOn []string
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

	return serveGit(realGit, os.Args[1:]), true
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

	self, err := os.Executable()
	if err != nil {
		tb.Fatalf("gittest: find the test binary: %v", err)
	}

	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}

	dir := tb.TempDir()
	if err := os.Link(self, filepath.Join(dir, name)); err != nil {
		tb.Fatalf("gittest: link the test binary as git: %v", err)
	}

	settings := []string{
		realGitEnv + "=" + realGit,
		// A binary built with -race sleeps a second before every exit unless told not to, which
		// each git call would pay.
		"GORACE=" + strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"),
	}
	if fake.Version != "" {
		settings = append(settings, versionEnv+"="+fake.Version)
	}

	if len(fake.FailOn) > 0 {
		settings = append(settings, failOnEnv+"="+strings.Join(fake.FailOn, failOnSep))
	}

	for _, setting := range settings {
		key, value, _ := strings.Cut(setting, "=")
		tb.Setenv(key, value)
	}

	tb.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return settings
}

// serveGit answers one call of the fake git with args and returns its exit code.
func serveGit(realGit string, args []string) int {
	if version, ok := os.LookupEnv(versionEnv); ok && slices.Equal(args, []string{"--version"}) {
		if _, err := fmt.Fprintln(os.Stdout, version); err != nil {
			return 1
		}

		return 0
	}

	if words := os.Getenv(failOnEnv); words != "" {
		failOn := strings.Split(words, failOnSep)
		if len(args) >= len(failOn) && slices.Equal(args[:len(failOn)], failOn) {
			_, _ = fmt.Fprintln(os.Stderr, FailedSaid)

			return FailedCode
		}
	}

	return runRealGit(realGit, args)
}

// runRealGit runs the real git with args, the environment, and the standard streams, and returns
// its exit code.
func runRealGit(realGit string, args []string) int {
	//nolint:gosec // G204: the fake names the real git and passes on the arguments it was given.
	cmd := exec.CommandContext(context.Background(), realGit, args...)
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

	_, _ = fmt.Fprintln(os.Stderr, err)

	return 1
}
