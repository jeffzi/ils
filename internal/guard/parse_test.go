//go:build unix

package guard_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/guard"
)

const (
	home  = "/home/reader"
	start = "/home/reader/repo"
)

// readerCaller returns the caller in start whose HOME is home.
func readerCaller() guard.Caller {
	return guard.Caller{Home: home, Dir: start}
}

// parse returns the invocations of command, read by a caller in start whose HOME is home.
func parse(t *testing.T, command string) []guard.Invocation {
	t.Helper()

	return parseWith(t, command, readerCaller())
}

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

// checkNoMarks fails unless every command parses to one invocation that carries no marks.
func checkNoMarks(t *testing.T, tests []namedCommand) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.Len(got.Marks, 0), "Parse(%q) marks", tt.command)
		})
	}
}

// gitRun returns the one invocation of command that runs git, read by a caller in start whose
// HOME is home.
func gitRun(t *testing.T, command string) guard.Invocation {
	t.Helper()

	var runs []guard.Invocation
	for _, inv := range parse(t, command) {
		if inv.Tool == guard.Git {
			runs = append(runs, inv)
		}
	}
	assert.Assert(t, is.Len(runs, 1), "Parse(%q) runs git %d times, want once", command, len(runs))

	return runs[0]
}

// last returns the last invocation of command, read by a caller in start whose HOME is home.
func last(t *testing.T, command string) guard.Invocation {
	t.Helper()

	return lastWith(t, command, readerCaller())
}

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

func heads(invocations []guard.Invocation) []bool {
	got := make([]bool, 0, len(invocations))
	for _, inv := range invocations {
		got = append(got, inv.HeadUnknown)
	}

	return got
}

func wholes(invocations []guard.Invocation) []bool {
	got := make([]bool, 0, len(invocations))
	for _, inv := range invocations {
		got = append(got, inv.Whole)
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
			assert.Check(t, failure.Message != "", "Parse(%q) carries no message", tt.command)
			assert.Check(t, is.Equal(failure.Line, tt.wantLine), "Parse(%q) line", tt.command)
			assert.Check(t, is.Equal(failure.Column, tt.wantColumn), "Parse(%q) column", tt.command)
			position := fmt.Sprintf("%d:%d", failure.Line, failure.Column)
			assert.Check(t, !strings.HasPrefix(failure.Message, position), "Parse(%q) message %q repeats the position", tt.command, failure.Message)
		})
	}
}

func TestParse_ReturnsEverySimpleCommandInTheTree(t *testing.T) {
	t.Parallel()

	checkTexts(t, []commandTexts{
		{name: "pipeline", command: "git log | grep fix", want: [][]string{{"git", "log"}, {"grep", "fix"}}},
		{
			name:    "and and or lists",
			command: "git fetch && git pull || git status",
			want:    [][]string{{"git", "fetch"}, {"git", "pull"}, {"git", "status"}},
		},
		{name: "subshell", command: "(git push)", want: [][]string{{"git", "push"}}},
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
		{name: "input process substitution", command: "cat <(git push)", want: [][]string{{"cat", ""}, {"git", "push"}}},
		{
			name:    "command substitution beside a shell string holding a pipeline",
			command: "bash -c 'echo a | cat' x $(git push origin main)",
			want:    [][]string{{"bash", "-c", "echo a | cat", "x", ""}, {"echo", "a"}, {"cat"}, {"git", "push", "origin", "main"}},
		},
	})
}

// commandTexts is a command whose invocations carry the word texts in want.
type commandTexts struct {
	name    string
	command string
	want    [][]string
}

func checkTexts(t *testing.T, tests []commandTexts) {
	t.Helper()

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

	checkTexts(t, []commandTexts{
		{name: "export of an assignment", command: "export GIT_DIR=/tmp/x; git push", want: [][]string{{"export", "GIT_DIR=/tmp/x"}, {"git", "push"}}},
		{name: "declare with an option", command: "declare -x A=1", want: [][]string{{"declare", "-x", "A=1"}}},
		{name: "local of a name", command: "local name", want: [][]string{{"local", "name"}}},
	})
}

