//go:build unix

package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/guard"
)

func TestParse_WhenAWrapperRunsACommand_ReportsTheCommandBehindIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		command      string
		wantWords    []string
		wantWrappers []string
	}{
		{name: "nice", command: "nice git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"nice"}},
		{name: "nice with a separate value", command: "nice -n 10 git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"nice"}},
		{name: "nice with a numeric option", command: "nice -10 git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"nice"}},
		{name: "env clearing the environment", command: "env -i git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"env"}},
		{name: "env end of options", command: "env -- git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"env"}},
		{name: "exec with grouped flags", command: "exec -cl git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"exec"}},
		{name: "time keyword", command: "time git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"time"}},
		{name: "command without a lookup flag", command: "command gh pr list", wantWords: []string{"gh", "pr", "list"}, wantWrappers: []string{"command"}},
		{name: "stacked wrappers", command: "env nohup ils push", wantWords: []string{"ils", "push"}, wantWrappers: []string{"env", "nohup"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.DeepEqual(wordTexts(got.Words), tt.wantWords), "Parse(%q) words", tt.command)
			assert.Check(t, is.DeepEqual(got.Wrappers, tt.wantWrappers), "Parse(%q) wrappers", tt.command)
			assert.Check(t, got.Tool != guard.NoTool, "Parse(%q) tool = none, want the wrapped tool", tt.command)
			assert.Check(t, !got.Whole, "Parse(%q) reports a wrapped invocation as the whole command", tt.command)
		})
	}
}

func TestParse_WhenAZshModifierIsGivenAnOption_ReadsTheOptionAsTheCommand(t *testing.T) {
	t.Parallel()

	// zsh's noglob and nocorrect take no options: `noglob -- git push` runs a command named `--`.
	const command = "noglob -- git push"

	got := only(t, command)

	assert.Check(t, is.DeepEqual(wordTexts(got.Words), []string{"--", "git", "push"}), "Parse(%q) words", command)
	assert.Check(t, is.DeepEqual(kinds(got.Marks), []guard.MarkKind{guard.ToolArgument}), "Parse(%q) marks", command)
}
