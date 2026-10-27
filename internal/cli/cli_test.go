package cli_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// ---------------------------------------------------------------------------
// Run helpers
// ---------------------------------------------------------------------------

const (
	bound = cli.Bound

	exitDone      = 0
	exitBroke     = 1
	exitRefused   = 2
	exitPending   = 4
	exitDelivered = 5
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

// finished is a landing command that ended as done with nothing to say.
func finished(req landing.Request) result.Result {
	return result.Result{Command: req.Command, Words: req.Words, Outcome: result.Done, Next: result.NextNone()}
}

// finishing is a landing command that ends as done with nothing to say.
func finishing(_ context.Context, _ landing.Deps, req landing.Request) result.Result {
	return finished(req)
}

// saying is a landing command that ended as done with line as its one line.
func saying(line result.Phrase) cli.Command {
	return func(_ context.Context, _ landing.Deps, req landing.Request) result.Result {
		r := finished(req)
		r.Lines = []result.Phrase{line}

		return r
	}
}

// plain checks, without stopping the test, that text carries no escape sequence; what names the
// text in the failure message.
func plain(t *testing.T, what, text string) {
	t.Helper()

	assert.Check(t, !strings.Contains(text, escape), "%s carries an escape sequence:\n%q", what, text)
}

const (
	escape = "\x1b"

	// forceColor is the variable that makes Fang style help on a stdout that is no terminal.
	forceColor = "CLICOLOR_FORCE"
)

// errReaderGone is the error a stdout that cannot be written fails with.
var errReaderGone = errors.New("the reader went away")

// shut is a stdout whose every write fails with err.
type shut struct {
	err error
}

// Write fails with err and writes nothing.
func (s shut) Write([]byte) (int, error) {
	return 0, s.err
}

func TestApp_Run_WhenAskedForTheVersion_PrintsTheVersionOfIls(t *testing.T) {
	t.Parallel()

	app := outside(t)
	app.Version = "0.2.0"
	app.SetBuildInfo(built("v9.9.9"), true)

	got := launch(t, app, "--version")

	assert.Check(t, is.Equal(got.code, exitDone))
	assert.Check(t, is.Equal(got.stderr, ""))
	assert.Check(t, is.Contains(got.stdout, "0.2.0"))
	assert.Check(t, !strings.Contains(got.stdout, "9.9.9"), "stdout = %q, want the set version over the build info's", got.stdout)
}

func TestApp_Run_WhenVersionIsEmpty_PrintsTheVersionTheBuildInfoReaderReports(t *testing.T) {
	t.Parallel()

	app := outside(t)
	app.SetBuildInfo(built("v0.2.0"), true)

	got := launch(t, app, "--version")

	assert.Check(t, is.Equal(got.code, exitDone))
	assert.Check(t, is.Equal(got.stderr, ""))
	assert.Check(t, is.Contains(got.stdout, "0.2.0"))
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
		{name: "build info not readable", want: "(devel)"},
		{name: "empty version", info: built(""), ok: true, want: "(devel)"},
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

// lost returns the cli in a directory that is no repository, with git's own words in English.
func lost(t *testing.T) cli.App {
	t.Helper()

	return over(git.Runner{Dir: t.TempDir(), Env: append(gittest.Env(t), "LC_ALL=C")})
}

// refusal is the grammar refusal of the words typed as the JSON renderer shows it, all but its
// reason and next step.
func refusal(typed ...string) resulttest.Shown {
	command := ""
	if len(typed) > 0 {
		command = typed[0]
	}

	return resulttest.Shown{Command: command, Words: typed, Outcome: "refused", ExitCode: exitRefused}
}

// worded returns the phrase say builds from text as its own wording, for wording only a run
// knows, such as a path under a temporary directory.
func worded[W ~string](say func(W, ...any) result.Phrase, text string) result.Phrase {
	return say(W(text))
}

// wrote is a landing command that ended as done with one line, whose own wording names path.
func wrote(path string) cli.Command {
	return saying(worded(result.Say, "Wrote "+path))
}

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

	tests := [][]string{{"push"}, {"--version"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			app := outside(t)
			app.Stdout = shut{err: errReaderGone}
			app.SetPush(finishing)

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
		{name: "flag set to neither, then set", args: []string{"push", "--json=maybe", "--json"}, want: refusal("push", "--json=maybe")},
		{name: "flag set to true", args: []string{"--json=true", "pish"}, want: refusal("pish")},
		{name: "debug flag set to neither", args: []string{"--json", "--debug=maybe"}, want: refusal()},
		{name: "debug and color flags", args: []string{"-d", "pish", "--no-color", "--json"}, want: refusal("pish")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			assert.Check(t, is.Equal(got.code, exitRefused))
			assert.Check(t, is.Equal(got.stderr, ""))

			printed := resulttest.Decode(t, got.stdout)
			assert.Check(t, is.DeepEqual(printed, tt.want, cmpopts.EquateEmpty(), cmpopts.IgnoreFields(resulttest.Shown{}, "Reason", "Next")))
		})
	}
}

func TestApp_Run_WhenTheJSONFlagIsSetToFalse_RendersTheResultAsText(t *testing.T) {
	t.Parallel()

	got := launch(t, lost(t), "push", "--json=false")

	assert.Check(t, is.Equal(got.code, exitBroke))
	assert.Check(t, is.Equal(got.stderr, ""))
	assert.Check(t, got.stdout != "" && !strings.HasPrefix(got.stdout, "{"), "stdout = %q, want the result as text", got.stdout)
}

func TestApp_Run_WhenALandingCommandRunsOutsideARepositoryWithTheJSONFlag_RendersTheResultWithoutTheFlagInTheWords(t *testing.T) {
	t.Parallel()

	got := launch(t, lost(t), "push", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke))
	assert.Check(t, is.Equal(got.stderr, ""))

	assert.Check(t, is.DeepEqual(resulttest.Decode(t, got.stdout).Words, []string{"push"}))
}

