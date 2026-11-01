//go:build unix

package guard_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/guard"
)

const (
	home  = "/home/reader"
	start = "/home/reader/repo"
)

// parse returns the invocations of command, read by a caller in start whose HOME is home.
func parse(t *testing.T, command string) []guard.Invocation {
	t.Helper()

	return parseWith(t, command, guard.Caller{Home: home, Dir: start})
}

// parseWith returns the invocations of command, read by caller.
func parseWith(t *testing.T, command string, caller guard.Caller) []guard.Invocation {
	t.Helper()

	got, err := guard.Parse(t.Context(), command, caller)
	assert.NilError(t, err, "Parse(%q)", command)

	return got
}

// only returns the one invocation of command.
func only(t *testing.T, command string) guard.Invocation {
	t.Helper()

	got := parse(t, command)
	assert.Assert(t, is.Len(got, 1), "Parse(%q)", command)

	return got[0]
}

// last returns the last invocation of command, read by a caller in start whose HOME is home.
func last(t *testing.T, command string) guard.Invocation {
	t.Helper()

	return lastWith(t, command, guard.Caller{Home: home, Dir: start})
}

// lastWith returns the last invocation of command, read by caller.
func lastWith(t *testing.T, command string, caller guard.Caller) guard.Invocation {
	t.Helper()

	got := parseWith(t, command, caller)
	assert.Assert(t, len(got) > 0, "Parse(%q) holds no invocation", command)

	return got[len(got)-1]
}

func texts(invocations []guard.Invocation) [][]string {
	lists := make([][]string, 0, len(invocations))
	for _, inv := range invocations {
		lists = append(lists, wordTexts(inv.Words))
	}

	return lists
}

func kinds(marks []guard.Mark) []guard.MarkKind {
	got := make([]guard.MarkKind, 0, len(marks))
	for _, m := range marks {
		got = append(got, m.Kind)
	}

	return got
}

// heads returns whether HEAD may have moved before each of invocations.
func heads(invocations []guard.Invocation) []bool {
	got := make([]bool, 0, len(invocations))
	for _, inv := range invocations {
		got = append(got, inv.HeadUnknown)
	}

	return got
}

// writeExecutable writes an executable file at path, creating its directory.
func writeExecutable(t *testing.T, path string) {
	t.Helper()

	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	assert.NilError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700)) //nolint:gosec // the test needs an executable file
}

func TestParse_WhenTheParserCannotReadTheCommand_ReturnsAParseError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		command    string
		wantLine   uint
		wantColumn uint
	}{
		{name: "stray closing parenthesis", command: "echo ok; rm -rf / )", wantLine: 1, wantColumn: 19},
		{name: "feature of another shell", command: "echo ok\n  echo ${(U)x}", wantLine: 2, wantColumn: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := guard.Parse(t.Context(), tt.command, guard.Caller{Home: home})

			failure, ok := errors.AsType[*guard.ParseError](err)
			assert.Assert(t, ok, "Parse(%q) error = %v, want a *guard.ParseError", tt.command, err)
			assert.Check(t, is.Equal(failure.Line, tt.wantLine), "Parse(%q) line", tt.command)
			assert.Check(t, is.Equal(failure.Column, tt.wantColumn), "Parse(%q) column", tt.command)
			assert.Check(t, failure.Message != "", "Parse(%q) carries no message", tt.command)
		})
	}
}

func TestParse_ReturnsEverySimpleCommandInTheTree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    [][]string
	}{
		{name: "pipeline", command: "git log | grep fix", want: [][]string{{"git", "log"}, {"grep", "fix"}}},
		{
			name:    "semicolon list",
			command: "git add x; git commit",
			want:    [][]string{{"git", "add", "x"}, {"git", "commit"}},
		},
		{
			name:    "and and or lists",
			command: "git fetch && git pull || git status",
			want:    [][]string{{"git", "fetch"}, {"git", "pull"}, {"git", "status"}},
		},
		{name: "subshell", command: "(git push)", want: [][]string{{"git", "push"}}},
		{name: "group", command: "{ git push; }", want: [][]string{{"git", "push"}}},
		{
			name:    "if branches",
			command: "if git a; then git b; else git c; fi",
			want:    [][]string{{"git", "a"}, {"git", "b"}, {"git", "c"}},
		},
		{
			name:    "while loop",
			command: "while git a; do git b; done",
			want:    [][]string{{"git", "a"}, {"git", "b"}},
		},
		{name: "for loop", command: "for x in 1; do git push; done", want: [][]string{{"git", "push"}}},
		{name: "case arm", command: "case x in y) git push;; esac", want: [][]string{{"git", "push"}}},
		{name: "function body never called", command: "f() { git push; }", want: [][]string{{"git", "push"}}},
		{name: "command substitution", command: "echo $(git push)", want: [][]string{{"echo", ""}, {"git", "push"}}},
		{name: "input process substitution", command: "cat <(git push)", want: [][]string{{"cat", ""}, {"git", "push"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Check(t, is.DeepEqual(texts(got), tt.want), "Parse(%q)", tt.command)
		})
	}
}

