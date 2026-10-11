package git_test

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/git"
)

// isolatedEnv returns a child environment whose global git config is the
// given file, with the system config and the user's home out of reach.
//
// The child gets a temporary directory of its own under every name a platform
// reads it from: git writes the files it hands an external diff program there,
// and Git for Windows has no usable default when none of the names is set.
func isolatedEnv(t *testing.T, globalConfig string) []string {
	t.Helper()

	home := t.TempDir()
	scratch := t.TempDir()
	env := []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"TMPDIR=" + scratch,
		"TMP=" + scratch,
		"TEMP=" + scratch,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + globalConfig,
		"PATH=" + os.Getenv("PATH"),
	}

	if root, ok := os.LookupEnv("SystemRoot"); ok {
		env = append(env, "SystemRoot="+root)
	}

	return env
}

// newRunner returns a runner in a fresh empty directory with no git config.
func newRunner(t *testing.T) git.Runner {
	t.Helper()

	return git.Runner{Dir: t.TempDir(), Env: isolatedEnv(t, os.DevNull)}
}

// endingContext is a context that ends with err once done closes.
type endingContext struct {
	context.Context

	done <-chan struct{}
	err  error
}

func (c endingContext) Done() <-chan struct{} { return c.done }

func (c endingContext) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

// stalledRemote returns the URL of a local server that accepts a git client and never answers it,
// and a channel that closes once the client has connected.
func stalledRemote(t *testing.T) (url string, connected <-chan struct{}) {
	t.Helper()

	addr, connected := silentListener(t)

	return "git://" + addr + "/project.git", connected
}

// silentListener returns the address of a local server that accepts one client, never answers it,
// and holds the connection until the test ends, and a channel that closes once the client has connected.
func silentListener(t *testing.T) (addr string, connected <-chan struct{}) {
	t.Helper()

	var config net.ListenConfig

	listener, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")
	assert.NilError(t, err)

	reached := make(chan struct{})
	released := make(chan struct{})

	t.Cleanup(func() {
		close(released)

		assert.Check(t, listener.Close())
	})

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}

		close(reached)
		<-released

		_ = conn.Close()
	}()

	return listener.Addr().String(), reached
}

// writeFile writes content to the named file in dir.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()

	assert.NilError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
}

// helperDiff returns a runner in a repository with two changed tracked files, and the arguments of a
// git diff that prints the first file's diff itself and then starts this test binary as the external
// diff program of the second. That program inherits git's output pipes. The test fails here unless a
// run whose helper exits at once shows git writing the first file's diff to stdout.
func helperDiff(t *testing.T) (runner git.Runner, args []string) {
	t.Helper()

	const plain, driven = "first-plain.txt", "second-driven.txt"

	binary, err := os.Executable()
	assert.NilError(t, err)

	runner = newRunner(t)
	steps := [][]string{{"init", "--quiet"}, {"add", "--", plain, driven}}

	writeFile(t, runner.Dir, plain, "before\n")
	writeFile(t, runner.Dir, driven, "before\n")
	writeFile(t, runner.Dir, ".gitattributes", driven+" diff=helper\n")

	for _, step := range steps {
		_, err = runner.Run(t.Context(), step...)
		assert.NilError(t, err)
	}

	writeFile(t, runner.Dir, plain, "after\n")
	writeFile(t, runner.Dir, driven, "after\n")

	args = []string{"-c", "diff.helper.command='" + filepath.ToSlash(binary) + "'", "diff"}
	control := runner
	control.Env = append(slices.Clone(runner.Env), helperModeEnv+"="+helperLeave)

	printed, err := control.Run(t.Context(), args...)
	assert.NilError(t, err)

	assert.Assert(t, is.Contains(printed.Stdout, plain))
	assert.Assert(t, !strings.Contains(printed.Stdout, driven), "stdout = %q, want only the diff of %s", printed.Stdout, plain)

	return runner, args
}