func TestApp_Run_WhenALineNamesAPathUnderTheGitDirOfTheRepository_ExitsOneWithNothingOnStdout(t *testing.T) {
	t.Parallel()

	tests := [][]string{{"push"}, {"push", "--json"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			f, under := stored(t)
			app := over(git.Runner{Dir: f.Dir(), Env: f.Env()})
			app.SetPush(wrote(under))

			got := launch(t, app, args...)

			assert.Check(t, is.Equal(got.code, exitBroke))
			assert.Check(t, is.Equal(got.stdout, ""))
		})
	}
}

// ---------------------------------------------------------------------------
// Color
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

// ---------------------------------------------------------------------------
// Pending hint
// ---------------------------------------------------------------------------

const hint = cli.PendingHint

// pending is a landing command that ended pending, with ils push as its next step.
func pending(_ context.Context, _ landing.Deps, req landing.Request) result.Result {
	r := finished(req)
	r.Outcome = result.Pending
	r.Reason = result.Say("the checks of %s are still running", result.PullRequest(opened))
	r.Next = result.NextCommand("push")

	return r
}

func TestApp_Run_WhenTheResultIsPendingOnATerminalStdout_EndsNextWithAHintDimOnlyInColor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		env   []string
		color bool
	}{
		{name: "color terminal", env: []string{colorTerm}, color: true},
		{name: "no color", env: []string{colorTerm, noColor}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := painted(t, tt.env, true, false)
			app.SetPush(pending)

			got := launch(t, app, "push")

			assert.Check(t, is.Equal(got.code, exitPending))
			drawn := resulttest.Draw(t, got.stdout)
			lines := strings.Split(strings.TrimSuffix(drawn.String(), "\n"), "\n")
			last := lines[len(lines)-1]
			assert.Check(t, strings.Contains(last, "ils push"), "Next line = %q", last)
			assert.Check(t, strings.HasSuffix(last, " "+hint), "Next line = %q", last)
			assert.Check(t, is.Equal(drawn.LookOf(t, last, hint).Dim, tt.color), "Next line = %q", last)
			assert.Check(t, is.Equal(strings.Contains(got.stdout, escape), tt.color), "stdout = %q", got.stdout)
		})
	}
}

