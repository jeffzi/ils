package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
)

const (
	trunk      = "develop"
	topic      = "team/topic"
	side       = "team/side"
	sibling    = "team/other"
	spare      = "team/spare"
	pullNumber = 7
	pullRef    = "refs/pull/7/head"
	unknownSHA = "0123456789012345678901234567890123456789"
)

// newRepo returns the repository of f, whose ssh reads the fixture's ssh config alone.
func newRepo(f *gittest.Fixture) git.Repo {
	return git.Repo{Runner: git.Runner{Dir: f.Dir(), Env: f.Env()}}
}

// repoCall is one call on the repository, reduced to its result and its error.
type repoCall struct {
	call func(ctx context.Context, repo git.Repo) (any, error)
	name string
}

// checkNoResult fails unless got, the result of the call name that failed, is its zero value.
func checkNoResult(t *testing.T, name string, got any) {
	t.Helper()
	assert.Check(t, got == nil || reflect.ValueOf(got).IsZero(), "%s() = %v, want no result", name, got)
}

// checkCanceled runs each call on one repository under a context that has already ended, and fails
// unless the call returns the context's error and no result.
func checkCanceled(t *testing.T, calls []repoCall) {
	t.Helper()

	repo := newRepo(gittest.New(t, trunk))
	for _, tt := range calls {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			got, err := tt.call(ctx, repo)

			assert.Check(t, is.ErrorIs(err, context.Canceled))
			checkNoResult(t, tt.name, got)
		})
	}
}

func fetch(f *gittest.Fixture, ref string) {
	f.Git("fetch", "--quiet", "origin", ref)
}

// outsideRepo returns the typed git calls over a directory that is not a repository.
func outsideRepo(t *testing.T) git.Repo {
	t.Helper()

	return git.Repo{Runner: newRunner(t)}
}

// landed returns a fixture whose topic branch was squashed onto origin's trunk as pull request 7 and
// deleted from origin, with the trunk fetched, and the merged head read from the pull request's ref.
func landed(t *testing.T) (f *gittest.Fixture, merged string) {
	t.Helper()

	f = gittest.New(t, trunk)
	shas := f.Commits(topic, "one", "two")
	f.Push(topic)
	f.LandPull(pullNumber, shas[1])
	f.RemoveFromOrigin(topic)
	fetch(f, trunk)
	fetch(f, pullRef)

	return f, f.Git("rev-parse", "FETCH_HEAD")
}

// topicWithSibling returns a fixture checked out on topic, with sibling branched from it, and topic's first commit.
func topicWithSibling(t *testing.T) (*gittest.Fixture, string) {
	t.Helper()

	f := gittest.New(t, trunk)
	first := f.Commits(topic, "one", "two")[0]
	f.Sibling(sibling, topic)
	f.Git("switch", "--quiet", topic)

	return f, first
}

// renameTracked commits a file and stages its rename, returning the new name.
func renameTracked(t *testing.T, f *gittest.Fixture) string {
	t.Helper()

	const before, after = "before.txt", "moved.txt"
	gittest.WriteFile(t, f.Dir(), before, "kept as is\n")
	f.Git("add", before)
	f.Git("commit", "--quiet", "--message", "add "+before)
	f.Git("mv", before, after)

	return after
}

// mergeTrunk moves origin's trunk once more, fetches it, and merges it into the branch checked out.
func mergeTrunk(t *testing.T, f *gittest.Fixture) {
	t.Helper()

	f.AdvanceOrigin(trunk)
	fetch(f, trunk)
	f.Git("merge", "--quiet", "--no-edit", "origin/"+trunk)
}

// mergeSide merges a side branch holding one commit into the topic branch and returns that commit.
func mergeSide(t *testing.T, f *gittest.Fixture) []git.Commit {
	t.Helper()

	f.Git("switch", "--quiet", "--create", side, trunk)
	f.Git("commit", "--quiet", "--allow-empty", "--message", "side")
	sha := f.Git("rev-parse", "HEAD")
	f.Git("switch", "--quiet", topic)
	f.Git("merge", "--quiet", "--no-edit", "--no-ff", side)

	return []git.Commit{{SHA: sha, Subject: "side"}}
}

// tips holds the shas of a branch that diverged from origin's copy.
type tips struct {
	base   string
	local  string
	remote string
	repo   git.Repo
}

