package result_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result"
)

func TestOutcome_ExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		outcome result.Outcome
		want    int
	}{
		{name: "ask is reserved", outcome: result.Ask, want: 3},
		{name: "above the table", outcome: result.Outcome(99), want: 1},
		{name: "below the table", outcome: result.Outcome(-1), want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.outcome.ExitCode()

			assert.Check(t, is.Equal(got, tt.want), "Outcome(%d)", int(tt.outcome))
		})
	}
}

func TestParseOutcome_WhenNameIsAnOutcomeName_ReturnsThatOutcome(t *testing.T) {
	t.Parallel()

	for _, want := range []result.Outcome{
		result.Done, result.Broke, result.Refused, result.Ask, result.Pending, result.DeliveredNotGreen,
	} {
		got, ok := result.ParseOutcome(want.String())

		assert.Check(t, ok, "ParseOutcome(%q) found nothing", want.String())
		assert.Check(t, is.Equal(got, want), "ParseOutcome(%q)", want.String())
	}
}

func TestParseOutcome_WhenNameIsNoOutcomeName_ReportsNotFound(t *testing.T) {
	t.Parallel()

	_, ok := result.ParseOutcome("Refused")

	assert.Check(t, !ok, "ParseOutcome(%q) found an outcome, want none", "Refused")
}

func TestOutcome_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		want    string
		outcome result.Outcome
	}{
		{name: "above the table", outcome: result.Outcome(99), want: "broke"},
		{name: "below the table", outcome: result.Outcome(-1), want: "broke"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.outcome.String()

			assert.Check(t, is.Equal(got, tt.want), "Outcome(%d)", int(tt.outcome))
		})
	}
}
