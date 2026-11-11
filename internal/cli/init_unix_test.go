//go:build unix

package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
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
		{name: ilsSkill + "/references/reference.md", want: 0o644},
	}

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	for _, tt := range tests {
		info, err := os.Stat(shippedPath(skillsUnder(home), tt.name))
		if assert.Check(t, err) {
			assert.Check(t, is.Equal(info.Mode().Perm(), tt.want), "file %s", tt.name)
		}
	}
}

func TestApp_Run_WhenInitClaudeFindsASkillDirectoryThatIsALink_WritesNothingThroughIt(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	target := t.TempDir()
	link := filepath.Join(skillsUnder(home), ilsSkill)
	assert.NilError(t, os.MkdirAll(filepath.Dir(link), 0o750))
	assert.NilError(t, os.Symlink(target, link))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	entries, err := os.ReadDir(target)
	assert.NilError(t, err)
	assert.Check(t, is.Len(entries, 0), "the run wrote through the link into %s", target)
}

func TestApp_Run_WhenInitClaudeFindsASkillDirectoryThatIsALink_PrintsTheLinkTarget(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	target := t.TempDir()
	link := filepath.Join(skillsUnder(home), ilsSkill)
	assert.NilError(t, os.MkdirAll(filepath.Dir(link), 0o750))
	assert.NilError(t, os.Symlink(target, link))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(got.stdout, target))
}

func TestApp_Run_WhenInitClaudeReplacesAHookScriptThatIsNotExecutable_MakesItExecutable(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	script := shippedPath(skillsUnder(home), ilsSkill+"/hook.sh")
	assert.NilError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o600))
	assert.NilError(t, os.Chmod(script, 0o600))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	info, err := os.Stat(script)
	if assert.Check(t, err) {
		assert.Check(t, is.Equal(info.Mode().Perm(), os.FileMode(0o755)))
	}
}