// diverged returns a repository whose topic branch and origin's fetched copy each have one commit
// past their shared base.
func diverged(t *testing.T) tips {
	t.Helper()

	f := gittest.New(t, trunk)
	base := f.Commits(topic, "one")[0]
	f.Push(topic)
	local, remote := f.Diverge(topic)
	fetch(f, topic)

	return tips{repo: newRepo(f), base: base, local: local, remote: remote}
}

func TestRepo_Standing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arrange func(f *gittest.Fixture)
		name    string
		want    git.Standing
	}{
		{
			name:    "on a branch",
			arrange: func(f *gittest.Fixture) { f.Commits(topic, "one") },
			want:    git.Standing{Kind: git.OnBranch, Branch: topic},
		},
		{
			name:    "detached head",
			arrange: func(f *gittest.Fixture) { f.Detach() },
			want:    git.Standing{Kind: git.Detached},
		},
		{
			name:    "no remote named origin",
			arrange: func(f *gittest.Fixture) { f.RemoveOrigin() },
			want:    git.Standing{Kind: git.NoOrigin},
		},
		{
			name: "detached head and no remote named origin",
			arrange: func(f *gittest.Fixture) {
				f.Detach()
				f.RemoveOrigin()
			},
			want: git.Standing{Kind: git.NoOrigin},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			tt.arrange(f)

			got, err := newRepo(f).Standing(t.Context())
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, tt.want))
		})
	}
}

func TestRepo_HeadSHA_WhenOnBranch_ReturnsTipSHA(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	want := f.Commits(topic, "one")[0]

	got, err := newRepo(f).HeadSHA(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got, want))
}

func TestRepo_GitDir_WhenInRepository_ReturnsAbsoluteGitDir(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	want := filepath.Join(f.Dir(), ".git")

	got, err := newRepo(f).GitDir(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got, want))
}

func TestRepo_WhenNotARepository_ReturnsExitError(t *testing.T) {
	t.Parallel()
	tests := []repoCall{
		{name: "Standing", call: func(ctx context.Context, repo git.Repo) (any, error) { return repo.Standing(ctx) }},
		{name: "HeadSHA", call: func(ctx context.Context, repo git.Repo) (any, error) { return repo.HeadSHA(ctx) }},
		{name: "GitDir", call: func(ctx context.Context, repo git.Repo) (any, error) { return repo.GitDir(ctx) }},
		{name: "HasCommits", call: func(ctx context.Context, repo git.Repo) (any, error) {
			return repo.HasCommits(ctx, "topic")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := outsideRepo(t)

			got, err := tt.call(t.Context(), repo)

			_, ok := errors.AsType[*child.ExitError](err)
			assert.Check(t, ok, "%s() error = %v, want *child.ExitError", tt.name, err)
			checkNoResult(t, tt.name, got)
		})
	}
}

func TestRepo_DirtyFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arrange func(f *gittest.Fixture) []string
		name    string
	}{
		{name: "unstaged change", arrange: func(f *gittest.Fixture) []string { return []string{f.DirtyFile()} }},
		{name: "staged change", arrange: func(f *gittest.Fixture) []string { return []string{f.StagedFile()} }},
		{
			name: "staged and unstaged changes",
			arrange: func(f *gittest.Fixture) []string {
				dirty := f.DirtyFile()

				return []string{f.StagedFile(), dirty}
			},
		},
		{name: "untracked file only", arrange: func(f *gittest.Fixture) []string { f.UntrackedFile(); return nil }},
		{name: "nothing changed", arrange: func(*gittest.Fixture) []string { return nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			want := tt.arrange(f)

			got, err := newRepo(f).DirtyFiles(t.Context())
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, want, cmpopts.EquateEmpty()))
		})
	}
}

func TestRepo_DirtyFiles_WhenTrackedFileRenamedInIndex_ListsNewNameOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arrange func(t *testing.T, f *gittest.Fixture) []string
		name    string
	}{
		{
			name: "rename only",
			arrange: func(t *testing.T, f *gittest.Fixture) []string {
				t.Helper()

				return []string{renameTracked(t, f)}
			},
		},
		{
			name: "rename and an unrelated unstaged change",
			arrange: func(t *testing.T, f *gittest.Fixture) []string {
				t.Helper()

				moved := renameTracked(t, f)

				return []string{f.DirtyFile(), moved}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			want := tt.arrange(t, f)

			got, err := newRepo(f).DirtyFiles(t.Context())
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, want))
		})
	}
}