func TestRunner_Run_WhenCallSucceeds_RunsInRunnerDir(t *testing.T) {
	t.Parallel()
	runner := newRunner(t)

	_, err := runner.Run(t.Context(), "init")
	assert.NilError(t, err)

	_, statErr := os.Stat(filepath.Join(runner.Dir, ".git"))
	assert.Check(t, statErr)
}

func TestRunner_Run_WhenArgHoldsShellSyntax_PassesItVerbatim(t *testing.T) {
	t.Parallel()
	runner := newRunner(t)
	value := "first && second; $HOME `third` > fourth"

	got, err := runner.Run(t.Context(), "-c", "user.name="+value, "config", "--get", "user.name")
	assert.NilError(t, err)

	want := git.Result{Stdout: value + "\n"}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestRunner_Run_WhenEnvNamesGlobalConfig_ReadsThatConfig(t *testing.T) {
	t.Parallel()
	config := filepath.Join(t.TempDir(), "config")
	assert.NilError(t, os.WriteFile(config, []byte("[user]\n\tname = Ada\n"), 0o600))
	runner := git.Runner{Dir: t.TempDir(), Env: isolatedEnv(t, config)}

	got, err := runner.Run(t.Context(), "config", "--global", "--get", "user.name")
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got.Stdout, "Ada\n"))
}

func TestRunner_Run_WhenGitExitsNonZero_ReturnsExitError(t *testing.T) {
	t.Parallel()
	runner := newRunner(t)

	_, err := runner.Run(t.Context(), "nonsense")

	exitErr, ok := errors.AsType[*git.ExitError](err)
	assert.Assert(t, ok, "Run(nonsense) error = %v, want *git.ExitError", err)
	assert.Check(t, is.Equal(exitErr.Code, 1))
	assert.Check(t, is.Contains(exitErr.Stderr, "nonsense"))
	assert.Check(t, is.DeepEqual(exitErr.Argv, []string{"git", "nonsense"}))
	assert.Check(t, is.Equal(err.Error(), "git nonsense: exit status 1: "+strings.TrimSpace(exitErr.Stderr)))
}

func TestRunner_Run_WhenGitMissingFromPath_ReturnsNotFoundError(t *testing.T) {
	runner := newRunner(t)
	// Not parallel: this empties PATH for the whole process while it runs.
	t.Setenv("PATH", t.TempDir())

	_, err := runner.Run(t.Context(), "version")

	notFound, ok := errors.AsType[*git.NotFoundError](err)
	assert.Assert(t, ok, "Run(version) error = %v, want *git.NotFoundError", err)
	assert.Check(t, is.Equal(notFound.Name, "git"))
	assert.Check(t, is.ErrorIs(err, exec.ErrNotFound))
	assert.Check(t, is.Equal(err.Error(), "git not found on PATH"))
}

func TestRunner_Run_WhenContextCanceled_ReturnsContextErrorAndNoOutput(t *testing.T) {
	t.Parallel()
	runner := newRunner(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got, err := runner.Run(ctx, "version")

	assert.Check(t, is.ErrorIs(err, context.Canceled))
	assert.Check(t, is.DeepEqual(got, git.Result{}))
}

func TestRunner_Run_WhenContextEndsWhileGitRuns_ReturnsContextErrorAndNoOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		cause error
		name  string
	}{
		{name: "canceled", cause: context.Canceled},
		{name: "deadline exceeded", cause: context.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			url, connected := stalledRemote(t)
			runner := newRunner(t)
			// The trace makes git write to stderr before it blocks on the silent server.
			runner.Env = append(runner.Env, "GIT_TRACE=1")
			ctx := endingContext{Context: t.Context(), done: connected, err: tt.cause}

			got, err := runner.Run(ctx, "ls-remote", url)

			assert.Check(t, is.ErrorIs(err, tt.cause))
			exitErr, ok := errors.AsType[*git.ExitError](err)
			assert.Check(t, !ok, "Run(ls-remote) error = %#v, want no *git.ExitError for a killed git", exitErr)
			assert.Check(t, is.DeepEqual(got, git.Result{}))
		})
	}
}