func TestParse_WhenASimpleCommandIsNotACall_ReportsAnInvocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    [][]string
	}{
		{name: "export of an assignment", command: "export GIT_DIR=/tmp/x; git push", want: [][]string{{"export", "GIT_DIR=/tmp/x"}, {"git", "push"}}},
		{name: "declare with an option", command: "declare -x A=1", want: [][]string{{"declare", "-x", "A=1"}}},
		{name: "local of a name", command: "local name", want: [][]string{{"local", "name"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Check(t, is.DeepEqual(texts(got), tt.want), "Parse(%q)", tt.command)
		})
	}
}

func TestParse_WhenACommandOnlyAssigns_ReportsAnInvocationWithNoWords(t *testing.T) {
	t.Parallel()

	const command = "GIT_DIR=/tmp/x"

	got := only(t, command)

	assert.Check(t, is.Len(got.Words, 0), "Parse(%q) words", command)
	want := []guard.Assignment{{Name: "GIT_DIR", Value: guard.Word{Text: "/tmp/x", Literal: true}}}
	assert.Check(t, is.DeepEqual(got.Assignments, want), "Parse(%q) assignments", command)
	assert.Check(t, !got.Whole, "Parse(%q) reports a command with no words as the whole command", command)
}

func TestParse_WhenALetClauseRuns_ReportsALetInvocation(t *testing.T) {
	t.Parallel()

	const command = "let n=1; git push"

	got := parse(t, command)

	assert.Assert(t, is.Len(got, 2), "Parse(%q) = %v", command, texts(got))
	assert.Assert(t, len(got[0].Words) > 0, "Parse(%q) let invocation has no words", command)
	assert.Check(t, is.DeepEqual(got[0].Words[0], guard.Word{Text: "let", Literal: true}), "Parse(%q) let word", command)
	assert.Check(t, is.DeepEqual(texts(got[1:]), [][]string{{"git", "push"}}), "Parse(%q)", command)
}

func TestParse_ReportsTheToolACommandWordNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		command  string
		wantTool guard.Tool
		wantBare bool
	}{
		{name: "bare git", command: "git status", wantTool: guard.Git, wantBare: true},
		{name: "absolute path", command: "/opt/homebrew/bin/git status", wantTool: guard.Git},
		{name: "zsh equals lookup", command: "=git status", wantTool: guard.Git},
		{name: "backslash escaped", command: `\git status`, wantTool: guard.Git},
		{name: "double quoted", command: `"gh" pr list`, wantTool: guard.GH},
		{name: "name that only starts with git", command: "gitk --all", wantTool: guard.NoTool, wantBare: true},
		{name: "literal prefix of an expansion", command: `git"$x" status`, wantTool: guard.NoTool},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.Equal(got.Tool, tt.wantTool), "Parse(%q) tool", tt.command)
			assert.Check(t, is.Equal(got.Bare, tt.wantBare), "Parse(%q) bare", tt.command)
		})
	}
}

func TestParse_WhenTheCommandWordIsInANSICQuotes_ReportsTheToolUnmarked(t *testing.T) {
	t.Parallel()

	const command = "$'git' push"

	got := only(t, command)

	assert.Check(t, is.Equal(got.Tool, guard.Git), "Parse(%q) tool", command)
	assert.Check(t, is.Len(got.Marks, 0), "Parse(%q) marks", command)
}

func TestParse_ReportsWhetherEachInvocationIsTheWholeCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    []bool
	}{
		{name: "one bare git command", command: "git status", want: []bool{true}},
		{name: "pipeline", command: "git status | cat", want: []bool{false, false}},
		{name: "redirect", command: "git status > out", want: []bool{false}},
		{name: "background", command: "git status &", want: []bool{false}},
		{name: "assignment prefix", command: "GIT_DIR=x git status", want: []bool{false}},
		{name: "escaped command word", command: `\git status`, want: []bool{false}},
		{name: "path command word", command: "/usr/bin/git status", want: []bool{false}},
		{name: "subshell", command: "(git status)", want: []bool{false}},
		{name: "nested substitution", command: `git log "$(git rev-parse HEAD)"`, want: []bool{false, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			invocations := parse(t, tt.command)

			got := make([]bool, 0, len(invocations))
			for _, inv := range invocations {
				got = append(got, inv.Whole)
			}

			assert.Check(t, is.DeepEqual(got, tt.want), "Parse(%q) whole", tt.command)
		})
	}
}