func TestRepo_DirtyFiles_WhenIndexIsCorrupt_ReturnsExitError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.CorruptIndex()

	got, err := newRepo(f).DirtyFiles(t.Context())

	_, ok := errors.AsType[*child.ExitError](err)
	assert.Check(t, ok, "DirtyFiles() = %v, error = %v, want *child.ExitError", got, err)
}

func TestRepo_Divergence(t *testing.T) {
	t.Parallel()
	shas := diverged(t)
	tests := []struct {
		name string
		a    string
		b    string
		want git.Divergence
	}{
		{name: "equal", a: shas.local, b: shas.local, want: git.Divergence{}},
		{name: "ahead", a: shas.local, b: shas.base, want: git.Divergence{Ahead: 1}},
		{name: "behind", a: shas.base, b: shas.local, want: git.Divergence{Behind: 1}},
		{name: "diverged", a: shas.local, b: shas.remote, want: git.Divergence{Ahead: 1, Behind: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := shas.repo.Divergence(t.Context(), tt.a, tt.b)
			assert.NilError(t, err, "Divergence(%s, %s)", tt.a, tt.b)

			assert.Check(t, is.DeepEqual(got, tt.want), "Divergence(%s, %s)", tt.a, tt.b)
		})
	}
}

func TestRepo_Divergence_WhenOutputDoesNotParse_ReturnsErrorNamingTheCall(t *testing.T) {
	// Not parallel: gittest.LinkAs sets PATH for the whole process.
	shas := diverged(t)
	_, err := shas.repo.Divergence(t.Context(), shas.local, unknownSHA)
	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Divergence(%s, %s) error = %v, want *child.ExitError", shas.local, unknownSHA, err)
	gittest.LinkAs(t, "git")
	repo := shas.repo
	repo.Runner.Env = append(slices.Clone(repo.Runner.Env), garbledGitEnv+"=not counts")

	_, err = repo.Divergence(t.Context(), shas.local, unknownSHA)

	assert.Check(t, is.ErrorContains(err, child.CommandLine(exitErr.Argv)))
}

func TestRepo_Contains(t *testing.T) {
	t.Parallel()
	shas := diverged(t)
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "equal", a: shas.local, b: shas.local, want: true},
		{name: "ahead", a: shas.local, b: shas.base, want: true},
		{name: "behind", a: shas.base, b: shas.local, want: false},
		{name: "diverged", a: shas.local, b: shas.remote, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := shas.repo.Contains(t.Context(), tt.a, tt.b)
			assert.NilError(t, err, "Contains(%s, %s)", tt.a, tt.b)

			assert.Check(t, is.Equal(got, tt.want), "Contains(%s, %s)", tt.a, tt.b)
		})
	}
}

func TestRepo_Contains_WhenCommitUnknown_ReturnsExitError(t *testing.T) {
	t.Parallel()
	shas := diverged(t)

	got, err := shas.repo.Contains(t.Context(), shas.local, unknownSHA)

	_, ok := errors.AsType[*child.ExitError](err)
	assert.Check(t, ok, "Contains(%s, %s) = %t, error = %v, want *child.ExitError", shas.local, unknownSHA, got, err)
}

func TestRepo_CommitsPast_WhenTrunkMergedIn_ListsOwnCommitsOldestFirst(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	shas := f.Commits(topic, "one", "two")
	mergeTrunk(t, f)
	tip := f.Git("rev-parse", "HEAD")

	got, err := newRepo(f).CommitsPast(t.Context(), tip, trunk)
	assert.NilError(t, err)

	want := []git.Commit{{SHA: shas[0], Subject: "one"}, {SHA: shas[1], Subject: "two"}}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestRepo_CommitsPast_WhenNothingPastTrunk_ReturnsNoCommits(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	tip := f.Git("rev-parse", "HEAD")

	got, err := newRepo(f).CommitsPast(t.Context(), tip, trunk)
	assert.NilError(t, err)

	assert.Check(t, is.Len(got, 0))
}

func TestRepo_BranchesContaining_WhenAnotherBranchCarriesCommit_ListsOnlyTheOtherBranch(t *testing.T) {
	t.Parallel()
	f, first := topicWithSibling(t)

	got, err := newRepo(f).BranchesContaining(t.Context(), first)
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got, []string{sibling}))
}

