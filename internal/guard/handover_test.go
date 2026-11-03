//go:build unix

package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/guard"
)

func TestParse_WhenADataConsumerNamesATool_ReportsNoToolInvocationOrMark(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "command -v lookup", command: "command -v git"},
		{name: "consumer behind a wrapper", command: "nice echo git push"},
		{name: "go words that run nothing behind a global option", command: "go -C dir test -run 'git push' ./..."},
		{name: "go words that run nothing beside a non-literal package", command: `go build "$pkg" git push`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Assert(t, len(got) > 0, "Parse(%q) = %v", tt.command, texts(got))
			for _, inv := range got {
				assert.Check(t, is.Equal(inv.Tool, guard.NoTool), "Parse(%q) invocation %v tool", tt.command, inv.Words)
				assert.Check(t, is.Len(inv.Marks, 0), "Parse(%q) invocation %v marks", tt.command, inv.Words)
			}
		})
	}
}

func TestParse_MarksWhatItCannotRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    guard.MarkKind
		index   int
	}{
		{name: "extended glob", command: "echo @(a|b)", want: guard.ExtendedGlob},
		{name: "substituted command word", command: "$(which git) push", want: guard.DynamicCommand},
		{name: "wrapper option it does not take", command: "nice -z git push", want: guard.ToolArgument},
		{name: "shell handed to xargs", command: "xargs sh -c 'git push origin main'", want: guard.ToolArgument},
		{name: "eval handed to another command", command: "xargs eval 'git status'", want: guard.ToolArgument},
		{name: "tool behind a wrapper in a string", command: "su -c 'exec git push'", want: guard.ToolArgument},
		{name: "tool in a string with an expansion", command: `su -c "git push $b"`, want: guard.ToolArgument},
		{name: "ils with no command behind another command", command: "xargs ils", want: guard.ToolArgument},
		{name: "ils command that changes state in a string", command: "ssh host 'ils status && ils merge'", want: guard.ToolArgument},
		{name: "non-literal wrapper option", command: `env -"$o" git push`, want: guard.DynamicCommand},
		{name: "non-literal -c string", command: `bash -c "$CMD"`, want: guard.DynamicScript},
		{name: "non-literal eval word", command: `eval "$x"`, want: guard.DynamicScript},
		{name: "non-literal heredoc fed to a shell", command: "bash <<EOF\n$x\nEOF", want: guard.DynamicScript},
		{name: "shell reading a pipe", command: "cat x | bash", index: 1, want: guard.HiddenScript},
		{name: "shell running a file", command: "bash ./x.sh", want: guard.HiddenScript},
		{name: "source of a file", command: "source x.sh", want: guard.HiddenScript},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Assert(t, len(got) > tt.index, "Parse(%q) = %v", tt.command, texts(got))
			assert.Check(t, is.DeepEqual(kinds(got[tt.index].Marks), []guard.MarkKind{tt.want}), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenAToolIsHandedToAnotherCommand_NamesItOnTheMark(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    string
	}{
		{name: "path to a tool", command: "find . -exec /bin/sh -c x \\;", want: "/bin/sh"},
		{name: "tool in a string", command: "su -c 'cd /repo && gh pr merge 3'", want: "gh"},
		{name: "tool handed on inside a string", command: "ssh host 'sudo ils merge'", want: "ils"},
		{name: "tool in a string that does not parse, behind a data consumer word", command: "ssh host echo 'fi; git push'", want: "git"},
		{name: "tool path in a handed string that does not parse", command: `python3 -c "os.system('/usr/bin/git push')"`, want: "/usr/bin/git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.DeepEqual(got.Marks, []guard.Mark{{Kind: guard.ToolArgument, Name: tt.want}}), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenNoArgumentIsAToolWord_MarksNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "tool word in a tool's own arguments", command: "git commit -m git"},
		{name: "string that does not parse, read by the data consumer that is the command", command: "echo 'fi; git push'"},
		{name: "handed string that does not parse and holds a hyphenated name starting with a tool", command: `python3 -c "os.system('ils-live push')"`},
		{name: "string whose command word is not a literal", command: `su -c "$g push"`},
		{name: "ils command that changes nothing in a string", command: "ssh host 'cd repo && ils status'"},
		{name: "ils command that only reads a command, behind another command", command: "timeout 60 ils parse --cwd . -- 'echo hi'"},
		{name: "go run with no tool word", command: "go run ./cmd/tool --verbose"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.Len(got.Marks, 0), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenAShellWordIsAnArgumentOfAShellOrEval_MarksNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "shell named as the zero argument of a shell", command: "bash -c 'true' sh"},
		{name: "shell named as an argument of eval", command: "eval sh -c true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Assert(t, len(got) > 0, "Parse(%q) = %v", tt.command, texts(got))
			for _, inv := range got {
				assert.Check(t, is.Len(inv.Marks, 0), "Parse(%q) invocation %v marks", tt.command, inv.Words)
			}
		})
	}
}

func TestParse_WhenGoRunsAProgramOrGenerator_ReadsItsWordsAsHanded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    string
	}{
		{name: "tool in a string handed to go generate", command: "go generate -run 'gh pr merge 3' ./...", want: "gh"},
		{name: "tool word handed to go run behind a joined global option", command: "go -C=dir run ./tools git push", want: "git"},
		{name: "tool in a string handed to go generate behind a global option", command: "go -C dir generate -run 'gh pr merge 3' ./...", want: "gh"},
		{name: "tool word behind a non-literal word where go reads its global option", command: `go "$o" . git push`, want: "git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.DeepEqual(got.Marks, []guard.Mark{{Kind: guard.ToolArgument, Name: tt.want}}), "Parse(%q) marks", tt.command)
		})
	}
}
