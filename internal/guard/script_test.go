//go:build unix

package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/guard"
)

func TestParse_WhenAShellRunsAStringItHolds_ParsesItsCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    [][]string
	}{
		{name: "bash -c string", command: "bash -c 'git push'", want: [][]string{{"bash", "-c", "git push"}, {"git", "push"}}},
		{name: "eval words", command: "eval git push", want: [][]string{{"eval", "git", "push"}, {"git", "push"}}},
		{
			name:    "escaped dollar in a quoted heredoc",
			command: "bash <<'EOF'\ngit commit -m \\$x\nEOF",
			want:    [][]string{{"bash"}, {"git", "commit", "-m", "$x"}},
		},
		{
			name:    "escaped dollar in an unquoted heredoc",
			command: "bash <<EOF\ngit commit -m \\$x\nEOF",
			want:    [][]string{{"bash"}, {"git", "commit", "-m", ""}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Check(t, is.DeepEqual(texts(got), tt.want), "Parse(%q)", tt.command)
		})
	}
}

func TestParse_WhenANestedStringIsPastTheThirdLevel_MarksItUnparsed(t *testing.T) {
	t.Parallel()

	const command = "eval eval eval eval git push"

	got := parse(t, command)

	want := [][]string{
		{"eval", "eval", "eval", "eval", "git", "push"},
		{"eval", "eval", "eval", "git", "push"},
		{"eval", "eval", "git", "push"},
		{"eval", "git", "push"},
	}
	assert.Assert(t, is.DeepEqual(texts(got), want), "Parse(%q)", command)
	assert.Check(t, is.DeepEqual(got[3].Marks, []guard.Mark{{Kind: guard.DeepScript}}), "Parse(%q) marks", command)
}

func TestParse_WhenGoIsGivenACommandToRunItsTools_ReadsTheCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "toolexec value", command: `go build -toolexec "sh -c 'git push'" .`},
		{name: "exec value", command: "go test -exec 'git push' ."},
		{name: "exec value joined by an equals sign", command: "go test -exec='git push' ."},
		{name: "exec value of go run", command: "go run -exec 'git push' ."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := gitRun(t, tt.command)

			assert.Check(t, is.DeepEqual(wordTexts(got.Words), []string{"git", "push"}), "Parse(%q) git words", tt.command)
		})
	}
}

func TestParse_WhenTheGoCommandToRunIsNotALiteral_MarksADynamicScript(t *testing.T) {
	t.Parallel()

	const command = `go build -toolexec "$T" .`

	got := only(t, command)

	assert.Check(t, is.DeepEqual(kinds(got.Marks), []guard.MarkKind{guard.DynamicScript}), "Parse(%q) marks", command)
}

func TestJudge_WhenTheGoCommandToRunDoesNotParse_RefusesNamingTheFlag(t *testing.T) {
	t.Parallel()

	const command = "go test -exec 'wrap )' ."

	got := guard.Judge(t.Context(), command, guard.Caller{Home: home, Dir: start})

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Contains(got.Reason, "-exec"), "Judge(%q) reason", command)
}

func TestJudge_WhenAShellStringHandsGoACommandToRunItsTools_RefusesTheCommand(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "toolexec value in a bash -c string", command: `bash -c "go build -toolexec 'sh -c \"git push\"'"`, wantNamed: "git push"},
	})
}

func TestParse_WhenAGoCommandToRunIsPastTheThirdLevel_MarksItUnparsed(t *testing.T) {
	t.Parallel()

	const command = "eval eval eval go build -toolexec 'git push'"

	got := last(t, command)

	assert.Check(t, is.DeepEqual(got.Marks, []guard.Mark{{Kind: guard.DeepScript, Name: "go -toolexec"}}), "Parse(%q) marks", command)
}
