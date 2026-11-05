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
		{name: "nice with a separate value", command: "nice -n 10 git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"nice"}},
		{name: "nice with a numeric option", command: "nice -10 git push", wantWords: []string{"git", "push"}, wantWrappers: []string{"nice"}},
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
		})
	}
}

func TestParse_WhenAnEnvArgumentIsAQuotedAssignment_ReadsItAsAnAssignment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		command         string
		wantAssignments []guard.Assignment
		wantWords       []string
	}{
		{
			name:            "whole word in double quotes",
			command:         `env "GIT_DIR=/x" git commit`,
			wantAssignments: []guard.Assignment{{Name: "GIT_DIR", Value: guard.Word{Text: "/x", Literal: true}}},
			wantWords:       []string{"git", "commit"},
		},
		{
			name:            "whole word in single quotes",
			command:         `env 'FOO=bar baz' make`,
			wantAssignments: []guard.Assignment{{Name: "FOO", Value: guard.Word{Text: "bar baz", Literal: true}}},
			wantWords:       []string{"make"},
		},
		{
			name:            "name alone in quotes",
			command:         `env "GIT_TRACE"=1 git status`,
			wantAssignments: []guard.Assignment{{Name: "GIT_TRACE", Value: guard.Word{Text: "1", Literal: true}}},
			wantWords:       []string{"git", "status"},
		},
		{
			name:            "expansion in the quoted value",
			command:         `env "PATH=$y/bin:$PATH" make`,
			wantAssignments: []guard.Assignment{{Name: "PATH", Value: guard.Word{Text: "", Literal: false}}},
			wantWords:       []string{"make"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.DeepEqual(got.Assignments, tt.wantAssignments), "Parse(%q) assignments", tt.command)
			assert.Check(t, is.DeepEqual(wordTexts(got.Words), tt.wantWords), "Parse(%q) words", tt.command)
		})
	}
}

func TestParse_WhenAQuotedEnvArgumentHasNoLiteralName_ReadsItAsTheCommand(t *testing.T) {
	t.Parallel()

	tests := []namedCommand{
		{name: "expansion before the equals sign", command: `env "$n=1" make`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.Len(got.Assignments, 0), "Parse(%q) assignments", tt.command)
			assert.Check(t, is.DeepEqual(wordTexts(got.Words), []string{"", "make"}), "Parse(%q) words", tt.command)
		})
	}
}

func TestParse_WhenAZshModifierIsGivenAnOption_ReadsTheOptionAsTheCommand(t *testing.T) {
	t.Parallel()

	// zsh's noglob and nocorrect take no options: `noglob -- git push` runs a command named `--`.
	const command = "noglob -- git push"

	got := only(t, command)

	assert.Check(t, is.DeepEqual(wordTexts(got.Words), []string{"--", "git", "push"}), "Parse(%q) words", command)
}
