package cli_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	outsideEnd      = "Nothing was written.\nNext: fix what failed, then run ils push again\n"
	outsideMergeEnd = "Nothing was written.\nNext: fix what failed, then run ils merge again\n"

	bound = 100 * time.Second
	// grace is how long a run may take to come back after the moment it is due: past the bound for
	// timed, after the held call starts for cutWhileHeld.
	grace = 2 * time.Second
	// held is how long a held call stays open, far past the moment the run is cut.
	held = 30 * time.Second
)

// ran is what one run of the cli printed and the code it exited with.
type ran struct {
	stdout string
	stderr string
	code   int
}

// launch runs app with args, launched now unless app says when, and returns what it printed; a
// stdout app already has is left in place, and nothing is returned for it.
func launch(t *testing.T, app cli.App, args ...string) ran {
	t.Helper()

	return launchUnder(t.Context(), t, app, args...)
}

// launchUnder is launch with the run bound to ctx rather than t.Context(), so a caller can end it
// early.
func launchUnder(ctx context.Context, t *testing.T, app cli.App, args ...string) ran {
	t.Helper()

	var stdout, stderr bytes.Buffer

	app.Stderr = &stderr
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

// cutWhileHeld runs `ils push` in a scene of sc under a context that ends with
// [context.DeadlineExceeded] the moment the fake gh starts holding an answer, and fails the test
// unless it exits as sc wants and comes back no later than grace after that moment.
func cutWhileHeld(t *testing.T, sc scenario) (*scene, ran) {
	t.Helper()

	s := newScene(t, sc)
	cut := make(chan struct{})
	cutAt := make(chan time.Time, 1)

	ctx, cancel := context.WithCancel(t.Context())

	var wg sync.WaitGroup

	wg.Go(func() {
		select {
		case <-s.fake.Holding():
			cutAt <- time.Now()
			close(cut)
		case <-ctx.Done():
		}
	})

	got := launchUnder(gittest.Ending(t.Context(), cut, context.DeadlineExceeded), t, s.app(), "push")

	cancel()
	wg.Wait()

	assert.Equal(t, got.code, sc.want, "stdout:\n%s\nstderr:\n%s", got.stdout, got.stderr)

	select {
	case at := <-cutAt:
		late := time.Since(at)
		assert.Check(t, late <= grace, "ils push returned %s after the held call started, want it back no later than %s after", late, grace)
	default:
		t.Error("ils push returned without the fake gh holding an answer")
	}

	return s, got
}

// heldChecks is a first push whose read of the checks is held open.
func heldChecks() scenario {
	slow := func(sha string) verdict {
		v := passing(sha)
		v.checks.Delay = held

		return v
	}

	return scenario{name: "the read of the checks", script: firstPush(slow), want: exitPending}
}

// heldIdentity is a push whose read of the repository's identity is held open.
func heldIdentity() scenario {
	calls := allowed()
	calls[1].Answers[0].Delay = held

	return scenario{name: "the read of the identity", script: only(calls...), want: exitRefused}
}

// heldVersion is a push whose read of gh's version is held open.
func heldVersion() scenario {
	slow := ghtest.Exits(ghtest.Answer{Stdout: "gh version 99.0.0\n", Delay: held}, ghtest.VersionArgs()...)

	return scenario{name: "the read of gh's version", script: only(slow), want: exitRefused}
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
		{name: "version flag set to neither", args: []string{"--version=maybe"}, reason: "--version takes true or false, and maybe was given"},
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

const (
	// rootHelp, pushHelp and mergeHelp are how the help of ils, of push and of merge each start.
	rootHelp  = "ils lands a branch on GitHub in fixed, checked steps"
	pushHelp  = "Push the current branch, open its pull request on the first push"
	mergeHelp = "Squash-merge the pull request of the current branch once its checks are green"

	// mergeSummary is the line that `ils --help` lists merge with.
	mergeSummary = "merge Squash and merge the pull request once its checks are green"
	// mergeUsage is the usage and the examples the help of merge ends with, before its flags.
	mergeUsage = "USAGE ils merge [--flags] EXAMPLES # Merge once the checks are green ils merge " +
		"# The same result as one JSON object ils merge --json"

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

// entries returns the first word of each line the help lists under heading.
func entries(help, heading string) []string {
	var found []string

	_, rest, _ := strings.Cut(help, heading)
	for line := range strings.SplitSeq(strings.TrimLeft(rest, " \n"), "\n") {
		words := strings.Fields(line)
		if len(words) == 0 {
			break
		}

		found = append(found, words[0])
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

	own := []string{"-h", "--json"}
	tests := []struct {
		args     []string
		commands []string
		flags    []string
	}{
		{args: []string{"--help"}, commands: []string{"help", "merge", "push"}, flags: []string{"-h", "--json", "--version"}},
		{args: []string{"push", "--help"}, flags: own},
		{args: []string{"merge", "--help"}, flags: own},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			assert.Check(t, is.DeepEqual(entries(got.stdout, "COMMANDS"), tt.commands))
			assert.Check(t, is.DeepEqual(entries(got.stdout, "FLAGS"), tt.flags))
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

	tests := [][]string{{"--version"}, {"--json", "--version"}, {"--version", "--json"}}
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
	s := newScene(t, scenario{name: "no pull request", script: only(found("[]", "[]")...), want: exitRefused})

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

	want := ran{stdout: "Wrote " + under + "\nNext: none\n", code: exitDone}
	assert.Check(t, is.DeepEqual(got, want, sameRun()))
}

// ---------------------------------------------------------------------------
// Deadline
// ---------------------------------------------------------------------------

func TestApp_Run_WhenTheBoundEndedBeforeAnyReadOfPush_RefusesBeforePushing(t *testing.T) {
	s := newScene(t, greenFirstPush())
	app := s.app()
	app.Launch = time.Now().Add(-2 * bound)

	got := launch(t, app, "push")

	assert.Check(t, is.Equal(got.code, exitRefused))
	startsWith(t, got.stdout, "Refused: the time limit ended ")

	nothingPushed(t, s)
}

func TestApp_Run_WhenChecksStillRunAtTheBound_EndsPendingWithoutOutlastingIt(t *testing.T) {
	_, got := timed(t, pendingAtTheBound())

	want := "\nNext: ils push\n"
	endsWith(t, got.stdout, want)
}

func TestApp_Run_WhenTheTimeLimitEndsDuringAHeldReadBeforeThePush_RefusesWithinGraceWithNothingPushed(t *testing.T) {
	const want = "Refused: the time limit ended while reading the repository's identity on GitHub; nothing was written\n" +
		"Next: ils push\n"

	for _, sc := range []scenario{heldIdentity(), heldVersion()} {
		t.Run(sc.name, func(t *testing.T) {
			s, got := cutWhileHeld(t, sc)

			assert.Check(t, is.Equal(got.stdout, want))
			assert.Check(t, is.Equal(got.stderr, ""))
			nothingPushed(t, s)
			assert.Check(t, !opensPullRequest(s), "gh was asked to create a pull request")
		})
	}
}

func TestApp_Run_WhenTheTimeLimitEndsDuringTheHeldReadOfTheChecks_EndsPendingWithinGraceWithThePullRequestOpen(t *testing.T) {
	s, got := cutWhileHeld(t, heldChecks())

	startsWith(t, got.stdout, "Pushed "+topic+" at ")
	assert.Check(t, is.Contains(got.stdout, pullURL))
	endsWith(t, got.stdout, "\nNext: ils push\n")
	assert.Check(t, is.Equal(got.stderr, ""))
	onOrigin := s.fixture.OriginGit("for-each-ref", "--points-at", tipOf(s.fixture), "refs/heads/"+topic)
	assert.Check(t, onOrigin != "", "%s is not on origin at its tip", topic)
	assert.Check(t, opensPullRequest(s), "gh was not asked to create a pull request")
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

// No test that builds a scene is parallel: the fake `gh` is found through `GH_PATH`, which
// `go-gh` reads from the environment of the whole process.

const (
	trunk     = "develop"
	topic     = "tidy-readme"
	host      = "github.com"
	owner     = "acme"
	widgets   = "widgets"
	slug      = host + "/" + owner + "/" + widgets
	originURL = "ssh://git@" + slug + ".git"

	// opened is the number of the open pull request.
	opened = 12

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

// scenario is one state of the repository and of GitHub that a landing command is run in.
type scenario struct {
	// arrange changes the repository, which stands on the topic branch with one commit.
	arrange func(f *gittest.Fixture)
	// script returns what gh answers, read once arrange has run.
	script func(f *gittest.Fixture) []ghtest.Call
	name   string
	// ssh is the ssh config ils reads; empty names no host.
	ssh string
	// left is the time left under the bound at launch; zero leaves the whole bound.
	left time.Duration
	want int
}

// scene is one repository and one scripted gh, with a record of what git was asked.
type scene struct {
	fixture  *gittest.Fixture
	recorder *git.Recorder
	fake     *ghtest.Fake
	// env reaches origin, whose URL names GitHub over ssh, through the stand-in for ssh.
	env  []string
	left time.Duration
}

// newScene returns a repository on the topic branch whose origin names acme/widgets on GitHub,
// once sc has arranged it, and a gh that answers from sc's script.
func newScene(t *testing.T, sc scenario) *scene {
	t.Helper()

	f, env := gittest.NewServed(t, trunk, topic, originURL)
	f.SetSSHConfig(sc.ssh)

	if sc.arrange != nil {
		sc.arrange(f)
	}

	var script []ghtest.Call
	if sc.script != nil {
		script = sc.script(f)
	}

	return &scene{
		fixture:  f,
		recorder: &git.Recorder{},
		fake:     ghtest.Setup(t, script...),
		env:      env,
		left:     sc.left,
	}
}

// app returns the cli over the scene's repository, launched so that the scene's time is left.
func (s *scene) app() cli.App {
	app := over(git.Runner{Dir: s.fixture.Dir(), Env: s.env, Recorder: s.recorder})

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
	return []ghtest.Call{
		login(ghtest.Answer{Stdout: "Logged in\n"}),
		ghtest.Says(ghtest.RepoViewJSON(fork, permission), ghtest.RepoViewArgs(slug)...),
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
	url := "https://" + slug + "/pull/" + strconv.Itoa(number)

	return "[" + ghtest.ChangeJSON(ghtest.Change{Number: number, URL: url, Title: topic, Base: base, SHA: sha}) + "]"
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

func redWithLog() scenario {
	red := func(sha string) verdict {
		v := passing(sha)
		v.head = headJSON(sha, "MERGEABLE", "UNSTABLE")
		v.checks = ghtest.Answer{Stdout: checkJSON("build", "fail"), Code: 1}

		return v
	}

	return scenario{name: "red with a failed log", script: firstPush(red), want: exitDelivered}
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