func TestApp_Run_WhenTheResultIsPendingOffATerminalStdoutOrAsJSON_PrintsNoHint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      []string
		args     []string
		terminal bool
	}{
		{name: "no terminal", env: []string{colorTerm}, args: []string{"push"}},
		{name: "dumb terminal", env: []string{"TERM=dumb"}, args: []string{"push"}, terminal: true},
		{name: "JSON on a terminal", env: []string{colorTerm}, args: []string{"push", "--json"}, terminal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := painted(t, tt.env, tt.terminal, false)
			app.SetPush(pending)

			got := launch(t, app, tt.args...)

			assert.Check(t, is.Equal(got.code, exitPending))
			assert.Check(t, !strings.Contains(got.stdout, hint), "stdout = %q", got.stdout)
		})
	}
}

func TestApp_Run_WhenOnlyTheProcessEnvironmentForcesColor_WritesNoEscapeSequence(t *testing.T) {
	t.Setenv(forceColor, "1")

	got := launch(t, outside(t), "pish")

	assert.Check(t, is.Equal(got.code, exitRefused))
	plain(t, "stdout", got.stdout)
}

func TestApp_Run_WhenNoStreamHasAColorProfile_WritesNoEscapeSequence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      []string
		args     []string
		terminal bool
		logs     bool
	}{
		{name: "terminal forced off a terminal", env: []string{colorTerm, "TTY_FORCE=1"}, args: []string{"push", "-d"}, logs: true},
		{name: "no color over forced color", env: []string{colorTerm, noColor, forced}, args: []string{"push", "-d"}, logs: true},
		{name: "flag over forced color", env: []string{colorTerm, forced}, terminal: true, args: []string{"push", "-d", "--no-color"}, logs: true},
		{name: "flag after an unknown flag", env: []string{colorTerm}, terminal: true, args: []string{"push", "--bogus", "--no-color"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, painted(t, tt.env, tt.terminal, tt.terminal), tt.args...)

			assert.Check(t, is.Equal(got.stderr != "", tt.logs), "ils %s wrote stderr = %q", strings.Join(tt.args, " "), got.stderr)
			plain(t, "stdout", got.stdout)
			plain(t, "stderr", got.stderr)
		})
	}
}

func TestApp_Run_WhenStdoutHasAColorProfile_StylesTheResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      []string
		args     []string
		terminal bool
	}{
		{name: "color terminal", env: []string{colorTerm}, terminal: true, args: []string{"push"}},
		{name: "flag set to false", env: []string{colorTerm}, terminal: true, args: []string{"merge", "--no-color=false"}},
		{name: "unknown word", env: []string{colorTerm}, terminal: true, args: []string{"pish"}},
		{name: "color flag set to neither", env: []string{forced}, args: []string{"merge", "--no-color=maybe"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, painted(t, tt.env, tt.terminal, false), tt.args...)

			assert.Check(t, strings.Contains(got.stdout, escape), "stdout = %q, want it styled", got.stdout)
		})
	}
}

func TestApp_Run_WhenOneStreamIsAColorTerminal_LeavesTheOtherStreamPlain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		// other picks the stream that is no terminal out of a run.
		other  func(ran) string
		name   string
		stdout bool
	}{
		{name: "stdout", stdout: true, other: func(r ran) string { return r.stderr }},
		{name: "stderr", other: func(r ran) string { return r.stdout }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, painted(t, []string{colorTerm}, tt.stdout, !tt.stdout), "push", "-d")

			other := tt.other(got)
			assert.Check(t, other != "", "the stream that is no terminal is empty")
			plain(t, "the stream that is no terminal", other)
		})
	}
}

