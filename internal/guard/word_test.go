//go:build unix

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
		{name: "escaped glob character", word: `\*`, want: "*"},
		{name: "quoted glob character", word: `"*.go"`, want: "*.go"},
		{name: "bare tilde", word: `~`, want: home},
		{name: "tilde path", word: `~/x`, want: home + "/x"},
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
		{name: "parameter expansion in double quotes", word: `"a$x"`, wantPrefix: "a"},
		{name: "brace expansion", word: `-{f,d}`, wantPrefix: "-"},
		{name: "bracket glob", word: `[ab]`, wantPrefix: ""},
		{name: "tilde with a user name", word: `~user/x`, wantPrefix: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			command := "echo " + tt.word

			got := only(t, command)

			assert.Assert(t, is.Len(got.Words, 2), "Parse(%q)", command)
			assert.Check(t, is.DeepEqual(got.Words[1], guard.Word{Text: tt.wantPrefix, Literal: false}), "Parse(%q)", command)
		})
	}
}
