package git_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
)

// prompt bounds how long a run may take to return once git or its context has ended.
const prompt = 10 * time.Second

// plain and driven name the two changed files of helperDiff's repository; the external diff program
// drives the diff of driven only.
const plain, driven = "first-plain.txt", "second-driven.txt"

// newRunner returns a runner in a fresh empty directory, cut off from the user's git config.
func newRunner(t *testing.T) git.Runner {
	t.Helper()

	return git.Runner{Dir: t.TempDir(), Env: gittest.Env(t)}
}

// newRecordedRunner returns a runner like newRunner with a recorder attached, and that recorder.
func newRecordedRunner(t *testing.T) (git.Runner, *git.Recorder) {
	t.Helper()

	recorder := &git.Recorder{}
	runner := newRunner(t)
	runner.Recorder = recorder

	return runner, recorder
}

// stalledRemote returns the URL of a local server that accepts a git client and never answers it,
// and a channel that closes once the client has connected.
func stalledRemote(t *testing.T) (url string, connected <-chan struct{}) {
	t.Helper()

	addr, connected := gittest.SilentListener(t)

	return "git://" + addr + "/project.git", connected
}

// helperDiff returns a runner in a repository with two changed tracked files, and the arguments of a
// git diff that prints the first file's diff itself and then starts this test binary as the external
// diff program of the second. That program inherits git's output pipes.
func helperDiff(t *testing.T) (runner git.Runner, args []string) {
	t.Helper()

	binary, err := os.Executable()
	assert.NilError(t, err)

	runner = newRunner(t)
	steps := [][]string{{"init", "--quiet"}, {"add", "--", plain, driven}}

	gittest.WriteFile(t, runner.Dir, plain, "before\n")
	gittest.WriteFile(t, runner.Dir, driven, "before\n")
	gittest.WriteFile(t, runner.Dir, ".gitattributes", driven+" diff=helper\n")

	for _, step := range steps {
		_, err = runner.Run(t.Context(), step...)
		assert.NilError(t, err)
	}

	gittest.WriteFile(t, runner.Dir, plain, "after\n")
	gittest.WriteFile(t, runner.Dir, driven, "after\n")

	args = []string{"-c", "diff.helper.command='" + filepath.ToSlash(binary) + "'", "diff"}

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
	runner := git.Runner{Dir: t.TempDir(), Env: append(gittest.Env(t), "GIT_CONFIG_GLOBAL="+config)}

	got, err := runner.Run(t.Context(), "config", "--global", "--get", "user.name")
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got.Stdout, "Ada\n"))
}

func TestRunner_Run_WhenGitExitsNonZero_ReturnsExitError(t *testing.T) {
	t.Parallel()
	runner := newRunner(t)

	_, err := runner.Run(t.Context(), "nonsense")

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Run(nonsense) error = %v, want *child.ExitError", err)
	assert.Check(t, is.Equal(exitErr.Code, 1))
	assert.Check(t, is.Contains(exitErr.Stderr, "nonsense"))
	assert.Check(t, is.DeepEqual(exitErr.Argv, []string{"git", "nonsense"}))
}

func TestRunner_Run_WhenGitMissingFromPath_ReturnsToolMissingError(t *testing.T) {
	runner := newRunner(t)
	// Not parallel: this empties PATH for the whole process while it runs.
	t.Setenv("PATH", t.TempDir())

	_, err := runner.Run(t.Context(), "version")

	missing, ok := errors.AsType[*child.ToolMissingError](err)
	assert.Assert(t, ok, "Run(version) error = %v, want *child.ToolMissingError", err)
	assert.Check(t, is.Equal(missing.Tool, "git"))
	assert.Check(t, is.Equal(missing.InstallURL, installURL))
	assert.Check(t, is.ErrorIs(err, exec.ErrNotFound))
}

func TestRunner_Run_WhenContextEndsWhileGitRuns_ReturnsOnlyContextError(t *testing.T) {
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
			ctx := gittest.Ending(t.Context(), connected, tt.cause)

			got, err := runner.Run(ctx, "ls-remote", url)

			assert.Check(t, is.ErrorIs(err, tt.cause))
			exitErr, ok := errors.AsType[*child.ExitError](err)
			assert.Check(t, !ok, "Run(ls-remote) error = %#v, want no *child.ExitError for a killed git", exitErr)
			assert.Check(t, is.DeepEqual(got, git.Result{}))
		})
	}
}

func TestRunner_Run_WhenContextEndsAfterOutputWhileChildHoldsPipes_ReturnsContextErrorPromptly(t *testing.T) {
	t.Parallel()
	runner, args := helperDiff(t)
	addr, started := gittest.SilentListener(t)
	runner.Env = append(runner.Env, helperModeEnv+"="+helperLinger, helperAddrEnv+"="+addr)
	ctx := gittest.Ending(t.Context(), started, context.Canceled)
	begun := time.Now()

	got, err := runner.Run(ctx, args...)

	took := time.Since(begun)
	assert.Check(t, took <= prompt, "Run(diff) took %v, want under %v with the helper still alive", took, prompt)
	assert.Check(t, is.ErrorIs(err, context.Canceled))
	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Check(t, !ok, "Run(diff) error = %#v, want no *child.ExitError for a killed git", exitErr)
	assert.Check(t, is.DeepEqual(got, git.Result{}))
}

