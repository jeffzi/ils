package cli_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

// grammarRefusal is a command line that breaks the grammar, and the reason ils refuses it with.
type grammarRefusal struct {
	name   string
	reason string
	args   []string
}

// shellAndManRefusals are the command lines that break the grammar of completion and man: no shell,
// a word that is no shell, and a word, an end of flags or an unknown flag after completion, after a
// shell or after man.
func shellAndManRefusals() []grammarRefusal {
	return []grammarRefusal{
		{name: "completion with no shell", args: []string{"completion"}, reason: "no shell was given"},
		{name: "unknown shell", args: []string{"completion", "teapot"}, reason: "teapot is not a shell"},
		{name: "extra word after a shell", args: []string{"completion", "bash", "extra"}, reason: "completion takes one shell, and extra was given"},
		{name: "end of flags after completion", args: []string{"completion", "--"}, reason: "-- is not a shell"},
		{name: "end of flags after a shell", args: []string{"completion", "bash", "--"}, reason: "completion takes one shell, and -- was given"},
		{name: "unknown flag after completion", args: []string{"completion", "--bogus"}, reason: "completion takes one shell, and --bogus was given"},
		{name: "unknown flag after a shell", args: []string{"completion", "zsh", "--bogus"}, reason: "completion takes one shell, and --bogus was given"},
		{name: "extra word after man", args: []string{"man", "extra"}, reason: "man takes no other word, and extra was given"},
		{name: "end of flags after man", args: []string{"man", "--"}, reason: "man takes no other word, and -- was given"},
		{name: "unknown flag after man", args: []string{"man", "--bogus"}, reason: "man takes no other word, and --bogus was given"},
	}
}

// grammarRefusals are the command lines that break the grammar: a missing, unknown or extra word,
// a command not in this version, and a flag that is unknown or set to neither true nor false.
func grammarRefusals() []grammarRefusal {
	return slices.Concat(shellAndManRefusals(), []grammarRefusal{
		{name: "no word", reason: "no command was given"},
		{name: "unknown word", args: []string{"pish"}, reason: "pish is not a command"},
		{name: "extra word", args: []string{"push", "extra"}, reason: "push takes no other word, and extra was given"},
		{name: "end of flags after push", args: []string{"push", "--", "x"}, reason: "push takes no other word, and -- was given"},
		{name: "branch", args: []string{"branch", "tidy"}, reason: "branch is not in this version"},
		{name: "release", args: []string{"release", "1.2.3"}, reason: "release is not in this version"},
		{name: "status", args: []string{"status"}, reason: "status is not in this version"},
		{name: "prefix of a command", args: []string{"pus"}, reason: "pus is not a command"},
		{name: "later command with an unknown flag", args: []string{"branch", "--bogus"}, reason: "branch is not in this version"},
		{name: "extra word after merge", args: []string{"merge", "extra"}, reason: "merge takes no other word, and extra was given"},
		{name: "end of flags after merge", args: []string{"merge", "--", "x"}, reason: "merge takes no other word, and -- was given"},
		{name: "unknown flag after merge", args: []string{"merge", "--bogus"}, reason: "merge takes no other word, and --bogus was given"},
		{name: "unknown short flag after merge", args: []string{"merge", "-x"}, reason: "merge takes no other word, and -x was given"},
		{name: "version flag after merge", args: []string{"merge", "--version"}, reason: "merge takes no other word, and --version was given"},
		{name: "later command with a word and a short flag", args: []string{"branch", "tidy", "-x"}, reason: "branch is not in this version"},
		{name: "later command with an unknown flag after a word", args: []string{"status", "tidy", "--bogus"}, reason: "status is not in this version"},
		{name: "later command with a short flag", args: []string{"release", "-x"}, reason: "release is not in this version"},
		{name: "unknown flag", args: []string{"--bogus"}, reason: "--bogus is not a command"},
		{name: "unknown short flag", args: []string{"-x"}, reason: "-x is not a command"},
		{name: "short version flag", args: []string{"-v"}, reason: "-v is not a command"},
		{name: "version flag set to false", args: []string{"--version=false"}, reason: "no command was given"},
		{name: "version flag before push", args: []string{"--version", "push"}, reason: "push takes no other word, and --version was given"},
		{name: "version flag before an extra word", args: []string{"--version", "extra"}, reason: "extra is not a command"},
		{name: "unknown flag after push", args: []string{"push", "--bogus"}, reason: "push takes no other word, and --bogus was given"},
		{name: "unknown short flag after push", args: []string{"push", "-x"}, reason: "push takes no other word, and -x was given"},
		{name: "group of short flags after push", args: []string{"push", "-xyz"}, reason: "push takes no other word, and -xyz was given"},
		{name: "version flag after push", args: []string{"push", "--version"}, reason: "push takes no other word, and --version was given"},
		{name: "short version flag after push", args: []string{"push", "-v"}, reason: "push takes no other word, and -v was given"},
		{name: "unknown flag after an extra word", args: []string{"push", "extra", "--bogus"}, reason: "push takes no other word, and --bogus was given"},
		{name: "unknown flag after an unknown word", args: []string{"pish", "--bogus"}, reason: "--bogus is not a command"},
		{name: "flag set to false", args: []string{"pish", "--json=false"}, reason: "pish is not a command"},
		{name: "debug flag after an unknown word", args: []string{"pish", "-d"}, reason: "pish is not a command"},
		{name: "debug flag after an extra word", args: []string{"push", "extra", "--debug"}, reason: "push takes no other word, and extra was given"},
		{name: "debug and color flags alone", args: []string{"-d", "--no-color"}, reason: "no command was given"},
		{name: "version flag set to maybe", args: []string{"--version=maybe"}, reason: versionNeither},
		{name: "JSON flag after push set to maybe", args: []string{"push", "--json=maybe"}, reason: jsonNeither},
		{name: "debug flag set to maybe", args: []string{"--debug=maybe"}, reason: debugNeither},
		{name: "debug flag after push set to maybe", args: []string{"push", "--debug=maybe"}, reason: debugNeither},
		{name: "debug flag after merge set to maybe", args: []string{"merge", "--debug=maybe"}, reason: debugNeither},
		{name: "color flag set to maybe", args: []string{"--no-color=maybe"}, reason: colorNeither},
		{name: "color flag after push set to maybe", args: []string{"push", "--no-color=maybe"}, reason: colorNeither},
		{name: "color flag after merge set to maybe", args: []string{"merge", "--no-color=maybe"}, reason: colorNeither},
	})
}

func TestApp_Run_WhenWordsBreakTheGrammar_RefusesNamingWhatBreaksIt(t *testing.T) {
	t.Parallel()

	for _, tt := range grammarRefusals() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			want := ran{stdout: "Refused: " + tt.reason + "\nNext: " + nextHelp + "\n", code: exitRefused}
			assert.Check(t, is.DeepEqual(got, want, sameRun()), "ils %s", strings.Join(tt.args, " "))
		})
	}
}

func TestApp_Run_WhenCompletionOrManBreaksTheGrammarWithColorOn_RefusesNamingWhatBreaksIt(t *testing.T) {
	t.Parallel()

	for _, tt := range shellAndManRefusals() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, painted(t, []string{colorTerm}, true, false), tt.args...)

			assert.Check(t, is.Equal(got.code, exitRefused))
			assert.Check(t, is.Equal(got.stderr, ""))
			assert.Check(t, is.Equal(ansi.Strip(got.stdout), "Refused: "+tt.reason+"\nNext: "+nextHelp+"\n"))
		})
	}
}
