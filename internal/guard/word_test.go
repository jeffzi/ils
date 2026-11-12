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
		{name: "single quotes", word: `'stash'`, want: "stash"},
		{name: "quotes inside a word", word: `g"i"t`, want: "git"},
		{name: "escaped dollar in double quotes", word: `"a\$x"`, want: "a$x"},
		{name: "escaped glob character", word: `\*`, want: "*"},
		{name: "trailing lone backslash", word: `a\`, want: `a\`},
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
		{name: "glob followed by a parameter expansion", word: `src/*$x`, wantPrefix: "src/"},
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

func TestParse_WhenAWordsOnlyUnreadablePartIsAGlob_ReportsAGlobWithItsLiteralPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		word       string
		wantPrefix string
	}{
		{name: "star glob", word: `src/*.go`, wantPrefix: "src/"},
		{name: "question mark glob", word: `src/?.go`, wantPrefix: "src/"},
		{name: "bracket glob", word: `src/[ab].go`, wantPrefix: "src/"},
		{name: "quoted prefix", word: `"my dir/"*`, wantPrefix: "my dir/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			command := "echo " + tt.word

			got := only(t, command)

			assert.Assert(t, is.Len(got.Words, 2), "Parse(%q)", command)
			assert.Check(t, is.DeepEqual(got.Words[1], guard.Word{Text: tt.wantPrefix, Glob: true}), "Parse(%q)", command)
		})
	}
}

func literal(text string) guard.Word {
	return guard.Word{Text: text, Literal: true}
}

func wordTexts(words []guard.Word) []string {
	got := make([]string, 0, len(words))
	for _, word := range words {
		got = append(got, word.Text)
	}

	return got
}
