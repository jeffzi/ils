package git_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
)

func TestRepo_Fetch_WhenRefOnOrigin_ReturnsFetchedSHA(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	want := f.Commits(topic, "one")[0]
	f.PullRef(pullNumber, want)

	got, err := newRepo(f).Fetch(t.Context(), pullRef)
	assert.NilError(t, err, "Fetch(%q)", pullRef)

	assert.Check(t, is.Equal(got, want), "Fetch(%q)", pullRef)
}

func TestRepo_Fetch_WhenFetchHeadNamesAnotherCommitAfterTheFetch_ReturnsTheFetchedSHA(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.Push(topic)
	want := f.AdvanceOrigin(topic)
	other := f.OriginGit("rev-parse", trunk)
	repo := newRepo(f)
	repo.Runner.Env = append(repo.Runner.Env, gittest.FakeGit(t, gittest.Fake{
		AnswerOn: []string{"rev-parse", "FETCH_HEAD"},
		Answer:   other,
	})...)

	got, err := repo.Fetch(t.Context(), topic)
	assert.NilError(t, err, "Fetch(%q)", topic)

	assert.Check(t, is.Equal(got, want), "Fetch(%q)", topic)
}

func TestRepo_Fetch_WhenItReturns_LeavesNoRefOfItsOwn(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.Push(topic)

	_, err := newRepo(f).Fetch(t.Context(), topic)
	assert.NilError(t, err, "Fetch(%q)", topic)

	assert.Check(t, is.Equal(f.Git("for-each-ref", "refs/ils/"), ""), "refs under refs/ils/ after Fetch")
}

func TestRepo_Fetch_WhenTheReadFails_LeavesNoRefOfItsOwn(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.Push(topic)
	repo := newRepo(f)
	repo.Runner.Env = append(repo.Runner.Env, gittest.FakeGit(t, gittest.Fake{FailOn: []string{"rev-parse"}})...)

	got, err := repo.Fetch(t.Context(), topic)
	assert.Assert(t, err != nil, "Fetch(%q) = %q, error = nil, want the failed read", topic, got)

	assert.Check(t, is.Equal(f.Git("for-each-ref", "refs/ils/"), ""), "refs under refs/ils/ after Fetch")
}

func TestRepo_Fetch_WhenTheFetchFailsAfterStoringTheRef_LeavesNoRefOfItsOwn(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.Push(topic)
	repo := newRepo(f)
	repo.Runner.Env = append(repo.Runner.Env, gittest.FakeGit(t, gittest.Fake{FailAfterOn: []string{"fetch"}})...)

	got, err := repo.Fetch(t.Context(), topic)
	assert.Assert(t, err != nil, "Fetch(%q) = %q, error = nil, want the failed fetch", topic, got)

	assert.Check(t, is.Equal(f.Git("for-each-ref", "refs/ils/"), ""), "refs under refs/ils/ after Fetch")
}

func TestRepo_Fetch_WhenTwoWorktreesFetchTheSameBranchAtOnce_EachReturnsTheFetchedSHA(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.Push(topic)
	want := f.AdvanceOrigin(topic)
	other := f.BranchWorktree(topic)
	repos := []git.Repo{newRepo(f), {Runner: git.Runner{Dir: other, Env: f.Env()}}}
	got := make([]string, len(repos))
	errs := make([]error, len(repos))
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i, repo := range repos {
		wg.Go(func() {
			<-start
			got[i], errs[i] = repo.Fetch(t.Context(), topic)
		})
	}
	close(start)
	wg.Wait()
	assert.NilError(t, errors.Join(errs...), "Fetch(%q) from two worktrees at once", topic)

	assert.Check(t, is.DeepEqual(got, []string{want, want}), "Fetch(%q) from two worktrees at once", topic)
}

func TestRepo_Fetch_WhenAnotherRunHoldsTheLockOfTheOldFetchRef_ReturnsTheFetchedSHA(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.Push(topic)
	want := f.AdvanceOrigin(topic)
	// Another run's fetch of the branch, under the one name a fetch had, holds its lock.
	common := f.Git("rev-parse", "--path-format=absolute", "--git-common-dir")
	lock := filepath.Join(common, "refs", "ils", "fetched", "heads", filepath.FromSlash(topic)+".lock")
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

// killsReceivePack kills the hook's parent: git runs a hook as a child of receive-pack, so `$PPID`
// is the process that must die mid-push.
const killsReceivePack = "kill -KILL $PPID"

// specMark stands, in a hook's script and in what a test wants, for the pair of refs Push hands git
// for topic's tip, which git's report names on its status line.
const specMark = "<spec>"

// otherSpec is the pair of refs for a branch Push never pushes, which a hook's report-shaped lines name.
const otherSpec = "refs/heads/other:refs/heads/other"

// localTip returns the sha of the clone's topic branch.
