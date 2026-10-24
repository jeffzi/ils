package cli_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

func TestApp_Run_WhenMergeRunsOnABranchWithNoPullRequest_ExitsRefused(t *testing.T) {
	s := newScene(t, noPullRequest())

	got := launch(t, s.app(), "merge")

	assert.Check(t, is.Equal(got.code, exitRefused), "ils merge printed %q", got.stdout)
}

func TestApp_Run_WhenPushEndsInAnyOutcome_JSONCarriesWhatTheTextShows(t *testing.T) {
	tests := []struct {
		outcome  string
		scenario scenario
	}{
		{outcome: "done", scenario: greenFirstPush()},
		{outcome: "refused", scenario: onTheTrunk()},
		{outcome: "broke", scenario: failedRead()},
		{outcome: "pending", scenario: pendingAtTheBound()},
		{outcome: "delivered-not-green", scenario: redWithLog()},
	}
	for _, tt := range tests {
		t.Run(tt.outcome, func(t *testing.T) {
			s := newScene(t, tt.scenario)

			text, encoded := both(t, s)

			assert.Check(t, is.Equal(text.code, tt.scenario.want))
			assert.Check(t, is.Equal(encoded.code, tt.scenario.want))

			got := resulttest.Decode(t, encoded.stdout)
			assert.Check(t, is.Equal(got.Outcome, tt.outcome))
			assert.Check(t, is.Equal(got.ExitCode, tt.scenario.want))

			resulttest.AssertCarried(t, text.stdout, got)
		})
	}
}

func onTheTrunk() scenario {
	return scenario{
		name: "on the trunk", script: only(allowed()...), want: exitRefused,
		arrange: func(f *gittest.Fixture) { f.Git("switch", "--quiet", trunk) },
	}
}

func failedRead() scenario {
	down := listed("open", "")
	down.Answers = []ghtest.Answer{ghtest.ServerError()}

	return scenario{name: "a read of GitHub fails", script: only(append(allowed(), down)...), want: exitBroke}
}