func TestRepo_BranchesContaining_WhenHeadDetached_ListsEveryBranchCarryingCommit(t *testing.T) {
	t.Parallel()
	f, first := topicWithSibling(t)
	f.Detach()

	got, err := newRepo(f).BranchesContaining(t.Context(), first)
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got, []string{sibling, topic}))
}

func TestRepo_BranchesContaining_WhenCurrentBranchReadFails_ReturnsItsExitError(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := gittest.New(t, trunk)
	first := f.Commits(topic, "one")[0]
	repo := newRepo(f)
	repo.Runner.Env = append(repo.Runner.Env, gittest.FakeGit(t, gittest.Fake{FailOn: []string{"symbolic-ref"}})...)

	got, err := repo.BranchesContaining(t.Context(), first)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "BranchesContaining() = %v, error = %v, want *child.ExitError", got, err)
	assert.Check(t, is.Equal(exitErr.Code, gittest.FailedCode))
}

func TestRepo_OwnCommits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arrange func(t *testing.T, f *gittest.Fixture) []git.Commit
		name    string
	}{
		{
			name: "first commit carried by one of the named branches, trunk merged in",
			arrange: func(t *testing.T, f *gittest.Fixture) []git.Commit {
				t.Helper()

				shas := f.Commits(topic, "one", "two", "three")
				f.Sibling(sibling, topic)
				f.Commits(spare, "spare change")
				f.Commits(topic)
				mergeTrunk(t, f)

				return []git.Commit{{SHA: shas[1], Subject: "two"}, {SHA: shas[2], Subject: "three"}}
			},
		},
		{
			name: "every commit carried by a named branch",
			arrange: func(_ *testing.T, f *gittest.Fixture) []git.Commit {
				f.Commits(topic, "one")
				f.Sibling(sibling, topic)
				f.Commits(spare, "spare change")
				f.Commits(topic)

				return nil
			},
		},
		{
			name: "first commit carried only by the last of the named branches",
			arrange: func(_ *testing.T, f *gittest.Fixture) []git.Commit {
				shas := f.Commits(topic, "one", "two", "three")
				f.Commits(sibling, "other change")
				f.Sibling(spare, topic)
				f.Commits(topic)

				return []git.Commit{{SHA: shas[1], Subject: "two"}, {SHA: shas[2], Subject: "three"}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			want := tt.arrange(t, f)
			tip := f.Git("rev-parse", "HEAD")

			got, err := newRepo(f).OwnCommits(t.Context(), tip, trunk, []string{sibling, spare})
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, want, cmpopts.EquateEmpty()))
		})
	}
}

func TestRepo_OwnCommits_WhenSeveralBranchesNamed_ReadsWithOneLogCall(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one", "two")
	f.Sibling(sibling, topic)
	f.Commits(spare, "spare change")
	f.Commits(topic)
	tip := f.Git("rev-parse", "HEAD")
	recorder := &git.Recorder{}
	repo := newRepo(f)
	repo.Runner.Recorder = recorder

	_, err := repo.OwnCommits(t.Context(), tip, trunk, []string{sibling, spare})
	assert.NilError(t, err)

	calls := recorder.Calls()
	assert.Assert(t, is.Len(calls, 1))
	assert.Check(t, is.DeepEqual(calls[0][:2], []string{"git", "log"}))
}

func TestRepo_OwnCommits_WhenTipUnknown_ReturnsExitError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")

	got, err := newRepo(f).OwnCommits(t.Context(), unknownSHA, trunk, []string{topic})

	_, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "OwnCommits() = %v, error = %v, want *child.ExitError", got, err)
	assert.Check(t, is.Len(got, 0))
}

