package cli_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/sys/unix"
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

// homeVariable is the variable that names the home directory.
const homeVariable = "HOME"

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
// shipped, with a HOME of its own, and that HOME.
func installing(t *testing.T) (app cli.App, home string) {
	t.Helper()

	return installingOver(t, git.Runner{Dir: t.TempDir(), Env: gittest.Env(t)})
}

// installingOver is installing with every git call made through runner.
func installingOver(t *testing.T, runner git.Runner) (app cli.App, home string) {
	t.Helper()

	home = t.TempDir()
	app = over(runner)
	app.Environ = []string{homeVariable + "=" + home}
	app.Skills = shipped()

	return app, home
}

func skillsUnder(dir string) string {
	return filepath.Join(dir, ".claude", "skills")
}

// shippedPath returns where the shipped file name lands for the Claude Code directory under dir.
func shippedPath(dir, name string) string {
	return filepath.Join(skillsUnder(dir), name)
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
		written, err := os.ReadFile(filepath.Join(claude, "skills", name))
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

func TestApp_Run_WhenInitClaudeInstallsEverySkill_PrintsTheDirectoryOfEach(t *testing.T) {
	t.Parallel()

	app, home := installing(t)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(lineWith(t, got.stdout, filepath.Join(skillsUnder(home), ilsSkill)), "Installed"))
	assert.Check(t, is.Contains(got.stdout, filepath.Join(skillsUnder(home), releaseSkill)))
}

// longAgo returns a modification time no write of a run leaves on a file.
func longAgo() time.Time {
	return time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
}

func backdated(t *testing.T, path string) {
	t.Helper()

	assert.NilError(t, os.Chtimes(path, longAgo(), longAgo()))
}

// assertUntouched checks, without stopping the test, that the file at path still bears the
// modification time longAgo.
func assertUntouched(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	if assert.Check(t, err) {
		assert.Check(t, info.ModTime().Equal(longAgo()), "%s was written again at %v", path, info.ModTime())
	}
}

func TestApp_Run_WhenInitClaudeFindsEveryFileInPlace_LeavesTheFilesUntouched(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	for name := range shipped() {
		backdated(t, shippedPath(home, name))
	}

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	for name := range shipped() {
		assertUntouched(t, shippedPath(home, name))
	}
}

// roomy is a terminal wide enough that no line of `ils init claude` wraps, whatever the length of
// the temporary directories it names.
const roomy = 4096

// onInitScreen returns app with stdout a terminal without color that is the returned screen, roomy
// columns wide, and stderr no terminal.
func onInitScreen(t *testing.T, app cli.App) (cli.App, *screen) {
	t.Helper()

	stdout := newScreen(t, roomy)
	app.Stdout = stdout
	app.StdoutTerminal = true
	app.Environ = append(slices.Clone(app.Environ), colorTerm, noColor)

	return app, stdout
}

// shownStep is a step line as a terminal shows it: its mark, and a word the line holds.
type shownStep struct {
	mark string
	word string
}

// assertShownSteps checks that lines begin with one line per step of want, in order, and stops the
// test unless there are that many lines.
func assertShownSteps(t *testing.T, lines []string, want []shownStep) {
	t.Helper()

	assert.Assert(t, len(lines) >= len(want), "stdout showed %q", lines)
	for i, step := range want {
		assert.Check(t, strings.HasPrefix(lines[i], step.mark+" "), "line %d = %q", i, lines[i])
		assert.Check(t, is.Contains(lines[i], step.word), "line %d", i)
	}
}

// eachStep returns the step lines `ils init claude` reports for the Claude Code directory under
// home, each behind mark: each skill, the hook, then each rule list.
func eachStep(home, mark string) []shownStep {
	settings := settingsUnder(home)

	return []shownStep{
		{mark: mark, word: filepath.Join(skillsUnder(home), ilsSkill)},
		{mark: mark, word: filepath.Join(skillsUnder(home), releaseSkill)},
		{mark: mark, word: settings},
		{mark: mark, word: settings},
		{mark: mark, word: settings},
		{mark: mark, word: settings},
	}
}

