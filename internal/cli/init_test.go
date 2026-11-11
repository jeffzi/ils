package cli_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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

	return installingOver(t, git.Runner{Dir: t.TempDir(), Env: gittest.Env(t)})
}

// installingOver is installing with every git call made through runner.
func installingOver(t *testing.T, runner git.Runner) (app cli.App, home string) {
	t.Helper()

	home = t.TempDir()
	app = over(runner)
	app.Environ = []string{"HOME=" + home}
	app.Skills = shipped()
	app.SetGuardSupported(true)

	return app, home
}

func skillsUnder(dir string) string {
	return filepath.Join(dir, ".claude", "skills")
}

// shippedPath returns where the shipped file name lands for the Claude Code directory under dir.
func shippedPath(dir, name string) string {
	return filepath.Join(skillsUnder(dir), filepath.FromSlash(name))
}

// installed runs `ils init claude` once with app, and stops the test unless it exits zero.
func installed(t *testing.T, app cli.App) {
	t.Helper()

	got := launch(t, app, "init", "claude")
	assert.Assert(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
}

// editedSkill overwrites the SKILL.md of the ils skill under home as a user would by hand, and
// returns its path.
func editedSkill(t *testing.T, home string) string {
	t.Helper()

	path := shippedPath(home, ilsSkill+"/SKILL.md")
	assert.NilError(t, os.WriteFile(path, []byte("edited by hand\n"), 0o600))

	return path
}

// assertShippedUnder checks, without stopping the test, that every shipped file lies as shipped
// for the Claude Code directory under dir.
func assertShippedUnder(t *testing.T, dir string) {
	t.Helper()

	for name, file := range shipped() {
		written, err := os.ReadFile(shippedPath(dir, name))
		if assert.Check(t, err) {
			assert.Check(t, is.Equal(string(written), string(file.Data)), "file %s", name)
		}
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()

	//nolint:gosec // G304: the path is one the test built under t.TempDir().
	text, err := os.ReadFile(path)
	assert.NilError(t, err)

	return string(text)
}

// writeText writes text to path, making the directory above it first.
func writeText(t *testing.T, path, text string) {
	t.Helper()

	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	assert.NilError(t, os.WriteFile(path, []byte(text), 0o600))
}

func TestApp_Run_WhenInitClaudeRunsWithNoSkillInstalled_WritesEveryShippedFileUnderHome(t *testing.T) {
	t.Parallel()

	app, home := installing(t)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assertShippedUnder(t, home)
}

func TestApp_Run_WhenInitClaudeInstallsEverySkill_PrintsTheDirectoryOfEach(t *testing.T) {
	t.Parallel()

	app, home := installing(t)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(lineWith(t, got.stdout, filepath.Join(skillsUnder(home), ilsSkill)), "Installed"))
	assert.Check(t, is.Contains(got.stdout, filepath.Join(skillsUnder(home), releaseSkill)))
}

func TestApp_Run_WhenInitClaudeFindsEveryFileInPlace_LeavesTheFilesUntouched(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	longAgo := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
	for name := range shipped() {
		assert.NilError(t, os.Chtimes(shippedPath(home, name), longAgo, longAgo))
	}

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	for name := range shipped() {
		info, err := os.Stat(shippedPath(home, name))
		if assert.Check(t, err) {
			assert.Check(t, info.ModTime().Equal(longAgo), "file %s was written again at %v", name, info.ModTime())
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
}

func TestApp_Run_WhenInitClaudeFindsAShippedFileChanged_ReplacesIt(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	changed := editedSkill(t, home)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Equal(readText(t, changed), string(shipped()[ilsSkill+"/SKILL.md"].Data)))
}

func TestApp_Run_WhenInitClaudeFindsAShippedFileChanged_PrintsThatSkillAlone(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	editedSkill(t, home)

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
	assert.Check(t, is.ErrorIs(err, fs.ErrNotExist), "a file beside the skills landed in %s", skillsUnder(home))
}

func TestApp_Run_WhenInitClaudeCannotWriteASkillFile_ExitsOneNamingThePath(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	// A file where the skill's directory goes makes every path under it impossible to write.
	blocked := filepath.Join(skillsUnder(home), ilsSkill)
	writeText(t, blocked, "in the way\n")

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, blocked))
}

