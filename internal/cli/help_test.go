package cli_test

import (
	"fmt"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

// flat returns the words of text in order, one space apart, so that a check reads past the padding
// that help lays its columns out with.
func flat(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// shownHelp is what the help of one command shows: the usage line that tells it from the help of
// the other commands, and the bound it states (empty when it states none).
type shownHelp struct {
	usage string
	bound string
}

// helpOf returns the help of push and of merge.
func helpOf() (push, merge shownHelp) {
	seconds := fmt.Sprintf("%d seconds", int(bound.Seconds()))
	push = shownHelp{usage: "Usage: ils push", bound: seconds}
	merge = shownHelp{usage: "Usage: ils merge", bound: seconds}

	return push, merge
}

func TestApp_Run_WhenAskedForHelp_PrintsTheHelpOfTheCommand(t *testing.T) {
	t.Parallel()

	push, merge := helpOf()
	tests := []struct {
		want shownHelp
		args []string
	}{
		{args: []string{"--help", "push"}, want: push},
		{args: []string{"merge", "--help"}, want: merge},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			assert.Check(t, is.Equal(got.code, exitDone))
			help := flat(got.stdout)
			assert.Check(t, is.Contains(help, tt.want.usage))
			assert.Check(t, is.Contains(help, tt.want.bound))
		})
	}
}
