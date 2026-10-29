package git_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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

// checkExitErrorOutsideRepo runs each call in its own t.Run on a directory that is not a
// repository, and fails unless it returns a *child.ExitError and no result.
func checkExitErrorOutsideRepo(t *testing.T, calls []repoCall) {
	t.Helper()

	for _, tt := range calls {
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

// fetch fetches ref from the fixture's origin, failing the test when git refuses.
func fetch(t *testing.T, f *gittest.Fixture, ref string) {
	t.Helper()
	f.Git("fetch", "--quiet", "origin", ref)
}

// shadowTrunk creates a local branch named origin/<trunk> at rev, whose short name git would read
// as origin's trunk when it resolved the short ref.
func shadowTrunk(t *testing.T, f *gittest.Fixture, rev string) {
	t.Helper()
	f.Git("branch", "origin/"+trunk, rev)
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
	fetch(t, f, trunk)
	fetch(t, f, pullRef)

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

// renameTracked commits a file and stages its rename, returning the new name. The new name sorts
// before the fixture's dirty file, so git lists that file after the rename and its source path.
func renameTracked(t *testing.T, f *gittest.Fixture) string {
	t.Helper()

	const before, after = "before.txt", "Moved.txt"
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
	fetch(t, f, trunk)
	f.Git("merge", "--quiet", "--no-edit", "origin/"+trunk)
}

// tips holds the shas of a branch that diverged from origin's copy.
type tips struct {
	base  string
	local string
	repo  git.Repo
}

// diverged returns a repository whose topic branch and origin's fetched copy each have one commit
// past their shared base.
func diverged(t *testing.T) tips {
	t.Helper()

	f := gittest.New(t, trunk)
	base := f.Commits(topic, "one")[0]
	f.Push(topic)
	local, _ := f.Diverge(topic)
	fetch(t, f, topic)

	return tips{repo: newRepo(f), base: base, local: local}
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
		{name: "GitDir", call: func(ctx context.Context, repo git.Repo) (any, error) { return repo.GitDir(ctx) }},
	}

	checkExitErrorOutsideRepo(t, tests)
}

func TestRepo_DirtyFiles_WhenStagedAndUnstagedChanges_ListsBoth(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	dirty := f.DirtyFile()
	want := []string{f.StagedFile(), dirty}

	got, err := newRepo(f).DirtyFiles(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got, want))
}

func TestRepo_DirtyFiles_WhenTrackedFileRenamedInIndex_ListsNewNameOnly(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	moved := renameTracked(t, f)
	want := []string{moved, f.DirtyFile()}

	got, err := newRepo(f).DirtyFiles(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got, want))
}

func TestRepo_Divergence_WhenFirstIsAhead_CountsItsCommitsAsAhead(t *testing.T) {
	t.Parallel()
	split := diverged(t)

	got, err := split.repo.Divergence(t.Context(), split.local, split.base)
	assert.NilError(t, err, "Divergence(%s, %s)", split.local, split.base)

	assert.Check(t, is.DeepEqual(got, git.Divergence{Ahead: 1}), "Divergence(%s, %s)", split.local, split.base)
}

// garbledDivergence returns a repository whose git prints out for any rev-list call, and the
// command line of the Divergence call between unknownSHA and itself. It cannot run in a parallel
// test: gittest.FakeGit sets PATH for the whole process.
func garbledDivergence(t *testing.T, out string) (repo git.Repo, call string) {
	t.Helper()

	runner := newRunner(t)
	runner.Env = append(runner.Env, gittest.FakeGit(t, gittest.Fake{AnswerOn: []string{"rev-list"}, Answer: out})...)

	return git.Repo{Runner: runner}, child.CommandLine([]string{"git", "rev-list", "--left-right", "--count", unknownSHA + "..." + unknownSHA})
}

