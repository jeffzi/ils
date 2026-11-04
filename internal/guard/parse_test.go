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

// markedCommand is a command whose invocation at index carries exactly one mark, of kind want.
type markedCommand struct {
	name    string
	command string
	want    guard.MarkKind
	index   int
}

// checkMarkKind fails unless each command's invocation at its index carries exactly its one mark.
func checkMarkKind(t *testing.T, tests []markedCommand) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Assert(t, len(got) > tt.index, "Parse(%q) = %v", tt.command, texts(got))
			assert.Check(t, is.DeepEqual(kinds(got[tt.index].Marks), []guard.MarkKind{tt.want}), "Parse(%q) marks", tt.command)
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

func TestParse_WhenTheCommandWordIsASubstitution_MarksADynamicCommand(t *testing.T) {
	t.Parallel()

	checkMarkKind(t, []markedCommand{
		{name: "substituted command word", command: "$(which git) push", want: guard.DynamicCommand},
	})
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

func TestParse_ReportsEachAssignmentTheCommandRunsWith(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    []guard.Assignment
	}{
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

func TestParse_WhenAGlobQualifierRunsNoCode_MarksNothing(t *testing.T) {
	t.Parallel()

	checkNoMarks(t, []namedCommand{
		{name: "null glob after a non-literal directory", command: `rm -f "$d"/tdd-red-phase*(N)`},
	})
}

func TestJudge_WhenAWordIsAnExtendedGlob_AllowsIt(t *testing.T) {
	t.Parallel()

	checkAllowed(t, readerCaller(), []namedCommand{
		{name: "extended glob", command: "echo @(a|b)"},
		{name: "qualifier evaluating a string", command: "ls *(e:'git push':)"},
	})
}

func TestJudge_WhenTheCommandWordIsAnExtendedGlob_RefusesItAsNotALiteral(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "extended glob naming git", command: "@(git) push", wantNamed: string(guard.DynamicCommand)},
	})
}

func TestJudge_AllowsACommandThatDefinesAnAlias(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "alias naming no tool", command: "alias ll='ls -l'; ll"},
		{name: "alias printing one", command: "alias ll"},
		{name: "alias naming git", command: "alias g=git; g status"},
		{name: "alias naming gh in a second operand", command: "alias ll='ls -l' ship='gh pr merge'; ship"},
		{name: "alias whose value is not a literal", command: `alias ship="$c"; ship`},
		{name: "alias starting with pushd", command: "alias hop='pushd sub'; hop; git status"},
		{name: "alias starting with a wrapper", command: "alias hop='builtin cd'; hop sub; git status"},
	})
}

func TestJudge_WhenAllexportIsOnAroundASource_AllowsGitStatus(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "set -a around a source", command: "set -a; . ./.env; set +a; git status"},
	})
}

func TestParse_WhenAllexportIsOn_MarksNoGitVariableABuiltinAssigns(t *testing.T) {
	t.Parallel()

	tests := []namedCommand{
		{name: "read after set -a", command: "set -a; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read after setopt allexport", command: "setopt allexport; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "assign-default expansion after set -a", command: `set -a; echo "${GIT_CONFIG_PARAMETERS:=x}"; git status`},
		{name: "read in a function called after set -a", command: "f() { read -r GIT_CONFIG_PARAMETERS; }; set -a; f; git status"},
		{name: "read in a bash -a string", command: "bash -a -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Assert(t, len(got) > 0, "Parse(%q) = %v", tt.command, texts(got))
			assert.Check(t, is.Len(marksOf(got), 0), "Parse(%q) marks", tt.command)
		})
	}
}
