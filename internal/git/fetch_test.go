package git_test

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/gittest"
)

func TestRepo_FetchPullHead_WhenTheHeadIsOnOrigin_ReturnsFetchedSHA(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	want := f.Commits(topic, "one")[0]
	f.PullRef(pullNumber, want)

	got, err := newRepo(f).FetchPullHead(t.Context(), pullNumber)
	assert.NilError(t, err, "FetchPullHead(%d)", pullNumber)

	assert.Check(t, is.Equal(got, want), "FetchPullHead(%d)", pullNumber)
}

// refNamedBranch puts on origin a branch literally named refs/heads/<topic> at another commit than
// origin's topic, and returns that branch's name and tip.
func refNamedBranch(t *testing.T, f *gittest.Fixture) (name, tip string) {
	t.Helper()
	tip = f.Commits(topic, "one")[0]
	f.Push(topic)
	f.AdvanceOrigin(topic)
	name = "refs/heads/" + topic
	f.OriginGit("update-ref", "refs/heads/"+name, tip)

	return name, tip
}

func TestRepo_Fetch_WhenTheBranchNameStartsWithRefs_ReturnsTheTipOfTheBranchSoNamed(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	name, want := refNamedBranch(t, f)

	got, err := newRepo(f).Fetch(t.Context(), name)
	assert.NilError(t, err, "Fetch(%q)", name)

	assert.Check(t, is.Equal(got, want), "Fetch(%q)", name)
}

// pushedTopic returns a fixture whose origin holds the topic branch.
func pushedTopic(t *testing.T) *gittest.Fixture {
	t.Helper()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.Push(topic)

	return f
}

func assertNoOwnRefs(t *testing.T, f *gittest.Fixture) {
	t.Helper()
	assert.Check(t, is.Equal(f.Git("for-each-ref", "refs/ils/"), ""), "refs under refs/ils/ after Fetch")
}

func TestRepo_Fetch_WhenTheFetchSucceeds_LeavesNoRefOfItsOwn(t *testing.T) {
	t.Parallel()
	f := pushedTopic(t)

	_, err := newRepo(f).Fetch(t.Context(), topic)
	assert.NilError(t, err, "Fetch(%q)", topic)

	assertNoOwnRefs(t, f)
}

func TestRepo_Fetch_WhenTheReadFails_LeavesNoRefOfItsOwn(t *testing.T) {
	// Not parallel: see fakedRepo.
	f := pushedTopic(t)
	repo := fakedRepo(t, f, gittest.Fake{FailOn: []string{"rev-parse"}})

	got, err := repo.Fetch(t.Context(), topic)
	assert.Assert(t, err != nil, "Fetch(%q) = %q, error = nil, want the failed read", topic, got)

	assertNoOwnRefs(t, f)
}

func TestRepo_Fetch_WhenTheFetchFailsAfterStoringTheRef_LeavesNoRefOfItsOwn(t *testing.T) {
	// Not parallel: see fakedRepo.
	f := pushedTopic(t)
	repo := fakedRepo(t, f, gittest.Fake{FailAfterOn: []string{"fetch"}})

	got, err := repo.Fetch(t.Context(), topic)
	assert.Assert(t, err != nil, "Fetch(%q) = %q, error = nil, want the failed fetch", topic, got)

	assertNoOwnRefs(t, f)
}

func TestRepo_Fetch_WhenAnotherRunHoldsTheLockOfAFixedFetchRefForTheBranch_ReturnsTheFetchedSHA(t *testing.T) {
	t.Parallel()
	f := pushedTopic(t)
	want := f.AdvanceOrigin(topic)
	// Another run's fetch holds the lock of a fixed ref named for the branch under refs/ils/fetched/.
	common := f.Git("rev-parse", "--path-format=absolute", "--git-common-dir")
	lock := filepath.Join(common, "refs", "ils", "fetched", "heads", topic+".lock")
	assert.NilError(t, os.MkdirAll(filepath.Dir(lock), 0o750))
	assert.NilError(t, os.WriteFile(lock, nil, 0o600))

	got, err := newRepo(f).Fetch(t.Context(), topic)
	assert.NilError(t, err, "Fetch(%q) while another run holds the lock", topic)

	assert.Check(t, is.Equal(got, want))
}

func TestRepo_Fetch_WhenTheFetchConfigLeavesTheBranchOut_PutsOriginsTrackingRefAtTheFetchedSHA(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Git("config", "remote.origin.fetch", "+refs/heads/"+topic+":refs/remotes/origin/"+topic)
	want := f.AdvanceOrigin(trunk)

	_, err := newRepo(f).Fetch(t.Context(), trunk)
	assert.NilError(t, err, "Fetch(%q)", trunk)

	assert.Check(t, is.Equal(f.Git("rev-parse", "refs/remotes/origin/"+trunk), want))
}
