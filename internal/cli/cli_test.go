package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/git"
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

	// outsideStart and outsideEnd are how `ils push` starts and ends what it prints in a directory
	// that is no repository; the middle is git's own wording.
	outsideStart = "Could not read where HEAD stands: "
	outsideEnd   = "Nothing was written.\nNext: fix what failed, then run ils push again\n"

	bound = 100 * time.Second
	// grace is how long past the bound a run may come back.
	grace = 2 * time.Second
	// held is how long a held call stays open, far past the bound, and heldLeft is the time left
	// under the bound when a run with a held call is launched.
	held     = 30 * time.Second
	heldLeft = 3 * time.Second
)

// ran is what one run of the cli printed and the code it exited with.
type ran struct {
	stdout string
	stderr string
	code   int
}

// launch runs app with args, launched now unless app says when, and returns what it printed.
func launch(t *testing.T, app cli.App, args ...string) ran {
	t.Helper()

	var stdout, stderr bytes.Buffer

	app.Stdout, app.Stderr = &stdout, &stderr
	if app.Launch.IsZero() {
		app.Launch = time.Now()
	}

	code := app.Run(t.Context(), args)

	return ran{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// outside returns the cli in a directory that is no repository.
func outside(t *testing.T) cli.App {
	t.Helper()

	return cli.App{Git: git.Runner{Dir: t.TempDir()}}
}

// refusal is the grammar refusal of the words typed, for reason, as the JSON renderer shows it.
func refusal(reason string, typed ...string) resulttest.Shown {
	command := ""
	if len(typed) > 0 {
		command = typed[0]
	}

	return resulttest.Shown{Command: command, Words: typed, Outcome: "refused", Reason: reason, Next: nextHelp, ExitCode: exitRefused}
}

// finished is a push that ended as done with nothing to say.
func finished(req landing.Request) result.Result {
	return result.Result{Command: req.Command, Words: req.Words, Outcome: result.Done, Next: result.NextNone()}
}

// worded returns the phrase say builds from text as its own wording, for wording only a run
// knows, such as a path under a temporary directory.
func worded[W ~string](say func(W, ...any) result.Phrase, text string) result.Phrase {
	return say(W(text))
}

// saying is a push that ended as done with line as its one line.
func saying(line result.Phrase) cli.Command {
	return func(_ context.Context, _ landing.Deps, req landing.Request) result.Result {
		r := finished(req)
		r.Lines = []result.Phrase{line}

		return r
	}
}

// wrote is a push that ended as done with one line, whose own wording names path.
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

// stored returns a repository whose git dir is no `.git`, so that a path under it is a git dir
// path to the one who resolved it and to no one else, and a path under that git dir.
func stored(t *testing.T) (f *gittest.Fixture, under string) {
	t.Helper()

	f = gittest.New(t, trunk)
	f.Git("init", "--quiet", "--separate-git-dir", filepath.Join(t.TempDir(), "store"))

	return f, f.Git("rev-parse", "--absolute-git-dir") + "/HEAD"
}

// timed runs `ils push` in a scene of sc, and fails the test unless it exits as sc wants and
// comes back no later than grace past the bound.
func timed(t *testing.T, sc scenario) (*scene, ran) {
	t.Helper()

	s := newScene(t, sc)
	app := s.app()

	got := launch(t, app, "push")
	late := time.Since(app.Launch.Add(bound))

	assert.Equal(t, got.code, sc.want, "stdout:\n%s\nstderr:\n%s", got.stdout, got.stderr)
	assert.Check(t, late <= grace, "ils push returned %s past the bound, want it back no later than %s past it", late, grace)

	return s, got
}

// heldChecks is a first push whose read of the checks is still open at the bound.
func heldChecks() scenario {
	slow := func(sha string) verdict {
		v := passing(sha)
		v.checks.Delay = held

		return v
	}

	return scenario{name: "the read of the checks", script: firstPush(slow), left: heldLeft, want: exitPending}
}

// heldIdentity is a push whose read of the repository's identity is still open at the bound.
func heldIdentity() scenario {
	calls := allowed()
	calls[1].Answers[0].Delay = held

	return scenario{name: "the read of the identity", script: only(calls...), left: heldLeft, want: exitRefused}
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
		{name: "branch", args: []string{"branch", "tidy"}, reason: "branch is not in this version"},
		{name: "merge", args: []string{"merge"}, reason: "merge is not in this version"},
		{name: "release", args: []string{"release", "1.2.3"}, reason: "release is not in this version"},
		{name: "status", args: []string{"status"}, reason: "status is not in this version"},
		{name: "prefix of a command", args: []string{"pus"}, reason: "pus is not a command"},
		{name: "completion", args: []string{"completion"}, reason: "completion is not a command"},
		{name: "man", args: []string{"man"}, reason: "man is not a command"},
		{name: "later command with an unknown flag", args: []string{"merge", "--bogus"}, reason: "merge is not in this version"},
		{name: "later command with a word and a short flag", args: []string{"branch", "tidy", "-x"}, reason: "branch is not in this version"},
		{name: "later command with an unknown flag after a word", args: []string{"status", "tidy", "--bogus"}, reason: "status is not in this version"},
		{name: "later command with a short flag", args: []string{"release", "-x"}, reason: "release is not in this version"},
		{name: "unknown flag", args: []string{"--bogus"}, reason: "--bogus is not a command"},
		{name: "unknown short flag", args: []string{"-x"}, reason: "-x is not a command"},
		{name: "version flag", args: []string{"--version"}, reason: "--version is not a command"},
		{name: "short version flag", args: []string{"-v"}, reason: "-v is not a command"},
		{name: "unknown flag after push", args: []string{"push", "--bogus"}, reason: "push takes no other word, and --bogus was given"},
		{name: "unknown short flag after push", args: []string{"push", "-x"}, reason: "push takes no other word, and -x was given"},
		{name: "group of short flags after push", args: []string{"push", "-xyz"}, reason: "push takes no other word, and -xyz was given"},
		{name: "version flag after push", args: []string{"push", "--version"}, reason: "push takes no other word, and --version was given"},
		{name: "short version flag after push", args: []string{"push", "-v"}, reason: "push takes no other word, and -v was given"},
		{name: "unknown flag after an extra word", args: []string{"push", "extra", "--bogus"}, reason: "push takes no other word, and --bogus was given"},
		{name: "unknown flag after an unknown word", args: []string{"pish", "--bogus"}, reason: "--bogus is not a command"},
		{name: "flag set to false", args: []string{"pish", "--json=false"}, reason: "pish is not a command"},
		{name: "flag set to neither", args: []string{"push", "--json=maybe"}, reason: "--json takes true or false, and maybe was given"},
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

// rootHelp, helpHelp and pushHelp are what `ils --help`, `ils help` and `ils push --help` print,
// line for line, without the spaces Fang pads each line with.
const (
	rootHelp = `
  ils lands a branch on GitHub in fixed, checked steps: push, pull request,
  CI verdict. It exists so that a coding agent cannot push to the wrong
  place, skip the checks, or call a red build done.

  Docs: https://github.com/jeffzi/ils

  USAGE

    ils [command] [--flags]

  EXAMPLES

    ils push

  COMMANDS

    help [command]  Help about any command
    push            Push the branch, open its pull request, wait for the checks

  FLAGS

    -h --help       Help for ils
    --json          Print the result as one JSON object

`

	// helpHelp differs from rootHelp by the entry of the help command, which Fang writes with the
	// flags that command takes.
	helpHelp = `
  ils lands a branch on GitHub in fixed, checked steps: push, pull request,
  CI verdict. It exists so that a coding agent cannot push to the wrong
  place, skip the checks, or call a red build done.

  Docs: https://github.com/jeffzi/ils

  USAGE

    ils [command] [--flags]

  EXAMPLES

    ils push

  COMMANDS

    help [command] [--flags]  Help about any command
    push                      Push the branch, open its pull request, wait for the checks

  FLAGS

    -h --help                 Help for ils
    --json                    Print the result as one JSON object

`

	pushHelp = `
  Push the current branch, open its pull request on the first push, and
  wait up to 100 seconds for the checks of the pushed commit. It never
  forces a push and it does not merge.

  It refuses before pushing anything when the branch is not ready, for
  example on the default branch, with uncommitted changes, or behind its
  copy on GitHub. The output gives the reason and the next step.

  USAGE

    ils push [--flags]

  EXAMPLES

    # Push and wait for the checks
    ils push

    # The same result as one JSON object
    ils push --json

  FLAGS

    -h --help  Help for push
    --json     Print the result as one JSON object

`

	escape = "\x1b"

	// encodedEscape is escape as the JSON encoder writes it inside a string.
	encodedEscape = `\u001b`

	// forceColor is the variable that makes Fang style help on a stdout that is no terminal.
	forceColor = "CLICOLOR_FORCE"
)

// flat returns the words of text in order, one space apart: Fang spaces the lines of styled help
// differently from those of plain help.
func flat(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// trimmed returns text without the spaces that end its lines.
func trimmed(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}

	return strings.Join(lines, "\n")
}

func unstyled(text string) string {
	return styling.ReplaceAllString(text, "")
}

// errReaderGone is the error a stdout that cannot be written fails with.
var errReaderGone = errors.New("the reader went away")

// sameRun lets cmp compare the unexported fields of ran.
func sameRun() cmp.Option {
	return cmp.AllowUnexported(ran{})
}

// styling matches the escape sequences that style text.
var styling = regexp.MustCompile(escape + `\[[0-9;:]*m`)

// shut is a stdout whose every write fails with err.
type shut struct {
	err error
}

func (s shut) Write([]byte) (int, error) {
	return 0, s.err
}

// shutOut runs app with args on a stdout that cannot be written, and returns what it printed on
// stderr and the code it exited with.
func shutOut(t *testing.T, app cli.App, failure error, args ...string) ran {
	t.Helper()

	var stderr bytes.Buffer

	app.Stdout, app.Stderr, app.Launch = shut{err: failure}, &stderr, time.Now()

	code := app.Run(t.Context(), args)

	return ran{stderr: stderr.String(), code: code}
}

func TestApp_Run_WhenAskedForHelp_PrintsTheHelpOfTheCommandAndRunsNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want string
		args []string
	}{
		{args: []string{"-h"}, want: rootHelp},
		{args: []string{"--help"}, want: rootHelp},
		{args: []string{"help"}, want: helpHelp},
		{args: []string{"pish", "--help"}, want: rootHelp},
		{args: []string{"-h", "pish"}, want: rootHelp},
		{args: []string{"help", "pish"}, want: helpHelp},
		{args: []string{"--json", "-h"}, want: rootHelp},
		{args: []string{"--help", "--json"}, want: rootHelp},
		{args: []string{"help", "--json"}, want: helpHelp},
		{args: []string{"help", "pish", "--json"}, want: helpHelp},
		{args: []string{"push", "-h"}, want: pushHelp},
		{args: []string{"push", "--help"}, want: pushHelp},
		{args: []string{"--help", "push"}, want: pushHelp},
		{args: []string{"help", "push"}, want: pushHelp},
		{args: []string{"push", "extra", "--help"}, want: pushHelp},
		{args: []string{"push", "--json", "-h"}, want: pushHelp},
		{args: []string{"--json", "push", "--help"}, want: pushHelp},
		{args: []string{"help", "push", "--json"}, want: pushHelp},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()

			ranPush := false
			app := outside(t)
			app.Git.Recorder = &git.Recorder{}
			app.SetPush(func(_ context.Context, _ landing.Deps, req landing.Request) result.Result {
				ranPush = true

				return finished(req)
			})

			got := launch(t, app, tt.args...)

			assert.Check(t, is.Equal(got.code, exitDone))
			assert.Check(t, is.Equal(got.stderr, ""))
			assert.Check(t, is.Equal(trimmed(got.stdout), tt.want))
			assert.Check(t, !strings.Contains(got.stdout, escape), "help carries an escape sequence:\n%q", got.stdout)
			assert.Check(t, !ranPush, "help ran push")
			assert.Check(t, is.Len(app.Git.Recorder.Calls(), 0))
		})
	}
}

