package ghtest_test

import (
	"context"
	"encoding/json/v2"
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

// No test here is parallel: the fake `gh` is found through `GH_PATH`, which `go-gh` reads from
// the environment of the whole process.

func TestMain(m *testing.M) {
	if code, served := ghtest.Main(); served {
		os.Exit(code)
	}

	os.Exit(m.Run())
}

func TestJSONHelpers_DecodeEachStringBackToTheValueGiven(t *testing.T) {
	const value = "say \"hi\" to C:\\home\nthen leave"

	tests := []struct {
		name  string
		build func(s string) string
		key   string
	}{
		{name: "ChangeJSON title", build: func(s string) string { return ghtest.ChangeJSON(ghtest.Change{Title: s}) }, key: "title"},
		{name: "ChangeJSON url", build: func(s string) string { return ghtest.ChangeJSON(ghtest.Change{URL: s}) }, key: "url"},
		{name: "ChangeJSON base", build: func(s string) string { return ghtest.ChangeJSON(ghtest.Change{Base: s}) }, key: "baseRefName"},
		{name: "ChangeJSON sha", build: func(s string) string { return ghtest.ChangeJSON(ghtest.Change{SHA: s}) }, key: "headRefOid"},
		{name: "HeadJSON sha", build: func(s string) string { return ghtest.HeadJSON(s, "MERGEABLE", "CLEAN", "APPROVED") }, key: "headRefOid"},
		{name: "HeadJSON mergeable", build: func(s string) string { return ghtest.HeadJSON("abc123", s, "CLEAN", "APPROVED") }, key: "mergeable"},
		{name: "HeadJSON status", build: func(s string) string { return ghtest.HeadJSON("abc123", "MERGEABLE", s, "APPROVED") }, key: "mergeStateStatus"},
		{name: "HeadJSON review", build: func(s string) string { return ghtest.HeadJSON("abc123", "MERGEABLE", "CLEAN", s) }, key: "reviewDecision"},
		{name: "CheckJSON name", build: func(s string) string { return ghtest.CheckJSON(s, "pass", "https://example.com") }, key: "name"},
		{name: "CheckJSON bucket", build: func(s string) string { return ghtest.CheckJSON("lint", s, "https://example.com") }, key: "bucket"},
		{name: "CheckJSON link", build: func(s string) string { return ghtest.CheckJSON("lint", "pass", s) }, key: "link"},
		{name: "RunJSON status", build: func(s string) string { return ghtest.RunJSON(s, "lint") }, key: "status"},
		{name: "RunJSON name", build: func(s string) string { return ghtest.RunJSON("completed", s) }, key: "name"},
		{name: "RepoViewSquashJSON permission", build: func(s string) string { return ghtest.RepoViewSquashJSON(false, s, true) }, key: "viewerPermission"},
		{name: "RepoViewJSON permission", build: func(s string) string { return ghtest.RepoViewJSON(false, s) }, key: "viewerPermission"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := tt.build(value)

			var got map[string]any
			err := json.Unmarshal([]byte(out), &got)
			assert.NilError(t, err, "decoding %s", out)

			assert.Check(t, is.Equal(got[tt.key], any(value)))
		})
	}
}

func TestFake_WhenArgvNotScripted_ExitsUnscriptedCodeNamingArgv(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "longer", args: []string{"pr", "view", "7", "--json", "x"}},
		{name: "shorter", args: []string{"pr", "view"}},
		{name: "different command", args: []string{"release", "view", "v1.2.3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Says("scripted\n", "pr", "view", "7"))

			_, err := github.Runner{}.Run(t.Context(), tt.args...)

			exitErr, ok := errors.AsType[*child.ExitError](err)
			assert.Assert(t, ok, "Run(%q) error = %v, want *child.ExitError", tt.args, err)
			assert.Check(t, is.Equal(exitErr.Code, ghtest.UnscriptedCode))
			assert.Check(t, is.Contains(exitErr.Stderr, strings.Join(tt.args, " ")))
		})
	}
}

func TestFake_WhenAskedForItsVersion_AnswersWithoutAScriptUnlessOneIsGiven(t *testing.T) {
	const scripted = "gh version 2.49.0 (2024-05-13)\n"

	tests := []struct {
		name      string
		wantStart string
		script    []ghtest.Call
	}{
		{name: "no script", wantStart: "gh version "},
		{name: "scripted", script: []ghtest.Call{ghtest.Says(scripted, ghtest.VersionArgs()...)}, wantStart: scripted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := ghtest.Setup(t, tt.script...)

			got, err := github.Runner{}.Run(t.Context(), ghtest.VersionArgs()...)
			assert.NilError(t, err)

			assert.Check(t, strings.HasPrefix(got.Stdout, tt.wantStart), "Run(--version) printed %q, want it to start with %q", got.Stdout, tt.wantStart)
			assert.Check(t, is.DeepEqual(fake.Calls(), [][]string{{"gh", "--version"}}))
		})
	}
}

func TestFake_WhenArgvScriptedWithSequence_AnswersInOrderThenRepeatsLast(t *testing.T) {
	args := []string{"pr", "checks", "7"}
	ghtest.Setup(t, ghtest.Call{
		Args:    args,
		Answers: []ghtest.Answer{{Stdout: "pending\n"}, {Stdout: "pass\n"}},
	})
	runner := github.Runner{}

	var got []string
	for range 3 {
		result, err := runner.Run(t.Context(), args...)
		assert.NilError(t, err)
		got = append(got, result.Stdout)
	}

	want := []string{"pending\n", "pass\n", "pass\n"}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestFake_WhenAnswerIsDelayed_AnswersAfterTheDelay(t *testing.T) {
	const delay = 300 * time.Millisecond
	args := []string{"pr", "checks", "7"}
	ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "pass\n", Delay: delay}, args...))
	start := time.Now()

	got, err := github.Runner{}.Run(t.Context(), args...)
	assert.NilError(t, err)

	elapsed := time.Since(start)
	assert.Check(t, elapsed >= delay, "Run(pr checks) answered after %s, want no sooner than %s", elapsed, delay)
	assert.Check(t, is.Equal(got.Stdout, "pass\n"))
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
