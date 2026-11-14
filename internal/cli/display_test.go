package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

const (
	// wide is a terminal wide enough for every line the progress tests print.
	wide = 80
	// topic is the branch the progress tests push.
	topic = "tidy-readme"
	// spun is how long a step runs before the screens are read: past 3 seconds by less than one
	// frame of the spinner, so that its timer reads 3s whether it rounds or truncates.
	spun = 3300 * time.Millisecond
)

// elapse lets d pass on the bubble's clock, then waits until every other goroutine of the run waits.
func elapse(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

// pacedLines returns the lines a paced command ends with, in order.
func pacedLines() []string {
	return []string{
		pushedText, "Checks still running: build.", openedText, pushedText,
	}
}

func pushedLine() result.Phrase { return result.Say("Pushed %s to origin.", result.Branch(topic)) }

func openedLine() result.Phrase {
	return result.Say("Opened pull request %s.", result.PullRequest(opened))
}

func waitingLabel() result.Phrase {
	return result.Say("Waiting for checks on %s", result.PullRequest(opened))
}

func pushingLabel() result.Phrase { return result.Say("Pushing %s", result.Branch(topic)) }

// paced is a landing command that pushes for 1.2s and opens a pull request for 2.5s, saying the
// line of each as it succeeds, then waits spun for checks of which 1 of 3 are done, has peek read
// the screens once every other goroutine of the run waits, and ends with outcome and pacedLines.
func paced(peek func(), outcome result.Outcome) cli.Command {
	return func(_ context.Context, deps landing.Deps, req landing.Request) result.Result {
		told := deps.Progress
		told.Started(pushingLabel())
		time.Sleep(1200 * time.Millisecond)
		told.Said(pushedLine())
		told.Started(result.Say("Opening a pull request against %s", result.Branch(trunk)))
		time.Sleep(2500 * time.Millisecond)
		told.Said(openedLine())
		startedWaiting(told)
		told.Counted(1, 3)
		elapse(spun)
		peek()

		return result.Result{
			Command: req.Command, Words: req.Words, Outcome: outcome,
			Lines:  []result.Phrase{pushedLine(), result.Say("Checks still running: %s.", "build"), openedLine(), pushedLine()},
			Reason: stillRunning(),
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

func startedWaiting(p landing.Progress) { p.Started(waitingLabel()) }

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
	if final == 'K' {
		s.erase(params)

		return
	}

	s.tb.Errorf("the screen does not draw the control sequence %q", params+string(final))
}

// erase blanks the cursor's row from the cursor to its end when params is empty or 0, and the
// whole row when params is 2, leaving the cursor where it stands.
func (s *screen) erase(params string) {
	row := s.line()
	switch params {
	case "", "0":
		*row = (*row)[:min(s.col, len(*row))]
	case "2":
		*row = nil
	default:
		s.tb.Errorf("the screen does not erase in line with the parameter %q", params)
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
	return s.rowTexts(func(row []cell) string {
		return strings.TrimRight(render(row), " ")
	})
}

// filled returns the text of each row the screen shows as lines does, but up to the last
// character written on the row, so that the spaces written at the end of a row stay in it.
func (s *screen) filled() []string {
	return s.rowTexts(func(row []cell) string {
		end := len(row)
		for end > 0 && row[end-1].r == 0 {
			end--
		}

		return render(row[:end])
	})
}

// rowTexts returns the text of each row as text gives it, without the blank rows after the last
// one that shows anything.
func (s *screen) rowTexts(text func(row []cell) string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var shown []string
	for _, row := range s.rows {
		shown = append(shown, text(row))
	}
	for len(shown) > 0 && shown[len(shown)-1] == "" {
		shown = shown[:len(shown)-1]
	}

	return shown
}

// render returns the characters of row as text, a blank for a cell nothing was written to.
func render(row []cell) string {
	var b strings.Builder
	for _, c := range row {
		b.WriteRune(max(c.r, ' '))
	}

	return b.String()
}

// styledRow returns row i as the screen draws it, each character behind the rendition it was
// written in, for resulttest.Draw to read on the test's goroutine.
func (s *screen) styledRow(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var b strings.Builder
	for _, c := range s.rows[i] {
		b.WriteString("\x1b[0m")
		b.WriteString(c.pen)
		b.WriteRune(max(c.r, ' '))
	}

	return b.String()
}

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

const (
	// pushedText and openedText are the texts of the lines a paced command says when its steps
	// succeed.
	pushedText = "Pushed " + topic + " to origin."
	openedText = "Opened pull request #12."

	// pushedStep and openedStep are the step lines of a paced command as a terminal stdout shows
	// them.
	pushedStep = "✓ " + pushedText + " 1.2s"
	openedStep = "✓ " + openedText + " 2.5s"
)

// sgr matches one select graphic rendition sequence: the sequences that style text, and the only
// ones that a stream that is not a terminal may carry.
var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// pacedEnd returns the reason and next that follow the lines of a paced command.
func pacedEnd() []string {
	return []string{
		resulttest.ReasonLine(result.DeliveredNotGreen, "the checks of #12 are still running"),
		resulttest.NextLine("ils push"),
	}
}

// stepping is a landing command that runs one step, begun by start, for spun, has peek read the
// screens once every other goroutine of the run waits, and ends as done with nothing to say.
func stepping(start func(landing.Progress), peek func()) cli.Command {
	return func(_ context.Context, deps landing.Deps, req landing.Request) result.Result {
		start(deps.Progress)
		elapse(spun)
		peek()

		return finished(req)
	}
}

// onStderrScreen returns the cli outside any repository under env, with stdout a terminal or not
// and stderr a terminal that is the returned screen, width columns wide. It gives the cli no query
// of the terminal's foreground job, so ils counts as that job.
func onStderrScreen(t *testing.T, env []string, stdoutTerminal bool, width int) (cli.App, *screen) {
	t.Helper()

	stderr := newScreen(t, width)
	app := painted(t, env, stdoutTerminal, true)
	app.Stderr = stderr
	app.StderrWidth = func() int { return width }

	return app, stderr
}

// onSharedScreen returns the cli outside any repository under env, with stdout and stderr both
// terminals that are the returned screen, wide columns wide.
func onSharedScreen(t *testing.T, env []string) (cli.App, *screen) {
	t.Helper()

	terminal := newScreen(t, wide)
	app := onTerminals(t, env, true, true)
	app.Stdout, app.Stderr = terminal, terminal

	return app, terminal
}

// onTwoScreens returns the cli outside any repository under env, with stdout and stderr each a
// terminal or not, and each a screen of its own, wide columns wide.
func onTwoScreens(t *testing.T, env []string, stdoutTerminal, stderrTerminal bool) (cli.App, *screen, *screen) {
	t.Helper()

	stdout, stderr := newScreen(t, wide), newScreen(t, wide)
	app := onTerminals(t, env, stdoutTerminal, stderrTerminal)
	app.Stdout, app.Stderr = stdout, stderr

	return app, stdout, stderr
}

var cursorSequence = regexp.MustCompile(regexp.QuoteMeta(ansi.HideCursor) + "|" + regexp.QuoteMeta(ansi.ShowCursor))

// unstyled checks, without stopping the test, that text carries no select graphic rendition
// sequence; what names the text in the failure.
func unstyled(t *testing.T, what, text string) {
	t.Helper()

	assert.Check(t, !sgr.MatchString(text), "%s carries a rendition:\n%q", what, text)
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
			name: "no checks counted", args: []string{"merge"},
			start: func(p landing.Progress) { p.Started(result.Say("Merging %s", result.PullRequest(opened))) },
			want:  regexp.MustCompile(`^\S+ Merging #12 3s$`),
		},
		{
			name: "checks counted", args: []string{"push"},
			start: func(p landing.Progress) {
				startedWaiting(p)
				p.Counted(1, 3)
			},
			want: regexp.MustCompile(`^\S+ Waiting for checks on #12 3s\D+1\D+3\D*$`),
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

func TestApp_Run_WhenSpinnerLinesShowOnStderr_HidesTheCursorOnceForEachUntilItGoes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		arrange func(t *testing.T, peek func(*screen)) (cli.App, *screen)
		name    string
		toggles int
	}{
		{
			name:    "one step",
			toggles: 1,
			arrange: func(t *testing.T, peek func(*screen)) (cli.App, *screen) {
				t.Helper()

				app, stderr := onStderrScreen(t, []string{colorTerm}, false, wide)
				app.SetPush(stepping(startedWaiting, func() { peek(stderr) }))

				return app, stderr
			},
		},
		{
			name:    "steps whose lines print between them",
			toggles: 3,
			arrange: func(t *testing.T, peek func(*screen)) (cli.App, *screen) {
				t.Helper()

				app, terminal := onSharedScreen(t, []string{colorTerm})
				app.SetPush(paced(func() { peek(terminal) }, result.DeliveredNotGreen))

				return app, terminal
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hiddenWhileShown bool
			app, stderr := tt.arrange(t, func(s *screen) { hiddenWhileShown = s.cursorHidden() })

			shown(t, app, "push")

			assert.Check(t, hiddenWhileShown, "the cursor showed beside the spinner line")
			toggles := cursorSequence.FindAllString(stderr.raw(), -1)
			assert.Check(t, is.DeepEqual(toggles, slices.Repeat([]string{ansi.HideCursor, ansi.ShowCursor}, tt.toggles)))
		})
	}
}

func TestApp_Run_WhenNoStepStartsOnAStderrTerminal_NeitherHidesNorShowsTheCursor(t *testing.T) {
	t.Parallel()
	app, stderr := onStderrScreen(t, []string{colorTerm}, false, wide)
	app.SetMerge(saying(pushedLine()))

	got := shown(t, app, "merge")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Len(cursorSequence.FindAllString(stderr.raw(), -1), 0), "stderr = %q", stderr.raw())
}

func TestApp_Run_WhenIlsIsNotTheForegroundJob_DrawsNothingUntilTheFrameAfterItIs(t *testing.T) {
	t.Parallel()

	app, stderr := onStderrScreen(t, []string{colorTerm}, false, wide)
	var foreground atomic.Bool
	app.StderrForeground = foreground.Load
	var inBackground string
	var again []string
	app.SetMerge(func(_ context.Context, deps landing.Deps, req landing.Request) result.Result {
		startedWaiting(deps.Progress)
		elapse(spun)
		inBackground = stderr.raw()
		foreground.Store(true)
		elapse(cli.FrameEvery)
		again = stderr.lines()

		return finished(req)
	})

	shown(t, app, "merge")

	assert.Check(t, is.Equal(inBackground, ""))
	assert.Check(t, is.Len(again, 1), "stderr showed %q", again)
}

func TestApp_Run_WhenIlsStopsBeingTheForegroundJob_LeavesTheSpinnerLineWithTheCursorShownUntilItIsAgain(t *testing.T) {
	t.Parallel()

	app, stderr := onStderrScreen(t, []string{colorTerm}, false, wide)
	var foreground atomic.Bool
	foreground.Store(true)
	app.StderrForeground = foreground.Load
	var before, after string
	var hiddenAgain bool
	app.SetMerge(func(_ context.Context, deps landing.Deps, req landing.Request) result.Result {
		startedWaiting(deps.Progress)
		elapse(spun)
		before = stderr.raw()
		foreground.Store(false)
		elapse(spun)
		after = stderr.raw()
		foreground.Store(true)
		elapse(cli.FrameEvery)
		hiddenAgain = stderr.cursorHidden()

		return finished(req)
	})

	shown(t, app, "merge")

	assert.Check(t, is.Equal(after, before+ansi.ShowCursor))
	assert.Check(t, hiddenAgain, "the spinner line was not drawn again once ils was the foreground job")
}

func TestApp_Run_WhenAStepRunsOnAColorTerminal_StylesTheSpinnerLine(t *testing.T) {
	t.Parallel()

	app, stderr := onStderrScreen(t, []string{colorTerm}, false, wide)
	var row string
	app.SetMerge(stepping(startedWaiting, func() { row = stderr.styledRow(0) }))

	shown(t, app, "merge")

	seen := resulttest.Draw(t, row)
	line := strings.TrimRight(seen.String(), " ")
	spinner, _, _ := strings.Cut(line, " ")
	assert.Check(t, seen.LookOf(t, line, spinner) != resulttest.Look{}, "the spinner is unstyled in %q", line)
}

func TestApp_Run_WhenTheSpinnerLineIsWiderThanTheTerminal_KeepsItOnOneRow(t *testing.T) {
	t.Parallel()

	const narrow = 24

	app, stderr := onStderrScreen(t, []string{colorTerm}, false, narrow)
	var seen []string
	app.SetMerge(stepping(startedWaiting, func() { seen = stderr.lines() }))

	shown(t, app, "merge")

	assert.Assert(t, is.Len(seen, 1), "stderr showed %q", seen)
	assert.Check(t, ansi.StringWidth(seen[0]) <= narrow-1, "spinner line %q is wider than %d columns", seen[0], narrow-1)
	assert.Check(t, regexp.MustCompile(`^\S+ Waiting `).MatchString(seen[0]), "spinner line = %q", seen[0])
}

func TestApp_Run_WhenStdoutAndStderrShareATerminalWithColorOff_PrintsEachStepLineAsItSucceedsThenTheOtherLines(t *testing.T) {
	t.Parallel()

	app, terminal := onSharedScreen(t, []string{colorTerm, noColor})
	var seen []string
	app.SetPush(paced(func() { seen = terminal.lines() }, result.DeliveredNotGreen))

	got := shown(t, app, "push")

	assert.Check(t, is.Equal(got.code, exitDelivered))
	assert.Assert(t, is.Len(seen, 3), "the terminal showed %q", seen)
	assert.Check(t, is.DeepEqual(seen[:2], []string{pushedStep, openedStep}))
	assert.Check(t, regexp.MustCompile(`^\S+ Waiting for checks on #12 `).MatchString(seen[2]), "spinner line = %q", seen[2])
	lines := pacedLines()
	want := slices.Concat([]string{pushedStep, openedStep, "✗ Waiting for checks on #12", lines[1], lines[3]}, pacedEnd())
	assert.Check(t, is.DeepEqual(terminal.lines(), want))
}

func TestApp_Run_WhenColorIsOffOnASharedTerminal_DrawsTheProgressWithoutRendition(t *testing.T) {
	t.Parallel()

	app, terminal := onSharedScreen(t, []string{colorTerm, noColor})
	app.SetPush(paced(func() {}, result.DeliveredNotGreen))

	shown(t, app, "push")

	unstyled(t, "the terminal", terminal.raw())
}

func TestApp_Run_WhenStepLinesPrintOnAColorTerminal_StylesTheStepLine(t *testing.T) {
	t.Parallel()

	app, stdout := onStdoutScreen(t, []string{colorTerm})
	app.SetPush(paced(func() {}, result.DeliveredNotGreen))

	shown(t, app, "push")

	drawn := resulttest.Draw(t, stdout.styledRow(0))
	assert.Check(t, drawn.LookOf(t, pushedStep, "✓") != resulttest.Look{}, "step line = %q", pushedStep)
}

func TestApp_Run_WhenAStepLineIsSaidWithoutAStepStartedForIt_TimesItFromTheLineBefore(t *testing.T) {
	t.Parallel()

	app, stdout := onStdoutScreen(t, []string{colorTerm, noColor})
	app.SetPush(func(_ context.Context, deps landing.Deps, req landing.Request) result.Result {
		told := deps.Progress
		told.Started(pushingLabel())
		time.Sleep(3 * time.Second)
		told.Said(pushedLine())
		time.Sleep(2 * time.Second)
		told.Said(openedLine())

		r := finished(req)
		r.Lines = []result.Phrase{pushedLine(), openedLine()}

		return r
	})

	shown(t, app, "push")

	want := []string{"✓ " + pushedText + " 3s", "✓ " + openedText + " 2s"}
	assert.Check(t, is.DeepEqual(stdout.lines(), want))
}

func TestApp_Run_WhenLinesPrintOverTheSpinnerLine_LeavesNoSpacesWrittenAfterAnyOfThem(t *testing.T) {
	t.Parallel()

	app, terminal := onSharedScreen(t, []string{colorTerm, noColor})
	var spinning, spinningFilled []string
	app.SetPush(paced(func() { spinning, spinningFilled = terminal.lines(), terminal.filled() }, result.DeliveredNotGreen))

	shown(t, app, "push")

	assert.Check(t, is.DeepEqual(spinningFilled, spinning), "while the spinner line showed")
	assert.Check(t, is.DeepEqual(terminal.filled(), terminal.lines()), "once the run ended")
}

func TestApp_Run_WhenAStepLineIsSaid_TimesItToTheNearestTenthOfASecondFromATenthOfASecond(t *testing.T) {
	t.Parallel()

	const pushed = "✓ " + pushedText

	tests := []struct {
		name string
		want string
		took time.Duration
	}{
		{name: "under a tenth of a second", took: 60 * time.Millisecond, want: pushed},
		{name: "exactly a tenth of a second", took: 100 * time.Millisecond, want: pushed + " 100ms"},
		{name: "seconds", took: 3660 * time.Millisecond, want: pushed + " 3.7s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, stdout := onStdoutScreen(t, []string{colorTerm, noColor})
			app.SetPush(func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result {
				deps.Progress.Started(pushingLabel())
				time.Sleep(tt.took)
				deps.Progress.Said(pushedLine())

				return saying(pushedLine())(ctx, deps, req)
			})

			shown(t, app, "push")

			assert.Check(t, is.DeepEqual(stdout.lines(), []string{tt.want}))
		})
	}
}

func TestApp_Run_WhenNeitherStreamIsATerminal_WritesNothingBeforeTheWholeResult(t *testing.T) {
	t.Parallel()
	app, stdout, stderr := onTwoScreens(t, []string{colorTerm, forced}, false, false)
	var early string
	app.SetPush(paced(func() { early = stdout.raw() }, result.DeliveredNotGreen))

	got := shown(t, app, "push")

	assert.Check(t, is.Equal(got.code, exitDelivered))
	assert.Check(t, is.Equal(early, ""))
	assert.Check(t, is.Equal(stderr.raw(), ""))
	assert.Check(t, is.DeepEqual(stdout.lines(), slices.Concat(pacedLines(), pacedEnd())))
}

func TestApp_Run_WhenTheJSONFlagIsAmongTheWordsOnTerminals_PrintsTheWholeResult(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := onTwoScreens(t, []string{colorTerm}, true, true)
	var (
		early    string
		spinning []string
	)
	app.SetPush(paced(func() { early, spinning = stdout.raw(), stderr.lines() }, result.DeliveredNotGreen))

	got := shown(t, app, "push", "--json")

	assert.Check(t, is.Equal(got.code, exitDelivered))
	assert.Check(t, is.Equal(early, ""))
	assert.Check(t, is.Len(spinning, 1), "stderr showed %q", spinning)
	assert.Check(t, is.DeepEqual(resulttest.Decode(t, stdout.raw()).Lines, pacedLines()))
}

func TestApp_Run_WhenDebugIsOn_StreamsStepLinesInPlaceOfTheSpinner(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := onTwoScreens(t, []string{colorTerm, noColor}, true, true)
	app.SetPush(paced(func() {}, result.DeliveredNotGreen))

	got := shown(t, app, "push", "-d")

	assert.Check(t, is.Equal(got.code, exitDelivered))
	assert.Check(t, !regexp.MustCompile(`Pushing|Opening|Waiting`).MatchString(stderr.raw()), "stderr = %q, want no spinner label", stderr.raw())
	lines := stdout.lines()
	assert.Assert(t, len(lines) > 2, "stdout showed %q", lines)
	assert.Check(t, is.DeepEqual(lines[:2], []string{pushedStep, openedStep}))
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

func TestApp_Run_WhenTheDisplayCannotBeWritten_EndsAsARunWithoutIt(t *testing.T) {
	t.Parallel()

	stdout := &faltering{}
	app := onTerminals(t, []string{colorTerm, noColor}, true, false)
	app.Stdout, app.Stderr = stdout, shut{err: errReaderGone}
	app.SetPush(func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result {
		stdout.down.Store(true)
		defer stdout.down.Store(false)

		return paced(func() {}, result.DeliveredNotGreen)(ctx, deps, req)
	})

	got := shown(t, app, "push")

	assert.Check(t, is.Equal(got.code, exitDelivered))
	printed := outputLines(stdout.buf.String())
	assert.Check(t, is.DeepEqual(printed, slices.Concat(pacedLines(), pacedEnd())))
}

// endingWith is a landing command that starts the step of waitingLabel, says openedLine to end it
// unless running, and ends as outcome with openedLine as its one line.
func endingWith(outcome result.Outcome, running bool) cli.Command {
	return func(_ context.Context, deps landing.Deps, req landing.Request) result.Result {
		startedWaiting(deps.Progress)
		if !running {
			deps.Progress.Said(pushedLine())
		}

		return result.Result{
			Command: req.Command, Words: req.Words, Outcome: outcome,
			Lines: []result.Phrase{openedLine()}, Reason: stillRunning(), Next: result.NextCommand("push"),
		}
	}
}

func TestApp_Run_WhenACommandEndsWhileAStepRuns_CrossesTheStepBeforeTheResultOnlyWhenItEndsBadly(t *testing.T) {
	t.Parallel()

	const crossed = "✗ Waiting for checks on #12"
	tests := []struct {
		name    string
		want    []string
		outcome result.Outcome
		running bool
	}{
		{name: "broke", outcome: result.Broke, running: true, want: []string{crossed}},
		{name: "done", outcome: result.Done, running: true},
		{name: "broke with no step running", outcome: result.Broke, want: []string{"✓ " + pushedText}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, stdout := onStdoutScreen(t, []string{colorTerm, noColor})
			app.SetPush(endingWith(tt.outcome, tt.running))

			shown(t, app, "push")

			lines := stdout.lines()
			at := slices.Index(lines, openedText)
			assert.Assert(t, at >= 0, "stdout showed %q", lines)
			assert.Check(t, is.DeepEqual(lines[:at], tt.want, cmpopts.EquateEmpty()))
		})
	}
}

// quiet is how long stderr of a stopped run stays silent before the tests take it to have said
// all it wrote before the stop: the bytes written before the stop are already in the pipe.
const quiet = 200 * time.Millisecond

// pipeBuffer is how many bytes of stderr one read of the pipe takes.
const pipeBuffer = 4096

// stuckChild is the test binary run as stuck runs, in a process group of its own so that a stop
// signal from the terminal reaches it.
type stuckChild struct {
	cmd    *exec.Cmd
	stderr *screen
	pipe   *os.File
	stdout *bufio.Reader
}

// startStuck starts the test binary as stuck runs it in the given way; the end of the test kills it
// unless the test waited for it.
func startStuck(t *testing.T, way string) *stuckChild {
	t.Helper()

	return startStuckBy(t, way, os.Args[0])
}

// startStuckIgnoring is startStuck, except that the run starts with signal ignored, as a
// non-interactive shell ignores the interrupt of a background job: the shell ignores it, and the
// ignore holds across its exec of the test binary. signal is a name trap takes, such as INT or
// TSTP.
func startStuckIgnoring(t *testing.T, way, signal string) *stuckChild {
	t.Helper()

	return startStuckBy(t, way, "sh", "-c", `trap "" `+signal+`; exec "$0"`, os.Args[0])
}

// startStuckBy is startStuck run through argv, whose run ends in an exec of the test binary.
func startStuckBy(t *testing.T, way string, argv ...string) *stuckChild {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv ends in this test binary
	cmd.Env = append(os.Environ(), stuckRun+"="+way)
	cmd.Dir = t.TempDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	read, write, err := os.Pipe()
	assert.NilError(t, err)
	cmd.Stderr = write
	stdout, err := cmd.StdoutPipe()
	assert.NilError(t, err)
	assert.NilError(t, cmd.Start())
	assert.NilError(t, write.Close())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Wait() // it reports the kill the end of the test's context sent
		}
		assert.Check(t, read.Close())
	})

	return &stuckChild{cmd: cmd, stderr: newScreen(t, wide), pipe: read, stdout: bufio.NewReader(stdout)}
}

