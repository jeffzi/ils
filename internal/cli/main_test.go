package cli_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"testing"

	"github.com/jeffzi/ils/internal/github/ghtest"
)

const (
	fangWidth = "__FANG_TEST_WIDTH"

	// helpWidth keeps every entry of the help on one line, which entries in cli_test.go relies on.
	helpWidth = "120"

	// gitVersionEnv makes the test binary act as git instead of running tests: `git --version`
	// prints the line it holds, and every other call runs the real git.
	gitVersionEnv = "ILS_CLI_TEST_GIT_VERSION_LINE"
	// realGitEnv holds the absolute path of the real git.
	realGitEnv = "ILS_CLI_TEST_REAL_GIT"
)

func TestMain(m *testing.M) {
	if code, served := ghtest.Main(); served {
		os.Exit(code)
	}

	if line, ok := os.LookupEnv(gitVersionEnv); ok {
		os.Exit(versionedGit(line, os.Args[1:]))
	}

	// Fang reads the width of the help once for the process, from the terminal unless this is set.
	if err := os.Setenv(fangWidth, helpWidth); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// versionedGit plays git and returns its exit code: it prints line when args ask for the version,
// and otherwise runs the real git with the same arguments, environment, and standard streams.
func versionedGit(line string, args []string) int {
	if slices.Equal(args, []string{"--version"}) {
		fmt.Println(line)

		return 0
	}

	//nolint:gosec // G204: the test names the real git and passes on the arguments it was given.
	cmd := exec.CommandContext(context.Background(), os.Getenv(realGitEnv), args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err == nil {
		return 0
	}

	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode()
	}

	fmt.Fprintln(os.Stderr, err)

	return 1
}
