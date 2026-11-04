//go:build unix

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
		{name: "shell reading a pipe", command: "cat x | bash", index: 1, want: guard.HiddenScript},
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

func TestParse_WhenGoIsGivenACommandToRunItsTools_ReadsTheCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "toolexec value", command: `go build -toolexec "sh -c 'git push'" .`},
		{name: "exec value joined by an equals sign", command: "go test -exec='git push' ."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := gitRun(t, tt.command)

			assert.Check(t, is.DeepEqual(wordTexts(got.Words), []string{"git", "push"}), "Parse(%q) git words", tt.command)
		})
	}
}

func TestParse_WhenTheGoCommandToRunIsNotALiteral_MarksADynamicScript(t *testing.T) {
	t.Parallel()

	const command = `go build -toolexec "$T" .`

	got := only(t, command)

	assert.Check(t, is.DeepEqual(kinds(got.Marks), []guard.MarkKind{guard.DynamicScript}), "Parse(%q) marks", command)
}

func TestJudge_WhenTheGoCommandToRunDoesNotParse_RefusesNamingTheFlag(t *testing.T) {
	t.Parallel()

	const command = "go test -exec 'wrap )' ."

	checkJudgedRow(t, readerCaller(), judged{command: command, want: guard.Refuse, wantNamed: "-exec"})
}

func TestParse_WhenAGoCommandToRunIsPastTheThirdLevel_MarksItUnparsed(t *testing.T) {
	t.Parallel()

	const command = "eval eval eval go build -toolexec 'git push'"

	got := last(t, command)

	assert.Check(t, is.DeepEqual(got.Marks, []guard.Mark{{Kind: guard.DeepScript, Name: "go -toolexec"}}), "Parse(%q) marks", command)
}

func TestJudge_WhenAShellsOptionsRunAStringOrAFile_RefusesIt(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "bash rcfile word after a short option is a bundle", command: "bash -e -rcfile 'git push --force origin main' -c true", wantNamed: "git push"},
		{name: "bash rcfile read under -i", command: "bash -rcfile ./evil.sh -i -c true"},
		{name: "bash init-file read under -i", command: "bash -init-file ./evil.sh -i -c true"},
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
		{name: "bash with an absolute path", command: "bash /tmp/probe.sh"},
		{name: "bash with an option before the file", command: "bash -e x.sh"},
		{name: "zsh with a non-literal path", command: `zsh "$S/probe.sh"`},
		{name: "literal sh string among the file's arguments", command: "bash x.sh sh -c 'make test'"},
		// cspell:disable-next-line -- zsh option name glued to -o, the form under test
		{name: "zsh o takes the rest of its bundle", command: `zsh -ocshnullglob "$s" true`},
		{name: "bash lone dash makes a later -c a file name", command: `bash - -c "$s"`},
		{name: "zsh lone plus makes a later -c a file name", command: `zsh + -c "$s"`},
	})
}

func TestJudge_WhenAShellReadsItsScriptFromStandardInputOrASubstitution_RefusesIt(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "shell reading a pipe", command: "cat x | bash", wantNamed: "`bash x.sh`"},
		{name: "bash -s with a script file", command: "bash -s x.sh", wantNamed: string(guard.HiddenScript)},
		{name: "process substitution as the file", command: "bash <(cat x)", wantNamed: string(guard.HiddenScript)},
		{name: "source of a process substitution", command: "source <(cat x)", wantNamed: string(guard.HiddenScript)},
		{name: "process substitution after an option", command: "bash -e <(cat x)", wantNamed: string(guard.HiddenScript)},
		{name: "standard input named as the file", command: "cat x | bash /dev/stdin", wantNamed: string(guard.HiddenScript)},
		{name: "standard input named by its descriptor", command: "cat x | sh /dev/fd/0", wantNamed: string(guard.HiddenScript)},
	})
}

func TestJudge_WhenAShellGivesItsScriptFileAToolWord_RefusesIt(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "tool word after the file", command: "bash x.sh git push", wantNamed: string(guard.ToolArgument)},
		{name: "tool in a string after the file", command: "sh ./run.sh 'git push'", wantNamed: string(guard.ToolArgument)},
	})
}

func TestJudge_WhenAShellsOptionsRunNothingHidden_AllowsIt(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "bash rcfile after a one-dash long option", command: "bash -login -rcfile 'git push --force origin main' -c true"},
		{name: "sh rcfile not read without -i", command: "sh -rcfile 'git push --force origin main' -c true"},
		{name: "sh rcfile not read even with -i", command: "sh -rcfile ./evil.sh -i -c true"},
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
		{name: "pager exported earlier in the list", command: "export GIT_PAGER='git push origin main'; git log", want: []string{"git", "push", "origin", "main"}},
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
		{name: "pager past the third level", command: "eval eval eval GIT_PAGER=less git log", index: 3, want: guard.DeepScript},
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
