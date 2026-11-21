package guard

import "strings"

// WriteDoubleQuoted writes text, the text between double quotes, to b after quote removal. A
// backslash drops before `$`, a backtick, a double quote, or a backslash; it and a newline after it
// both drop; before any other character, or at the end, it stays.
func WriteDoubleQuoted(b *strings.Builder, text string) {
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' || i+1 == len(text) {
			b.WriteByte(text[i])

			continue
		}

		i++
		switch text[i] {
		case '$', '`', '"', '\\':
			b.WriteByte(text[i])
		case '\n':
		default:
			b.WriteByte('\\')
			b.WriteByte(text[i])
		}
	}
}

// WriteUnquoted writes text, text outside quotes, to b after quote removal. A backslash quotes any
// character and drops before it; it and a newline after it both drop; at the end, it stays, as
// bash keeps it.
func WriteUnquoted(b *strings.Builder, text string) {
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' || i+1 == len(text) {
			b.WriteByte(text[i])

			continue
		}

		i++
		if text[i] != '\n' {
			b.WriteByte(text[i])
		}
	}
}
