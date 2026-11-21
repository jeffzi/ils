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

func TestApp_Run_WhenAskedForHelp_PrintsTheWaitLimitsInTheLongHelp(t *testing.T) {
	t.Parallel()

	never := []string{landing.Span(landing.WaitTimeLimit), landing.Span(landing.TimeLimit)}
	for _, command := range []string{"merge", "release"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), "--help", command)

			assert.Check(t, is.Equal(got.code, exitDone))
			help := flat(got.stdout)
			long, _, found := strings.Cut(help, "Flags:")
			assert.Assert(t, found, "help has no Flags: section: %s", got.stdout)
			for _, limit := range []string{"14 minutes 40 seconds", "80 seconds"} {
				assert.Check(t, is.Contains(long, limit))
			}
			for _, limit := range never {
				assert.Check(t, !strings.Contains(help, limit), "help states %q: %s", limit, got.stdout)
			}
		})
	}
}