func TestParse_WhenACommandOnlyAssigns_ReportsAnInvocationWithNoWords(t *testing.T) {
	t.Parallel()

	const command = "GIT_DIR=/tmp/x"

	got := only(t, command)

	assert.Check(t, is.Len(got.Words, 0), "Parse(%q) words", command)
	want := []guard.Assignment{{Name: "GIT_DIR", Value: guard.Word{Text: "/tmp/x", Literal: true}}}
	assert.Check(t, is.DeepEqual(got.Assignments, want), "Parse(%q) assignments", command)
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
	}{
		{name: "absolute path", command: "/opt/homebrew/bin/git status", wantTool: guard.Git},
		{name: "zsh equals lookup", command: "=git status", wantTool: guard.Git},
		{name: "ANSI-C quoted", command: "$'git' push", wantTool: guard.Git},
		{name: "name that only starts with git", command: "gitk --all", wantTool: guard.NoTool},
		{name: "literal prefix of an expansion", command: `git"$x" status`, wantTool: guard.NoTool},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.Equal(got.Tool, tt.wantTool), "Parse(%q) tool", tt.command)
		})
	}
}

func TestParse_ReportsWhetherTheCommandWordIsBare(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		command  string
		wantBare bool
	}{
		{name: "plain command word", command: "git status", wantBare: true},
		{name: "backslash escaped", command: `\git status`},
		{name: "double quoted", command: `"gh" pr list`},
		{name: "path to the command", command: "/usr/bin/git status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.Equal(got.Bare, tt.wantBare), "Parse(%q) bare", tt.command)
		})
	}
}

func TestParse_ReportsWhetherEachInvocationIsTheWholeCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    []bool
	}{
		{name: "one bare git command", command: "git status", want: []bool{true}},
		{name: "redirect", command: "git status > out", want: []bool{false}},
		{name: "background", command: "git status &", want: []bool{false}},
		{name: "assignment prefix", command: "GIT_DIR=x git status", want: []bool{false}},
		{name: "escaped command word", command: `\git status`, want: []bool{false}},
		{name: "subshell", command: "(git status)", want: []bool{false}},
		{name: "nested substitution", command: `git log "$(git rev-parse HEAD)"`, want: []bool{false, false}},
		{name: "wrapper", command: "nice git status", want: []bool{false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := wholes(parse(t, tt.command))

			assert.Check(t, is.DeepEqual(got, tt.want), "Parse(%q) whole", tt.command)
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

func TestParse_WhenAnArgumentIsAPathNamedForATool_MarksItUnlessProvenMissingOrNotAnExecutableRegularFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path func(t *testing.T) string
		want []guard.Mark
	}{
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
			name: "path whose stat fails",
			path: func(t *testing.T) string {
				loop := filepath.Join(t.TempDir(), "loop")
				assert.NilError(t, os.Symlink("loop", loop))

				return filepath.Join(loop, "git")
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

func TestParse_WhenAGlobQualifierRunsNoCode_MarksNothing(t *testing.T) {
	t.Parallel()

	checkNoMarks(t, []namedCommand{
		{name: "null glob after a non-literal directory", command: `rm -f "$d"/tdd-red-phase*(N)`},
	})
}

func TestParse_WhenAnExtendedGlobCouldRunCodeOrIsNoQualifier_MarksIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "qualifier evaluating a string", command: "ls *(e:'touch x':)"},
		{name: "qualifier calling a function", command: "ls *(+f)"},
		{name: "negation", command: "ls !(x)"},
		{name: "qualifier holding a substitution in backticks", command: "ls *(`git stash`N)"},
		{name: "qualifier holding a parameter expansion", command: `ls *(${x}N)`},
		{name: "qualifier holding a process substitution", command: "ls *(<(git stash)N)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.DeepEqual(kinds(got.Marks), []guard.MarkKind{guard.ExtendedGlob}), "Parse(%q) marks", tt.command)
		})
	}
}

func TestJudge_AllowsAnAliasThatHidesNoTool(t *testing.T) {
	t.Parallel()

	checkAllowed(t, readerCaller(), []namedCommand{
		{name: "alias naming no tool", command: "alias ll='ls -l'; ll"},
		{name: "alias printing one", command: "alias ll"},
	})
}

func TestJudge_RefusesAnAliasWhoseValueNamesATool(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "gh", command: "alias ship='gh pr merge'; ship", wantNamed: "ship"},
		{name: "second operand", command: "alias ll='ls -l' ship='git push'; ship", wantNamed: "ship"},
		{name: "non-literal value", command: `alias ship="$c"; ship`, wantNamed: "ship"},
	})
}

func TestJudge_RefusesAnAliasWhoseValueStartsWithACommandTheGuardReadsThrough(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "pushd", command: "alias hop='pushd sub'; hop; git commit -m x", wantNamed: "hop"},
		{name: "wrapper", command: "alias hop='builtin cd'; hop sub; git commit -m x", wantNamed: "hop"},
	})
}
