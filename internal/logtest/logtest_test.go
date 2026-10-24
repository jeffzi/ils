package logtest_test

import (
	"log/slog"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/logtest"
)

func TestSink_Records(t *testing.T) {
	t.Parallel()
	tests := []struct {
		log   func(*slog.Logger)
		name  string
		want  []string
		level slog.Level
	}{
		{
			name: "record at debug level",
			log:  func(l *slog.Logger) { l.Debug("read", "state", "clean") },
			want: []string{"state=clean"}, level: slog.LevelDebug,
		},
		{
			name: "attributes added through With come first",
			log:  func(l *slog.Logger) { l.With("run", 1).Info("read", "state", "clean") },
			want: []string{"run=1", "state=clean"}, level: slog.LevelInfo,
		},
		{
			name: "keys after a group are led by its name",
			log: func(l *slog.Logger) {
				l.With("run", 1).WithGroup("wait").With("stage", "head").Info("read", "state", "clean")
			},
			want: []string{"run=1", "wait.stage=head", "wait.state=clean"}, level: slog.LevelInfo,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger, sink := logtest.New()

			tt.log(logger)

			records := sink.Records()
			assert.Assert(t, is.Len(records, 1))
			attrs := make([]string, 0, len(records[0].Attrs))
			for _, attr := range records[0].Attrs {
				attrs = append(attrs, attr.String())
			}
			assert.Check(t, is.Equal(records[0].Message, "read"))
			assert.Check(t, is.Equal(records[0].Level, tt.level))
			assert.Check(t, is.DeepEqual(attrs, tt.want))
		})
	}
}

func TestRecord_Map_WhenAttributeIsLazy_HoldsItsResolvedValue(t *testing.T) {
	t.Parallel()
	logger, sink := logtest.New()

	logger.Info("read", "state", lazy("clean"))

	records := sink.Records()
	assert.Assert(t, is.Len(records, 1))
	assert.Check(t, is.DeepEqual(records[0].Map(), map[string]any{"state": "clean"}))
}

// lazy is a slog.LogValuer, which a record keeps unresolved, so it shows that Record.Map resolves
// each value.
type lazy string

func (l lazy) LogValue() slog.Value { return slog.StringValue(string(l)) }