func TestApp_Run_WhenColorIsForced_StylesTheHelpAndKeepsItsWords(t *testing.T) {
	t.Setenv(forceColor, "1")

	tests := []struct {
		name string
		want string
		args []string
	}{
		{name: "root", args: []string{"--help"}, want: rootHelp},
		{name: "push", args: []string{"push", "--help"}, want: pushHelp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := launch(t, outside(t), tt.args...)

			assert.Check(t, is.Equal(got.code, exitDone))
			assert.Check(t, is.Contains(got.stdout, escape))
			assert.Check(t, is.Equal(flat(unstyled(got.stdout)), flat(tt.want)))
		})
	}
}

func TestApp_Run_WhenAskedForHelpInARepository_CallsNeitherGitNorGh(t *testing.T) {
	tests := [][]string{
		{"--help"}, {"-h"}, {"pish", "--help"}, {"-h", "pish"}, {"push", "--help"}, {"push", "extra", "--help"}, {"help", "push"},
	}
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
			assert.Check(t, !strings.Contains(got.stdout, escape), "the refusal carries an escape sequence:\n%q", got.stdout)
		})
	}
}

func TestApp_Run_WhenPushEndsInAnyWay_NoResultCarriesAnEscapeSequence(t *testing.T) {
	tests := []struct {
		name  string
		force string
	}{
		{name: "color forced", force: "1"},
		{name: "color not forced", force: ""},
	}
	for _, tt := range tests {
		for _, sc := range scenarios() {
			t.Run(tt.name+"/"+sc.name, func(t *testing.T) {
				t.Setenv(forceColor, tt.force)

				text, encoded := both(t, newScene(t, sc))

				assert.Check(t, is.Equal(text.code, sc.want), "%s\nstdout:\n%s\nstderr:\n%s", sc.name, text.stdout, text.stderr)
				assert.Check(t, !strings.Contains(text.stdout, escape), "the text carries an escape sequence:\n%q", text.stdout)
				assert.Check(t, !strings.Contains(encoded.stdout, escape), "the JSON carries an escape sequence:\n%q", encoded.stdout)
				assert.Check(t, !strings.Contains(strings.ToLower(encoded.stdout), encodedEscape), "a value of the JSON carries an escape sequence:\n%s", encoded.stdout)
			})
		}
	}
}

