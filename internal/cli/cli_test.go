package cli_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/google/go-cmp/cmp"
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
	nextHelp = "ils --help"

	// versionNeither, jsonNeither, debugNeither and colorNeither are the reasons a version, JSON,
	// debug and no-color flag set to maybe are refused for.
	versionNeither = "--version takes true or false, and maybe was given"
	jsonNeither    = "--json takes true or false, and maybe was given"
	debugNeither   = "--debug takes true or false, and maybe was given"
	colorNeither   = "--no-color takes true or false, and maybe was given"

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

const (
	escape = "\x1b"

	// forceColor is the variable that makes Fang style help on a stdout that is no terminal.
	forceColor = "CLICOLOR_FORCE"
)

// errReaderGone is the error a stdout that cannot be written fails with.
var errReaderGone = errors.New("the reader went away")

// sameRun lets cmp compare the unexported fields of ran.
func sameRun() cmp.Option {
	return cmp.AllowUnexported(ran{})
}

// anyOrder lets cmp compare two slices of strings whatever the order of their elements.
func anyOrder() cmp.Option {
	return cmpopts.SortSlices(func(a, b string) bool { return a < b })
}

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

// lost returns the cli in a directory that is no repository, with git's own words in English.
func lost(t *testing.T) cli.App {
	t.Helper()

	return over(git.Runner{Dir: t.TempDir(), Env: append(gittest.Env(t), "LC_ALL=C")})
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

func TestApp_Run_WhenADebugLineCannotBeWritten_PrintsWhatTheSameRunPrintsWithoutDebug(t *testing.T) {
	t.Parallel()

	tests := [][]string{{"push"}, {"merge", "--json"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			app := lost(t)
			want := launch(t, app, args...)
			app.Stderr = shut{err: errReaderGone}

			got := launch(t, app, append(args, "-d")...)

			assert.Check(t, is.Equal(got.stdout, want.stdout))
			assert.Check(t, is.Equal(got.code, want.code))
		})
	}
}

func TestApp_Run_WhenDebugIsOnDuringALanding_PrintsWhatTheSameRunPrintsWithoutDebug(t *testing.T) {
	tests := []struct {
		sc   func() scenario
		args []string
	}{
		{sc: greenFirstPush, args: []string{"push"}},
		{sc: greenFirstPush, args: []string{"push", "--json"}},
		{sc: noPullRequest, args: []string{"merge"}},
		{sc: noPullRequest, args: []string{"merge", "--json"}},
		{sc: landedMerge, args: []string{"merge"}},
		{sc: landedMerge, args: []string{"merge", "--json"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			want := launch(t, newScene(t, tt.sc()).app(), tt.args...)

			got := launch(t, newScene(t, tt.sc()).app(), append(tt.args, "-d")...)

			assert.Check(t, is.Equal(got.code, tt.sc().want))
			assert.Check(t, is.Equal(got.stdout, want.stdout))
			assert.Check(t, is.Equal(got.code, want.code))
			assert.Check(t, got.stderr != "", "ils %s -d wrote no debug line", strings.Join(tt.args, " "))
		})
	}
}

func TestApp_Run_WhenDebugIsSetToFalse_PrintsWhatTheSameRunPrintsWithoutIt(t *testing.T) {
	t.Parallel()

	app := lost(t)
	want := launch(t, app, "push", "--json")

	got := launch(t, app, "push", "--json", "--debug=false")

	assert.Check(t, is.DeepEqual(got, want, sameRun()))
	assert.Check(t, is.Equal(got.stderr, ""))
}

func TestApp_Run_WhenEveryDebugLineOfALandingCannotBeWritten_PrintsWhatTheSameRunPrintsWithoutDebug(t *testing.T) {
	want := launch(t, newScene(t, greenFirstPush()).app(), "push")
	app := newScene(t, greenFirstPush()).app()
	app.Stderr = shut{err: errReaderGone}

	got := launch(t, app, "push", "-d")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Equal(got.stdout, want.stdout))
	assert.Check(t, is.Equal(got.code, want.code))
}

