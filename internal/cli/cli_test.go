package cli_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// ---------------------------------------------------------------------------
// Run helpers
// ---------------------------------------------------------------------------

const (
	nextHelp = "ils --help"

	// hookLine is a line the renderers reject, since its own wording names a hook.
	hookLine = "The pre-push step ran."

	// outsideStart is how `ils push` and `ils merge` start what they print in a directory that is no
	// repository; the middle is git's own wording.
	outsideStart = "Could not read where HEAD stands: "

	// outsideEnd and outsideMergeEnd are how `ils push` and `ils merge` end it.
	outsideEnd      = "Nothing was written.\nBroke: could not read where HEAD stands\nNext: fix what failed, then run ils push again\n"
	outsideMergeEnd = "Nothing was written.\nBroke: could not read where HEAD stands\nNext: fix what failed, then run ils merge again\n"

	// debugNeither and colorNeither are the reasons a debug and a no-color flag set to maybe are
	// refused for.
	debugNeither = "--debug takes true or false, and maybe was given"
	colorNeither = "--no-color takes true or false, and maybe was given"

	bound = 100 * time.Second
)

// ran is what one run of the cli printed and the code it exited with.
type ran struct {
	stdout string
	stderr string
	code   int
}

// launch runs app with args, launched now unless app says when, and returns what it printed; a
// stdout or stderr app already has is left in place, and nothing is returned for it.
func launch(t *testing.T, app cli.App, args ...string) ran {
	t.Helper()

	return launchUnder(t.Context(), t, app, args...)
}