func TestApp_Run_WhenInitClaudeEndsDoneOnATerminal_PrintsOneLinePerStepBehindTheMarkOfItsKind(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	app, stdout := onInitScreen(t, app)

	got := shown(t, app, "init", "claude")

	lines := stdout.lines()
	assert.Check(t, is.Equal(got.code, exitDone), "stdout showed %q", lines)
	assert.Check(t, is.Len(lines, len(eachStep(home, "✓"))), "stdout showed %q", lines)
	assertShownSteps(t, lines, eachStep(home, "✓"))
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

func TestApp_Run_WhenInitClaudeFindsAShippedFileChanged_MarksThatSkillAloneChanged(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	editedSkill(t, home)
	app, stdout := onInitScreen(t, app)

	got := shown(t, app, "init", "claude")

	screenText := strings.Join(stdout.lines(), "\n")
	assert.Check(t, is.Equal(got.code, exitDone), "stdout showed %q", screenText)
	updated := lineWith(t, screenText, filepath.Join(skillsUnder(home), ilsSkill))
	assert.Check(t, strings.HasPrefix(updated, "✓ "), "line = %q", updated)
	assert.Check(t, is.Contains(updated, "Updated"))
	kept := lineWith(t, screenText, filepath.Join(skillsUnder(home), releaseSkill))
	assert.Check(t, strings.HasPrefix(kept, "– "), "line = %q", kept)
}

func TestApp_Run_WhenInitClaudeIsRefusedTheSettingsOnATerminal_CrossesTheReadOfTheFileBeforeAnySkillStep(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	path := withSettings(t, home, `[]`)
	app, stdout := onInitScreen(t, app)

	got := shown(t, app, "init", "claude")

	lines := stdout.lines()
	assert.Check(t, is.Equal(got.code, exitRefused), "stdout showed %q", lines)
	assertShownSteps(t, lines, []shownStep{{mark: "✗", word: path}})
	assert.Check(t, !strings.Contains(strings.Join(lines, "\n"), skillsUnder(home)), "stdout showed %q", lines)
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

func TestApp_Run_WhenInitClaudeCannotLookAtASkillDirectory_ExitsOneNamingIt(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	// A file where the skills directory goes makes the look at each skill's directory fail.
	writeText(t, skillsUnder(home), "in the way\n")

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, filepath.Join(skillsUnder(home), ilsSkill)))
}

// assertNoClaudeUnder checks, without stopping the test, that dir holds no Claude Code directory.
func assertNoClaudeUnder(t *testing.T, dir string) {
	t.Helper()

	_, err := os.Stat(filepath.Join(dir, ".claude"))
	assert.Check(t, is.ErrorIs(err, fs.ErrNotExist), "a Claude Code directory landed in %s", dir)
}

func TestApp_Run_WhenInitClaudeFindsTheClaudeConfigDirectoryVariableSetEmpty_InstallsUnderTheHome(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	app.Environ = append(app.Environ, claudeConfigDir+"=")

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
			name: "HOME empty",
			environ: func(string) []string {
				return []string{homeVariable + "="}
			},
			want: homeVariable,
		},
		{
			name: "CLAUDE_CONFIG_DIR not an absolute path",
			environ: func(home string) []string {
				return []string{homeVariable + "=" + home, claudeConfigDir + "=" + filepath.Join("config", "claude")}
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

	app, _, config := configuring(t)
	app.Environ = []string{claudeConfigDir + "=" + config}

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assertShippedIn(t, config)
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

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	entries, err := os.ReadDir(target)
	assert.NilError(t, err)
	assert.Check(t, is.Len(entries, 0), "the run wrote through the link into %s", target)
}

// releaseLinked returns the cli with the directory of the release skill under its HOME a link to
// target, then that HOME and that link.
func releaseLinked(t *testing.T, target string) (app cli.App, home, link string) {
	t.Helper()

	app, home = installing(t)
	link = filepath.Join(skillsUnder(home), releaseSkill)
	linkAt(t, target, link)

	return app, home, link
}

// shippedElsewhere returns a directory outside any Claude Code directory holding the files of the
// skill name as shipped.
func shippedElsewhere(t *testing.T, name string) string {
	t.Helper()

	dir := t.TempDir()
	for file, data := range shipped() {
		if rest, ok := strings.CutPrefix(file, name+"/"); ok {
			writeText(t, filepath.Join(dir, rest), string(data.Data))
		}
	}

	return dir
}

// differingTarget is a target for the link of a skill directory that does not read as the shipped
// skill: target makes it, given the path of the link.
type differingTarget struct {
	target func(t *testing.T, link string) string
	name   string
}

func differingTargets() []differingTarget {
	return []differingTarget{
		{
			name:   "a shipped file missing behind the link",
			target: func(t *testing.T, _ string) string { t.Helper(); return t.TempDir() },
		},
		{
			name: "a shipped file different behind the link",
			target: func(t *testing.T, _ string) string {
				t.Helper()

				dir := t.TempDir()
				writeText(t, filepath.Join(dir, "SKILL.md"), "edited by hand\n")

				return dir
			},
		},

		{
			name: "a link to a file",
			target: func(t *testing.T, _ string) string {
				t.Helper()

				file := filepath.Join(t.TempDir(), "SKILL.md")
				writeText(t, file, string(shipped()[releaseSkill+"/SKILL.md"].Data))

				return file
			},
		},
		{
			name:   "a link to itself",
			target: func(_ *testing.T, link string) string { return link },
		},
	}
}

func linesStartingWith(lines []string, prefix string) []string {
	var found []string
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			found = append(found, line)
		}
	}

	return found
}

