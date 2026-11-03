//go:build unix

package guard

import (
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
}

// expander is what readWord reads a word with: the HOME that `~` and `~/…` expand to, and the
// shell variables whose value an expansion may stand for.
type expander struct {
	vars shellVars
	home string
}

// readWord returns w after quote removal, or its literal prefix when any part of it is one the
// shell would expand and ex cannot tell the value of.
func readWord(w *syntax.Word, ex expander) Word {
	if w == nil {
		return Word{}
	}

	// SplitBraces rewrites the word it is given, and the tree is still walked after this.
	split := &syntax.Word{Parts: slices.Clone(w.Parts)}
	syntax.SplitBraces(split)

	var text strings.Builder
	for i, part := range split.Parts {
		literal := false
		switch p := part.(type) {
		case *syntax.Lit:
			raw, expanded := p.Value, true
			if i == 0 {
				raw, expanded = expandTilde(&text, raw, len(split.Parts) == 1, ex.home)
			}

			literal = expanded && readUnquoted(&text, raw, i == len(split.Parts)-1)
		case *syntax.SglQuoted:
			value := p.Value
			if p.Dollar {
				value, literal = decodeANSIC(value)
			} else {
				literal = true
			}

			text.WriteString(value)
		case *syntax.DblQuoted:
			literal = readDoubleQuoted(&text, p.Parts, ex.vars)
		case *syntax.ParamExp:
			literal = readUnquotedExpansion(&text, split.Parts, i, ex.vars)
		}

		if !literal {
			return Word{Text: text.String()}
		}
	}

	return Word{Text: text.String(), Literal: true}
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
	escaped := false
	for i := range len(raw) {
		c := raw[i]
		switch {
		case escaped:
			escaped = false
			if c != '\n' {
				b.WriteByte(c)
			}
		case c == '\\':
			escaped = true
		case c == '*' || c == '?':
			return false
		case c == '[' && (!last || strings.Contains(raw[i+1:], "]")):
			return false
		default:
			b.WriteByte(c)
		}
	}

	return true
}

// readDoubleQuoted writes the parts of a double-quoted string to b after quote removal, and reports
// whether they are all plain text, or expansions of variables whose value vars knows.
func readDoubleQuoted(b *strings.Builder, parts []syntax.WordPart, vars shellVars) bool {
	for i, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			writeDoubleQuotedText(b, p.Value)
		case *syntax.ParamExp:
			value, known := vars.expansion(p)
			if !known || modifiedByZsh(parts, i) {
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

// readUnquotedExpansion writes to b the value of parts[i], an unquoted parameter expansion, and
// reports whether it stands for one plain word: a variable whose value vars knows, which is not
// empty, since an empty one removes the word, holds none of splitOrGlobChars, and is not modified
// by zsh (see modifiedByZsh).
func readUnquotedExpansion(b *strings.Builder, parts []syntax.WordPart, i int, vars shellVars) bool {
	value, known := vars.expansion(parts[i].(*syntax.ParamExp))
	if !known || value == "" || strings.ContainsAny(value, splitOrGlobChars) || modifiedByZsh(parts, i) {
		return false
	}

	b.WriteString(value)

	return true
}

// modifiedByZsh reports whether parts[i], a parameter expansion, is a `$NAME` that zsh reads with
// the modifier the text after it opens, as in `$f:h`: a colon and a letter or `&`.
func modifiedByZsh(parts []syntax.WordPart, i int) bool {
	if !parts[i].(*syntax.ParamExp).Short || i+1 == len(parts) {
		return false
	}

	next, ok := parts[i+1].(*syntax.Lit)
	if !ok || len(next.Value) < 2 || next.Value[0] != ':' {
		return false
	}

	c := next.Value[1]

	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '&'
}

// writeDoubleQuotedText writes value, the text between double quotes, to b after quote removal. A
// backslash drops before `$`, a backtick, a double quote, or a backslash; it and a newline after it
// both drop; before any other character, or at the end, it stays.
func writeDoubleQuotedText(b *strings.Builder, value string) {
	escaped := false
	for i := range len(value) {
		c := value[i]
		switch {
		case escaped:
			escaped = false
			switch c {
			case '$', '`', '"', '\\':
				b.WriteByte(c)
			case '\n':
			default:
				b.WriteByte('\\')
				b.WriteByte(c)
			}
		case c == '\\':
			escaped = true
		default:
			b.WriteByte(c)
		}
	}

	if escaped {
		b.WriteByte('\\')
	}
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