// launchUnder is launch with the run bound to ctx rather than t.Context(), so a caller can end it
// early.
func launchUnder(ctx context.Context, t *testing.T, app cli.App, args ...string) ran {
	t.Helper()

	var stdout, stderr bytes.Buffer

	if app.Stderr == nil {
		app.Stderr = &stderr
	}
	if app.Stdout == nil {
		app.Stdout = &stdout
	}

	if app.Launch.IsZero() {
		app.Launch = time.Now()
	}

	code := app.Run(ctx, args)

	return ran{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// outside returns the cli in a directory that is no repository.
func outside(t *testing.T) cli.App {
	t.Helper()

	return over(git.Runner{Dir: t.TempDir(), Env: gittest.Env(t)})
}

// over returns the cli with every git call made through runner.
func over(runner git.Runner) cli.App {
	var app cli.App
	app.SetGit(runner)

	return app
}

// refusal is the grammar refusal of the words typed, for reason, as the JSON renderer shows it.
func refusal(reason string, typed ...string) resulttest.Shown {
	command := ""
	if len(typed) > 0 {
		command = typed[0]
	}

	return resulttest.Shown{Command: command, Words: typed, Outcome: "refused", Reason: reason, Next: nextHelp, ExitCode: exitRefused}
}

// finished is a landing command that ended as done with nothing to say.
func finished(req landing.Request) result.Result {
	return result.Result{Command: req.Command, Words: req.Words, Outcome: result.Done, Next: result.NextNone()}
}

// worded returns the phrase say builds from text as its own wording, for wording only a run
// knows, such as a path under a temporary directory.
func worded[W ~string](say func(W, ...any) result.Phrase, text string) result.Phrase {
	return say(W(text))
}

// saying is a landing command that ended as done with line as its one line.
func saying(line result.Phrase) cli.Command {
	return func(_ context.Context, _ landing.Deps, req landing.Request) result.Result {
		r := finished(req)
		r.Lines = []result.Phrase{line}

		return r
	}
}

// wrote is a landing command that ended as done with one line, whose own wording names path.
func wrote(path string) cli.Command {
	return saying(worded(result.Say, "Wrote "+path))
}

// startsWith checks, without stopping the test, that stdout starts with want.
func startsWith(t *testing.T, stdout, want string) {
	t.Helper()

	assert.Check(t, strings.HasPrefix(stdout, want), "stdout = %q, want it to start with %q", stdout, want)
}

// endsWith checks, without stopping the test, that stdout ends with want.
func endsWith(t *testing.T, stdout, want string) {
	t.Helper()

	assert.Check(t, strings.HasSuffix(stdout, want), "stdout = %q, want it to end with %q", stdout, want)
}

// nothingPushed checks, without stopping the test, that the topic branch is not on origin.
func nothingPushed(t *testing.T, s *scene) {
	t.Helper()

	assert.Check(t, is.Equal(s.fixture.OriginGit("for-each-ref", "refs/heads/"+topic), ""))
}

// opensPullRequest reports whether the fake gh was asked to create a pull request.
func opensPullRequest(s *scene) bool {
	createPR := []string{"gh", "pr", "create"}

	return slices.ContainsFunc(s.fake.Calls(), func(argv []string) bool {
		return slices.Equal(argv[:min(len(createPR), len(argv))], createPR)
	})
}

// plain checks, without stopping the test, that text carries no escape sequence; what names the
// text in the failure message.
func plain(t *testing.T, what, text string) {
	t.Helper()

	assert.Check(t, !strings.Contains(text, escape), "%s carries an escape sequence:\n%q", what, text)
}

// stored returns a repository whose git dir is no `.git`, so that a path under it is a git dir
// path to the one who resolved it and to no one else, and a path under that git dir.
func stored(t *testing.T) (f *gittest.Fixture, under string) {
	t.Helper()

	f = gittest.New(t, trunk)
	f.Git("init", "--quiet", "--separate-git-dir", filepath.Join(t.TempDir(), "store"))

	return f, f.Git("rev-parse", "--absolute-git-dir") + "/HEAD"
}

// outdated is a first push with a gh that is logged out and prints line for its version.
func outdated(name, line string) scenario {
	calls := []ghtest.Call{
		ghtest.Says(line+"\n", ghtest.VersionArgs()...),
		login(ghtest.Answer{Stderr: "You are not logged into any GitHub hosts.\n", Code: 1}),
	}

	return scenario{name: name, script: only(calls...), want: exitRefused}
}

// ---------------------------------------------------------------------------
// Grammar, help and rendering
// ---------------------------------------------------------------------------

func TestApp_Run_WhenWordsBreakTheGrammar_RefusesNamingTheWord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
		args   []string
	}{
		{name: "no word", reason: "no command was given"},
		{name: "unknown word", args: []string{"pish"}, reason: "pish is not a command"},
		{name: "extra word", args: []string{"push", "extra"}, reason: "push takes no other word, and extra was given"},
		{name: "end of flags after push", args: []string{"push", "--", "x"}, reason: "push takes no other word, and -- was given"},
		{name: "branch", args: []string{"branch", "tidy"}, reason: "branch is not in this version"},
		{name: "release", args: []string{"release", "1.2.3"}, reason: "release is not in this version"},
		{name: "status", args: []string{"status"}, reason: "status is not in this version"},
		{name: "prefix of a command", args: []string{"pus"}, reason: "pus is not a command"},
		{name: "completion", args: []string{"completion"}, reason: "completion is not a command"},
		{name: "man", args: []string{"man"}, reason: "man is not a command"},
		{name: "later command with an unknown flag", args: []string{"branch", "--bogus"}, reason: "branch is not in this version"},
		{name: "extra word after merge", args: []string{"merge", "extra"}, reason: "merge takes no other word, and extra was given"},
		{name: "end of flags after merge", args: []string{"merge", "--", "x"}, reason: "merge takes no other word, and -- was given"},
		{name: "unknown flag after merge", args: []string{"merge", "--bogus"}, reason: "merge takes no other word, and --bogus was given"},
		{name: "unknown short flag after merge", args: []string{"merge", "-x"}, reason: "merge takes no other word, and -x was given"},
		{name: "version flag after merge", args: []string{"merge", "--version"}, reason: "merge takes no other word, and --version was given"},
		{name: "later command with a word and a short flag", args: []string{"branch", "tidy", "-x"}, reason: "branch is not in this version"},
		{name: "later command with an unknown flag after a word", args: []string{"status", "tidy", "--bogus"}, reason: "status is not in this version"},
		{name: "later command with a short flag", args: []string{"release", "-x"}, reason: "release is not in this version"},
		{name: "unknown flag", args: []string{"--bogus"}, reason: "--bogus is not a command"},
		{name: "unknown short flag", args: []string{"-x"}, reason: "-x is not a command"},
		{name: "short version flag", args: []string{"-v"}, reason: "-v is not a command"},
		{name: "version flag set to false", args: []string{"--version=false"}, reason: "no command was given"},
		{name: "version flag before push", args: []string{"--version", "push"}, reason: "push takes no other word, and --version was given"},
		{name: "version flag before an extra word", args: []string{"--version", "extra"}, reason: "extra is not a command"},
		{name: "unknown flag after push", args: []string{"push", "--bogus"}, reason: "push takes no other word, and --bogus was given"},
		{name: "unknown short flag after push", args: []string{"push", "-x"}, reason: "push takes no other word, and -x was given"},
		{name: "group of short flags after push", args: []string{"push", "-xyz"}, reason: "push takes no other word, and -xyz was given"},
		{name: "version flag after push", args: []string{"push", "--version"}, reason: "push takes no other word, and --version was given"},
		{name: "short version flag after push", args: []string{"push", "-v"}, reason: "push takes no other word, and -v was given"},
		{name: "unknown flag after an extra word", args: []string{"push", "extra", "--bogus"}, reason: "push takes no other word, and --bogus was given"},
		{name: "unknown flag after an unknown word", args: []string{"pish", "--bogus"}, reason: "--bogus is not a command"},
		{name: "flag set to false", args: []string{"pish", "--json=false"}, reason: "pish is not a command"},
		{name: "debug flag after an unknown word", args: []string{"pish", "-d"}, reason: "pish is not a command"},
		{name: "debug flag after an extra word", args: []string{"push", "extra", "--debug"}, reason: "push takes no other word, and extra was given"},
		{name: "debug and color flags alone", args: []string{"-d", "--no-color"}, reason: "no command was given"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			want := ran{stdout: "Refused: " + tt.reason + "\nNext: " + nextHelp + "\n", code: exitRefused}
			assert.Check(t, is.DeepEqual(got, want, sameRun()), "ils %s", strings.Join(tt.args, " "))
		})
	}
}