func TestApp_Run_WhenAskedForCompletions_ExitsZeroWithoutARefusal(t *testing.T) {
	t.Parallel()

	tests := []string{"__complete", "__completeNoDesc"}
	for _, word := range tests {
		t.Run(word, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), word, "push")

			assert.Check(t, is.Equal(got.code, exitDone))
			assert.Check(t, !strings.Contains(got.stdout, "Refused"), "stdout = %q, want no refusal", got.stdout)
		})
	}
}

func TestApp_Run_WhenAResultCannotBeWrittenToStdout_ExitsOneWithTheErrorOnStderr(t *testing.T) {
	t.Parallel()

	tests := [][]string{{"push"}, {"push", "--json"}, {"pish"}, {"pish", "--json"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			app := outside(t)
			app.SetPush(func(_ context.Context, _ landing.Deps, req landing.Request) result.Result { return finished(req) })

			got := shutOut(t, app, errReaderGone, args...)

			assert.Check(t, is.Equal(got.code, exitBroke))
			assert.Check(t, is.Contains(got.stderr, errReaderGone.Error()))
		})
	}
}

func TestApp_Run_WhenHelpCannotBeWrittenToStdout_ExitsZeroWithNothingOnStderr(t *testing.T) {
	t.Parallel()

	tests := [][]string{{"--help"}, {"help"}, {"push", "--help"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			got := shutOut(t, outside(t), errReaderGone, args...)

			want := ran{code: exitDone}
			assert.Check(t, is.DeepEqual(got, want, sameRun()))
		})
	}
}

