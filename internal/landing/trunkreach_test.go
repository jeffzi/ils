package landing_test

import (
	"context"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/result"
)

// trunkMiss is one way origin keeps a status from learning its trunk: arrange changes the
// repository before the scene is built, and keep, when set, changes how origin answers after.
type trunkMiss struct {
	arrange func(*gittest.Fixture)
	keep    func(*scene, *testing.T)
	name    string
	offline bool
}

// lastFetched returns an arrange that runs stand, then records origin's trunk as the last fetch saw
// it.
func lastFetched(stand func(*gittest.Fixture)) func(*gittest.Fixture) {
	return func(f *gittest.Fixture) {
		f.Git("remote", "set-head", "origin", trunk)
		stand(f)
	}
}

// dirtyPublished publishes the topic branch and leaves a tracked file changed.
func dirtyPublished(f *gittest.Fixture) {
	f.Publish(topic)
	f.DirtyFile()
}

// missedTrunk returns the scene of miss under the forge build.
func missedTrunk(t *testing.T, miss trunkMiss, build forgeFor) *scene {
	t.Helper()

	s := statusSceneAt(t, miss.arrange, build, miss.offline)
	if miss.keep != nil {
		miss.keep(s, t)
	}

	return s
}

func TestStatus_WhenOriginsTrunkCannotBeReadOnAWorkBranch_EndsOnTheStepPushEndsOn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want string
		miss trunkMiss
	}{
		{
			miss: trunkMiss{name: "origin turns the caller away", arrange: lastFetched(publishTopic), keep: (*scene).refuse},
			want: statusCommand().fixed,
		},
		{
			miss: trunkMiss{
				name: "origin turns the caller away with changed tracked files", arrange: lastFetched(dirtyPublished),
				keep: (*scene).refuse,
			},
			want: statusCommand().fixed,
		},
		{miss: trunkMiss{name: "origin does not answer", arrange: lastFetched(publishTopic), offline: true}, want: nextPush()},
		{
			miss: trunkMiss{name: "origin does not answer with changed tracked files", arrange: lastFetched(dirtyPublished), offline: true},
			want: nextPush(),
		},
		{
			miss: trunkMiss{name: "origin does not answer on a branch with no commit past the trunk", arrange: lastFetched(func(f *gittest.Fixture) {
				f.Git("switch", "--quiet", "--create", spare, trunk)
			}), offline: true},
			want: nextPush(),
		},
		{
			miss: trunkMiss{name: "origin does not answer with a local trunk ahead of origin's", arrange: lastFetched(func(f *gittest.Fixture) {
				f.Publish(topic)
				f.AdvanceTrunk()
			}), offline: true},
			want: nextPush(),
		},
		{
			miss: trunkMiss{name: "origin's answer names no branch", arrange: func(f *gittest.Fixture) {
				f.Publish(topic)
				f.DetachOriginHead()
			}},
			want: nextPush(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.miss.name, func(t *testing.T) {
			t.Parallel()
			s := missedTrunk(t, tt.miss, greenOpen())

			got := seeStatus(t, s, (*testing.T).Context)

			assertSays(t, "Next:"+nextOf(t, got.text), tt.want)
		})
	}
}

// detachOnTrunk detaches HEAD on the trunk, with no commit of its own.
func detachOnTrunk(f *gittest.Fixture) { f.Git("switch", "--quiet", "--detach", trunk) }

// detachPastTrunk detaches HEAD on a commit of its own past the trunk, which no branch carries.
func detachPastTrunk(f *gittest.Fixture) {
	f.Commits(spare, sharpen)
	f.Detach()
	f.Git("branch", "--quiet", "--delete", "--force", spare)
}