func TestApp_Run_WhenAFlagIsSetToNeitherTrueNorFalse_RefusesNamingTheFlagAndTheValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
		args   []string
	}{
		{name: "version flag", args: []string{"--version=maybe"}, reason: "--version takes true or false, and maybe was given"},
		{name: "JSON flag after push", args: []string{"push", "--json=maybe"}, reason: "--json takes true or false, and maybe was given"},
		{name: "debug flag", args: []string{"--debug=maybe"}, reason: debugNeither},
		{name: "debug flag after push", args: []string{"push", "--debug=maybe"}, reason: debugNeither},
		{name: "debug flag after merge", args: []string{"merge", "--debug=maybe"}, reason: debugNeither},
		{name: "color flag", args: []string{"--no-color=maybe"}, reason: colorNeither},
		{name: "color flag after push", args: []string{"push", "--no-color=maybe"}, reason: colorNeither},
		{name: "color flag after merge", args: []string{"merge", "--no-color=maybe"}, reason: colorNeither},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			want := ran{stdout: "Refused: " + tt.reason + "\nNext: " + nextHelp + "\n", code: exitRefused}
			assert.Check(t, is.DeepEqual(got, want, sameRun()), "ils %s", strings.Join(tt.args, " "))
		})
	}
}

const (
	// rootHelp, pushHelp and mergeHelp are how the help of ils, of push and of merge each start.
	rootHelp  = "ils lands a branch on GitHub in fixed, checked steps"
	pushHelp  = "Push the current branch, open its pull request on the first push"
	mergeHelp = "Squash-merge the pull request of the current branch once its checks are green"

	// mergeSummary is the line that `ils --help` lists merge with.
	mergeSummary = "merge Squash and merge the pull request once its checks are green"
	// mergeUsage is the usage and the examples the plain help of merge ends with, before its flags.
	mergeUsage = "Usage: ils merge [flags] Examples: # Merge once the checks are green ils merge " +
		"# The same result as one JSON object ils merge --json"

	escape = "\x1b"

	// encodedEscape is escape as the JSON encoder writes it inside a string.
	encodedEscape = `\u001b`

	// forceColor is the variable that makes Fang style help on a stdout that is no terminal.
	forceColor = "CLICOLOR_FORCE"
)

