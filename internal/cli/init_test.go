package cli_test

import (
	"context"
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
	"mvdan.cc/sh/v3/shell"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// The skills the tests ship, by the directory each one lands in.
const (
	ilsSkill     = "ils"
	releaseSkill = "write-release"
)

// claudeConfigDir is the variable that names the Claude Code directory in place of the one under
// the home directory.
const claudeConfigDir = "CLAUDE_CONFIG_DIR"

func homeVariable() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}

	return "HOME"
}

func otherHomeVariable() string {
	if runtime.GOOS == "windows" {
		return "HOME"
	}

	return "USERPROFILE"
}

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
	app.Environ = []string{homeVariable() + "=" + home}
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

// configuring returns the cli as installing does, with a CLAUDE_CONFIG_DIR that does not exist yet
// and whose name holds a space and a quote; then its home and that directory.
func configuring(t *testing.T) (app cli.App, home, config string) {
	t.Helper()

	app, home = installing(t)
	config = filepath.Join(t.TempDir(), "Jane's claude")
	app.Environ = append(app.Environ, claudeConfigDir+"="+config)

	return app, home, config
}

// assertShippedUnder checks, without stopping the test, that every shipped file lies as shipped
// for the Claude Code directory under dir.
func assertShippedUnder(t *testing.T, dir string) {
	t.Helper()

	assertShippedIn(t, filepath.Join(dir, ".claude"))
}

// assertShippedIn checks, without stopping the test, that every shipped file lies as shipped for
// the Claude Code directory claude.
func assertShippedIn(t *testing.T, claude string) {
	t.Helper()

	for name, file := range shipped() {
		//nolint:gosec // G304: the path is a shipped file under the Claude Code directory of the test.
		written, err := os.ReadFile(filepath.Join(claude, "skills", filepath.FromSlash(name)))
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

// assertNoClaudeUnder checks, without stopping the test, that dir holds no Claude Code directory.
func assertNoClaudeUnder(t *testing.T, dir string) {
	t.Helper()

	_, err := os.Stat(filepath.Join(dir, ".claude"))
	assert.Check(t, is.ErrorIs(err, fs.ErrNotExist), "a Claude Code directory landed in %s", dir)
}

func TestApp_Run_WhenInitClaudeFindsTheHomeOfEveryPlatform_InstallsUnderTheHomeOfThisOne(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	app.Environ = append(app.Environ, otherHomeVariable()+"="+t.TempDir())

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assertShippedUnder(t, home)
}

func TestApp_Run_WhenInitClaudeFindsNoUsableClaudeDirectory_ExitsTwoNamingTheVariable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		environ func(home string) []string
		name    string
		want    string
	}{
		{
			name: "home of this platform empty, home of the others set",
			environ: func(home string) []string {
				return []string{homeVariable() + "=", otherHomeVariable() + "=" + home}
			},
			want: homeVariable(),
		},
		{
			name: "CLAUDE_CONFIG_DIR not an absolute path",
			environ: func(home string) []string {
				return []string{homeVariable() + "=" + home, claudeConfigDir + "=" + filepath.Join("config", "claude")}
			},
			want: claudeConfigDir,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)
			app.Environ = tt.environ(home)

			got := launch(t, app, "init", "claude", "--json")

			assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
			assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, tt.want))
			assertNoClaudeUnder(t, home)
		})
	}
}

func TestApp_Run_WhenInitClaudeFindsAClaudeConfigDirectory_WritesEveryShippedFileInIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		homeless bool
	}{
		{name: "home set"},
		{name: "home unset", homeless: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home, config := configuring(t)
			if tt.homeless {
				app.Environ = []string{claudeConfigDir + "=" + config}
			}

			got := launch(t, app, "init", "claude")

			assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
			assertShippedIn(t, config)
			assertNoClaudeUnder(t, home)
		})
	}
}

// hookCommand returns the command of the one hook of the one PreToolUse group of the settings file
// at path, and stops the test unless there is exactly one.
func hookCommand(t *testing.T, path string) string {
	t.Helper()

	groups, _ := preToolUse(t, path).([]any)
	assert.Assert(t, is.Len(groups, 1), "PreToolUse = %v", groups)
	group, _ := groups[0].(map[string]any)
	hooks, _ := group["hooks"].([]any)
	assert.Assert(t, is.Len(hooks, 1), "group = %v", group)
	hook, _ := hooks[0].(map[string]any)
	command, ok := hook["command"].(string)
	assert.Assert(t, ok, "hook = %v", hook)

	return command
}

func TestApp_Run_WhenInitClaudeFindsAClaudeConfigDirectory_HooksTheInstalledScriptAsOneWord(t *testing.T) {
	t.Parallel()

	app, _, config := configuring(t)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	words, err := shell.Fields(hookCommand(t, filepath.Join(config, "settings.json")), func(string) string { return "" })
	assert.NilError(t, err)
	assert.Check(t, is.DeepEqual(words, []string{"bash", filepath.Join(config, "skills", ilsSkill, "hook.sh")}))
}

func TestApp_Run_WhenInitClaudeRunsTwiceWithAClaudeConfigDirectory_LeavesTheSettingsAsTheFirstRunLeftThem(t *testing.T) {
	t.Parallel()

	app, _, config := configuring(t)
	installed(t, app)
	path := filepath.Join(config, "settings.json")
	first := readText(t, path)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Equal(readText(t, path), first))
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

func TestApp_Run_WhenInitClaudeRunsWithTheProjectFlag_WritesNothingInTheClaudeConfigDirectory(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	app, _ := installingOver(t, git.Runner{Dir: f.Dir(), Env: f.Env()})
	config := t.TempDir()
	app.Environ = append(app.Environ, claudeConfigDir+"="+config)

	got := launch(t, app, "init", "claude", "--project")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assertShippedUnder(t, f.Dir())
	entries, err := os.ReadDir(config)
	assert.NilError(t, err)
	assert.Check(t, is.Len(entries, 0), "the run wrote into %s", config)
}

func TestApp_Run_WhenInitClaudeRunsWithTheProjectFlagOutsideAWorkTree_ExitsTwoRefused(t *testing.T) {
	t.Parallel()

	app, _ := installing(t)

	got := launch(t, app, "init", "claude", "--project")

	assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
}

// initProject is the command line that runs `ils init claude --project` again.
const initProject = "ils init claude --project"

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

func TestApp_Run_WhenInitClaudeRunsWithTheProjectFlagAndNoGit_ExitsTwoSayingToInstallGit(t *testing.T) {
	// Not parallel: this empties PATH for the whole process while it runs.
	f := gittest.New(t, trunk)
	app, _ := installingOver(t, git.Runner{Dir: f.Dir(), Env: f.Env()})
	t.Setenv("PATH", t.TempDir())

	got := launch(t, app, "init", "claude", "--project", "--json")

	assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)

	next := resulttest.Decode(t, got.stdout).Next
	assert.Check(t, is.Contains(next, "install git"))
	assert.Check(t, is.Contains(next, initProject))
}

func TestApp_Run_WhenInitClaudeRunsWithTheProjectFlagPastTheTimeLimit_ExitsTwoNamingTheCommandToRunAgain(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	app, _ := installingOver(t, git.Runner{Dir: f.Dir(), Env: f.Env()})
	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
	t.Cleanup(cancel)

	got := launchUnder(ctx, t, app, "init", "claude", "--project", "--json")

	assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)

	printed := resulttest.Decode(t, got.stdout)
	assert.Check(t, is.Contains(printed.Reason, landing.Span(landing.TimeLimit)))
	assert.Check(t, is.Equal(printed.Next, initProject))
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