func TestApp_Run_WhenInitClaudeFindsASkillDirectoryLinkedToTheShippedFiles_MarksItsStepUnchangedNamingTheTarget(t *testing.T) {
	t.Parallel()

	target := shippedElsewhere(t, releaseSkill)
	app, _, _ := releaseLinked(t, target)
	app, stdout := onInitScreen(t, app)

	got := shown(t, app, "init", "claude")

	screenText := strings.Join(stdout.lines(), "\n")
	assert.Check(t, is.Equal(got.code, exitDone), "stdout showed %q", screenText)
	line := lineWith(t, screenText, target)
	assert.Check(t, strings.HasPrefix(line, "– "), "line = %q", line)
}

func TestApp_Run_WhenInitClaudeFindsASkillDirectoryLinkedToOtherFiles_ExitsOneCrossingItsStepNamingTheTarget(t *testing.T) {
	t.Parallel()

	for _, tt := range differingTargets() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)
			link := filepath.Join(skillsUnder(home), releaseSkill)
			target := tt.target(t, link)
			linkAt(t, target, link)
			app, stdout := onInitScreen(t, app)

			got := shown(t, app, "init", "claude")

			lines := stdout.lines()
			assert.Check(t, is.Equal(got.code, exitBroke), "stdout showed %q", lines)
			crossed := linesStartingWith(lines, "✗ ")
			assert.Assert(t, is.Len(crossed, 1), "stdout showed %q", lines)
			assert.Check(t, is.Contains(crossed[0], target))
		})
	}
}

func TestApp_Run_WhenInitClaudeFindsASkillDirectoryLinkedToOtherFiles_WritesTheHookAndTheRules(t *testing.T) {
	t.Parallel()

	app, home, _ := releaseLinked(t, t.TempDir())

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	want := map[string]any{
		"hooks":       map[string]any{"PreToolUse": []any{hookGroup(cli.UserHook)}},
		"permissions": ruleLists(),
	}
	assert.Check(t, is.DeepEqual(decodedFile(t, settingsUnder(home)), want))
}

func TestApp_Run_WhenInitClaudeFindsSkillDirectoriesLinkedToOtherFiles_NamesEachInTheReason(t *testing.T) {
	t.Parallel()

	const reviewSkill = "review"
	app, home := installing(t)
	skills := shipped()
	skills[reviewSkill+"/SKILL.md"] = &fstest.MapFile{Data: []byte("the review skill\n")}
	app.Skills = skills
	for _, name := range []string{releaseSkill, reviewSkill} {
		linkAt(t, t.TempDir(), filepath.Join(skillsUnder(home), name))
	}

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	reason := resulttest.Decode(t, got.stdout).Reason
	assert.Check(t, is.Contains(reason, releaseSkill))
	assert.Check(t, is.Contains(reason, reviewSkill))
}

func TestApp_Run_WhenInitClaudeFindsASkillDirectoryLinkedToOtherFiles_SaysToRunTheSameWordsAgain(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	app, _ := installingOver(t, git.Runner{Dir: f.Dir(), Env: f.Env()})
	linkAt(t, t.TempDir(), filepath.Join(skillsUnder(f.Dir()), releaseSkill))

	got := launch(t, app, "init", "claude", "--project", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Next, initProject))
}

// marked matches a line that starts with the mark of a step.
var marked = regexp.MustCompile(`^[✓–✗] `)

// linkedPiped arranges `ils init claude` to run piped, with every setting in place and the directory
// of the release skill a link to target; it returns the app, and a word each step line holds in
// order: each skill, the hook, then each rule list.
func linkedPiped(t *testing.T, target string) (app cli.App, want []string) {
	t.Helper()

	app, home, _ := releaseLinked(t, target)
	withSettings(t, home, encodedJSON(t, map[string]any{
		"hooks":       map[string]any{"PreToolUse": []any{hookGroup(cli.UserHook)}},
		"permissions": ruleLists(),
	}))
	settings := settingsUnder(home)

	return app, []string{filepath.Join(skillsUnder(home), ilsSkill), target, settings, settings, settings, settings}
}