// flat returns the words of text in order, one space apart, so that a check reads past the padding
// that help lays its columns out with.
func flat(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// entries returns the first word of each line the help lists under any of headings, each a line of
// its own, with the comma that follows a short flag dropped.
func entries(help string, headings ...string) []string {
	var found []string

	for _, heading := range headings {
		_, rest, listed := strings.Cut(help, "\n"+heading+"\n")
		if !listed {
			continue
		}

		for line := range strings.SplitSeq(rest, "\n") {
			words := strings.Fields(line)
			if len(words) == 0 {
				break
			}

			found = append(found, strings.TrimSuffix(words[0], ","))
		}
	}

	return found
}

// errReaderGone is the error a stdout that cannot be written fails with.
var errReaderGone = errors.New("the reader went away")

// sameRun lets cmp compare the unexported fields of ran.
func sameRun() cmp.Option {
	return cmp.AllowUnexported(ran{})
}

// shut is a stdout whose every write fails with err.
type shut struct {
	err error
}

// Write fails with err and writes nothing.
func (s shut) Write([]byte) (int, error) {
	return 0, s.err
}

func TestApp_Run_WhenAskedForHelp_PrintsTheHelpOfTheCommand(t *testing.T) {
	t.Parallel()

	root := []string{rootHelp, "--version Print the version of ils", mergeSummary}
	merge := []string{mergeHelp, mergeUsage}
	tests := []struct {
		args []string
		want []string
	}{
		{args: []string{"-h"}, want: []string{rootHelp}},
		{args: []string{"--help"}, want: root},
		{args: []string{"help"}, want: []string{rootHelp, mergeSummary}},
		{args: []string{"pish", "--help"}, want: []string{rootHelp}},
		{args: []string{"-h", "pish"}, want: []string{rootHelp}},
		{args: []string{"help", "pish"}, want: []string{rootHelp}},
		{args: []string{"--json", "-h"}, want: []string{rootHelp}},
		{args: []string{"--help", "--json"}, want: []string{rootHelp}},
		{args: []string{"help", "--json"}, want: []string{rootHelp}},
		{args: []string{"help", "pish", "--json"}, want: []string{rootHelp}},
		{args: []string{"push", "-h"}, want: []string{pushHelp}},
		{args: []string{"push", "--help"}, want: []string{pushHelp}},
		{args: []string{"--help", "push"}, want: []string{pushHelp}},
		{args: []string{"help", "push"}, want: []string{pushHelp}},
		{args: []string{"push", "extra", "--help"}, want: []string{pushHelp}},
		{args: []string{"push", "--json", "-h"}, want: []string{pushHelp}},
		{args: []string{"push", "-d", "--no-color", "--help"}, want: []string{pushHelp}},
		{args: []string{"--json", "push", "--help"}, want: []string{pushHelp}},
		{args: []string{"help", "push", "--json"}, want: []string{pushHelp}},
		{args: []string{"merge", "-h"}, want: merge},
		{
			args: []string{"merge", "--help"},
			want: append([]string{
				"squash-merges the pull request once its checks are green",
				"deletes the branch",
				"never pushes",
				"leaves the trunk checked out",
			}, merge...),
		},
		{args: []string{"help", "merge"}, want: merge},
		{args: []string{"merge", "--json", "--help"}, want: []string{mergeHelp}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			assert.Check(t, is.Equal(got.code, exitDone))
			assert.Check(t, is.Equal(got.stderr, ""))
			help := flat(got.stdout)
			for _, want := range tt.want {
				assert.Check(t, is.Contains(help, want))
			}
			assert.Check(t, is.Len(listedCalls(got.stdout), 0))
		})
	}
}

func TestApp_Run_WhenAskedForHelp_ListsEveryEntryOfTheCommand(t *testing.T) {
	t.Parallel()

	own := []string{"-d", "-h", "--json", "--no-color"}
	tests := []struct {
		args     []string
		commands []string
		flags    []string
	}{
		{args: []string{"--help"}, commands: []string{"help", "merge", "push"}, flags: []string{"-d", "-h", "--json", "--no-color", "--version"}},
		{args: []string{"push", "--help"}, flags: own},
		{args: []string{"merge", "--help"}, flags: own},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			assert.Check(t, is.DeepEqual(entries(got.stdout, "Available Commands:"), tt.commands))
			assert.Check(t, is.DeepEqual(entries(got.stdout, "Flags:", "Global Flags:"), tt.flags, cmpopts.SortSlices(func(a, b string) bool { return a < b })))
		})
	}
}

// listedCalls returns the lines of help that name a `git` or `gh` command, wherever the name
// stands in the line and however it is marked: in backticks, after a bullet, or bare.
func listedCalls(help string) []string {
	var found []string

	for line := range strings.Lines(help) {
		for word := range strings.FieldsSeq(line) {
			if name := strings.Trim(word, "`*-•:,.;()"); name == "git" || name == "gh" {
				found = append(found, strings.TrimSpace(line))

				break
			}
		}
	}

	return found
}

func TestApp_Run_WhenAskedForTheVersion_PrintsTheVersionOfIls(t *testing.T) {
	t.Parallel()

	tests := [][]string{{"--version"}, {"--json", "--version"}, {"--version", "--json"}, {"-d", "--version"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			app := outside(t)
			app.Version = "0.2.0"
			app.SetBuildInfo(built("v9.9.9"), true)

			got := launch(t, app, args...)

			want := ran{stdout: "ils version 0.2.0\n", code: exitDone}
			assert.Check(t, is.DeepEqual(got, want, sameRun()))
		})
	}
}

