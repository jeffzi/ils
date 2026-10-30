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

func TestFake_WhenScriptedArgvHoldsFileHolding_AnswersOnlyAFileWithThatContent(t *testing.T) {
	const scripted = "## [1.2.0]\n\n- Fix `$HOME`\n"
	tests := []struct {
		name    string
		written string
		want    bool
	}{
		{name: "file holds the content", written: scripted, want: true},
		{name: "file holds other content", written: scripted + "- Add the release\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notes := filepath.Join(t.TempDir(), "notes.md")
			assert.NilError(t, os.WriteFile(notes, []byte(tt.written), 0o600))
			ghtest.Setup(t, ghtest.Says("scripted\n", "release", "create", "--notes-file", ghtest.FileHolding(scripted)))

			_, err := github.Runner{}.Run(t.Context(), "release", "create", "--notes-file", notes)

			assert.Check(t, is.Equal(err == nil, tt.want), "Run error = %v, want answered %t", err, tt.want)
		})
	}
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
