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
		{name: "env removing a name", command: "env -u GIT_DIR git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"env"}},
		{name: "env end of options", command: "env -- git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"env"}},
		{name: "exec with a value", command: "exec -a name git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"exec"}},
		{name: "exec with grouped flags", command: "exec -cl git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"exec"}},
		{name: "command with a default path", command: "command -p git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"command"}},
		{name: "time with posix format", command: "time -p git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"time"}},
		{name: "nohup end of options", command: "nohup -- git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"nohup"}},
		{name: "time keyword", command: "time git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"time"}},
		{name: "command without a lookup flag", command: "command gh pr list", wantWords: []string{"gh", "pr", "list"}, wantWrappers: []string{"command"}},
		{name: "stacked wrappers", command: "env nohup ils push", wantWords: []string{"ils", "push"}, wantWrappers: []string{"env", "nohup"}},
		{name: "zsh noglob", command: "noglob git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"noglob"}},
		{name: "zsh nocorrect", command: "nocorrect git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"nocorrect"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.DeepEqual(texts([]guard.Invocation{got})[0], tt.wantWords), "Parse(%q) words", tt.command)
			assert.Check(t, is.DeepEqual(got.Wrappers, tt.wantWrappers), "Parse(%q) wrappers", tt.command)
			assert.Check(t, got.Tool != guard.NoTool, "Parse(%q) tool = none, want the wrapped tool", tt.command)
			assert.Check(t, !got.Whole, "Parse(%q) reports a wrapped invocation as the whole command", tt.command)
		})
	}
}

func TestParse_WhenAZshModifierIsGivenAnOption_ReadsTheOptionAsTheCommand(t *testing.T) {
	t.Parallel()

	// zsh's noglob and nocorrect take no options: `noglob -- git push` runs a command named `--`.
	for _, command := range []string{"noglob -- git push", "nocorrect -- git push"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			got := only(t, command)

			assert.Check(t, is.DeepEqual(texts([]guard.Invocation{got})[0], []string{"--", "git", "push"}), "Parse(%q) words", command)
			assert.Check(t, is.DeepEqual(kinds(got.Marks), []guard.MarkKind{guard.ToolArgument}), "Parse(%q) marks", command)
		})
	}
}

func TestParse_WhenEnvChangesTheEnvironment_ReportsItsOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		command     string
		wantUnset   []string
		wantCleared bool
	}{
		{name: "no option", command: "env git push"},
		{name: "cleared environment", command: "env -i git push", wantCleared: true},
		{name: "removed names", command: "env -u GIT_DIR -u GIT_WORK_TREE git push", wantUnset: []string{"GIT_DIR", "GIT_WORK_TREE"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.Equal(got.EnvCleared, tt.wantCleared), "Parse(%q) env cleared", tt.command)
			assert.Check(t, is.DeepEqual(got.EnvUnset, tt.wantUnset), "Parse(%q) env unset", tt.command)
		})
	}
}
