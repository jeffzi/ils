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
		name    string
		command string
	}{
		{name: "stray closing parenthesis", command: "echo ok; rm -rf / )"},
		{name: "feature of another shell", command: "echo ok\n  echo ${(U)x}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := guard.Parse(t.Context(), tt.command, guard.Caller{Home: home})

			failure, ok := errors.AsType[*guard.ParseError](err)
			assert.Assert(t, ok, "Parse(%q) error = %v, want a *guard.ParseError", tt.command, err)
			assert.Check(t, failure.Message != "", "Parse(%q) carries no message", tt.command)
			position := fmt.Sprintf("%d:%d", failure.Line, failure.Column)
			assert.Check(t, !strings.HasPrefix(failure.Message, position), "Parse(%q) message %q repeats the position", tt.command, failure.Message)
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
		{
			name:    "command substitution beside a shell string whose pipeline moves HEAD",
			command: "bash -c 'git switch x | cat' x $(git commit)",
			want:    [][]string{{"bash", "-c", "git switch x | cat", "x", ""}, {"git", "switch", "x"}, {"cat"}, {"git", "commit"}},
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
		{name: "redirect", command: "git status > out", want: []bool{false}},
		{name: "background", command: "git status &", want: []bool{false}},
		{name: "assignment prefix", command: "GIT_DIR=x git status", want: []bool{false}},
		{name: "escaped command word", command: `\git status`, want: []bool{false}},
		{name: "subshell", command: "(git status)", want: []bool{false}},
		{name: "nested substitution", command: `git log "$(git rev-parse HEAD)"`, want: []bool{false, false}},
		{name: "assignment only", command: "GIT_DIR=/tmp/x", want: []bool{false}},
		{name: "wrapper", command: "nice git status", want: []bool{false}},
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

			for _, inv := range got {
				assert.Check(t, is.Len(inv.Marks, 0), "Parse(%q) invocation %v marks", tt.command, inv.Words)
			}
		})
	}
}

func TestJudge_WhenAHandedStringThatDoesNotParseHoldsAToolWord_RefusesNamingBoth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		runner  string
		tool    string
	}{
		{name: "python string running git by its path", command: `python3 -c "os.system('/usr/bin/git push')"`, runner: "python3", tool: "/usr/bin/git"},
		{name: "remote shell line behind a data consumer word", command: "ssh host echo 'fi; git push'", runner: "ssh", tool: "git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := guard.Judge(t.Context(), tt.command, guard.Caller{Home: home, Dir: start})

			assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", tt.command)
			assert.Check(t, is.Contains(got.Reason, tt.runner), "Judge(%q) reason", tt.command)
			assert.Check(t, is.Contains(got.Reason, tt.tool), "Judge(%q) reason", tt.command)
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

func TestJudge_AllowsAnAliasThatHidesNoTool(t *testing.T) {
	t.Parallel()

	checkAllowed(t, guard.Caller{Home: home, Dir: start}, []namedCommand{
		{name: "alias naming no tool", command: "alias ll='ls -l'; ll"},
		{name: "alias listing", command: "alias"},
		{name: "alias printing one", command: "alias ll"},
	})
}

func TestJudge_RefusesAnAliasWhoseValueNamesATool(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "gh", command: "alias ship='gh pr merge'; ship", wantNamed: "ship"},
		{name: "tool word alone", command: "alias ship=git; ship push", wantNamed: "ship"},
		{name: "tool behind a wrapper", command: "alias ship='env git push'; ship", wantNamed: "ship"},
		{name: "second operand", command: "alias ll='ls -l' ship='git push'; ship", wantNamed: "ship"},
		{name: "non-literal value", command: `alias ship="$c"; ship`, wantNamed: "ship"},
	})
}

func TestJudge_RefusesAnAliasWhoseValueStartsWithACommandTheGuardReadsThrough(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "pushd", command: "alias hop='pushd sub'; hop; git commit -m x", wantNamed: "hop"},
		{name: "popd", command: "alias hop=popd; pushd sub; hop; git commit -m x", wantNamed: "hop"},
		{name: "wrapper", command: "alias hop='builtin cd'; hop sub; git commit -m x", wantNamed: "hop"},
		{name: "second operand", command: "alias ll='ls -l' hop=cd; hop sub; git commit -m x", wantNamed: "hop"},
	})
}

func TestJudge_WhenAPushIsSubstitutedBesideAShellStringHoldingAPipeline_RefusesIt(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "bash -c string", command: "bash -c 'echo a | cat' x $(git push origin main)", wantNamed: "git push"},
	})
}
