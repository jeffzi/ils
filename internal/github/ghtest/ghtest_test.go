package ghtest_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/failtest"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
)

// No test that scripts `gh` is parallel: the fake `gh` is found through `GH_PATH`, which the runner
// reads from the environment of the whole process.

func TestMain(m *testing.M) {
	if code, served := ghtest.Main(); served {
		os.Exit(code)
	}

	os.Exit(m.Run())
}

func TestFake_WhenArgvNotScripted_ExitsUnscriptedCodeNamingArgv(t *testing.T) {
	args := []string{"pr", "view", "7", "--json", "x"}
	ghtest.Setup(t, ghtest.Says("scripted\n", "pr", "view", "7"))

	_, err := github.Runner{}.Run(t.Context(), args...)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Run(%q) error = %v, want *child.ExitError", args, err)
	assert.Check(t, is.Equal(exitErr.Code, ghtest.UnscriptedCode))
	assert.Check(t, is.Contains(exitErr.Stderr, strings.Join(args, " ")))
}

func TestFake_WhenScriptedArgvHoldsAny_AnswersWhateverTheCallHoldsThere(t *testing.T) {
	ghtest.Setup(t, ghtest.Says("scripted\n", "pr", "view", "7", "--json", ghtest.Any))

	got, err := github.Runner{}.Run(t.Context(), "pr", "view", "7", "--json", "number,url")
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got.Stdout, "scripted\n"))
}

// scriptedNotes is the content that the FileHolding tests script `gh release create` to expect.
const scriptedNotes = "## [1.2.0]\n\n- Fix `$HOME`\n"

// runReleaseCreateWithNotes runs `gh release create` against a fake scripted to expect
// scriptedNotes, passing a notes file that holds notes.
func runReleaseCreateWithNotes(t *testing.T, notes string) (github.Result, error) {
	t.Helper()

	file := filepath.Join(t.TempDir(), "notes.md")
	assert.NilError(t, os.WriteFile(file, []byte(notes), 0o600))
	ghtest.Setup(t, ghtest.Says("scripted\n", "release", "create", "--notes-file", ghtest.FileHolding(scriptedNotes)))

	return github.Runner{}.Run(t.Context(), "release", "create", "--notes-file", file)
}

func TestFileHolding_WhenFileHoldsTheContent_Answers(t *testing.T) {
	_, err := runReleaseCreateWithNotes(t, scriptedNotes)

	assert.NilError(t, err)
}

func TestFileHolding_WhenFileHoldsOtherContent_ExitsUnscripted(t *testing.T) {
	_, err := runReleaseCreateWithNotes(t, scriptedNotes+"- Add the release\n")

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Run error = %v, want *child.ExitError", err)
	assert.Check(t, is.Equal(exitErr.Code, ghtest.UnscriptedCode))
}

func TestFileHolding_WhenFileCannotBeRead_ExitsBrokenNamingIt(t *testing.T) {
	dir := t.TempDir()
	ghtest.Setup(t, ghtest.Says("scripted\n", "release", "create", "--notes-file", ghtest.FileHolding(scriptedNotes)))

	_, err := github.Runner{}.Run(t.Context(), "release", "create", "--notes-file", dir)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Run error = %v, want *child.ExitError", err)
	assert.Check(t, exitErr.Code != ghtest.UnscriptedCode, "Code = %d, want the code of a broken fake", exitErr.Code)
	assert.Check(t, is.Contains(exitErr.Stderr, dir))
}

func TestFake_WhenAnswerIsDelayed_AnswersAfterTheDelay(t *testing.T) {
	const delay = 300 * time.Millisecond
	args := []string{"pr", "checks", "7"}
	ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "pass\n", Delay: delay}, args...))
	start := time.Now()

	_, err := github.Runner{}.Run(t.Context(), args...)
	assert.NilError(t, err)

	elapsed := time.Since(start)
	assert.Check(t, elapsed >= delay, "Run(pr checks) answered after %s, want no sooner than %s", elapsed, delay)
}

func TestFake_WhileHolding_WhenTheActionTakesLong_AnswersOnlyAfterItReturns(t *testing.T) {
	args := []string{"pr", "checks", "7"}
	fake := ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "pass\n", Delay: time.Nanosecond}, args...))
	var returned atomic.Bool
	fake.WhileHolding(func() error {
		time.Sleep(300 * time.Millisecond)
		returned.Store(true)

		return nil
	})

	_, err := github.Runner{}.Run(t.Context(), args...)
	assert.NilError(t, err)

	assert.Check(t, returned.Load(), "Run(pr checks) returned before the hold action did")
}

func TestFake_WhileHolding_WhenTheActionFails_FailsTheTestNamingTheError(t *testing.T) {
	args := []string{"pr", "checks", "7"}
	spy := failtest.New(t)
	fake := ghtest.Setup(spy, ghtest.Exits(ghtest.Answer{Stdout: "pass\n", Delay: time.Nanosecond}, args...))
	fake.WhileHolding(func() error { return errors.New("notes file locked") })

	_, err := github.Runner{}.Run(t.Context(), args...)

	assert.NilError(t, err)
	assert.Check(t, spy.Failed(), "WhileHolding action error left the test passing")
	assert.Check(t, is.Contains(strings.Join(spy.Messages(), "\n"), "notes file locked"))
}

func TestFake_Holding_WhenNoAnswerIsHeld_StaysOpen(t *testing.T) {
	args := []string{"pr", "checks", "7"}
	fake := ghtest.Setup(t, ghtest.Says("pass\n", args...))

	_, err := github.Runner{}.Run(t.Context(), args...)
	assert.NilError(t, err)

	select {
	case <-fake.Holding():
		t.Error("Holding() closed, want it open since no answer was held")
	default:
	}
}
