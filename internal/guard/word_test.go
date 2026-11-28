package guard_test

import (
	"strings"
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
		{name: "single quotes", word: `'stash'`, want: "stash"},
		{name: "ANSI-C quotes", word: `$'stash'`, want: "stash"},
		{name: "quotes inside a word", word: `g"i"t`, want: "git"},
		{name: "escaped glob character", word: `\*`, want: "*"},
		{name: "quoted glob character", word: `"*.go"`, want: "*.go"},
		{name: "bare tilde", word: `~`, want: home},
		{name: "tilde path", word: `~/x`, want: home + "/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checkEchoWord(t, tt.word, literal(tt.want))
		})
	}
}

// checkEchoWord fails unless Parse reads `echo word` as echo with one argument equal to want.
func checkEchoWord(t *testing.T, word string, want guard.Word) {
	t.Helper()

	command := "echo " + word

	got := only(t, command)

	assert.Assert(t, is.Len(got.Words, 2), "Parse(%q)", command)
	assert.Check(t, is.DeepEqual(got.Words[1], want), "Parse(%q)", command)
}

func TestParse_WhenAnUnquotedExpansionWouldSplitOrGlob_ReadsTheWordUnknown(t *testing.T) {
	t.Parallel()

	checkReadUnknown(t, []namedCommand{
		{name: "unquoted expansion of a value holding a space", command: "S='/tmp/my work'; $S/run.sh"},
		{name: "unquoted expansion of a value holding a glob character", command: "S='/tmp/*'; $S/run.sh"},
		{name: "unquoted expansion of a for loop value holding a backslash", command: `for S in '/tmp/a\b'; do $S/run.sh; done`},
	})
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

			checkEchoWord(t, tt.word, guard.Word{Text: tt.wantPrefix, Literal: false})
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

			checkEchoWord(t, tt.word, guard.Word{Text: tt.wantPrefix, Glob: true})
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

func TestWriteDoubleQuoted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "backslash before dollar drops", in: `\$HOME`, want: "$HOME"},
		{name: "backslash before newline drops with it", in: "a\\\nb", want: "ab"},
		{name: "backslash before other character stays", in: `\n`, want: `\n`},
		{name: "trailing backslash stays", in: `a\`, want: `a\`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var b strings.Builder

			guard.WriteDoubleQuoted(&b, tt.in)

			if got := b.String(); got != tt.want {
				t.Errorf("WriteDoubleQuoted(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWriteUnquoted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "backslash before a character drops", in: `a\ b\.sh`, want: "a b.sh"},
		{name: "backslash before newline drops with it", in: "a\\\nb", want: "ab"},
		{name: "trailing backslash stays", in: `a\`, want: `a\`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var b strings.Builder

			guard.WriteUnquoted(&b, tt.in)

			if got := b.String(); got != tt.want {
				t.Errorf("WriteUnquoted(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
