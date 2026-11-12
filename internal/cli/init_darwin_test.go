package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

func TestApp_Run_WhenInitClaudeCannotReplaceAChangedSkillFile_LeavesNoFileBesideIt(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	// An immutable file takes a new file beside it but refuses the rename over it.
	changed := editedSkill(t, home)
	assert.NilError(t, unix.Chflags(changed, unix.UF_IMMUTABLE))
	t.Cleanup(func() { assert.Check(t, unix.Chflags(changed, 0)) })
	dir := filepath.Dir(changed)
	before, err := os.ReadDir(dir)
	assert.NilError(t, err)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	after, err := os.ReadDir(dir)
	assert.NilError(t, err)
	assert.Check(t, is.DeepEqual(entryNames(after), entryNames(before)))
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names
}