func TestApp_Run_WhenPushRunsWithoutTheJSONFlag_RendersTextAndKeepsTheFlagOutOfTheWords(t *testing.T) {
	t.Parallel()

	tests := [][]string{{"push"}, {"push", "--json=false"}, {"--json=false", "push"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			var words []string

			app := outside(t)
			app.SetPush(func(_ context.Context, _ landing.Deps, req landing.Request) result.Result {
				words = req.Words

				return finished(req)
			})

			got := launch(t, app, args...)

			want := ran{stdout: "Next: none\n", code: exitDone}
			assert.Check(t, is.DeepEqual(got, want, sameRun()))
			assert.Check(t, is.DeepEqual(words, []string{"push"}))
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
		{name: "command of a later version", args: []string{"merge", "--json"}, want: refusal("merge is not in this version", "merge")},
		{name: "prefix of a command", args: []string{"--json", "pus"}, want: refusal("pus is not a command", "pus")},
		{name: "completion", args: []string{"completion", "--json"}, want: refusal("completion is not a command", "completion")},
		{name: "man", args: []string{"man", "--json"}, want: refusal("man is not a command", "man")},
		{name: "unknown short flag", args: []string{"--json", "-x"}, want: refusal("-x is not a command", "-x")},
		{name: "version flag", args: []string{"--json", "--version"}, want: refusal("--version is not a command", "--version")},
		{name: "short version flag", args: []string{"--json", "-v"}, want: refusal("-v is not a command", "-v")},
		{name: "unknown flag", args: []string{"--bogus", "--json"}, want: refusal("--bogus is not a command", "--bogus")},
		{name: "unknown short flag after push", args: []string{"--json", "push", "-x"}, want: refusal("push takes no other word, and -x was given", "push", "-x")},
		{name: "version flag after push", args: []string{"push", "--version", "--json"}, want: refusal("push takes no other word, and --version was given", "push", "--version")},
		{name: "short version flag after push", args: []string{"--json", "push", "-v"}, want: refusal("push takes no other word, and -v was given", "push", "-v")},
		{name: "flag set to neither, then set", args: []string{"push", "--json=maybe", "--json"}, want: refusal("--json takes true or false, and maybe was given", "push", "--json=maybe")},
		{name: "release", args: []string{"release", "--json"}, want: refusal("release is not in this version", "release")},
		{name: "status", args: []string{"--json", "status"}, want: refusal("status is not in this version", "status")},
		{name: "flag set to true", args: []string{"--json=true", "pish"}, want: refusal("pish is not a command", "pish")},
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

	app := outside(t)
	app.Git.Env = append(os.Environ(), "LC_ALL=C")

	return app
}

func TestApp_Run_WhenPushRunsOutsideARepository_RendersTheResultOfPush(t *testing.T) {
	t.Parallel()

	got := launch(t, lost(t), "push")

	assert.Check(t, is.Equal(got.code, exitBroke))
	assert.Check(t, is.Equal(got.stderr, ""))
	startsWith(t, got.stdout, outsideStart)
	endsWith(t, got.stdout, outsideEnd)
}

func TestApp_Run_WhenPushRunsOutsideARepositoryWithTheJSONFlag_RendersTheResultWithoutTheFlagInTheWords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "flag last", args: []string{"push", "--json"}},
		{name: "flag first", args: []string{"--json", "push"}},
		{name: "flag set to true", args: []string{"push", "--json=true"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, lost(t), tt.args...)

			assert.Check(t, is.Equal(got.code, exitBroke))
			assert.Check(t, is.Equal(got.stderr, ""))

			printed := resulttest.Decode(t, got.stdout)
			assert.Check(t, is.Equal(printed.Command, "push"))
			assert.Check(t, is.DeepEqual(printed.Words, []string{"push"}))
			assert.Check(t, is.Equal(printed.Outcome, "broke"))
		})
	}
}

