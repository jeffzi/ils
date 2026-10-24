package cli_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

const (
	// outsideStart is how `ils push` and `ils merge` start what they print in a directory that is no
	// repository; the middle is git's own wording.
	outsideStart = "Could not read where HEAD stands: "

	// outsideEnd and outsideMergeEnd are how `ils push` and `ils merge` end it.
	outsideEnd      = "Nothing was written.\nBroke: could not read where HEAD stands\nNext: fix what failed, then run ils push again\n"
	outsideMergeEnd = "Nothing was written.\nBroke: could not read where HEAD stands\nNext: fix what failed, then run ils merge again\n"
)

// stored returns a repository whose git dir is no `.git`, so that a path under it is a git dir
// path to the one who resolved it and to no one else, and a path under that git dir.
func stored(t *testing.T) (f *gittest.Fixture, under string) {
	t.Helper()

	f = gittest.New(t, trunk)
	f.Git("init", "--quiet", "--separate-git-dir", filepath.Join(t.TempDir(), "store"))

	return f, f.Git("rev-parse", "--absolute-git-dir") + "/HEAD"
}

func TestApp_Run_WhenAResultCannotBeWrittenToStdout_ExitsOneWithTheErrorOnStderr(t *testing.T) {
	t.Parallel()

	colorOff, colorOn := outputPath{name: "color off"}, outputPath{name: "color on", color: true}
	tests := []struct {
		path outputPath
		args []string
	}{
		{path: colorOff, args: []string{"push"}},
		{path: colorOff, args: []string{"push", "--json"}},
		{path: colorOff, args: []string{"merge"}},
		{path: colorOff, args: []string{"merge", "--json"}},
		{path: colorOff, args: []string{"pish"}},
		{path: colorOff, args: []string{"pish", "--json"}},
		{path: colorOff, args: []string{"--version"}},
		{path: colorOff, args: []string{"man"}},
		{path: colorOn, args: []string{"man"}},
	}
	for _, tt := range tests {
		t.Run(tt.path.name+"/"+strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()

			app := tt.path.on(t)
			app.Stdout = shut{err: errReaderGone}

			got := launch(t, app, tt.args...)

			assert.Check(t, is.Equal(got.code, exitBroke))
			assert.Check(t, is.Contains(got.stderr, errReaderGone.Error()))
		})
	}
}

func TestApp_Run_WhenJSONFlagIsAmongTheWords_RendersJSONWithoutTheFlagInTheWords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want resulttest.Shown
	}{
		{name: "flag alone", args: []string{"--json"}, want: refusal("no command was given")},
		{name: "flag first", args: []string{"--json", "pish"}, want: refusal("pish is not a command", "pish")},
		{name: "flag last", args: []string{"pish", "--json"}, want: refusal("pish is not a command", "pish")},
		{
			name: "flag between", args: []string{"push", "--json", "extra"},
			want: refusal("push takes no other word, and extra was given", "push", "extra"),
		},
		{name: "command of a later version", args: []string{"status", "--json"}, want: refusal("status is not in this version", "status")},
		{name: "unknown short flag after merge", args: []string{"--json", "merge", "-x"}, want: refusal("merge takes no other word, and -x was given", "merge", "-x")},
		{name: "unknown short flag", args: []string{"--json", "-x"}, want: refusal("-x is not a command", "-x")},
		{name: "unknown flag", args: []string{"--bogus", "--json"}, want: refusal("--bogus is not a command", "--bogus")},
		{name: "unknown short flag after push", args: []string{"--json", "push", "-x"}, want: refusal("push takes no other word, and -x was given", "push", "-x")},
		{name: "flag set to neither, then set", args: []string{"push", "--json=maybe", "--json"}, want: refusal(jsonNeither, "push", "--json=maybe")},
		{name: "flag set to true", args: []string{"--json=true", "pish"}, want: refusal("pish is not a command", "pish")},
		{name: "debug flag set to neither", args: []string{"--json", "--debug=maybe"}, want: refusal(debugNeither)},
		{name: "debug flag set to neither after push", args: []string{"push", "--debug=maybe", "--json"}, want: refusal(debugNeither, "push")},
		{name: "short debug flag set to neither after merge", args: []string{"merge", "-d=maybe", "--json"}, want: refusal(debugNeither, "merge")},
		{name: "color flag set to neither", args: []string{"--no-color=maybe", "--json"}, want: refusal(colorNeither)},
		{name: "color flag set to neither after push", args: []string{"push", "--json", "--no-color=maybe"}, want: refusal(colorNeither, "push")},
		{name: "color flag set to neither after merge", args: []string{"--json", "merge", "--no-color=maybe"}, want: refusal(colorNeither, "merge")},
		{name: "debug and color flags", args: []string{"-d", "pish", "--no-color", "--json"}, want: refusal("pish is not a command", "pish")},
		{
			name: "flag after an unknown flag", args: []string{"push", "--bogus", "--json"},
			want: refusal("push takes no other word, and --bogus was given", "push", "--bogus"),
		},
		{
			name: "flag after an unknown flag and an unknown word", args: []string{"pish", "--bogus", "--json"},
			want: refusal("--bogus is not a command", "pish", "--bogus"),
		},
		{
			name: "flag among the words of a later command", args: []string{"branch", "--json", "tidy", "-x"},
			want: refusal("branch is not in this version", "branch", "tidy", "-x"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			assert.Check(t, is.Equal(got.code, exitRefused))
			assert.Check(t, is.Equal(got.stderr, ""))

			assert.Check(t, is.DeepEqual(resulttest.Decode(t, got.stdout), tt.want, cmpopts.EquateEmpty()))
		})
	}
}

