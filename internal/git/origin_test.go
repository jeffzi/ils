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

func TestParseOrigin(t *testing.T) {
	t.Parallel()
	hub := git.Origin{Host: "github.com", Owner: "jeffzi", Name: "ils"}
	other := git.Origin{Host: "example.com", Owner: "jeffzi", Name: "ils"}
	tests := []struct {
		name string
		in   string
		want git.Origin
	}{
		{name: "https", in: "https://github.com/jeffzi/ils", want: hub},
		{name: "https with .git", in: "https://github.com/jeffzi/ils.git", want: hub},
		{name: "https with trailing slash", in: "https://github.com/jeffzi/ils/", want: hub},
		{name: "https with .git and slash", in: "https://github.com/jeffzi/ils.git/", want: hub},
		{name: "https with port", in: "https://example.com:8443/jeffzi/ils.git", want: other},
		{name: "ssh url", in: "ssh://git@github.com/jeffzi/ils", want: hub},
		{name: "ssh url with .git", in: "ssh://git@github.com/jeffzi/ils.git", want: hub},
		{name: "ssh url with trailing slash", in: "ssh://git@github.com/jeffzi/ils/", want: hub},
		{name: "ssh url with .git and slash", in: "ssh://git@github.com/jeffzi/ils.git/", want: hub},
		{name: "ssh url with port", in: "ssh://git@example.com:2222/jeffzi/ils.git", want: other},
		{name: "short ssh", in: "git@github.com:jeffzi/ils", want: hub},
		{name: "short ssh with .git", in: "git@github.com:jeffzi/ils.git", want: hub},
		{name: "short ssh with trailing slash", in: "git@github.com:jeffzi/ils/", want: hub},
		{name: "short ssh with .git and slash", in: "git@github.com:jeffzi/ils.git/", want: hub},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := git.ParseOrigin(tt.in)
			assert.NilError(t, err, "ParseOrigin(%q)", tt.in)

			assert.Check(t, is.DeepEqual(got, tt.want), "ParseOrigin(%q)", tt.in)
		})
	}
}

func TestParseOrigin_WhenShapeUnknown_ReturnsOriginErrorNamingURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "absolute path", in: "/home/user/project"},
		{name: "relative path", in: "../project"},
		{name: "windows path", in: `C:\work\project`},
		{name: "file url", in: "file:///home/user/project.git"},
		{name: "https with no owner", in: "https://github.com/ils"},
		{name: "https with empty owner", in: "https://github.com//ils"},
		{name: "https with host only", in: "https://github.com/"},
		{name: "https with three segments", in: "https://github.com/jeffzi/ils/tree"},
		{name: "https with empty host", in: "https:///jeffzi/ils"},
		{name: "https with bad percent escape", in: "https://github.com/jeffzi/%zz"},
		{name: "https with control character", in: "https://github.com/jeffzi/ils\x7f"},
		{name: "ssh url with no owner", in: "ssh://git@github.com/ils.git"},
		{name: "short ssh with no owner", in: "git@github.com:ils.git"},
		{name: "short ssh with empty name", in: "git@github.com:jeffzi/"},
		{name: "short ssh with slash in host", in: "git@github.com/team:jeffzi/ils"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := git.ParseOrigin(tt.in)

			originErr, ok := errors.AsType[*git.OriginError](err)
			assert.Assert(t, ok, "ParseOrigin(%q) error = %v, want *git.OriginError", tt.in, err)
			assert.Check(t, is.Equal(originErr.URL, tt.in))
		})
	}
}

func TestOriginError_Error_WhenURLRejected_ListsAcceptedForms(t *testing.T) {
	t.Parallel()
	err := &git.OriginError{URL: localPath}

	got := err.Error()

	want := `remote URL "/srv/project" is not https://<host>/<owner>/<name>, ` +
		"ssh://git@<host>/<owner>/<name>, or git@<host>:<owner>/<name>"
	assert.Check(t, is.Equal(got, want))
}

func TestOrigin_Same(t *testing.T) {
	t.Parallel()
	base := git.Origin{Host: "github.com", Owner: "jeffzi", Name: "ils"}
	tests := []struct {
		name  string
		other git.Origin
		want  bool
	}{
		{name: "identical", other: base, want: true},
		{name: "host case differs", other: git.Origin{Host: "GitHub.com", Owner: "jeffzi", Name: "ils"}, want: true},
		{name: "owner case differs", other: git.Origin{Host: "github.com", Owner: "JeffZi", Name: "ils"}, want: true},
		{name: "name case differs", other: git.Origin{Host: "github.com", Owner: "jeffzi", Name: "ILS"}, want: true},
		{name: "host differs", other: git.Origin{Host: "example.com", Owner: "jeffzi", Name: "ils"}, want: false},
		{name: "owner differs", other: git.Origin{Host: "github.com", Owner: "someone", Name: "ils"}, want: false},
		{name: "name differs", other: git.Origin{Host: "github.com", Owner: "jeffzi", Name: "tools"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := base.Same(tt.other)

			assert.Check(t, is.Equal(got, tt.want), "%+v.Same(%+v)", base, tt.other)
		})
	}
}