func TestRepo_Unlanded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arrange func(t *testing.T, f *gittest.Fixture) []git.Commit
		name    string
	}{
		{name: "tip is the merged head", arrange: func(*testing.T, *gittest.Fixture) []git.Commit { return nil }},
		{
			name: "message of a landed commit amended",
			arrange: func(_ *testing.T, f *gittest.Fixture) []git.Commit {
				f.Git("commit", "--quiet", "--amend", "--message", "two reworded")

				return nil
			},
		},
		{
			name: "one commit after the merged head",
			arrange: func(_ *testing.T, f *gittest.Fixture) []git.Commit {
				return []git.Commit{{SHA: f.Commits(topic, "three")[0], Subject: "three"}}
			},
		},
		{
			name: "trunk merged into the tip",
			arrange: func(t *testing.T, f *gittest.Fixture) []git.Commit {
				mergeTrunk(t, f)

				return nil
			},
		},
		{
			name: "one commit after merging the trunk in",
			arrange: func(t *testing.T, f *gittest.Fixture) []git.Commit {
				mergeTrunk(t, f)

				return []git.Commit{{SHA: f.Commits(topic, "three")[0], Subject: "three"}}
			},
		},
		{name: "side branch merged into the tip", arrange: mergeSide},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, merged := landed(t)
			want := tt.arrange(t, f)
			tip := f.Git("rev-parse", "HEAD")

			got, err := newRepo(f).Unlanded(t.Context(), merged, tip, trunk)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, want, cmpopts.EquateEmpty()))
		})
	}
}

func TestRepo_Unlanded_WhenMergedHeadHasCommitTheTipLacks_ListsNothing(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	tip := f.Commits(topic, "one", "two")[1]
	f.Push(topic)
	f.OriginGit("update-ref", pullRef, f.AdvanceOrigin(topic))
	fetch(f, pullRef)
	merged := f.Git("rev-parse", "FETCH_HEAD")
	f.SquashOnOrigin(merged, "topic (#7)", "")
	f.RemoveFromOrigin(topic)
	fetch(f, trunk)

	got, err := newRepo(f).Unlanded(t.Context(), merged, tip, trunk)
	assert.NilError(t, err)

	assert.Check(t, is.Len(got, 0))
}

func TestRepo_WhenContextCanceled_ReturnsContextError(t *testing.T) {
	t.Parallel()

	checkCanceled(t, []repoCall{
		{name: "Divergence", call: func(ctx context.Context, repo git.Repo) (any, error) {
			return repo.Divergence(ctx, unknownSHA, unknownSHA)
		}},
		{name: "CommitsPast", call: func(ctx context.Context, repo git.Repo) (any, error) {
			return repo.CommitsPast(ctx, unknownSHA, trunk)
		}},
		{name: "BranchesContaining", call: func(ctx context.Context, repo git.Repo) (any, error) {
			return repo.BranchesContaining(ctx, unknownSHA)
		}},
		{name: "OwnCommits", call: func(ctx context.Context, repo git.Repo) (any, error) {
			return repo.OwnCommits(ctx, unknownSHA, trunk, []string{topic})
		}},
		{name: "Unlanded", call: func(ctx context.Context, repo git.Repo) (any, error) {
			return repo.Unlanded(ctx, unknownSHA, unknownSHA, trunk)
		}},
		{name: "Switch", call: func(ctx context.Context, repo git.Repo) (any, error) {
			return nil, repo.Switch(ctx, topic)
		}},
		{name: "DetachAtOrigin", call: func(ctx context.Context, repo git.Repo) (any, error) {
			return nil, repo.DetachAtOrigin(ctx, trunk)
		}},
		{name: "DeleteBranch", call: func(ctx context.Context, repo git.Repo) (any, error) {
			return nil, repo.DeleteBranch(ctx, topic)
		}},
		{name: "CheckedOutElsewhere", call: func(ctx context.Context, repo git.Repo) (any, error) {
			return repo.CheckedOutElsewhere(ctx, trunk)
		}},
		{name: "FastForwardTrunk", call: func(ctx context.Context, repo git.Repo) (any, error) {
			return nil, repo.FastForwardTrunk(ctx, trunk)
		}},
	})
}

func TestRepo_HasCommits(t *testing.T) {
	t.Parallel()
	const orphan = "spare"
	f := gittest.New(t, trunk)
	f.Commits(topic, "first change")
	f.Git("switch", "--quiet", "--orphan", orphan)
	repo := newRepo(f)
	tests := []struct {
		name   string
		branch string
		want   bool
	}{
		{name: "branch with a commit", branch: topic, want: true},
		{name: "branch made with switch --orphan", branch: orphan, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := repo.HasCommits(t.Context(), tt.branch)
			assert.NilError(t, err, "HasCommits(%q)", tt.branch)

			assert.Check(t, is.Equal(got, tt.want), "HasCommits(%q)", tt.branch)
		})
	}
}

