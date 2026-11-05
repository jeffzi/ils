//go:build unix

package guard_test

import (
	"os"
	"path/filepath"
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

	checkMarkKind(t, []markedCommand{
		{name: "wrapper option it does not take", command: "nice -z git push", want: guard.ToolArgument},
		{name: "shell handed to xargs", command: "xargs sh -c 'git push origin main'", want: guard.ToolArgument},
		{name: "shell whose string hands git over, handed to xargs", command: "xargs sh -c 'sudo git log'", want: guard.ToolArgument},
		{name: "shell whose string runs a shell, handed to xargs", command: "xargs sh -c 'sh -c ls'", want: guard.ToolArgument},
		{name: "shell with no -c string, handed to xargs", command: "xargs sh", want: guard.ToolArgument},
		{name: "shell with a non-literal -c string, handed to xargs", command: `xargs bash -c "$s"`, want: guard.ToolArgument},
		{name: "shell with another option before its string, handed to xargs", command: "xargs bash -e -c ls", want: guard.ToolArgument},
		{name: "shell with -c and no string, handed to xargs", command: "xargs sh -c", want: guard.ToolArgument},
		{name: "shell string among an interpreter's arguments", command: "python3 tool.py sh -c 'git push'", want: guard.ToolArgument},
		{name: "eval handed to another command", command: "xargs eval 'git status'", want: guard.ToolArgument},
		{name: "tool behind a wrapper in a string", command: "su -c 'exec git push'", want: guard.ToolArgument},
		{name: "tool in a string with an expansion", command: `su -c "git push $b"`, want: guard.ToolArgument},
		{name: "ils with no command behind another command", command: "xargs ils", want: guard.ToolArgument},
		{name: "ils command that changes state in a string", command: "ssh host 'ils status && ils merge'", want: guard.ToolArgument},
		{name: "non-literal wrapper option", command: `env -"$o" git push`, want: guard.DynamicCommand},
	})
}

func TestParse_WhenAToolIsHandedToAnotherCommand_NamesItOnTheMark(t *testing.T) {
	t.Parallel()

	checkHandedTool(t, []handedTool{
		{name: "path to a tool", command: "find . -exec /bin/sh -c x \\;", want: "/bin/sh"},
		{name: "tool in a string", command: "su -c 'cd /repo && gh pr merge 3'", want: "gh"},
		{name: "tool handed on inside a string", command: "ssh host 'sudo ils merge'", want: "ils"},
		{name: "bare tool word among an interpreter's arguments", command: "python3 tool.py git push", want: "git"},
		{name: "bare tool word after an interpreter word behind another command", command: "timeout 30 python3 tool.py git push", want: "git"},
		{name: "bare tool word among a script file's arguments behind another command", command: "timeout 60 bash x.sh git push", want: "git"},
		{name: "shell word after an interpreter word behind another command", command: "timeout 30 python3 tool.py bash x.sh", want: "bash"},
		{name: "bare tool word among the positional parameters of a shell string, behind xargs", command: `xargs sh -c 'ls "$1"' sh git push`, want: "git"},
		{name: "code for a python3 word with no minor", command: "python3. -c 'import git'", want: "git"},
		{name: "code for a python3 word whose minor is not a number", command: "python3.x -c 'import git'", want: "git"},
	})
}

func TestParse_WhenNoArgumentHandsATool_MarksNothing(t *testing.T) {
	t.Parallel()

	checkNoMarks(t, []namedCommand{
		{name: "tool word in a tool's own arguments", command: "git commit -m git"},
		{name: "string whose command word is not a literal", command: `su -c "$g push"`},
		{name: "ils command that changes nothing in a string", command: "ssh host 'cd repo && ils status'"},
		{name: "ils command that only reads a command, behind another command", command: "timeout 60 ils parse --cwd . -- 'echo hi'"},
		{name: "go run with no tool word", command: "go run ./cmd/tool --verbose"},
		{name: "sed script that parses as a shell script", command: "sed -n '/git push/p' README.md"},
		{name: "code that parses as a shell script, for a versioned python", command: "python3.13 -c 'import git'"},
		{name: "code that does not parse, for a command the guard does not know", command: `php -r 'system("git push");'`},
	})
}

