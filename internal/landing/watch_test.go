package landing_test

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/logtest"
	"github.com/jeffzi/ils/internal/result"
)

const (
	checkingStep = "start Checking the branch and origin"
	pushingStep  = "start Pushing " + topic
	openingStep  = "start Opening a pull request against " + trunk
	findingStep  = "start Finding the pull request"
	mergingStep  = "start Merging #12"
	deletingStep = "start Deleting " + topic + " on origin"
	movingStep   = "start Moving to origin's tip of " + trunk
	// mergeStateStep is the step of the wait for the merge state, with its 30-second limit.
	mergeStateStep = "start Waiting for GitHub to say whether #12 can merge of 30s"
)

// eventKind is what a run told its progress hook.
type eventKind int

const (
	// eventStart is a step that started, with its limit.
	eventStart eventKind = iota
	// eventSaid is a step line the run said as its step succeeded.
	eventSaid
	// eventCount is a count of the checks done.
	eventCount
)

// watchEvent is one thing a run told its progress hook.
type watchEvent struct {
	phrase result.Phrase
	kind   eventKind
	limit  time.Duration
	done   int
	total  int
}

// watch is what a run told the progress hook and the logger of its Deps, each in the order told.
type watch struct {
	log    *logtest.Sink
	events []watchEvent
	mu     sync.Mutex
}

// watchOn gives deps a progress hook and a logger that both tell the watch it returns.
func watchOn(deps *landing.Deps) *watch {
	logger, sink := logtest.New()
	w := &watch{log: sink}
	deps.Progress = w
	deps.Logger = logger

	return w
}

// Started keeps a step that started, with its limit.
func (w *watch) Started(label result.Phrase, limit time.Duration) {
	w.keep(watchEvent{kind: eventStart, phrase: label, limit: limit})
}

// Said keeps a step line the run said as its step succeeded.
func (w *watch) Said(line result.Phrase) {
	w.keep(watchEvent{kind: eventSaid, phrase: line})
}

// Counted keeps a count of the checks done.
func (w *watch) Counted(done, total int) {
	w.keep(watchEvent{kind: eventCount, done: done, total: total})
}

func (w *watch) keep(e watchEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.events = append(w.events, e)
}

// told returns each event the hook was told, in order, as `start <label>` (with ` of <limit>` for a
// step with a limit), `said <line>`, or `count <done> of <total>`.
func (w *watch) told(t *testing.T) []string {
	t.Helper()

	w.mu.Lock()
	defer w.mu.Unlock()

	told := make([]string, 0, len(w.events))
	for _, e := range w.events {
		switch e.kind {
		case eventCount:
			told = append(told, fmt.Sprintf("count %d of %d", e.done, e.total))
		case eventStart:
			step := "start " + plainText(t, e.phrase)
			if e.limit > 0 {
				step += " of " + e.limit.String()
			}

			told = append(told, step)
		case eventSaid:
			told = append(told, "said "+plainText(t, e.phrase))
		}
	}

	return told
}

// logged returns each record the logger was given, in order, as its level, its message, and each
// of its attributes as key=value, apart by spaces.
func (w *watch) logged() []string {
	records := w.log.Records()
	logged := make([]string, 0, len(records))

	for _, record := range records {
		parts := []string{record.Level.String(), record.Message}
		for _, attr := range record.Attrs {
			parts = append(parts, attr.String())
		}

		logged = append(logged, strings.Join(parts, " "))
	}

	return logged
}

// lines returns the step lines the hook was told the run said, in order.
func (w *watch) lines(t *testing.T) []string {
	t.Helper()

	w.mu.Lock()
	defer w.mu.Unlock()

	var lines []string

	for _, e := range w.events {
		if e.kind == eventSaid {
			lines = append(lines, plainText(t, e.phrase))
		}
	}

	return lines
}

// plainText returns the text of phrase on its own, as a line of a result prints it.
func plainText(t *testing.T, phrase result.Phrase) string {
	t.Helper()

	text, err := result.RenderText(result.Result{
		Command: landing.PushCommand,
		Outcome: result.Done,
		Lines:   []result.Phrase{phrase},
		Next:    result.NextNone(),
	}, "")
	assert.NilError(t, err)

	return strings.TrimSuffix(text, "\n")
}

// said is the event of a step that succeeded saying line, a line of a result's text.
func said(line string) string {
	return "said " + strings.TrimSuffix(line, "\n")
}

// markAll returns events with the sha tip in place of tipMark in each.
func markAll(events []string, tip string) []string {
	marked := make([]string, 0, len(events))
	for _, event := range events {
		marked = append(marked, mark(event, tip))
	}

	return marked
}

// waitingStep is the step of the wait for the checks on pull request 12, limited to left.
func waitingStep(left time.Duration) string {
	return "start Waiting for checks on #12 of " + left.String()
}

// assertHeld fails unless each step line the run said is a line of got, a line of its own for
// each, and unless every record the run logged is a debug record of the wait.
func assertHeld(t *testing.T, w *watch, got result.Result) {
	t.Helper()

	lines := make([]string, 0, len(got.Lines))
	for _, line := range got.Lines {
		lines = append(lines, plainText(t, line))
	}

	for _, line := range w.lines(t) {
		i := slices.Index(lines, line)
		assert.Check(t, i >= 0, "said %q, want it among the lines of the result left unmatched: %q", line, lines)

		if i >= 0 {
			lines = slices.Delete(lines, i, i+1)
		}
	}

	for _, record := range w.logged() {
		assert.Check(t, record == "DEBUG wait" || strings.HasPrefix(record, "DEBUG wait "),
			"logged %q, want only debug records of the wait", record)
	}
}