func TestApp_Run_WhenVersionIsEmpty_PrintsTheVersionTheBuildInfoReaderReports(t *testing.T) {
	t.Parallel()

	app := outside(t)
	app.SetBuildInfo(built("v0.2.0"), true)

	got := launch(t, app, "--version")

	want := ran{stdout: "ils version 0.2.0\n", code: exitDone}
	assert.Check(t, is.DeepEqual(got, want, sameRun()))
}

func TestVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info *debug.BuildInfo
		want string
		ok   bool
	}{
		{name: "release", info: built("v0.2.0"), ok: true, want: "0.2.0"},
		{
			name: "pseudo-version", info: built("v0.2.1-0.20261020093000-abcdef123456+dirty"), ok: true,
			want: "0.2.1-0.20261020093000-abcdef123456+dirty",
		},
		{name: "build info not readable", want: "(devel)"},
		{name: "empty version", info: built(""), ok: true, want: "(devel)"},
		{name: "development build", info: built("(devel)"), ok: true, want: "(devel)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := cli.Version(tt.info, tt.ok)

			assert.Check(t, is.Equal(got, tt.want))
		})
	}
}

// built is a build info whose main module path is fixed; only its version varies.
func built(version string) *debug.BuildInfo {
	return &debug.BuildInfo{Main: debug.Module{Path: "github.com/jeffzi/ils", Version: version}}
}

func TestApp_Run_WhenAskedForHelpInARepository_CallsNeitherGitNorGh(t *testing.T) {
	tests := [][]string{{"push", "--help"}, {"merge", "--help"}, {"merge", "-h"}, {"help", "merge"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			s := newScene(t, scenario{})

			got := launch(t, s.app(), args...)

			assert.Check(t, is.Equal(got.code, exitDone))
			assert.Check(t, is.Len(s.recorder.Calls(), 0))
			assert.Check(t, is.Len(s.fake.Calls(), 0))
		})
	}
}

func TestApp_Run_WhenColorIsForced_RefusalCarriesNoEscapeSequence(t *testing.T) {
	t.Setenv(forceColor, "1")

	tests := [][]string{{"pish"}, {"pish", "--json"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got := launch(t, outside(t), args...)

			assert.Check(t, is.Equal(got.code, exitRefused))
			plain(t, "the refusal", got.stdout)
			assert.Check(t, !strings.Contains(strings.ToLower(got.stdout), encodedEscape), "a value of the refusal carries an escape sequence:\n%s", got.stdout)
		})
	}
}

func TestApp_Run_WhenColorIsForced_PushResultCarriesNoEscapeSequence(t *testing.T) {
	t.Setenv(forceColor, "1")

	text, encoded := both(t, newScene(t, redWithLog()))

	plain(t, "the text", text.stdout)
	plain(t, "the JSON", encoded.stdout)
	assert.Check(t, !strings.Contains(strings.ToLower(encoded.stdout), encodedEscape), "a value of the JSON carries an escape sequence:\n%s", encoded.stdout)
}

func TestApp_Run_WhenHelpIsAskedForWithColorOffOnAColorTerminal_PrintsItWithNoEscapeSequence(t *testing.T) {
	// Fang styles help from the process environment, which forced color would leave styled were Fang
	// to run.
	t.Setenv(forceColor, "1")

	tests := []struct {
		name string
		env  []string
		args []string
	}{
		{name: "flag on the root", env: []string{colorTerm}, args: []string{"--help", "--no-color"}},
		{name: "flag on push", env: []string{colorTerm}, args: []string{"push", "--no-color", "--help"}},
		{name: "flag on merge", env: []string{colorTerm}, args: []string{"merge", "-h", "--no-color=true"}},
		{name: "no color on the root", env: []string{colorTerm, noColor}, args: []string{"--help"}},
		{name: "no color on push", env: []string{colorTerm, noColor}, args: []string{"push", "--help"}},
		{name: "no color on merge", env: []string{colorTerm, noColor}, args: []string{"help", "merge"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := launch(t, painted(t, tt.env, true, true), tt.args...)

			assert.Check(t, is.Equal(got.code, exitDone))
			assert.Check(t, is.Contains(got.stdout, "Usage:"))
			plain(t, "the help", got.stdout)
		})
	}
}

func TestApp_Run_WhenHelpIsAskedForOnAColorTerminal_StylesIt(t *testing.T) {
	// Fang styles help only when the process environment lets it; ils's profile then decides.
	t.Setenv(forceColor, "1")

	tests := [][]string{{"--help"}, {"push", "--help"}, {"merge", "--no-color=false", "--help"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got := launch(t, painted(t, []string{colorTerm}, true, false), args...)

			assert.Check(t, is.Equal(got.code, exitDone))
			assert.Check(t, strings.Contains(got.stdout, escape), "the help carries no escape sequence:\n%q", got.stdout)
		})
	}
}