var (
	// execLine is a debug line of one git or gh call that exited: its time since launch, the call,
	// its exit code and its duration.
	execLine = regexp.MustCompile(`^\+\d+\.\d{3}s ((?:git|gh) .+) exit \d+ (\S+)$`)
	// waitLine is a debug line of one decision of a wait.
	waitLine = regexp.MustCompile(`^\+\d+\.\d{3}s wait \S+=\S+( \S+=\S+)*$`)
)

func TestApp_Run_WhenDebugIsOn_WritesALineForEachGitAndGhCallAndWaitDecision(t *testing.T) {
	s := newScene(t, greenFirstPush())

	got := launch(t, s.app(), "push", "-d")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	var called, waited []string
	for line := range strings.Lines(got.stderr) {
		line = strings.TrimSuffix(line, "\n")
		if m := execLine.FindStringSubmatch(line); m != nil {
			_, err := time.ParseDuration(m[2])
			assert.Check(t, err, "duration of %q", line)
			called = append(called, m[1])

			continue
		}
		assert.Check(t, waitLine.MatchString(line), "line %q is neither a call nor a wait decision", line)
		waited = append(waited, line[strings.Index(line, " wait ")+1:])
	}
	var want []string
	for _, argv := range slices.Concat(s.recorder.Calls(), s.fake.Calls()) {
		want = append(want, strings.Join(argv, " "))
	}
	assert.Check(t, is.DeepEqual(called, want, anyOrder()))
	assert.Check(t, is.Contains(waited, "wait ended=green"))
}

// debugRecord returns a debug record of message and attrs made at the moment at.
func debugRecord(at time.Time, message string, attrs ...slog.Attr) slog.Record {
	record := slog.NewRecord(at, slog.LevelDebug, message, 0)
	record.AddAttrs(attrs...)

	return record
}

func TestApp_Run_WhenDebugIsOn_DrawsEachRecordTimedFromLaunchWithTimesDim(t *testing.T) {
	t.Parallel()

	app := painted(t, []string{colorTerm}, false, true)
	app.Launch = time.Now()
	app.SetPush(func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result {
		if deps.Logger == nil {
			t.Error("push ran with no logger")

			return finished(req)
		}
		for _, record := range []slog.Record{
			debugRecord(app.Launch.Add(1500*time.Millisecond), "wait",
				slog.String("read", "merge-state"), slog.String("state", "unknown"), slog.Duration("window", 30*time.Second)),
			debugRecord(app.Launch.Add(2250*time.Millisecond), "exec",
				slog.String("tool", "gh"), slog.Any("args", []string{"pr", "view", "12"}), slog.Int64("exit", 0),
				slog.Duration("duration", 1234*time.Millisecond)),
			debugRecord(app.Launch.Add(3*time.Second), "exec",
				slog.String("tool", "gh"), slog.Any("args", []string{"pr", "checks", "12"}), slog.String("error", "signal: killed"),
				slog.Duration("duration", 30*time.Millisecond)),
		} {
			assert.Check(t, deps.Logger.Handler().Handle(ctx, record))
		}

		return finished(req)
	})

	got := launch(t, app, "push", "-d")

	drawn := resulttest.Draw(t, got.stderr)
	lines := strings.Split(strings.TrimSuffix(drawn.String(), "\n"), "\n")
	want := []string{
		"+1.500s wait read=merge-state state=unknown window=30s",
		"+2.250s gh pr view 12 exit 0 1.234s",
		"+3.000s gh pr checks 12 signal: killed 30ms",
	}
	assert.Assert(t, len(lines) > len(want), "stderr = %q", drawn.String())
	assert.Check(t, is.DeepEqual(lines[len(lines)-len(want):], want))
	dim := resulttest.Look{Dim: true}
	assert.Check(t, is.Equal(drawn.LookOf(t, want[1], "+2.250s"), dim))
	assert.Check(t, is.Equal(drawn.LookOf(t, want[1], "1.234s"), dim))
	assert.Check(t, is.Equal(drawn.LookOf(t, want[1], "gh pr view 12 exit 0"), resulttest.Look{}))
}

// ---------------------------------------------------------------------------
// Progress display
// ---------------------------------------------------------------------------

