package ghtest_test

import (
	"context"
	"errors"
	"os"
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

func TestFake_RunHeld_WhenAnAnswerIsHeld_ReturnsOnceTheCallIsRecorded(t *testing.T) {
	const longerThanTheTest = 30 * time.Second
	args := []string{"pr", "checks", "7"}
	fake := ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "pass\n", Delay: longerThanTheTest}, args...))

	// The run is cut once the test ends, so its error says only that it was cut.
	fake.RunHeld(func(ctx context.Context) error {
		_, err := github.Runner{}.Run(ctx, args...)

		return err
	})

	assert.Check(t, is.DeepEqual(fake.Calls(), [][]string{{"gh", "pr", "checks", "7"}}))
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