// await draws stderr onto the screen one byte at a time until done holds, and fails the test
// when stderr ends first; what names the state awaited.
func (c *stuckChild) await(t *testing.T, what string, done func() bool) {
	t.Helper()

	b := make([]byte, 1)
	for !done() {
		n, err := c.pipe.Read(b)
		assert.NilError(t, err, "stderr ended before %s:\n%q", what, c.stderr.raw())
		_, err = c.stderr.Write(b[:n])
		assert.NilError(t, err)
	}
}

// awaitReady waits for the landing command of the run to say on stdout that it runs.
func (c *stuckChild) awaitReady(t *testing.T) {
	t.Helper()

	c.awaitLine(t, ready)
}

// awaitSpinning waits until the spinner line of the run shows and its landing command says on
// stdout that it runs.
func (c *stuckChild) awaitSpinning(t *testing.T) {
	t.Helper()

	c.await(t, "the spinner line showed", c.spinnerShows)
	c.awaitReady(t)
}

// awaitLine reads the next line of stdout, and fails the test unless it is want.
func (c *stuckChild) awaitLine(t *testing.T, want string) {
	t.Helper()

	line, err := c.stdout.ReadString('\n')
	assert.NilError(t, err, "stdout ended before %q", want)
	assert.Assert(t, line == want+"\n", "stdout = %q, want %q", line, want+"\n")
}

