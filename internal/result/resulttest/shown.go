package resulttest

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result"
)

const notOneObject = "output is not one JSON object:\n%s"

// Decode reads the one JSON object out holds, and stops the test when out is anything else.
func Decode(tb testing.TB, out string) result.Object {
	tb.Helper()

	var value jsontext.Value

	err := json.Unmarshal([]byte(out), &value)
	assert.NilError(tb, err, "output is not one JSON value:\n%s", out)
	// Unmarshal reads null into a struct as its zero value, so the kind is checked first.
	assert.Assert(tb, value.Kind() == '{', notOneObject, out)

	var got result.Object

	err = json.Unmarshal(value, &got)
	assert.NilError(tb, err, notOneObject, out)

	return got
}

// ReasonLine returns the reason line the text prints for reason, led by the label of outcome,
// without its line break. It panics for Done, which prints no reason line.
func ReasonLine(outcome result.Outcome, reason string) string {
	return label(outcome) + ": " + reason
}

// label returns the word the text prints before the reason of outcome. It is written out here, not
// read from the result package, so that a test of a reason line fails when the renderer's label
// changes.
func label(outcome result.Outcome) string {
	switch outcome {
	case result.Broke:
		return "Broke"
	case result.Refused:
		return "Refused"
	case result.Ask:
		return "Ask"
	case result.Pending:
		return "Pending"
	case result.DeliveredNotGreen:
		return "Delivered, not green"
	case result.Done:
		fallthrough
	default:
		panic(fmt.Sprintf("resulttest: outcome %v has no reason line", outcome))
	}
}

// NextLine returns the Next line the text prints for next, without its line break.
func NextLine(next string) string {
	return result.NextLabel + ": " + next
}

// AssertCarried fails unless the JSON object's outcome is want and every line of text is in what
// it carries: its lines, its excerpt, its reason behind want's label, or its next.
func AssertCarried(tb testing.TB, text string, want result.Outcome, got result.Object) {
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
