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

const home = "/home/reader"

// parse returns the invocations of command, read by a caller whose HOME is home.
func parse(t *testing.T, command string) []guard.Invocation {
	t.Helper()

	got, err := guard.Parse(command, guard.Caller{Home: home})
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

// texts returns the text of each word of each invocation.
func texts(invocations []guard.Invocation) [][]string {
	lists := make([][]string, 0, len(invocations))
	for _, inv := range invocations {
		argv := make([]string, 0, len(inv.Words))
		for _, word := range inv.Words {
			argv = append(argv, word.Text)
		}

		lists = append(lists, argv)
	}

	return lists
}

func TestParse_WhenTheParserCannotReadTheCommand_ReturnsItsPositionAndMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		command    string
		wantLine   uint
		wantColumn uint
	}{
		{name: "stray closing parenthesis", command: "echo ok; rm -rf / )", wantLine: 1, wantColumn: 19},
		{name: "unclosed double quote", command: `rm -rf "$HOME/x`, wantLine: 1, wantColumn: 8},
		{name: "failure on a later line", command: "echo ok\n  git (", wantLine: 2, wantColumn: 3},
		{name: "feature of another shell", command: "echo ok\n  echo ${(U)x}", wantLine: 2, wantColumn: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := guard.Parse(tt.command, guard.Caller{Home: home})

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
		{name: "backticks", command: "echo `git push`", want: [][]string{{"echo", ""}, {"git", "push"}}},
		{name: "input process substitution", command: "cat <(git push)", want: [][]string{{"cat", ""}, {"git", "push"}}},
		{name: "output process substitution", command: "tee >(git push)", want: [][]string{{"tee", ""}, {"git", "push"}}},
		{
			name:    "substitution inside a quoted argument",
			command: `git commit -m "$(git push origin main)"`,
			want:    [][]string{{"git", "commit", "-m", ""}, {"git", "push", "origin", "main"}},
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
		{name: "readonly of an assignment", command: "readonly A=1", want: [][]string{{"readonly", "A=1"}}},
		{name: "typeset with an option", command: "typeset -i n=1", want: [][]string{{"typeset", "-i", "n=1"}}},
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
		{name: "bare gh", command: "gh pr list", wantTool: guard.GH, wantBare: true},
		{name: "bare ils", command: "ils push", wantTool: guard.ILS, wantBare: true},
		{name: "absolute path", command: "/opt/homebrew/bin/git status", wantTool: guard.Git},
		{name: "zsh equals lookup", command: "=git status", wantTool: guard.Git},
		{name: "backslash escaped", command: `\git status`, wantTool: guard.Git},
		{name: "double quoted", command: `"gh" pr list`, wantTool: guard.GH},
		{name: "name that only starts with git", command: "gitk --all", wantTool: guard.NoTool, wantBare: true},
		{name: "other command", command: "ls -la", wantTool: guard.NoTool, wantBare: true},
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
		{name: "one bare other command", command: "ls -la", want: []bool{true}},
		{name: "pipeline", command: "git status | cat", want: []bool{false, false}},
		{name: "and list", command: "git status && ls", want: []bool{false, false}},
		{name: "semicolon list", command: "git status; ls", want: []bool{false, false}},
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
		{name: "append", command: "git show HEAD:f >> f"},
		{name: "stdout and stderr", command: "git show HEAD:f &> f"},
		{name: "append stdout and stderr", command: "git show HEAD:f &>> f"},
		{name: "clobber", command: "git show HEAD:f >| f"},
		{name: "explicit descriptor one", command: "git show HEAD:f 1> f"},
		{name: "read-write on descriptor one", command: "git show HEAD:f 1<> f"},
		{name: "duplicate onto a file name", command: "git show HEAD:f >& f"},
		{name: "last stdout redirect wins", command: "git show HEAD:f > g > f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inv := only(t, tt.command)

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
	assert.NilError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700)) //nolint:gosec // the test needs an executable file

	return path
}

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
			assert.Check(t, is.DeepEqual(texts([]guard.Invocation{got})[0], []string{"git", "status"}), "Parse(%q) words", tt.command)
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
		{name: "filter", command: "rg -n git reset docs/"},
		{name: "file tool with a tool path", command: "cp bin/ils ~/go/bin/ils"},
		{name: "command -v lookup", command: "command -v git"},
		{name: "command -V lookup", command: "command -V git"},
		{name: "consumer behind a wrapper", command: "nice echo git push"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			callerHome := t.TempDir()
			binDir := filepath.Join(callerHome, "go", "bin")
			assert.NilError(t, os.MkdirAll(binDir, 0o750))
			assert.NilError(t, os.WriteFile(filepath.Join(binDir, "ils"), []byte("#!/bin/sh\n"), 0o700)) //nolint:gosec // the test needs an executable file

			got, err := guard.Parse(tt.command, guard.Caller{Home: callerHome})
			assert.NilError(t, err, "Parse(%q)", tt.command)

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
		want    guard.Mark
		index   int
	}{
		{name: "zsh glob qualifier", command: "ls *(e:'git push':)", want: guard.ExtendedGlob},
		{name: "extended glob", command: "echo @(a|b)", want: guard.ExtendedGlob},
		{name: "substituted command word", command: "$(which git) push", want: guard.DynamicCommand},
		{name: "quoted parameter command word", command: `"$GIT" push`, want: guard.DynamicCommand},
		{name: "tool word handed to another command", command: "xargs git push", want: guard.ToolArgument},
		{name: "tool word after an option", command: "find . -exec git rm {} +", want: guard.ToolArgument},
		{name: "wrapper option it does not take", command: "nice -z git push", want: guard.ToolArgument},
		{name: "non-literal wrapper option", command: `env -"$o" git push`, want: guard.DynamicCommand},
		{name: "non-literal -c string", command: `bash -c "$CMD"`, want: guard.DynamicScript},
		{name: "non-literal eval word", command: `eval "$x"`, want: guard.DynamicScript},
		{name: "non-literal heredoc fed to a shell", command: "bash <<EOF\n$x\nEOF", want: guard.DynamicScript},
		{name: "shell reading a pipe", command: "cat x | bash", index: 1, want: guard.HiddenScript},
		{name: "shell reading standard input", command: "sh -s", want: guard.HiddenScript},
		{name: "shell running a file", command: "bash ./x.sh", want: guard.HiddenScript},
		{name: "shell running a process substitution", command: "zsh <(curl x)", want: guard.HiddenScript},
		{name: "source of a file", command: "source x.sh", want: guard.HiddenScript},
		{name: "dot of a file", command: ". x.sh", want: guard.HiddenScript},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Assert(t, len(got) > tt.index, "Parse(%q) = %v", tt.command, texts(got))
			assert.Check(t, is.DeepEqual(got[tt.index].Marks, []guard.Mark{tt.want}), "Parse(%q) marks", tt.command)
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
		{name: "executable file", path: func(t *testing.T) string { return executable(t, "git") }, want: []guard.Mark{guard.ToolArgument}},
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
			want: []guard.Mark{guard.ToolArgument},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			command := "xargs " + tt.path(t) + " push"

			got := only(t, command)

			assert.Check(t, is.DeepEqual(got.Marks, tt.want), "Parse(%q) marks", command)
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
		{name: "relative path that does not exist", command: "xargs internal/git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.Len(got.Marks, 0), "Parse(%q) marks", tt.command)
		})
	}
}