func TestRepo_Divergence_WhenOutputHoldsNoTab_ReturnsErrorNamingTheCallAndItsOutput(t *testing.T) {
	// Not parallel: garbledDivergence sets PATH for the whole process.
	repo, call := garbledDivergence(t, "not counts")

	_, err := repo.Divergence(t.Context(), unknownSHA, unknownSHA)

	assert.Check(t, is.ErrorContains(err, call))
	assert.Check(t, is.ErrorContains(err, strconv.Quote("not counts")))
}

func TestRepo_Divergence_WhenACountIsNotANumber_ReturnsSyntaxErrorNamingTheCall(t *testing.T) {
	// Not parallel: garbledDivergence sets PATH for the whole process.
	tests := []struct {
		name string
		out  string
	}{
		{name: "ahead count not a number", out: "x\t1"},
		{name: "behind count not a number", out: "1\tx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, call := garbledDivergence(t, tt.out)

			_, err := repo.Divergence(t.Context(), unknownSHA, unknownSHA)

			assert.Check(t, is.ErrorContains(err, call))
			assert.Check(t, is.ErrorIs(err, strconv.ErrSyntax))
		})
	}
}

func TestRepo_Contains(t *testing.T) {
	t.Parallel()
	split := diverged(t)
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "ahead", a: split.local, b: split.base, want: true},
		{name: "behind", a: split.base, b: split.local, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := split.repo.Contains(t.Context(), tt.a, tt.b)
			assert.NilError(t, err, "Contains(%s, %s)", tt.a, tt.b)

			assert.Check(t, is.Equal(got, tt.want), "Contains(%s, %s)", tt.a, tt.b)
		})
	}
}

func TestRepo_CommitsPast_WhenALocalBranchIsNamedLikeOriginsTrunk_ListsTheCommitsPastOriginsTrunk(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	sha := f.Commits(topic, "one")[0]
	shadowTrunk(t, f, sha)
	tip := f.Git("rev-parse", "HEAD")

	got, err := newRepo(f).CommitsPast(t.Context(), tip, trunk)
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got, []git.Commit{{SHA: sha, Subject: "one"}}, cmpopts.EquateEmpty()))
}

func TestRepo_LocalTrunkAhead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		// arrange shapes the local trunk and returns the commits it has past origin's trunk.
		arrange   func(t *testing.T, f *gittest.Fixture) []git.Commit
		name      string
		wantAhead bool
	}{
		{
			name: "no local trunk",
			arrange: func(t *testing.T, f *gittest.Fixture) []git.Commit {
				t.Helper()
				f.Commits(topic, "one")
				f.Git("branch", "--delete", "--force", trunk)

				return nil
			},
		},
		{
			name: "local trunk behind origin's",
			arrange: func(t *testing.T, f *gittest.Fixture) []git.Commit {
				t.Helper()
				f.AdvanceOrigin(trunk)
				fetch(t, f, trunk)

				return nil
			},
		},
		{
			name: "local trunk with a commit origin's lacks",
			arrange: func(t *testing.T, f *gittest.Fixture) []git.Commit {
				t.Helper()
				f.Commits(topic, "one")
				sha := f.AdvanceTrunk()

				return []git.Commit{{SHA: sha, Subject: "local change on " + trunk}}
			},
			wantAhead: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			want := tt.arrange(t, f)

			ahead, past, err := newRepo(f).LocalTrunkAhead(t.Context(), trunk)
			assert.NilError(t, err)

			assert.Check(t, is.Equal(ahead, tt.wantAhead))
			assert.Check(t, is.DeepEqual(past, want))
		})
	}
}

func TestRepo_BranchesContaining(t *testing.T) {
	t.Parallel()
	tests := []struct {
		// arrange changes where HEAD stands; nil leaves the topic branch checked out.
		arrange func(f *gittest.Fixture)
		name    string
		want    []string
	}{
		{name: "another branch carrying the commit is listed alone", want: []string{sibling}},
		{name: "head detached lists every branch carrying the commit", arrange: (*gittest.Fixture).Detach, want: []string{sibling, topic}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, first := topicWithSibling(t)
			if tt.arrange != nil {
				tt.arrange(f)
			}

			got, err := newRepo(f).BranchesContaining(t.Context(), first)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, tt.want))
		})
	}
}

