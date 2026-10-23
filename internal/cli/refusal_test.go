package cli_test

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

func TestApp_Run_WhenWordsBreakTheGrammar_RefusesNamingTheWord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
		args   []string
	}{
		{name: "no word", reason: "no command was given"},
		{name: "unknown word", args: []string{"pish"}, reason: "pish is not a command"},
		{name: "extra word", args: []string{"push", "extra"}, reason: "push takes no other word, and extra was given"},
		{name: "end of flags after push", args: []string{"push", "--", "x"}, reason: "push takes no other word, and -- was given"},
		{name: "branch", args: []string{"branch", "tidy"}, reason: "branch is not in this version"},
		{name: "release", args: []string{"release", "1.2.3"}, reason: "release is not in this version"},
		{name: "status", args: []string{"status"}, reason: "status is not in this version"},
		{name: "prefix of a command", args: []string{"pus"}, reason: "pus is not a command"},
		{name: "completion", args: []string{"completion"}, reason: "completion is not a command"},
		{name: "man", args: []string{"man"}, reason: "man is not a command"},
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			want := ran{stdout: "Refused: " + tt.reason + "\nNext: " + nextHelp + "\n", code: exitRefused}
			assert.Check(t, is.DeepEqual(got, want, sameRun()), "ils %s", strings.Join(tt.args, " "))
		})
	}
}

func TestApp_Run_WhenAFlagIsSetToNeitherTrueNorFalse_RefusesNamingTheFlagAndTheValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
		args   []string
	}{
		{name: "version flag", args: []string{"--version=maybe"}, reason: versionNeither},
		{name: "JSON flag after push", args: []string{"push", "--json=maybe"}, reason: jsonNeither},
		{name: "debug flag", args: []string{"--debug=maybe"}, reason: debugNeither},
		{name: "debug flag after push", args: []string{"push", "--debug=maybe"}, reason: debugNeither},
		{name: "debug flag after merge", args: []string{"merge", "--debug=maybe"}, reason: debugNeither},
		{name: "color flag", args: []string{"--no-color=maybe"}, reason: colorNeither},
		{name: "color flag after push", args: []string{"push", "--no-color=maybe"}, reason: colorNeither},
		{name: "color flag after merge", args: []string{"merge", "--no-color=maybe"}, reason: colorNeither},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			want := ran{stdout: "Refused: " + tt.reason + "\nNext: " + nextHelp + "\n", code: exitRefused}
			assert.Check(t, is.DeepEqual(got, want, sameRun()), "ils %s", strings.Join(tt.args, " "))
		})
	}
}
