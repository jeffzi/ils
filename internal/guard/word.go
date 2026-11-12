//go:build unix

package guard

import (
	"path"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// Word is one word of an invocation after quote removal.
type Word struct {
	// Text is the word after quote removal when Literal is set, and otherwise the text before the
	// first part the guard cannot read, with each expansion of a variable whose value the guard
	// knows read as that value.
	Text string
	// Literal reports whether quote removal leaves plain text: the word holds no substitution, no
	// brace expansion, no unquoted glob character, no `~user`, and no expansion other than one of a
	// variable whose value the guard knows (see shellVars), which the text holds in its place.
	Literal bool
	// Glob reports whether the only parts of the word the guard cannot read are unquoted glob
	// characters (`*`, `?`, `[`). Text is then the text before the first of them, and every word the
	// shell expands the pattern to starts with it.
	Glob bool
}

// isEndOfFlags reports whether w is a literal `--`, which ends the options of most commands.
func isEndOfFlags(w Word) bool {
	return w.Literal && w.Text == "--"
}

// expander is what readWord reads a word with: the HOME that `~` and `~/…` expand to, and the
// shell variables whose value an expansion may stand for.
type expander struct {
	vars shellVars
	home string
}

// readWord returns w after quote removal, or its literal prefix when any part of it is one the
// shell would expand and ex cannot tell the value of. A word whose only such parts are unquoted
// glob characters is reported as a glob.
func readWord(w *syntax.Word, ex expander) Word {
	if w == nil {
		return Word{}
	}

	// SplitBraces rewrites the word it is given, and the tree is still walked after this.
	split := &syntax.Word{Parts: slices.Clone(w.Parts)}
	syntax.SplitBraces(split)

	// Once a glob character ends the prefix, the parts after it are still read, into pattern, which
	// is thrown away: one the guard cannot read keeps the word from counting as a glob.
	var prefix, pattern strings.Builder
	text, glob := &prefix, false
	for i, part := range split.Parts {
		lit, unquoted := part.(*syntax.Lit)
		switch {
		case unquoted && glob:
			// Unquoted text after a glob character only extends the pattern.
		case unquoted:
			raw, expanded := lit.Value, true
			if i == 0 {
				raw, expanded = expandTilde(text, raw, len(split.Parts) == 1, ex.home)
			}

			if !expanded {
				return Word{Text: prefix.String()}
			}

			if !readUnquoted(text, raw, i == len(split.Parts)-1) {
				text, glob = &pattern, true
			}
		case !readQuotedOrExpansion(text, split.Parts, i, ex.vars):
			return Word{Text: prefix.String()}
		}
	}

	return Word{Text: prefix.String(), Literal: !glob, Glob: glob}
}

// readQuotedOrExpansion writes parts[i], a word part other than unquoted text, to b after quote
// removal, and reports whether it is plain text: a quoted string, or an expansion of a variable
// whose value vars knows.
func readQuotedOrExpansion(b *strings.Builder, parts []syntax.WordPart, i int, vars shellVars) bool {
	switch p := parts[i].(type) {
	case *syntax.SglQuoted:
		if !p.Dollar {
			b.WriteString(p.Value)

			return true
		}

		value, decoded := decodeANSIC(p.Value)
		b.WriteString(value)

		return decoded
	case *syntax.DblQuoted:
		return readDoubleQuoted(b, p.Parts, vars)
	case *syntax.ParamExp:
		return readUnquotedExpansion(b, p, nextPart(parts, i), vars)
	default:
		return false
	}
}

// decodeANSIC returns the value of a `$'…'` string once its escapes are decoded as bash decodes
// them, ending at the first NUL byte as bash does. It reports false for a value holding `\c`, the
// control-character escape that expand.Format leaves as written, and for a value that
// expand.Format returns an error for, whatever the error is.
func decodeANSIC(value string) (string, bool) {
	if strings.Contains(value, `\c`) {
		return "", false
	}

	// With no arguments, Format decodes escapes only and leaves `%` as written, so it is not
	// expected to fail; an error it does return is reported as false, never as a decoded value.
	// It writes to the config it is given, and to a shared one when given nil, so each call gets its
	// own to keep concurrent Parse calls apart.
	decoded, _, err := expand.Format(&expand.Config{}, value, nil)
	if err != nil {
		return "", false
	}

	decoded, _, _ = strings.Cut(decoded, "\x00")

	return decoded, true
}

// expandTilde writes home to b when raw, the unquoted text that opens a word, starts with `~` or
// `~/`, and returns the rest of raw. It reports false for any other tilde prefix, `~user` among
// them; alone says whether raw is the whole word, since a tilde followed by a quoted part is no
// plain `~`.
func expandTilde(b *strings.Builder, raw string, alone bool, home string) (string, bool) {
	if !strings.HasPrefix(raw, "~") {
		return raw, true
	}

	prefix, _, slash := strings.Cut(raw, "/")
	if prefix != "~" || (!slash && !alone) {
		return raw, false
	}

	b.WriteString(home)

	return raw[1:], true
}

// readUnquoted writes the unquoted text raw to b after quote removal, and reports whether it holds
// no glob character. last says whether raw ends its word, since a `[` can be closed by a later
// part.
func readUnquoted(b *strings.Builder, raw string, last bool) bool {
	// Globs are found before the backslashes go, since a backslash is what keeps `\*` from being one.
	escaped := false
	for i := range len(raw) {
		c := raw[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\':
			escaped = true
		case c == '*' || c == '?', c == '[' && (!last || strings.Contains(raw[i+1:], "]")):
			WriteUnquoted(b, raw[:i])

			return false
		}
	}

	WriteUnquoted(b, raw)

	return true
}

// readDoubleQuoted writes the parts of a double-quoted string to b after quote removal, and reports
// whether they are all plain text, or expansions of variables whose value vars knows.
func readDoubleQuoted(b *strings.Builder, parts []syntax.WordPart, vars shellVars) bool {
	for i, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			WriteDoubleQuoted(b, p.Value)
		case *syntax.ParamExp:
			value, known := vars.expansion(p)
			if !known || modifiedByZsh(p, nextPart(parts, i)) {
				return false
			}

			b.WriteString(value)
		default:
			return false
		}
	}

	return true
}

