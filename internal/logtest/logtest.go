// Package logtest keeps what a *slog.Logger is given, for tests that check what code logs.
package logtest

import (
	"context"
	"log/slog"
	"slices"
	"sync"

	"github.com/jeffzi/ils/internal/flat"
)

// Record is one record a logger was given: its level, its message, and its attributes in order,
// those added through With first. Each key is led by the groups opened before it, as `group.key`.
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

// handler is the slog.Handler that feeds a Sink, with the attributes and groups added through
// WithAttrs and WithGroup.
type handler struct {
	sink  *Sink
	attrs flat.Attrs
}

func (handler) Enabled(context.Context, slog.Level) bool { return true }

func (h handler) Handle(_ context.Context, record slog.Record) error {
	attrs := h.attrs.Of(record)

	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()

	h.sink.records = append(h.sink.records, Record{Level: record.Level, Message: record.Message, Attrs: attrs})

	return nil
}

func (h handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = h.attrs.With(attrs)

	return h
}

func (h handler) WithGroup(name string) slog.Handler {
	h.attrs = h.attrs.Group(name)

	return h
}
