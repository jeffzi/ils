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
	// Three attributes appended one by one leave the slice with spare capacity, so a With that
	// stopped copying would let the second derivation write over the first one's last attribute.
	base := flat.Attrs{}.With([]slog.Attr{slog.Int("run", 1), slog.Int("try", 2), slog.String("branch", "topic")})
	record := slog.NewRecord(time.Time{}, slog.LevelInfo, "read", 0)

	first := base.With([]slog.Attr{slog.String("stage", "head")})
	_ = base.With([]slog.Attr{slog.String("stage", "checks")})

	assert.Check(t, is.DeepEqual(keyValues(first.Of(record)), []string{"run=1", "try=2", "branch=topic", "stage=head"}))
}

func keyValues(attrs []slog.Attr) []string {
	out := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		out = append(out, attr.String())
	}

	return out
}
