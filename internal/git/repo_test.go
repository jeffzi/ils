package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
)

const (
	trunk      = "develop"
	topic      = "team/topic"
	side       = "team/side"
	otherPath  = "../elsewhere"
	httpsURL   = "https://github.com/jeffzi/ils.git"
	sshURL     = "git@github.com:jeffzi/tools.git"
	localPath  = "/srv/project"
	pullRef    = "refs/pull/7/head"
	unknownSHA = "0123456789012345678901234567890123456789"
)

// newRepo returns the typed git calls over the fixture's working repository.
func newRepo(f *gittest.Fixture) git.Repo {
	return git.Repo{Runner: git.Runner{Dir: f.Dir(), Env: f.Env()}}
}

// outsideRepo returns the typed git calls over a directory that is not a repository.
func outsideRepo(t *testing.T) git.Repo {
	t.Helper()

	return git.Repo{Runner: newRunner(t)}
}

// unreachable points the fixture's origin at a location that holds no repository.
func unreachable(f *gittest.Fixture) {
	f.SetOriginURL(filepath.ToSlash(f.OriginDir()) + "-missing")
}

// decoyHead gives origin a symbolic ref outside its own HEAD whose name ends in HEAD and that names
// a branch called decoy. The test fails here unless origin lists that ref when asked for HEAD.
func decoyHead(t *testing.T, f *gittest.Fixture) {
	t.Helper()

	const name, target = "refs/remotes/upstream/HEAD", "refs/heads/decoy"

	f.OriginGit("update-ref", target, f.OriginGit("rev-parse", "HEAD"))
	f.OriginGit("symbolic-ref", name, target)

	listed := f.Git("ls-remote", "--symref", "origin", "HEAD")
	if line := "ref: " + target + "\t" + name; !strings.Contains(listed, line) {
		t.Fatalf("git ls-remote --symref origin HEAD printed %q, want it to hold %q", listed, line)
	}
}

// landed returns a fixture whose topic branch was squashed onto origin's trunk as pull request 7 and
// deleted from origin, with the trunk fetched, and the merged head read from the pull request's ref.
func landed(t *testing.T) (f *gittest.Fixture, merged string) {
	t.Helper()

	f = gittest.New(t, trunk)
	shas := f.Commits(topic, "one", "two")
	f.Push(topic)
	f.PullRef(7, shas[1])
	f.SquashOnOrigin(shas[1], "topic (#7)")
	f.RemoveFromOrigin(topic)
	f.Git("fetch", "--quiet", "origin", trunk)
	f.Git("fetch", "--quiet", "origin", pullRef)

	return f, f.Git("rev-parse", "FETCH_HEAD")
}

// renameTracked commits a file and stages its rename, returning the new name.
func renameTracked(t *testing.T, f *gittest.Fixture) string {
	t.Helper()

	const before, after = "before.txt", "moved.txt"
	if err := os.WriteFile(filepath.Join(f.Dir(), before), []byte("kept as is\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", before, err)
	}
	f.Git("add", before)
	f.Git("commit", "--quiet", "--message", "add "+before)
	f.Git("mv", before, after)

	return after
}

// mergeTrunk moves origin's trunk once more, fetches it, and merges it into the branch checked out.
func mergeTrunk(f *gittest.Fixture) {
	f.AdvanceOrigin(trunk)
	f.Git("fetch", "--quiet", "origin", trunk)
	f.Git("merge", "--quiet", "--no-edit", "origin/"+trunk)
}