func TestParse_ReportsEachRedirect(t *testing.T) {
	t.Parallel()

	const command = "git show HEAD:f 2> err < in"

	got := only(t, command)

	want := []guard.Redirect{
		{Fd: "2", Op: ">", Target: guard.Word{Text: "err", Literal: true}},
		{Op: "<", Target: guard.Word{Text: "in", Literal: true}},
	}
	assert.Check(t, is.DeepEqual(got.Redirects, want), "Parse(%q) redirects", command)
}

func TestInvocation_Stdout_WhenStdoutGoesToAFile_ReturnsItsTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "truncate", command: "git show HEAD:f > f"},
		{name: "stdout and stderr", command: "git show HEAD:f &> f"},
		{name: "explicit descriptor one", command: "git show HEAD:f 1> f"},
		{name: "read-write on descriptor one", command: "git show HEAD:f 1<> f"},
		{name: "duplicate onto a file name", command: "git show HEAD:f >& f"},
		{name: "last stdout redirect wins", command: "git show HEAD:f > g > f"},
		{name: "redirect on an enclosing group", command: "{ git show HEAD:f; } > f"},
		{name: "earlier exec with no command", command: "exec > f; git show HEAD:f"},
		{name: "stdout onto stderr after stderr goes to a file", command: "git show HEAD:f 2> f >&2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			invocations := parse(t, tt.command)
			inv := invocations[len(invocations)-1]

			got, ok := inv.Stdout()

			assert.Assert(t, ok, "Parse(%q) reports stdout not redirected", tt.command)
			assert.Check(t, is.DeepEqual(got, guard.Word{Text: "f", Literal: true}), "Parse(%q) stdout", tt.command)
		})
	}
}

func TestInvocation_Stdout_WhenStdoutIsNotRedirectedToAFile_ReportsNone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "no redirect", command: "git show HEAD:f"},
		{name: "stderr only", command: "git show HEAD:f 2> err"},
		{name: "input only", command: "git show HEAD:f < in"},
		{name: "stdout duplicated onto stderr", command: "git show HEAD:f >&2"},
		{name: "read-write on standard input", command: "git show HEAD:f <> f"},
		{name: "stdout onto stderr before stderr goes to a file", command: "git show HEAD:f >&2 2> f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inv := only(t, tt.command)

			got, ok := inv.Stdout()

			assert.Check(t, !ok, "Parse(%q) stdout = %+v, want none", tt.command, got)
		})
	}
}

// executable writes an executable file named name in a new directory and returns its path.
func executable(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	writeExecutable(t, path)

	return path
}

func TestParse_ReportsEachAssignmentTheCommandRunsWith(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    []guard.Assignment
	}{
		{
			name:    "assignment prefix",
			command: "GIT_DIR=x git status",
			want:    []guard.Assignment{{Name: "GIT_DIR", Value: guard.Word{Text: "x", Literal: true}}},
		},
		{
			name:    "env argument",
			command: "env GIT_DIR=x git status",
			want:    []guard.Assignment{{Name: "GIT_DIR", Value: guard.Word{Text: "x", Literal: true}}},
		},
		{
			name:    "prefix then env argument",
			command: "A=1 env B=$y git status",
			want: []guard.Assignment{
				{Name: "A", Value: guard.Word{Text: "1", Literal: true}},
				{Name: "B", Value: guard.Word{Text: "", Literal: false}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.DeepEqual(got.Assignments, tt.want), "Parse(%q) assignments", tt.command)
			assert.Check(t, is.DeepEqual(wordTexts(got.Words), []string{"git", "status"}), "Parse(%q) words", tt.command)
		})
	}
}

