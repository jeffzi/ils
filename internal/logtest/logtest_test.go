package logtest_test

import (
	"log/slog"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/failtest"
	"github.com/jeffzi/ils/internal/logtest"
)

func TestSink_Records_WhenLoggedAtDebug_HoldsTheRecord(t *testing.T) {
	t.Parallel()
	logger, sink := logtest.New()

	logger.Debug("read", "state", "clean")

	records := sink.Records()
	assert.Assert(t, is.Len(records, 1))
	assert.Check(t, is.Equal(records[0].Message, "read"))
	assert.Check(t, is.Equal(records[0].Level, slog.LevelDebug))
	assert.Check(t, is.DeepEqual(records[0].Map(), map[string]any{"state": "clean"}))
}

func TestRecord_Map_WhenAttributeIsLazy_HoldsItsResolvedValue(t *testing.T) {
	t.Parallel()
	record := logtest.Record{Attrs: []slog.Attr{slog.Any("state", lazy("clean"))}}

	got := record.Map()

	assert.Check(t, is.DeepEqual(got, map[string]any{"state": "clean"}))
}

func TestRecord_Without_WhenKeysNamed_HoldsEveryOtherAttribute(t *testing.T) {
	t.Parallel()
	record := logtest.Record{Attrs: []slog.Attr{slog.String("state", "clean"), slog.Int("took", 3), slog.Int("run", 1)}}

	got := record.Without("took", "run")

	assert.Check(t, is.DeepEqual(got, map[string]any{"state": "clean"}))
}

func TestRecordsOf_WhenLogWritesTwice_ReturnsBothRecordsInOrder(t *testing.T) {
	t.Parallel()

	got := logtest.RecordsOf(func(l *slog.Logger) {
		l.Info("first")
		l.Debug("second")
	})

	assert.Assert(t, is.Len(got, 2))
	assert.Check(t, is.Equal(got[0].Message, "first"))
	assert.Check(t, is.Equal(got[1].Message, "second"))
}

func TestOnly_WhenOneRecord_ReturnsIt(t *testing.T) {
	t.Parallel()
	first := logtest.Record{Message: "first"}
	spy := failtest.New(t)
	var got logtest.Record

	spy.Run(func() { got = logtest.Only(spy, []logtest.Record{first}) })

	assert.Check(t, !spy.Failed(), "Only failed the test on exactly one record")
	assert.Check(t, is.DeepEqual(got, first))
}

func TestOnly_WhenNotExactlyOneRecord_FailsTest(t *testing.T) {
	t.Parallel()
	spy := failtest.New(t)

	spy.Run(func() { logtest.Only(spy, []logtest.Record{{Message: "first"}, {Message: "second"}}) })

	assert.Check(t, spy.Stopped(), "Only did not fail the test on two records")
}

// lazy is a slog.LogValuer, which a record keeps unresolved, so it shows that Record.Map resolves
// each value.
type lazy string

func (l lazy) LogValue() slog.Value { return slog.StringValue(string(l)) }
