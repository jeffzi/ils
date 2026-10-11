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
		{name: "done", outcome: result.Done, want: 0},
		{name: "broke", outcome: result.Broke, want: 1},
		{name: "refused", outcome: result.Refused, want: 2},
		{name: "ask is reserved", outcome: result.Ask, want: 3},
		{name: "pending", outcome: result.Pending, want: 4},
		{name: "delivered, not green", outcome: result.DeliveredNotGreen, want: 5},
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
