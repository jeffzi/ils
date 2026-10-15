package git_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/semver"
	"gotest.tools/v3/assert"

	"github.com/jeffzi/ils/internal/git"
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

	// versionLineEnv makes the test binary act as a git whose `git --version` prints the line it
	// holds, followed by a second line, instead of running tests.
	versionLineEnv = "ILS_GIT_TEST_VERSION_LINE"
	// versionSecondLine follows the version line so that a read of more than the first line shows
	// up as a wrong version.
	versionSecondLine = "cpu: x86_64"

	// testGitVar names the git the tests must find on PATH: testGitMinimum asks for exactly the
	// minimum ils enforces, and testGitLatest or unset for that version or newer.
	testGitVar     = "ILS_TEST_GIT"
	testGitMinimum = "minimum"
	testGitLatest  = "latest"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperModeEnv); mode != "" {
		os.Exit(diffHelper(mode))
	}

	if line, ok := os.LookupEnv(versionLineEnv); ok {
		fmt.Println(line + "\n" + versionSecondLine)
		os.Exit(0)
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

	return checkTestGit(os.Getenv(testGitVar), line)
}

// checkTestGit returns why a git whose `git --version` prints line first does not suit the leg
// that testGitVar names, or nil when it does.
func checkTestGit(leg, line string) error {
	found := git.VersionOf(line)

	switch leg {
	case testGitMinimum:
		if found != git.MinimumVersion {
			return fmt.Errorf("%s is %s and the git found prints %q, want the minimum ils enforces, %s",
				testGitVar, leg, line, git.MinimumVersion)
		}
	case "", testGitLatest:
		if semver.Compare("v"+found, "v"+git.MinimumVersion) < 0 {
			return fmt.Errorf("the git found prints %q, older than the minimum ils enforces, %s", line, git.MinimumVersion)
		}
	default:
		return fmt.Errorf("%s is %q, want %s, %s, or unset", testGitVar, leg, testGitMinimum, testGitLatest)
	}

	return nil
}

func TestCheckTestGit_WhenGitSuitsLeg_ReturnsNil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		leg  string
		line string
	}{
		{name: "minimum leg and the minimum", leg: testGitMinimum, line: "git version " + git.MinimumVersion},
		{name: "latest leg and the minimum", leg: testGitLatest, line: "git version " + git.MinimumVersion},
		{name: "latest leg and a newer git", leg: testGitLatest, line: "git version 2.56.0.windows.2"},
		{name: "unset leg and the minimum", line: "git version " + git.MinimumVersion},
		{name: "unset leg and a newer git", line: "git version 2.54.0 (Apple Git-157)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkTestGit(tt.leg, tt.line)

			assert.NilError(t, err, "checkTestGit(%q, %q)", tt.leg, tt.line)
		})
	}
}

func TestCheckTestGit_WhenGitDoesNotSuitLeg_ReturnsError(t *testing.T) {
	t.Parallel()

	const (
		notMinimum = "ILS_TEST_GIT is minimum and the git found prints %q, want the minimum ils enforces, 2.34.1"
		older      = "the git found prints %q, older than the minimum ils enforces, 2.34.1"
	)

	tests := []struct {
		name string
		leg  string
		line string
		want string
	}{
		{
			name: "minimum leg and a newer git", leg: testGitMinimum, line: "git version 2.56.0",
			want: fmt.Sprintf(notMinimum, "git version 2.56.0"),
		},
		{
			name: "minimum leg and an older git", leg: testGitMinimum, line: "git version 2.30.2",
			want: fmt.Sprintf(notMinimum, "git version 2.30.2"),
		},
		{
			name: "minimum leg and no readable version", leg: testGitMinimum, line: "git version DEV",
			want: fmt.Sprintf(notMinimum, "git version DEV"),
		},
		{
			name: "latest leg and an older git", leg: testGitLatest, line: "git version 2.30.2",
			want: fmt.Sprintf(older, "git version 2.30.2"),
		},
		{
			name: "latest leg and no readable version", leg: testGitLatest, line: "git version DEV",
			want: fmt.Sprintf(older, "git version DEV"),
		},
		{name: "unset leg and an older git", line: "git version 2.30.2", want: fmt.Sprintf(older, "git version 2.30.2")},
		{name: "unset leg and no readable version", line: "git version DEV", want: fmt.Sprintf(older, "git version DEV")},
		{
			name: "unknown leg", leg: "newest", line: "git version 2.56.0",
			want: `ILS_TEST_GIT is "newest", want minimum, latest, or unset`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkTestGit(tt.leg, tt.line)

			assert.Error(t, err, tt.want, "checkTestGit(%q, %q)", tt.leg, tt.line)
		})
	}
}

// guardSees puts a git first on PATH that prints line as the first line of `git --version`, and
// sets the leg testGitVar names. It cannot run in a parallel test: it sets the environment of the
// whole process, which the git the guard starts inherits.
func guardSees(t *testing.T, leg, line string) {
	t.Helper()

	fakeGitRepo(t, line)
	t.Setenv(versionLineEnv, line)
	t.Setenv(testGitVar, leg)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
}

func TestGuardTestGit_WhenTheMinimumLegFindsTheMinimum_ReturnsNil(t *testing.T) {
	guardSees(t, testGitMinimum, "git version "+git.MinimumVersion)

	err := guardTestGit()

	assert.NilError(t, err)
}

func TestGuardTestGit_WhenGitOnPathIsOlder_ReturnsErrorQuotingItsFirstLine(t *testing.T) {
	guardSees(t, "", "git version 2.30.2")

	err := guardTestGit()

	assert.Error(t, err, `the git found prints "git version 2.30.2", older than the minimum ils enforces, 2.34.1`)
}

func TestGuardTestGit_WhenGitMissingFromPath_ReturnsNotFoundError(t *testing.T) {
	// Not parallel: this empties PATH for the whole process while it runs.
	t.Setenv("PATH", t.TempDir())

	err := guardTestGit()

	_, ok := errors.AsType[*git.NotFoundError](err)
	assert.Check(t, ok, "guardTestGit error = %v, want a *git.NotFoundError", err)
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