// mergeSide merges a side branch holding one commit into the topic branch and returns that commit.
func mergeSide(f *gittest.Fixture) []git.Commit {
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
	f.Git("fetch", "--quiet", "origin", topic)

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
			if err != nil {
				t.Fatalf("Standing() error = %v, want nil", err)
			}

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Standing() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRepo_HeadSHA_WhenOnBranch_ReturnsTipSHA(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	want := f.Commits(topic, "one")[0]

	got, err := newRepo(f).HeadSHA(t.Context())
	if err != nil {
		t.Fatalf("HeadSHA() error = %v, want nil", err)
	}

	if got != want {
		t.Errorf("HeadSHA() = %q, want %q", got, want)
	}
}

func TestRepo_GitDir_WhenInRepository_ReturnsAbsoluteGitDir(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	want := filepath.Join(f.Dir(), ".git")

	got, err := newRepo(f).GitDir(t.Context())
	if err != nil {
		t.Fatalf("GitDir() error = %v, want nil", err)
	}

	if got != want {
		t.Errorf("GitDir() = %q, want %q", got, want)
	}
}

func TestRepo_RunsExactGitCall(t *testing.T) {
	t.Parallel()
	tests := []struct {
		call func(ctx context.Context, repo git.Repo) error
		name string
		want []string
	}{
		{
			name: "HeadSHA",
			call: func(ctx context.Context, repo git.Repo) error { _, err := repo.HeadSHA(ctx); return err },
			want: []string{"git", "rev-parse", "HEAD"},
		},
		{
			name: "GitDir",
			call: func(ctx context.Context, repo git.Repo) error { _, err := repo.GitDir(ctx); return err },
			want: []string{"git", "rev-parse", "--absolute-git-dir"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			recorder := &git.Recorder{}
			repo := git.Repo{Runner: git.Runner{Dir: f.Dir(), Env: f.Env(), Recorder: recorder}}

			err := tt.call(t.Context(), repo)
			if err != nil {
				t.Fatalf("%s() error = %v, want nil", tt.name, err)
			}

			if diff := cmp.Diff([][]string{tt.want}, recorder.Calls()); diff != "" {
				t.Errorf("%s() recorded calls mismatch (-want +got):\n%s", tt.name, diff)
			}
		})
	}
}

func TestRepo_WhenNotARepository_ReturnsExitError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		call func(ctx context.Context, repo git.Repo) error
		name string
	}{
		{name: "Standing", call: func(ctx context.Context, repo git.Repo) error { _, err := repo.Standing(ctx); return err }},
		{name: "HeadSHA", call: func(ctx context.Context, repo git.Repo) error { _, err := repo.HeadSHA(ctx); return err }},
		{name: "GitDir", call: func(ctx context.Context, repo git.Repo) error { _, err := repo.GitDir(ctx); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := outsideRepo(t)

			err := tt.call(t.Context(), repo)

			if _, ok := errors.AsType[*git.ExitError](err); !ok {
				t.Errorf("%s() error = %v, want *git.ExitError", tt.name, err)
			}
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
			if err != nil {
				t.Fatalf("DirtyFiles() error = %v, want nil", err)
			}

			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("DirtyFiles() mismatch (-want +got):\n%s", diff)
			}
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
			if err != nil {
				t.Fatalf("DirtyFiles() error = %v, want nil", err)
			}

			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("DirtyFiles() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRepo_DirtyFiles_WhenStatusCannotRead_ReturnsExitError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arrange func(t *testing.T) git.Repo
		name    string
	}{
		{name: "not a repository", arrange: outsideRepo},
		{
			name: "corrupt index",
			arrange: func(t *testing.T) git.Repo {
				t.Helper()

				f := gittest.New(t, trunk)
				if err := os.WriteFile(filepath.Join(f.Dir(), ".git", "index"), []byte("corrupt"), 0o600); err != nil {
					t.Fatalf("overwrite index: %v", err)
				}

				return newRepo(f)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := tt.arrange(t)

			got, err := repo.DirtyFiles(t.Context())

			if _, ok := errors.AsType[*git.ExitError](err); !ok {
				t.Errorf("DirtyFiles() = %v, error = %v, want *git.ExitError", got, err)
			}
		})
	}
}

func TestRepo_OriginURLs_WhenFetchAndPushDiffer_ReturnsBothParsedAndRaw(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(httpsURL)
	f.SetPushURL(sshURL)

	got, err := newRepo(f).OriginURLs(t.Context())
	if err != nil {
		t.Fatalf("OriginURLs() error = %v, want nil", err)
	}

	want := git.OriginURLs{
		Fetch:    git.Origin{Host: "github.com", Owner: "jeffzi", Name: "ils"},
		Push:     git.Origin{Host: "github.com", Owner: "jeffzi", Name: "tools"},
		FetchURL: httpsURL,
		PushURL:  sshURL,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("OriginURLs() mismatch (-want +got):\n%s", diff)
	}
}

func TestRepo_OriginURLs_WhenURLDoesNotParse_ReturnsOriginErrorWithRawURLsAndParsedSide(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		named string
		want  git.OriginURLs
	}{
		{
			name:  "fetch url",
			named: localPath,
			want: git.OriginURLs{
				Push:     git.Origin{Host: "github.com", Owner: "jeffzi", Name: "tools"},
				FetchURL: localPath,
				PushURL:  sshURL,
			},
		},
		{
			name:  "push url",
			named: localPath,
			want: git.OriginURLs{
				Fetch:    git.Origin{Host: "github.com", Owner: "jeffzi", Name: "ils"},
				FetchURL: httpsURL,
				PushURL:  localPath,
			},
		},
		{name: "both urls", named: localPath, want: git.OriginURLs{FetchURL: localPath, PushURL: otherPath}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.SetOriginURL(tt.want.FetchURL)
			f.SetPushURL(tt.want.PushURL)

			got, err := newRepo(f).OriginURLs(t.Context())

			originErr, ok := errors.AsType[*git.OriginError](err)
			if !ok {
				t.Fatalf("OriginURLs() error = %v, want *git.OriginError", err)
			}
			if originErr.URL != tt.named {
				t.Errorf("OriginURLs() error names %q, want %q", originErr.URL, tt.named)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("OriginURLs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRepo_Trunk_WhenOriginAnswers_ReturnsDefaultBranch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		trunk string
	}{
		{name: "plain name", trunk: trunk},
		{name: "name with a slash", trunk: "release/next"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, tt.trunk)

			got, err := newRepo(f).Trunk(t.Context())
			if err != nil {
				t.Fatalf("Trunk() error = %v, want nil", err)
			}

			if got != tt.trunk {
				t.Errorf("Trunk() = %q, want %q", got, tt.trunk)
			}
		})
	}
}

func TestRepo_Trunk_WhenOriginDoesNotAnswer_ReturnsRemoteError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	unreachable(f)

	got, err := newRepo(f).Trunk(t.Context())

	if _, ok := errors.AsType[*git.RemoteError](err); !ok {
		t.Fatalf("Trunk() = %q, error = %v, want *git.RemoteError", got, err)
	}
	if phrase := "origin could not be reached"; !strings.Contains(err.Error(), phrase) {
		t.Errorf("Trunk() error text = %q, want it to contain %q", err.Error(), phrase)
	}
}

