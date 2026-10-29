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

// Decode reads the one JSON object out holds, and stops the test when out is anything else.
func Decode(tb testing.TB, out string) Shown {
	tb.Helper()

	var value jsontext.Value

	err := json.Unmarshal([]byte(out), &value)
	assert.NilError(tb, err, "output is not one JSON value:\n%s", out)
	// Unmarshal reads null into a struct as its zero value, so the kind is checked first.
	assert.Assert(tb, value.Kind() == '{', "output is not one JSON object:\n%s", out)

	var got Shown

	err = json.Unmarshal(value, &got)
	assert.NilError(tb, err, "output is not one JSON object:\n%s", out)

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

// AssertCarried fails unless every line of text is in what the JSON object carries: its lines, its
// excerpt, its reason behind its outcome's label, or its next. It reports through tb only by Helper
// and the calls of assert.TestingT, so a *testing.T or a stand-in that records them both serve.
func AssertCarried(tb interface {
	assert.TestingT
	Helper()
}, text string, got Shown,
) {
	tb.Helper()

	carried := append(strings.Split(got.Excerpt, "\n"), got.Lines...)
	carried = append(carried, NextLine(got.Next))
	if outcome, ok := result.ParseOutcome(got.Outcome); ok && outcome != result.Done {
		carried = append(carried, ReasonLine(outcome, got.Reason))
	}

	for line := range strings.Lines(text) {
		line = strings.TrimSuffix(line, "\n")
		assert.Check(tb, is.Contains(carried, line), "JSON: %+v", got)
	}
}
