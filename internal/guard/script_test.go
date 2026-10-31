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
			name:    "eval words joined by spaces",
			command: "eval 'git add x;' git commit",
			want:    [][]string{{"eval", "git add x;", "git", "commit"}, {"git", "add", "x"}, {"git", "commit"}},
		},
		{name: "quoted heredoc fed to a shell", command: "bash <<'EOF'\ngit push\nEOF", want: [][]string{{"bash"}, {"git", "push"}}},
		{
			name:    "escaped dollar in a quoted heredoc",
			command: "bash <<'EOF'\ngit commit -m \\$x\nEOF",
			want:    [][]string{{"bash"}, {"git", "commit", "-m", "$x"}},
		},
		{name: "unquoted literal heredoc fed to a shell", command: "bash <<EOF\ngit push\nEOF", want: [][]string{{"bash"}, {"git", "push"}}},
		{
			name:    "escaped dollar in an unquoted heredoc",
			command: "bash <<EOF\ngit commit -m \\$x\nEOF",
			want:    [][]string{{"bash"}, {"git", "commit", "-m", ""}},
		},
		{
			name:    "escaped backticks in an unquoted heredoc",
			command: "bash <<EOF\necho \\`git push\\`\nEOF",
			want:    [][]string{{"bash"}, {"echo", ""}, {"git", "push"}},
		},
		{
			name:    "third level",
			command: "eval eval eval git push",
			want:    [][]string{{"eval", "eval", "eval", "git", "push"}, {"eval", "eval", "git", "push"}, {"eval", "git", "push"}, {"git", "push"}},
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
	assert.Check(t, is.DeepEqual(got[3].Marks, []guard.Mark{guard.DeepScript}), "Parse(%q) marks", command)
}
