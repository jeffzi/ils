//go:build unix

package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
)

func TestApp_Run_WhenInitClaudeWritesASkill_MakesTheHookScriptAloneExecutable(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	tests := []struct {
		name string
		want os.FileMode
	}{
		{name: ilsSkill + "/hook.sh", want: 0o755},
		{name: ilsSkill + "/SKILL.md", want: 0o644},
	}

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	for _, tt := range tests {
		info, err := os.Stat(shippedPath(home, tt.name))
		if assert.Check(t, err) {
			assert.Check(t, is.Equal(info.Mode().Perm(), tt.want), "file %s", tt.name)
		}
	}
}

// linkedSkill returns the cli with the directory of the ils skill a link to an empty directory
// elsewhere, then that directory.
func linkedSkill(t *testing.T) (app cli.App, target string) {
	t.Helper()

	app, home := installing(t)
	target = t.TempDir()
	linkAt(t, target, filepath.Join(skillsUnder(home), ilsSkill))

	return app, target
}

// linkAt makes link, with its parent directories, a symbolic link to target.
func linkAt(t *testing.T, target, link string) {
	t.Helper()

	assert.NilError(t, os.MkdirAll(filepath.Dir(link), 0o750))
	assert.NilError(t, os.Symlink(target, link))
}

func TestApp_Run_WhenInitClaudeFindsASkillDirectoryThatIsALink_WritesNothingThroughIt(t *testing.T) {
	t.Parallel()

	app, target := linkedSkill(t)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	entries, err := os.ReadDir(target)
	assert.NilError(t, err)
	assert.Check(t, is.Len(entries, 0), "the run wrote through the link into %s", target)
}

func TestApp_Run_WhenInitClaudeFindsASkillDirectoryThatIsALink_PrintsTheLinkTarget(t *testing.T) {
	t.Parallel()

	app, target := linkedSkill(t)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(got.stdout, target))
}

func TestApp_Run_WhenInitClaudeReplacesAHookScriptThatIsNotExecutable_MakesItExecutable(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	script := shippedPath(home, ilsSkill+"/hook.sh")
	assert.NilError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o600))
	assert.NilError(t, os.Chmod(script, 0o600))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	info, err := os.Stat(script)
	if assert.Check(t, err) {
		assert.Check(t, is.Equal(info.Mode().Perm(), os.FileMode(0o755)))
	}
}
