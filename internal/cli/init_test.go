package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// The skills the tests ship, by the directory each one lands in.
const (
	ilsSkill     = "ils"
	releaseSkill = "write-release"
)

// shipped returns the skills the tests hand the cli to install: two skills, one of which holds a
// hook script and a file in a directory of its own.
func shipped() fstest.MapFS {
	return fstest.MapFS{
		ilsSkill + "/SKILL.md":                {Data: []byte("the ils skill\n")},
		ilsSkill + "/hook.sh":                 {Data: []byte("#!/bin/sh\nexec ils hook claude\n")},
		ilsSkill + "/references/reference.md": {Data: []byte("the ils reference\n")},
		releaseSkill + "/SKILL.md":            {Data: []byte("the release skill\n")},
	}
}

// installing returns the cli in a directory that is no repository, shipping the skills of
// shipped, with a HOME of its own, acting as on a system where the guard runs, and that HOME.
func installing(t *testing.T) (app cli.App, home string) {
	t.Helper()

	home = t.TempDir()
	app = outside(t)
	app.Environ = []string{"HOME=" + home}
	app.Skills = shipped()
	app.SetGuardSupported(true)

	return app, home
}

// skillsUnder returns the directory the skills land in for the Claude Code directory under dir.
func skillsUnder(dir string) string {
	return filepath.Join(dir, ".claude", "skills")
}

// shippedPath returns where the shipped file name lands in the skills directory skills.
func shippedPath(skills, name string) string {
	return filepath.Join(skills, filepath.FromSlash(name))
}

// installed runs `ils init claude` once with app, and stops the test unless it exits zero.
func installed(t *testing.T, app cli.App) {
	t.Helper()

	got := launch(t, app, "init", "claude")
	assert.Assert(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
}

// assertNoNextLine checks, without stopping the test, that no line of text is a Next line.
func assertNoNextLine(t *testing.T, text string) {
	t.Helper()

	for _, line := range outputLines(text) {
		assert.Check(t, !strings.HasPrefix(line, result.NextLabel+":"), "stdout = %q", text)
	}
}

func TestApp_Run_WhenInitClaudeRunsWithNoSkillInstalled_WritesEveryShippedFileUnderHome(t *testing.T) {
	t.Parallel()

	app, home := installing(t)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	for name, file := range shipped() {
		written, err := os.ReadFile(shippedPath(skillsUnder(home), name))
		if assert.Check(t, err) {
			assert.Check(t, is.Equal(string(written), string(file.Data)), "file %s", name)
		}
	}
}

func TestApp_Run_WhenInitClaudeInstallsEverySkill_PrintsTheDirectoryOfEachWithoutANextLine(t *testing.T) {
	t.Parallel()

	app, home := installing(t)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(lineWith(t, got.stdout, filepath.Join(skillsUnder(home), ilsSkill)), "Installed"))
	assert.Check(t, is.Contains(got.stdout, filepath.Join(skillsUnder(home), releaseSkill)))
	assertNoNextLine(t, got.stdout)
}

func TestApp_Run_WhenInitClaudeRunsWithTheJSONFlag_PrintsEachSkillAndNoNextStep(t *testing.T) {
	t.Parallel()

	app, home := installing(t)

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	printed := resulttest.Decode(t, got.stdout)
	assert.Check(t, is.Equal(printed.Outcome, result.Done.String()))
	assert.Check(t, is.Equal(printed.Next, "none"))
	lines := strings.Join(printed.Lines, "\n")
	assert.Check(t, is.Contains(lines, filepath.Join(skillsUnder(home), ilsSkill)))
	assert.Check(t, is.Contains(lines, filepath.Join(skillsUnder(home), releaseSkill)))
}

func TestApp_Run_WhenInitClaudeFindsEveryFileInPlace_LeavesTheFilesUntouched(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	long := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
	for name := range shipped() {
		assert.NilError(t, os.Chtimes(shippedPath(skillsUnder(home), name), long, long))
	}

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	for name := range shipped() {
		info, err := os.Stat(shippedPath(skillsUnder(home), name))
		if assert.Check(t, err) {
			assert.Check(t, info.ModTime().Equal(long), "file %s was written again at %v", name, info.ModTime())
		}
	}
}

func TestApp_Run_WhenInitClaudeFindsEveryFileInPlace_PrintsOneLineNamingNoSkill(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Len(outputLines(got.stdout), 1), "stdout = %q", got.stdout)
	assert.Check(t, !strings.Contains(got.stdout, skillsUnder(home)), "stdout = %q", got.stdout)
	assert.Check(t, !strings.Contains(got.stdout, settingsUnder(home)), "stdout = %q", got.stdout)
	assertNoNextLine(t, got.stdout)
}

func TestApp_Run_WhenInitClaudeFindsAShippedFileChanged_ReplacesIt(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	changed := shippedPath(skillsUnder(home), ilsSkill+"/SKILL.md")
	assert.NilError(t, os.WriteFile(changed, []byte("edited by hand\n"), 0o600))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	//nolint:gosec // G304: the path is one the test built under t.TempDir().
	written, err := os.ReadFile(changed)
	assert.NilError(t, err)
	assert.Check(t, is.Equal(string(written), string(shipped()[ilsSkill+"/SKILL.md"].Data)))
}

func TestApp_Run_WhenInitClaudeFindsAShippedFileChanged_PrintsThatSkillAlone(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	changed := shippedPath(skillsUnder(home), ilsSkill+"/SKILL.md")
	assert.NilError(t, os.WriteFile(changed, []byte("edited by hand\n"), 0o600))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(lineWith(t, got.stdout, filepath.Join(skillsUnder(home), ilsSkill)), "Updated"))
	assert.Check(t, !strings.Contains(got.stdout, filepath.Join(skillsUnder(home), releaseSkill)), "stdout = %q", got.stdout)
}