func TestApp_Run_WhenAResultCannotBeWrittenToStdout_ExitsOneWithTheErrorOnStderr(t *testing.T) {
	t.Parallel()

	tests := [][]string{{"push"}, {"push", "--json"}, {"merge"}, {"merge", "--json"}, {"pish"}, {"pish", "--json"}, {"--version"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			app := outside(t)
			app.Stdout = shut{err: errReaderGone}

			got := launch(t, app, args...)

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
		{name: "flag set to neither, then set", args: []string{"push", "--json=maybe", "--json"}, want: refusal("--json takes true or false, and maybe was given", "push", "--json=maybe")},
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

// lost returns the cli in a directory that is no repository, with git's own words in English.
func lost(t *testing.T) cli.App {
	t.Helper()

	return over(git.Runner{Dir: t.TempDir(), Env: append(gittest.Env(t), "LC_ALL=C")})
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

func TestApp_Run_WhenMergeRuns_EndsItAtTheBoundFromLaunch(t *testing.T) {
	t.Parallel()

	var (
		deadline time.Time
		bounded  bool
	)

	app := outside(t)
	app.Launch = time.Now()
	app.SetMerge(func(ctx context.Context, _ landing.Deps, req landing.Request) result.Result {
		deadline, bounded = ctx.Deadline()

		return finished(req)
	})

	got := launch(t, app, "merge")

	assert.Check(t, is.Equal(got.code, exitDone))
	assert.Check(t, bounded, "ils merge ran with no deadline")
	assert.Check(t, deadline.Equal(app.Launch.Add(bound)), "deadline = %s, want %s", deadline, app.Launch.Add(bound))
}

func TestApp_Run_WhenMergeRunsOnABranchWithNoPullRequest_RefusesNamingPush(t *testing.T) {
	s := newScene(t, noPullRequest())

	got := launch(t, s.app(), "merge")

	assert.Check(t, is.Equal(got.code, exitRefused))
	assert.Check(t, is.Equal(got.stdout,
		"Refused: the branch "+topic+" has no open pull request against the trunk "+trunk+"\nNext: ils push\n"))
}

func TestApp_Run_WhenRendererRejectsTheResult_PrintsTheErrorOnStderrOnly(t *testing.T) {
	t.Parallel()

	hooked := saying(result.Say(hookLine))

	tests := [][]string{{"push"}, {"push", "--json"}, {"merge"}, {"merge", "--json"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			app := outside(t)
			app.SetPush(hooked)
			app.SetMerge(hooked)

			got := launch(t, app, args...)

			assert.Check(t, is.Equal(got.code, exitBroke))
			assert.Check(t, is.Equal(got.stdout, ""))

			assert.Check(t, is.Contains(got.stderr, hookLine))
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

// ---------------------------------------------------------------------------
// Color and debug
// ---------------------------------------------------------------------------

const (
	colorTerm = "TERM=xterm-256color"
	forced    = forceColor + "=1"
	noColor   = "NO_COLOR=1"
)

// painted returns the cli outside any repository under env, with stdout and stderr each a
// terminal or not.
func painted(t *testing.T, env []string, stdoutTerminal, stderrTerminal bool) cli.App {
	t.Helper()

	app := lost(t)
	app.Environ = env
	app.StdoutTerminal = stdoutTerminal
	app.StderrTerminal = stderrTerminal

	return app
}

func TestApp_Run_WhenNoStreamHasAColorProfile_WritesNoEscapeSequence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      []string
		args     []string
		terminal bool
	}{
		{name: "no terminal", args: []string{"push", "-d"}},
		{name: "terminal forced off a terminal", env: []string{colorTerm, "TTY_FORCE=1"}, args: []string{"push", "-d"}},
		{name: "dumb terminal", env: []string{"TERM=dumb"}, terminal: true, args: []string{"push", "-d"}},
		{name: "no color on a terminal", env: []string{colorTerm, noColor}, terminal: true, args: []string{"merge", "--debug"}},
		{name: "no color over forced color", env: []string{colorTerm, noColor, forced}, args: []string{"push", "-d"}},
		{name: "no color over forced color on a terminal", env: []string{colorTerm, noColor, forced}, terminal: true, args: []string{"push", "-d"}},
		{name: "flag over forced color", env: []string{colorTerm, forced}, terminal: true, args: []string{"push", "-d", "--no-color"}},
		{name: "flag set to true before merge", env: []string{colorTerm, forced}, terminal: true, args: []string{"--no-color=true", "merge", "-d"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, painted(t, tt.env, tt.terminal, tt.terminal), tt.args...)

			assert.Check(t, got.stderr != "", "ils %s wrote no debug line", strings.Join(tt.args, " "))
			plain(t, "stdout", got.stdout)
			plain(t, "stderr", got.stderr)
		})
	}
}

func TestApp_Run_WhenStdoutHasAColorProfile_DrawsTheOutcomeLabelRed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		label    string
		env      []string
		args     []string
		terminal bool
	}{
		{name: "color terminal", label: "Broke", env: []string{colorTerm}, terminal: true, args: []string{"push"}},
		{name: "forced color off a terminal", label: "Broke", env: []string{forced}, args: []string{"push"}},
		{name: "flag set to false", label: "Broke", env: []string{colorTerm}, terminal: true, args: []string{"merge", "--no-color=false"}},
		{name: "unknown word", label: "Refused", env: []string{colorTerm}, terminal: true, args: []string{"pish"}},
		{name: "debug flag set to neither", label: "Refused", env: []string{colorTerm}, terminal: true, args: []string{"push", "--debug=maybe"}},
		{name: "color flag set to neither", label: "Refused", env: []string{forced}, args: []string{"merge", "--no-color=maybe"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, painted(t, tt.env, tt.terminal, false), tt.args...)

			look := resulttest.Draw(t, got.stdout).LookOf(t, tt.label+": ", tt.label)
			assert.Check(t, is.Equal(look.Color, "red"))
		})
	}
}

func TestApp_Run_WhenTheWordsAreRefusedWithColorOff_PrintsTheRefusalPlain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  []string
		args []string
	}{
		{name: "no color", env: []string{colorTerm, noColor}, args: []string{"pish"}},
		{name: "flag before an unknown word", env: []string{colorTerm}, args: []string{"--no-color", "pish"}},
		{name: "flag after an unknown flag", env: []string{colorTerm}, args: []string{"push", "--bogus", "--no-color"}},
		{name: "flag set to neither under no color", env: []string{colorTerm, noColor}, args: []string{"merge", "--no-color=maybe"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, painted(t, tt.env, true, false), tt.args...)

			assert.Check(t, is.Equal(got.code, exitRefused))
			plain(t, "the refusal", got.stdout)
		})
	}
}