// ownCommitsCase is one case of OwnCommits: arrange shapes the repository and returns the commits
// OwnCommits must report.
type ownCommitsCase struct {
	arrange func(t *testing.T, f *gittest.Fixture) []git.Commit
	name    string
}

// ownCommitsCases returns the cases of OwnCommits.
func ownCommitsCases() []ownCommitsCase {
	return []ownCommitsCase{
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
}

func TestRepo_OwnCommits(t *testing.T) {
	t.Parallel()
	for _, tt := range ownCommitsCases() {
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

func TestRepo_Unlanded_WhenACommitFollowsTheTrunkMergedIn_ListsOnlyThatCommit(t *testing.T) {
	t.Parallel()
	f, merged := landed(t)
	mergeTrunk(t, f)
	want := []git.Commit{{SHA: f.Commits(topic, "three")[0], Subject: "three"}}
	tip := f.Git("rev-parse", "HEAD")

	got, err := newRepo(f).Unlanded(t.Context(), merged, tip, trunk)
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got, want, cmpopts.EquateEmpty()))
}

// previousBranch returns a repository checked out on the trunk after topic, so that git reads
// @{-1} as topic.
func previousBranch(t *testing.T) git.Repo {
	t.Helper()

	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.Git("switch", "--quiet", trunk)

	return newRepo(f)
}

func TestRepo_CheckBranchName_WhenGitAcceptsTheName_ReturnsNil(t *testing.T) {
	t.Parallel()
	const name = "fix/parser"

	err := previousBranch(t).CheckBranchName(t.Context(), name)

	assert.NilError(t, err, "CheckBranchName(%q)", name)
}

func TestRepo_CheckBranchName_WhenNameCannotBeABranch_ReturnsBranchNameErrorNamingIt(t *testing.T) {
	t.Parallel()
	repo := previousBranch(t)
	tests := []struct {
		name string
		in   string
	}{
		{name: "git refuses the name", in: "fix..parser"},
		{name: "starts with a dash", in: "-parser"},
		{name: "names the previous branch", in: "@{-1}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := repo.CheckBranchName(t.Context(), tt.in)

			nameErr, ok := errors.AsType[*git.BranchNameError](err)
			assert.Assert(t, ok, "CheckBranchName(%q) error = %v, want *git.BranchNameError", tt.in, err)
			assert.Check(t, is.Equal(nameErr.Name, tt.in))
		})
	}
}

func TestRepo_CheckBranchName_WhenGitRefusesTheName_ReasonIsGitsMessage(t *testing.T) {
	t.Parallel()
	const name = "fix..parser"

	err := previousBranch(t).CheckBranchName(t.Context(), name)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "CheckBranchName(%q) error = %v, want it to carry git's *child.ExitError", name, err)
	nameErr, ok := errors.AsType[*git.BranchNameError](err)
	assert.Assert(t, ok, "CheckBranchName(%q) error = %v, want *git.BranchNameError", name, err)
	assert.Check(t, nameErr.Reason() != "", "CheckBranchName(%q) gives no reason from git", name)
	assert.Check(t, is.Equal(nameErr.Reason(), strings.TrimSpace(exitErr.Stderr)))
}

func TestRepo_CheckBranchName_WhenGitIsNotAsked_GivesNoReason(t *testing.T) {
	t.Parallel()
	const name = "-parser"

	err := previousBranch(t).CheckBranchName(t.Context(), name)

	nameErr, ok := errors.AsType[*git.BranchNameError](err)
	assert.Assert(t, ok, "CheckBranchName(%q) error = %v, want *git.BranchNameError", name, err)
	assert.Check(t, is.Equal(nameErr.Reason(), ""))
}

