package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/logtest"
)

// prompt bounds how long a test waits for the helper's child to connect.
const prompt = 10 * time.Second

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

// helperRunner returns a runner whose git is the test binary acting as the helper of mode, whose
// lingering copy connects to addr once started. It puts the test binary on PATH as git, so the
// test cannot be parallel.
func helperRunner(t *testing.T, mode, addr string) git.Runner {
	t.Helper()

	gittest.LinkAs(t, "git")

	return git.Runner{
		Dir: t.TempDir(),
		Env: append(os.Environ(), helperModeEnv+"="+mode, helperAddrEnv+"="+addr, "GORACE="+gittest.RaceSetting()),
	}
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

func TestRunner_Run_WhenGitExitsNonZero_ReturnsExitErrorNamingCall(t *testing.T) {
	t.Parallel()
	runner := newRunner(t)

	_, err := runner.Run(t.Context(), "rev-parse", "--verify", "--quiet", "refs/heads/absent")

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Run(rev-parse) error = %v, want *child.ExitError", err)
	assert.Check(t, is.DeepEqual(exitErr.Argv, []string{"git", "rev-parse", "--verify", "--quiet", "refs/heads/absent"}))
}

func TestRunner_Run_WhenGitMissingFromPath_ReturnsToolMissingError(t *testing.T) {
	runner := newRunner(t)
	// Not parallel: this empties PATH for the whole process while it runs.
	t.Setenv("PATH", t.TempDir())

	_, err := runner.Run(t.Context(), "version")

	_, ok := errors.AsType[*child.ToolMissingError](err)
	assert.Assert(t, ok, "Run(version) error = %v, want *child.ToolMissingError", err)
	assert.Check(t, is.ErrorIs(err, exec.ErrNotFound))
}

func TestRunner_Run_WhenContextEndsAfterOutputWhileChildHoldsPipes_ReturnsContextErrorPromptly(t *testing.T) {
	// Not parallel: helperRunner sets PATH for the whole process.
	addr, started := gittest.SilentListener(t)
	runner := helperRunner(t, helperWait, addr)
	ctx := gittest.Ending(t.Context(), started, context.Canceled)

	_, err := runner.Run(ctx)

	assert.Check(t, is.ErrorIs(err, context.Canceled))
}

func TestRunner_Run_WhenGitExitsZeroWhileChildHoldsPipes_ReturnsOutputPromptly(t *testing.T) {
	// Not parallel: helperRunner sets PATH for the whole process.
	addr, lingering := gittest.SilentListener(t)
	runner := helperRunner(t, helperSpawn, addr)

	got, err := runner.Run(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.Contains(got.Stdout, helperSaid))
	select {
	case <-lingering:
	case <-time.After(prompt):
		t.Error("the helper's child never connected, so nothing held the pipes open")
	}
}

func TestRunner_Run_WhenGitExitsNonZero_StillRecordsCall(t *testing.T) {
	t.Parallel()
	runner, recorder := newRecordedRunner(t)

	_, err := runner.Run(t.Context(), "nonsense")
	assert.Assert(t, err != nil, "Run(nonsense) error = nil, want a failing call")

	assert.Check(t, is.DeepEqual(recorder.Calls(), [][]string{{"git", "nonsense"}}))
}

// newLoggedRunner returns a runner like newRunner with a logger attached, and the sink that keeps
// what it logs.
func newLoggedRunner(t *testing.T) (git.Runner, *logtest.Sink) {
	t.Helper()

	logger, sink := logtest.New()
	runner := newRunner(t)
	runner.Logger = logger

	return runner, sink
}

// onlyCall returns the attributes of the one record in records, all but the duration, and the
// duration, after checking that it is a time.Duration.
func onlyCall(t *testing.T, records []logtest.Record) (attrs map[string]any, took time.Duration) {
	t.Helper()

	attrs = logtest.CallAttrs(t, records)
	took, ok := records[0].Map()[child.KeyDuration].(time.Duration)
	assert.Assert(t, ok, "duration attribute = %#v, want a time.Duration", records[0].Map()[child.KeyDuration])

	return attrs, took
}

func TestRunner_Run_WhenGitExitsWithALoggerAttached_LogsTheCall(t *testing.T) {
	t.Parallel()
	runner, sink := newLoggedRunner(t)
	args := []string{"nonsense"}

	_, err := runner.Run(t.Context(), args...)

	attrs, took := onlyCall(t, sink.Records())
	assert.Check(t, is.DeepEqual(attrs, logtest.ExecAttrs(t, child.Exec{Tool: "git", Args: args, Err: err})))
	assert.Check(t, took > 0, "duration = %v, want above zero for a call git ran", took)
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

func TestEnvValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		key     string
		want    string
		env     []string
		wantSet bool
	}{
		{name: "set twice keeps the last", env: []string{"TERM=xterm", "TERM=dumb"}, key: "TERM", want: "dumb", wantSet: true},
		{name: "set to nothing", env: []string{"NO_COLOR="}, key: "NO_COLOR", want: "", wantSet: true},
		{name: "only a longer key set", env: []string{"TERMINAL=xterm"}, key: "TERM", wantSet: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, set := git.EnvValue(tt.env, tt.key)

			assert.Check(t, is.Equal(set, tt.wantSet), "EnvValue(%q, %q) set", tt.env, tt.key)
			assert.Check(t, is.Equal(got, tt.want), "EnvValue(%q, %q)", tt.env, tt.key)
		})
	}
}

func TestSameKey_WhenCaseDiffers_MatchesOnlyOnWindows(t *testing.T) {
	t.Parallel()

	got := git.SameKey("Path", "PATH")

	assert.Check(t, is.Equal(got, runtime.GOOS == "windows"), "SameKey(%q, %q) on %s", "Path", "PATH", runtime.GOOS)
}
