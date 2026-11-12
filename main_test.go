package main_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	main "github.com/jeffzi/ils"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
)

const (
	trunk     = "develop"
	topic     = "tidy-readme"
	host      = "github.com"
	slug      = host + "/acme/widgets"
	originURL = "ssh://git@" + slug + ".git"

	// held is how long the fake gh holds the read of the repository's identity open: far past the
	// moment a test sends its signal.
	held = 30 * time.Second
	// patience is how long a test waits for a program or the fake gh before it gives up.
	patience = time.Minute

	exitRefused = 2
)

func TestMain(m *testing.M) {
	if code, served := ghtest.Main(); served {
		os.Exit(code)
	}

	os.Exit(m.Run())
}

// ran is what one run of the binary printed and the code it exited with.
type ran struct {
	stdout string
	stderr string
	code   int
}

// binary is the path of an ils binary built from the module root.
type binary string

// build compiles the module root into a binary under the test's temporary directory.
//
// Under coverage the binary is instrumented with this test's counter mode and for this package
// alone: `go test` merges into its profile only the coverage data whose meta-data matches its own,
// and any other mode or package set records every line of main.go as never run.
func build(t *testing.T) binary {
	t.Helper()

	name := "ils"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	bin := filepath.Join(t.TempDir(), name)

	args := []string{"build", "-o", bin}
	if gittest.CoverSetting() != "" {
		args = append(args, "-cover", "-covermode="+testing.CoverMode(), "-coverpkg="+coveredPackages(t))
	}

	out, err := exec.CommandContext(t.Context(), "go", append(args, ".")...).CombinedOutput() //nolint:gosec // argv is written by this function
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	return binary(bin)
}

// coveredPackages returns, comma-separated, the import paths of the packages this test binary is
// instrumented for, which `go test` picks from its whole package pattern. It runs the test binary
// again with no test selected, so that the binary writes its coverage meta-data and nothing else.
func coveredPackages(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	out, err := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$", "-test.gocoverdir="+dir).CombinedOutput() //nolint:gosec // argv is written by this function
	if err != nil {
		t.Fatalf("write the coverage meta-data of the test binary: %v\n%s", err, out)
	}

	out, err = exec.CommandContext(t.Context(), "go", "tool", "covdata", "pkglist", "-i="+dir).Output() //nolint:gosec // argv is written by this function
	if err != nil {
		t.Fatalf("list the packages covered by the test binary: %v", err)
	}

	return strings.Join(strings.Fields(string(out)), ",")
}

// withCoverDir returns env plus the coverage directory when there is one.
func withCoverDir(env []string) []string {
	if covered := gittest.CoverSetting(); covered != "" {
		return append(env, covered)
	}

	return env
}

// exitCode returns the code a program exited with, -1 when a signal ended it, and fails the test
// when err says anything but an exit. doing and program name the failed step in the message.
func exitCode(t *testing.T, err error, doing, program string) int {
	t.Helper()

	if err == nil {
		return 0
	}

	exit, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		t.Fatalf("%s %s: %v", doing, program, err)
	}

	return exit.ExitCode()
}

// launch runs the binary with args in dir under env, plus the coverage directory when there is one,
// and returns what it printed.
func (b binary) launch(t *testing.T, dir string, env []string, args ...string) ran {
	t.Helper()

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(t.Context(), string(b), args...) //nolint:gosec // argv is written by the tests in this file
	cmd.Dir = dir
	cmd.Env = withCoverDir(env)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	return ran{stdout: stdout.String(), stderr: stderr.String(), code: exitCode(t, err, "run", string(b))}
}

// hookCommand returns the command of the one PreToolUse hook of the settings file at path.
func hookCommand(t *testing.T, path string) string {
	t.Helper()

	var settings struct {
		Hooks struct {
			PreToolUse []struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	//nolint:gosec // G304: the path is the settings file init wrote under t.TempDir().
	text, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.NilError(t, json.Unmarshal(text, &settings))
	assert.Assert(t, is.Len(settings.Hooks.PreToolUse, 1))
	assert.Assert(t, is.Len(settings.Hooks.PreToolUse[0].Hooks, 1))

	return settings.Hooks.PreToolUse[0].Hooks[0].Command
}

// opened returns a file of its own under the test's temporary directory, closed at cleanup, so
// that its descriptor is no other file's.
func opened(t *testing.T, name string) *os.File {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), name)
	assert.NilError(t, err)
	t.Cleanup(func() { assert.Check(t, f.Close()) })

	return f
}

func TestLaunched_WhenOnlyStdoutIsATerminal_CountsOnlyStdoutAsOne(t *testing.T) {
	t.Parallel()

	stdout, stderr := opened(t, "stdout"), opened(t, "stderr")
	isTerminal := func(fd uintptr) bool { return fd == stdout.Fd() }

	app := main.Launched(stdout, stderr, main.Terminal{IsTerminal: isTerminal})

	assert.Check(t, app.StdoutTerminal, "stdout was not counted as a terminal")
	assert.Check(t, !app.StderrTerminal, "stderr was counted as a terminal")
}

