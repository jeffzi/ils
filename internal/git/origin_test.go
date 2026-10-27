package git_test

import (
	"context"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
		{name: "https with .git and slash", in: "https://github.com/jeffzi/ils.git/", want: hub},
		{name: "https with port", in: "https://example.com:8443/jeffzi/ils.git", want: other},
		{name: "ssh url", in: "ssh://git@github.com/jeffzi/ils", want: hub},
		{name: "short ssh", in: "git@github.com:jeffzi/ils", want: hub},
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
		{name: "https with no owner", in: "https://github.com/ils"},
		{name: "https with empty owner", in: "https://github.com//ils"},
		{name: "https with three segments", in: "https://github.com/jeffzi/ils/tree"},
		{name: "https with empty host", in: "https:///jeffzi/ils"},
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

func TestParseOrigin_WhenURLFailsToParse_ReturnsOriginErrorWrappingTheCause(t *testing.T) {
	t.Parallel()
	in := "https://github.com/a/b%zz"

	_, err := git.ParseOrigin(in)

	originErr, ok := errors.AsType[*git.OriginError](err)
	assert.Assert(t, ok, "ParseOrigin(%q) error = %v, want *git.OriginError", in, err)
	_, ok = errors.AsType[*url.Error](originErr)
	assert.Check(t, ok, "ParseOrigin(%q) error = %v, want it to wrap *url.Error", in, err)
}

func TestOriginError_Error_WhenURLRejected_NamesTheURL(t *testing.T) {
	t.Parallel()
	err := &git.OriginError{URL: localPath}

	got := err.Error()

	assert.Check(t, is.Contains(got, strconv.Quote(localPath)))
}

func TestOriginError_Error_WhenCauseSet_HoldsTheCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("invalid URL escape")
	err := &git.OriginError{URL: localPath, Err: cause}

	got := err.Error()

	assert.Check(t, is.Contains(got, cause.Error()))
}

func TestOrigin_Same(t *testing.T) {
	t.Parallel()
	base := git.Origin{Host: "github.com", Owner: "jeffzi", Name: "ils"}
	tests := []struct {
		name  string
		other git.Origin
		want  bool
	}{
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
	httpsURL  = "https://github.com/jeffzi/ils.git"
	sshURL    = "git@github.com:jeffzi/tools.git"
	localPath = "/srv/project"
)

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

func TestRepo_OriginURLs_WhenSSHHostStartsWithDash_RefusesWithOriginError(t *testing.T) {
	// An ssh command that ran would leave the marker.
	t.Parallel()
	const scpLike = "git@-example.com:acme/widgets.git"
	f := gittest.New(t, trunk)
	f.SetOriginURL(scpLike)
	marker := filepath.Join(t.TempDir(), "ran")
	repo := plainSSH(t, newRepo(f), standInRefuse)
	repo.Runner.Env = append(append(repo.Runner.Env, variantEnv("ssh")...), "GIT_SSH_COMMAND="+gittest.Marking(marker))

	_, err := repo.OriginURLs(t.Context())

	originErr, ok := errors.AsType[*git.OriginError](err)
	assert.Assert(t, ok, "OriginURLs() of %q error = %v, want *git.OriginError", scpLike, err)
	assert.Check(t, is.Equal(originErr.URL, scpLike))
	_, statErr := os.Stat(marker)
	assert.Check(t, is.ErrorIs(statErr, fs.ErrNotExist), "OriginURLs() of %q ran the ssh command", scpLike)
}

func TestRepo_OriginURLs_WhenURLIsHTTPS_RunsNoSSHCommand(t *testing.T) {
	// A plain ssh that ran would refuse, failing the read.
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(httpsURL)
	repo := plainSSH(t, newRepo(f), standInRefuse)

	_, err := repo.OriginURLs(t.Context())

	assert.NilError(t, err)
}

func TestRepo_OriginURLs_WhenURLDoesNotParse_ReturnsOriginErrorWithWhatParsed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want git.OriginURLs
	}{
		{
			name: "fetch url",
			want: git.OriginURLs{
				Push:     git.Origin{Host: "github.com", Owner: "jeffzi", Name: "tools"},
				FetchURL: localPath,
				PushURL:  sshURL,
			},
		},
		{
			name: "push url",
			want: git.OriginURLs{
				Fetch:    git.Origin{Host: "github.com", Owner: "jeffzi", Name: "ils"},
				FetchURL: httpsURL,
				PushURL:  localPath,
			},
		},
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
			assert.Check(t, is.Equal(originErr.URL, localPath))
			assert.Check(t, is.DeepEqual(got, tt.want))
		})
	}
}