const (
	// wide is a terminal wide enough for every line the progress tests print.
	wide = 80
	// spun is how long a step runs before the screens are read: past 3 seconds by less than one
	// frame of the spinner, so that its timer reads 3s whether it rounds or truncates.
	spun = 3300 * time.Millisecond
	// limit is the most the step of a test that has a limit can last.
	limit = 30 * time.Second

	hint = "# waits again; nothing to fix"

	// pushedStep and openedStep are the step lines of a paced command as a terminal stdout shows
	// them.
	pushedStep = "✓ Pushed " + topic + " to origin. 1.2s"
	openedStep = "✓ Opened pull request #12. 2.5s"
)

// sgr matches one select graphic rendition sequence: the sequences that style text, and the only
// ones that a stream that is not a terminal may carry.
var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// pacedLines returns the lines a paced command ends with, in order.
func pacedLines() []string {
	return []string{
		"Pushed " + topic + " to origin.", "Checks still running: build.", "Opened pull request #12.", "Pushed " + topic + " to origin.",
	}
}

// pacedEnd returns the reason and next that follow the lines of a paced command.
func pacedEnd() []string {
	return []string{"Delivered, not green: the checks of #12 are still running", "Next: ils push"}
}

func pushedLine() result.Phrase { return result.Say("Pushed %s to origin.", result.Branch(topic)) }

func openedLine() result.Phrase {
	return result.Say("Opened pull request %s.", result.PullRequest(opened))
}

func waitingLabel() result.Phrase {
	return result.Say("Waiting for checks on %s", result.PullRequest(opened))
}

func mergeStateLabel() result.Phrase {
	return result.Say("Waiting for GitHub to say whether %s can merge", result.PullRequest(opened))
}

// deaf is a progress that hears nothing, told in place of a run's when it has none.
type deaf struct{}

func (deaf) Started(result.Phrase, time.Duration) {}

func (deaf) Said(result.Phrase) {}

func (deaf) Counted(int, int) {}

// progress returns what deps tells how the command advances, or deaf when it has nothing.
func progress(deps landing.Deps) landing.Progress {
	if deps.Progress == nil {
		return deaf{}
	}

	return deps.Progress
}

// paced is a landing command that pushes for 1.2s and opens a pull request for 2.5s, saying the
// line of each as it succeeds, then waits spun for checks of which 1 of 3 are done, has peek read
// the screens once every other goroutine of the run waits, and ends with outcome and pacedLines.
func paced(peek func(), outcome result.Outcome) cli.Command {
	return func(_ context.Context, deps landing.Deps, req landing.Request) result.Result {
		told := progress(deps)
		told.Started(result.Say("Pushing %s", result.Branch(topic)), 0)
		time.Sleep(1200 * time.Millisecond)
		told.Said(pushedLine())
		told.Started(result.Say("Opening a pull request against %s", result.Branch(trunk)), 0)
		time.Sleep(2500 * time.Millisecond)
		told.Said(openedLine())
		told.Started(waitingLabel(), limit)
		told.Counted(1, 3)
		time.Sleep(spun)
		synctest.Wait()
		peek()

		return result.Result{
			Command: req.Command, Words: req.Words, Outcome: outcome,
			Lines:  []result.Phrase{pushedLine(), result.Say("Checks still running: %s.", "build"), openedLine(), pushedLine()},
			Reason: result.Say("the checks of %s are still running", result.PullRequest(opened)),
			Next:   result.NextCommand("push"),
		}
	}
}

// stepping is a landing command that runs one step, begun by start, for spun, has peek read the
// screens once every other goroutine of the run waits, and ends as done with nothing to say.
func stepping(start func(landing.Progress), peek func()) cli.Command {
	return func(_ context.Context, deps landing.Deps, req landing.Request) result.Result {
		start(progress(deps))
		time.Sleep(spun)
		synctest.Wait()
		peek()

		return finished(req)
	}
}

// shown runs app with args inside a bubble, whose clock moves only while every goroutine of the
// run waits, so that each step of a command lasts exactly as long as the command sleeps.
func shown(t *testing.T, app cli.App, args ...string) ran {
	t.Helper()

	var got ran

	synctest.Test(t, func(t *testing.T) { got = launch(t, app, args...) })

	return got
}

