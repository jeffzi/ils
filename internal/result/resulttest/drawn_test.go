package resulttest_test

import (
	"runtime"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result/resulttest"
)

// stoppingTB records a failure reported through it and stops the goroutine that reported it when
// the failure is fatal, as a test does. Calls it does not override reach the nil testing.TB and
// panic.
type stoppingTB struct {
	testing.TB

	failed bool
}

func (*stoppingTB) Helper() {}

func (*stoppingTB) Log(...any) {}

func (s *stoppingTB) FailNow() {
	s.failed = true
	runtime.Goexit()
}

func (s *stoppingTB) Fatalf(string, ...any) { s.FailNow() }

// drawFails runs Draw in a goroutine of its own, because a fatal failure ends the goroutine that
// reports it; the caller survives to read tb.failed.
func drawFails(out string) bool {
	tb := &stoppingTB{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		resulttest.Draw(tb, out)
	}()
	<-done
	return tb.failed
}

func TestDraw_WhenForegroundCode_DrawsItsPaletteColor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		code    string
		palette string
	}{
		{name: "first color", code: "30", palette: "0"},
		{name: "last color", code: "37", palette: "7"},
		{name: "first bright color", code: "90", palette: "8"},
		{name: "last bright color", code: "97", palette: "15"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := resulttest.Draw(t, "\x1b["+tt.code+"mx").LookOf(t, "x", "x")

			want := resulttest.Draw(t, "\x1b[38;5;"+tt.palette+"mx").LookOf(t, "x", "x")
			assert.Check(t, is.Equal(got, want), "code %s", tt.code)
		})
	}
}

func TestDraw_WhenHyperlinkWellFormed_DrawsItsLink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		out  string
		want string
	}{
		{name: "link start", out: "\x1b]8;;https://example.com\x07x", want: "https://example.com"},
		{name: "link end", out: "\x1b]8;;https://example.com\x07y\x1b]8;;\x07x", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := resulttest.Draw(t, tt.out).LookOf(t, "x", "x")

			assert.Check(t, is.Equal(got.Link, tt.want), "Draw(%q)", tt.out)
		})
	}
}

func TestDraw_WhenSequenceUnknown_FailsTest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		out  string
	}{
		{name: "italic", out: "\x1b[3mx"},
		{name: "slow blink", out: "\x1b[5mx"},
		{name: "rapid blink", out: "\x1b[6mx"},
		{name: "reverse", out: "\x1b[7mx"},
		{name: "conceal", out: "\x1b[8mx"},
		{name: "crossed out", out: "\x1b[9mx"},
		{name: "primary font", out: "\x1b[10mx"},
		{name: "last alternative font below the colors", out: "\x1b[15mx"},
		{name: "unknown code beside a known one", out: "\x1b[1;7mx"},
		{name: "hyperlink without its parameters field", out: "\x1b]8;https://example.com\x07x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			failed := drawFails(tt.out)

			assert.Check(t, failed, "Draw(%q) did not fail the test", tt.out)
		})
	}
}