func TestStatus_WhenOriginsTrunkCannotBeReadOnADetachedHEAD_EndsOnTheStepBranchEndsOn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want string
		miss trunkMiss
	}{
		{
			miss: trunkMiss{name: "origin turns the caller away", arrange: lastFetched(detachPastTrunk), keep: (*scene).refuse},
			want: statusCommand().fixed,
		},
		{
			miss: trunkMiss{name: "origin's answer names no branch", arrange: lastFetched(func(f *gittest.Fixture) {
				detachPastTrunk(f)
				f.DetachOriginHead()
			})},
			want: statusCommand().fixed,
		},
		{
			// Branch refuses there and names itself to run again.
			miss: trunkMiss{name: "origin does not answer and no fetch named its trunk", arrange: func(f *gittest.Fixture) {
				detachPastTrunk(f)
				f.Git("remote", "set-head", "origin", "--delete")
			}, offline: true},
			want: nextBranch(),
		},
		{
			miss: trunkMiss{name: "origin does not answer with commits past the last fetch", arrange: lastFetched(detachPastTrunk), offline: true},
			want: keepDetached(),
		},
		{
			miss: trunkMiss{name: "origin does not answer with no commit past the last fetch", arrange: lastFetched(detachOnTrunk), offline: true},
			want: nextBranch(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.miss.name, func(t *testing.T) {
			t.Parallel()
			s := missedTrunk(t, tt.miss, viewing(cleanHead(), passing()))

			got := seeStatus(t, s, (*testing.T).Context)

			assert.Check(t, is.Equal(got.status.Standing, standingDetached))
			assertSays(t, "Next:"+nextOf(t, got.text), tt.want)
		})
	}
}

func TestStatus_WhenOriginsTrunkIsNotLearnedOnTheTrunk_StandsOnTheLastFetchedTrunk(t *testing.T) {
	t.Parallel()

	tests := []trunkMiss{
		{name: "origin turns the caller away", arrange: lastFetched(standOnTrunk), keep: (*scene).refuse},
		{name: "origin does not answer", arrange: lastFetched(standOnTrunk), offline: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := missedTrunk(t, tt, ruledBy(forge.RulesProtect))

			got := seeStatus(t, s, (*testing.T).Context)

			assert.Check(t, is.Equal(got.status.Standing, standingTrunk))
			assert.Check(t, is.Equal(got.status.Trunk, trunk))
			assertSays(t, nextOf(t, got.text), nextBranch())
		})
	}
}

func TestStatus_WhenTheReadOfOriginsURLsIsCutShortOnTheTrunk_StandsOnTheLastFetchedTrunk(t *testing.T) {
	// Not parallel: stallingGit puts a git that stalls first on PATH with t.Setenv.
	s := statusScene(t, lastFetched(standOnTrunk), ruledBy(forge.RulesProtect))
	ctx := byTimeLimit().stallingGit([]string{"remote", "get-url", "--push"})(t, s)

	got := seeStatus(t, s, func(*testing.T) context.Context { return ctx })

	assert.Assert(t, hasUnread(got.status, readURLs), "unread %v, want %s listed", got.status.Unread, readURLs)
	assert.Check(t, is.Equal(got.status.Standing, standingTrunk))
	assert.Check(t, is.Equal(got.status.Trunk, trunk))
	assertEndsOn(t, got, nextBranch())
}

func TestStatus_WhenTheReadOfOriginsURLsIsCutShortWithNoTrunkFetched_EndsOnFixingWhatFailed(t *testing.T) {
	// Not parallel: stallingGit puts a git that stalls first on PATH with t.Setenv.
	for _, c := range cuts() {
		t.Run(c.name, func(t *testing.T) {
			s := statusScene(t, func(f *gittest.Fixture) {
				f.Publish(topic)
				f.Git("remote", "set-head", "origin", "--delete")
			}, greenOpen())
			ctx := c.stallingGit([]string{"remote", "get-url", "--push"})(t, s)

			got := seeStatus(t, s, func(*testing.T) context.Context { return ctx })

			assert.Check(t, is.Equal(got.status.Standing, result.Standing("")))
			assertEndsOn(t, got, statusCommand().fixed)
		})
	}
}

func TestStatus_WhenNoTrunkNameCanBeLearned_LeavesWhereHEADStandsUnread(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) {
		standOnTrunk(f)
		f.Git("remote", "set-head", "origin", "--delete")
	}, ruledBy(forge.RulesProtect))
	s.refuse(t)

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Standing, result.Standing("")))
	assertEndsOn(t, got, expect("fix what failed"))
}