// onTerminals returns the cli outside any repository under env, with stdout and stderr each a
// terminal or not, and stderr's terminal wide.
func onTerminals(t *testing.T, env []string, stdoutTerminal, stderrTerminal bool) cli.App {
	t.Helper()

	app := painted(t, env, stdoutTerminal, stderrTerminal)
	app.StderrWidth = func() int { return wide }

	return app
}

// onStderrScreen returns the cli outside any repository under env, with stdout a terminal or not
// and stderr a terminal that is the returned screen, width columns wide.
func onStderrScreen(t *testing.T, env []string, stdoutTerminal bool, width int) (cli.App, *screen) {
	t.Helper()

	stderr := newScreen(t, width)
	app := onTerminals(t, env, stdoutTerminal, true)
	app.Stderr = stderr
	app.StderrWidth = func() int { return width }

	return app, stderr
}

// onStdoutScreen returns the cli outside any repository under env, with stderr no terminal and
// stdout a terminal that is the returned screen, wide columns wide.
func onStdoutScreen(t *testing.T, env []string) (cli.App, *screen) {
	t.Helper()

	stdout := newScreen(t, wide)
	app := onTerminals(t, env, true, false)
	app.Stdout = stdout

	return app, stdout
}

// startedMerge begins the step of waiting for GitHub to say whether a pull request can merge.
func startedMerge(p landing.Progress) { p.Started(mergeStateLabel(), limit) }

// cell is one character of a screen and the select graphic rendition sequences it is drawn with.
type cell struct {
	pen string
	r   rune
}

// screen is a terminal width columns wide, which stdout and stderr may share: it keeps what each
// row shows once every character, line break and cursor sequence written to it is drawn, and fails
// the test on a sequence it does not know.
type screen struct {
	tb      testing.TB
	written strings.Builder
	pending string
	pen     string
	rows    [][]cell
	mu      sync.Mutex
	width   int
	row     int
	col     int
}

func newScreen(tb testing.TB, width int) *screen {
	return &screen{tb: tb, width: width}
}

// Write draws p, keeping a sequence or character it ends inside of until the next write.
func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.written.Write(p)
	s.pending += string(p)
	for s.pending != "" {
		n := s.draw(s.pending)
		if n == 0 {
			break
		}
		s.pending = s.pending[n:]
	}

	return len(p), nil
}

// draw draws the sequence, control character or character text starts with and returns its
// length, or 0 when text ends before it does.
func (s *screen) draw(text string) int {
	if text == "\x1b" || !utf8.FullRuneInString(text) {
		return 0
	}
	if params, ok := strings.CutPrefix(text, "\x1b["); ok {
		end := strings.IndexFunc(params, func(r rune) bool { return r >= 0x40 && r <= 0x7e })
		if end < 0 {
			return 0
		}
		s.control(params[:end], params[end])

		return len("\x1b[") + end + 1
	}

	r, size := utf8.DecodeRuneInString(text)
	switch {
	case r == '\r':
		s.col = 0
	case r == '\n':
		s.row, s.col = s.row+1, 0
	case r == '\b':
		s.col = max(s.col-1, 0)
	case r < ' ' || r == 0x7f:
		s.tb.Errorf("a terminal draws no %q in %q", r, text)
	default:
		s.put(r)
	}

	return size
}

// control draws one control sequence: its parameters and its final byte.
func (s *screen) control(params string, final byte) {
	if strings.HasPrefix(params, "?") && (final == 'h' || final == 'l') {
		return
	}
	if final == 'm' {
		if params == "" || params == "0" {
			s.pen = ""
		} else {
			s.pen += "\x1b[" + params + "m"
		}

		return
	}

	n := 0
	if params != "" {
		var err error
		n, err = strconv.Atoi(params)
		assert.Check(s.tb, err, "parameters of %q", params+string(final))
	}
	switch final {
	case 'K':
		s.erase(n)
	case 'G':
		s.col = max(n, 1) - 1
	case 'A':
		s.row = max(s.row-max(n, 1), 0)
	case 'B':
		s.row += max(n, 1)
	case 'C':
		s.col = min(s.col+max(n, 1), s.width-1)
	case 'D':
		s.col = max(s.col-max(n, 1), 0)
	default:
		s.tb.Errorf("the screen does not draw the control sequence %q", params+string(final))
	}
}