func TestParse_WhenADataConsumerNamesATool_ReportsNoInvocationOrMark(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "reader", command: "echo git reset is banned"},
		{name: "command -v lookup", command: "command -v git"},
		{name: "consumer behind a wrapper", command: "nice echo git push"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

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
		{name: "tool word handed to another command", command: "xargs git push", want: guard.ToolArgument},
		{name: "wrapper option it does not take", command: "nice -z git push", want: guard.ToolArgument},
		{name: "shell handed to xargs", command: "xargs sh -c 'git push origin main'", want: guard.ToolArgument},
		{name: "command string handed to su", command: "su -c 'git push origin main'", want: guard.ToolArgument},
		{name: "eval handed to another command", command: "xargs eval 'git status'", want: guard.ToolArgument},
		{name: "source handed to another command", command: "xargs source x.sh", want: guard.ToolArgument},
		{name: "tool after a list in a string", command: "su -c 'cd /repo && git push'", want: guard.ToolArgument},
		{name: "tool behind a wrapper in a string", command: "su -c 'exec git push'", want: guard.ToolArgument},
		{name: "tool handed to an unknown command in a string", command: "su -c 'sudo git push'", want: guard.ToolArgument},
		{name: "tool in a string with an expansion", command: `su -c "git push $b"`, want: guard.ToolArgument},
		{name: "ils command that changes state behind another command", command: "timeout 60 ils merge", want: guard.ToolArgument},
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
		{name: "tool word", command: "xargs git push", want: "git"},
		{name: "path to a tool", command: "find . -exec /bin/sh -c x \\;", want: "/bin/sh"},
		{name: "tool in a string", command: "su -c 'cd /repo && gh pr merge 3'", want: "gh"},
		{name: "tool handed on inside a string", command: "ssh host 'sudo ils merge'", want: "ils"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.DeepEqual(got.Marks, []guard.Mark{{Kind: guard.ToolArgument, Name: tt.want}}), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenAnArgumentIsAPathNamedForATool_MarksOnlyAnExecutableFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path func(t *testing.T) string
		want []guard.Mark
	}{
		{name: "executable file", path: func(t *testing.T) string { return executable(t, "git") }, want: []guard.Mark{{Kind: guard.ToolArgument}}},
		{name: "executable file named for a shell", path: func(t *testing.T) string { return executable(t, "bash") }, want: []guard.Mark{{Kind: guard.ToolArgument}}},
		{
			name: "directory",
			path: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "ils")
				assert.NilError(t, os.Mkdir(dir, 0o750))

				return dir
			},
		},
		{
			name: "file that is not executable",
			path: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "gh")
				assert.NilError(t, os.WriteFile(path, []byte("text\n"), 0o600))

				return path
			},
		},
		{name: "missing path", path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent", "git") }},
		{
			name: "path whose stat is denied",
			path: func(t *testing.T) string {
				locked := filepath.Join(t.TempDir(), "locked")
				assert.NilError(t, os.Mkdir(locked, 0o000))
				t.Cleanup(func() { assert.Check(t, os.Chmod(locked, 0o700)) }) //nolint:gosec // the directory needs its search bit back to be removed

				return filepath.Join(locked, "git")
			},
			want: []guard.Mark{{Kind: guard.ToolArgument}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			command := "xargs " + tt.path(t) + " push"

			got := only(t, command)

			assert.Check(t, is.DeepEqual(kinds(got.Marks), kinds(tt.want)), "Parse(%q) marks", command)
		})
	}
}

func TestParse_WhenAnArgumentIsARelativePathNamedForATool_ReadsItWhereTheInvocationRuns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    []guard.Mark
	}{
		{name: "path under the caller's directory", command: "xargs ./bin/git push", want: []guard.Mark{{Kind: guard.ToolArgument}}},
		{name: "path under the directory a cd moved to", command: "cd bin && xargs ./git push", want: []guard.Mark{{Kind: guard.ToolArgument}}},
		{name: "path missing under the directory a cd moved to", command: "cd bin && xargs ./bin/git push"},
		{name: "path under an unknown directory", command: `cd "$d" && xargs ./absent/git push`, want: []guard.Mark{{Kind: guard.ToolArgument}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			callerDir := t.TempDir()
			writeExecutable(t, filepath.Join(callerDir, "bin", "git"))

			xargs := lastWith(t, tt.command, guard.Caller{Home: home, Dir: callerDir})

			assert.Check(t, is.DeepEqual(kinds(xargs.Marks), kinds(tt.want)), "Parse(%q) marks", tt.command)
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
		{name: "name that only starts with git", command: "xargs gitk"},
		{name: "command run by xargs that is no shell", command: "xargs echo hi"},
		{name: "string a data consumer in it reads", command: "xargs echo 'grep -n git push docs'"},
		{name: "string that does not parse", command: "xargs echo 'fi git push'"},
		{name: "string held in a variable", command: `su -c "$cmd"`},
		{name: "string whose command word is not a literal", command: `su -c "$g push"`},
		{name: "ils command that changes nothing behind another command", command: "timeout 60 ils status"},
		{name: "ils command that changes nothing in a string", command: "ssh host 'cd repo && ils status'"},
		{name: "ils command that only reads a command, behind another command", command: "timeout 60 ils parse --cwd . -- 'echo hi'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.Len(got.Marks, 0), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenAShellWordIsAnArgumentOfAShellWrapperOrDataConsumer_MarksNothing(t *testing.T) {
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

			for _, inv := range got {
				assert.Check(t, is.Len(inv.Marks, 0), "Parse(%q) invocation %v marks", tt.command, inv.Words)
			}
		})
	}
}