// ---------------------------------------------------------------------------
// Bound
// ---------------------------------------------------------------------------

// held is how long a held call stays open, far past the moment the run is cut.
const held = 30 * time.Second

// endWhileHeld runs app with args in s under a context that ends with cause the moment the fake gh
// of s starts holding an answer, and fails the test unless it comes back well before the held
// answer would have.
func (s *scene) endWhileHeld(t *testing.T, app cli.App, cause error, args ...string) ran {
	t.Helper()

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

	got := launchUnder(gittest.Ending(t.Context(), cut, cause), t, app, args...)

	cancel()
	wg.Wait()

	select {
	case at := <-cutAt:
		late := time.Since(at)
		assert.Check(t, late < held/2, "ils %v returned %s after the held call started, want it back well before the %s hold ends", args, late, held)
	default:
		t.Errorf("ils %v returned without the fake gh holding an answer", args)
	}

	return got
}

func heldIdentity() scenario {
	calls := allowed()
	calls[1].Answer.Delay = held

	return scenario{script: only(calls...)}
}

func TestApp_Run_WhenMergeRuns_EndsItAtTheBoundFromLaunch(t *testing.T) {
	t.Parallel()

	var deadline time.Time

	app := outside(t)
	app.Launch = time.Now()
	app.SetMerge(func(ctx context.Context, _ landing.Deps, req landing.Request) result.Result {
		deadline, _ = ctx.Deadline()

		return finished(req)
	})

	got := launch(t, app, "merge")

	assert.Check(t, is.Equal(got.code, exitDone))
	assert.Check(t, deadline.Equal(app.Launch.Add(bound)), "deadline = %s, want %s", deadline, app.Launch.Add(bound))
}

func TestApp_Run_WhenAnInterruptEndsAHeldRead_RefusesWithTheDisplayEnded(t *testing.T) {
	tests := []struct {
		name string
		next string
		args []string
	}{
		{name: "push", args: []string{"push"}, next: "ils push"},
		{name: "merge", args: []string{"merge"}, next: "ils merge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScene(t, heldIdentity())
			app, stderr := onStderrScreenOf(t, s.app(), stderrScreen{env: []string{colorTerm}, width: wide})

			got := s.endWhileHeld(t, app, context.Canceled, tt.args...)

			assert.Check(t, is.Equal(got.code, exitRefused))
			assert.Check(t, is.Contains(got.stdout, tt.next))
			assert.Check(t, stderr.raw() != "", "the run drew no progress display on stderr")
			assert.Check(t, is.Len(stderr.lines(), 0), "stderr still shows %q", stderr.lines())
			assert.Check(t, !stderr.cursorHidden(), "the cursor is still hidden:\n%q", stderr.raw())
		})
	}
}

func TestApp_Run_WhenHelpIsAskedForWithColorOffOnAColorTerminal_PrintsCobrasOwnHelp(t *testing.T) {
	t.Parallel()

	got := launch(t, painted(t, []string{colorTerm, noColor}, true, true), "--help")

	assert.Check(t, is.Equal(got.code, exitDone))
	assert.Check(t, is.Contains(got.stdout, "Usage:"))
}

func TestApp_Run_WhenHelpIsAskedForOnAColorTerminal_StylesIt(t *testing.T) {
	// Fang styles help only when the process environment lets it; ils's profile then decides.
	t.Setenv(forceColor, "1")

	got := launch(t, painted(t, []string{colorTerm}, true, false), "--help")

	assert.Check(t, is.Equal(got.code, exitDone))
	assert.Check(t, strings.Contains(got.stdout, escape), "the help carries no escape sequence:\n%q", got.stdout)
}