// erase blanks the current row from the cursor to its end (0), from its start to the cursor (1),
// or whole (2).
func (s *screen) erase(how int) {
	row := s.line()
	from, to := 0, len(*row)
	switch how {
	case 0:
		from = min(s.col, to)
	case 1:
		to = min(s.col+1, to)
	}
	for i := from; i < to; i++ {
		(*row)[i] = cell{}
	}
}

// put draws r at the cursor, first moving to the next row when the cursor stands past the last
// column, as a terminal that wraps does.
func (s *screen) put(r rune) {
	if s.col >= s.width {
		s.row, s.col = s.row+1, 0
	}
	row := s.line()
	for len(*row) <= s.col {
		*row = append(*row, cell{})
	}
	(*row)[s.col] = cell{pen: s.pen, r: r}
	s.col++
}

// line returns the row the cursor is on, adding the rows above it that nothing reached yet.
func (s *screen) line() *[]cell {
	for len(s.rows) <= s.row {
		s.rows = append(s.rows, nil)
	}

	return &s.rows[s.row]
}

// lines returns the text of each row the screen shows, without the blanks at the end of each row
// and the blank rows after the last one that shows anything.
func (s *screen) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var shown []string
	for _, row := range s.rows {
		var b strings.Builder
		for _, c := range row {
			b.WriteRune(max(c.r, ' '))
		}
		shown = append(shown, strings.TrimRight(b.String(), " "))
	}
	for len(shown) > 0 && shown[len(shown)-1] == "" {
		shown = shown[:len(shown)-1]
	}

	return shown
}

// drawn returns row i as the screen draws it, each character in the rendition it was written in.
func (s *screen) drawn(i int) resulttest.Drawn {
	s.mu.Lock()
	defer s.mu.Unlock()

	var b strings.Builder
	for _, c := range s.rows[i] {
		b.WriteString("\x1b[0m" + c.pen)
		b.WriteRune(max(c.r, ' '))
	}

	return resulttest.Draw(s.tb, b.String())
}

// raw returns every byte written to the screen.
func (s *screen) raw() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.written.String()
}

func TestApp_Run_WhenAStepRunsWithStderrATerminal_ShowsOneSpinnerLineForItUntilTheEnd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		start func(landing.Progress)
		want  *regexp.Regexp
		name  string
		args  []string
	}{
		{
			name: "step with no limit", args: []string{"merge"},
			start: func(p landing.Progress) { p.Started(result.Say("Merging %s", result.PullRequest(opened)), 0) },
			want:  regexp.MustCompile(`^\S+ Merging #12 3s$`),
		},
		{
			name: "step with a limit", args: []string{"merge"},
			start: startedMerge,
			want:  regexp.MustCompile(`^\S+ Waiting for GitHub to say whether #12 can merge 3s of 30s$`),
		},
		{
			name: "checks counted", args: []string{"push"},
			start: func(p landing.Progress) {
				p.Started(waitingLabel(), limit)
				p.Counted(1, 3)
			},
			want: regexp.MustCompile(`^\S+ Waiting for checks on #12 (3s of 30s\W+1 of 3 done|1 of 3 done\W+3s of 30s)$`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, stderr := onStderrScreen(t, []string{colorTerm}, false, wide)
			var seen []string
			run := stepping(tt.start, func() { seen = stderr.lines() })
			app.SetPush(run)
			app.SetMerge(run)

			got := shown(t, app, tt.args...)

			assert.Check(t, is.Equal(got.code, exitDone))
			assert.Assert(t, is.Len(seen, 1), "stderr showed %q", seen)
			assert.Check(t, tt.want.MatchString(seen[0]), "spinner line = %q, want it to match %s", seen[0], tt.want)
			assert.Check(t, is.Len(stderr.lines(), 0), "stderr still shows %q", stderr.lines())
		})
	}
}

