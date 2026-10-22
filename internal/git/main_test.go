package git_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/quiet"
	"github.com/jeffzi/ils/internal/testtarget"
)

const (
	// helperModeEnv makes the test binary act as git's external diff program instead of running tests.
	helperModeEnv = "ILS_GIT_TEST_DIFF_HELPER"
	// helperAddrEnv holds the local address a lingering helper connects to once it has started.
	helperAddrEnv = "ILS_GIT_TEST_DIFF_HELPER_ADDR"

	// helperLinger makes the helper outlive git, holding the output pipes it inherited.
	helperLinger = "linger"
	// helperSpawn makes the helper start a lingering copy of itself and exit at once, so that git
	// exits zero while a process it started still holds its output pipes.
	helperSpawn = "spawn"

	// sshStandInEnv makes the test binary act as an ssh command a user configured: standInAnswer
	// followed by a name answers `-G <host>` with a config whose hostname is that name,
	// standInSuffix followed by a suffix answers with the host it was asked for and that suffix,
	// standInWorkDir answers with the name of the directory it runs in, standInNoHostname answers
	// with a config that has no hostname line, standInRefuse exits non-zero with standInRefusal on
	// stderr, standInExit followed by a number exits with that code and standInRefusal on stderr, and
	// standInLinger starts a lingering helper that inherits its output and waits for it.
	sshStandInEnv     = "ILS_GIT_TEST_SSH_STAND_IN"
	standInAnswer     = "hostname="
	standInSuffix     = "suffix="
	standInWorkDir    = "work-dir"
	standInNoHostname = "no-hostname"
	standInRefuse     = "refuse"
	standInExit       = "exit="
	standInLinger     = "linger"
	standInRefusal    = "stand-in refused the call"

	// plainSSHEnv makes the test binary act as plain ssh in the stand-in mode it names when called as
	// plain ssh is, `-G -- <host>`, so that a configured command and plain ssh that share one
	// environment answer apart; any other call takes its mode from sshStandInEnv.
	plainSSHEnv = "ILS_GIT_TEST_PLAIN_SSH"

	// garbledGitEnv makes the test binary act as a git that prints the variable's value for any call
	// and exits zero, an answer no read of git's output can parse.
	garbledGitEnv = "ILS_GIT_TEST_GARBLED_GIT"

	// originHostEnv makes the test binary print the host OriginURLs reports for the fetch URL of the
	// repository its value names, read by a runner that sets no environment and so inherits this
	// process's.
	originHostEnv = "ILS_GIT_TEST_ORIGIN_HOST"

	// helperLife bounds how long a lingering helper stays when nothing releases it.
	helperLife = 30 * time.Second

	// versionSecondLine follows the line a fake git prints for `git --version`, so that a read of
	// more than the first line shows up as a wrong version.
	versionSecondLine = "cpu: x86_64"

	// testGitVar names the git the tests must find on PATH: testtarget.Minimum asks for exactly the
	// minimum ils enforces, and testtarget.Latest or unset for that version or newer.
	testGitVar = "ILS_TEST_GIT"
	// testGitVersionVar, when set, is the exact version of git the tests must find on PATH.
	testGitVersionVar = "ILS_TEST_GIT_VERSION"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperModeEnv); mode != "" {
		os.Exit(diffHelper(mode))
	}

	if garbled, ok := os.LookupEnv(garbledGitEnv); ok {
		fmt.Println(garbled)
		os.Exit(0)
	}

	if mode, ok := standInMode(os.Args[1:]); ok {
		os.Exit(sshStandIn(mode, os.Args[1:]))
	}

	if dir := os.Getenv(originHostEnv); dir != "" {
		os.Exit(printOriginHost(dir))
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

// fakeGitVersion puts a git first on PATH that prints line, then versionSecondLine, for `git --version`,
// and returns the settings that make it so. It cannot run in a parallel test: it sets PATH for the
// whole process.
func fakeGitVersion(t *testing.T, line string) []string {
	t.Helper()

	return gittest.FakeGit(t, gittest.Fake{Version: line + "\n" + versionSecondLine})
}

func TestGuardTestGit_WhenGitSuitsTarget_ReturnsNil(t *testing.T) {
	// Not parallel: this sets the environment of the whole process, which the git the guard starts
	// inherits.
	fakeGitVersion(t, "git version "+git.MinimumVersion)
	t.Setenv(testGitVar, testtarget.Minimum)
	t.Setenv(testGitVersionVar, "")

	err := guardTestGit()

	assert.NilError(t, err)
}

func TestGuardTestGit_WhenGitDoesNotSuitTarget_ReturnsError(t *testing.T) {
	// Not parallel: each row sets the environment of the whole process, which the git the guard
	// starts inherits.
	tests := []struct {
		name    string
		target  string
		version string
		line    string
		// want is the text of the error guardTestGit returns.
		want string
	}{
		{
			name: "minimum target finds a newer git", target: testtarget.Minimum, line: "git version 99.0.0",
			want: "ILS_TEST_GIT is minimum and the git found is 99.0.0, want the minimum ils enforces, " + git.MinimumVersion,
		},
		{
			name: "version wanted is not the git on PATH", target: testtarget.Latest, version: "2.43.0",
			line: "git version 2.42.0", want: "the git found is 2.42.0, and ILS_TEST_GIT_VERSION wants 2.43.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeGitVersion(t, tt.line)
			t.Setenv(testGitVar, tt.target)
			t.Setenv(testGitVersionVar, tt.version)

			err := guardTestGit()

			assert.Error(t, err, tt.want)
		})
	}
}

