//go:build unix

package cli_test

import (
	"io"
	"os"
	"path/filepath"
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
	link := shippedPath(home, ilsSkill+"/SKILL.md")
	linkAt(t, filepath.Join(t.TempDir(), "SKILL.md"), link)

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, link))
}
