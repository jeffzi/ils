package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/jeffzi/ils/internal/result"
)

// display shows how a landing command advances on the terminals ils prints to: one spinner line
// for the running step on stderr, and each step line on stdout as soon as its step succeeds. A
// stream that gets neither is left untouched. A failed write is not reported: the display never
// changes the result or the exit code, and a step line that could not be written is not printed.
type display struct {
	// stdout and stderr are nil when the stream gets no step line and no spinner. drop also sets
	// stderr to nil, which keeps draw and clear silent once the spinner is gone for good.
	stdout, stderr io.Writer
	width          func() int
	foreground     func() bool
	stop           chan struct{}
	// began is when the running step began: the last Started, or the last Said after it.
	began  time.Time
	gitDir string
	// label is the running step drawn for stderr, and empty while no step runs.
	label string
	// printed are the plain texts of the step lines written on stdout.
	printed                []string
	frame, done, total     int
	wg                     sync.WaitGroup
	mu                     sync.Mutex
	outProfile, errProfile colorprofile.Profile
	// drawn is whether a spinner line stands on stderr.
	drawn bool
}

const (
	// frameEvery is how often the spinner turns and its timer is redrawn.
	frameEvery = 100 * time.Millisecond

	// fallbackWidth is the width in columns of a terminal whose width is unknown.
	fallbackWidth = 80
)

// display returns the display of a landing command run with the words typed, stderr drawn for
// errProfile: the spinner on a terminal stderr unless debug is on, and the step lines on a terminal
// stdout unless the result is JSON.
func (p *parse) display(errProfile colorprofile.Profile) *display {
	a := p.app
	d := &display{
		width:      a.StderrWidth,
		foreground: a.StderrForeground,
		outProfile: p.outProfile,
		errProfile: errProfile,
	}

	if a.terminal(a.StdoutTerminal) && !p.encoded {
		d.stdout = a.Stdout
	}

	if a.terminal(a.StderrTerminal) && !bool(p.debug) {
		d.stderr = a.Stderr
	}

	return d
}

// start turns the spinner, if any, until end or until ctx ends; gitDir is the git dir the lines are
// checked against.
func (d *display) start(ctx context.Context, gitDir string) {
	// A signal may call drop while start runs, so stderr is read under the lock.
	d.mu.Lock()
	d.gitDir = gitDir
	silent := d.stderr == nil
	d.mu.Unlock()

	if silent {
		return
	}

	d.stop = make(chan struct{})
	d.wg.Go(func() {
		ticker := time.NewTicker(frameEvery)
		defer ticker.Stop()

		for {
			select {
			case <-d.stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.mu.Lock()
				d.frame++
				d.draw()
				d.mu.Unlock()
			}
		}
	})
}

// end stops the spinner and takes its line off stderr.
func (d *display) end() {
	if d.stop != nil {
		close(d.stop)
		d.wg.Wait()
	}

	d.drop()
}

// drop takes the spinner line off stderr for good, which shows the cursor again. It is safe to call
// from any goroutine, while the spinner still turns: nothing is drawn on stderr after it.
func (d *display) drop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.clear()
	d.stderr = nil
}

// suspend takes the spinner line off stderr, if one stands, and stops the process through stop, both
// under mu so that no frame draws the line between the two: the first frame after the process
// continues draws it again. It returns the error of stop.
func (d *display) suspend(stop func() error) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.clear()

	return stop()
}

// Started draws the spinner line of the step label names, timed from now.
func (d *display) Started(label result.Phrase) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// A label the wording check rejects is not drawn; the landing tests keep every label in it.
	d.label, _ = renderLine(label, d.gitDir, d.errProfile)
	d.done, d.total, d.began = 0, 0, time.Now()
	d.draw()
}

