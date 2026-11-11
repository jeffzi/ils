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

// lockedSettings returns the cli with every skill installed under its HOME, one skill file changed
// since, and the settings file holding text in a Claude Code directory nobody may add a file to;
// then that HOME, the changed skill file and the settings file. It skips the test for a user whom
// the mode of a directory does not stop.
func lockedSettings(t *testing.T, text string) (app cli.App, home, changed, path string) {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("the mode of a directory does not stop root")
	}

	app, home = installing(t)
	installed(t, app)
	changed = shippedPath(skillsUnder(home), ilsSkill+"/SKILL.md")
	assert.NilError(t, os.WriteFile(changed, []byte("edited by hand\n"), 0o600))
	path = withSettings(t, home, text)
	dir := filepath.Dir(path)
	//nolint:gosec // G302: the test takes write access from a directory it made under t.TempDir().
	assert.NilError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() {
		//nolint:gosec // G302: the directory is one the test made under t.TempDir().
		assert.Check(t, os.Chmod(dir, 0o750))
	})

	return app, home, changed, path
}

func TestApp_Run_WhenInitClaudeCannotWriteTheSettings_ExitsOneNamingTheFile(t *testing.T) {
	t.Parallel()

	app, _, _, path := lockedSettings(t, "{}")

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, path))
}

func TestApp_Run_WhenInitClaudeCannotWriteTheSettings_LeavesTheOldFileWhole(t *testing.T) {
	t.Parallel()

	app, _, _, path := lockedSettings(t, "{}")

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	//nolint:gosec // G304: the path is one the test built under t.TempDir().
	text, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.Check(t, is.Equal(string(text), "{}"))
}

func TestApp_Run_WhenInitClaudeCannotWriteTheSettings_KeepsTheSkillsItWrote(t *testing.T) {
	t.Parallel()

	app, _, changed, _ := lockedSettings(t, "{}")

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	//nolint:gosec // G304: the path is one the test built under t.TempDir().
	written, err := os.ReadFile(changed)
	assert.NilError(t, err)
	assert.Check(t, is.Equal(string(written), string(shipped()[ilsSkill+"/SKILL.md"].Data)))
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
// then the link and that file.
func linkedSettings(t *testing.T) (app cli.App, link, target string) {
	t.Helper()

	app, home := installing(t)
	target = filepath.Join(t.TempDir(), "settings.json")
	assert.NilError(t, os.WriteFile(target, []byte("{}"), 0o600))
	link = settingsUnder(home)
	assert.NilError(t, os.MkdirAll(filepath.Dir(link), 0o750))
	assert.NilError(t, os.Symlink(target, link))

	return app, link, target
}

func TestApp_Run_WhenInitClaudeFindsTheSettingsAreALink_KeepsTheLink(t *testing.T) {
	t.Parallel()

	app, link, target := linkedSettings(t)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	pointed, err := os.Readlink(link)
	assert.NilError(t, err)
	assert.Check(t, is.Equal(pointed, target))
}

func TestApp_Run_WhenInitClaudeFindsTheSettingsAreALink_WritesItsTarget(t *testing.T) {
	t.Parallel()

	app, _, target := linkedSettings(t)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.DeepEqual(listsHolding(decodedFile(t, target), "Bash(git push)"), []string{"deny"}))
}

func TestApp_Run_WhenInitClaudeFindsTheSettingsAreALinkToNothing_ExitsOneNamingTheFile(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	link := settingsUnder(home)
	assert.NilError(t, os.MkdirAll(filepath.Dir(link), 0o750))
	missing := filepath.Join(t.TempDir(), "settings.json")
	assert.NilError(t, os.Symlink(missing, link))

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, link))
	pointed, err := os.Readlink(link)
	if assert.Check(t, err) {
		assert.Check(t, is.Equal(pointed, missing))
	}
}
