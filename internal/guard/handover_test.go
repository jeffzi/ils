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
		{name: "go words that run nothing beside a non-literal package", command: `go build "$pkg" git push`},
		{name: "search pattern that parses as a script", command: "rg 'git push' docs"},
		{name: "package manager naming a tool", command: "brew install git"},
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
		{name: "shell whose string runs git, handed to docker run", command: "docker run img sh -c 'git log'", want: guard.ToolArgument},
		{name: "shell whose string hands git over, handed to xargs", command: "xargs sh -c 'sudo git log'", want: guard.ToolArgument},
		{name: "shell whose string runs a shell, handed to xargs", command: "xargs sh -c 'sh -c ls'", want: guard.ToolArgument},
		{name: "shell with no -c string, handed to xargs", command: "xargs sh", want: guard.ToolArgument},
		{name: "shell with a non-literal -c string, handed to xargs", command: `xargs bash -c "$s"`, want: guard.ToolArgument},
		{name: "shell with another option before its string, handed to xargs", command: "xargs bash -e ls", want: guard.ToolArgument},
		{name: "shell with -c and no string, handed to xargs", command: "xargs sh -c", want: guard.ToolArgument},
		{name: "shell string among an interpreter's arguments", command: "python3 tool.py sh -c 'git push'", want: guard.ToolArgument},
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
		{name: "bare tool word among an interpreter's arguments", command: "python3 tool.py git push", want: "git"},
		{name: "code for a python3 word with no minor", command: "python3. -c 'import git'", want: "git"},
		{name: "code for a python3 word whose minor is not a number", command: "python3.x -c 'import git'", want: "git"},
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
		{name: "sed script that parses as a shell script", command: "sed -n '/git push/p' README.md"},
		{name: "code that parses as a shell script, for a versioned python", command: "python3.13 -c 'import git'"},
		{name: "code that does not parse, for an interpreter", command: `python3 -c 'import os; os.system("git push")'`},
		{name: "code that does not parse, for a command the guard does not know", command: `php -r 'system("git push");'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.Len(got.Marks, 0), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenAShellStringHandedToAnotherCommandHandsNoTool_MarksNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "sh string behind xargs", command: "xargs -I{} sh -c 'ls {}'"},
		{name: "sh string behind docker run", command: "docker run img sh -c 'make test'"},
		{name: "bash string behind timeout", command: "timeout 60 bash -c 'go test ./...'"},
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
