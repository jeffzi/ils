package resulttest_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result/resulttest"
)

// spyTB records failures reported through it instead of failing the real test.
type spyTB struct {
	testing.TB

	failed bool
}

func (s *spyTB) Log(...any) {}

func (s *spyTB) Fail() { s.failed = true }

func TestDecode_WhenOutIsOneObject_ReturnsItsFields(t *testing.T) {
	t.Parallel()
	out := `{"command":"push","words":["push"],"outcome":"refused","exit_code":2,` +
		`"reason":"the branch is the trunk","lines":["Nothing was pushed."],"next":"switch to a branch"}`

	got := resulttest.Decode(t, out)

	want := resulttest.Shown{
		Command: "push", Words: []string{"push"}, Outcome: "refused", ExitCode: 2,
		Reason: "the branch is the trunk", Lines: []string{"Nothing was pushed."}, Next: "switch to a branch",
	}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestAssertCarried(t *testing.T) {
	t.Parallel()
	carrier := resulttest.Shown{
		Excerpt: "first line of the log\nsecond line of the log",
		Lines:   []string{"Pushed the branch to origin."},
		Reason:  "the branch is the trunk",
		Next:    "switch to a branch",
	}
	tests := []struct {
		name     string
		text     string
		wantFail bool
	}{
		{name: "a line of the lines", text: "Pushed the branch to origin.\n"},
		{name: "each line of the excerpt", text: "first line of the log\nsecond line of the log\n"},
		{name: "the reason behind its label", text: "Refused: the branch is the trunk\n"},
		{name: "the next behind its label", text: "Next: switch to a branch\n"},
		{name: "a line the object lacks", text: "Pushed the branch to origin.\nOpened the pull request.\n", wantFail: true},
		{name: "the reason without its label", text: "the branch is the trunk\n", wantFail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spy := &spyTB{TB: t}

			resulttest.AssertCarried(spy, tt.text, carrier)

			assert.Check(t, is.Equal(spy.failed, tt.wantFail), "AssertCarried(%q)", tt.text)
		})
	}
}
