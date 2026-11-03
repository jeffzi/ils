package git_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
)

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

func TestRunner_Run_WhenArgHoldsShellSyntax_PassesItVerbatim(t *testing.T) {
	t.Parallel()
	runner := newRunner(t)
	value := "first && second; $HOME `third` > fourth"

	got, err := runner.Run(t.Context(), "-c", "user.name="+value, "config", "--get", "user.name")
	assert.NilError(t, err)

	want := git.Result{Stdout: value + "\n"}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestRunner_Run_WhenGitExitsNonZero_StillRecordsCall(t *testing.T) {
	t.Parallel()
	runner, recorder := newRecordedRunner(t)

	_, err := runner.Run(t.Context(), "nonsense")
	assert.Assert(t, err != nil, "Run(nonsense) error = nil, want a failing call")

	assert.Check(t, is.DeepEqual(recorder.Calls(), [][]string{{"git", "nonsense"}}))
}

func TestRecorder_Calls_WhenReturnedSliceMutated_LaterCallsUnchanged(t *testing.T) {
	t.Parallel()
	runner, recorder := newRecordedRunner(t)
	_, err := runner.Run(t.Context(), "version")
	assert.NilError(t, err)
	calls := recorder.Calls()

	calls[0][1] = "changed"

	want := [][]string{{"git", "version"}}
	assert.Check(t, is.DeepEqual(recorder.Calls(), want))
}
