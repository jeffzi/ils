package resulttest_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result/resulttest"
)

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
		outcome  string
		text     string
		wantFail bool
	}{
		{name: "a line of the lines", outcome: "refused", text: "Pushed the branch to origin.\n"},
		{name: "each line of the excerpt", outcome: "refused", text: "first line of the log\nsecond line of the log\n"},
		{name: "a refusal behind its label", outcome: "refused", text: "Refused: the branch is the trunk\n"},
		{name: "a break behind its label", outcome: "broke", text: "Broke: the branch is the trunk\n"},
		{name: "a pending reason behind its label", outcome: "pending", text: "Pending: the branch is the trunk\n"},
		{name: "an ask reason behind its label", outcome: "ask", text: "Ask: the branch is the trunk\n"},
		{
			name: "a delivered, not green reason behind its label", outcome: "delivered-not-green",
			text: "Delivered, not green: the branch is the trunk\n",
		},
		{name: "the next behind its label", outcome: "refused", text: "Next: switch to a branch\n"},
		{
			name: "a line the object lacks", outcome: "refused",
			text: "Pushed the branch to origin.\nOpened the pull request.\n", wantFail: true,
		},
		{name: "the reason without its label", outcome: "refused", text: "the branch is the trunk\n", wantFail: true},
		{
			name: "the reason behind another outcome's label", outcome: "pending",
			text: "Refused: the branch is the trunk\n", wantFail: true,
		},
		{name: "a done result's reason behind the refused label", outcome: "done", text: "Refused: the branch is the trunk\n", wantFail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			shown := carrier
			shown.Outcome = tt.outcome
			spy := &recordingTB{}

			resulttest.AssertCarried(spy, tt.text, shown)

			assert.Check(t, is.Equal(spy.failed, tt.wantFail), "AssertCarried(%q) of a %s result", tt.text, tt.outcome)
		})
	}
}
