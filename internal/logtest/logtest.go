// Package logtest keeps what a *slog.Logger is given, for tests that check what code logs.
package logtest

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"

	"github.com/jeffzi/ils/internal/child"
)

// Record is one record a logger was given: its level, its message, and its attributes in order.
type Record struct {
	Message string
	Attrs   []slog.Attr
	Level   slog.Level
}

// Map returns the attributes of r by key, each as its resolved value.
func (r Record) Map() map[string]any {
	attrs := make(map[string]any, len(r.Attrs))
	for _, attr := range r.Attrs {
		attrs[attr.Key] = attr.Value.Resolve().Any()
	}

	return attrs
}

// Without returns the attributes of r by key, as Map does, all but those named by keys.
func (r Record) Without(keys ...string) map[string]any {
	attrs := r.Map()
	for _, key := range keys {
		delete(attrs, key)
	}

	return attrs
}

// RecordsOf returns every record log gives the logger it is handed, in order.
func RecordsOf(log func(*slog.Logger)) []Record {
	logger, sink := New()
	log(logger)

	return sink.Records()
}

// Only returns the one record of records, and fails t when there is not exactly one.
func Only(t testing.TB, records []Record) Record {
	t.Helper()

	if len(records) != 1 {
		t.Fatalf("got %d records, want exactly one: %+v", len(records), records)

		return Record{}
	}

	return records[0]
}

// ExecAttrs returns the attributes, all but the duration, of the one record run logs, and fails t
// when run logs other than one record.
func ExecAttrs(t testing.TB, run child.Exec) map[string]any {
	t.Helper()

	records := RecordsOf(func(logger *slog.Logger) { run.Log(t.Context(), logger) })

	return Only(t, records).Without(child.KeyDuration)
}

// Sink keeps every record the logger built with it is given, at any level, safe for concurrent use.
type Sink struct {
	records []Record
	mu      sync.Mutex
}

// New returns a logger that sends every record, at any level, to the sink it returns.
func New() (*slog.Logger, *Sink) {
	sink := &Sink{}

	return slog.New(handler{sink: sink}), sink
}

// Records returns every record the sink was given, in order.
func (s *Sink) Records() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.records)
}

// handler is the slog.Handler that feeds a Sink. It takes no attributes or groups through
// WithAttrs and WithGroup: no logger under test derives one, so both panic rather than drop what
// they were given.
type handler struct {
	sink *Sink
}

func (handler) Enabled(context.Context, slog.Level) bool { return true }

func (h handler) Handle(_ context.Context, record slog.Record) error {
	attrs := make([]slog.Attr, 0, record.NumAttrs())
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, attr)

		return true
	})

	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()

	h.sink.records = append(h.sink.records, Record{Level: record.Level, Message: record.Message, Attrs: attrs})

	return nil
}

func (handler) WithAttrs([]slog.Attr) slog.Handler {
	panic("logtest: WithAttrs is not supported")
}

func (handler) WithGroup(string) slog.Handler {
	panic("logtest: WithGroup is not supported")
}