func TestGuardTestGit_WhenGitMissingFromPath_ReturnsToolMissingError(t *testing.T) {
	// Not parallel: this empties PATH for the whole process while it runs.
	t.Setenv("PATH", t.TempDir())

	err := guardTestGit()

	_, ok := errors.AsType[*child.ToolMissingError](err)
	assert.Check(t, ok, "guardTestGit error = %v, want a *child.ToolMissingError", err)
	assert.Check(t, is.ErrorContains(err, "read the version of the git on PATH: "))
}

// runSelf runs the test binary with env and args, quieting the race detector's report in it, and
// returns what it printed on stdout and stderr and how it ended.
func runSelf(t *testing.T, env []string, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	binary, err := os.Executable()
	assert.NilError(t, err)

	var out, errOut bytes.Buffer

	//nolint:gosec // G204: the test binary starts itself; nothing here comes from outside the test.
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Env = append(slices.Clip(env), "GORACE="+quiet.Setting())
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	err = cmd.Run()

	return out.String(), errOut.String(), err
}

func TestMain_WhenTestGitTargetIsUnknown_ExitsBeforeTheTests(t *testing.T) {
	t.Parallel()
	env := append(os.Environ(), testGitVar+"=newest")

	_, stderr, err := runSelf(t, env, "-test.run=^$")

	exitErr, ok := errors.AsType[*exec.ExitError](err)
	assert.Assert(t, ok, "test binary error = %v, want it to exit non-zero", err)
	assert.Check(t, is.Equal(exitErr.ExitCode(), 1))
	assert.Check(t, is.Contains(stderr, `ILS_TEST_GIT is "newest", want minimum, latest, or unset`))
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
// the modes that refuse, exit, linger, print no hostname or print the directory answer any call.
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
			return 2
		}

		return n
	}

	if mode == standInNoHostname {
		fmt.Print("user git\nport 22\n")

		return 0
	}

	if mode == standInWorkDir {
		dir, err := os.Getwd()
		if err != nil {
			return 2
		}

		fmt.Printf("user git\nhostname %s\nport 22\n", filepath.Base(dir))

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

// printOriginHost prints the host OriginURLs reports for the fetch URL of the repository at dir,
// read by a runner with no environment of its own, and returns the exit code.
func printOriginHost(dir string) int {
	urls, err := git.Repo{Runner: git.Runner{Dir: dir}}.OriginURLs(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	fmt.Print(urls.Fetch.Host)

	return 0
}

// diffHelper plays git's external diff program and returns its exit code. In the lingering mode it
// connects to the address in the environment, then stays until that connection closes or its life is up.
func diffHelper(mode string) int {
	if mode == helperSpawn {
		return spawnLingerer()
	}

	ctx, cancel := context.WithTimeout(context.Background(), helperLife)
	defer cancel()

	var dialer net.Dialer

	conn, err := dialer.DialContext(ctx, "tcp", os.Getenv(helperAddrEnv))
	if err != nil {
		return 1
	}

	defer func() { _ = conn.Close() }()

	deadline, _ := ctx.Deadline()
	if err = conn.SetReadDeadline(deadline); err != nil {
		return 1
	}

	// The test never writes: the read returns when it closes the connection or the deadline passes.
	_, _ = conn.Read(make([]byte, 1))

	return 0
}

// spawnLingerer starts a lingering helper that inherits this process's output and does not wait for it.
func spawnLingerer() int {
	if _, err := startLingerer(); err != nil {
		return 1
	}

	return 0
}

// waitForLingerer starts a lingering helper that inherits this process's output and waits for it,
// so that this process is still running when the helper has connected.
func waitForLingerer() int {
	lingerer, err := startLingerer()
	if err != nil {
		return 1
	}

	if err = lingerer.Wait(); err != nil {
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