func TestApp_Run_WhenAStepRunsOnAColorTerminal_DrawsTheSpinnerAndItsTimerYellowAndTheLimitDim(t *testing.T) {
	t.Parallel()

	app, stderr := onStderrScreen(t, []string{colorTerm}, false, wide)
	var seen resulttest.Drawn
	app.SetMerge(stepping(startedMerge, func() { seen = stderr.drawn(0) }))

	shown(t, app, "merge")

	line := strings.TrimRight(seen.String(), " ")
	spinner, _, _ := strings.Cut(line, " ")
	yellow := resulttest.Look{Color: "yellow"}
	assert.Check(t, is.Equal(seen.LookOf(t, line, spinner), yellow))
	assert.Check(t, is.Equal(seen.LookOf(t, "3s of 30s", "3s"), yellow))
	assert.Check(t, seen.LookOf(t, "3s of 30s", "of 30s").Dim, "the limit is not dim in %q", line)
}

func TestApp_Run_WhenColorIsOffOnATerminal_DrawsTheSpinnerWithNoEscapeSequence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  []string
		args []string
	}{
		{name: "no color", env: []string{colorTerm, noColor}, args: []string{"merge"}},
		{name: "flag over forced color", env: []string{colorTerm, forced}, args: []string{"merge", "--no-color"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, stderr := onStderrScreen(t, tt.env, false, wide)
			var seen []string
			app.SetMerge(stepping(startedMerge, func() { seen = stderr.lines() }))

			shown(t, app, tt.args...)

			assert.Check(t, is.Len(seen, 1), "stderr showed %q", seen)
			plain(t, "stderr", stderr.raw())
		})
	}
}

func TestApp_Run_WhenTheSpinnerLineIsWiderThanTheTerminal_KeepsItOnOneRow(t *testing.T) {
	t.Parallel()

	const narrow = 24

	tests := []struct {
		name  string
		label result.Phrase
	}{
		{name: "single-width characters", label: mergeStateLabel()},
		{name: "double-width characters", label: result.Say("Pushing %s", result.Branch("整理整理整理整理整理整理"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, stderr := onStderrScreen(t, []string{colorTerm}, false, narrow)
			var seen []string
			app.SetMerge(stepping(func(p landing.Progress) { p.Started(tt.label, limit) }, func() { seen = stderr.lines() }))

			shown(t, app, "merge")

			assert.Assert(t, is.Len(seen, 1), "stderr showed %q", seen)
			assert.Check(t, ansi.StringWidth(seen[0]) <= narrow-1, "spinner line %q is wider than %d columns", seen[0], narrow-1)
			assert.Check(t, regexp.MustCompile(`^\S+ (Waiting|Pushing) `).MatchString(seen[0]), "spinner line = %q", seen[0])
		})
	}
}

func TestApp_Run_WhenTheTerminalIsOneColumnWide_DrawsNoSpinnerCharacter(t *testing.T) {
	t.Parallel()

	app, stderr := onStderrScreen(t, []string{colorTerm}, false, 1)
	var seen []string
	app.SetMerge(stepping(startedMerge, func() { seen = stderr.lines() }))

	got := shown(t, app, "merge")

	assert.Check(t, is.Equal(got.code, exitDone))
	assert.Check(t, is.Len(seen, 0), "stderr showed %q", seen)
}

func TestApp_Run_WhenStdoutAndStderrShareATerminalWithColorOff_PrintsEachStepLineAsItSucceedsThenTheOtherLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  []string
		args []string
	}{
		{name: "no color", env: []string{colorTerm, noColor}, args: []string{"push"}},
		{name: "flag over forced color", env: []string{colorTerm, forced}, args: []string{"push", "--no-color"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			terminal := newScreen(t, wide)
			app := onTerminals(t, tt.env, true, true)
			app.Stdout, app.Stderr = terminal, terminal
			var seen []string
			app.SetPush(paced(func() { seen = terminal.lines() }, result.DeliveredNotGreen))

			got := shown(t, app, tt.args...)

			assert.Check(t, is.Equal(got.code, exitDelivered))
			assert.Assert(t, is.Len(seen, 3), "the terminal showed %q", seen)
			assert.Check(t, is.DeepEqual(seen[:2], []string{pushedStep, openedStep}))
			assert.Check(t, regexp.MustCompile(`^\S+ Waiting for checks on #12 `).MatchString(seen[2]), "spinner line = %q", seen[2])
			lines := pacedLines()
			want := slices.Concat([]string{pushedStep, openedStep, lines[1], lines[3]}, pacedEnd())
			assert.Check(t, is.DeepEqual(terminal.lines(), want))
			plain(t, "the terminal", terminal.raw())
		})
	}
}