// drawRead reads what stderr holds into buf and draws it onto the screen, and reports whether the
// read deadline passed, which clears the deadline. It fails the test on any other error, naming
// what the run was while stderr ended: state.
func (c *stuckChild) drawRead(t *testing.T, buf []byte, state string) (quietPassed bool) {
	t.Helper()

	n, err := c.pipe.Read(buf)
	_, writeErr := c.stderr.Write(buf[:n])
	assert.NilError(t, writeErr)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		assert.NilError(t, c.pipe.SetReadDeadline(time.Time{}))

		return true
	}
	assert.NilError(t, err, "stderr ended while the run was %s:\n%q", state, c.stderr.raw())

	return false
}

// drain draws onto the screen everything stderr holds, which is all a stopped run wrote.
func (c *stuckChild) drain(t *testing.T) {
	t.Helper()

	buf := make([]byte, pipeBuffer)
	for {
		assert.NilError(t, c.pipe.SetReadDeadline(time.Now().Add(quiet)))
		if c.drawRead(t, buf, "stopped") {
			return
		}
	}
}

// end draws the rest of stderr onto the screen, reads the rest of stdout, and waits for the run to
// end; it returns the rest of stdout and what waiting returned.
func (c *stuckChild) end(t *testing.T) (stdout string, err error) {
	t.Helper()

	_, err = io.Copy(c.stderr, c.pipe)
	assert.NilError(t, err)
	rest, err := io.ReadAll(c.stdout)
	assert.NilError(t, err)

	return string(rest), c.cmd.Wait()
}

