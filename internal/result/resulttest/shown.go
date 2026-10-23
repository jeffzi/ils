// Package resulttest provides the JSON renderer's object as a caller decodes it, and styled text as
// a terminal draws it, for the tests of packages that print results.
package resulttest

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result"
)

// Shown is the JSON renderer's object as a caller decodes it.
type Shown struct {
	Payload  *result.Payload `json:"payload"`
	Command  string          `json:"command"`
	Outcome  string          `json:"outcome"`
	Next     string          `json:"next"`
	Reason   string          `json:"reason"`
	Excerpt  string          `json:"excerpt"`
	Words    []string        `json:"words"`
	Lines    []string        `json:"lines"`
	ExitCode int             `json:"exit_code"`
}

// Decode reads the one JSON object out holds, and stops the test when out is anything else.
func Decode(tb testing.TB, out string) Shown {
	tb.Helper()

	var got Shown

	err := json.Unmarshal([]byte(out), &got)
	assert.NilError(tb, err, "output is not one JSON object:\n%s", out)

	return got
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

	labels := map[string]string{
		"refused": "Refused", "broke": "Broke", "pending": "Pending", "delivered-not-green": "Delivered, not green",
	}

	carried := append(strings.Split(got.Excerpt, "\n"), got.Lines...)
	carried = append(carried, "Next: "+got.Next)
	if label, ok := labels[got.Outcome]; ok {
		carried = append(carried, label+": "+got.Reason)
	}

	for line := range strings.Lines(text) {
		line = strings.TrimSuffix(line, "\n")
		assert.Check(tb, is.Contains(carried, line), "JSON: %+v", got)
	}
}