func TestApp_Run_WhenOneStreamIsAColorTerminal_DrawsColorOnThatStreamOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stdout bool
		stderr bool
	}{
		{name: "stdout", stdout: true},
		{name: "stderr", stderr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, painted(t, []string{colorTerm}, tt.stdout, tt.stderr), "push", "-d")

			assert.Check(t, is.Equal(strings.Contains(got.stdout, escape), tt.stdout), "stdout = %q", got.stdout)
			assert.Check(t, is.Equal(strings.Contains(got.stderr, escape), tt.stderr), "stderr = %q", got.stderr)
		})
	}
}

func TestApp_Run_WhenStdoutIsAColorTerminalAndTheJSONFlagIsAmongTheWords_PrintsNoEscapeSequence(t *testing.T) {
	t.Parallel()

	got := launch(t, painted(t, []string{colorTerm, forced}, true, false), "push", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke))
	plain(t, "the JSON", got.stdout)
	assert.Check(t, !strings.Contains(strings.ToLower(got.stdout), encodedEscape), "a value of the JSON carries an escape sequence:\n%s", got.stdout)
}

func TestApp_Run_WhenDebugOrColorFlagIsAmongTheWords_PrintsWhatTheSameRunPrintsWithoutIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args    []string
		flagged []string
		logs    bool
	}{
		{args: []string{"push"}, flagged: []string{"push", "-d"}, logs: true},
		{args: []string{"push", "--json"}, flagged: []string{"--debug", "push", "--json"}, logs: true},
		{args: []string{"merge"}, flagged: []string{"merge", "--debug=true"}, logs: true},
		{args: []string{"merge", "--json"}, flagged: []string{"merge", "-d", "--json"}, logs: true},
		{args: []string{"push", "--json"}, flagged: []string{"push", "--json", "--debug=false"}},
		{args: []string{"push", "--json"}, flagged: []string{"--no-color", "push", "--json"}},
		{args: []string{"merge", "--json"}, flagged: []string{"merge", "--json", "--no-color=true"}},
		{args: []string{"merge"}, flagged: []string{"merge", "--no-color=false"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.flagged, " "), func(t *testing.T) {
			t.Parallel()

			app := lost(t)
			without := launch(t, app, tt.args...)

			got := launch(t, app, tt.flagged...)

			assert.Check(t, is.Equal(without.stderr, ""))
			assert.Check(t, is.Equal(got.stdout, without.stdout))
			assert.Check(t, is.Equal(got.code, without.code))
			assert.Check(t, is.Equal(got.stderr != "", tt.logs), "stderr = %q", got.stderr)
		})
	}
}