// readsStopDisposition reports whether ils reads the disposition of the stop signal on this system,
// as the build constraints of suspend_bsd.go and suspend_linux.go select: the darwin constraint
// also holds on ios, and the linux one on android. Elsewhere ils stops on the signal even when
// started with it ignored.
func readsStopDisposition() bool {
	switch runtime.GOOS {
	case "darwin", "ios", "dragonfly", "freebsd":
		return true
	case "linux", "android":
		return !strings.HasPrefix(runtime.GOARCH, "mips")
	default:
		return false
	}
}

// spinnerShows reports whether the screen shows a line with the cursor hidden.
func (c *stuckChild) spinnerShows() bool {
	return c.stderr.cursorHidden() && len(c.stderr.lines()) > 0
}

// cleared reports whether the screen shows nothing and its cursor.
func (c *stuckChild) cleared() bool {
	return !c.stderr.cursorHidden() && len(c.stderr.lines()) == 0
}

// suspend sends the run the stop signal of a terminal and returns its status once it stopped or
// ended, without reaping it.
func (c *stuckChild) suspend(t *testing.T) syscall.WaitStatus {
	t.Helper()

	assert.NilError(t, c.cmd.Process.Signal(syscall.SIGTSTP))
	var status syscall.WaitStatus
	_, err := syscall.Wait4(c.cmd.Process.Pid, &status, syscall.WUNTRACED, nil)
	assert.NilError(t, err)

	return status
}