func TestApp_Run_WhenInitClaudeHasNoHome_ExitsTwoRefused(t *testing.T) {
	t.Parallel()

	app, _ := installing(t)
	app.Environ = []string{"HOME="}

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
}

func TestApp_Run_WhenInitClaudeFindsHomeInLowercase_InstallsUnderItOnlyOnWindows(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	app.Environ = []string{"home=" + home}
	want := exitRefused
	if runtime.GOOS == "windows" {
		want = exitDone
	}

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, want), "stdout = %q on %s", got.stdout, runtime.GOOS)
}

func TestApp_Run_WhenInitIsGivenNoHarness_ExitsTwoRefused(t *testing.T) {
	t.Parallel()

	app, _ := installing(t)

	got := launch(t, app, "init")

	assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
}

func TestApp_Run_WhenInitIsGivenWordsItDoesNotTake_ExitsTwoNamingTheWord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
		args []string
	}{
		{name: "harness it does not know", args: []string{"codex"}, want: "codex"},
		{name: "word after the harness", args: []string{"claude", "extra"}, want: "extra"},
		{name: "end of flags before the harness", args: []string{"--", "claude"}, want: "--"},
		{name: "end of flags after the harness", args: []string{"claude", "--"}, want: "--"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, _ := installing(t)

			got := launch(t, app, slices.Concat([]string{"init", "--json"}, tt.args)...)

			assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
			assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, tt.want))
		})
	}
}

func TestApp_Run_WhenInitClaudeRunsWithTheProjectFlag_WritesTheSkillsAtTheTopLevelOfTheWorkTree(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	sub := filepath.Join(f.Dir(), "sub")
	assert.NilError(t, os.Mkdir(sub, 0o750))
	app, _ := installingOver(t, git.Runner{Dir: sub, Env: f.Env()})

	got := launch(t, app, "init", "claude", "--project")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assertShippedUnder(t, f.Dir())
}

func TestApp_Run_WhenInitClaudeRunsWithTheProjectFlag_HooksTheScriptOfTheProject(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	app, _ := installingOver(t, git.Runner{Dir: f.Dir(), Env: f.Env()})

	got := launch(t, app, "init", "claude", "--project")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.DeepEqual(preToolUse(t, settingsUnder(f.Dir())), []any{hookGroup(cli.ProjectHook)}))
}

func TestApp_Run_WhenInitClaudeRunsWithTheProjectFlagOutsideAWorkTree_ExitsTwoRefused(t *testing.T) {
	t.Parallel()

	app, _ := installing(t)

	got := launch(t, app, "init", "claude", "--project")

	assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
}

func TestApp_Run_WhenInitClaudeRunsWithTheProjectFlagAndGitFails_ExitsOneBroke(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := gittest.New(t, trunk)
	app, _ := installingOver(t, git.Runner{
		Dir: f.Dir(),
		Env: append(f.Env(), gittest.FakeGit(t, gittest.Fake{FailOn: []string{"rev-parse"}})...),
	})

	got := launch(t, app, "init", "claude", "--project")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
}

func TestApp_Run_WhenInitClaudeRunsWhereTheGuardDoesNot_SaysWhereTheGuardRuns(t *testing.T) {
	t.Parallel()

	app, _ := installing(t)
	app.SetGuardSupported(false)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(got.stdout, cli.GuardUnsupported))
}

func TestApp_Run_WhenInitClaudeChangesOnlyTheSettings_PrintsNoLineSayingAllIsInPlace(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	inPlace := launch(t, app, "init", "claude").stdout
	withSettings(t, home, "{}")

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, !strings.Contains(got.stdout, strings.TrimSpace(inPlace)), "stdout = %q", got.stdout)
}