func TestRunner_Run_WhenGitExitsZeroWhileChildHoldsPipes_ReturnsOutputPromptly(t *testing.T) {
	t.Parallel()
	runner, args := helperDiff(t)
	addr, lingering := gittest.SilentListener(t)
	runner.Env = append(runner.Env, helperModeEnv+"="+helperSpawn, helperAddrEnv+"="+addr)
	begun := time.Now()

	got, err := runner.Run(t.Context(), args...)

	took := time.Since(begun)
	assert.Check(t, took <= prompt, "Run(diff) took %v, want under %v with the helper's child still alive", took, prompt)
	assert.NilError(t, err)
	assert.Check(t, is.Contains(got.Stdout, plain))
	select {
	case <-lingering:
	case <-time.After(prompt):
		t.Error("the helper's child never connected, so nothing held git's pipes open")
	}
}

func TestRunner_Run_WhenRecorderAttached_RecordsEachArgv(t *testing.T) {
	t.Parallel()
	runner, recorder := newRecordedRunner(t)

	_, err := runner.Run(t.Context(), "init", "--quiet")
	assert.NilError(t, err)
	_, err = runner.Run(t.Context(), "status", "--short")
	assert.NilError(t, err)

	want := [][]string{{"git", "init", "--quiet"}, {"git", "status", "--short"}}
	assert.Check(t, is.DeepEqual(recorder.Calls(), want))
}

func TestRunner_Run_WhenGitExitsNonZero_StillRecordsCall(t *testing.T) {
	t.Parallel()
	runner, recorder := newRecordedRunner(t)

	_, err := runner.Run(t.Context(), "nonsense")
	assert.Assert(t, err != nil, "Run(nonsense) error = nil, want a failing call")

	assert.Check(t, is.DeepEqual(recorder.Calls(), [][]string{{"git", "nonsense"}}))
}

// captureHandler keeps every record logged through it, at any level.
type captureHandler struct {
	mu      *sync.Mutex
	records *[]slog.Record
	attrs   []slog.Attr
}

// newCapture returns a logger that keeps what it logs, and a function that returns the records
// logged so far.
func newCapture() (*slog.Logger, func() []slog.Record) {
	handler := captureHandler{mu: &sync.Mutex{}, records: &[]slog.Record{}}
	records := func() []slog.Record {
		handler.mu.Lock()
		defer handler.mu.Unlock()

		return slices.Clone(*handler.records)
	}

	return slog.New(handler), records
}

func (h captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h captureHandler) Handle(_ context.Context, record slog.Record) error {
	kept := record.Clone()
	kept.AddAttrs(h.attrs...)

	h.mu.Lock()
	defer h.mu.Unlock()
	*h.records = append(*h.records, kept)

	return nil
}

func (h captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = append(slices.Clip(h.attrs), attrs...)

	return h
}

func (h captureHandler) WithGroup(string) slog.Handler { return h }

// newLoggedRunner returns a runner like newRunner with a capturing logger attached, and a function
// that returns the records logged so far.
func newLoggedRunner(t *testing.T) (git.Runner, func() []slog.Record) {
	t.Helper()

	logger, records := newCapture()
	runner := newRunner(t)
	runner.Logger = logger

	return runner, records
}

// onlyCall returns the attributes of the one record in records, all but the duration, after
// checking that the record is an exec record whose duration is a time.Duration.
func onlyCall(t *testing.T, records []slog.Record) (attrs map[string]any, took time.Duration) {
	t.Helper()

	assert.Assert(t, is.Len(records, 1))
	record := records[0]
	assert.Check(t, is.Equal(record.Message, "exec"))

	attrs = map[string]any{}
	record.Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value.Any()

		return true
	})

	took, ok := attrs["duration"].(time.Duration)
	assert.Assert(t, ok, "duration attribute = %#v, want a time.Duration", attrs["duration"])
	delete(attrs, "duration")

	return attrs, took
}

func TestRunner_Run_WhenLoggerAttachedAndGitExits_LogsToolArgvExitCodeAndDuration(t *testing.T) {
	t.Parallel()
	runner, records := newLoggedRunner(t)
	args := []string{"nonsense"}

	_, _ = runner.Run(t.Context(), args...)

	attrs, took := onlyCall(t, records())
	want := map[string]any{"tool": "git", "args": args, "exit": int64(1)}
	assert.Check(t, is.DeepEqual(attrs, want))
	assert.Check(t, took > 0, "duration = %v, want above zero for a call git ran", took)
}

func TestRunner_Run_WhenLoggerAttachedAndGitMissingFromPath_LogsFailureInPlaceOfExit(t *testing.T) {
	runner, records := newLoggedRunner(t)
	// Not parallel: this empties PATH for the whole process while it runs.
	t.Setenv("PATH", t.TempDir())

	_, err := runner.Run(t.Context(), "version")
	assert.Assert(t, err != nil, "Run(version) error = nil, want git missing")

	attrs, _ := onlyCall(t, records())
	want := map[string]any{"tool": "git", "args": []string{"version"}, "error": err.Error()}
	assert.Check(t, is.DeepEqual(attrs, want))
}

func TestRecorder_Calls_WhenReturnedSliceMutated_LaterCallsUnchanged(t *testing.T) {
	t.Parallel()
	runner, recorder := newRecordedRunner(t)
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
