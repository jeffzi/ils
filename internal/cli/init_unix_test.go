//go:build unix

package cli_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

func TestApp_Run_WhenInitClaudeWritesASkillUnderAUmaskThatHidesEveryFile_MakesTheHookScriptAloneExecutable(t *testing.T) {
	// Not parallel: the umask belongs to the whole process.
	previous := unix.Umask(0o077)
	t.Cleanup(func() { unix.Umask(previous) })
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

func TestApp_Run_WhenInitClaudeFindsASkillFileThatIsALink_WritesItsTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		// linked returns what the link at link holds to reach target.
		linked func(t *testing.T, link, target string) string
		name   string
	}{
		{
			name:   "absolute link",
			linked: func(_ *testing.T, _, target string) string { return target },
		},
		{
			name: "link relative to its directory",
			linked: func(t *testing.T, link, target string) string {
				t.Helper()

				relative, err := filepath.Rel(filepath.Dir(link), target)
				assert.NilError(t, err)

				return relative
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)
			target := filepath.Join(t.TempDir(), "SKILL.md")
			assert.NilError(t, os.WriteFile(target, []byte("edited by hand\n"), 0o600))
			link := shippedPath(home, ilsSkill+"/SKILL.md")
			linkAt(t, tt.linked(t, link, target), link)

			got := launch(t, app, "init", "claude")

			assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
			assert.Check(t, is.Equal(readText(t, target), string(shipped()[ilsSkill+"/SKILL.md"].Data)))
		})
	}
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

func TestApp_Run_WhenInitClaudeReplacesASkillFile_SetsTheModeItShipsWhateverTheOldMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want os.FileMode
	}{
		{name: ilsSkill + "/hook.sh", want: 0o755},
		{name: ilsSkill + "/SKILL.md", want: 0o644},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)
			installed(t, app)
			path := shippedPath(home, tt.name)
			assert.NilError(t, os.WriteFile(path, []byte("edited by hand\n"), 0o600))
			assert.NilError(t, os.Chmod(path, 0o600))

			got := launch(t, app, "init", "claude")

			assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
			info, err := os.Stat(path)
			if assert.Check(t, err) {
				assert.Check(t, is.Equal(info.Mode().Perm(), tt.want))
			}
		})
	}
}

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

// lockedSkill returns the cli with every skill installed under its HOME, the SKILL.md of the ils
// skill edited by hand, and the directory of that skill one nobody may add a file to; then that
// SKILL.md. It skips the test for a user whom the mode of a directory does not stop.
func lockedSkill(t *testing.T) (app cli.App, path string) {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("the mode of a directory does not stop root")
	}

	app, home := installing(t)
	installed(t, app)
	path = editedSkill(t, home)
	dir := filepath.Dir(path)
	//nolint:gosec // G302: the test takes write access from a directory it made under t.TempDir().
	assert.NilError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() {
		//nolint:gosec // G302: the directory is one the test made under t.TempDir().
		assert.Check(t, os.Chmod(dir, 0o750))
	})

	return app, path
}

func TestApp_Run_WhenInitClaudeCannotAddAFileBesideAChangedSkillFile_ExitsOneNamingThePath(t *testing.T) {
	t.Parallel()

	app, path := lockedSkill(t)

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, path))
}

func TestApp_Run_WhenInitClaudeCannotAddAFileBesideAChangedSkillFile_LeavesTheFileAsItWas(t *testing.T) {
	t.Parallel()

	app, path := lockedSkill(t)

	launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(readText(t, path), "edited by hand\n"))
}