const freshBranch = "fix/parser"

func TestRepo_CreateBranch_WhenOriginsTrunkIsFetched_SwitchesToABranchAtIt(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	shadowTrunk(t, f, f.Commits(topic, "one")[0])
	f.AdvanceTrunk()
	f.AdvanceOrigin(trunk)
	want := f.Git("rev-parse", "refs/remotes/origin/"+trunk)

	err := newRepo(f).CreateBranch(t.Context(), freshBranch, trunk)
	assert.NilError(t, err, "CreateBranch(%q, %q)", freshBranch, trunk)

	assert.Check(t, is.Equal(f.Git("symbolic-ref", "HEAD"), "refs/heads/"+freshBranch))
	assert.Check(t, is.Equal(f.Git("rev-parse", "refs/heads/"+freshBranch), want))
}

// changedOffTrunk commits a file to topic alone and changes it without committing, so that
// switching to origin's trunk, which lacks the file, would discard the change. It returns its path.
func changedOffTrunk(t *testing.T, f *gittest.Fixture) string {
	t.Helper()

	const path = "notes.txt"
	f.Commits(topic, "one")
	gittest.WriteFile(t, f.Dir(), path, "first draft\n")
	f.Git("add", path)
	f.Git("commit", "--quiet", "--message", "add "+path)
	gittest.WriteFile(t, f.Dir(), path, "second draft\n")

	return path
}

func TestRepo_CreateBranch_WhenAFileWouldBeOverwritten_ReturnsOverwriteErrorNamingTheFile(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	path := changedOffTrunk(t, f)

	err := newRepo(f).CreateBranch(t.Context(), freshBranch, trunk)

	overwrite, ok := errors.AsType[*git.OverwriteError](err)
	assert.Assert(t, ok, "CreateBranch(%q, %q) error = %v, want *git.OverwriteError", freshBranch, trunk, err)
	assert.Check(t, is.DeepEqual(overwrite.Files, []string{path}))
}

func TestRepo_DetachAtOrigin_WhenLocalBranchNamedLikeOriginsTrunk_DetachesAtOriginsTrunk(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	shadowTrunk(t, f, f.Commits(topic, "one")[0])
	want := f.Git("rev-parse", "refs/remotes/origin/"+trunk)

	err := newRepo(f).DetachAtOrigin(t.Context(), trunk)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(f.Git("rev-parse", "HEAD"), want))
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
	dir := f.TrunkWorktree()
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
				f.TrunkWorktree()

				return f.Dir()
			},
			want: true,
		},
		{
			name: "main checkout on the trunk, seen from a linked worktree",
			arrange: func(t *testing.T, f *gittest.Fixture) string {
				t.Helper()

				f.Git("branch", topic)

				return f.BranchWorktree(topic)
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
				f.BranchWorktree(topic)

				return f.Dir()
			},
		},
		{
			name:    "only the current worktree on the trunk, which git lists through a symbolic link",
			arrange: linkedThroughSymlink,
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

func TestRepo_CheckedOutElsewhere_WhenOutsideAWorkTree_ReturnsExitError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	repo := git.Repo{Runner: git.Runner{Dir: filepath.Join(f.Dir(), ".git"), Env: f.Env()}}

	got, err := repo.CheckedOutElsewhere(t.Context(), trunk)

	_, ok := errors.AsType[*child.ExitError](err)
	assert.Check(t, ok, "CheckedOutElsewhere() = %t, error = %v, want *child.ExitError", got, err)
}

func TestRepo_CheckedOutElsewhere_WhenTheWorktreeOnTheBranchIsGone_ReturnsNotExistError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	dir := f.TrunkWorktree()
	assert.NilError(t, os.RemoveAll(dir))

	got, err := newRepo(f).CheckedOutElsewhere(t.Context(), trunk)

	assert.Check(t, is.ErrorIs(err, fs.ErrNotExist), "CheckedOutElsewhere() = %t", got)
}

