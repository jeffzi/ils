package flat_test

import (
	"log/slog"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/flat"
)

func TestAttrs_Of(t *testing.T) {
	t.Parallel()
	tests := []struct {
		build func(flat.Attrs) flat.Attrs
		name  string
		want  []string
	}{
		{
			name:  "no attribute added",
			build: func(a flat.Attrs) flat.Attrs { return a },
			want:  []string{"state=clean"},
		},
		{
			name:  "attributes added through With come first",
			build: func(a flat.Attrs) flat.Attrs { return a.With([]slog.Attr{slog.Int("run", 1)}) },
			want:  []string{"run=1", "state=clean"},
		},
		{
			name: "keys after a group are led by its name",
			build: func(a flat.Attrs) flat.Attrs {
				return a.With([]slog.Attr{slog.Int("run", 1)}).Group("wait").With([]slog.Attr{slog.String("stage", "head")})
			},
			want: []string{"run=1", "wait.stage=head", "wait.state=clean"},
		},
		{
			name:  "nested groups lead a key in the order they opened",
			build: func(a flat.Attrs) flat.Attrs { return a.Group("wait").Group("head") },
			want:  []string{"wait.head.state=clean"},
		},
		{
			name:  "a group with no name leads no key",
			build: func(a flat.Attrs) flat.Attrs { return a.Group("") },
			want:  []string{"state=clean"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			record := slog.NewRecord(time.Time{}, slog.LevelInfo, "read", 0)
			record.AddAttrs(slog.String("state", "clean"))

			got := tt.build(flat.Attrs{}).Of(record)

			assert.Check(t, is.DeepEqual(keyValues(got), tt.want))
		})
	}
}

func TestAttrs_With_WhenDerivedTwice_SharesNothing(t *testing.T) {
	t.Parallel()
	base := flat.Attrs{}.With([]slog.Attr{slog.Int("run", 1)})
	record := slog.NewRecord(time.Time{}, slog.LevelInfo, "read", 0)

	first := base.With([]slog.Attr{slog.String("stage", "head")})
	second := base.With([]slog.Attr{slog.String("stage", "checks")})

	assert.Check(t, is.DeepEqual(keyValues(first.Of(record)), []string{"run=1", "stage=head"}))
	assert.Check(t, is.DeepEqual(keyValues(second.Of(record)), []string{"run=1", "stage=checks"}))
}

func keyValues(attrs []slog.Attr) []string {
	out := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		out = append(out, attr.String())
	}

	return out
}
