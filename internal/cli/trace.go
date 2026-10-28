package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/charmbracelet/colorprofile"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/result"
)

// logger returns the logger of the debug trace on Stderr, drawn for profile, when the debug flag is
// on, and nil when it is off.
func (p *parse) logger(profile colorprofile.Profile) *slog.Logger {
	if !p.debug {
		return nil
	}

	a := p.app

	return slog.New(trace{w: a.Stderr, launch: a.Launch, pen: result.PenFor(profile), mu: &sync.Mutex{}})
}

// trace is the log handler of the debug trace: it writes each record as one line on w, timed from
// launch. A git or gh call reads `<tool> <args> exit <code> <duration>`, with the failure in place
// of `exit <code>` for a call that did not exit; any other record reads its message, then each
// attribute as key=value. When styled, the time and the duration are dim.
type trace struct {
	w      io.Writer
	launch time.Time
	// mu keeps the lines of calls logged at once from interleaving.
	mu    *sync.Mutex
	attrs flatAttrs
	pen   result.Pen
}

// Enabled reports that every level is written.
func (trace) Enabled(context.Context, slog.Level) bool {
	return true
}

// Handle writes r as one line, and returns the error of the write.
func (h trace) Handle(_ context.Context, r slog.Record) error {
	attrs := h.attrs.Of(r)

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
// one or its args or duration attribute is not of the type the child package logs.
func (h trace) call(message string, attrs []slog.Attr) (string, bool) {
	if message != child.ExecMessage {
		return "", false
	}

	var (
		tool, end string
		args      []string
		took      time.Duration
	)

	for _, attr := range attrs {
		switch attr.Key {
		case child.KeyTool:
			tool = attr.Value.String()
		case child.KeyArgs:
			var ok bool
			if args, ok = attr.Value.Any().([]string); !ok {
				return "", false
			}
		case child.KeyExit:
			end = "exit " + attr.Value.String()
		case child.KeyError:
			end = attr.Value.String()
		case child.KeyDuration:
			if attr.Value.Kind() != slog.KindDuration {
				return "", false
			}

			took = attr.Value.Duration()
		}
	}

	command := child.CommandLine(child.Argv(tool, args))
	duration := h.dim(took.Round(time.Millisecond).String())

	return command + " " + end + " " + duration, true
}

// dim returns text drawn dim when the trace is styled.
func (h trace) dim(text string) string {
	return h.pen.Styled(result.FaintStyle(), text)
}

// WithAttrs returns a handler that writes attrs on every line after the record's own message.
func (h trace) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = h.attrs.With(attrs)

	return h
}

// WithGroup returns a handler that writes the key of every attribute after it under name.
func (h trace) WithGroup(name string) slog.Handler {
	h.attrs = h.attrs.Group(name)

	return h
}

// flatAttrs is what a slog.Handler was given through WithAttrs and WithGroup, kept flat: the
// attributes in order, each key led by the groups open when it was added, as `group.key`, and the
// groups open now. The zero value holds no attribute under no group. Its methods return copies, so a
// handler that holds one by value derives handlers that share nothing with it.
type flatAttrs struct {
	prefix string
	attrs  []slog.Attr
}

// With returns a copy of a with attrs added under the groups open now.
func (a flatAttrs) With(attrs []slog.Attr) flatAttrs {
	a.attrs = slices.Clone(a.attrs)
	for _, attr := range attrs {
		a.attrs = append(a.attrs, a.grouped(attr))
	}

	return a
}

// Group returns a copy of a that adds every later attribute under name, or a itself when name is
// empty, as slog.Handler asks of WithGroup.
func (a flatAttrs) Group(name string) flatAttrs {
	if name == "" {
		return a
	}

	a.prefix += name + "."

	return a
}

// Of returns the attributes of a followed by those of record, each of record's under the groups
// open now.
func (a flatAttrs) Of(record slog.Record) []slog.Attr {
	attrs := slices.Clone(a.attrs)
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, a.grouped(attr))

		return true
	})

	return attrs
}

// grouped returns attr with its key under the groups open now.
func (a flatAttrs) grouped(attr slog.Attr) slog.Attr {
	attr.Key = a.prefix + attr.Key

	return attr
}
