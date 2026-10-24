// Package flat keeps the attributes a slog.Handler is given flat: each key led by the groups open
// when it was added, as `group.key`.
package flat

import (
	"log/slog"
	"slices"
)

// Attrs is what a slog.Handler was given through WithAttrs and WithGroup: the attributes in order,
// each key led by the groups open when it was added, and the groups open now. The zero value holds
// no attribute under no group. Its methods return copies, so a handler that holds one by value
// derives handlers that share nothing with it.
type Attrs struct {
	prefix string
	attrs  []slog.Attr
}

// With returns a copy of a with attrs added under the groups open now.
func (a Attrs) With(attrs []slog.Attr) Attrs {
	a.attrs = slices.Clone(a.attrs)
	for _, attr := range attrs {
		a.attrs = append(a.attrs, a.grouped(attr))
	}

	return a
}

// Group returns a copy of a that adds every later attribute under name, or a itself when name is
// empty, as slog.Handler asks of WithGroup.
func (a Attrs) Group(name string) Attrs {
	if name == "" {
		return a
	}

	a.prefix += name + "."

	return a
}

// Of returns the attributes of a followed by those of record, each of record's under the groups
// open now.
func (a Attrs) Of(record slog.Record) []slog.Attr {
	attrs := slices.Clone(a.attrs)
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, a.grouped(attr))

		return true
	})

	return attrs
}

// grouped returns attr with its key under the groups open now.
func (a Attrs) grouped(attr slog.Attr) slog.Attr {
	attr.Key = a.prefix + attr.Key

	return attr
}