// versionCase is a line git prints for --version and the version VersionOf reads from it.
type versionCase struct {
	name string
	line string
	want string
}

// versionCases returns the lines VersionOf reads, and the version it finds in each.
func versionCases() []versionCase {
	return []versionCase{
		{name: "Git for Windows", line: "git version 2.56.0.windows.2", want: "2.56.0"},
		{name: "Apple", line: "git version 2.54.0 (Apple Git-157)", want: "2.54.0"},
		{name: "release candidate", line: "git version 2.35.0.rc0", want: "2.35.0-rc0"},
		{name: "development build", line: "git version DEV"},
		{name: "three components, one not a number", line: "git version 2.x.1"},
		{name: "no git version prefix", line: "2.40.0"},
	}
}

func TestVersionOf(t *testing.T) {
	t.Parallel()
	for _, tt := range versionCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := git.VersionOf(tt.line)

			assert.Check(t, is.Equal(got, tt.want), "VersionOf(%q)", tt.line)
		})
	}
}

func FuzzVersionOf(f *testing.F) {
	for _, tt := range versionCases() {
		f.Add(tt.line)
	}
	f.Fuzz(func(t *testing.T, line string) {
		got := git.VersionOf(line)
		if got == "" {
			return
		}

		numbers, _, _ := strings.Cut(got, "-")
		assert.Check(t, strings.HasPrefix(line, "git version "+numbers), "VersionOf(%q) = %q", line, got)
	})
}

func TestRepo_WhenAGitReadFails_ReturnsItsExitError(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	trunkAhead := func(f *gittest.Fixture) string {
		f.Commits(topic, "one")
		f.AdvanceTrunk()

		return ""
	}
	localTrunkAhead := func(ctx context.Context, repo git.Repo, _ string) (any, error) {
		ahead, past, err := repo.LocalTrunkAhead(ctx, trunk)
		if err != nil {
			return nil, err
		}

		return []any{ahead, past}, nil
	}
	tests := []struct {
		// arrange shapes the repository before git is faked, and returns the argument of call.
		arrange func(f *gittest.Fixture) string
		call    func(ctx context.Context, repo git.Repo, arg string) (any, error)
		name    string
		failOn  []string
	}{
		{name: "local trunk ahead, its ref read", failOn: []string{"show-ref"}, arrange: trunkAhead, call: localTrunkAhead},
		{name: "local trunk ahead, its merge base read", failOn: []string{"merge-base"}, arrange: trunkAhead, call: localTrunkAhead},
		{name: "local trunk ahead, its commits read", failOn: []string{"log"}, arrange: trunkAhead, call: localTrunkAhead},
		{
			name: "branches containing, the current branch read", failOn: []string{"symbolic-ref"},
			arrange: func(f *gittest.Fixture) string { return f.Commits(topic, "one")[0] },
			call: func(ctx context.Context, repo git.Repo, first string) (any, error) {
				return repo.BranchesContaining(ctx, first)
			},
		},
		{
			name: "checked out elsewhere, the worktree list read", failOn: []string{"worktree"},
			arrange: func(*gittest.Fixture) string { return "" },
			call: func(ctx context.Context, repo git.Repo, _ string) (any, error) {
				return repo.CheckedOutElsewhere(ctx, trunk)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := gittest.New(t, trunk)
			arg := tt.arrange(f)
			repo := newRepo(f)
			repo.Runner.Env = append(repo.Runner.Env, gittest.FakeGit(t, gittest.Fake{FailOn: tt.failOn})...)

			got, err := tt.call(t.Context(), repo, arg)

			exitErr, ok := errors.AsType[*child.ExitError](err)
			assert.Assert(t, ok, "%s: got %v, error = %v, want *child.ExitError", tt.name, got, err)
			assert.Check(t, is.Equal(exitErr.Code, gittest.FailedCode))
		})
	}
}
