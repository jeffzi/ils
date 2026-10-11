package landing_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const (
	// failingWordsEnv makes the test binary act as git instead of running tests: a call whose
	// arguments start with the words it holds fails, and every other call runs the real git.
	failingWordsEnv = "ILS_LANDING_TEST_GIT_FAILS_ON"
	// failingWordsSep separates the words in failingWordsEnv; no git argument holds a line break.
	failingWordsSep = "\n"
	// realGitEnv holds the absolute path of the real git.
	realGitEnv = "ILS_LANDING_TEST_REAL_GIT"

	// failingGitExit and failingGitSaid are the exit code and the stderr line of the call that fails.
	failingGitExit = 128
	failingGitSaid = "fatal: this read was made to fail"
)

func TestMain(m *testing.M) {
	if words := os.Getenv(failingWordsEnv); words != "" {
		os.Exit(failingGit(strings.Split(words, failingWordsSep), os.Args[1:]))
	}

	os.Exit(m.Run())
}

// failingGit plays git and returns its exit code: it fails when args start with words, and
// otherwise runs the real git with the same arguments, environment, and standard streams.
func failingGit(words, args []string) int {
	if len(args) >= len(words) && slices.Equal(args[:len(words)], words) {
		fmt.Fprintln(os.Stderr, failingGitSaid)

		return failingGitExit
	}

	//nolint:gosec // G204: the test names the real git and passes on the arguments it was given.
	real := exec.CommandContext(context.Background(), os.Getenv(realGitEnv), args...)
	real.Stdin = os.Stdin
	real.Stdout = os.Stdout
	real.Stderr = os.Stderr

	err := real.Run()
	if err == nil {
		return 0
	}

	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode()
	}

	fmt.Fprintln(os.Stderr, err)

	return 1
}
