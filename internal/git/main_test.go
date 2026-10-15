package git_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
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

	// raceEnv names the environment variable that configures the race detector.
	raceEnv = "GORACE"
	// raceNoExitPause removes the race detector's pause at exit, which would otherwise cost a second
	// per run of the test binary.
	raceNoExitPause = "atexit_sleep_ms=0"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperModeEnv); mode != "" {
		os.Exit(diffHelper(mode))
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

	line, _, _ := strings.Cut(got.Stdout, "\n")

	return checkTestGit(os.Getenv(testGitVar), os.Getenv(testGitVersionVar), line)
}

// checkTestGit returns why a git whose `git --version` prints line first does not suit the tests,
// or nil when it does. The line must name a version that ils reads, and that version must suit the
// target testGitVar names and the version testGitVersionVar names, as testtarget.Tool.Check
// decides.
func checkTestGit(target, version, line string) error {
	found := git.VersionOf(line)
	if found == "" {
		return fmt.Errorf("git --version printed %q, which names no version the suite can read", line)
	}

	tool := testtarget.Tool{
		Name: "git", TargetVar: testGitVar, VersionVar: testGitVersionVar, Minimum: git.MinimumVersion,
	}

	return tool.Check(target, version, found)
}

func TestCheckTestGit_WhenGitSuitsTarget_ReturnsNil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		target  string
		version string
		line    string
	}{
		{name: "minimum target and the minimum", target: testtarget.Minimum, line: "git version " + git.MinimumVersion},
		{name: "latest target and the minimum", target: testtarget.Latest, line: "git version " + git.MinimumVersion},
		{name: "unset target and the minimum", line: "git version " + git.MinimumVersion},
		{
			name: "version wanted is the minimum found on the minimum target", target: testtarget.Minimum,
			version: git.MinimumVersion, line: "git version " + git.MinimumVersion,
		},
		{
			name: "version wanted is the git found on the latest target", target: testtarget.Latest,
			version: "2.56.0", line: "git version 2.56.0",
		},
		{name: "version wanted is the git found on the unset target", version: "2.54.0", line: "git version 2.54.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkTestGit(tt.target, tt.version, tt.line)

			assert.NilError(t, err, "checkTestGit(%q, %q, %q)", tt.target, tt.version, tt.line)
		})
	}
}

type unsuitedTestGit struct {
	name    string
	target  string
	version string
	line    string
	// want is the text of the error checkTestGit returns.
	want string
}

func unsuitedTestGits() []unsuitedTestGit {
	const (
		notMinimum = "ILS_TEST_GIT is minimum and the git found is %s, want the minimum ils enforces, 2.34.1"
		older      = "the git found is %s, older than the minimum ils enforces, 2.34.1"
		notWanted  = "the git found is %s, and ILS_TEST_GIT_VERSION wants %s"
		unreadable = "git --version printed %q, which names no version the suite can read"
	)

	return []unsuitedTestGit{
		{
			name: "minimum target and a newer git", target: testtarget.Minimum, line: "git version 2.56.0",
			want: fmt.Sprintf(notMinimum, "2.56.0"),
		},
		{
			name: "minimum target and an older git", target: testtarget.Minimum, line: "git version 2.30.2",
			want: fmt.Sprintf(notMinimum, "2.30.2"),
		},
		{
			name: "latest target and an older git", target: testtarget.Latest, line: "git version 2.30.2",
			want: fmt.Sprintf(older, "2.30.2"),
		},
		{name: "unset target and an older git", line: "git version 2.30.2", want: fmt.Sprintf(older, "2.30.2")},
		{
			name: "unknown target", target: "newest", line: "git version 2.56.0",
			want: `ILS_TEST_GIT is "newest", want minimum, latest, or unset`,
		},
		{
			name: "version wanted is not the git found", target: testtarget.Latest, version: "2.43.0",
			line: "git version 2.42.0", want: fmt.Sprintf(notWanted, "2.42.0", "2.43.0"),
		},
		{name: "development build", line: "git version DEV", want: fmt.Sprintf(unreadable, "git version DEV")},
		{
			name: "no readable version is refused before the minimum target", target: testtarget.Minimum,
			line: "git version DEV", want: fmt.Sprintf(unreadable, "git version DEV"),
		},
		{
			name: "no readable version is refused before the version wanted", version: "2.43.0",
			line: "git version DEV", want: fmt.Sprintf(unreadable, "git version DEV"),
		},
	}
}