// assertUnmarkedSteps checks that lines begin with one line per word of want, in order, each
// holding its word behind no mark, and stops the test unless there are that many lines.
func assertUnmarkedSteps(t *testing.T, lines, want []string) {
	t.Helper()

	assert.Assert(t, len(lines) >= len(want), "lines = %q", lines)
	for i, word := range want {
		assert.Check(t, is.Contains(lines[i], word), "line %d", i)
		assert.Check(t, !marked.MatchString(lines[i]), "line %d = %q", i, lines[i])
	}
}

func TestApp_Run_WhenInitClaudeFindsASkillDirectoryLinkedToTheShippedFilesOffATerminal_ReturnsEveryStepLineInOrderWithoutAMark(t *testing.T) {
	t.Parallel()

	app, want := linkedPiped(t, shippedElsewhere(t, releaseSkill))

	got := launch(t, app, "init", "claude")

	lines := outputLines(got.stdout)
	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Len(lines, len(want)), "lines = %q", lines)
	assertUnmarkedSteps(t, lines, want)
}

func TestApp_Run_WhenInitClaudeFindsASkillDirectoryLinkedToOtherFilesOffATerminal_ReturnsEveryStepLineInOrderWithoutAMark(t *testing.T) {
	t.Parallel()

	app, want := linkedPiped(t, t.TempDir())

	got := launch(t, app, "init", "claude")

	lines := outputLines(got.stdout)
	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assertUnmarkedSteps(t, lines, want)
}

func TestApp_Run_WhenInitClaudeCannotReadThroughASkillDirectoryLink_ExitsOneNamingTheLinkBeforeWritingTheSettings(t *testing.T) {
	t.Parallel()

	skipAsRoot(t)
	target := shippedElsewhere(t, releaseSkill)
	assert.NilError(t, os.Chmod(target, 0o000))
	t.Cleanup(func() {
		//nolint:gosec // G302: the directory is one the test made under t.TempDir().
		assert.Check(t, os.Chmod(target, 0o750))
	})
	app, home, link := releaseLinked(t, target)

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, link))
	_, err := os.Stat(settingsUnder(home))
	assert.Check(t, is.ErrorIs(err, fs.ErrNotExist), "the settings were written")
}

func TestApp_Run_WhenInitClaudeFindsASkillFileThatIsALink_WritesItsTarget(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	target := filepath.Join(t.TempDir(), "SKILL.md")
	assert.NilError(t, os.WriteFile(target, []byte("edited by hand\n"), 0o600))
	linkAt(t, target, shippedPath(home, ilsSkill+"/SKILL.md"))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Equal(readText(t, target), string(shipped()[ilsSkill+"/SKILL.md"].Data)))
}

// skipAsRoot skips the test for a user whom the mode of a directory does not stop.
func skipAsRoot(t *testing.T) {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("the mode of a directory does not stop root")
	}
}

// lockDir takes the write access from dir, a directory the test made under t.TempDir(), until the
// test ends.
func lockDir(t *testing.T, dir string) {
	t.Helper()

	//nolint:gosec // G302: the test takes write access from a directory it made under t.TempDir().
	assert.NilError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() {
		//nolint:gosec // G302: the directory is one the test made under t.TempDir().
		assert.Check(t, os.Chmod(dir, 0o750))
	})
}

// lockedSkill returns the cli with every skill installed under its HOME, the SKILL.md of the ils
// skill edited by hand, and the directory of that skill one nobody may add a file to; then that
// SKILL.md. It skips the test for a user whom the mode of a directory does not stop.
func lockedSkill(t *testing.T) (app cli.App, path string) {
	t.Helper()

	skipAsRoot(t)

	app, home := installing(t)
	installed(t, app)
	path = editedSkill(t, home)
	lockDir(t, filepath.Dir(path))

	return app, path
}

func TestApp_Run_WhenInitClaudeCannotAddAFileBesideAChangedSkillFile_ExitsOneNamingThePath(t *testing.T) {
	t.Parallel()

	app, path := lockedSkill(t)

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, path))
}

func TestApp_Run_WhenInitClaudeCannotWriteTheSettingsOnATerminal_CrossesTheWriteAfterTheSkillSteps(t *testing.T) {
	t.Parallel()

	app, home, path := lockedSettings(t, "{}")
	app, stdout := onInitScreen(t, app)

	got := shown(t, app, "init", "claude")

	lines := stdout.lines()
	assert.Check(t, is.Equal(got.code, exitBroke), "stdout showed %q", lines)
	assertShownSteps(t, lines, slices.Concat(eachStep(home, "–")[:2], []shownStep{{mark: "✗", word: path}}))
	assert.Check(t, is.Len(linesStartingWith(lines, "✓ "), 0), "stdout showed %q", lines)
}
