package resulttest_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/failtest"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

func TestDraw_WhenForegroundCode_DrawsItsColor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code string
		want resulttest.Look
	}{
		{name: "first color", code: "30", want: resulttest.Look{Color: "black"}},
		{name: "last color", code: "37", want: resulttest.Look{Color: "white"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := resulttest.Draw(t, "\x1b["+tt.code+"mx").LookOf(t, "x", "x")

			assert.Check(t, is.Equal(got, tt.want), "code %s", tt.code)
		})
	}
}

func TestDraw_WhenRenditionParameterUnknown_FailsTest(t *testing.T) {
	t.Parallel()
	const out = "\x1b[1;7mx"
	spy := failtest.New(t)

	spy.Run(func() { resulttest.Draw(spy, out) })

	assert.Check(t, spy.Failed(), "Draw(%q) did not fail the test", out)
}

func TestDraw_WhenRenditionParameterEmpty_Resets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		out  string
		want resulttest.Look
	}{
		{name: "leading empty parameter", out: "\x1b[31m\x1b[;1mx", want: resulttest.Look{Bold: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := resulttest.Draw(t, tt.out).LookOf(t, "x", "x")

			assert.Check(t, is.Equal(got, tt.want), "Draw(%q)", tt.out)
		})
	}
}
