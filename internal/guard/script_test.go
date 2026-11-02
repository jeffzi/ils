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

	got := guard.Judge(t.Context(), command, guard.Caller{Home: home, Dir: start})

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Contains(got.Reason, "-exec"), "Judge(%q) reason", command)
}

func TestJudge_WhenAShellStringHandsGoACommandToRunItsTools_RefusesTheCommand(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "toolexec value in a bash -c string", command: `bash -c "go build -toolexec 'sh -c \"git push\"'"`, wantNamed: "git push"},
	})
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
		{name: "bash rcfile word after -c is a bundle", command: "bash -c -rcfile 'git push --force origin main' true", wantNamed: "git push"},
		{name: "bash short option ends the run of long options", command: "bash -login -e -rcfile 'git push --force origin main' -c true", wantNamed: "git push"},
		{name: "sh rcfile word after a short option is a bundle", command: "sh -e -rcfile 'git push --force origin main' -c true", wantNamed: "git push"},
		{name: "bash -c after a one-dash long option", command: "bash -norc -c 'git push --force origin main'", wantNamed: "git push"},
		{name: "bash rcfile value that starts with a dash", command: "bash -rcfile -c 'git push --force origin main'"},
		{name: "bash rcfile read under -i", command: "bash -rcfile ./evil.sh -i -c true"},
		{name: "bash init-file read under -i", command: "bash -init-file ./evil.sh -i -c true"},
		{name: "bash rcfile read under a bundle holding i", command: "bash -rcfile ./evil.sh -ic true"},
		{name: "zsh one-dash rcfile is a bundle", command: "zsh -rcfile 'git push --force origin main' -c true", wantNamed: "git push"},
		{name: "zsh one-dash rcfile after -c is a bundle", command: "zsh -c -rcfile 'git push --force origin main' true", wantNamed: "git push"},
		{name: "zsh one-dash login is a bundle", command: "zsh -login -c 'git push --force origin main'", wantNamed: "git push"},
		{name: "zsh O in a bundle takes no value", command: "zsh -cO 'git push --force origin main' true", wantNamed: "git push"},
		{name: "zsh -O takes no value", command: "zsh -O ./evil.sh -c true"},
		{name: "zsh +O takes no value", command: "zsh +O ./evil.sh -c true"},
		// cspell:disable-next-line -- zsh option name glued to -o, the form under test
		{name: "zsh o takes the rest of its bundle", command: "zsh -ocshnullglob ./evil.sh true"},
		{name: "dash one-dash rcfile is a bundle", command: "dash -rcfile 'git push --force origin main' -c true", wantNamed: "git push"},
		{name: "bash lone dash ends the options before the -c string", command: "bash -c - 'git push --force origin main'", wantNamed: "git push"},
		{name: "sh lone dash ends the options before the -c string", command: "sh -c - 'git push --force origin main'", wantNamed: "git push"},
		{name: "zsh lone dash ends the options before the -c string", command: "zsh -c - 'git push --force origin main'", wantNamed: "git push"},
		{name: "dash lone dash ends the options before the -c string", command: "dash -c - 'git push --force origin main'", wantNamed: "git push"},
		{name: "bash lone dash ends the options of a bundle holding c", command: "bash -ce - 'git push --force origin main'", wantNamed: "git push"},
		{name: "bash lone dash makes a later -c a file name", command: "bash - -c 'git status'"},
		{name: "bash lone plus before the -c string", command: "bash -c + 'git push --force origin main'", wantNamed: "git push"},
		{name: "sh lone plus before the -c string", command: "sh -c + 'git push --force origin main'", wantNamed: "git push"},
		{name: "zsh lone plus ends the options before the -c string", command: "zsh -c + 'git push --force origin main'", wantNamed: "git push"},
		{name: "dash lone plus before the -c string", command: "dash -c + 'git push --force origin main'", wantNamed: "git push"},
		{name: "bash lone plus before the -c string of a bundle holding c", command: "bash -ce + 'git push --force origin main'", wantNamed: "git push"},
		{name: "bash lone plus leaves a later short option an option", command: "bash -c + -x 'git push --force origin main'", wantNamed: "git push"},
		{name: "dash lone plus leaves a later short option an option", command: "dash -c + -x 'git push --force origin main'", wantNamed: "git push"},
		{name: "zsh lone plus makes a later -c a file name", command: "zsh + -c 'git status'"},
	})
}

func TestJudge_WhenAShellsOptionsRunNothingHidden_AllowsIt(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "bash rcfile not read without -i", command: "bash -rcfile 'git push --force origin main' -c true"},
		{name: "bash rcfile after a one-dash long option", command: "bash -login -rcfile 'git push --force origin main' -c true"},
		{name: "bash init-file not read without -i", command: "bash -init-file 'git push --force origin main' -c true"},
		{name: "sh rcfile not read without -i", command: "sh -rcfile 'git push --force origin main' -c true"},
		{name: "sh rcfile not read even with -i", command: "sh -rcfile ./evil.sh -i -c true"},
		{name: "bash rcfile file not read without -i", command: "bash -rcfile ./evil.sh -c true"},
		{name: "bash -c after a one-dash long option", command: "bash -norc -c 'git status'"},
		{name: "bash two-dash rcfile takes its value", command: "bash --rcfile x -c 'git status'"},
		// cspell:disable-next-line -- zsh option name glued to -o, the form under test
		{name: "zsh o taking the rest of its bundle", command: "zsh -oallexport -c 'git status'"},
		{name: "zsh o ending its bundle takes the next word", command: "zsh -co allexport 'git status'"},
		{name: "dash o takes the next word", command: "dash -eo errexit -c 'git status'"},
		{name: "bash lone dash before the -c string", command: "bash -c - 'git status'"},
		{name: "bash lone plus before the -c string", command: "bash -c + 'git status'"},
		{name: "bash lone plus leaves a later -c an option", command: "bash + -c 'git status'"},
	})
}
