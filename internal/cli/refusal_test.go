package cli_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

const (
	// jsonNeither is the reason a JSON flag set to maybe is refused for.
	jsonNeither = "--json takes true or false, and maybe was given"

	// nextHelp is the Next step of every grammar refusal.
	nextHelp = "ils --help"
)

// sameRun lets cmp compare the unexported fields of ran.
func sameRun() cmp.Option {
	return cmp.AllowUnexported(ran{})
}

// grammarRefusal is a command line that breaks the grammar, and the reason ils refuses it with.
type grammarRefusal struct {
	name   string
	reason string
	args   []string
}

// grammarRefusals are the command lines that break the grammar: a missing, unknown or extra word,
// a command not in this version, and a flag that is unknown or set to neither true nor false.
func grammarRefusals() []grammarRefusal {
	return []grammarRefusal{
		{name: "unknown word", args: []string{"pish"}, reason: "pish is not a command"},
		{name: "extra word", args: []string{"push", "extra"}, reason: "push takes no other word, and extra was given"},
		{name: "end of flags", args: []string{"--", "push"}, reason: "-- is not a command"},
		{name: "end of flags after push", args: []string{"push", "--", "x"}, reason: "push takes no other word, and -- was given"},
		{name: "command not in this version", args: []string{"branch", "tidy"}, reason: "branch is not in this version"},
		{name: "unknown flag", args: []string{"--bogus"}, reason: "--bogus is not a command"},
		{name: "version flag set to false", args: []string{"--version=false"}, reason: "no command was given"},
		{name: "version flag before an extra word", args: []string{"--version", "extra"}, reason: "extra is not a command"},
		{name: "unknown flag after push", args: []string{"push", "--bogus"}, reason: "push takes no other word, and --bogus was given"},
		{name: "group of short flags after push", args: []string{"push", "-xyz"}, reason: "push takes no other word, and -xyz was given"},
		{name: "JSON flag after push set to maybe", args: []string{"push", "--json=maybe"}, reason: jsonNeither},
	}
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
