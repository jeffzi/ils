package landing_test

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/landing"
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

// watchEvent is one thing a run told its progress hook.
type watchEvent struct {
	kind   string
	phrase result.Phrase
	limit  time.Duration
	done   int
	total  int
}

// watch is what a run told the progress hook and the logger of its Deps, each in the order told.
type watch struct {
	events  []watchEvent
	records []string
	mu      sync.Mutex
}

// placedResult is a result as both renderers show it, with <scene> where git's messages name the
// directory of the scene it ran on, so that the results of two scenes built alike compare.
type placedResult struct {
	object string
	drawn  string
}

// assertSameUnwatched fails unless unwatched, the result of a run on a scene of its own built alike
// and given no progress hook and no logger, is exactly watched: the same object and the same text
// drawn with color, so that no name changes its kind either.
func assertSameUnwatched(t *testing.T, watched, unwatched placedResult) {
	t.Helper()

	assert.Check(t, is.Equal(unwatched.object, watched.object), "with no progress hook and no logger")
	assert.Check(t, is.Equal(unwatched.drawn, watched.drawn), "with no progress hook and no logger")
}

// unwatchedRun returns, as it shows, the result of `ils <cmd>` given req on the scene and under the
// context build returns, with no progress hook and no logger. The run is a test of its own, so that
// a fake git build puts on PATH comes off with that test's cleanup before the caller's watched run
// puts its own: a fake git takes the git it finds on PATH as the real one. A test in a bubble
// cannot start a test of its own; it runs unwatched on a scene of its own directly instead.
func unwatchedRun(t *testing.T, build func(t *testing.T) (*scene, context.Context), req landing.Request) placedResult {
	t.Helper()

	var bare placedResult

	t.Run("with no progress hook and no logger", func(t *testing.T) {
		s, ctx := build(t)
		s.deps.Progress, s.deps.Logger = nil, nil
		bare = s.placed(t, land(ctx, s.deps, req))
	})

	return bare
}

// land runs the landing command req names: merge for a merge request, and push otherwise.
func land(ctx context.Context, deps landing.Deps, req landing.Request) result.Result {
	if req.Command == landing.MergeCommand {
		return landing.Merge(ctx, deps, req)
	}

	return landing.Push(ctx, deps, req)
}

// placed returns r as it shows, with the directory that holds the scene's repository and its
// origin, as given and with its symbolic links resolved, replaced by <scene>. The scene must still
// be on disk.
func (s *scene) placed(t *testing.T, r result.Result) placedResult {
	t.Helper()

	root := filepath.Dir(s.fixture.Dir())
	resolved, err := filepath.EvalSymlinks(root)
	assert.NilError(t, err)

	place := func(text string) string {
		return strings.ReplaceAll(strings.ReplaceAll(text, resolved, "<scene>"), root, "<scene>")
	}

	object, err := result.RenderJSON(r, "")
	assert.NilError(t, err)

	drawn, err := result.RenderStyled(r, "", colorprofile.ANSI)
	assert.NilError(t, err)

	return placedResult{object: place(object), drawn: place(drawn)}
}

// watchOn gives deps a progress hook and a logger that both tell the watch it returns.
func watchOn(deps *landing.Deps) *watch {
	w := &watch{}
	deps.Progress = w
	deps.Logger = slog.New(watchLog{watch: w})

	return w
}

// Started keeps a step that started, with its limit.
func (w *watch) Started(label result.Phrase, limit time.Duration) {
	w.keep(watchEvent{kind: "start", phrase: label, limit: limit})
}

// Said keeps a step line the run said as its step succeeded.
func (w *watch) Said(line result.Phrase) {
	w.keep(watchEvent{kind: "said", phrase: line})
}

// Counted keeps a count of the checks done.
func (w *watch) Counted(done, total int) {
	w.keep(watchEvent{kind: "count", done: done, total: total})
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
		case "count":
			told = append(told, fmt.Sprintf("count %d of %d", e.done, e.total))
		case "start":
			step := "start " + plainText(t, e.phrase)
			if e.limit > 0 {
				step += " of " + e.limit.String()
			}

			told = append(told, step)
		default:
			told = append(told, "said "+plainText(t, e.phrase))
		}
	}

	return told
}

// logged returns each record the logger was given, in order, as its level, its message, and each
// of its attributes as key=value, apart by spaces.
func (w *watch) logged() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return slices.Clone(w.records)
}

// lines returns the step lines the hook was told the run said, in order.
func (w *watch) lines(t *testing.T) []string {
	t.Helper()

	var lines []string

	for _, e := range w.told(t) {
		if line, ok := strings.CutPrefix(e, "said "); ok {
			lines = append(lines, line)
		}
	}

	return lines
}

// watchLog is a log handler that keeps every record in its watch.
type watchLog struct {
	watch *watch
	group string
	attrs []slog.Attr
}

// Enabled reports that every level is kept.
func (watchLog) Enabled(context.Context, slog.Level) bool { return true }

// Handle keeps r in the watch.
func (h watchLog) Handle(_ context.Context, r slog.Record) error {
	parts := []string{r.Level.String(), r.Message}
	for _, a := range h.attrs {
		parts = append(parts, a.String())
	}

	r.Attrs(func(a slog.Attr) bool {
		parts = append(parts, h.group+a.String())

		return true
	})

	h.watch.mu.Lock()
	defer h.watch.mu.Unlock()

	h.watch.records = append(h.watch.records, strings.Join(parts, " "))

	return nil
}

// WithAttrs returns a handler that adds attrs to every record it keeps.
func (h watchLog) WithAttrs(attrs []slog.Attr) slog.Handler {
	kept := slices.Clone(h.attrs)
	for _, a := range attrs {
		a.Key = h.group + a.Key
		kept = append(kept, a)
	}

	h.attrs = kept

	return h
}

// WithGroup returns a handler that keeps the key of every attribute after it under name.
func (h watchLog) WithGroup(name string) slog.Handler {
	h.group += name + "."

	return h
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
