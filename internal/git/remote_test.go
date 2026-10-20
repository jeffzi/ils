package git_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
)

// unreachable points the fixture's origin at a location that holds no repository.
func unreachable(t *testing.T, f *gittest.Fixture) {
	t.Helper()

	f.SetOriginURL(filepath.ToSlash(f.MissingOriginDir()))
}

// decoyHead gives origin a symbolic ref outside its own HEAD whose name ends in HEAD and that names
// a branch called decoy. The test fails here unless origin lists that ref when asked for HEAD.
func decoyHead(t *testing.T, f *gittest.Fixture) {
	t.Helper()

	const name, target = "refs/remotes/upstream/HEAD", "refs/heads/decoy"

	f.OriginGit("update-ref", target, f.OriginGit("rev-parse", "HEAD"))
	f.OriginGit("symbolic-ref", name, target)

	listed := f.Git("ls-remote", "--symref", "origin", "HEAD")
	line := "ref: " + target + "\t" + name
	assert.Assert(t, is.Contains(listed, line))
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
			assert.NilError(t, err)

			assert.Check(t, is.Equal(got, tt.trunk))
		})
	}
}

// remoteBranch is what RemoteBranch returns besides its error.
type remoteBranch struct {
	sha   string
	found bool
}

// originCalls returns the repository calls that read from origin without changing the repository.
func originCalls() []repoCall {
	return []repoCall{
		{name: "Trunk", call: func(ctx context.Context, repo git.Repo) (any, error) { return repo.Trunk(ctx) }},
		{name: "RemoteBranch", call: func(ctx context.Context, repo git.Repo) (any, error) {
			sha, found, err := repo.RemoteBranch(ctx, trunk)

			return remoteBranch{sha, found}, err
		}},
	}
}

func TestRepo_OriginMethods_WhenOriginDoesNotAnswer_ReturnsRemoteError(t *testing.T) {
	t.Parallel()
	for _, tt := range originCalls() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			unreachable(t, f)

			got, err := tt.call(t.Context(), newRepo(f))

			_, ok := errors.AsType[*git.RemoteError](err)
			assert.Check(t, ok, "%s() error = %v, want *git.RemoteError", tt.name, err)
			checkNoResult(t, tt.name, got)
		})
	}
}

func TestRepo_OriginMethods_WhenGitMissingFromPath_ReturnsToolMissingError(t *testing.T) {
	tests := append(originCalls(), repoCall{
		name: "Fetch", call: func(ctx context.Context, repo git.Repo) (any, error) { return repo.Fetch(ctx, trunk) },
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRepo(gittest.New(t, trunk))
			// Not parallel: this empties PATH for the whole process while it runs.
			t.Setenv("PATH", t.TempDir())

			got, err := tt.call(t.Context(), repo)

			missing, ok := errors.AsType[*child.ToolMissingError](err)
			assert.Assert(t, ok, "%s() error = %v, want *child.ToolMissingError", tt.name, err)
			assert.Check(t, is.Equal(missing.Tool, "git"))
			_, remote := errors.AsType[*git.RemoteError](err)
			assert.Check(t, !remote, "%s() error = %v, want it not to read as origin not answering", tt.name, err)
			checkNoResult(t, tt.name, got)
		})
	}
}

func TestRepo_Trunk_WhenContextEnded_ReturnsRemoteError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got, err := newRepo(f).Trunk(ctx)

	_, ok := errors.AsType[*git.RemoteError](err)
	assert.Check(t, ok, "Trunk() = %q, error = %v, want *git.RemoteError", got, err)
	assert.Check(t, is.ErrorIs(err, context.Canceled))
}

func TestRemoteError_Error_WhenCauseSet_PrefixesCauseWithOriginUnreachable(t *testing.T) {
	t.Parallel()
	err := &git.RemoteError{Err: errors.New("exit status 128")}

	got := err.Error()

	assert.Check(t, is.Equal(got, "origin could not be reached: exit status 128"))
}

