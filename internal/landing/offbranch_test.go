package landing_test

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/result"
)

const (
	standingDetached     result.Standing = "detached"
	standingNoOrigin     result.Standing = "no-origin"
	standingNoRepository result.Standing = "no-repository"
)

// noForge is a forge with no answer, so that any call to it is unscripted.
func noForge(string) forgetest.Script { return forgetest.Script{} }

func TestStatus_WhenHEADIsDetachedWithNoCommitOfItsOwn_EndsOnStartingABranch(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) {
		f.Git("switch", "--quiet", "--detach", trunk)
		f.AdvanceOrigin(trunk)
	}, viewing(cleanHead(), passing()))

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Standing, standingDetached))
	assert.Check(t, is.Len(got.status.Unlanded, 0))
	assertSays(t, got.text, detached())
}

func TestStatus_WhenDetachedHEADHasCommitsOriginsTrunkLacks_ListsThemToKeepOnABranch(t *testing.T) {
	t.Parallel()

	var own []string

	s := statusScene(t, func(f *gittest.Fixture) {
		own = f.Commits(spare, sharpen)
		f.Detach()
		f.Git("branch", "--quiet", "--delete", "--force", spare)
	}, viewing(cleanHead(), passing()))

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Standing, standingDetached))
	assert.Check(t, is.DeepEqual(got.status.Unlanded, own))
	assertSays(t, got.text, expect(shortSHA(own[0]), sharpen)+keepDetached())
}

func TestStatus_WhenDetachedHEADStandsBesideALocalTrunkAheadOfOrigins_EndsOnItsFix(t *testing.T) {
	t.Parallel()

	var sha string

	s := statusScene(t, func(f *gittest.Fixture) {
		detachOnTrunk(f)
		sha = f.AdvanceTrunk()
	}, viewing(cleanHead(), passing()))

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Standing, standingDetached))
	assertSays(t, got.text, trunkAheadText(sha, nextTrunkFix(fixNowhere, startCommand)))
}

func TestStatus_WhenTheCommitsOfADetachedHEADAreNotRead_EndsOnTheStepBranchEndsOn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		want  string
		words []string
	}{
		{name: "their read fails", words: history(), want: statusCommand().fixed},
		// Branch refuses there and names itself to run again.
		{name: "origin cannot be reached to fetch its trunk", words: trunkFetch(), want: nextBranch()},
		{name: "what the fetch of origin's trunk fetched cannot be read", words: fetchedRead(), want: statusCommand().fixed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := statusScene(t, detachOnTrunk, viewing(cleanHead(), passing()))
			failGit(s, tt.words)

			got := seeStatus(t, s, (*testing.T).Context)

			assert.Check(t, is.Equal(got.status.Standing, standingDetached))
			assertEndsOn(t, got, tt.want)
		})
	}
}

func TestStatus_WhenOnTheTrunk_EndsOnStartingABranch(t *testing.T) {
	t.Parallel()
	s := statusScene(t, standOnTrunk, ruledBy(forge.RulesProtect))

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, nextOf(t, got.text), nextBranch())
}

func TestStatus_WhenTheFetchOfOriginsTrunkFailsOnTheTrunk_EndsOnTheStepBranchEndsOn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		want  string
		words []string
	}{
		// Branch refuses there and names itself to run again.
		{name: "origin cannot be reached", words: trunkFetch(), want: nextBranch()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := statusScene(t, standOnTrunk, ruledBy(forge.RulesProtect))
			failGit(s, tt.words)

			got := seeStatus(t, s, (*testing.T).Context)

			assert.Check(t, is.Equal(got.status.Standing, standingTrunk))
			assertEndsOn(t, got, tt.want)
		})
	}
}

func TestStatus_WhenOnTheTrunkAheadOfOrigins_EndsOnTheFix(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) { f.Commits(trunk, strayOnTrunk) }, ruledBy(forge.RulesProtect))

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, "Next:"+nextOf(t, got.text), nextTrunkFix(fixHere, "ils push"))
}

func TestStatus_WhenThereIsNoOrigin_EndsOnCreatingIt(t *testing.T) {
	t.Parallel()
	s := statusScene(t, (*gittest.Fixture).RemoveOrigin, noForge)

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Standing, standingNoOrigin))

	_, next, _ := strings.Cut(got.text, "Next:")
	assertSays(t, next, expect("gh repo create --source . --push"))
}

func TestStatus_WhenGitIsTooOldWithNoOrigin_EndsOnUpgradingItThenStatus(t *testing.T) {
	t.Parallel()
	s := statusScene(t, (*gittest.Fixture).RemoveOrigin, noForge)
	fakeGit(s, gittest.Fake{Version: "git version 1.0.0"})

	got := seeStatus(t, s, (*testing.T).Context)

	assertEndsOn(t, got, statusCommand().after("upgrade git"))
}

func TestStatus_WhenOutsideAGitRepository_SaysSo(t *testing.T) {
	t.Parallel()
	s := statusScene(t, nil, noForge)
	s.deps.Repo.Runner.Dir = t.TempDir()
	missing := s.deps.Repo.CheckRepository(t.Context())
	assert.Assert(t, missing != nil, "CheckRepository() = nil outside a repository")

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Standing, standingNoRepository))
	assertSays(t, got.text, expect(missing.Error()))
	assert.Check(t, is.Equal(got.next, "none"))
}