func TestRunner_Run_WhenContextEndsAfterOutputWhileChildHoldsPipes_ReturnsPromptlyWithContextErrorAndNoOutput(t *testing.T) {
	t.Parallel()
	const prompt = 10 * time.Second
	runner, args := helperDiff(t)
	addr, started := silentListener(t)
	runner.Env = append(runner.Env, helperModeEnv+"="+helperLinger, helperAddrEnv+"="+addr)
	ctx := endingContext{Context: t.Context(), done: started, err: context.Canceled}
	begun := time.Now()

	got, err := runner.Run(ctx, args...)

	took := time.Since(begun)
	assert.Check(t, took <= prompt, "Run(diff) took %v, want under %v with the helper still alive", took, prompt)
	assert.Check(t, is.ErrorIs(err, context.Canceled))
	exitErr, ok := errors.AsType[*git.ExitError](err)
	assert.Check(t, !ok, "Run(diff) error = %#v, want no *git.ExitError for a killed git", exitErr)
	assert.Check(t, is.DeepEqual(got, git.Result{}))
}

func TestRunner_Run_WhenGitExitsZeroWhileChildHoldsPipes_ReturnsOutputPromptly(t *testing.T) {
	t.Parallel()
	const prompt = 10 * time.Second
	runner, args := helperDiff(t)
	addr, lingering := silentListener(t)
	runner.Env = append(runner.Env, helperModeEnv+"="+helperSpawn, helperAddrEnv+"="+addr)
	begun := time.Now()

	got, err := runner.Run(t.Context(), args...)

	took := time.Since(begun)
	assert.Check(t, took <= prompt, "Run(diff) took %v, want under %v with the helper's child still alive", took, prompt)
	assert.NilError(t, err)
	assert.Check(t, is.Contains(got.Stdout, "first-plain.txt"))
	select {
	case <-lingering:
	case <-time.After(prompt):
		t.Error("the helper's child never connected, so nothing held git's pipes open")
	}
}

func TestRunner_Run_WhenRecorderAttached_RecordsEachArgv(t *testing.T) {
	t.Parallel()
	recorder := &git.Recorder{}
	runner := newRunner(t)
	runner.Recorder = recorder
	_, err := runner.Run(t.Context(), "init", "--quiet")
	assert.NilError(t, err)

	_, err = runner.Run(t.Context(), "status", "--short")
	assert.NilError(t, err)

	want := [][]string{{"git", "init", "--quiet"}, {"git", "status", "--short"}}
	assert.Check(t, is.DeepEqual(recorder.Calls(), want))
}

func TestRunner_Run_WhenGitExitsNonZero_StillRecordsCall(t *testing.T) {
	t.Parallel()
	recorder := &git.Recorder{}
	runner := newRunner(t)
	runner.Recorder = recorder

	_, err := runner.Run(t.Context(), "nonsense")
	assert.Assert(t, err != nil, "Run(nonsense) error = nil, want a failing call")

	assert.Check(t, is.DeepEqual(recorder.Calls(), [][]string{{"git", "nonsense"}}))
}

func TestRecorder_Calls_WhenReturnedSliceMutated_LaterCallsUnchanged(t *testing.T) {
	t.Parallel()
	recorder := &git.Recorder{}
	runner := newRunner(t)
	runner.Recorder = recorder
	_, err := runner.Run(t.Context(), "version")
	assert.NilError(t, err)
	_, err = runner.Run(t.Context(), "init", "--quiet")
	assert.NilError(t, err)
	calls := recorder.Calls()

	calls[0][1] = "changed"
	calls[1] = nil

	want := [][]string{{"git", "version"}, {"git", "init", "--quiet"}}
	assert.Check(t, is.DeepEqual(recorder.Calls(), want))
}
