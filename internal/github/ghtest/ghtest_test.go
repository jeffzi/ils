package ghtest_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
)

// No test that scripts `gh` is parallel: the fake `gh` is found through `GH_PATH`, which `go-gh`
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

func TestFileHolding_WhenFileHoldsTheContent_Answers(t *testing.T) {
	notes := filepath.Join(t.TempDir(), "notes.md")
	assert.NilError(t, os.WriteFile(notes, []byte(scriptedNotes), 0o600))
	ghtest.Setup(t, ghtest.Says("scripted\n", "release", "create", "--notes-file", ghtest.FileHolding(scriptedNotes)))

	got, err := github.Runner{}.Run(t.Context(), "release", "create", "--notes-file", notes)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got.Stdout, "scripted\n"))
}

func TestFileHolding_WhenFileHoldsOtherContent_ExitsUnscripted(t *testing.T) {
	notes := filepath.Join(t.TempDir(), "notes.md")
	assert.NilError(t, os.WriteFile(notes, []byte(scriptedNotes+"- Add the release\n"), 0o600))
	ghtest.Setup(t, ghtest.Says("scripted\n", "release", "create", "--notes-file", ghtest.FileHolding(scriptedNotes)))

	_, err := github.Runner{}.Run(t.Context(), "release", "create", "--notes-file", notes)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Run error = %v, want *child.ExitError", err)
	assert.Check(t, is.Equal(exitErr.Code, ghtest.UnscriptedCode))
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
