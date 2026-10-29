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

// The steps a run starts, as told returns them: the kind of event, the word that names the step,
// and what the step is about.

func checkingStep() string {
	return expect("start", "Checking")
}

func pushingStep() string {
	return expect("start", "Pushing", topic)
}

func openingStep() string {
	return expect("start", "Opening", trunk)
}

func findingStep() string {
	return expect("start", "Finding")
}

func mergingStep() string {
	return expect("start", "Merging", "#12")
}

func deletingStep() string {
	return expect("start", "Deleting", topic)
}

func movingStep() string {
	return expect("start", "Moving", trunk)
}

// mergeStateStep is the step of the wait for the merge state, which names the merge window.
func mergeStateStep() string {
	return expect("start", landing.Seconds(landing.MergeWindow), "#12", "can merge")
}

// eventKind is what a run told its progress hook.
type eventKind int

const (
	eventStart eventKind = iota
	eventSaid
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

func (w *watch) Started(label result.Phrase, limit time.Duration) {
	w.keep(watchEvent{kind: eventStart, phrase: label, limit: limit})
}

func (w *watch) Said(line result.Phrase) {
	w.keep(watchEvent{kind: eventSaid, phrase: line})
}

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

// said is the event of a step that succeeded saying line, a line of a result's text. The event
// holds the line without the newline that ends it in the text.
func said(line string) string {
	if trimmed, ok := strings.CutSuffix(line, "\n"+partEnd); ok {
		line = trimmed + partEnd
	}

	return expect("said") + line
}

// assertTold fails unless got holds one event for each of want, each holding the parts of its want.
func assertTold(t *testing.T, got, want []string) {
	t.Helper()

	matched := len(got) == len(want)
	for i := 0; matched && i < len(got); i++ {
		matched = holds(got[i], want[i])
	}

	wanted := make([][]string, 0, len(want))
	for _, event := range want {
		wanted = append(wanted, partsOf(event))
	}

	assert.Check(t, matched, "told %q, want one event for each of %q", got, wanted)
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
	return expect("start", "checks", "#12", "of "+left.String())
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
