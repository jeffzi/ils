package cli_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

func TestApp_Run_WhenInitClaudeReplacesASkillFile_LeavesAReaderOfTheOldFileTheOldText(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	changed := editedSkill(t, home)
	reader, err := os.Open(changed) //nolint:gosec // G304: the path is one the test built under t.TempDir().
	assert.NilError(t, err)
	t.Cleanup(func() { assert.Check(t, reader.Close()) })

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	text, err := io.ReadAll(reader)
	assert.NilError(t, err)
	assert.Check(t, is.Equal(string(text), "edited by hand\n"))
}

func TestApp_Run_WhenInitClaudeFindsASkillFileThatIsALinkToNothing_ExitsOneNamingTheFile(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	link := shippedPath(home, ilsSkill+"/SKILL.md")
	linkAt(t, filepath.Join(t.TempDir(), "SKILL.md"), link)

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, link))
}

// unsyncedSettings returns the cli with every skill installed under its HOME and the settings file
// holding {} in a Claude Code directory nobody may list, so that a new file takes its place there
// but the directory cannot be opened to flush the rename; then the settings file. It skips the test
// for a user whom the mode of a directory does not stop.
func unsyncedSettings(t *testing.T) (app cli.App, path string) {
	t.Helper()

	skipAsRoot(t)
	app, home := installing(t)
	installed(t, app)
	path = withSettings(t, home, "{}")
	lockDir(t, filepath.Dir(path), 0o300)

	return app, path
}

// unlocked gives the owner every access to dir, a directory the test made under t.TempDir().
func unlocked(t *testing.T, dir string) {
	t.Helper()

	//nolint:gosec // G302: the test gives back access to a directory it made under t.TempDir().
	assert.NilError(t, os.Chmod(dir, 0o700))
}

func TestApp_Run_WhenInitClaudeCannotSyncTheDirectoryOfTheSettings_ExitsOneNamingItWithTheNewFileInPlace(t *testing.T) {
	t.Parallel()

	app, path := unsyncedSettings(t)
	dir := filepath.Dir(path)

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	reason := resulttest.Decode(t, got.stdout).Reason
	assert.Check(t, is.Contains(strings.ReplaceAll(reason, path, ""), dir), "reason = %q", reason)
	unlocked(t, dir)
	assert.Check(t, is.DeepEqual(decodedFile(t, path), installedSettings()))
	left, err := filepath.Glob(filepath.Join(dir, ".settings.json-*"))
	assert.NilError(t, err)
	assert.Check(t, is.Len(left, 0))
}

func TestApp_Run_WhenInitClaudeRunsAgainAfterTheDirectorySyncFailed_MarksTheHookUnchanged(t *testing.T) {
	t.Parallel()

	app, path := unsyncedSettings(t)
	first := launch(t, app, "init", "claude")
	assert.Assert(t, is.Equal(first.code, exitBroke), "stdout = %q", first.stdout)
	unlocked(t, filepath.Dir(path))

	got := settingsStepsShown(t, app)

	assertShownSteps(t, got[:1], []shownStep{{mark: "–", word: path}})
}
