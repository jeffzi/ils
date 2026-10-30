package resulttest

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"gotest.tools/v3/assert"
)

// Look is how a terminal draws a character: its color by name, empty for the default, and its
// decorations.
type Look struct {
	Color     string
	Bold      bool
	Dim       bool
	Underline bool
}

// NameLook is how a terminal with color draws a branch, a pull request number and a command.
func NameLook() Look { return Look{Color: "blue", Bold: true} }

// SHALook is how a terminal with color draws a SHA.
func SHALook() Look { return Look{Dim: true} }

// SkippedLook is how a terminal with color draws the count of a tally of skipped checks above zero,
// and a tally of zero as a whole.
func SkippedLook() Look { return Look{Dim: true} }

// PassedLook is how a terminal with color draws the count of a tally of passed checks above zero.
func PassedLook() Look { return Look{Color: "green"} }

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
// sequences before it. Other control sequences draw nothing.
func Draw(tb testing.TB, out string) Drawn {
	tb.Helper()

	var d Drawn
	pen := Look{}
	for out != "" {
		if strings.HasPrefix(out, controlSequenceIntroducer) {
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

const controlSequenceIntroducer = "\x1b["

// selectGraphicRenditionFinal is the final byte of the control sequences that set the pen.
const selectGraphicRenditionFinal = 'm'

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

	rest := out[len(controlSequenceIntroducer):]
	end := strings.IndexFunc(rest, isFinalByte)
	assert.Assert(tb, end >= 0, "unterminated sequence in %q", out)
	if rest[end] == selectGraphicRenditionFinal {
		pen = rendition(tb, pen, rest[:end])
	}

	return pen, rest[end+1:]
}

// resetParam clears every decoration; a sequence with no parameters means it too.
const resetParam = "0"

// rendition returns pen as the parameters of one select graphic rendition sequence change it, and
// fails the test on a parameter it does not know.
func rendition(tb testing.TB, pen Look, params string) Look {
	tb.Helper()

	fields := strings.FieldsFunc(params, func(r rune) bool { return r == ';' })
	if len(fields) == 0 {
		fields = []string{resetParam}
	}
	for _, code := range fields {
		switch code {
		case resetParam:
			pen = Look{}
		case "1":
			pen.Bold = true
		case "2":
			pen.Dim = true
		case "4":
			pen.Underline = true
		default:
			pen.Color = colorName(tb, code)
		}
	}

	return pen
}

// foregroundBase is the parameter of the first of the eight basic foreground colors.
const foregroundBase = 30

// colorName returns the name of one of the eight basic foreground colors, given as its select
// graphic rendition parameter, and fails the test on any other parameter.
func colorName(tb testing.TB, code string) string {
	tb.Helper()

	names := []string{"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"}
	n, err := strconv.Atoi(code)
	assert.NilError(tb, err, "parameter %q", code)
	if n < foregroundBase || n >= foregroundBase+len(names) {
		tb.Fatalf("parameter %q is not a basic color", code)
	}

	return names[n-foregroundBase]
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