// stopped reports whether status is that of a run that neither exited nor died of a signal.
func stopped(status syscall.WaitStatus) bool {
	return !status.Exited() && !status.Signaled()
}

// suspendedSpinning starts a stuckHolding run, suspends it once its spinner line shows and its
// landing command runs, and returns it once its stopped run shows nothing.
func suspendedSpinning(t *testing.T) *stuckChild {
	t.Helper()

	run := startStuck(t, stuckHolding)
	run.awaitSpinning(t)
	status := run.suspend(t)
	assert.Assert(t, stopped(status), "status = %v, want the run stopped", status)
	run.drain(t)
	assert.Assert(t, run.cleared(), "the spinner line still shows while the run is stopped:\n%q", run.stderr.raw())

	return run
}

func TestApp_Run_WhenContinuedAfterASuspend_DrawsTheSpinnerLineAgain(t *testing.T) {
	t.Parallel()
	run := suspendedSpinning(t)

	assert.NilError(t, run.cmd.Process.Signal(syscall.SIGCONT))

	run.await(t, "the spinner line showed again", run.spinnerShows)
}

func TestApp_Run_WhenStartedWithTheStopSignalIgnored_GoesOnIgnoringIt(t *testing.T) {
	t.Parallel()
	if !readsStopDisposition() {
		t.Skipf("ils does not read the disposition of the stop signal on %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	run := startStuckIgnoring(t, stuckTellingStop, "TSTP")
	t.Cleanup(func() {
		assert.Check(t, run.cmd.Process.Signal(syscall.SIGTERM))
		_, _ = run.end(t) // the run ends Stopped. on the termination; the stop line is the check
	})

	line, err := run.stdout.ReadString('\n')
	assert.NilError(t, err)

	assert.Check(t, is.Equal(line, stopIgnoredLine+"\n"), "ils listens for the stop signal it was started ignoring")
}

func TestApp_Run_WhenSuspendedAfterTheLandingCommandReturned_Stops(t *testing.T) {
	t.Parallel()

	run := startStuck(t, stuckReturned)
	run.awaitReady(t)

	status := run.suspend(t)

	assert.Check(t, stopped(status), "status = %v, want the run stopped", status)
}