// linkWorktree adds a linked worktree of f on branch, with opts before its path, and returns its path.
func linkWorktree(t *testing.T, f *gittest.Fixture, branch string, opts ...string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "linked")
	args := append([]string{"worktree", "add", "--quiet"}, opts...)
	f.Git(append(args, dir, branch)...)

	return dir
}

// checkGitRefused fails unless err is a *child.ExitError whose stderr holds message.
func checkGitRefused(t *testing.T, err error, message string) {
	t.Helper()

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "error = %v, want *child.ExitError", err)
	assert.Check(t, is.Contains(exitErr.Stderr, message))
}

func standing(t *testing.T, f *gittest.Fixture) git.Standing {
	t.Helper()

	got, err := newRepo(f).Standing(t.Context())
	assert.NilError(t, err)

	return got
}

func TestRepo_Switch_WhenBranchIsLocal_PutsHeadOnIt(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")

	err := newRepo(f).Switch(t.Context(), trunk)
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(standing(t, f), git.Standing{Kind: git.OnBranch, Branch: trunk}))
}

func TestRepo_Switch_WhenOnlyOriginHasBranch_CreatesItFromOriginsCopy(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	want := f.Commits(topic, "one")[0]
	f.Push(topic)
	fetch(f, topic)
	f.Git("switch", "--quiet", trunk)
	f.Git("branch", "--quiet", "--delete", "--force", topic)
	f.Git("config", "checkout.guess", "false")

	err := newRepo(f).Switch(t.Context(), topic)
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(standing(t, f), git.Standing{Kind: git.OnBranch, Branch: topic}))
	assert.Check(t, is.Equal(f.Git("rev-parse", "HEAD"), want))
}

func TestRepo_Switch_WhenAnotherWorktreeHasBranch_ReturnsGitsMessage(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Git("branch", topic)
	linkWorktree(t, f, topic)

	err := newRepo(f).Switch(t.Context(), topic)

	checkGitRefused(t, err, "'"+topic+"' is already")
	assert.Check(t, is.DeepEqual(standing(t, f), git.Standing{Kind: git.OnBranch, Branch: trunk}))
}

func TestRepo_Switch_WhenUntrackedFileWouldBeOverwritten_FailsAndLeavesTreeAsItWas(t *testing.T) {
	t.Parallel()
	const name, untracked = "notes.txt", "written here\n"
	f := gittest.New(t, trunk)
	f.Git("switch", "--quiet", "--create", topic)
	gittest.WriteFile(t, f.Dir(), name, "committed on topic\n")
	f.Git("add", name)
	f.Git("commit", "--quiet", "--message", "add "+name)
	f.Git("switch", "--quiet", trunk)
	gittest.WriteFile(t, f.Dir(), name, untracked)

	err := newRepo(f).Switch(t.Context(), topic)

	checkGitRefused(t, err, "would be overwritten")
	checkGitRefused(t, err, name)
	content, readErr := os.ReadFile(filepath.Join(f.Dir(), name))
	assert.NilError(t, readErr)
	assert.Check(t, is.Equal(string(content), untracked))
	assert.Check(t, is.DeepEqual(standing(t, f), git.Standing{Kind: git.OnBranch, Branch: trunk}))
}

func TestRepo_DetachAtOrigin_LeavesHeadDetachedAtFetchedTrunkWithUntrackedFilesKept(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	fetched := f.AdvanceOrigin(trunk)
	fetch(f, trunk)
	f.AdvanceOrigin(trunk)
	untracked := f.UntrackedFile()

	err := newRepo(f).DetachAtOrigin(t.Context(), trunk)
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(standing(t, f), git.Standing{Kind: git.Detached}))
	assert.Check(t, is.Equal(f.Git("rev-parse", "HEAD"), fetched))
	_, statErr := os.Stat(filepath.Join(f.Dir(), untracked))
	assert.Check(t, statErr, "untracked file %s after the detach", untracked)
}

func TestRepo_DeleteBranch_WhenNothingMergedIt_RemovesIt(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.Git("switch", "--quiet", trunk)

	err := newRepo(f).DeleteBranch(t.Context(), topic)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(f.Git("branch", "--list", topic), ""))
}

func TestRepo_DeleteBranch_WhenHeadIsOnIt_ReturnsGitsMessage(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")

	err := newRepo(f).DeleteBranch(t.Context(), topic)

	checkGitRefused(t, err, "delete branch '"+topic+"'")
}

