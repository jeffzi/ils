package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// logger returns the logger of the debug trace on Stderr when the debug flag is on, and nil when
// it is off.
func (p *parse) logger() *slog.Logger {
	if !p.debug {
		return nil
	}

	a := p.app
	styled := a.profile(a.StderrTerminal, p.noColor) >= colorprofile.ANSI

	return slog.New(trace{w: a.Stderr, launch: a.Launch, styled: styled, mu: &sync.Mutex{}})
}

// trace is the log handler of the debug trace: it writes each record as one line on w, timed from
// launch. A git or gh call reads `<tool> <args> exit <code> <duration>`, with the failure in place
// of `exit <code>` for a call that did not exit; any other record reads its message, then each
// attribute as key=value. When styled, the time and the duration are dim.
type trace struct {
	w      io.Writer
	launch time.Time
	// mu keeps the lines of calls logged at once from interleaving.
	mu     *sync.Mutex
	group  string
	attrs  []slog.Attr
	styled bool
}

// Enabled reports that every level is written.
func (trace) Enabled(context.Context, slog.Level) bool {
	return true
}

// Handle writes r as one line, and returns the error of the write.
func (h trace) Handle(_ context.Context, r slog.Record) error {
	attrs := slices.Clone(h.attrs)
	r.Attrs(func(attr slog.Attr) bool {
		attr.Key = h.group + attr.Key
		attrs = append(attrs, attr)

		return true
	})

	body := r.Message
	if call, ok := h.call(r.Message, attrs); ok {
		body = call
	} else {
		for _, attr := range attrs {
			body += " " + attr.Key + "=" + attr.Value.String()
		}
	}

	line := h.dim(fmt.Sprintf("+%.3fs", r.Time.Sub(h.launch).Seconds())) + " " + body + "\n"

	h.mu.Lock()
	defer h.mu.Unlock()

	if _, err := io.WriteString(h.w, line); err != nil {
		return fmt.Errorf("write a debug line: %w", err)
	}

	return nil
}

// call returns the line body of a record of one git or gh call, and false when the record is not
// one.
func (h trace) call(message string, attrs []slog.Attr) (string, bool) {
	if message != "exec" {
		return "", false
	}

	var (
		tool, end string
		args      []string
		took      time.Duration
	)

	for _, attr := range attrs {
		switch attr.Key {
		case "tool":
			tool = attr.Value.String()
		case "args":
			args, _ = attr.Value.Any().([]string)
		case "exit":
			end = "exit " + attr.Value.String()
		case "error":
			end = attr.Value.String()
		case "duration":
			took, _ = attr.Value.Any().(time.Duration)
		}
	}

	return strings.Join(append([]string{tool}, args...), " ") + " " + end + " " + h.dim(took.Round(time.Millisecond).String()), true
}

// dim returns text drawn dim when the trace is styled.
func (h trace) dim(text string) string {
	return paint(h.styled, ansi.NewStyle().Faint(), text)
}

// WithAttrs returns a handler that writes attrs on every line after the record's own message.
func (h trace) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = slices.Clone(h.attrs)
	for _, attr := range attrs {
		attr.Key = h.group + attr.Key
		h.attrs = append(h.attrs, attr)
	}

	return h
}

// WithGroup returns a handler that writes the key of every attribute after it under name.
func (h trace) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	h.group += name + "."

	return h
}
