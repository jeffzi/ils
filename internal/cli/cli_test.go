package cli_test

import (
	"bytes"
	"context"
	"errors"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

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
)

// pacedLines returns the lines a paced command ends with, in order.
func pacedLines() []string {
	return []string{
		"Pushed " + topic + " to origin.", "Checks still running: build.", "Opened pull request #12.", "Pushed " + topic + " to origin.",
	}
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
// row shows and whether its cursor is hidden once every character, line break and cursor sequence
// written to it is drawn, and fails the test on a sequence it does not know.
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
	hidden  bool
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
		if params == cursorMode {
			s.hidden = final == 'l'
		}

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

func (s *screen) cursorHidden() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.hidden
}

// cursorMode is the DEC private mode that sets whether the cursor shows: the final byte h shows it
// and l hides it.
const cursorMode = "?25"

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
