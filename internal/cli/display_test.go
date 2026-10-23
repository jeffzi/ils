package cli_test

import (
	"bytes"
	"context"
	"regexp"
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
			start: func(p landing.Progress) { p.Started(mergeStateLabel(), limit) },
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

			stderr := newScreen(t, wide)
			app := onTerminals(t, []string{colorTerm}, false, true)
			app.Stderr = stderr
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

	stderr := newScreen(t, wide)
	app := onTerminals(t, []string{colorTerm}, false, true)
	app.Stderr = stderr
	var seen resulttest.Drawn
	app.SetMerge(stepping(func(p landing.Progress) { p.Started(mergeStateLabel(), limit) }, func() { seen = stderr.drawn(0) }))

	shown(t, app, "merge")

	line := strings.TrimRight(seen.String(), " ")
	spinner, _, _ := strings.Cut(line, " ")
	yellow := resulttest.Look{Color: "yellow"}
	assert.Check(t, is.Equal(seen.LookOf(t, line, spinner), yellow))
	assert.Check(t, is.Equal(seen.LookOf(t, "3s of 30s", "3s"), yellow))
	assert.Check(t, seen.LookOf(t, "3s of 30s", "of 30s").Dim, "the limit is not dim in %q", line)
}

func TestApp_Run_WhenColorIsOffOnATerminal_DrawsTheSpinnerWithNoEscapeSequenceAndTakesItOff(t *testing.T) {
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

			stderr := newScreen(t, wide)
			app := onTerminals(t, tt.env, false, true)
			app.Stderr = stderr
			var seen []string
			app.SetMerge(stepping(func(p landing.Progress) { p.Started(mergeStateLabel(), limit) }, func() { seen = stderr.lines() }))

			shown(t, app, tt.args...)

			assert.Check(t, is.Len(seen, 1), "stderr showed %q", seen)
			plain(t, "stderr", stderr.raw())
			assert.Check(t, is.Len(stderr.lines(), 0), "stderr still shows %q", stderr.lines())
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

			stderr := newScreen(t, narrow)
			app := onTerminals(t, []string{colorTerm}, false, true)
			app.Stderr = stderr
			app.StderrWidth = func() int { return narrow }
			var seen []string
			app.SetMerge(stepping(func(p landing.Progress) { p.Started(tt.label, limit) }, func() { seen = stderr.lines() }))

			shown(t, app, "merge")

			assert.Assert(t, is.Len(seen, 1), "stderr showed %q", seen)
			assert.Check(t, ansi.StringWidth(seen[0]) <= narrow-1, "spinner line %q is wider than %d columns", seen[0], narrow-1)
			assert.Check(t, regexp.MustCompile(`^\S+ (Waiting|Pushing) `).MatchString(seen[0]), "spinner line = %q", seen[0])
			assert.Check(t, is.Len(stderr.lines(), 0), "stderr still shows %q", stderr.lines())
		})
	}
}

func TestApp_Run_WhenTheTerminalIsOneColumnWide_DrawsNoSpinnerCharacter(t *testing.T) {
	t.Parallel()

	stderr := newScreen(t, 1)
	app := onTerminals(t, []string{colorTerm}, false, true)
	app.Stderr = stderr
	app.StderrWidth = func() int { return 1 }
	var seen []string
	app.SetMerge(stepping(func(p landing.Progress) { p.Started(mergeStateLabel(), limit) }, func() { seen = stderr.lines() }))

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

	stdout := newScreen(t, wide)
	app := onTerminals(t, []string{colorTerm}, true, false)
	app.Stdout = stdout
	app.SetPush(paced(func() {}, result.DeliveredNotGreen))

	shown(t, app, "push")

	drawn := stdout.drawn(0)
	assert.Check(t, is.Equal(drawn.LookOf(t, pushedStep, "✓"), resulttest.Look{Color: "green"}))
	assert.Check(t, is.Equal(drawn.LookOf(t, pushedStep, "1.2s"), resulttest.Look{Color: "green", Dim: true}))
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

			stdout := newScreen(t, wide)
			app := onTerminals(t, tt.env, true, false)
			app.Stdout = stdout
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
			assert.Check(t, !strings.Contains(got.stdout, "waits again"), "stdout = %q", got.stdout)
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

func TestApp_Run_WhenARunIsRefusedBeforeACommandStarts_DrawsNoSpinnerAndStreamsNothing(t *testing.T) {
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