func TestRepo_Trunk_WhenContextEnded_ReturnsRemoteError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got, err := newRepo(f).Trunk(ctx)

	if _, ok := errors.AsType[*git.RemoteError](err); !ok {
		t.Errorf("Trunk() = %q, error = %v, want *git.RemoteError", got, err)
	}
}

func TestRepo_Trunk_WhenOriginHeadIsNotSymbolic_ReturnsErrNoSymref(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.OriginGit("update-ref", "--no-deref", "HEAD", f.OriginGit("rev-parse", "HEAD"))

	got, err := newRepo(f).Trunk(t.Context())

	if !errors.Is(err, git.ErrNoSymref) {
		t.Fatalf("Trunk() = %q, error = %v, want git.ErrNoSymref", got, err)
	}
	want := "git ls-remote --symref origin HEAD: origin's HEAD names no branch"
	if err.Error() != want {
		t.Errorf("Trunk() error text = %q, want %q", err.Error(), want)
	}
}

func TestRepo_Trunk_WhenOriginListsAnotherSymbolicHead_ReturnsOwnDefaultBranch(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	decoyHead(t, f)

	got, err := newRepo(f).Trunk(t.Context())
	if err != nil {
		t.Fatalf("Trunk() error = %v, want nil", err)
	}

	if got != trunk {
		t.Errorf("Trunk() = %q, want %q", got, trunk)
	}
}

