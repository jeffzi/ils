package git_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/testtarget"
)

const (
	// helperModeEnv makes the test binary act, instead of running tests, as a program that leaves a
	// lingering copy of itself holding the output pipes it inherited, in the mode the variable names.
	helperModeEnv = "ILS_GIT_TEST_HELPER"
	// helperAddrEnv holds the local address a lingering helper connects to once it has started.
	helperAddrEnv = "ILS_GIT_TEST_HELPER_ADDR"

	// helperLinger makes the helper hold the address in helperAddrEnv until the test ends.
	helperLinger = "linger"
	// helperSpawn makes the helper print helperSaid, start a lingering copy of itself and exit zero
	// at once, so that a process it started still holds its output pipes after it exited.
	helperSpawn = "spawn"
	// helperWait makes the helper print helperSaid on stdout and stderr, then start a lingering copy
	// of itself and wait for it, so that it is still running once that copy has connected.
	helperWait = "wait"
	// helperSaid is what the helper prints before it starts its lingering copy.
	helperSaid = "helper started"

	// sshStandInEnv makes the test binary act as an ssh command a user configured: standInAnswer
	// followed by a name answers `-G <host>` with a config whose hostname is that name,
	// standInSuffix followed by a suffix answers with the host it was asked for and that suffix,
	// standInNoHostname answers with a config that has no hostname line, standInRefuse exits non-zero
	// with standInRefusal on stderr, standInExit followed by a number exits with that code and
	// standInRefusal on stderr, and standInLinger starts a lingering helper that inherits its output
	// and waits for it.
	sshStandInEnv     = "ILS_GIT_TEST_SSH_STAND_IN"
	standInAnswer     = "hostname="
	standInSuffix     = "suffix="
	standInNoHostname = "no-hostname"
	standInRefuse     = "refuse"
	standInExit       = "exit="
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
	if mode := os.Getenv(helperModeEnv); mode != "" {
		os.Exit(helper(mode))
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

	return tool.CheckOutput(os.Getenv(testGitVar), os.Getenv(testGitVersionVar), got.Stdout, git.VersionOf)
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

	if mode == standInRefuse {
		fmt.Fprintln(os.Stderr, standInRefusal)

		return 3
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

// helper plays the program of the lingering mode it is given and returns its exit code.
func helper(mode string) int {
	switch mode {
	case helperLinger:
		return gittest.Hold(context.Background(), os.Getenv(helperAddrEnv))
	case helperSpawn:
		fmt.Println(helperSaid)

		return spawnLingerer()
	case helperWait:
		fmt.Println(helperSaid)
		fmt.Fprintln(os.Stderr, helperSaid)

		return waitForLingerer()
	default:
		fmt.Fprintf(os.Stderr, "helper: unknown mode %q\n", mode)

		return 2
	}
}

// spawnLingerer starts a lingering helper that inherits this process's output and does not wait for it.
func spawnLingerer() int {
	if _, err := startLingerer(); err != nil {
		fmt.Fprintf(os.Stderr, "helper: %v\n", err)

		return 1
	}

	return 0
}

// waitForLingerer starts a lingering helper that inherits this process's output and waits for it,
// so that this process is still running when the helper has connected.
func waitForLingerer() int {
	lingerer, err := startLingerer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: %v\n", err)

		return 1
	}

	if err = lingerer.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "helper: wait for the lingering helper: %v\n", err)

		return 1
	}

	return 0
}

// startLingerer starts a lingering copy of the test binary that inherits this process's output.
func startLingerer() (*exec.Cmd, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find the test binary: %w", err)
	}

	//nolint:gosec // G204: the test binary starts itself; nothing here comes from outside the test.
	lingerer := exec.CommandContext(context.Background(), binary)
	lingerer.Env = append(os.Environ(), helperModeEnv+"="+helperLinger)
	lingerer.Stdout = os.Stdout
	lingerer.Stderr = os.Stderr

	if err = lingerer.Start(); err != nil {
		return nil, fmt.Errorf("start the lingering helper: %w", err)
	}

	return lingerer, nil
}
