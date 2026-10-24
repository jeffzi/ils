package resulttest_test

import (
	"runtime"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result/resulttest"
)

// recordingTB records a failure reported through it instead of failing the real test. A failure
// that is not fatal is recorded and the caller goes on; a fatal one also stops the goroutine that
// reported it, as a test does. Calls it does not override reach the nil testing.TB and panic.
type recordingTB struct {
	testing.TB

	failed bool
}

func (*recordingTB) Helper() {}

func (*recordingTB) Log(...any) {}

func (s *recordingTB) Fail() { s.failed = true }

func (s *recordingTB) FailNow() {
	s.failed = true
	runtime.Goexit()
}

func (s *recordingTB) Fatalf(string, ...any) { s.FailNow() }

// drawFails runs Draw in a goroutine of its own, because a fatal failure ends the goroutine that
// reports it; the caller survives to read tb.failed.
func drawFails(out string) bool {
	tb := &recordingTB{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		resulttest.Draw(tb, out)
	}()
	<-done
	return tb.failed
}

func TestDraw_WhenForegroundCode_DrawsItsColor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code string
		want resulttest.Look
	}{
		{name: "first color", code: "30", want: resulttest.Look{Color: "black"}},
		{name: "last color", code: "37", want: resulttest.Look{Color: "white"}},
		{name: "first bright color", code: "90", want: resulttest.Look{Color: "black"}},
		{name: "last bright color", code: "97", want: resulttest.Look{Color: "white"}},
		{name: "first palette color", code: "38;5;0", want: resulttest.Look{Color: "black"}},
		{name: "last palette color", code: "38;5;7", want: resulttest.Look{Color: "white"}},
		{name: "first bright palette color", code: "38;5;8", want: resulttest.Look{Color: "black"}},
		{name: "last bright palette color", code: "38;5;15", want: resulttest.Look{Color: "white"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := resulttest.Draw(t, "\x1b["+tt.code+"mx").LookOf(t, "x", "x")

			assert.Check(t, is.Equal(got, tt.want), "code %s", tt.code)
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