func TestRepo_Trunk_WhenOnlyAnotherHeadIsSymbolic_ReturnsErrNoSymref(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.OriginGit("update-ref", "--no-deref", "HEAD", f.OriginGit("rev-parse", "HEAD"))
	decoyHead(t, f)

	got, err := newRepo(f).Trunk(t.Context())

	if !errors.Is(err, git.ErrNoSymref) || got != "" {
		t.Errorf("Trunk() = %q, error = %v, want no branch and git.ErrNoSymref", got, err)
	}
}

func TestRepo_Trunk_WhenOriginHeadPointsOutsideBranches_ReturnsError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.OriginGit("update-ref", "refs/odd/place", f.OriginGit("rev-parse", "HEAD"))
	f.OriginGit("symbolic-ref", "HEAD", "refs/odd/place")

	got, err := newRepo(f).Trunk(t.Context())

	if err == nil || got != "" {
		t.Errorf("Trunk() = %q, error = %v, want no branch and an error", got, err)
	}
}

func TestRepo_RemoteBranch_WhenOnOrigin_ReturnsItsSHA(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	want := f.Commits(topic, "one")[0]
	f.Push(topic)

	got, found, err := newRepo(f).RemoteBranch(t.Context(), topic)
	if err != nil {
		t.Fatalf("RemoteBranch(%q) error = %v, want nil", topic, err)
	}

	if !found || got != want {
		t.Errorf("RemoteBranch(%q) = %q, %t, want %q, true", topic, got, found, want)
	}
}

func TestRepo_RemoteBranch_WhenNotOnOrigin_ReportsAbsent(t *testing.T) {
	t.Parallel()
	const asked = "topic"
	tests := []struct {
		arrange func(f *gittest.Fixture)
		name    string
	}{
		{name: "never pushed", arrange: func(*gittest.Fixture) {}},
		{name: "only a longer name ending the same", arrange: func(f *gittest.Fixture) { f.Push(topic) }},
		{
			name: "only a ref outside the branches ending the same",
			arrange: func(f *gittest.Fixture) {
				f.OriginGit("update-ref", "refs/odd/refs/heads/"+asked, f.OriginGit("rev-parse", "HEAD"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.Commits(topic, "one")
			tt.arrange(f)

			got, found, err := newRepo(f).RemoteBranch(t.Context(), asked)
			if err != nil {
				t.Fatalf("RemoteBranch(%q) error = %v, want nil", asked, err)
			}

			if found || got != "" {
				t.Errorf("RemoteBranch(%q) = %q, %t, want absent", asked, got, found)
			}
		})
	}
}

func TestRepo_RemoteBranch_WhenOriginDoesNotAnswer_ReturnsRemoteError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	unreachable(f)

	_, found, err := newRepo(f).RemoteBranch(t.Context(), trunk)

	if _, ok := errors.AsType[*git.RemoteError](err); !ok {
		t.Errorf("RemoteBranch(%q) found = %t, error = %v, want *git.RemoteError", trunk, found, err)
	}
}

func TestRepo_Fetch_WhenRefOnOrigin_ReturnsFetchedSHA(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arrange func(f *gittest.Fixture) string
		name    string
		ref     string
	}{
		{
			name: "branch origin moved",
			ref:  topic,
			arrange: func(f *gittest.Fixture) string {
				f.Commits(topic, "one")
				f.Push(topic)

				return f.AdvanceOrigin(topic)
			},
		},
		{
			name: "pull request head",
			ref:  pullRef,
			arrange: func(f *gittest.Fixture) string {
				sha := f.Commits(topic, "one")[0]
				f.PullRef(7, sha)

				return sha
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			want := tt.arrange(f)

			got, err := newRepo(f).Fetch(t.Context(), tt.ref)
			if err != nil {
				t.Fatalf("Fetch(%q) error = %v, want nil", tt.ref, err)
			}

			if got != want {
				t.Errorf("Fetch(%q) = %q, want %q", tt.ref, got, want)
			}
		})
	}
}

