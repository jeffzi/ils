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

	"github.com/google/go-cmp/cmp"

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
	if err != nil {
		t.Fatalf("listen on a local port: %v", err)
	}

	reached := make(chan struct{})
	released := make(chan struct{})

	t.Cleanup(func() {
		close(released)

		if closeErr := listener.Close(); closeErr != nil {
			t.Errorf("close the local listener: %v", closeErr)
		}
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

	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// helperDiff returns a runner in a repository with two changed tracked files, and the arguments of a
// git diff that prints the first file's diff itself and then starts this test binary as the external
// diff program of the second. That program inherits git's output pipes. The test fails here unless a
// run whose helper exits at once shows git writing the first file's diff to stdout.
func helperDiff(t *testing.T) (runner git.Runner, args []string) {
	t.Helper()

	const plain, driven = "first-plain.txt", "second-driven.txt"

	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("find the test binary: %v", err)
	}

	runner = newRunner(t)
	steps := [][]string{{"init", "--quiet"}, {"add", "--", plain, driven}}

	writeFile(t, runner.Dir, plain, "before\n")
	writeFile(t, runner.Dir, driven, "before\n")
	writeFile(t, runner.Dir, ".gitattributes", driven+" diff=helper\n")

	for _, step := range steps {
		if _, err = runner.Run(t.Context(), step...); err != nil {
			t.Fatalf("Run(%s) error = %v, want nil", step[0], err)
		}
	}

	writeFile(t, runner.Dir, plain, "after\n")
	writeFile(t, runner.Dir, driven, "after\n")

	args = []string{"-c", "diff.helper.command='" + filepath.ToSlash(binary) + "'", "diff"}
	control := runner
	control.Env = append(slices.Clone(runner.Env), helperModeEnv+"="+helperLeave)

	printed, err := control.Run(t.Context(), args...)
	if err != nil {
		t.Fatalf("Run(diff) with a helper that exits at once: error = %v, want nil", err)
	}

	if !strings.Contains(printed.Stdout, plain) || strings.Contains(printed.Stdout, driven) {
		t.Fatalf("Run(diff) with a helper that exits at once: stdout = %q, want only the diff of %s", printed.Stdout, plain)
	}

	return runner, args
}

func TestRunner_Run_WhenCallSucceeds_RunsInRunnerDir(t *testing.T) {
	t.Parallel()
	runner := newRunner(t)

	_, err := runner.Run(t.Context(), "init")
	if err != nil {
		t.Fatalf("Run(init) error = %v, want nil", err)
	}

	if _, statErr := os.Stat(filepath.Join(runner.Dir, ".git")); statErr != nil {
		t.Errorf("Run(init) left no .git in the runner's directory: %v", statErr)
	}
}

func TestRunner_Run_WhenArgHoldsShellSyntax_PassesItVerbatim(t *testing.T) {
	t.Parallel()
	runner := newRunner(t)
	value := "first && second; $HOME `third` > fourth"

	got, err := runner.Run(t.Context(), "-c", "user.name="+value, "config", "--get", "user.name")
	if err != nil {
		t.Fatalf("Run(config --get) error = %v, want nil", err)
	}

	want := git.Result{Stdout: value + "\n"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Run(config --get) mismatch (-want +got):\n%s", diff)
	}
}

func TestRunner_Run_WhenEnvNamesGlobalConfig_ReadsThatConfig(t *testing.T) {
	t.Parallel()
	config := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(config, []byte("[user]\n\tname = Ada\n"), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	runner := git.Runner{Dir: t.TempDir(), Env: isolatedEnv(t, config)}

	got, err := runner.Run(t.Context(), "config", "--global", "--get", "user.name")
	if err != nil {
		t.Fatalf("Run(config --global --get) error = %v, want nil", err)
	}

	if got.Stdout != "Ada\n" {
		t.Errorf("Run(config --global --get).Stdout = %q, want %q", got.Stdout, "Ada\n")
	}
}

func TestRunner_Run_WhenGitExitsNonZero_ReturnsExitError(t *testing.T) {
	t.Parallel()
	runner := newRunner(t)

	_, err := runner.Run(t.Context(), "nonsense")

	exitErr, ok := errors.AsType[*git.ExitError](err)
	if !ok {
		t.Fatalf("Run(nonsense) error = %v, want *git.ExitError", err)
	}
	if exitErr.Code != 1 {
		t.Errorf("Run(nonsense) exit code = %d, want 1", exitErr.Code)
	}
	if !strings.Contains(exitErr.Stderr, "nonsense") {
		t.Errorf("Run(nonsense) stderr = %q, want git's complaint about nonsense", exitErr.Stderr)
	}
	if diff := cmp.Diff([]string{"git", "nonsense"}, exitErr.Argv); diff != "" {
		t.Errorf("Run(nonsense) error argv mismatch (-want +got):\n%s", diff)
	}
	if want := "git nonsense: exit status 1: " + strings.TrimSpace(exitErr.Stderr); err.Error() != want {
		t.Errorf("Run(nonsense) error text = %q, want %q", err.Error(), want)
	}
}

func TestRunner_Run_WhenGitMissingFromPath_ReturnsNotFoundError(t *testing.T) {
	runner := newRunner(t)
	// Not parallel: this empties PATH for the whole process while it runs.
	t.Setenv("PATH", t.TempDir())

	_, err := runner.Run(t.Context(), "version")

	notFound, ok := errors.AsType[*git.NotFoundError](err)
	if !ok {
		t.Fatalf("Run(version) error = %v, want *git.NotFoundError", err)
	}
	if notFound.Name != "git" {
		t.Errorf("Run(version) missing program = %q, want %q", notFound.Name, "git")
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("Run(version) error = %v, want it to wrap exec.ErrNotFound", err)
	}
	if want := "git not found on PATH"; err.Error() != want {
		t.Errorf("Run(version) error text = %q, want %q", err.Error(), want)
	}
}

func TestRunner_Run_WhenContextCanceled_ReturnsContextErrorAndNoOutput(t *testing.T) {
	t.Parallel()
	runner := newRunner(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got, err := runner.Run(ctx, "version")

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run(version) error = %v, want context.Canceled", err)
	}
	if diff := cmp.Diff(git.Result{}, got); diff != "" {
		t.Errorf("Run(version) result mismatch (-want +got):\n%s", diff)
	}
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

			if !errors.Is(err, tt.cause) {
				t.Errorf("Run(ls-remote) error = %v, want %v", err, tt.cause)
			}
			if exitErr, ok := errors.AsType[*git.ExitError](err); ok {
				t.Errorf("Run(ls-remote) error = %#v, want no *git.ExitError for a killed git", exitErr)
			}
			if diff := cmp.Diff(git.Result{}, got); diff != "" {
				t.Errorf("Run(ls-remote) result mismatch (-want +got):\n%s", diff)
			}
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

	if took := time.Since(begun); took > prompt {
		t.Errorf("Run(diff) took %v, want under %v with the helper still alive", took, prompt)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run(diff) error = %v, want context.Canceled", err)
	}
	if exitErr, ok := errors.AsType[*git.ExitError](err); ok {
		t.Errorf("Run(diff) error = %#v, want no *git.ExitError for a killed git", exitErr)
	}
	if diff := cmp.Diff(git.Result{}, got); diff != "" {
		t.Errorf("Run(diff) result mismatch (-want +got):\n%s", diff)
	}
}

func TestRunner_Run_WhenGitExitsZeroWhileChildHoldsPipes_ReturnsOutputPromptly(t *testing.T) {
	t.Parallel()
	const prompt = 10 * time.Second
	runner, args := helperDiff(t)
	addr, lingering := silentListener(t)
	runner.Env = append(runner.Env, helperModeEnv+"="+helperSpawn, helperAddrEnv+"="+addr)
	begun := time.Now()

	got, err := runner.Run(t.Context(), args...)

	if took := time.Since(begun); took > prompt {
		t.Errorf("Run(diff) took %v, want under %v with the helper's child still alive", took, prompt)
	}
	if err != nil {
		t.Fatalf("Run(diff) error = %v, want nil for a git that exited zero", err)
	}
	if !strings.Contains(got.Stdout, "first-plain.txt") {
		t.Errorf("Run(diff) stdout = %q, want the diff git printed before it exited", got.Stdout)
	}
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
	if _, err := runner.Run(t.Context(), "init", "--quiet"); err != nil {
		t.Fatalf("Run(init) error = %v, want nil", err)
	}

	_, err := runner.Run(t.Context(), "status", "--short")
	if err != nil {
		t.Fatalf("Run(status) error = %v, want nil", err)
	}

	want := [][]string{{"git", "init", "--quiet"}, {"git", "status", "--short"}}
	if diff := cmp.Diff(want, recorder.Calls()); diff != "" {
		t.Errorf("Recorder.Calls() mismatch (-want +got):\n%s", diff)
	}
}

func TestRunner_Run_WhenGitExitsNonZero_StillRecordsCall(t *testing.T) {
	t.Parallel()
	recorder := &git.Recorder{}
	runner := newRunner(t)
	runner.Recorder = recorder

	_, err := runner.Run(t.Context(), "nonsense")
	if err == nil {
		t.Fatal("Run(nonsense) error = nil, want a failing call")
	}

	if diff := cmp.Diff([][]string{{"git", "nonsense"}}, recorder.Calls()); diff != "" {
		t.Errorf("Recorder.Calls() mismatch (-want +got):\n%s", diff)
	}
}

func TestRecorder_Calls_WhenReturnedSliceMutated_LaterCallsUnchanged(t *testing.T) {
	t.Parallel()
	recorder := &git.Recorder{}
	runner := newRunner(t)
	runner.Recorder = recorder
	if _, err := runner.Run(t.Context(), "version"); err != nil {
		t.Fatalf("Run(version) error = %v, want nil", err)
	}
	if _, err := runner.Run(t.Context(), "init", "--quiet"); err != nil {
		t.Fatalf("Run(init) error = %v, want nil", err)
	}
	calls := recorder.Calls()

	calls[0][1] = "changed"
	calls[1] = nil

	want := [][]string{{"git", "version"}, {"git", "init", "--quiet"}}
	if diff := cmp.Diff(want, recorder.Calls()); diff != "" {
		t.Errorf("Recorder.Calls() after mutating an earlier answer mismatch (-want +got):\n%s", diff)
	}
}
