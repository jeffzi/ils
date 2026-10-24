package main_test

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	main "github.com/jeffzi/ils"
	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

const (
	// actAs, set in its environment to one of the parts below, makes the test binary act as that
	// part of ils instead of running the tests.
	actAs = "ILS_TEST_ACT_AS"
	// asTerminalRun runs ils with the arguments it was started with, built as ils builds itself but
	// with stderr counted as a terminal, wide columns wide.
	asTerminalRun = "terminal run"

	wide = 80

	trunk     = "develop"
	topic     = "tidy-readme"
	host      = "github.com"
	slug      = host + "/acme/widgets"
	originURL = "ssh://git@" + slug + ".git"

	// held is how long the fake gh holds the read of the repository's identity open: far past the
	// moment a test sends its signal.
	held = 30 * time.Second

	// interruptedReason is why ils refuses a landing command that an interrupt ends during the read
	// of the repository's identity.
	interruptedReason = "ils was interrupted while reading the repository's identity on GitHub; nothing was written"
	// interruptedRead is what `ils push` prints when an interrupt ends the read of the repository's
	// identity.
	interruptedRead = "Refused: " + interruptedReason + "\nNext: ils push\n"
	exitRefused     = 2

	// hideCursor is the sequence the progress display hides the cursor with while its spinner line
	// shows, and showCursor the one it shows it again with.
	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"
)

func TestMain(m *testing.M) {
	if part := os.Getenv(actAs); part != "" {
		os.Exit(act(part))
	}
	if code, served := ghtest.Main(); served {
		os.Exit(code)
	}

	os.Exit(m.Run())
}

// act runs the part of ils named part and returns its exit code. It drops actAs from its own
// environment first, so that a gh it starts acts as gh.
func act(part string) int {
	if err := os.Unsetenv(actAs); err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	if part != asTerminalRun {
		fmt.Fprintf(os.Stderr, "no part of ils is named %q\n", part)

		return 1
	}

	stderr := os.Stderr
	app := main.Launched(os.Stdout, stderr, main.Terminal{
		IsTerminal: func(fd uintptr) bool { return fd == stderr.Fd() },
		Size:       func(uintptr) (int, int, error) { return wide, 24, nil },
		Foreground: func(uintptr) bool { return true },
	})

	return app.Run(context.Background(), os.Args[1:])
}

// ran is what one run of the binary printed and the code it exited with.
type ran struct {
	stdout string
	stderr string
	code   int
}