func TestRepo_Fetch_WhenTrunkMovedOnOrigin_MovesItsRemoteTrackingRef(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	want := f.AdvanceOrigin(trunk)

	_, err := newRepo(f).Fetch(t.Context(), trunk)
	if err != nil {
		t.Fatalf("Fetch(%q) error = %v, want nil", trunk, err)
	}

	if got := f.Git("rev-parse", "origin/"+trunk); got != want {
		t.Errorf("origin/%s after Fetch(%q) = %q, want %q", trunk, trunk, got, want)
	}
}

func TestRepo_Fetch_WhenRefNotOnOrigin_ReturnsRemoteError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)

	got, err := newRepo(f).Fetch(t.Context(), pullRef)

	if _, ok := errors.AsType[*git.RemoteError](err); !ok {
		t.Errorf("Fetch(%q) = %q, error = %v, want *git.RemoteError", pullRef, got, err)
	}
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
			if err != nil {
				t.Fatalf("Divergence(%s, %s) error = %v, want nil", tt.a, tt.b, err)
			}

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Divergence(%s, %s) mismatch (-want +got):\n%s", tt.a, tt.b, diff)
			}
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
			if err != nil {
				t.Fatalf("Contains(%s, %s) error = %v, want nil", tt.a, tt.b, err)
			}

			if got != tt.want {
				t.Errorf("Contains(%s, %s) = %t, want %t", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestRepo_Contains_WhenCommitUnknown_ReturnsExitError(t *testing.T) {
	t.Parallel()
	shas := diverged(t)

	got, err := shas.repo.Contains(t.Context(), shas.local, unknownSHA)

	if _, ok := errors.AsType[*git.ExitError](err); !ok {
		t.Errorf("Contains(%s, %s) = %t, error = %v, want *git.ExitError", shas.local, unknownSHA, got, err)
	}
}

func TestRepo_CommitsPast_WhenTrunkMergedIn_ListsOwnCommitsOldestFirst(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	shas := f.Commits(topic, "one", "two")
	f.AdvanceOrigin(trunk)
	f.Git("fetch", "--quiet", "origin", trunk)
	f.Git("merge", "--quiet", "--no-edit", "origin/"+trunk)
	tip := f.Git("rev-parse", "HEAD")

	got, err := newRepo(f).CommitsPast(t.Context(), tip, trunk)
	if err != nil {
		t.Fatalf("CommitsPast(%s, %q) error = %v, want nil", tip, trunk, err)
	}

	want := []git.Commit{{SHA: shas[0], Subject: "one"}, {SHA: shas[1], Subject: "two"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("CommitsPast(%s, %q) mismatch (-want +got):\n%s", tip, trunk, diff)
	}
}

func TestRepo_CommitsPast_WhenNothingPastTrunk_ReturnsNoCommits(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	tip := f.Git("rev-parse", "HEAD")

	got, err := newRepo(f).CommitsPast(t.Context(), tip, trunk)
	if err != nil {
		t.Fatalf("CommitsPast(%s, %q) error = %v, want nil", tip, trunk, err)
	}

	if len(got) != 0 {
		t.Errorf("CommitsPast(%s, %q) = %v, want no commits", tip, trunk, got)
	}
}

func TestRepo_BranchesContaining_WhenAnotherBranchCarriesCommit_ListsOnlyTheOtherBranch(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	first := f.Commits(topic, "one", "two")[0]
	f.Sibling("team/other", topic)
	f.Git("switch", "--quiet", topic)

	got, err := newRepo(f).BranchesContaining(t.Context(), first)
	if err != nil {
		t.Fatalf("BranchesContaining(%s) error = %v, want nil", first, err)
	}

	if diff := cmp.Diff([]string{"team/other"}, got); diff != "" {
		t.Errorf("BranchesContaining(%s) mismatch (-want +got):\n%s", first, diff)
	}
}

func TestRepo_BranchesContaining_WhenHeadDetached_ListsEveryBranchCarryingCommit(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	first := f.Commits(topic, "one", "two")[0]
	f.Sibling("team/other", topic)
	f.Git("switch", "--quiet", topic)
	f.Detach()

	got, err := newRepo(f).BranchesContaining(t.Context(), first)
	if err != nil {
		t.Fatalf("BranchesContaining(%s) error = %v, want nil", first, err)
	}

	if diff := cmp.Diff([]string{"team/other", topic}, got); diff != "" {
		t.Errorf("BranchesContaining(%s) mismatch (-want +got):\n%s", first, diff)
	}
}

func TestRepo_Unlanded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arrange func(f *gittest.Fixture) []git.Commit
		name    string
	}{
		{name: "tip is the merged head", arrange: func(*gittest.Fixture) []git.Commit { return nil }},
		{
			name: "message of a landed commit amended",
			arrange: func(f *gittest.Fixture) []git.Commit {
				f.Git("commit", "--quiet", "--amend", "--message", "two reworded")

				return nil
			},
		},
		{
			name: "one commit after the merged head",
			arrange: func(f *gittest.Fixture) []git.Commit {
				return []git.Commit{{SHA: f.Commits(topic, "three")[0], Subject: "three"}}
			},
		},
		{name: "trunk merged into the tip", arrange: func(f *gittest.Fixture) []git.Commit { mergeTrunk(f); return nil }},
		{
			name: "one commit after merging the trunk in",
			arrange: func(f *gittest.Fixture) []git.Commit {
				mergeTrunk(f)

				return []git.Commit{{SHA: f.Commits(topic, "three")[0], Subject: "three"}}
			},
		},
		{name: "side branch merged into the tip", arrange: mergeSide},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, merged := landed(t)
			want := tt.arrange(f)
			tip := f.Git("rev-parse", "HEAD")

			got, err := newRepo(f).Unlanded(t.Context(), merged, tip, trunk)
			if err != nil {
				t.Fatalf("Unlanded(%s, %s, %q) error = %v, want nil", merged, tip, trunk, err)
			}

			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unlanded(%s, %s, %q) mismatch (-want +got):\n%s", merged, tip, trunk, diff)
			}
		})
	}
}

