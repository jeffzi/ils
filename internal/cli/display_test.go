package cli_test

import (
	"bytes"
	"context"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

const (
	// pushedStep and openedStep are the step lines of a paced command as a terminal stdout shows
	// them.
	pushedStep = "✓ Pushed " + topic + " to origin. 1.2s"
	openedStep = "✓ Opened pull request #12. 2.5s"
)

// sgr matches one select graphic rendition sequence: the sequences that style text, and the only
// ones that a stream that is not a terminal may carry.
var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// pacedEnd returns the reason and next that follow the lines of a paced command.
func pacedEnd() []string {
	return []string{"Delivered, not green: the checks of #12 are still running", "Next: ils push"}
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

// onStderrScreen returns the cli outside any repository under env, with stdout a terminal or not
// and stderr a terminal that is the returned screen, width columns wide. It gives the cli no query
// of the terminal's foreground job, so ils counts as that job.
func onStderrScreen(t *testing.T, env []string, stdoutTerminal bool, width int) (cli.App, *screen) {
	t.Helper()

	stderr := newScreen(t, width)
	app := onTerminals(t, env, stdoutTerminal, true)
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
				app.SetPush(stepping(startedMerge, func() { peek(stderr) }))

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
	var hiddenAgain bool
	app.SetMerge(func(_ context.Context, deps landing.Deps, req landing.Request) result.Result {
		startedMerge(progress(deps))
		time.Sleep(spun)
		synctest.Wait()
		inBackground = stderr.raw()
		foreground.Store(true)
		time.Sleep(cli.FrameEvery)
		synctest.Wait()
		again, hiddenAgain = stderr.lines(), stderr.cursorHidden()

		return finished(req)
	})

	shown(t, app, "merge")

	assert.Check(t, is.Equal(inBackground, ""))
	assert.Check(t, is.Len(again, 1), "stderr showed %q", again)
	assert.Check(t, hiddenAgain, "the cursor showed beside the spinner line")
}

func TestApp_Run_WhenIlsStopsBeingTheForegroundJob_ShowsTheCursorAndDrawsNothingMoreUntilItIsAgain(t *testing.T) {
	t.Parallel()

	app, stderr := onStderrScreen(t, []string{colorTerm}, false, wide)
	var foreground atomic.Bool
	foreground.Store(true)
	app.StderrForeground = foreground.Load
	var before, after string
	var hiddenAgain bool
	app.SetMerge(func(_ context.Context, deps landing.Deps, req landing.Request) result.Result {
		startedMerge(progress(deps))
		time.Sleep(spun)
		synctest.Wait()
		before = stderr.raw()
		foreground.Store(false)
		time.Sleep(spun)
		synctest.Wait()
		after = stderr.raw()
		foreground.Store(true)
		time.Sleep(cli.FrameEvery)
		synctest.Wait()
		hiddenAgain = stderr.cursorHidden()

		return finished(req)
	})

	shown(t, app, "merge")

	assert.Check(t, is.Equal(after, before+ansi.ShowCursor))
	assert.Check(t, hiddenAgain, "the spinner line was not drawn again once ils was the foreground job")
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

func TestApp_Run_WhenColorIsOffOnATerminal_DrawsTheSpinnerWithNoRendition(t *testing.T) {
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
			unstyled(t, "stderr", stderr.raw())
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

			app, terminal := onSharedScreen(t, tt.env)
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
			unstyled(t, "the terminal", terminal.raw())
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
			assert.Check(t, !regexp.MustCompile(`Pushing|Opening|Waiting`).MatchString(stderr.raw()), "stderr = %q, want no spinner label", stderr.raw())
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