func TestRepo_Trunk_WhenHeadNamesNoBranch_ReturnsErrNoSymref(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arrange func(t *testing.T, f *gittest.Fixture)
		name    string
	}{
		{name: "head is not symbolic", arrange: func(_ *testing.T, f *gittest.Fixture) { f.DetachOriginHead() }},
		{
			name: "only another head is symbolic",
			arrange: func(t *testing.T, f *gittest.Fixture) {
				t.Helper()
				f.DetachOriginHead()
				decoyHead(t, f)
			},
		},
		{
			name: "head points outside the branches",
			arrange: func(_ *testing.T, f *gittest.Fixture) {
				f.OriginGit("update-ref", "refs/odd/place", f.OriginGit("rev-parse", "HEAD"))
				f.OriginGit("symbolic-ref", "HEAD", "refs/odd/place")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			tt.arrange(t, f)

			got, err := newRepo(f).Trunk(t.Context())

			assert.Check(t, is.ErrorIs(err, git.ErrNoSymref))
			assert.Check(t, is.Equal(got, ""))
			want := "git ls-remote --symref origin HEAD: origin's HEAD names no branch"
			assert.Check(t, is.Error(err, want))
		})
	}
}

func TestRepo_Trunk_WhenOriginListsAnotherSymbolicHead_ReturnsOwnDefaultBranch(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	decoyHead(t, f)

	got, err := newRepo(f).Trunk(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got, trunk))
}

func TestRepo_RemoteBranch_WhenOnOrigin_ReturnsItsSHA(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	want := f.Commits(topic, "one")[0]
	f.Push(topic)

	got, found, err := newRepo(f).RemoteBranch(t.Context(), topic)
	assert.NilError(t, err)

	assert.Check(t, found, "RemoteBranch(%q) = %q, %t, want %q, true", topic, got, found, want)
	assert.Check(t, is.Equal(got, want))
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
			assert.NilError(t, err)

			assert.Check(t, !found, "RemoteBranch(%q) = %q, %t, want absent", asked, got, found)
			assert.Check(t, is.Equal(got, ""))
		})
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
				f.PullRef(pullNumber, sha)

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
			assert.NilError(t, err, "Fetch(%q)", tt.ref)

			assert.Check(t, is.Equal(got, want), "Fetch(%q)", tt.ref)
		})
	}
}

func TestRepo_Fetch_WhenTrunkMovedOnOrigin_MovesItsRemoteTrackingRef(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	want := f.AdvanceOrigin(trunk)

	_, err := newRepo(f).Fetch(t.Context(), trunk)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(f.Git("rev-parse", "origin/"+trunk), want))
}

func TestRepo_Fetch_WhenRefNotOnOrigin_ReturnsRemoteError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)

	got, err := newRepo(f).Fetch(t.Context(), pullRef)

	_, ok := errors.AsType[*git.RemoteError](err)
	assert.Check(t, ok, "Fetch(%q) = %q, error = %v, want *git.RemoteError", pullRef, got, err)
}

func TestRepo_Push_WhenBranchIsNew_PublishesItAsTheUpstream(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	tip := f.Commits(topic, "one")[0]

	err := newRepo(f).Push(t.Context(), topic)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(f.OriginGit("rev-parse", "refs/heads/"+topic), tip))
	assert.Check(t, is.Equal(f.Git("config", "branch."+topic+".remote"), "origin"))
	assert.Check(t, is.Equal(f.Git("config", "branch."+topic+".merge"), "refs/heads/"+topic))
}

func TestRepo_Push_WhenOriginHasCommitsTheBranchLacks_RefusesWithPushError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.Push(topic)
	_, remote := f.Diverge(topic)

	err := newRepo(f).Push(t.Context(), topic)

	pushErr, ok := errors.AsType[*git.PushError](err)
	assert.Assert(t, ok, "Push(%q) error = %v, want *git.PushError", topic, err)
	assert.Check(t, is.Contains(pushErr.Stderr, "rejected"))
	assert.Check(t, is.Equal(pushErr.Branch, topic))
	assert.Check(t, is.Equal(err.Error(), "push of "+topic+" refused: "+strings.TrimSpace(pushErr.Stderr)))
	assert.Check(t, is.Equal(f.OriginGit("rev-parse", "refs/heads/"+topic), remote))
}

func TestRepo_OriginMethods_WhenContextCanceled_ReturnsContextError(t *testing.T) {
	t.Parallel()

	checkCanceled(t, []repoCall{
		{name: "OriginURLs", call: func(ctx context.Context, repo git.Repo) (any, error) { return repo.OriginURLs(ctx) }},
		{name: "Push", call: func(ctx context.Context, repo git.Repo) (any, error) { return nil, repo.Push(ctx, topic) }},
	})
}
