package git_test

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/testtarget"
)

const (
	// helperAddrEnv holds the local address the lingering copy of a standInLinger call holds.
	helperAddrEnv = "ILS_GIT_TEST_HELPER_ADDR"

	// sshStandInEnv makes the test binary act as an ssh command a user configured: standInAnswer
	// followed by a name answers `-G <host>` with a config whose hostname is that name,
	// standInSuffix followed by a suffix answers with the host it was asked for and that suffix,
	// standInNoHostname answers with a config that has no hostname line, standInExit followed by a
	// number exits with that code and standInRefusal on stderr, standInRefuse is standInExit with
	// code 3, and standInLinger starts a lingering helper that inherits its output and waits for it.
	sshStandInEnv     = "ILS_GIT_TEST_SSH_STAND_IN"
	standInAnswer     = "hostname="
	standInSuffix     = "suffix="
	standInNoHostname = "no-hostname"
	standInExit       = "exit="
	standInRefuse     = standInExit + "3"
	standInLinger     = "linger"
	standInRefusal    = "stand-in refused the call"

	// plainSSHEnv makes the test binary act as plain ssh in the stand-in mode it names when called as
	// plain ssh is, `-G -- <host>`, so that a configured command and plain ssh that share one
	// environment answer apart; any other call takes its mode from sshStandInEnv.
	plainSSHEnv = "ILS_GIT_TEST_PLAIN_SSH"

	// testGitVar names the git the tests must find on PATH: testtarget.Minimum asks for exactly the
	// minimum ils enforces, and testtarget.Latest or unset for that version or newer.
	testGitVar = "ILS_TEST_GIT"
	// testGitVersionVar, when set, is the exact version of git the tests must find on PATH.
	testGitVersionVar = "ILS_TEST_GIT_VERSION"
)

func TestMain(m *testing.M) {
	// First: the lingering copy of a standInLinger call inherits sshStandInEnv, and would start
	// another copy of itself as the stand-in, without end.
	if code, served := gittest.Linger(); served {
		os.Exit(code)
	}

	if mode, ok := standInMode(os.Args[1:]); ok {
		os.Exit(sshStandIn(mode, os.Args[1:]))
	}

	if code, served := gittest.Main(); served {
		os.Exit(code)
	}

	if err := guardTestGit(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// guardTestGit returns why the git on PATH does not suit the tests, or nil when it does.
func guardTestGit() error {
	got, err := git.Runner{}.Run(context.Background(), "--version")
	if err != nil {
		return fmt.Errorf("read the version of the git on PATH: %w", err)
	}

	tool := testtarget.Tool{
		Name: "git", TargetVar: testGitVar, VersionVar: testGitVersionVar, MinimumVersion: git.MinimumVersion,
	}

	return tool.CheckEnvOutput(got.Stdout, git.VersionOf)
}

// standInMode returns the stand-in mode of a call of the test binary with args, and whether the
// environment makes the call one of a stand-in ssh: plainSSHEnv for a call shaped as plain ssh's,
// and sshStandInEnv for any other. A call that only plainSSHEnv marks gets no mode, which the
// stand-in refuses.
func standInMode(args []string) (string, bool) {
	plain, plainSet := os.LookupEnv(plainSSHEnv)
	if plainSet && slices.Contains(args, "--") {
		return plain, true
	}

	if mode, ok := os.LookupEnv(sshStandInEnv); ok {
		return mode, true
	}

	return "", plainSet
}

// sshStandIn plays the ssh command mode names, called with args, and returns its exit code. It
// answers `-G <host>`, the call of a configured command, and `-G -- <host>`, the call of plain ssh;
// the modes that refuse, exit, linger or print no hostname answer any call.
func sshStandIn(mode string, args []string) int {
	if mode == standInLinger {
		return waitForLingerer()
	}

	if code, exits := strings.CutPrefix(mode, standInExit); exits {
		fmt.Fprintln(os.Stderr, standInRefusal)

		n, err := strconv.Atoi(code)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stand-in: read the exit code of mode %q: %v\n", mode, err)

			return 2
		}

		return n
	}

	if mode == standInNoHostname {
		fmt.Print("user git\nport 22\n")

		return 0
	}

	args = slices.DeleteFunc(slices.Clone(args), func(arg string) bool { return arg == "--" })

	name, answers := strings.CutPrefix(mode, standInAnswer)
	suffix, suffixes := strings.CutPrefix(mode, standInSuffix)
	known := answers || suffixes
	wellFormed := len(args) == 2 && args[0] == "-G"
	if !known || !wellFormed {
		fmt.Fprintf(os.Stderr, "stand-in in mode %q called with %q, want -G [--] <host>\n", mode, args)

		return 2
	}

	if suffixes {
		name = args[1] + suffix
	}

	fmt.Printf("user git\nhostname %s\nport 22\n", name)

	return 0
}

// waitForLingerer starts a lingering copy of the test binary that inherits this process's output
// and holds the address in helperAddrEnv, and waits for it, so that this process is still running
// when the copy has connected.
func waitForLingerer() int {
	lingerer, err := gittest.StartLingerer(context.Background(), os.Getenv(helperAddrEnv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "stand-in: %v\n", err)

		return 1
	}

	if err = lingerer.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "stand-in: wait for the lingering copy: %v\n", err)

		return 1
	}

	return 0
}