func TestApp_Run_WhenStepLinesPrintOnAColorTerminal_DrawsTheCheckGreenAndTheDurationDimGreen(t *testing.T) {
	t.Parallel()

	app, stdout := onStdoutScreen(t, []string{colorTerm})
	app.SetPush(paced(func() {}, result.DeliveredNotGreen))

	shown(t, app, "push")

	drawn := stdout.drawn(0)
	assert.Check(t, is.Equal(drawn.LookOf(t, pushedStep, "✓"), resulttest.Look{Color: "green"}))
	assert.Check(t, is.Equal(drawn.LookOf(t, pushedStep, "1.2s"), resulttest.Look{Color: "green", Dim: true}))
}

func TestApp_Run_WhenAStepLineIsSaidWithoutAStepStartedForIt_TimesItFromTheLineBefore(t *testing.T) {
	t.Parallel()

	app, stdout := onStdoutScreen(t, []string{colorTerm, noColor})
	app.SetPush(func(_ context.Context, deps landing.Deps, req landing.Request) result.Result {
		told := progress(deps)
		told.Started(result.Say("Pushing %s", result.Branch(topic)), 0)
		time.Sleep(3 * time.Second)
		told.Said(pushedLine())
		time.Sleep(2 * time.Second)
		told.Said(openedLine())

		r := finished(req)
		r.Lines = []result.Phrase{pushedLine(), openedLine()}

		return r
	})

	shown(t, app, "push")

	want := []string{"✓ Pushed " + topic + " to origin. 3s", "✓ Opened pull request #12. 2s"}
	assert.Check(t, is.DeepEqual(stdout.lines(), want))
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

			app, stdout := onStdoutScreen(t, tt.env)
			app.SetPush(paced(func() {}, result.Pending))

			got := shown(t, app, "push")

			assert.Check(t, is.Equal(got.code, exitPending))
			lines := stdout.lines()
			last := lines[len(lines)-1]
			assert.Check(t, strings.HasPrefix(last, "Next: ils push "), "Next line = %q", last)
			assert.Check(t, strings.HasSuffix(last, " "+hint), "Next line = %q", last)
			assert.Check(t, is.Equal(stdout.drawn(len(lines)-1).LookOf(t, last, hint).Dim, tt.color), "Next line = %q", last)
			assert.Check(t, is.Equal(strings.Contains(stdout.raw(), escape), tt.color), "stdout = %q", stdout.raw())
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

			app := onTerminals(t, tt.env, tt.terminal, false)
			app.SetPush(paced(func() {}, result.Pending))

			got := shown(t, app, tt.args...)

			assert.Check(t, is.Equal(got.code, exitPending))
			assert.Check(t, !strings.Contains(got.stdout, hint), "stdout = %q", got.stdout)
		})
	}
}

func TestApp_Run_WhenNeitherStreamCountsAsATerminal_WritesNothingBeforeTheWholeResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      []string
		terminal bool
	}{
		{name: "no terminal", env: []string{colorTerm}},
		{name: "forced color off a terminal", env: []string{colorTerm, forced}},
		{name: "dumb terminal", env: []string{"TERM=dumb"}, terminal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stdout, stderr := newScreen(t, wide), newScreen(t, wide)
			app := onTerminals(t, tt.env, tt.terminal, tt.terminal)
			app.Stdout, app.Stderr = stdout, stderr
			var early string
			app.SetPush(paced(func() { early = stdout.raw() }, result.DeliveredNotGreen))

			got := shown(t, app, "push")

			assert.Check(t, is.Equal(got.code, exitDelivered))
			assert.Check(t, is.Equal(early, ""))
			assert.Check(t, is.Equal(stderr.raw(), ""))
			assert.Check(t, is.DeepEqual(stdout.lines(), slices.Concat(pacedLines(), pacedEnd())))
			plain(t, "stdout without its renditions", sgr.ReplaceAllString(stdout.raw(), ""))
		})
	}
}