func TestApp_Run_WhenALandingCommandRunsOutsideARepositoryWithoutTheJSONFlag_RendersTheResultAsText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		end  string
		args []string
	}{
		{args: []string{"push"}, end: outsideEnd},
		{args: []string{"push", "--json=false"}, end: outsideEnd},
		{args: []string{"--json=false", "push"}, end: outsideEnd},
		{args: []string{"merge"}, end: outsideMergeEnd},
		{args: []string{"merge", "--json=false"}, end: outsideMergeEnd},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()

			got := launch(t, lost(t), tt.args...)

			assert.Check(t, is.Equal(got.code, exitBroke))
			assert.Check(t, is.Equal(got.stderr, ""))
			startsWith(t, got.stdout, outsideStart)
			endsWith(t, got.stdout, tt.end)
		})
	}
}

func TestApp_Run_WhenALandingCommandRunsOutsideARepositoryWithTheJSONFlag_RendersTheResultWithoutTheFlagInTheWords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command string
		args    []string
	}{
		{command: "push", args: []string{"push", "--json"}},
		{command: "push", args: []string{"--json", "push"}},
		{command: "merge", args: []string{"merge", "--json"}},
		{command: "merge", args: []string{"--json", "merge"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()

			got := launch(t, lost(t), tt.args...)

			assert.Check(t, is.Equal(got.code, exitBroke))
			assert.Check(t, is.Equal(got.stderr, ""))

			printed := resulttest.Decode(t, got.stdout)
			assert.Check(t, is.Equal(printed.Command, tt.command))
			assert.Check(t, is.DeepEqual(printed.Words, []string{tt.command}))
			assert.Check(t, is.Equal(printed.Outcome, "broke"))
		})
	}
}

func TestApp_Run_WhenALineNamesAPathUnderTheGitDirOfTheRepository_ExitsOneWithNothingOnStdout(t *testing.T) {
	t.Parallel()

	tests := [][]string{{"push"}, {"push", "--json"}, {"merge"}, {"merge", "--json"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			f, under := stored(t)
			app := over(git.Runner{Dir: f.Dir(), Env: f.Env()})
			app.SetPush(wrote(under))
			app.SetMerge(wrote(under))

			got := launch(t, app, args...)

			assert.Check(t, is.Equal(got.code, exitBroke))
			assert.Check(t, is.Equal(got.stdout, ""))

			assert.Check(t, is.Contains(got.stderr, "Wrote "+under))
		})
	}
}

func TestApp_Run_WhenALineNamesAPathUnderTheGitDirOfAnotherDirectory_RendersTheLine(t *testing.T) {
	t.Parallel()

	_, under := stored(t)
	app := outside(t)
	app.SetPush(wrote(under))

	got := launch(t, app, "push")

	want := ran{stdout: "Wrote " + under + "\n", code: exitDone}
	assert.Check(t, is.DeepEqual(got, want, sameRun()))
}
