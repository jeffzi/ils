package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/guard"
)

func TestParse_WhenQuoteRemovalLeavesPlainText_ReportsALiteral(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		word string
		want string
	}{
		{name: "backslash escape", word: `\git`, want: "git"},
		{name: "double quotes", word: `"git"`, want: "git"},
		{name: "single quotes", word: `'stash'`, want: "stash"},
		{name: "quotes inside a word", word: `g"i"t`, want: "git"},
		{name: "escaped dollar in double quotes", word: `"a\$x"`, want: "a$x"},
		{name: "dollar in single quotes", word: `'$x'`, want: "$x"},
		{name: "escaped glob character", word: `\*`, want: "*"},
		{name: "quoted glob character", word: `"*.go"`, want: "*.go"},
		{name: "bare tilde", word: `~`, want: home},
		{name: "tilde path", word: `~/x`, want: home + "/x"},
		{name: "ansi-c quotes", word: `$'git'`, want: "git"},
		{name: "ansi-c escape", word: `$'\x67it'`, want: "git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			command := "echo " + tt.word

			got := only(t, command)

			assert.Assert(t, is.Len(got.Words, 2), "Parse(%q)", command)
			assert.Check(t, is.DeepEqual(got.Words[1], guard.Word{Text: tt.want, Literal: true}), "Parse(%q)", command)
		})
	}
}

func TestParse_WhenAWordCarriesAnExpansion_ReportsItNotLiteralWithItsLiteralPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		word       string
		wantPrefix string
	}{
		{name: "parameter expansion after a literal", word: `--grep=$x`, wantPrefix: "--grep="},
		{name: "braced parameter expansion", word: `${x}`, wantPrefix: ""},
		{name: "parameter expansion in double quotes", word: `"a$x"`, wantPrefix: "a"},
		{name: "command substitution", word: `a$(b)`, wantPrefix: "a"},
		{name: "backtick substitution", word: "a`b`", wantPrefix: "a"},
		{name: "arithmetic", word: `$((1+2))`, wantPrefix: ""},
		{name: "process substitution", word: `<(ls)`, wantPrefix: ""},
		{name: "brace expansion", word: `-{f,d}`, wantPrefix: "-"},
		{name: "brace sequence", word: `x{1..3}`, wantPrefix: "x"},
		{name: "star glob", word: `src/*.go`, wantPrefix: "src/"},
		{name: "question mark glob", word: `a?`, wantPrefix: "a"},
		{name: "bracket glob", word: `[ab]`, wantPrefix: ""},
		{name: "extended glob", word: `@(a|b)`, wantPrefix: ""},
		{name: "tilde with a user name", word: `~user/x`, wantPrefix: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			command := "echo " + tt.word

			// A substitution's own command is an invocation of its own, after the echo.
			got := parse(t, command)[0]

			assert.Assert(t, is.Len(got.Words, 2), "Parse(%q)", command)
			assert.Check(t, is.DeepEqual(got.Words[1], guard.Word{Text: tt.wantPrefix, Literal: false}), "Parse(%q)", command)
		})
	}
}