// Said takes the spinner line off stderr and prints line on stdout as the running step's line,
// with the time since the last Started or Said, whichever came later, rounded to frameEvery; a step
// that took less than frameEvery prints no time.
func (d *display) Said(line result.Phrase) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.clear()
	d.label = ""

	// A line said without a step started for it is timed from the line before it.
	now := time.Now()
	took := now.Sub(d.began)
	d.began = now

	if d.stdout == nil {
		return
	}

	text, err := renderLine(line, d.gitDir, d.outProfile)
	if err != nil {
		return
	}

	pen := result.PenFor(d.outProfile)
	green := result.GreenStyle()
	step := pen.Styled(green, "✓") + " " + text
	if took >= frameEvery {
		step += " " + pen.Styled(green.Faint(), took.Round(frameEvery).String())
	}
	step += "\n"

	if _, err := io.WriteString(d.stdout, step); err == nil {
		d.printed = append(d.printed, plainText(line, d.gitDir))
	}
}

// Counted redraws the spinner line with done of total checks concluded.
func (d *display) Counted(done, total int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.done, d.total = done, total
	d.draw()
}

// draw writes the spinner line of the running step over the one stderr shows, cut to one column
// short of the terminal's width so that it never wraps, then erases the rest of the row so that
// nothing of the line before is left. The caller holds mu.
//
// It never pads the row with spaces: a terminal keeps written spaces as part of the row, so a
// shorter line printed over it later would keep them, and they would wrap into a blank row once
// the terminal narrows. The first draw of a line hides the cursor, whatever the profile, so that it
// does not blink after the timer, and clear shows it again. Besides its colors, the spinner writes
// no control sequence other than the carriage return, those two, and the erasures of draw and
// clear. While ils is not the terminal's foreground job, it draws nothing and clears the line that
// stands.
func (d *display) draw() {
	if d.stderr == nil || d.label == "" || !d.inForeground() {
		d.clear()

		return
	}

	pen := result.PenFor(d.errProfile)
	yellow := result.YellowStyle()
	frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

	line := pen.Styled(yellow, string(frames[d.frame%len(frames)])) + " " + d.label + " " +
		pen.Styled(yellow, time.Since(d.began).Truncate(time.Second).String())
	if d.total > 0 {
		line += fmt.Sprintf(", %d of %d done", d.done, d.total)
	}

	line = ansi.Truncate(line, d.columns(), "")
	if pen.Color() {
		line += ansi.ResetStyle
	}

	start := "\r"
	if !d.drawn {
		start = ansi.HideCursor + start
	}

	_, _ = io.WriteString(d.stderr, start+line+ansi.EraseLineRight)
	d.drawn = true
}

// clear takes the spinner line off stderr by erasing its row, never by writing spaces over it, and
// shows the cursor that draw hid, at the start of the row. The caller holds mu.
//
// While ils is not the terminal's foreground job, it only shows the cursor and leaves the line
// standing: the cursor then stands on the line of the shell, which the erasure would wipe.
func (d *display) clear() {
	if !d.drawn {
		return
	}

	wipe := "\r" + ansi.EraseEntireLine
	if !d.inForeground() {
		wipe = ""
	}

	_, _ = io.WriteString(d.stderr, wipe+ansi.ShowCursor)
	d.drawn = false
}

// inForeground reports whether ils is the foreground job of the terminal stderr is; with no query
// of it, ils is.
func (d *display) inForeground() bool {
	return d.foreground == nil || d.foreground()
}

// columns returns how many columns the spinner line may take: one short of the terminal's width,
// read now, so that the line never fills its row and wraps; an unknown width counts as
// fallbackWidth.
func (d *display) columns() int {
	width := fallbackWidth
	if d.width != nil {
		if w := d.width(); w > 0 {
			width = w
		}
	}

	return width - 1
}

// renderLine returns line drawn for profile as the result renderer draws it, or the renderer's error.
func renderLine(line result.Phrase, gitDir string, profile colorprofile.Profile) (string, error) {
	out, err := result.RenderStyled(result.Result{Lines: []result.Phrase{line}, Next: result.NextNone()}, gitDir, profile)
	if err != nil {
		return "", fmt.Errorf("render the line of a step: %w", err)
	}

	return strings.TrimSuffix(out, "\n"), nil
}

// plainText returns the text of line as the text renderer prints it, and "" when it rejects it.
func plainText(line result.Phrase, gitDir string) string {
	out, _ := renderLine(line, gitDir, colorprofile.NoTTY)

	return out
}
