package cli_test

import (
	"fmt"
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

func TestApp_Run_WhenAskedForHelp_PrintsTheHelpOfTheCommand(t *testing.T) {
	t.Parallel()

	seconds := fmt.Sprintf("%d seconds", int(landing.TimeLimit.Seconds()))
	tests := [][]string{{"--help", "push"}, {"merge", "--help"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), args...)

			assert.Check(t, is.Equal(got.code, exitDone))
			assert.Check(t, is.Contains(flat(got.stdout), seconds))
		})
	}
}