func TestLaunched_WhenStdoutAndStderrAnswerDifferently_AsksTheDisplayQueriesOfStderr(t *testing.T) {
	t.Parallel()

	stdout, stderr := opened(t, "stdout"), opened(t, "stderr")
	size := func(fd uintptr) (int, int, error) {
		if fd == stderr.Fd() {
			return 132, 40, nil
		}

		return 7, 3, nil
	}
	foreground := func(fd uintptr) bool { return fd != stderr.Fd() }

	app := main.Launched(stdout, stderr, main.Terminal{IsTerminal: func(uintptr) bool { return true }, Size: size, Foreground: foreground})

	assert.Check(t, is.Equal(app.StderrWidth(), 132))
	assert.Check(t, !app.StderrForeground(), "the foreground query was not asked of stderr")
}

func TestLaunched_WhenTheWidthOfStderrCannotBeRead_CountsItAsNone(t *testing.T) {
	t.Parallel()

	unread := func(uintptr) (int, int, error) { return 132, 40, errors.New("not a terminal") }

	app := main.Launched(opened(t, "stdout"), opened(t, "stderr"), main.Terminal{IsTerminal: func(uintptr) bool { return true }, Size: unread})

	assert.Check(t, is.Equal(app.StderrWidth(), 0))
}

// heldIdentityRead returns a repository on the topic branch whose origin names acme/widgets on
// GitHub, an environment to run ils in it, and a fake gh that holds the read of the repository's
// identity open for held. The fake is set up first, so that the environment of the repository,
// read from the process, carries it.
//
// It sets process environment, so the test cannot be parallel.
func heldIdentityRead(t *testing.T) (dir string, env []string, fake *ghtest.Fake) {
	t.Helper()

	fake = ghtest.Setup(t, ghtest.Identity(host, slug, ghtest.Answer{Stdout: ghtest.RepoViewJSON(false, "WRITE"), Delay: held})...)
	f, env := gittest.NewServed(t, trunk, topic, originURL)

	return f.Dir(), env, fake
}

// awaitHold waits until fake holds an answer open, and fails the test when patience runs out first.
func awaitHold(t *testing.T, fake *ghtest.Fake) {
	t.Helper()

	select {
	case <-fake.Holding():
	case <-time.After(patience):
		t.Fatalf("the fake gh held no answer within %v", patience)
	}
}

// started is a program the test started and can signal.
type started struct {
	cmd    *exec.Cmd
	stdout io.Reader
	stderr io.Reader
}

// start starts the program argv names, with its arguments, in dir under env. The end of the
// test kills it unless the test waited for it, and the end of patience kills it whatever the test
// does.
func start(t *testing.T, dir string, env []string, argv ...string) *started {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), patience)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is written by the tests in this file
	cmd.Dir = dir
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	assert.NilError(t, err)
	stderr, err := cmd.StderrPipe()
	assert.NilError(t, err)
	assert.NilError(t, cmd.Start())
	t.Cleanup(func() {
		cancel()
		if cmd.ProcessState == nil {
			_ = cmd.Wait() // it reports the kill the cancel sent
		}
	})

	return &started{cmd: cmd, stdout: stdout, stderr: stderr}
}

// end reads the rest of stdout and stderr, waits for the program to end, and returns what it
// printed and the code it exited with, -1 when a signal ended it.
func (s *started) end(t *testing.T) ran {
	t.Helper()

	stdout, err := io.ReadAll(s.stdout)
	assert.NilError(t, err)
	stderr, err := io.ReadAll(s.stderr)
	assert.NilError(t, err)

	err = s.cmd.Wait()

	return ran{stdout: string(stdout), stderr: string(stderr), code: exitCode(t, err, "wait for", s.cmd.Path)}
}

// signal sends sig to the program, and fails the test when it cannot.
func (s *started) signal(t *testing.T, sig syscall.Signal) {
	t.Helper()

	assert.NilError(t, s.cmd.Process.Signal(sig))
}

// skipWithoutSignals skips the test on Windows, where a signal cannot be sent to a program.
func skipWithoutSignals(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("a signal cannot be sent to a program on Windows")
	}
}

func TestIls(t *testing.T) {
	// The tests below share this one build: building the binary costs more than any of them.
	bin := build(t)

	t.Run("WhenASignalLandsDuringALandingCommand_PrintsTheInterruptedResult", func(t *testing.T) {
		skipWithoutSignals(t)

		dir, env, fake := heldIdentityRead(t)
		run := start(t, dir, withCoverDir(env), string(bin), "push")
		awaitHold(t, fake)

		run.signal(t, syscall.SIGINT)
		got := run.end(t)

		assert.Check(t, is.Equal(got.code, exitRefused))
		assert.Check(t, got.stdout != "", "the interrupted push printed no result")
		assert.Check(t, is.Equal(got.stderr, ""))
	})

	t.Run("WhenInitClaudeRuns_HooksTheScriptItInstalled", func(t *testing.T) {
		t.Parallel()

		if runtime.GOOS == "windows" {
			t.Skip("the guard does not run on Windows, so init adds no hook")
		}

		home := t.TempDir()
		env := append(gittest.Without(gittest.Without(os.Environ(), "HOME"), "CLAUDE_CONFIG_DIR"), "HOME="+home)

		got := bin.launch(t, home, env, "init", "claude")

		assert.Assert(t, is.Equal(got.code, 0), "stdout = %q, stderr = %q", got.stdout, got.stderr)
		script, found := strings.CutPrefix(hookCommand(t, filepath.Join(home, ".claude", "settings.json")), "bash ~/")
		assert.Assert(t, found, "the hook does not run bash on a path under ~/")
		_, err := os.Stat(filepath.Join(home, filepath.FromSlash(script)))
		assert.NilError(t, err, "the hook runs %s, which init did not install", script)
	})
}