// checkedOutCase is a layout of worktrees and whether the trunk is checked out in one other than
// the worktree arrange returns.
type checkedOutCase struct {
	// arrange sets up the worktrees and returns the one to read from.
	arrange func(t *testing.T, f *gittest.Fixture) string
	name    string
	want    bool
}

// linkedThroughSymlink leaves the main checkout on topic and links a worktree on the trunk that git
// records under a path through a symbolic link, as another tool or an older git may record it, and
// returns the worktree's real path.
func linkedThroughSymlink(t *testing.T, f *gittest.Fixture) string {
	t.Helper()

	f.Commits(topic, "one")
	dir := linkWorktree(t, f, trunk)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Dir(dir), link); err != nil {
		t.Skipf("symbolic links are not available here: %v", err)
	}

	admin := filepath.Join(f.Dir(), ".git", "worktrees", filepath.Base(dir))
	gittest.WriteFile(t, admin, "gitdir", filepath.ToSlash(filepath.Join(link, filepath.Base(dir), ".git"))+"\n")

	return dir
}

// elsewhereCases returns the layouts in which a worktree other than the one read from has the trunk.
func elsewhereCases() []checkedOutCase {
	return []checkedOutCase{
		{
			name: "linked worktree on the trunk, seen from the main checkout",
			arrange: func(t *testing.T, f *gittest.Fixture) string {
				t.Helper()

				f.Commits(topic, "one")
				linkWorktree(t, f, trunk)

				return f.Dir()
			},
			want: true,
		},
		{
			name: "main checkout on the trunk, seen from a linked worktree",
			arrange: func(t *testing.T, f *gittest.Fixture) string {
				t.Helper()

				f.Git("branch", topic)

				return linkWorktree(t, f, topic)
			},
			want: true,
		},
	}
}

// notElsewhereCases returns the layouts in which no worktree but the one read from has the trunk.
func notElsewhereCases() []checkedOutCase {
	return []checkedOutCase{
		{
			name: "only the current worktree on the trunk",
			arrange: func(t *testing.T, f *gittest.Fixture) string {
				t.Helper()

				f.Git("branch", topic)
				linkWorktree(t, f, topic)

				return f.Dir()
			},
		},
		{
			name: "no worktree on the trunk",
			arrange: func(t *testing.T, f *gittest.Fixture) string {
				t.Helper()

				f.Commits(topic, "one")
				f.Git("branch", side)
				linkWorktree(t, f, side)

				return f.Dir()
			},
		},
		{
			name:    "only the current worktree on the trunk, which git lists through a symbolic link",
			arrange: linkedThroughSymlink,
		},
		{
			name: "linked worktree detached at the trunk's commit",
			arrange: func(t *testing.T, f *gittest.Fixture) string {
				t.Helper()

				f.Commits(topic, "one")
				linkWorktree(t, f, trunk, "--detach")

				return f.Dir()
			},
		},
	}
}

func TestRepo_CheckedOutElsewhere(t *testing.T) {
	t.Parallel()
	for _, tt := range append(elsewhereCases(), notElsewhereCases()...) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			dir := tt.arrange(t, f)
			repo := git.Repo{Runner: git.Runner{Dir: dir, Env: f.Env()}}

			got, err := repo.CheckedOutElsewhere(t.Context(), trunk)
			assert.NilError(t, err)

			assert.Check(t, is.Equal(got, tt.want))
		})
	}
}

func TestRepo_CheckedOutElsewhere_WhenWorktreeListFails_ReturnsItsExitError(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := gittest.New(t, trunk)
	repo := newRepo(f)
	repo.Runner.Env = append(repo.Runner.Env, gittest.FakeGit(t, gittest.Fake{FailOn: []string{"worktree"}})...)

	got, err := repo.CheckedOutElsewhere(t.Context(), trunk)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "CheckedOutElsewhere() = %t, error = %v, want *child.ExitError", got, err)
	assert.Check(t, is.Equal(exitErr.Code, gittest.FailedCode))
}

func TestRepo_CheckedOutElsewhere_WhenOutsideAWorkTree_ReturnsTheExitErrorOfTheTopLevelRead(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	repo := git.Repo{Runner: git.Runner{Dir: filepath.Join(f.Dir(), ".git"), Env: f.Env()}}

	got, err := repo.CheckedOutElsewhere(t.Context(), trunk)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "CheckedOutElsewhere() = %t, error = %v, want *child.ExitError", got, err)
	assert.Check(t, is.Contains(exitErr.Argv, "--show-toplevel"))
}