func TestApp_Run_WhenTheJSONFlagIsAmongTheWordsOnTerminals_PrintsTheWholeResultAfterTheSpinnerIsGone(t *testing.T) {
	t.Parallel()

	stdout, stderr := newScreen(t, wide), newScreen(t, wide)
	app := onTerminals(t, []string{colorTerm}, true, true)
	app.Stdout, app.Stderr = stdout, stderr
	var (
		early    string
		spinning []string
	)
	app.SetPush(paced(func() { early, spinning = stdout.raw(), stderr.lines() }, result.DeliveredNotGreen))

	got := shown(t, app, "push", "--json")

	assert.Check(t, is.Equal(got.code, exitDelivered))
	assert.Check(t, is.Equal(early, ""))
	assert.Check(t, is.Len(spinning, 1), "stderr showed %q", spinning)
	assert.Check(t, is.Len(stderr.lines(), 0), "stderr still shows %q", stderr.lines())
	assert.Check(t, is.DeepEqual(resulttest.Decode(t, stdout.raw()).Lines, pacedLines()))
}

func TestApp_Run_WhenTheWordsRunNoCommand_LeavesTheDisplaySilent(t *testing.T) {
	t.Parallel()

	told := func(_ context.Context, deps landing.Deps, req landing.Request) result.Result {
		progress(deps).Started(waitingLabel(), limit)
		progress(deps).Said(pushedLine())

		return finished(req)
	}

	tests := [][]string{{"pish"}, {"push", "extra"}, {"push", "--help"}, {"merge", "--help"}, {"--version"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			app := onTerminals(t, []string{colorTerm}, true, true)
			app.Version = "0.2.0"
			app.SetPush(told)
			app.SetMerge(told)

			got := launch(t, app, args...)

			assert.Check(t, is.Equal(got.stderr, ""))
			assert.Check(t, !strings.Contains(got.stdout, "✓"), "stdout = %q", got.stdout)
		})
	}
}

func TestApp_Run_WhenDebugIsOn_DrawsNoSpinnerButStreamsStepLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		terminal bool
	}{
		{name: "stderr a terminal", terminal: true},
		{name: "stderr no terminal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stdout, stderr := newScreen(t, wide), newScreen(t, wide)
			app := onTerminals(t, []string{colorTerm, noColor}, true, tt.terminal)
			app.Stdout, app.Stderr = stdout, stderr
			app.SetPush(paced(func() {}, result.DeliveredNotGreen))

			got := shown(t, app, "push", "-d")

			assert.Check(t, is.Equal(got.code, exitDelivered))
			for _, label := range []string{"Pushing", "Opening", "Waiting"} {
				assert.Check(t, !strings.Contains(stderr.raw(), label), "stderr = %q", stderr.raw())
			}
			lines := stdout.lines()
			assert.Assert(t, len(lines) > 2, "stdout showed %q", lines)
			assert.Check(t, is.DeepEqual(lines[:2], []string{pushedStep, openedStep}))
		})
	}
}

// faltering is a stdout whose writes fail while it is down and land in its buffer otherwise.
type faltering struct {
	buf  bytes.Buffer
	mu   sync.Mutex
	down atomic.Bool
}

// Write fails with errReaderGone while f is down, and otherwise adds p to its buffer.
func (f *faltering) Write(p []byte) (int, error) {
	if f.down.Load() {
		return 0, errReaderGone
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.buf.Write(p)
}

func TestApp_Run_WhenTheDisplayCannotBeWritten_PrintsTheResultAndCodeOfARunWithoutIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stdout bool
	}{
		{name: "spinner"},
		{name: "step lines", stdout: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := []string{colorTerm, noColor}
			without := onTerminals(t, env, false, false)
			without.SetPush(paced(func() {}, result.DeliveredNotGreen))
			want := shown(t, without, "push")
			stdout := &faltering{}
			app := onTerminals(t, env, tt.stdout, !tt.stdout)
			app.Stdout, app.Stderr = stdout, shut{err: errReaderGone}
			app.SetPush(func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result {
				stdout.down.Store(true)
				defer stdout.down.Store(false)

				return paced(func() {}, result.DeliveredNotGreen)(ctx, deps, req)
			})

			got := shown(t, app, "push")

			assert.Check(t, is.Equal(got.code, want.code))
			assert.Check(t, is.Equal(stdout.buf.String(), want.stdout))
		})
	}
}