func TestRepo_FastForwardTrunk_WhenTrunkIsBehindOrigin_MovesItToOriginTip(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	want := f.AdvanceOrigin(trunk)

	err := newRepo(f).FastForwardTrunk(t.Context(), trunk)
	assert.NilError(t, err, "FastForwardTrunk(%q)", trunk)

	assert.Check(t, is.Equal(f.Git("rev-parse", trunk), want))
}

func TestRepo_FastForwardTrunk_WhenTrunkHasCommitsOriginLacks_RefusesWithExitError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Diverge(trunk)

	err := newRepo(f).FastForwardTrunk(t.Context(), trunk)

	_, remote := errors.AsType[*git.RemoteError](err)
	assert.Check(t, !remote, "FastForwardTrunk(%q) error = %v, want git's refusal, not *git.RemoteError", trunk, err)
	_, refused := errors.AsType[*child.ExitError](err)
	assert.Check(t, refused, "FastForwardTrunk(%q) error = %v, want *child.ExitError", trunk, err)
}

func TestRepo_FastForwardTrunk_WhenHeadIsOnAnotherBranch_RefusesWithoutMovingTheCheckedOutBranch(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.AdvanceOrigin(trunk)
	f.Git("switch", "--quiet", "--create", topic)
	before := f.Git("rev-parse", topic)

	err := newRepo(f).FastForwardTrunk(t.Context(), trunk)

	assert.Check(t, is.ErrorContains(err, trunk))
	assert.Check(t, is.Equal(f.Git("rev-parse", topic), before))
}

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
	const trunk = "release/next"
	f := gittest.New(t, trunk)

	got, err := newRepo(f).Trunk(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got, trunk))
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