func TestParse_WhenAShellStringHandedToAnotherCommandHandsNoTool_MarksNothing(t *testing.T) {
	t.Parallel()

	checkNoMarks(t, []namedCommand{
		{name: "sh string behind xargs", command: "xargs -I{} sh -c 'ls {}'"},
		{name: "shell named as the zero argument of a sh string behind xargs", command: `xargs sh -c 'ls "$1"' sh`},
	})
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

	checkHandedTool(t, []handedTool{
		{name: "tool in a string handed to go generate", command: "go generate -run 'gh pr merge 3' ./...", want: "gh"},
		{name: "tool word handed to go run behind a joined global option", command: "go -C=dir run ./tools git push", want: "git"},
		{name: "tool in a string handed to go generate behind a global option", command: "go -C dir generate -run 'gh pr merge 3' ./...", want: "gh"},
		{name: "tool word behind a non-literal word where go reads its global option", command: `go "$o" . git push`, want: "git"},
	})
}

func TestParse_WhenARelativePathAfterACdIsNamedForATool_ReadsItWhereTheKernelOpensIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		// dir returns the directory the command moves into before naming ../bin/git.
		dir  func(t *testing.T) string
		name string
	}{
		{
			name: "link whose target sits beside the tool",
			dir: func(t *testing.T) string {
				root := outsideDir(t)
				writeExecutable(t, filepath.Join(root, "bin", "git"))

				return linkTo(t, subdirectory(t, root))
			},
		},
		{
			name: "directory that does not exist, beside the tool",
			dir: func(t *testing.T) string {
				root := outsideDir(t)
				writeExecutable(t, filepath.Join(root, "bin", "git"))

				return filepath.Join(root, "absent")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			command := "cd " + quotedPath(tt.dir(t)) + " && xargs ../bin/git push"

			got := last(t, command)

			assert.Check(t, is.DeepEqual(kinds(got.Marks), []guard.MarkKind{guard.ToolArgument}), "Parse(%q) marks", command)
		})
	}
}

// handedTool is a command whose one invocation carries one ToolArgument mark naming want.
type handedTool struct {
	name    string
	command string
	want    string
}

func checkHandedTool(t *testing.T, tests []handedTool) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.DeepEqual(got.Marks, []guard.Mark{{Kind: guard.ToolArgument, Name: tt.want}}), "Parse(%q) marks", tt.command)
		})
	}
}

func TestJudge_WhenFindRunsASedScriptThatNamesATool_AllowsIt(t *testing.T) {
	t.Parallel()

	checkAllowed(t, guard.Caller{Home: home, Dir: outsideDir(t)}, []namedCommand{
		{name: "sed script behind find -exec", command: `find . -name '*.md' -exec sed -i 's/git push/ils push/' {} +`},
	})
}

func TestJudge_WhenAShellScriptHandedToAnotherCommandRunsGit_Refuses(t *testing.T) {
	t.Parallel()

	caller := guard.Caller{Home: home, Dir: outsideDir(t)}

	checkJudged(t, caller, []judged{
		{name: "interpreter arguments ssh hands to a shell", command: "ssh host sed 's/x/; git push/' f", want: guard.Refuse},
	})
}

func TestJudge_WhenACommandHandsAnotherCommandAShellWithNoScriptFile_Refuses(t *testing.T) {
	t.Parallel()

	caller := guard.Caller{Home: home, Dir: outsideDir(t)}

	checkJudged(t, caller, []judged{
		{name: "shell reading /dev/stdin behind timeout", command: "timeout 60 bash /dev/stdin", wantNamed: "bash", want: guard.Refuse},
		{name: "shell reading a process substitution behind timeout", command: "timeout 60 bash <(cat x)", wantNamed: "bash", want: guard.Refuse},
	})
}

// executable writes an executable file named name in a new directory and returns its path.
func executable(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	writeExecutable(t, path)

	return path
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