func TestApp_Run_WhenRendererRejectsTheResult_PrintsTheErrorOnStderrOnly(t *testing.T) {
	t.Parallel()

	hooked := saying(result.Say(hookLine))

	tests := [][]string{{"push"}, {"push", "--json"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			app := outside(t)
			app.SetPush(hooked)

			got := launch(t, app, args...)

			assert.Check(t, is.Equal(got.code, exitBroke))
			assert.Check(t, is.Equal(got.stdout, ""))

			assert.Check(t, is.Contains(got.stderr, hookLine))
		})
	}
}

func TestApp_Run_WhenALineNamesAPathUnderTheGitDirOfTheRepository_ExitsOneWithNothingOnStdout(t *testing.T) {
	t.Parallel()

	tests := [][]string{{"push"}, {"push", "--json"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			f, under := stored(t)
			app := cli.App{Git: git.Runner{Dir: f.Dir(), Env: f.Env()}}
			app.SetPush(wrote(under))

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

	want := ran{stdout: "Wrote " + under + "\nNext: none\n", code: exitDone}
	assert.Check(t, is.DeepEqual(got, want, sameRun()))
}

// ---------------------------------------------------------------------------
// Deadline
// ---------------------------------------------------------------------------

func TestApp_Run_WhenPushRuns_GivesTheCommandADeadlineOneHundredSecondsAfterLaunch(t *testing.T) {
	t.Parallel()

	var (
		deadline time.Time
		bounded  bool
	)

	app := outside(t)
	app.Launch = time.Now().Add(-30 * time.Second)
	app.SetPush(func(ctx context.Context, _ landing.Deps, req landing.Request) result.Result {
		deadline, bounded = ctx.Deadline()

		return finished(req)
	})

	got := launch(t, app, "push")

	assert.Equal(t, got.code, exitDone, "stderr:\n%s", got.stderr)

	want := app.Launch.Add(bound)
	assert.Check(t, bounded, "the command's context has no deadline")
	assert.Check(t, deadline.Equal(want), "deadline of the command's context = %v, want %v", deadline, want)
}

func TestApp_Run_WhenTheBoundEndedBeforeAnyReadOfPush_RefusesBeforePushing(t *testing.T) {
	s := newScene(t, greenFirstPush())
	app := s.app()
	app.Launch = time.Now().Add(-2 * bound)

	got := launch(t, app, "push")

	assert.Check(t, is.Equal(got.code, exitRefused))
	startsWith(t, got.stdout, "Refused: the time limit ended ")

	assert.Check(t, is.Equal(s.fixture.OriginGit("for-each-ref", "refs/heads/"+topic), ""))
}

func TestApp_Run_WhenChecksStillRunAtTheBound_EndsPendingWithoutOutlastingIt(t *testing.T) {
	_, got := timed(t, pendingAtTheBound())

	want := "\nNext: ils push\n"
	endsWith(t, got.stdout, want)
}

func TestApp_Run_WhenACallIsStillOpenAtTheBound_EndsByTheBound(t *testing.T) {
	const cut = "Refused: the time limit ended while reading the repository's identity on GitHub; nothing was written\n"

	// createPR is the start of the call that opens a pull request.
	createPR := []string{"gh", "pr", "create"}

	tests := []struct {
		// wantStart and wantEnd are how stdout starts and ends.
		wantStart string
		wantEnd   string
		scenario  scenario
		// delivered says whether the branch is on origin at its tip and its pull request opened.
		delivered bool
	}{
		{scenario: heldChecks(), wantStart: "Pushed " + topic + " at ", wantEnd: "\nNext: ils push\n", delivered: true},
		{scenario: heldIdentity(), wantStart: cut, wantEnd: cut + "Next: ils push\n"},
	}
	for _, tt := range tests {
		t.Run(tt.scenario.name, func(t *testing.T) {
			s, got := timed(t, tt.scenario)

			startsWith(t, got.stdout, tt.wantStart)
			endsWith(t, got.stdout, tt.wantEnd)

			onOrigin := s.fixture.OriginGit("for-each-ref", "--points-at", tipOf(s.fixture), "refs/heads/"+topic)
			opening := slices.ContainsFunc(s.fake.Calls(), func(argv []string) bool {
				return slices.Equal(argv[:min(len(createPR), len(argv))], createPR)
			})
			atTip := onOrigin != ""
			assert.Check(t, is.Equal(atTip, tt.delivered))
			assert.Check(t, is.Equal(opening, tt.delivered))
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

// No test that builds a scene is parallel: the fake `gh` is found through `GH_PATH`, which
// `go-gh` reads from the environment of the whole process.

const (
	trunk     = "develop"
	topic     = "tidy-readme"
	spare     = "spare"
	otherBase = "release"
	host      = "github.com"
	owner     = "acme"
	widgets   = "widgets"
	slug      = host + "/" + owner + "/" + widgets
	originURL = "ssh://git@" + slug + ".git"
	otherURL  = "ssh://git@" + host + "/" + owner + "/gadgets.git"

	// strayHead is a head sha that no commit of the fixture has.
	strayHead = "0a1b2c3"

	// opened and merged are the numbers of the open and of the merged pull request.
	opened = 12
	merged = 9

	pullURL = "https://" + slug + "/pull/12"
	// runID is the workflow run that stands behind a check, and runLink is that check's link.
	runID   = "9001"
	runLink = "https://" + slug + "/actions/runs/" + runID + "/job/42"

	// shortLeft is less than one poll of the checks, so that a wait for running checks ends
	// as pending right after its first read.
	shortLeft = 4900 * time.Millisecond

	exitDone      = 0
	exitBroke     = 1
	exitRefused   = 2
	exitPending   = 4
	exitDelivered = 5
)

// scenario is one state of the repository and of GitHub that `ils push` is run in.
type scenario struct {
	// arrange changes the repository, which stands on the topic branch with one commit.
	arrange func(f *gittest.Fixture)
	// script returns what gh answers, read once arrange has run.
	script func(f *gittest.Fixture) []ghtest.Call
	name   string
	// left is the time left under the bound at launch; zero leaves the whole bound.
	left time.Duration
	want int
}

// scene is one repository and one scripted gh, with a record of what git was asked.
type scene struct {
	fixture  *gittest.Fixture
	recorder *git.Recorder
	fake     *ghtest.Fake
	left     time.Duration
}

// newScene returns a repository on the topic branch whose origin names acme/widgets on GitHub,
// once sc has arranged it, and a gh that answers from sc's script.
func newScene(t *testing.T, sc scenario) *scene {
	t.Helper()

	f := gittest.New(t, trunk)
	f.Commits(topic, "tidy the readme")
	f.SetOriginURL(originURL)

	if sc.arrange != nil {
		sc.arrange(f)
	}

	var script []ghtest.Call
	if sc.script != nil {
		script = sc.script(f)
	}

	return &scene{fixture: f, recorder: &git.Recorder{}, fake: ghtest.Setup(t, script...), left: sc.left}
}

// app returns the cli over the scene's repository, launched so that the scene's time is left.
func (s *scene) app() cli.App {
	// Origin's URL names GitHub over ssh, which the stand-in for ssh serves from the local origin.
	env := s.fixture.ServedEnv(s.fixture.OriginDir())
	app := cli.App{Git: git.Runner{Dir: s.fixture.Dir(), Env: env, Recorder: s.recorder}}

	if s.left > 0 {
		app.Launch = time.Now().Add(s.left - bound)
	}

	return app
}

func tipOf(f *gittest.Fixture) string {
	return f.Git("rev-parse", "HEAD")
}

// login scripts the login check with answer.
func login(answer ghtest.Answer) ghtest.Call {
	return ghtest.Exits(answer, ghtest.LoginArgs(host)...)
}

// identity scripts a logged-in gh and a repository that is a fork or not, with permission.
func identity(fork bool, permission string) []ghtest.Call {
	view := `{"isFork":` + strconv.FormatBool(fork) + `,"viewerPermission":"` + permission + `"}`

	return []ghtest.Call{
		login(ghtest.Answer{Stdout: "Logged in\n"}),
		ghtest.Says(view, ghtest.RepoViewArgs(slug)...),
	}
}

// allowed scripts a logged-in gh and a repository the user can push to.
func allowed() []ghtest.Call {
	return identity(false, "WRITE")
}

// listed scripts the answer to the list of the topic branch's pull requests in state.
func listed(state, body string) ghtest.Call {
	return ghtest.Exits(ghtest.Answer{Stdout: body}, ghtest.ListArgs(slug, topic, state)...)
}

// changeJSON is one pull request of the topic branch as `gh pr list` prints it.
func changeJSON(number int, base, sha string) string {
	n := strconv.Itoa(number)

	return `[{"number":` + n + `,"url":"https://` + slug + `/pull/` + n + `","title":"` + topic +
		`","baseRefName":"` + base + `","headRefOid":"` + sha + `","isDraft":false}]`
}

// found scripts the pull requests of the topic branch: the open ones, then the merged ones.
func found(open, landed string) []ghtest.Call {
	return append(allowed(), listed("open", open), listed("merged", landed))
}

// headJSON is `gh pr view`'s answer for the pull request at sha, with no review decision.
func headJSON(sha, mergeable, status string) string {
	return ghtest.HeadJSON(sha, mergeable, status, "")
}

// checkJSON is `gh pr checks`'s answer with one check of workflow run 9001.
func checkJSON(name, bucket string) string {
	return "[" + ghtest.CheckJSON(name, bucket, runLink) + "]"
}

// verdict is what GitHub says of pull request 12 once it is open.
type verdict struct {
	// rules is the answer to the read of the trunk's rules.
	rules string
	// head is the answer to `gh pr view`.
	head string
	// run is the status of the one workflow run on the commit.
	run string
	// checks is the answer to `gh pr checks`.
	checks ghtest.Answer
}

// passing is a mergeable pull request at sha whose one check passed, on a trunk with no rule.
func passing(sha string) verdict {
	return verdict{
		rules: "[]", head: headJSON(sha, "MERGEABLE", "CLEAN"), run: "completed",
		checks: ghtest.Answer{Stdout: checkJSON("build", "pass")},
	}
}

// calls scripts the reads of v for the commit sha.
func (v verdict) calls(sha string) []ghtest.Call {
	run := "[" + ghtest.RunJSON(v.run, "Tidy") + "]"

	return []ghtest.Call{
		ghtest.Says(v.rules, ghtest.RulesArgs(host, owner+"/"+widgets, trunk)...),
		ghtest.Says(v.head, ghtest.HeadArgs(slug, opened)...),
		ghtest.Exits(v.checks, ghtest.ChecksArgs(slug, opened)...),
		ghtest.Says(run, ghtest.RunsArgs(slug, sha)...),
		ghtest.Says("build\tRun tests\tFAIL\n", ghtest.LogArgs(slug, runID)...),
	}
}

// firstPush scripts a branch with no pull request yet, and then the verdict judge gives.
func firstPush(judge func(sha string) verdict) func(*gittest.Fixture) []ghtest.Call {
	return func(f *gittest.Fixture) []ghtest.Call {
		sha := tipOf(f)
		opening := ghtest.Says(pullURL+"\n", ghtest.CreateArgs(slug, topic, trunk, topic)...)

		return slices.Concat(found("[]", "[]"), []ghtest.Call{opening}, judge(sha).calls(sha))
	}
}

// laterPush scripts a branch whose pull request 12 is open at its tip, and then the verdict
// judge gives.
func laterPush(judge func(sha string) verdict) func(*gittest.Fixture) []ghtest.Call {
	return func(f *gittest.Fixture) []ghtest.Call {
		sha := tipOf(f)

		return slices.Concat(found(changeJSON(opened, trunk, sha), "[]"), judge(sha).calls(sha))
	}
}

// only scripts the same answers whatever the repository holds.
func only(calls ...ghtest.Call) func(*gittest.Fixture) []ghtest.Call {
	return func(*gittest.Fixture) []ghtest.Call { return calls }
}

func greenFirstPush() scenario {
	return scenario{name: "green on a first push", script: firstPush(passing), want: exitDone}
}

func greenSecondRun() scenario {
	return scenario{name: "second run on a pushed tip", arrange: func(f *gittest.Fixture) { f.Publish(topic) }, script: laterPush(passing), want: exitDone}
}

func redWithLog() scenario {
	red := func(sha string) verdict {
		v := passing(sha)
		v.head = headJSON(sha, "MERGEABLE", "UNSTABLE")
		v.checks = ghtest.Answer{Stdout: checkJSON("build", "fail"), Code: 1}

		return v
	}

	return scenario{name: "red with a failed log", script: firstPush(red), want: exitDelivered}
}

func blockedByMissingCheck() scenario {
	blocked := func(sha string) verdict {
		v := passing(sha)
		v.rules = `[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"build"},{"context":"test"}]}}]`
		v.head = headJSON(sha, "MERGEABLE", "BLOCKED")

		return v
	}

	return scenario{name: "blocked by a required check that never reported", script: firstPush(blocked), want: exitDelivered}
}

func conflicting() scenario {
	conflict := func(sha string) verdict {
		v := passing(sha)
		v.head = headJSON(sha, "CONFLICTING", "DIRTY")

		return v
	}

	return scenario{name: "conflicting with the trunk", script: firstPush(conflict), want: exitDelivered}
}

func pendingAtTheBound() scenario {
	running := func(sha string) verdict {
		v := passing(sha)
		v.run = "in_progress"
		v.checks = ghtest.Answer{Stdout: checkJSON("build", "pending"), Code: 8}

		return v
	}

	return scenario{
		name: "checks still running at the bound", script: laterPush(running), left: shortLeft, want: exitPending,
		arrange: func(f *gittest.Fixture) { f.Publish(topic) },
	}
}

func onTheTrunk() scenario {
	return scenario{
		name: "on the trunk", script: only(allowed()...), want: exitRefused,
		arrange: func(f *gittest.Fixture) { f.Git("switch", "--quiet", trunk) },
	}
}

func failedRead() scenario {
	down := listed("open", "")
	down.Answers = []ghtest.Answer{{Stderr: "gh: Server Error (HTTP 500)\n", Code: 1}}

	return scenario{name: "a read of GitHub fails", script: only(append(allowed(), down)...), want: exitBroke}
}

// standingRefusals are the refusals decided before the branch's commits are read.
func standingRefusals() []scenario {
	loggedOut := login(ghtest.Answer{Stderr: "You are not logged into any GitHub hosts.\n", Code: 1})

	return []scenario{
		{name: "no origin", arrange: (*gittest.Fixture).RemoveOrigin, want: exitRefused},
		{
			name: "origin's two addresses differ", want: exitRefused,
			arrange: func(f *gittest.Fixture) { f.SetPushURL(otherURL) },
		},
		{name: "not logged in", script: only(loggedOut), want: exitRefused},
		{name: "a fork", script: only(identity(true, "WRITE")...), want: exitRefused},
		{name: "no right to push", script: only(identity(false, "READ")...), want: exitRefused},
		onTheTrunk(),
		{name: "detached", arrange: (*gittest.Fixture).Detach, script: only(allowed()...), want: exitRefused},
		{
			name: "tracked file changed", script: only(allowed()...), want: exitRefused,
			arrange: func(f *gittest.Fixture) { f.DirtyFile() },
		},
	}
}

// commitRefusals are the refusals decided from the branch's commits and origin's copy of it.
func commitRefusals() []scenario {
	refused := func(name string, arrange func(f *gittest.Fixture)) scenario {
		return scenario{name: name, arrange: arrange, script: only(allowed()...), want: exitRefused}
	}

	return []scenario{
		refused("no commit past the trunk", func(f *gittest.Fixture) { f.Git("switch", "--quiet", "--create", spare, trunk) }),
		refused("no commit at all", func(f *gittest.Fixture) { f.Git("switch", "--quiet", "--orphan", spare) }),
		refused("cut from another branch", func(f *gittest.Fixture) { f.Sibling(spare, topic) }),
		refused("behind origin's copy", func(f *gittest.Fixture) {
			f.Publish(topic)
			f.AdvanceOrigin(topic)
		}),
		refused("diverged from origin's copy", func(f *gittest.Fixture) {
			f.Publish(topic)
			f.Diverge(topic)
		}),
	}
}

// changeRefusals are the refusals decided from the pull requests GitHub finds for the branch.
func changeRefusals() []scenario {
	landed := only(found("[]", changeJSON(merged, trunk, strayHead))...)

	return []scenario{
		{
			name: "open pull request against another base", want: exitRefused,
			script: only(found(changeJSON(opened, otherBase, strayHead), "[]")...),
		},
		{
			name: "merged pull request with nothing unlanded", script: landed, want: exitRefused,
			arrange: func(f *gittest.Fixture) { f.LandPull(merged, topic) },
		},
		{
			name: "merged pull request with commits made since", script: landed, want: exitRefused,
			arrange: func(f *gittest.Fixture) {
				f.LandPull(merged, topic)
				f.Commits(topic, "fix a typo")
			},
		},
	}
}

// scenarios are the runs of `ils push`, one for each way it ends.
func scenarios() []scenario {
	ends := []scenario{
		greenFirstPush(), greenSecondRun(), redWithLog(), blockedByMissingCheck(), conflicting(),
		pendingAtTheBound(), failedRead(),
	}

	return slices.Concat(ends, standingRefusals(), commitRefusals(), changeRefusals())
}