func TestApp_Run_WhenInitClaudeShipsAFileBesideTheSkills_InstallsItNowhere(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	skills := shipped()
	skills["README.md"] = &fstest.MapFile{Data: []byte("about the skills\n")}
	app.Skills = skills

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	_, err := os.Stat(filepath.Join(skillsUnder(home), "README.md"))
	assert.Check(t, os.IsNotExist(err), "a file beside the skills landed in %s", skillsUnder(home))
}

func TestApp_Run_WhenInitClaudeFindsAFileItDoesNotShip_LeavesIt(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	own := filepath.Join(skillsUnder(home), ilsSkill, "notes.md")
	assert.NilError(t, os.MkdirAll(filepath.Dir(own), 0o750))
	assert.NilError(t, os.WriteFile(own, []byte("my notes\n"), 0o600))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	//nolint:gosec // G304: the path is one the test built under t.TempDir().
	kept, err := os.ReadFile(own)
	assert.NilError(t, err)
	assert.Check(t, is.Equal(string(kept), "my notes\n"))
}

func TestApp_Run_WhenInitClaudeCannotWriteASkillFile_ExitsOneNamingThePath(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	// A file where the skill's directory goes makes every path under it impossible to write.
	blocked := filepath.Join(skillsUnder(home), ilsSkill)
	assert.NilError(t, os.MkdirAll(filepath.Dir(blocked), 0o750))
	assert.NilError(t, os.WriteFile(blocked, []byte("in the way\n"), 0o600))

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	printed := resulttest.Decode(t, got.stdout)
	assert.Check(t, is.Equal(printed.Outcome, result.Broke.String()))
	assert.Check(t, is.Contains(printed.Reason, blocked))
	_, err := os.Stat(settingsUnder(home))
	assert.Check(t, os.IsNotExist(err), "a run whose skills broke wrote %s", settingsUnder(home))
}

func TestApp_Run_WhenInitClaudeHasNoHome_ExitsTwoRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		environ []string
	}{
		{name: "HOME unset", environ: []string{"PATH=/usr/bin"}},
		{name: "HOME empty", environ: []string{"HOME="}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, _ := installing(t)
			app.Environ = tt.environ

			got := launch(t, app, "init", "claude", "--json")

			assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
			assert.Check(t, is.Equal(resulttest.Decode(t, got.stdout).Outcome, result.Refused.String()))
		})
	}
}

func TestApp_Run_WhenInitIsGivenWordsItDoesNotTake_ExitsTwoNamingTheHarnessAndTheWord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
		args []string
	}{
		{name: "no harness"},
		{name: "harness it does not know", args: []string{"codex"}, want: "codex"},
		{name: "word after the harness", args: []string{"claude", "extra"}, want: "extra"},
		{name: "end of flags before the harness", args: []string{"--", "claude"}, want: "--"},
		{name: "end of flags after the harness", args: []string{"claude", "--"}, want: "--"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)

			got := launch(t, app, slices.Concat([]string{"init", "--json"}, tt.args)...)

			assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
			printed := resulttest.Decode(t, got.stdout)
			assert.Check(t, is.Equal(printed.Outcome, result.Refused.String()))
			assert.Check(t, is.Contains(printed.Reason, "claude"))
			assert.Check(t, is.Contains(printed.Reason, tt.want))
			_, err := os.Stat(filepath.Join(home, ".claude"))
			assert.Check(t, os.IsNotExist(err), "a refused run created %s", filepath.Join(home, ".claude"))
		})
	}
}

func TestApp_Run_WhenInitClaudeRunsWithTheProjectFlag_WritesTheSkillsAtTheTopLevelOfTheWorkTree(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	sub := filepath.Join(f.Dir(), "sub")
	assert.NilError(t, os.Mkdir(sub, 0o750))
	app := over(git.Runner{Dir: sub, Env: f.Env()})
	home := t.TempDir()
	app.Environ = []string{"HOME=" + home}
	app.Skills = shipped()

	got := launch(t, app, "init", "claude", "--project")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	for name, file := range shipped() {
		written, err := os.ReadFile(shippedPath(skillsUnder(f.Dir()), name))
		if assert.Check(t, err) {
			assert.Check(t, is.Equal(string(written), string(file.Data)), "file %s", name)
		}
	}
	_, err := os.Stat(skillsUnder(home))
	assert.Check(t, os.IsNotExist(err), "a project run wrote under HOME: %s", skillsUnder(home))
}

func TestApp_Run_WhenInitClaudeRunsWithTheProjectFlagOutsideAWorkTree_ExitsTwoRefused(t *testing.T) {
	t.Parallel()

	app, _ := installing(t)

	got := launch(t, app, "init", "claude", "--project", "--json")

	assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
	assert.Check(t, is.Equal(resulttest.Decode(t, got.stdout).Outcome, result.Refused.String()))
}

func TestApp_Run_WhenInitClaudeRunsWithTheProjectFlagAndGitFails_ExitsOneBroke(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := gittest.New(t, trunk)
	app := over(git.Runner{Dir: f.Dir(), Env: append(f.Env(), gittest.FakeGit(t, gittest.Fake{FailOn: []string{"rev-parse"}})...)})
	app.Environ = []string{"HOME=" + t.TempDir()}
	app.Skills = shipped()

	got := launch(t, app, "init", "claude", "--project", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assert.Check(t, is.Equal(resulttest.Decode(t, got.stdout).Outcome, result.Broke.String()))
	_, err := os.Stat(filepath.Join(f.Dir(), ".claude"))
	assert.Check(t, os.IsNotExist(err), "a run whose git read broke wrote %s", filepath.Join(f.Dir(), ".claude"))
}
