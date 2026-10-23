package resulttest

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"gotest.tools/v3/assert"
)

// Look is how a terminal draws a character: its color by name, empty for the default, its
// decorations, and the URI of the hyperlink it is part of, empty for none.
type Look struct {
	Color     string
	Link      string
	Bold      bool
	Dim       bool
	Underline bool
}

// Drawn is text as a terminal shows it: the characters, and how each is drawn.
type Drawn struct {
	text  []rune
	looks []Look
}

// String returns the characters a terminal shows.
func (d Drawn) String() string {
	return string(d.text)
}

// Draw reads out as a terminal does, drawing each character with the select graphic rendition
// and hyperlink sequences before it. Other sequences draw nothing.
func Draw(tb testing.TB, out string) Drawn {
	tb.Helper()

	var d Drawn
	pen := Look{}
	for out != "" {
		if rest, ok := strings.CutPrefix(out, "\x1b]"); ok {
			pen, out = command(tb, pen, rest)

			continue
		}
		if strings.HasPrefix(out, "\x1b[") {
			pen, out = sequence(tb, pen, out)

			continue
		}
		r, size := utf8.DecodeRuneInString(out)
		d.text = append(d.text, r)
		d.looks = append(d.looks, pen)
		out = out[size:]
	}

	return d
}

// Control sequences end at the first byte in this range.
const (
	finalByteFirst = 0x40
	finalByteLast  = 0x7e
)

func isFinalByte(r rune) bool {
	return r >= finalByteFirst && r <= finalByteLast
}

// sequence returns pen as the control sequence at the start of out changes it, and the rest of out
// after the sequence. Only a select graphic rendition sequence changes the pen. The test fails on
// a sequence that is not terminated.
func sequence(tb testing.TB, pen Look, out string) (Look, string) {
	tb.Helper()

	rest := out[len("\x1b["):]
	end := strings.IndexFunc(rest, isFinalByte)
	assert.Assert(tb, end >= 0, "unterminated sequence in %q", out)
	if rest[end] == 'm' {
		pen = rendition(tb, pen, rest[:end])
	}

	return pen, rest[end+1:]
}

// rendition returns pen as the parameters of one select graphic rendition sequence change it, and
// fails the test on a parameter it does not know.
func rendition(tb testing.TB, pen Look, params string) Look {
	tb.Helper()

	fields := strings.FieldsFunc(params, func(r rune) bool { return r == ';' })
	if len(fields) == 0 {
		fields = []string{"0"}
	}
	for i := 0; i < len(fields); i++ {
		switch code := fields[i]; code {
		case "0":
			pen = Look{Link: pen.Link}
		case "1":
			pen.Bold = true
		case "2":
			pen.Dim = true
		case "22":
			pen.Bold, pen.Dim = false, false
		case "4":
			pen.Underline = true
		case "24":
			pen.Underline = false
		case "39":
			pen.Color = ""
		case "38":
			assert.Assert(tb, i+2 < len(fields) && fields[i+1] == "5", "color %q is not from the palette", params)
			pen.Color = colorName(tb, fields[i+2], paletteBase, brightPaletteBase)
			i += 2
		default:
			pen.Color = colorName(tb, code, foregroundBase, brightForegroundBase)
		}
	}

	return pen
}

// command returns pen as the operating system command at the start of out changes it, and the
// rest of out after the command. A hyperlink command links the characters after it to its URI,
// or ends the link when the URI is empty; any other command changes nothing. The test fails on a
// command that is not terminated by BEL or ST, and on a hyperlink without its parameters field.
func command(tb testing.TB, pen Look, out string) (Look, string) {
	tb.Helper()

	end := strings.IndexAny(out, "\x07\x1b")
	assert.Assert(tb, end >= 0, "unterminated command in %q", out)
	body, rest := out[:end], out[end+1:]
	if out[end] == '\x1b' {
		var terminated bool
		rest, terminated = strings.CutPrefix(rest, "\\")
		assert.Assert(tb, terminated, "command %q is not terminated by ST", body)
	}

	if link, ok := strings.CutPrefix(body, "8;"); ok {
		var hasParams bool
		_, pen.Link, hasParams = strings.Cut(link, ";")
		assert.Assert(tb, hasParams, "hyperlink %q has no parameters field", body)
	}

	return pen, rest
}

// The first parameter or palette index of each run of eight basic colors.
const (
	foregroundBase       = 30
	brightForegroundBase = 90
	paletteBase          = 0
	brightPaletteBase    = 8
)

// colorName returns the name of one of the 16 basic colors, given as code counted from base for
// the eight colors and from bright for their bright forms, and fails the test on any other code.
func colorName(tb testing.TB, code string, base, bright int) string {
	tb.Helper()

	names := []string{"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"}
	n, err := strconv.Atoi(code)
	assert.NilError(tb, err, "parameter %q", code)
	for _, first := range []int{base, bright} {
		if n >= first && n < first+len(names) {
			return names[n-first]
		}
	}
	tb.Fatalf("parameter %q is not a basic color", code)

	return ""
}

// LookOf returns how part is drawn where it first appears inside the first appearance of within,
// and fails the test unless every character of part is drawn alike.
func (d Drawn) LookOf(tb testing.TB, within, part string) Look {
	tb.Helper()

	text := d.String()
	at := strings.Index(text, within)
	assert.Assert(tb, at >= 0, "%q is not in %q", within, text)
	in := strings.Index(within, part)
	assert.Assert(tb, in >= 0, "%q is not in %q", part, within)
	start := utf8.RuneCountInString(text[:at+in])
	looks := d.looks[start : start+utf8.RuneCountInString(part)]
	for i, l := range looks {
		assert.Assert(tb, l == looks[0], "%q: character %d drawn %+v, the first %+v", part, i, l, looks[0])
	}

	return looks[0]
}