func FuzzParseOrigin(f *testing.F) {
	f.Add("https://github.com/jeffzi/ils.git")
	f.Add("ssh://git@example.com:2222/jeffzi/ils")
	f.Add("git@github.com:jeffzi/ils.git/")
	f.Add("/home/user/project")
	f.Fuzz(func(t *testing.T, in string) {
		got, err := git.ParseOrigin(in)
		if err != nil {
			return
		}

		assert.Check(t, got.Host != "", "ParseOrigin(%q) = %+v with nil error", in, got)
		assert.Check(t, got.Owner != "", "ParseOrigin(%q) = %+v with nil error", in, got)
		assert.Check(t, got.Name != "", "ParseOrigin(%q) = %+v with nil error", in, got)
	})
}

const (
	otherPath = "../elsewhere"
	httpsURL  = "https://github.com/jeffzi/ils.git"
	sshURL    = "git@github.com:jeffzi/tools.git"
	localPath = "/srv/project"
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

func TestRepo_OriginURLs_WhenFetchAndPushDiffer_ReturnsEachSideParsedWithRawURL(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(httpsURL)
	f.SetPushURL(sshURL)

	got, err := newRepo(f).OriginURLs(t.Context())
	assert.NilError(t, err)

	want := git.OriginURLs{
		Fetch:    git.Origin{Host: "github.com", Owner: "jeffzi", Name: "ils"},
		Push:     git.Origin{Host: "github.com", Owner: "jeffzi", Name: "tools"},
		FetchURL: httpsURL,
		PushURL:  sshURL,
	}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestRepo_OriginURLs_WhenURLDoesNotParse_ReturnsOriginErrorWithWhatParsed(t *testing.T) {
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
			assert.Assert(t, ok, "OriginURLs() error = %v, want *git.OriginError", err)
			assert.Check(t, is.Equal(originErr.URL, tt.named))
			assert.Check(t, is.DeepEqual(got, tt.want))
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
			assert.NilError(t, err)

			assert.Check(t, is.Equal(got, tt.trunk))
		})
	}
}

func TestRepo_OriginMethods_WhenOriginDoesNotAnswer_ReturnsRemoteError(t *testing.T) {
	t.Parallel()
	tests := []repoCall{
		{name: "Trunk", call: func(ctx context.Context, repo git.Repo) error { _, err := repo.Trunk(ctx); return err }},
		{name: "RemoteBranch", call: func(ctx context.Context, repo git.Repo) error {
			_, _, err := repo.RemoteBranch(ctx, trunk)

			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			unreachable(t, f)

			err := tt.call(t.Context(), newRepo(f))

			_, ok := errors.AsType[*git.RemoteError](err)
			assert.Check(t, ok, "%s() error = %v, want *git.RemoteError", tt.name, err)
		})
	}
}

func TestRepo_OriginMethods_WhenGitMissingFromPath_ReturnsToolMissingError(t *testing.T) {
	tests := []repoCall{
		{name: "Trunk", call: func(ctx context.Context, repo git.Repo) error { _, err := repo.Trunk(ctx); return err }},
		{name: "RemoteBranch", call: func(ctx context.Context, repo git.Repo) error {
			_, _, err := repo.RemoteBranch(ctx, trunk)

			return err
		}},
		{name: "Fetch", call: func(ctx context.Context, repo git.Repo) error { _, err := repo.Fetch(ctx, trunk); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRepo(gittest.New(t, trunk))
			// Not parallel: this empties PATH for the whole process while it runs.
			t.Setenv("PATH", t.TempDir())

			err := tt.call(t.Context(), repo)

			missing, ok := errors.AsType[*child.ToolMissingError](err)
			assert.Assert(t, ok, "%s() error = %v, want *child.ToolMissingError", tt.name, err)
			assert.Check(t, is.Equal(missing.Tool, "git"))
			_, remote := errors.AsType[*git.RemoteError](err)
			assert.Check(t, !remote, "%s() error = %v, want it not to read as origin not answering", tt.name, err)
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
		{name: "OriginURLs", call: func(ctx context.Context, repo git.Repo) error { _, err := repo.OriginURLs(ctx); return err }},
		{name: "Push", call: func(ctx context.Context, repo git.Repo) error { return repo.Push(ctx, topic) }},
	})
}