// ---------------------------------------------------------------------------
// Scenes and scenarios
// ---------------------------------------------------------------------------

// both runs `ils push` once in the scene and returns its one result as the text renderer and
// as the JSON renderer print it.
func both(t *testing.T, s *scene) (text, encoded ran) {
	t.Helper()

	var kept result.Result

	app := s.app()
	app.SetPush(func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result {
		kept = landing.Push(ctx, deps, req)

		return kept
	})
	text = launch(t, app, "push")

	app.SetPush(func(context.Context, landing.Deps, landing.Request) result.Result { return kept })
	encoded = launch(t, app, "push", "--json")

	return text, encoded
}

func TestApp_Run_WhenPushEndsInAnyOutcome_JSONCarriesWhatTheTextShows(t *testing.T) {
	tests := []struct {
		outcome  string
		scenario scenario
	}{
		{outcome: "done", scenario: greenFirstPush()},
		{outcome: "refused", scenario: onTheTrunk()},
		{outcome: "broke", scenario: failedRead()},
		{outcome: "pending", scenario: pendingAtTheBound()},
		{outcome: "delivered-not-green", scenario: redWithLog()},
	}
	for _, tt := range tests {
		t.Run(tt.outcome, func(t *testing.T) {
			s := newScene(t, tt.scenario)

			text, encoded := both(t, s)

			assert.Check(t, is.Equal(text.code, tt.scenario.want))
			assert.Check(t, is.Equal(encoded.code, tt.scenario.want))

			got := resulttest.Decode(t, encoded.stdout)
			assert.Check(t, is.Equal(got.Outcome, tt.outcome))
			assert.Check(t, is.Equal(got.ExitCode, tt.scenario.want))

			resulttest.AssertCarried(t, text.stdout, got)
		})
	}
}

func TestApp_Run_WhenOriginNamesAnSSHAlias_AsksGhAboutTheHostItStandsFor(t *testing.T) {
	const alias = "github-personal"

	sc := greenFirstPush()
	sc.ssh = "Host " + alias + "\n  HostName " + host + "\n"
	sc.arrange = func(f *gittest.Fixture) { f.SetOriginURL("git@" + alias + ":" + owner + "/" + widgets + ".git") }
	s := newScene(t, sc)

	got := launch(t, s.app(), "push")

	assert.Check(t, is.Equal(got.code, exitDone), "ils push printed %q", got.stdout)
	assert.Check(t, is.Contains(s.fake.Calls(), append([]string{"gh"}, ghtest.LoginArgs(host)...)))
}

func TestApp_Run_WhenGhIsOlderThanTheMinimum_RefusesWithNothingPushedOrOpened(t *testing.T) {
	const (
		next   = "upgrade gh, then run ils push again"
		reason = "gh 1.0.0 is older than " + github.MinimumVersion +
			", the oldest version ils works with; upgrade it from https://cli.github.com"
	)

	s := newScene(t, outdated("an older release", "gh version 1.0.0"))

	text, encoded := both(t, s)

	want := ran{stdout: "Refused: " + reason + "\nNext: " + next + "\n", code: exitRefused}
	assert.Check(t, is.DeepEqual(text, want, sameRun()))
	assert.Check(t, is.Equal(encoded.code, exitRefused))
	got := resulttest.Decode(t, encoded.stdout)
	assert.Check(t, is.Equal(got.Outcome, "refused"))
	assert.Check(t, is.Equal(got.Reason, reason))
	assert.Check(t, is.Equal(got.Next, next))
	nothingPushed(t, s)
	assert.Check(t, !opensPullRequest(s), "gh was asked to create a pull request")
}

func TestApp_Run_WhenGhIsNotInstalled_RefusesNamingTheInstallPage(t *testing.T) {
	s := newScene(t, scenario{})
	t.Setenv("GH_PATH", filepath.Join(t.TempDir(), "gh"))

	text, encoded := both(t, s)

	want := ran{
		stdout: "Refused: gh is not installed; install it from https://cli.github.com\nNext: install gh, then run ils push again\n",
		code:   exitRefused,
	}
	assert.Check(t, is.DeepEqual(text, want, sameRun()))
	assert.Check(t, is.Equal(encoded.code, exitRefused))
	assert.Check(t, is.Equal(resulttest.Decode(t, encoded.stdout).Outcome, "refused"))
	nothingPushed(t, s)
}
