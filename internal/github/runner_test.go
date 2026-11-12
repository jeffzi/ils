package github_test

import (
	"errors"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
)

// No test that scripts `gh` is parallel: the fake `gh` is found through `GH_PATH`,
// which the runner reads from the environment of the whole process.

func TestVersionOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "release with a date", line: "gh version 2.50.0 (2024-05-29)", want: "2.50.0"},
		{name: "development build", line: "gh version DEV"},
		{name: "no gh version prefix", line: "2.50.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := github.VersionOf(tt.line)

			assert.Check(t, is.Equal(got, tt.want), "VersionOf(%q)", tt.line)
		})
	}
}

// withoutGh empties `GH_PATH` and `PATH` for the rest of the test, so that no `gh` can be found.
func withoutGh(t *testing.T) {
	t.Helper()

	t.Setenv("GH_PATH", "")
	t.Setenv("PATH", t.TempDir())
}

// assertToolMissing fails unless err is a *child.ToolMissingError.
func assertToolMissing(t *testing.T, err error) {
	t.Helper()

	_, ok := errors.AsType[*child.ToolMissingError](err)
	assert.Assert(t, ok, "Run(pr view) error = %v, want *child.ToolMissingError", err)
}

func TestRunner_Run_WhenGhMissing_ReturnsToolMissingError(t *testing.T) {
	withoutGh(t)

	_, err := github.Runner{}.Run(t.Context(), "pr", "view")

	assertToolMissing(t, err)
}

func TestRunner_Run_WhenGhPathIsSet_RunsTheGhItNames(t *testing.T) {
	ghtest.Setup(t, ghtest.Says("named\n", "pr", "view"))
	t.Setenv("PATH", t.TempDir())

	got, err := github.Runner{}.Run(t.Context(), "pr", "view")
	assert.NilError(t, err, "Run(pr view)")

	assert.Check(t, is.Equal(got.Stdout, "named\n"))
}

// onPath scripts the fake gh, then makes it findable only as the gh on PATH, with PATH set to
// entry and the working directory to the fake's directory.
func onPath(t *testing.T, entry func(dir string) string, script ...ghtest.Call) {
	t.Helper()

	ghtest.Setup(t, script...)
	dir := t.TempDir()
	gittest.Link(t, dir, github.Program)
	t.Setenv("GH_PATH", "")
	t.Setenv("PATH", entry(dir))
	t.Chdir(dir)
}

func TestRunner_Run_WhenGhPathUnset_RunsTheGhOnPath(t *testing.T) {
	onPath(t, func(dir string) string { return dir }, ghtest.Says("found\n", "pr", "view"))

	got, err := github.Runner{}.Run(t.Context(), "pr", "view")
	assert.NilError(t, err, "Run(pr view)")

	assert.Check(t, is.Equal(got.Stdout, "found\n"))
}

func TestRunner_Run_WhenGhOnlyOnRelativePathEntry_ReturnsToolMissingError(t *testing.T) {
	onPath(t, func(string) string { return "." }, ghtest.Says("found\n", "pr", "view"))

	_, err := github.Runner{}.Run(t.Context(), "pr", "view")

	assertToolMissing(t, err)
}

func TestRunner_CheckLogin_WhenNotLoggedIn_ReturnsNotLoggedInError(t *testing.T) {
	ghtest.Setup(t, ghtest.Exits(ghtest.LoggedOut(), ghtest.LoginArgs(engineHost)...))

	err := github.Runner{}.CheckLogin(t.Context(), engineHost)

	notLoggedIn, ok := errors.AsType[*forge.NotLoggedInError](err)
	assert.Assert(t, ok, "CheckLogin error = %v, want *forge.NotLoggedInError", err)
	assert.Check(t, is.Equal(notLoggedIn.Host, engineHost))
	_, ok = errors.AsType[*child.ExitError](err)
	assert.Check(t, ok, "CheckLogin error = %v, want it to wrap the *child.ExitError", err)
}