func TestCheckTestGit_WhenGitDoesNotSuitTarget_ReturnsError(t *testing.T) {
	t.Parallel()
	for _, tt := range unsuitedTestGits() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkTestGit(tt.target, tt.version, tt.line)

			assert.Error(t, err, tt.want, "checkTestGit(%q, %q, %q)", tt.target, tt.version, tt.line)
		})
	}
}

// guardSees puts a git first on PATH that prints line as the first line of `git --version`, and
// sets the target testGitVar names and the version testGitVersionVar names. It cannot run in a
// parallel test: it sets the environment of the whole process, which the git the guard starts
// inherits.
func guardSees(t *testing.T, target, version, line string) {
	t.Helper()

	gittest.FakeGit(t, gittest.Fake{Version: line + "\n" + versionSecondLine})
	t.Setenv(testGitVar, target)
	t.Setenv(testGitVersionVar, version)
}

func TestGuardTestGit_WhenGitOnPathIsTheMinimum_ReturnsNil(t *testing.T) {
	guardSees(t, "", "", "git version "+git.MinimumVersion)

	err := guardTestGit()

	assert.NilError(t, err)
}

func TestGuardTestGit_WhenTheMinimumTargetFindsANewerGit_ReturnsNotMinimumError(t *testing.T) {
	guardSees(t, testtarget.Minimum, "", "git version 2.56.0")

	err := guardTestGit()

	assert.Error(t, err, "ILS_TEST_GIT is minimum and the git found is 2.56.0, want the minimum ils enforces, 2.34.1")
}

func TestGuardTestGit_WhenVersionWantedIsNotTheGitOnPath_ReturnsErrorNamingBoth(t *testing.T) {
	guardSees(t, testtarget.Latest, "2.43.0", "git version 2.42.0")

	err := guardTestGit()

	assert.Error(t, err, "the git found is 2.42.0, and ILS_TEST_GIT_VERSION wants 2.43.0")
}

func TestGuardTestGit_WhenGitMissingFromPath_ReturnsToolMissingError(t *testing.T) {
	// Not parallel: this empties PATH for the whole process while it runs.
	t.Setenv("PATH", t.TempDir())

	err := guardTestGit()

	_, ok := errors.AsType[*child.ToolMissingError](err)
	assert.Check(t, ok, "guardTestGit error = %v, want a *child.ToolMissingError", err)
	assert.Check(t, is.ErrorContains(err, "read the version of the git on PATH: "))
}

func TestMain_WhenTestGitTargetIsUnknown_ExitsBeforeTheTests(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	assert.NilError(t, err)

	var stderr bytes.Buffer

	//nolint:gosec // G204: the test binary starts itself; nothing here comes from outside the test.
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^$")
	cmd.Env = append(os.Environ(), testGitVar+"=newest", raceEnv+"="+raceNoExitPause)
	cmd.Stderr = &stderr

	err = cmd.Run()

	exitErr, ok := errors.AsType[*exec.ExitError](err)
	assert.Assert(t, ok, "test binary error = %v, want it to exit non-zero", err)
	assert.Check(t, is.Equal(exitErr.ExitCode(), 1))
	assert.Check(t, is.Contains(stderr.String(), `ILS_TEST_GIT is "newest", want minimum, latest, or unset`))
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
	binary, err := os.Executable()
	if err != nil {
		return 1
	}

	//nolint:gosec // G204: the test binary starts itself; nothing here comes from outside the test.
	lingerer := exec.CommandContext(context.Background(), binary)
	lingerer.Env = append(os.Environ(), helperModeEnv+"="+helperLinger)
	lingerer.Stdout = os.Stdout
	lingerer.Stderr = os.Stderr

	if err = lingerer.Start(); err != nil {
		return 1
	}

	return 0
}
