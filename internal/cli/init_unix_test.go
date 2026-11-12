//go:build unix

package cli_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"golang.org/x/sys/unix"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
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
			writeText(t, filepath.Join(dir, filepath.FromSlash(rest)), string(data.Data))
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