func TestRepo_Unlanded_WhenMergedHeadHasCommitTheTipLacks_ListsNothing(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	tip := f.Commits(topic, "one", "two")[1]
	f.Push(topic)
	f.OriginGit("update-ref", pullRef, f.AdvanceOrigin(topic))
	f.Git("fetch", "--quiet", "origin", pullRef)
	merged := f.Git("rev-parse", "FETCH_HEAD")
	f.SquashOnOrigin(merged, "topic (#7)")
	f.RemoveFromOrigin(topic)
	f.Git("fetch", "--quiet", "origin", trunk)

	got, err := newRepo(f).Unlanded(t.Context(), merged, tip, trunk)
	if err != nil {
		t.Fatalf("Unlanded(%s, %s, %q) error = %v, want nil", merged, tip, trunk, err)
	}

	if len(got) != 0 {
		t.Errorf("Unlanded(%s, %s, %q) = %v, want no commits", merged, tip, trunk, got)
	}
}

func TestRepo_Push_WhenBranchIsNew_PushesNamedBranchWithoutForce(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	tip := f.Commits(topic, "one")[0]
	recorder := &git.Recorder{}
	repo := git.Repo{Runner: git.Runner{Dir: f.Dir(), Env: f.Env(), Recorder: recorder}}

	err := repo.Push(t.Context(), topic)
	if err != nil {
		t.Fatalf("Push(%q) error = %v, want nil", topic, err)
	}

	want := [][]string{{"git", "push", "-u", "origin", topic}}
	if diff := cmp.Diff(want, recorder.Calls()); diff != "" {
		t.Errorf("Push(%q) recorded calls mismatch (-want +got):\n%s", topic, diff)
	}
	if got := f.OriginGit("rev-parse", "refs/heads/"+topic); got != tip {
		t.Errorf("origin's %s after Push = %q, want %q", topic, got, tip)
	}
}

