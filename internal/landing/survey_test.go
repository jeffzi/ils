package landing_test

import (
	"errors"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
)

func TestSurvey_WhenAReadIsAskedTwice_MakesItOnceAndGivesTheSameAnswer(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, allowed(), false)
	record := landing.NewSurvey(s.deps)
	first, err := record.OriginsTrunk(t.Context())
	assert.NilError(t, err)
	made := len(s.recorder.Calls())

	second, err := record.OriginsTrunk(t.Context())

	assert.NilError(t, err)
	assert.Check(t, is.Equal(second, first))
	assert.Check(t, is.Len(s.recorder.Calls(), made))
}

func TestSurvey_WhenAReadFailed_GivesTheSecondAskerTheSameFailureWithoutTryingAgain(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, allowed(), true)
	record := landing.NewSurvey(s.deps)
	_, first := record.OriginsTrunk(t.Context())
	assert.Assert(t, errors.Is(first, git.ErrRemote), "first ask: %v", first)
	made := len(s.recorder.Calls())

	_, second := record.OriginsTrunk(t.Context())

	assert.Check(t, errors.Is(second, first), "second ask: %v, want %v", second, first)
	assert.Check(t, is.Len(s.recorder.Calls(), made))
}

func TestSurvey_WhenAReadWasCutShort_GivesTheSecondAskerTheCutWithoutTryingAgain(t *testing.T) {
	t.Parallel()
	for _, c := range cuts() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, nil, allowed(), false)
			record := landing.NewSurvey(s.deps)
			spent, cancel := c.spend(t.Context())
			defer cancel()
			_, first := record.OriginsTrunk(spent)
			assert.Assert(t, errors.Is(first, c.err), "first ask: %v", first)
			made := len(s.recorder.Calls())

			_, second := record.OriginsTrunk(t.Context())

			assert.Check(t, errors.Is(second, first), "second ask: %v, want %v", second, first)
			assert.Check(t, is.Len(s.recorder.Calls(), made))
		})
	}
}

func TestSurvey_WhenAReadItNeedsFailed_FailsWithThatFailureWithoutACallOfItsOwn(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, allowed(), true)
	record := landing.NewSurvey(s.deps)
	_, needed := record.OriginsTrunk(t.Context())
	assert.Assert(t, errors.Is(needed, git.ErrRemote), "origin's trunk: %v", needed)
	made := len(s.recorder.Calls())

	_, err := record.TrunkFetch(t.Context())

	assert.Check(t, errors.Is(err, needed), "fetch: %v, want %v", err, needed)
	assert.Check(t, is.Len(s.recorder.Calls(), made))
}

func TestSurvey_WhenPullRequestHeadsAreAskedTwice_MakesEachOnceAndKeepsThemApart(t *testing.T) {
	t.Parallel()
	var heads []string
	s := newScene(t, func(f *gittest.Fixture) {
		heads = f.Commits(topic, "first change", "second change")
		f.PullRef(1, heads[0])
		f.PullRef(2, heads[1])
	}, allowed(), false)
	record := landing.NewSurvey(s.deps)
	first, err := record.MergedHead(t.Context(), 1)
	assert.NilError(t, err)
	second, err := record.MergedHead(t.Context(), 2)
	assert.NilError(t, err)
	made := len(s.recorder.Calls())

	again, err := record.MergedHead(t.Context(), 1)

	assert.NilError(t, err)
	assert.Check(t, is.Equal(first, heads[0]))
	assert.Check(t, is.Equal(second, heads[1]))
	assert.Check(t, is.Equal(again, first))
	assert.Check(t, is.Len(s.recorder.Calls(), made))
}