func TestRepo_CheckedOutElsewhere_WhenTheWorktreeOnTheBranchIsGone_ReturnsErrorNamingIt(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	dir := linkWorktree(t, f, trunk)
	assert.NilError(t, os.RemoveAll(dir))

	got, err := newRepo(f).CheckedOutElsewhere(t.Context(), trunk)

	assert.Check(t, is.ErrorContains(err, "resolve worktree path"), "CheckedOutElsewhere() = %t", got)
	assert.Check(t, is.ErrorContains(err, filepath.Base(dir)))
}

// installURL is where the error for a git that is missing or too old sends the user.
const installURL = "https://git-scm.com/downloads"

// fakeGitRepo returns a repository whose git, first on PATH, prints line as the first line of
// `git --version`. It cannot run in a parallel test: it sets PATH for the whole process.
func fakeGitRepo(t *testing.T, line string) git.Repo {
	t.Helper()

	env := append(gittest.Env(t), fakeGitVersion(t, line)...)

	return git.Repo{Runner: git.Runner{Dir: t.TempDir(), Env: env}}
}

func TestRepo_CheckVersion_WhenGitIsAtOrAboveTheMinimum_ReturnsNil(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "exactly the minimum", line: "git version " + git.MinimumVersion},
		{name: "release candidate newer than the minimum", line: "git version 2.35.0.rc0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: fakeGitRepo sets PATH for the whole process.
			repo := fakeGitRepo(t, tt.line)

			err := repo.CheckVersion(t.Context())

			assert.NilError(t, err, "CheckVersion with %q", tt.line)
		})
	}
}

func TestRepo_CheckVersion_WhenGitIsOlderThanTheMinimum_ReturnsToolTooOldError(t *testing.T) {
	tests := []struct {
		name string
		// line is the first line `git --version` prints.
		line string
		// version is the version the error carries; a line with none carries none.
		version string
	}{
		{name: "one patch release older", line: "git version 2.34.0", version: "2.34.0"},
		{name: "release candidate of the minimum", line: "git version 2.34.1.rc0", version: "2.34.1-rc0"},
		{name: "development build", line: "git version DEV"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: fakeGitRepo sets PATH for the whole process.
			repo := fakeGitRepo(t, tt.line)

			err := repo.CheckVersion(t.Context())

			tooOld, ok := errors.AsType[*child.ToolTooOldError](err)
			assert.Assert(t, ok, "CheckVersion with %q error = %v, want *child.ToolTooOldError", tt.line, err)
			want := child.ToolTooOldError{
				Tool: "git", Version: tt.version, Line: tt.line, Minimum: git.MinimumVersion, InstallURL: installURL,
			}
			assert.Check(t, is.DeepEqual(*tooOld, want))
		})
	}
}

func TestRepo_CheckVersion_WhenGitMissingFromPath_ReturnsToolMissingError(t *testing.T) {
	repo := git.Repo{Runner: newRunner(t)}
	// Not parallel: this empties PATH for the whole process while it runs.
	t.Setenv("PATH", t.TempDir())

	err := repo.CheckVersion(t.Context())

	missing, ok := errors.AsType[*child.ToolMissingError](err)
	assert.Assert(t, ok, "CheckVersion error = %v, want *child.ToolMissingError", err)
	assert.Check(t, is.Equal(missing.Tool, "git"))
}

func TestVersionOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "plain release", line: "git version 2.34.1", want: "2.34.1"},
		{name: "Git for Windows", line: "git version 2.56.0.windows.2", want: "2.56.0"},
		{name: "Apple", line: "git version 2.54.0 (Apple Git-157)", want: "2.54.0"},
		{name: "release candidate", line: "git version 2.35.0.rc0", want: "2.35.0-rc0"},
		{name: "development build", line: "git version DEV"},
		{name: "three components, one not a number", line: "git version 2.x.1"},
		{name: "no git version prefix", line: "2.40.0"},
		{name: "empty line"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := git.VersionOf(tt.line)

			assert.Check(t, is.Equal(got, tt.want), "VersionOf(%q)", tt.line)
		})
	}
}
