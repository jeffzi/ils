//go:build unix

package guard_test

import (
	"errors"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
)

func TestJudge_WhenSeveralInvocationsRefuse_NamesTheFirst(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkJudgedRow(t, caller, judged{command: "git clean -f; git branch -D x", want: guard.Refuse, wantNamed: "git clean"})
}

func TestJudge_WhenTheCommandCannotBeParsed_RefusesNamingWhatFailed(t *testing.T) {
	t.Parallel()
	const command = "echo ok; rm -rf / )"
	caller := readerCaller()
	_, err := guard.Parse(t.Context(), command, caller)
	failure, ok := errors.AsType[*guard.ParseError](err)
	assert.Assert(t, ok, "Parse(%q) error = %v, want a *guard.ParseError", command, err)

	got := guard.Judge(t.Context(), command, caller)

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Contains(got.Reason, failure.Error()), "Judge(%q) reason", command)
}

func TestJudge_WhenAMarkNamesNothing_RefusesWithItsKindAsTheReason(t *testing.T) {
	t.Parallel()
	const command = "echo @(a|b)"

	got := guard.Judge(t.Context(), command, readerCaller())

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Equal(got.Reason, string(guard.ExtendedGlob)), "Judge(%q) reason", command)
}

func TestJudge_WhenAMarkNamesWhatItFound_RefusesNamingItBesideTheKind(t *testing.T) {
	t.Parallel()
	const command = "export GIT_CONFIG_GLOBAL=/tmp/c"

	got := guard.Judge(t.Context(), command, readerCaller())

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Contains(got.Reason, string(guard.GitVariable)), "Judge(%q) reason", command)
	assert.Check(t, is.Contains(got.Reason, "GIT_CONFIG_GLOBAL"), "Judge(%q) reason", command)
}

func TestJudge_WhenAToolWordIsHandedToAnotherCommand_RefusesNamingBoth(t *testing.T) {
	t.Parallel()
	callerDir := t.TempDir()
	writeExecutable(t, filepath.Join(callerDir, "bin", "git"))
	const command = "xargs ./bin/git push"

	got := guard.Judge(t.Context(), command, guard.Caller{Home: home, Dir: callerDir})

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Contains(got.Reason, "xargs"), "Judge(%q) reason", command)
	assert.Check(t, is.Contains(got.Reason, "./bin/git"), "Judge(%q) reason", command)
}

func TestJudge_WhenNoInvocationRunsATool_AllowsWithoutStartingGit(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	caller, recorder := recordingCaller(f, f.Dir())
	const command = "ls -la | grep x; echo git push"

	got := guard.Judge(t.Context(), command, caller)

	assert.Check(t, is.DeepEqual(got, guard.Verdict{Outcome: guard.Allow}), "Judge(%q)", command)
	assert.Check(t, is.Len(recorder.Calls(), 0), "Judge(%q) git calls", command)
}

func TestJudge_WhenAGitReadFails_RefusesSayingWhatBroke(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := aliased(t)
	caller := callerFailingOn(t, f, f.Dir(), "config")

	checkJudgedRow(t, caller, judged{command: "git co topic", want: guard.Refuse, wantNamed: gittest.FailedSaid})
}
