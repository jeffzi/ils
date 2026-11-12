//go:build unix

package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// lockedSettings returns the cli with every skill installed under its HOME and the settings file
// holding text in a Claude Code directory nobody may add a file to; then that HOME and the settings
// file. It skips the test for a user whom the mode of a directory does not stop.
func lockedSettings(t *testing.T, text string) (app cli.App, home, path string) {
	t.Helper()

	skipAsRoot(t)
	app, home = installing(t)
	installed(t, app)
	path = withSettings(t, home, text)
	lockDir(t, filepath.Dir(path))

	return app, home, path
}

func TestApp_Run_WhenInitClaudeCannotWriteTheSettings_ExitsOneNamingTheFile(t *testing.T) {
	t.Parallel()

	app, _, path := lockedSettings(t, "{}")

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, path))
}

func TestApp_Run_WhenInitClaudeRewritesTheSettings_KeepsTheirMode(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	path := withSettings(t, home, "{}")
	//nolint:gosec // G302: the test needs this mode on a file it made under t.TempDir(), to see it kept.
	assert.NilError(t, os.Chmod(path, 0o640))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	info, err := os.Stat(path)
	assert.NilError(t, err)
	assert.Check(t, is.Equal(info.Mode().Perm(), os.FileMode(0o640)))
}

// linkedSettings returns the cli with its settings file a link to a file holding {} elsewhere,
// then that file.
func linkedSettings(t *testing.T) (app cli.App, target string) {
	t.Helper()

	app, home := installing(t)
	target = filepath.Join(t.TempDir(), "settings.json")
	assert.NilError(t, os.WriteFile(target, []byte("{}"), 0o600))
	linkAt(t, target, settingsUnder(home))

	return app, target
}

func TestApp_Run_WhenInitClaudeFindsTheSettingsAreALink_WritesItsTarget(t *testing.T) {
	t.Parallel()

	app, target := linkedSettings(t)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.DeepEqual(listsHolding(decodedFile(t, target), firstRule("deny")), []string{"deny"}))
}
