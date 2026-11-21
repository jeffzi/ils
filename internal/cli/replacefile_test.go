package cli_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

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
	link := shippedPath(home, ilsSkillFile)
	linkAt(t, filepath.Join(t.TempDir(), "SKILL.md"), link)

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, link))
}

// unlocked gives the owner every access to dir, a directory the test made under t.TempDir().
func unlocked(t *testing.T, dir string) {
	t.Helper()

	//nolint:gosec // G302: the test gives back access to a directory it made under t.TempDir().
	assert.NilError(t, os.Chmod(dir, 0o700))
}

func TestApp_Run_WhenInitClaudeCannotSyncTheDirectoryOfTheSettings_ExitsOneNamingItWithTheNewFileInPlace(t *testing.T) {
	t.Parallel()

	skipAsRoot(t)
	app, home := installing(t)
	installed(t, app)
	path := withSettings(t, home, "{}")
	dir := filepath.Dir(path)
	// A directory nobody may list takes a new file but cannot be opened to flush the rename.
	lockDir(t, dir, 0o300)

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	reason := resulttest.Decode(t, got.stdout).Reason
	assert.Check(t, is.Contains(strings.ReplaceAll(reason, path, ""), dir), "reason = %q", reason)
	unlocked(t, dir)
	assert.Check(t, is.DeepEqual(decodedFile(t, path), installedSettings()))
}
