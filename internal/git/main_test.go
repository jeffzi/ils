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

	"golang.org/x/mod/semver"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

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
	// testGitVersionVar, when set, is the exact version of git the tests must find on PATH.
	testGitVersionVar = "ILS_TEST_GIT_VERSION"
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

	return checkTestGit(os.Getenv(testGitVar), os.Getenv(testGitVersionVar), line)
}

// checkTestGit returns why a git whose `git --version` prints line first does not suit the tests,
// or nil when it does. A version that is not empty is the exact one the git must be, and is checked
// before the target that testGitVar names.
func checkTestGit(target, version, line string) error {
	found := git.VersionOf(line)

	if version != "" && found != version {
		return fmt.Errorf("the git found prints %q, and %s wants %s", line, testGitVersionVar, version)
	}

	switch target {
	case testGitMinimum:
		if found != git.MinimumVersion {
			return fmt.Errorf("%s is %s and the git found prints %q, want the minimum ils enforces, %s",
				testGitVar, target, line, git.MinimumVersion)
		}
	case "", testGitLatest:
		if semver.Compare("v"+found, "v"+git.MinimumVersion) < 0 {
			return fmt.Errorf("the git found prints %q, older than the minimum ils enforces, %s", line, git.MinimumVersion)
		}
	default:
		return fmt.Errorf("%s is %q, want %s, %s, or unset", testGitVar, target, testGitMinimum, testGitLatest)
	}

	return nil
}

func TestCheckTestGit_WhenGitSuitsTarget_ReturnsNil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		target  string
		version string
		line    string
	}{
		{name: "minimum target and the minimum", target: testGitMinimum, line: "git version " + git.MinimumVersion},
		{name: "latest target and the minimum", target: testGitLatest, line: "git version " + git.MinimumVersion},
		{name: "latest target and a newer git", target: testGitLatest, line: "git version 2.56.0.windows.2"},
		{name: "unset target and the minimum", line: "git version " + git.MinimumVersion},
		{name: "unset target and a newer git", line: "git version 2.54.0 (Apple Git-157)"},
		{
			name: "version wanted is the minimum found on the minimum target", target: testGitMinimum,
			version: git.MinimumVersion, line: "git version " + git.MinimumVersion + ".windows.1",
		},
		{
			name: "version wanted is the Git for Windows found on the latest target", target: testGitLatest,
			version: "2.56.0", line: "git version 2.56.0.windows.2",
		},
		{name: "version wanted is the git found on the unset target", version: "2.54.0", line: "git version 2.54.0 (Apple Git-157)"},
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
		notMinimum = "ILS_TEST_GIT is minimum and the git found prints %q, want the minimum ils enforces, 2.34.1"
		older      = "the git found prints %q, older than the minimum ils enforces, 2.34.1"
		notWanted  = "the git found prints %q, and ILS_TEST_GIT_VERSION wants %s"
	)

	return []unsuitedTestGit{
		{
			name: "minimum target and a newer git", target: testGitMinimum, line: "git version 2.56.0",
			want: fmt.Sprintf(notMinimum, "git version 2.56.0"),
		},
		{
			name: "minimum target and an older git", target: testGitMinimum, line: "git version 2.30.2",
			want: fmt.Sprintf(notMinimum, "git version 2.30.2"),
		},
		{
			name: "minimum target and no readable version", target: testGitMinimum, line: "git version DEV",
			want: fmt.Sprintf(notMinimum, "git version DEV"),
		},
		{
			name: "latest target and an older git", target: testGitLatest, line: "git version 2.30.2",
			want: fmt.Sprintf(older, "git version 2.30.2"),
		},
		{
			name: "latest target and no readable version", target: testGitLatest, line: "git version DEV",
			want: fmt.Sprintf(older, "git version DEV"),
		},
		{name: "unset target and an older git", line: "git version 2.30.2", want: fmt.Sprintf(older, "git version 2.30.2")},
		{name: "unset target and no readable version", line: "git version DEV", want: fmt.Sprintf(older, "git version DEV")},
		{
			name: "unknown target", target: "newest", line: "git version 2.56.0",
			want: `ILS_TEST_GIT is "newest", want minimum, latest, or unset`,
		},
		{
			name: "version wanted is not the git found on the latest target", target: testGitLatest, version: "2.43.0",
			line: "git version 2.42.0", want: fmt.Sprintf(notWanted, "git version 2.42.0", "2.43.0"),
		},
		{
			name: "version wanted is not the git found on the unset target", version: "2.43.0",
			line: "git version 2.56.0", want: fmt.Sprintf(notWanted, "git version 2.56.0", "2.43.0"),
		},
		{
			name: "version wanted and no readable version", version: "2.43.0",
			line: "git version DEV", want: fmt.Sprintf(notWanted, "git version DEV", "2.43.0"),
		},
		{
			name: "version wanted is checked before the minimum target", target: testGitMinimum, version: git.MinimumVersion,
			line: "git version 2.56.0", want: fmt.Sprintf(notWanted, "git version 2.56.0", git.MinimumVersion),
		},
		{
			name: "version wanted is checked before an unknown target", target: "newest", version: "2.56.0",
			line: "git version 2.55.0", want: fmt.Sprintf(notWanted, "git version 2.55.0", "2.56.0"),
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

	fakeGitRepo(t, line)
	t.Setenv(versionLineEnv, line)
	t.Setenv(testGitVar, target)
	t.Setenv(testGitVersionVar, version)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
}

func TestGuardTestGit_WhenTheMinimumTargetFindsTheMinimum_ReturnsNil(t *testing.T) {
	guardSees(t, testGitMinimum, "", "git version "+git.MinimumVersion)

	err := guardTestGit()

	assert.NilError(t, err)
}

func TestGuardTestGit_WhenGitOnPathIsOlder_ReturnsErrorQuotingItsFirstLine(t *testing.T) {
	guardSees(t, "", "", "git version 2.30.2")

	err := guardTestGit()

	assert.Error(t, err, `the git found prints "git version 2.30.2", older than the minimum ils enforces, 2.34.1`)
}

func TestGuardTestGit_WhenVersionWantedIsNotTheGitOnPath_ReturnsErrorNamingBoth(t *testing.T) {
	guardSees(t, testGitLatest, "2.43.0", "git version 2.42.0")

	err := guardTestGit()

	assert.Error(t, err, `the git found prints "git version 2.42.0", and ILS_TEST_GIT_VERSION wants 2.43.0`)
}

func TestGuardTestGit_WhenGitMissingFromPath_ReturnsNotFoundError(t *testing.T) {
	// Not parallel: this empties PATH for the whole process while it runs.
	t.Setenv("PATH", t.TempDir())

	err := guardTestGit()

	_, ok := errors.AsType[*git.NotFoundError](err)
	assert.Check(t, ok, "guardTestGit error = %v, want a *git.NotFoundError", err)
	assert.Check(t, is.ErrorContains(err, "read the version of the git on PATH: "))
}

func TestMain_WhenTestGitTargetIsUnknown_ExitsBeforeTheTests(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	assert.NilError(t, err)

	var stderr bytes.Buffer

	//nolint:gosec // G204: the test binary starts itself; nothing here comes from outside the test.
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^$")
	cmd.Env = append(os.Environ(), testGitVar+"=newest", "GORACE=atexit_sleep_ms=0")
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
