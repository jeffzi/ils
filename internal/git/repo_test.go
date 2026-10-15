package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	pullNumber = 7
	pullRef    = "refs/pull/7/head"
	unknownSHA = "0123456789012345678901234567890123456789"
)

func newRepo(f *gittest.Fixture) git.Repo {
	return git.Repo{Runner: git.Runner{Dir: f.Dir(), Env: f.Env()}}
}

// repoCall is one call on the repository, reduced to its error.
type repoCall struct {
	call func(ctx context.Context, repo git.Repo) error
	name string
}

// checkCanceled runs each call on one repository under a context that has already ended, and fails
// unless the call returns the context's error.
func checkCanceled(t *testing.T, calls []repoCall) {
	t.Helper()

	repo := newRepo(gittest.New(t, trunk))
	for _, tt := range calls {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			err := tt.call(ctx, repo)

			assert.Check(t, is.ErrorIs(err, context.Canceled))
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
		{name: "Standing", call: func(ctx context.Context, repo git.Repo) error { _, err := repo.Standing(ctx); return err }},
		{name: "HeadSHA", call: func(ctx context.Context, repo git.Repo) error { _, err := repo.HeadSHA(ctx); return err }},
		{name: "GitDir", call: func(ctx context.Context, repo git.Repo) error { _, err := repo.GitDir(ctx); return err }},
		{name: "HasCommits", call: func(ctx context.Context, repo git.Repo) error {
			_, err := repo.HasCommits(ctx, "topic")

			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := outsideRepo(t)

			err := tt.call(t.Context(), repo)

			_, ok := errors.AsType[*child.ExitError](err)
			assert.Check(t, ok, "%s() error = %v, want *child.ExitError", tt.name, err)
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
	f := gittest.New(t, trunk)
	first := f.Commits(topic, "one", "two")[0]
	f.Sibling("team/other", topic)
	f.Git("switch", "--quiet", topic)

	got, err := newRepo(f).BranchesContaining(t.Context(), first)
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got, []string{"team/other"}))
}

func TestRepo_BranchesContaining_WhenHeadDetached_ListsEveryBranchCarryingCommit(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	first := f.Commits(topic, "one", "two")[0]
	f.Sibling("team/other", topic)
	f.Git("switch", "--quiet", topic)
	f.Detach()

	got, err := newRepo(f).BranchesContaining(t.Context(), first)
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got, []string{"team/other", topic}))
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
	f.SquashOnOrigin(merged, "topic (#7)")
	f.RemoveFromOrigin(topic)
	fetch(f, trunk)

	got, err := newRepo(f).Unlanded(t.Context(), merged, tip, trunk)
	assert.NilError(t, err)

	assert.Check(t, is.Len(got, 0))
}

func TestRepo_WhenContextCanceled_ReturnsContextError(t *testing.T) {
	t.Parallel()

	checkCanceled(t, []repoCall{
		{name: "Divergence", call: func(ctx context.Context, repo git.Repo) error {
			_, err := repo.Divergence(ctx, unknownSHA, unknownSHA)
			return err
		}},
		{name: "CommitsPast", call: func(ctx context.Context, repo git.Repo) error {
			_, err := repo.CommitsPast(ctx, unknownSHA, trunk)
			return err
		}},
		{name: "BranchesContaining", call: func(ctx context.Context, repo git.Repo) error {
			_, err := repo.BranchesContaining(ctx, unknownSHA)
			return err
		}},
		{name: "Unlanded", call: func(ctx context.Context, repo git.Repo) error {
			_, err := repo.Unlanded(ctx, unknownSHA, unknownSHA, trunk)
			return err
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

// installURL is where the error for a git that is too old sends the user.
const installURL = "https://git-scm.com/downloads"

// fakeGitRepo returns a repository whose git, first on PATH, prints line as the first line of
// `git --version`. It cannot run in a parallel test: it sets PATH for the whole process.
func fakeGitRepo(t *testing.T, line string) git.Repo {
	t.Helper()

	self, err := os.Executable()
	assert.NilError(t, err)

	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}

	dir := t.TempDir()
	assert.NilError(t, os.Link(self, filepath.Join(dir, name)))
	t.Setenv("PATH", dir)

	env := append(gittest.Env(t),
		versionLineEnv+"="+line,
		// The race detector's exit pause would otherwise cost a second per git call.
		"GORACE=atexit_sleep_ms=0",
	)

	return git.Repo{Runner: git.Runner{Dir: t.TempDir(), Env: env}}
}

func TestRepo_CheckVersion_WhenGitIsAtOrAboveTheMinimum_ReturnsNil(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "exactly the minimum", line: "git version " + git.MinimumVersion},
		{name: "minor number with more digits", line: "git version 2.100.0"},
		{name: "Git for Windows", line: "git version 2.56.0.windows.2"},
		{name: "Apple", line: "git version 2.54.0 (Apple Git-157)"},
		{name: "release candidate newer than the minimum", line: "git version 2.35.0.rc0"},
		{name: "next major release", line: "git version 3.0.0"},
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
		{name: "one major release older", line: "git version 1.9.5", version: "1.9.5"},
		{name: "Git for Windows", line: "git version 2.30.2.windows.1", version: "2.30.2"},
		{name: "Apple", line: "git version 2.24.3 (Apple Git-128)", version: "2.24.3"},
		{name: "release candidate of the minimum", line: "git version 2.34.1.rc0", version: "2.34.1-rc0"},
		{name: "development build", line: "git version DEV"},
		{name: "no git version prefix", line: "2.40.0"},
		{name: "empty first line"},
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

func TestRepo_CheckVersion_WhenGitIsInstalled_RunsGitVersion(t *testing.T) {
	t.Parallel()
	runner, recorder := newRecordedRunner(t)
	repo := git.Repo{Runner: runner}

	err := repo.CheckVersion(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(recorder.Calls(), [][]string{{"git", "--version"}}))
}

func TestRepo_CheckVersion_WhenGitMissingFromPath_ReturnsNotFoundError(t *testing.T) {
	repo := git.Repo{Runner: newRunner(t)}
	// Not parallel: this empties PATH for the whole process while it runs.
	t.Setenv("PATH", t.TempDir())

	err := repo.CheckVersion(t.Context())

	notFound, ok := errors.AsType[*git.NotFoundError](err)
	assert.Assert(t, ok, "CheckVersion error = %v, want *git.NotFoundError", err)
	assert.Check(t, is.Equal(notFound.Name, "git"))
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
