//go:build unix

package guard_test

import (
	"path/filepath"
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
		{name: "-c after a long option", command: "dash --login -c 'git push'", want: [][]string{{"dash", "--login", "-c", "git push"}, {"git", "push"}}},
		{name: "heredoc read under -s given arguments", command: "bash -s arg <<EOF\ngit push\nEOF", want: [][]string{{"bash", "-s", "arg"}, {"git", "push"}}},
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

func TestParse_WhenAShellRunsAScriptItCannotRead_MarksIt(t *testing.T) {
	t.Parallel()

	checkMarkKind(t, []markedCommand{
		{name: "non-literal -c string", command: `bash -c "$CMD"`, want: guard.DynamicScript},
		{name: "non-literal eval word", command: `eval "$x"`, want: guard.DynamicScript},
		{name: "non-literal heredoc fed to a shell", command: "bash <<EOF\n$x\nEOF", want: guard.DynamicScript},
	})
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

func TestJudge_WhenGoRunsAProgramGivenAToolWord_RefusesIt(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "bare git among go run's arguments", command: "go run ./tool git push", wantNamed: "go runs git"},
	})
}

func TestJudge_WhenAShellsOptionsRunAStringOrAFile_RefusesIt(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "bash rcfile word after a short option is a bundle", command: "bash -e -rcfile 'git push --force origin main' -c true", wantNamed: "git push"},
		{name: "bash -o takes the next word as its value", command: "bash -o pipefail -c 'git push --force origin main'", wantNamed: "git push"},
		{name: "zsh one-dash rcfile is a bundle", command: "zsh -rcfile 'git push --force origin main' -c true", wantNamed: "git push"},
		{name: "zsh one-dash login is a bundle", command: "zsh -login -c 'git push --force origin main'", wantNamed: "git push"},
		{name: "zsh O in a bundle takes no value", command: "zsh -cO 'git push --force origin main' true", wantNamed: "git push"},
		{name: "bash lone dash ends the options before the -c string", command: "bash -c - 'git push --force origin main'", wantNamed: "git push"},
		{name: "bash lone plus leaves a later short option an option", command: "bash -c + -x 'git push --force origin main'", wantNamed: "git push"},
	})
}

func TestJudge_WhenAShellIsGivenAScriptFile_AllowsIt(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "literal sh string among the file's arguments", command: "bash x.sh sh -c 'make test'"},
		// cspell:disable-next-line -- zsh option name glued to -o, the form under test
		{name: "zsh o takes the rest of its bundle", command: `zsh -ocshnullglob "$s" true`},
		{name: "bash lone dash makes a later -c a file name", command: `bash - -c "$s"`},
		{name: "zsh lone plus makes a later -c a file name", command: `zsh + -c "$s"`},
	})
}

func TestJudge_WhenAShellReadsItsScriptFromOutsideTheCommand_AllowsIt(t *testing.T) {
	t.Parallel()

	checkAllowed(t, readerCaller(), []namedCommand{
		{name: "tool string piped into bash", command: "echo 'git push' | bash"},
	})
}

func TestJudge_WhenGitRunsAfterASourceOfASubstitution_RefusesForTheUnknownDirectory(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "commit", command: "source <(echo x); git commit -m x", wantNamed: "cannot tell where the command runs"},
	})
}

func TestJudge_WhenAShellGivesItsScriptFileAToolWord_RefusesIt(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "tool word after the file", command: "bash x.sh git push", wantNamed: string(guard.ToolArgument)},
		{name: "tool in a string after the file", command: "sh ./run.sh 'git push'", wantNamed: string(guard.ToolArgument)},
	})
}

func TestJudge_WhenAStringRedirectsAroundACd_RefusesGitHistoryWrittenWhereTheStringStarts(t *testing.T) {
	t.Parallel()

	f, _ := standingOn(t, "main", "topic")
	caller := callerIn(f, filepath.Dir(f.Dir()))
	inner := "'(cd " + filepath.ToSlash(outsideDir(t)) + " && git -C " + filepath.ToSlash(f.Dir()) + " show HEAD:go.mod) > go.mod'"
	named := "writes over " + filepath.Join(f.Dir(), "go.mod")

	checkJudged(t, caller, []judged{
		{name: "sh -c", command: "cd repo && sh -c " + inner, want: guard.Refuse, wantNamed: named},
		{name: "eval", command: "cd repo && eval " + inner, want: guard.Refuse, wantNamed: named},
	})
}

func TestJudge_WhenAShellsOptionsRunNothingHidden_AllowsIt(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "bash rcfile after a one-dash long option", command: "bash -login -rcfile 'git push --force origin main' -c true"},
		{name: "sh rcfile not read without -i", command: "sh -rcfile 'git push --force origin main' -c true"},
		{name: "bash two-dash rcfile takes its value", command: "bash --rcfile x -c 'git status'"},
	})
}

func TestParse_WhenAGitVariableNamesACommandGitRuns_ParsesItsCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{name: "sequence editor as an assignment prefix", command: "GIT_SEQUENCE_EDITOR='git push origin main; true' git rebase -i HEAD~1", want: []string{"git", "push", "origin", "main"}},
		{name: "editor exported earlier in the list", command: "export GIT_EDITOR='git push origin main'; git commit", want: []string{"git", "push", "origin", "main"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Check(t, is.Contains(texts(got), tt.want), "Parse(%q)", tt.command)
		})
	}
}

func TestParse_WhenACommandAGitVariableNamesCannotBeRead_MarksIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    guard.MarkKind
		index   int
	}{
		{name: "non-literal editor", command: `GIT_EDITOR="$e" git commit`, want: guard.DynamicScript},
		{name: "editor past the third level", command: "eval eval eval GIT_EDITOR=vim git commit", index: 3, want: guard.DeepScript},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Assert(t, len(got) > tt.index, "Parse(%q) = %v", tt.command, texts(got))
			assert.Check(t, is.DeepEqual(got[tt.index].Marks, []guard.Mark{{Kind: tt.want}}), "Parse(%q) marks", tt.command)
		})
	}
}