func TestRepo_WhenOriginDoesNotAnswer_ReturnsRemoteError(t *testing.T) {
	t.Parallel()
	tests := append(originCalls(), repoCall{
		name: "FastForwardTrunk", call: func(ctx context.Context, repo git.Repo) (any, error) {
			return nil, repo.FastForwardTrunk(ctx, trunk)
		},
	})
	for _, tt := range tests {
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

func TestRepo_Trunk_WhenGitMissingFromPath_ReturnsToolMissingError(t *testing.T) {
	// Not parallel: this empties PATH for the whole process while it runs. RemoteBranch hands a
	// missing git to the same remoteFailure as Trunk does, so Trunk alone stands for both.
	repo := newRepo(gittest.New(t, trunk))
	t.Setenv("PATH", t.TempDir())

	_, err := repo.Trunk(t.Context())

	_, ok := errors.AsType[*child.ToolMissingError](err)
	assert.Assert(t, ok, "Trunk() error = %v, want *child.ToolMissingError", err)
	_, remote := errors.AsType[*git.RemoteError](err)
	assert.Check(t, !remote, "Trunk() error = %v, want it not to read as origin not answering", err)
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

func TestRemoteError_Error_WhenCauseSet_HoldsTheCause(t *testing.T) {
	t.Parallel()
	err := &git.RemoteError{Err: errors.New("exit status 128")}

	got := err.Error()

	assert.Check(t, is.Contains(got, "exit status 128"))
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

			_, err := newRepo(f).Trunk(t.Context())

			assert.Check(t, is.ErrorIs(err, git.ErrNoSymref))
		})
	}
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
	tests := []struct {
		arrange func(f *gittest.Fixture)
		name    string
	}{
		{name: "never pushed", arrange: func(*gittest.Fixture) {}},
		{
			name: "only a ref outside the branches ending the same",
			arrange: func(f *gittest.Fixture) {
				f.OriginGit("update-ref", "refs/odd/refs/heads/"+topic, f.OriginGit("rev-parse", "HEAD"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.Commits(topic, "one")
			tt.arrange(f)

			got, found, err := newRepo(f).RemoteBranch(t.Context(), topic)
			assert.NilError(t, err)

			assert.Check(t, !found, "RemoteBranch(%q) = %q, %t, want absent", topic, got, found)
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

// killsReceivePack kills the hook's parent: git runs a hook as a child of receive-pack, so `$PPID`
// is the process that must die mid-push.
const killsReceivePack = "kill -KILL $PPID"

// originTip returns the sha origin holds for branch, or nothing when origin lacks it.
func originTip(f *gittest.Fixture, branch string) string {
	return f.OriginGit("for-each-ref", "--format=%(objectname)", "refs/heads/"+branch)
}

func TestRepo_Push_WhenOriginReportsTheRefUpdated_PublishesTheBranch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		// arrange changes origin before the push; nil leaves it as built.
		arrange func(t *testing.T, f *gittest.Fixture)
		name    string
	}{
		{name: "origin answers to the end"},
		{
			name: "origin hangs up after its report",
			arrange: func(t *testing.T, f *gittest.Fixture) {
				t.Helper()
				gittest.NeedSignals(t)
				f.OriginHook("post-receive", killsReceivePack)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			tip := f.Commits(topic, "one")[0]
			if tt.arrange != nil {
				tt.arrange(t, f)
			}

			err := newRepo(f).Push(t.Context(), topic)
			assert.NilError(t, err)

			assert.Check(t, is.Equal(originTip(f, topic), tip))
		})
	}
}

func TestRepo_Push_WhenTagSharesTheBranchName_PublishesTheBranch(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	tip := f.Commits(topic, "one")[0]
	// Origin and the clone each hold a tag of the branch's name, on different tag objects, so a
	// push that names the source or the destination loosely fails.
	trunkTip := f.Git("rev-parse", trunk)
	f.OriginGit("tag", topic, trunkTip)
	f.Git("tag", "-a", "-m", "release", topic, trunk)

	err := newRepo(f).Push(t.Context(), topic)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(f.OriginGit("rev-parse", "refs/heads/"+topic), tip))
}

func TestRepo_Push_WhenOriginRefusesTheRef_ReturnsPushErrorNamingTheReason(t *testing.T) {
	t.Parallel()
	const said = "policy forbids this push"
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.Push(topic)
	f.OriginHook("pre-receive", "echo '"+said+"'\nexit 1")
	f.Commits(topic, "two")

	err := newRepo(f).Push(t.Context(), topic)

	pushErr, ok := errors.AsType[*git.PushError](err)
	assert.Assert(t, ok, "Push(%q) error = %v, want *git.PushError", topic, err)
	assert.Check(t, is.Equal(pushErr.Branch, topic))
	assert.Check(t, is.Contains(pushErr.Reason, "pre-receive hook declined"))
	assert.Check(t, is.Contains(pushErr.Stderr, said))
}

func TestRepo_Push_WhenThePrePushHookRefuses_ReturnsPushErrorHoldingWhatTheHookSaid(t *testing.T) {
	t.Parallel()
	const said = "policy forbids this push"
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.Hook("pre-push", "echo '"+said+"' >&2\nexit 1")

	err := newRepo(f).Push(t.Context(), topic)

	pushErr, ok := errors.AsType[*git.PushError](err)
	assert.Assert(t, ok, "Push(%q) error = %v, want *git.PushError", topic, err)
	assert.Check(t, strings.HasPrefix(pushErr.Detail(), said), "Detail() = %q, want it to start with %q",
		pushErr.Detail(), said)
}

func TestRepo_Push_WhenOriginNeverAnswers_ReturnsRemoteError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	unreachable(t, f)

	err := newRepo(f).Push(t.Context(), topic)

	_, remote := errors.AsType[*git.RemoteError](err)
	assert.Check(t, remote, "Push(%q) error = %v, want *git.RemoteError", topic, err)
}

func TestRepo_Push_WhenContextCanceled_ReturnsNeitherPushNorRemoteError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := newRepo(f).Push(ctx, topic)

	assert.Check(t, is.ErrorIs(err, context.Canceled))
	_, refused := errors.AsType[*git.PushError](err)
	assert.Check(t, !refused, "Push(%q) error = %v, want no *git.PushError", topic, err)
	_, remote := errors.AsType[*git.RemoteError](err)
	assert.Check(t, !remote, "Push(%q) error = %v, want no *git.RemoteError", topic, err)
}

func TestRepo_OriginURLsAndFastForwardTrunk_WhenNotARepository_ReturnsExitError(t *testing.T) {
	t.Parallel()
	tests := []repoCall{
		{name: "OriginURLs", call: func(ctx context.Context, repo git.Repo) (any, error) { return repo.OriginURLs(ctx) }},
		{name: "FastForwardTrunk", call: func(ctx context.Context, repo git.Repo) (any, error) {
			return nil, repo.FastForwardTrunk(ctx, trunk)
		}},
	}

	checkExitErrorOutsideRepo(t, tests)
}
