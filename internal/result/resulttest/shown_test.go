package resulttest_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/failtest"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

func TestAssertCarried(t *testing.T) {
	t.Parallel()
	const reason, next = "the branch is the trunk", "switch to a branch"
	carrier := resulttest.Shown{
		Excerpt: "first line of the log\nsecond line of the log",
		Lines:   []string{"Pushed the branch to origin."},
		Reason:  reason,
		Next:    next,
	}
	tests := []struct {
		name     string
		outcome  string
		text     string
		wantFail bool
	}{
		{name: "a line of the lines", outcome: "refused", text: "Pushed the branch to origin.\n"},
		{name: "each line of the excerpt", outcome: "refused", text: "first line of the log\nsecond line of the log\n"},
		{name: "a refusal behind its label", outcome: "refused", text: resulttest.ReasonLine(result.Refused, reason) + "\n"},
		{name: "the next behind its label", outcome: "refused", text: resulttest.NextLine(next) + "\n"},
		{
			name: "a line the object lacks", outcome: "refused",
			text: "Pushed the branch to origin.\nOpened the pull request.\n", wantFail: true,
		},
		{name: "the reason without its label", outcome: "refused", text: reason + "\n", wantFail: true},
		{
			name: "the reason behind another outcome's label", outcome: "pending",
			text: resulttest.ReasonLine(result.Refused, reason) + "\n", wantFail: true,
		},
		{
			name: "a done result's reason behind the label of a broke one", outcome: "done",
			text: resulttest.ReasonLine(result.Broke, reason) + "\n", wantFail: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			shown := carrier
			shown.Outcome = tt.outcome
			spy := failtest.New(t)

			resulttest.AssertCarried(spy, tt.text, shown)

			assert.Check(t, is.Equal(spy.Failed(), tt.wantFail), "AssertCarried(%q) of a %s result", tt.text, tt.outcome)
		})
	}
}
