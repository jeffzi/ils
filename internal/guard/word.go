package guard

import (
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// Word is one word of an invocation after quote removal.
type Word struct {
	// Text is the word after quote removal when Literal is set, and otherwise the literal text
	// before the first part the shell would expand.
	Text string
	// Literal reports whether quote removal leaves plain text: the word holds no expansion, no
	// substitution, no brace expansion, no unquoted glob character, and no `~user`.
	Literal bool
}

// readWord returns w after quote removal, or its literal prefix when any part of it is one the
// shell would expand.
func readWord(w *syntax.Word, home string) Word {
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
				raw, expanded = expandTilde(&text, raw, len(split.Parts) == 1, home)
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
			literal = readDoubleQuoted(&text, p.Parts)
		}

		if !literal {
			return Word{Text: text.String()}
		}
	}

	return Word{Text: text.String(), Literal: true}
}

// decodeANSIC returns the value of a `$'…'` string once its escapes are decoded as bash decodes
// them, ending at the first NUL byte as bash does. It reports false for a value holding `\c`, the
// control-character escape that expand.Format leaves as written.
func decodeANSIC(value string) (string, bool) {
	if strings.Contains(value, `\c`) {
		return "", false
	}

	// With no arguments, Format decodes escapes only and leaves `%` as written, so it cannot fail.
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
// whether they are all plain text.
func readDoubleQuoted(b *strings.Builder, parts []syntax.WordPart) bool {
	for _, part := range parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			return false
		}

		escaped := false
		for i := range len(lit.Value) {
			c := lit.Value[i]
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

	return true
}