// coverDir returns the directory `go test -cover` collects coverage data in, or "" when the test
// binary runs without coverage.
func coverDir() string {
	f := flag.Lookup("test.gocoverdir")
	if f == nil {
		return ""
	}

	return f.Value.String()
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
	if coverDir() != "" {
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
	if covered := coverDir(); covered != "" {
		return append(env, "GOCOVERDIR="+covered)
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

func TestIls(t *testing.T) {
	t.Parallel()

	bin := build(t)

	// The runs below go one after the other: instrumented binaries that exit at once race to write
	// the same coverage meta-data file, and the loser prints the failure on its stderr.
	t.Run("WhenAskedForTheVersion_PrintsTheModuleVersionOfTheBinary", func(t *testing.T) {
		info, err := buildinfo.ReadFile(string(bin))
		assert.NilError(t, err)

		stampedFromCheckout := slices.ContainsFunc(info.Settings, func(s debug.BuildSetting) bool {
			return s.Key == "vcs"
		})
		if !stampedFromCheckout {
			t.Skip("the binary was built from a source tree with no version control, so its version is (devel)")
		}

		want := cli.Version(info, true)
		assert.Assert(t, want != "(devel)", "the build info of %s names no module version", bin)

		got := bin.launch(t, t.TempDir(), os.Environ(), "--version")

		assert.Check(t, is.Equal(got.code, 0))
		assert.Check(t, is.Equal(got.stdout, "ils version "+want+"\n"))
		assert.Check(t, is.Equal(got.stderr, ""))
	})

	t.Run("WhenPushRunsOutsideARepository_ReportsTheFailedReadOfHead", func(t *testing.T) {
		dir, env := outsideRepository(t)

		got := bin.launch(t, dir, env, "push")

		assert.Check(t, is.Equal(got.code, 1))
		assert.Check(t, is.Equal(got.stderr, ""))
		assert.Check(t, strings.HasPrefix(got.stdout, "Could not read where HEAD stands: "),
			"stdout = %q", got.stdout)
		assert.Check(t, strings.HasSuffix(got.stdout, "Nothing was written.\nBroke: could not read where HEAD stands\nNext: fix what failed, then run ils push again\n"),
			"stdout = %q", got.stdout)
	})

	t.Run("WhenColorIsForcedInTheEnvironment_DrawsTheResultInColor", func(t *testing.T) {
		dir, env := outsideRepository(t, "CLICOLOR_FORCE=1")

		got := bin.launch(t, dir, env, "push")

		assert.Check(t, is.Equal(got.code, 1))
		assert.Check(t, is.Equal(resulttest.Draw(t, got.stdout).LookOf(t, "Broke: ", "Broke").Color, "red"), "stdout = %q", got.stdout)
	})

	t.Run("WhenDebugIsOn_LogsTheGitCallsOnStderr", func(t *testing.T) {
		dir, env := outsideRepository(t)

		got := bin.launch(t, dir, env, "push", "-d")

		assert.Check(t, strings.HasPrefix(resulttest.Draw(t, got.stderr).String(), "+"), "stderr = %q", got.stderr)
		assert.Check(t, strings.Contains(got.stderr, " git "), "stderr = %q", got.stderr)
	})
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

// outsideRepository returns an empty directory that is in no git repository, and an environment to
// run git in it: the test's git environment, the extra entries, and a C locale so that git's
// messages do not depend on the machine.
//
// The directory is resolved through symlinks because git ceilings are compared with the path git
// finds by walking up from the working directory, which is the resolved one (a temporary directory
// on macOS lies behind a symlink). The ceiling is set at the directory itself so that git stops
// there instead of finding a repository in one of its parents.
func outsideRepository(t *testing.T, extra ...string) (dir string, env []string) {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	assert.NilError(t, err)

	env = append(gittest.Env(t), "GIT_CEILING_DIRECTORIES="+dir, "LC_ALL=C")

	return dir, append(env, extra...)
}

func TestLaunched(t *testing.T) {
	t.Parallel()

	tests := []struct {
		terminal func(stdout, stderr *os.File) *os.File
		name     string
	}{
		{name: "stdout a terminal", terminal: func(stdout, _ *os.File) *os.File { return stdout }},
		{name: "stderr a terminal", terminal: func(_, stderr *os.File) *os.File { return stderr }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stdout, stderr := opened(t, "stdout"), opened(t, "stderr")
			terminal := tt.terminal(stdout, stderr)
			isTerminal := func(fd uintptr) bool { return fd == terminal.Fd() }
			size := func(fd uintptr) (int, int, error) {
				if fd == stderr.Fd() {
					return 132, 40, nil
				}

				return 7, 3, nil
			}
			foreground := func(fd uintptr) bool { return fd != stderr.Fd() }

			app := main.Launched(stdout, stderr, main.Terminal{IsTerminal: isTerminal, Size: size, Foreground: foreground})

			assert.Check(t, is.Equal(app.StdoutTerminal, terminal == stdout))
			assert.Check(t, is.Equal(app.StderrTerminal, terminal == stderr))
			assert.Check(t, is.Equal(app.StderrWidth(), 132))
			assert.Check(t, !app.StderrForeground(), "the foreground query was not asked of stderr")
		})
	}
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

	fake = ghtest.Setup(t,
		ghtest.Says("Logged in\n", ghtest.LoginArgs(host)...),
		ghtest.Exits(ghtest.Answer{Stdout: ghtest.RepoViewJSON(false, "WRITE"), Delay: held}, ghtest.RepoViewArgs(slug)...),
	)
	f, env := gittest.NewServed(t, trunk, topic, originURL)

	return f.Dir(), env, fake
}

// awaitHold waits until fake holds an answer open, and fails the test when a minute passes first.
func awaitHold(t *testing.T, fake *ghtest.Fake) {
	t.Helper()

	select {
	case <-fake.Holding():
	case <-time.After(time.Minute):
		t.Fatal("the fake gh held no answer within a minute")
	}
}

// started is a program the test started and can signal.
type started struct {
	cmd    *exec.Cmd
	stdout io.Reader
	stderr io.Reader
}

// start starts the program argv names, with its arguments, in dir under env. The end of the
// test kills it unless the test waited for it, and a minute kills it whatever the test does.
func start(t *testing.T, dir string, env []string, argv ...string) *started {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
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

// awaitStderr reads stderr until what it read holds want, and returns what it read; it fails the
// test when stderr ends first.
func (s *started) awaitStderr(t *testing.T, want string) string {
	t.Helper()

	var read bytes.Buffer

	next := make([]byte, 1)
	for !strings.Contains(read.String(), want) {
		n, err := s.stderr.Read(next)
		read.Write(next[:n])
		assert.NilError(t, err, "stderr ended before it wrote %q:\n%q", want, read.String())
	}

	return read.String()
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

// interrupt is a signal that interrupts a run of ils, named for the test case that sends it.
type interrupt struct {
	name   string
	signal syscall.Signal
}

func interrupts() []interrupt {
	return []interrupt{
		{name: "interrupt", signal: syscall.SIGINT},
		{name: "terminate", signal: syscall.SIGTERM},
	}
}

// interruptedLanding is a landing command run with args and sent signal, whose printed stdout
// is checked by printed.
type interruptedLanding struct {
	printed func(t *testing.T, stdout string)
	name    string
	args    []string
	signal  syscall.Signal
}

// interruptedLandings returns the landing commands that the signal test interrupts, with the
// stdout each prints.
func interruptedLandings() []interruptedLanding {
	return []interruptedLanding{
		{
			name: "push interrupted", args: []string{"push"}, signal: syscall.SIGINT,
			printed: func(t *testing.T, stdout string) {
				t.Helper()

				assert.Check(t, is.Equal(stdout, interruptedRead))
			},
		},
		{
			name: "push terminated", args: []string{"push"}, signal: syscall.SIGTERM,
			printed: func(t *testing.T, stdout string) {
				t.Helper()

				assert.Check(t, is.Equal(stdout, interruptedRead))
			},
		},
		{
			name: "merge interrupted", args: []string{"merge"}, signal: syscall.SIGINT,
			printed: func(t *testing.T, stdout string) {
				t.Helper()

				assert.Check(t, is.Equal(stdout, "Refused: "+interruptedReason+"\nNext: ils merge\n"))
			},
		},
		{
			name: "push as JSON terminated", args: []string{"push", "--json"}, signal: syscall.SIGTERM,
			printed: func(t *testing.T, stdout string) {
				t.Helper()

				shown := resulttest.Decode(t, stdout)
				assert.Check(t, is.Equal(shown.Command, "push"))
				assert.Check(t, is.Equal(shown.Outcome, "refused"))
				assert.Check(t, is.Equal(shown.Reason, interruptedReason))
				assert.Check(t, is.Equal(shown.Next, "ils push"))
				assert.Check(t, is.Equal(shown.ExitCode, exitRefused))
			},
		},
	}
}

func TestIls_WhenASignalLandsDuringALandingCommand_PrintsTheInterruptedResult(t *testing.T) {
	skipWithoutSignals(t)

	bin := build(t)
	for _, tt := range interruptedLandings() {
		t.Run(tt.name, func(t *testing.T) {
			dir, env, fake := heldIdentityRead(t)
			run := start(t, dir, withCoverDir(env), append([]string{string(bin)}, tt.args...)...)
			awaitHold(t, fake)

			run.signal(t, tt.signal)
			got := run.end(t)

			assert.Check(t, is.Equal(got.code, exitRefused))
			tt.printed(t, got.stdout)
			assert.Check(t, is.Equal(got.stderr, ""))
		})
	}
}

func TestLaunched_WhenASignalLandsWhileTheSpinnerLineShows_PrintsTheInterruptedResultWithTheCursorShown(t *testing.T) {
	skipWithoutSignals(t)

	for _, tt := range interrupts() {
		t.Run(tt.name, func(t *testing.T) {
			dir, env, fake := heldIdentityRead(t)
			run := start(t, dir, append(env, actAs+"="+asTerminalRun, "TERM=xterm-256color"), os.Args[0], "push")
			spinning := run.awaitStderr(t, hideCursor)
			awaitHold(t, fake)

			run.signal(t, tt.signal)
			got := run.end(t)

			stderr := spinning + got.stderr
			assert.Check(t, is.Equal(got.code, exitRefused))
			assert.Check(t, is.Equal(got.stdout, interruptedRead))
			assert.Check(t, strings.LastIndex(stderr, showCursor) > strings.LastIndex(stderr, hideCursor),
				"the cursor is still hidden:\n%q", stderr)
		})
	}
}