func TestRepo_Push_WhenOriginHasCommitsTheBranchLacks_ReturnsPushErrorAndLeavesOrigin(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.Push(topic)
	_, remote := f.Diverge(topic)

	err := newRepo(f).Push(t.Context(), topic)

	pushErr, ok := errors.AsType[*git.PushError](err)
	if !ok {
		t.Fatalf("Push(%q) error = %v, want *git.PushError", topic, err)
	}
	if !strings.Contains(pushErr.Stderr, "rejected") {
		t.Errorf("Push(%q) error stderr = %q, want git's rejection", topic, pushErr.Stderr)
	}
	if pushErr.Branch != topic {
		t.Errorf("Push(%q) error names branch %q, want %q", topic, pushErr.Branch, topic)
	}
	if want := "push of " + topic + " refused: " + strings.TrimSpace(pushErr.Stderr); err.Error() != want {
		t.Errorf("Push(%q) error text = %q, want %q", topic, err.Error(), want)
	}
	if got := f.OriginGit("rev-parse", "refs/heads/"+topic); got != remote {
		t.Errorf("origin's %s after rejected Push = %q, want %q", topic, got, remote)
	}
}

func TestRepo_WhenContextCanceled_ReturnsContextError(t *testing.T) {
	t.Parallel()
	repo := newRepo(gittest.New(t, trunk))
	tests := []struct {
		call func(ctx context.Context) error
		name string
	}{
		{name: "Standing", call: func(ctx context.Context) error { _, err := repo.Standing(ctx); return err }},
		{name: "HeadSHA", call: func(ctx context.Context) error { _, err := repo.HeadSHA(ctx); return err }},
		{name: "GitDir", call: func(ctx context.Context) error { _, err := repo.GitDir(ctx); return err }},
		{name: "DirtyFiles", call: func(ctx context.Context) error { _, err := repo.DirtyFiles(ctx); return err }},
		{name: "OriginURLs", call: func(ctx context.Context) error { _, err := repo.OriginURLs(ctx); return err }},
		{name: "Trunk", call: func(ctx context.Context) error { _, err := repo.Trunk(ctx); return err }},
		{name: "RemoteBranch", call: func(ctx context.Context) error { _, _, err := repo.RemoteBranch(ctx, topic); return err }},
		{name: "Fetch", call: func(ctx context.Context) error { _, err := repo.Fetch(ctx, trunk); return err }},
		{name: "Divergence", call: func(ctx context.Context) error { _, err := repo.Divergence(ctx, unknownSHA, unknownSHA); return err }},
		{name: "Contains", call: func(ctx context.Context) error { _, err := repo.Contains(ctx, unknownSHA, unknownSHA); return err }},
		{name: "CommitsPast", call: func(ctx context.Context) error { _, err := repo.CommitsPast(ctx, unknownSHA, trunk); return err }},
		{name: "BranchesContaining", call: func(ctx context.Context) error { _, err := repo.BranchesContaining(ctx, unknownSHA); return err }},
		{name: "Unlanded", call: func(ctx context.Context) error {
			_, err := repo.Unlanded(ctx, unknownSHA, unknownSHA, trunk)
			return err
		}},
		{name: "Push", call: func(ctx context.Context) error { return repo.Push(ctx, topic) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			err := tt.call(ctx)

			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s() error = %v, want context.Canceled", tt.name, err)
			}
		})
	}
}
