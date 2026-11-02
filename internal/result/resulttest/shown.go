package resulttest

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result"
)

// Shown is the JSON renderer's object as a caller decodes it.
type Shown = result.Object

const notOneObject = "output is not one JSON object:\n%s"

// Decode reads the one JSON object out holds, and stops the test when out is anything else.
func Decode(tb testing.TB, out string) Shown {
	tb.Helper()

	var value jsontext.Value

	err := json.Unmarshal([]byte(out), &value)
	assert.NilError(tb, err, "output is not one JSON value:\n%s", out)
	// Unmarshal reads null into a struct as its zero value, so the kind is checked first.
	assert.Assert(tb, value.Kind() == '{', notOneObject, out)

	var got Shown

	err = json.Unmarshal(value, &got)
	assert.NilError(tb, err, notOneObject, out)

	return got
}

// ReasonLine returns the reason line the text prints for reason, led by the label of outcome,
// without its line break.
func ReasonLine(outcome result.Outcome, reason string) string {
	return outcome.Label() + ": " + reason
}

// NextLine returns the Next line the text prints for next, without its line break.
func NextLine(next string) string {
	return result.NextLabel + ": " + next
}

// AssertCarried fails unless the JSON object's outcome is want and every line of text is in what
// it carries: its lines, its excerpt, its reason behind want's label, or its next.
func AssertCarried(tb testing.TB, text string, want result.Outcome, got Shown) {
	tb.Helper()

	assert.Check(tb, is.Equal(got.Outcome, want.String()), "JSON outcome")
	carried := append(strings.Split(got.Excerpt, "\n"), got.Lines...)
	carried = append(carried, NextLine(got.Next))
	if want != result.Done {
		carried = append(carried, ReasonLine(want, got.Reason))
	}

	for line := range strings.Lines(text) {
		line = strings.TrimSuffix(line, "\n")
		assert.Check(tb, is.Contains(carried, line), "JSON: %+v", got)
	}
}
