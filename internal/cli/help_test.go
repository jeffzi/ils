package cli_test

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/landing"
)

// flat returns the words of text in order, one space apart, so that a check reads past the padding
// that help lays its columns out with.
func flat(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func TestApp_Run_WhenAskedForHelp_PrintsTheWaitLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command string
		states  []string
		never   []string
	}{
		{command: "push", states: []string{landing.Span(landing.WaitTimeLimit), landing.Span(landing.TimeLimit)}},
		{
			command: "merge", states: []string{"14 minutes 40 seconds", "80 seconds"},
			never: []string{landing.Span(landing.WaitTimeLimit), landing.Span(landing.TimeLimit)},
		},
		{
			command: "release", states: []string{"14 minutes 40 seconds", "80 seconds"},
			never: []string{landing.Span(landing.WaitTimeLimit), landing.Span(landing.TimeLimit)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), "--help", tt.command)

			assert.Check(t, is.Equal(got.code, exitDone))
			help := flat(got.stdout)
			for _, limit := range tt.states {
				assert.Check(t, is.Contains(help, limit))
			}
			for _, limit := range tt.never {
				assert.Check(t, !strings.Contains(help, limit), "help states %q: %s", limit, help)
			}
		})
	}
}