// splitOrGlobChars are the characters that make the value of an unquoted expansion more than one
// plain word: a blank the shell splits it at, or a character that may open a glob pattern.
const splitOrGlobChars = " \t\n*?[(\\"

// readUnquotedExpansion writes to b the value of p, an unquoted parameter expansion followed by the
// word part next (nil when p ends the word), and reports whether it stands for one plain word: a
// variable whose value vars knows, which is not empty, since an empty one removes the word, holds
// none of splitOrGlobChars, and is not modified by zsh (see modifiedByZsh).
func readUnquotedExpansion(b *strings.Builder, p *syntax.ParamExp, next syntax.WordPart, vars shellVars) bool {
	value, known := vars.expansion(p)
	if !known || value == "" || strings.ContainsAny(value, splitOrGlobChars) || modifiedByZsh(p, next) {
		return false
	}

	b.WriteString(value)

	return true
}

// modifiedByZsh reports whether p, a parameter expansion followed by the word part next (nil when
// p ends the word), is a `$NAME` that zsh reads with the modifier next opens, as in `$f:h`: a colon
// and a letter or `&`.
func modifiedByZsh(p *syntax.ParamExp, next syntax.WordPart) bool {
	if !p.Short {
		return false
	}

	lit, ok := next.(*syntax.Lit)
	if !ok || len(lit.Value) < 2 || lit.Value[0] != ':' {
		return false
	}

	c := lit.Value[1]

	return isASCIILetter(c) || c == '&'
}

// nextPart returns the word part after parts[i], nil when parts[i] is the last.
func nextPart(parts []syntax.WordPart, i int) syntax.WordPart {
	if i+1 == len(parts) {
		return nil
	}

	return parts[i+1]
}

func inTable(table map[string]bool, w Word) bool {
	return w.Literal && table[w.Text]
}

func wordTexts(words []Word) []string {
	texts := make([]string, 0, len(words))
	for _, w := range words {
		texts = append(texts, w.Text)
	}

	return texts
}

func literalWords(texts []string) []Word {
	words := make([]Word, 0, len(texts))
	for _, text := range texts {
		words = append(words, Word{Text: text, Literal: true})
	}

	return words
}

// programName returns the basename of the command word, with a leading zsh `=` lookup removed.
func programName(command Word) string {
	return path.Base(strings.TrimPrefix(command.Text, "="))
}
