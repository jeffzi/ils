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

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
)

func TestParseOrigin(t *testing.T) {
	t.Parallel()
	hub := ilsOrigin()
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
	base := ilsOrigin()
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

// ilsOrigin returns the origin httpsURL parses to.
func ilsOrigin() git.Origin { return git.Origin{Host: "github.com", Owner: "jeffzi", Name: "ils"} }

// toolsOrigin returns the origin sshURL parses to.
func toolsOrigin() git.Origin { return git.Origin{Host: "github.com", Owner: "jeffzi", Name: "tools"} }

func TestRepo_OriginURLs_WhenFetchAndPushDiffer_ReturnsEachSideParsedWithRawURL(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(httpsURL)
	f.SetPushURL(sshURL)

	got, err := newRepo(f).OriginURLs(t.Context())
	assert.NilError(t, err)

	want := git.OriginURLs{
		Fetch:    ilsOrigin(),
		Push:     toolsOrigin(),
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
				Push:     toolsOrigin(),
				FetchURL: localPath,
				PushURL:  sshURL,
			},
		},
		{
			name: "push url",
			want: git.OriginURLs{
				Fetch:    ilsOrigin(),
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

// originRefAtHead creates ref on origin at origin's HEAD.
func originRefAtHead(f *gittest.Fixture, ref string) {
	f.OriginGit("update-ref", ref, f.OriginGit("rev-parse", "HEAD"))
}

// decoyHead gives origin a symbolic ref outside its own HEAD whose name ends in HEAD and that names
// a branch called decoy. The test fails here unless origin lists that ref when asked for HEAD.
func decoyHead(t *testing.T, f *gittest.Fixture) {
	t.Helper()

	const name, target = "refs/remotes/upstream/HEAD", "refs/heads/decoy"

	originRefAtHead(f, target)
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
		{name: "RemoteBranch", call: func(ctx context.Context, repo git.Repo) (any, error) {
			sha, found, err := repo.RemoteBranch(ctx, trunk)

			return remoteBranch{sha, found}, err
		}},
		{name: "OriginTags", call: func(ctx context.Context, repo git.Repo) (any, error) { return repo.OriginTags(ctx) }},
	}
}

// fastForwardTrunkCall returns the call of FastForwardTrunk on the trunk.
func fastForwardTrunkCall() repoCall {
	return repoCall{name: "FastForwardTrunk", call: func(ctx context.Context, repo git.Repo) (any, error) {
		return nil, repo.FastForwardTrunk(ctx, trunk)
	}}
}

// takingCase is a ref created before BranchesTaking reads which branches take the name in.
type takingCase struct {
	arrange    func(f *gittest.Fixture)
	name       string
	in         string
	wantLocal  []string
	wantOrigin []string
}

// localBranch returns an arrangement that creates branch in the working repository alone.
func localBranch(branch string) func(f *gittest.Fixture) {
	return func(f *gittest.Fixture) { f.Git("branch", branch) }
}

// originBranch returns an arrangement that creates branch on origin at its trunk, without the working
// repository fetching it.
func originBranch(branch string) func(f *gittest.Fixture) {
	return func(f *gittest.Fixture) { f.OriginGit("branch", branch, trunk) }
}

// takingCases returns the branches that take a name, locally or on origin, and the name each takes.
func takingCases() []takingCase {
	return []takingCase{
		{name: "local branch of that name", in: "fix", arrange: localBranch("fix"), wantLocal: []string{"fix"}},
		{name: "local branch whose name is a path prefix", in: "fix/x/y", arrange: localBranch("fix"), wantLocal: []string{"fix"}},
		{name: "local branch under the name", in: "fix", arrange: localBranch("fix/x"), wantLocal: []string{"fix/x"}},
		{name: "origin branch pushed since the last fetch", in: "fix", arrange: originBranch("fix"), wantOrigin: []string{"fix"}},
	}
}

// freeCases returns the refs that leave a name free.
func freeCases() []takingCase {
	return []takingCase{
		{name: "local branch whose name only ends with the name", in: "fix", arrange: localBranch("team/fix")},
	}
}

func TestRepo_BranchesTaking(t *testing.T) {
	t.Parallel()
	for _, tt := range append(takingCases(), freeCases()...) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			tt.arrange(f)

			local, onOrigin, err := newRepo(f).BranchesTaking(t.Context(), tt.in)
			assert.NilError(t, err, "BranchesTaking(%q)", tt.in)

			assert.Check(t, is.DeepEqual(local, tt.wantLocal, cmpopts.EquateEmpty()), "BranchesTaking(%q) local", tt.in)
			assert.Check(t, is.DeepEqual(onOrigin, tt.wantOrigin, cmpopts.EquateEmpty()), "BranchesTaking(%q) on origin", tt.in)
		})
	}
}

func TestRepo_OriginTags_WhenLightweightAndAnnotatedTags_ListsEachWithTheCommitItTags(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	first := f.OriginGit("rev-parse", trunk)
	second := f.AdvanceOrigin(trunk)
	f.OriginGit("tag", "v1.0.0", first)
	f.OriginGit("tag", "--annotate", "--message", "release", "v1.1.0", second)
	want := []git.Tag{{Name: "v1.0.0", SHA: first}, {Name: "v1.1.0", SHA: second}}

	got, err := newRepo(f).OriginTags(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got, want))
}

func TestRepo_WhenOriginDoesNotAnswer_ReturnsRemoteError(t *testing.T) {
	t.Parallel()
	tests := append(originCalls(), fastForwardTrunkCall())
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
				originRefAtHead(f, "refs/odd/place")
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
				originRefAtHead(f, "refs/odd/refs/heads/"+topic)
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

// killsReceivePack kills the hook's parent: git runs a hook as a child of receive-pack, so `$PPID`
// is the process that must die mid-push.
const killsReceivePack = "kill -KILL $PPID"

// topicSpec is the pair of refs Push hands git for topic, which git's report names on its status line.
const topicSpec = "refs/heads/" + topic + ":refs/heads/" + topic

// otherSpec is the pair of refs for a branch Push never pushes, which a hook's report-shaped lines name.
const otherSpec = "refs/heads/other:refs/heads/other"

// originTip returns the sha origin holds for branch, or nothing when origin lacks it.
func originTip(f *gittest.Fixture, branch string) string {
	return f.OriginGit("for-each-ref", "--format=%(objectname)", "refs/heads/"+branch)
}

// originRefusing returns a fixture whose origin holds topic and whose pre-receive hook prints said and
// refuses the next push of it, which the clone's topic is one commit ahead of.
func originRefusing(t *testing.T, said string) *gittest.Fixture {
	t.Helper()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.Push(topic)
	f.OriginHook("pre-receive", "echo '"+said+"'\nexit 1")
	f.Commits(topic, "two")
	return f
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

func TestRepo_Push_WhenOriginRefusesATagThePushFollows_PublishesTheBranch(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	tip := f.Commits(topic, "one")[0]
	// push.followTags sends the annotated tag on the branch's tip with it, so git's report holds a
	// status line for the tag beside the branch's. The update hook fails for any ref under refs/tags/.
	f.Git("config", "push.followTags", "true")
	f.Git("tag", "-a", "-m", "release", "v1", tip)
	f.OriginHook("update", `test "${1#refs/tags/}" = "$1"`)

	err := newRepo(f).Push(t.Context(), topic)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(originTip(f, topic), tip))
}

func TestRepo_Push_WhenOriginRefusesTheRef_ReturnsPushErrorNamingTheReason(t *testing.T) {
	t.Parallel()
	const said = "policy forbids this push"
	f := originRefusing(t, said)

	err := newRepo(f).Push(t.Context(), topic)

	pushErr, ok := errors.AsType[*git.PushError](err)
	assert.Assert(t, ok, "Push(%q) error = %v, want *git.PushError", topic, err)
	assert.Check(t, is.Equal(pushErr.Ref, "refs/heads/"+topic))
	assert.Check(t, is.Contains(pushErr.Reason, "pre-receive hook declined"))
	assert.Check(t, !strings.Contains(pushErr.Reason, "refs/heads/"), "Reason = %q, want the summary, not the ref it names",
		pushErr.Reason)
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

func TestRepo_Push_WhenThePrePushHookRefusesAfterPrintingOnStdout_ReturnsPushErrorLeadingWithWhatTheHookPrinted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		script string
		// want starts Output(): git adds its own failure line on stderr after the hook's.
		want string
	}{
		{
			name:   "stdout ends in lines shaped like the report",
			script: "printf 'To elsewhere\\n*\\t" + otherSpec + "\\t[new branch]\\nDone\\n'\nexit 1",
			want:   "To elsewhere\n*\t" + otherSpec + "\t[new branch]\nDone\n",
		},
		{
			name:   "stdout holds a rejected status line for the pushed ref",
			script: "printf '!\\t" + topicSpec + "\\tblocked by policy\\n'\nexit 1",
			want:   "!\t" + topicSpec + "\tblocked by policy\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.Commits(topic, "one")
			f.Hook("pre-push", tt.script)

			err := newRepo(f).Push(t.Context(), topic)

			pushErr, ok := errors.AsType[*git.PushError](err)
			assert.Assert(t, ok, "Push(%q) error = %v, want *git.PushError", topic, err)
			assert.Check(t, is.Equal(pushErr.Reason, ""))
			assert.Check(t, strings.HasPrefix(pushErr.Output(), tt.want), "Output() = %q, want it to start with %q",
				pushErr.Output(), tt.want)
		})
	}
}

func TestRepo_Push_WhenOriginRefusesTheRef_KeepsOnlyThePrePushHookLinesInStdout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// script is the pre-push hook, which lets the push through.
		script string
		want   string
	}{
		{name: "pre-push hook prints lines", script: "echo checked\necho passed", want: "checked\npassed"},
		// git writes its report right after the hook's output, so the report's `To` line starts
		// on the hook's last line.
		{name: "pre-push hook ends without a newline", script: "printf checked", want: "checked"},
		{
			name:   "pre-push hook prints lines shaped like the report",
			script: "printf 'Done\\nTo elsewhere\\n*\\t" + otherSpec + "\\t[new branch]\\n'",
			want:   "Done\nTo elsewhere\n*\t" + otherSpec + "\t[new branch]",
		},
		{
			name:   "pre-push hook prints a new-branch status line for the pushed ref",
			script: "printf '*\\t" + topicSpec + "\\t[new branch]\\n'",
			want:   "*\t" + topicSpec + "\t[new branch]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := originRefusing(t, "policy forbids this push")
			f.Hook("pre-push", tt.script)

			err := newRepo(f).Push(t.Context(), topic)

			pushErr, ok := errors.AsType[*git.PushError](err)
			assert.Assert(t, ok, "Push(%q) error = %v, want *git.PushError", topic, err)
			assert.Check(t, is.Equal(strings.TrimSpace(pushErr.Stdout), tt.want))
		})
	}
}

func TestPushError_Output(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  git.PushError
		want string
	}{
		{name: "stdout before stderr", err: git.PushError{Stdout: "out\n", Stderr: "err\n"}, want: "out\nerr"},
		{name: "stdout only", err: git.PushError{Stdout: "out\n"}, want: "out"},
		{name: "stderr only", err: git.PushError{Stderr: "\nerr\n"}, want: "err"},
		{name: "blank streams", err: git.PushError{Stdout: "\n", Stderr: " \n"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.err.Output()

			assert.Check(t, is.Equal(got, tt.want))
		})
	}
}

func TestPushError_Detail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  git.PushError
		want string
	}{
		{
			name: "reason before output",
			err:  git.PushError{Reason: "pre-receive hook declined", Stdout: "out\n", Stderr: "err\n"},
			want: "pre-receive hook declined\nout\nerr",
		},
		{name: "reason only", err: git.PushError{Reason: "pre-receive hook declined"}, want: "pre-receive hook declined"},
		{name: "output only", err: git.PushError{Stdout: "out\n"}, want: "out"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.err.Detail()

			assert.Check(t, is.Equal(got, tt.want))
		})
	}
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

const releaseRef = "refs/tags/" + release

func originRefs(f *gittest.Fixture) string {
	return f.OriginGit("for-each-ref", "--format=%(refname)")
}

// taggedRelease returns a fixture whose clone holds release as a local tag on tip.
func taggedRelease(t *testing.T) (f *gittest.Fixture, repo git.Repo, tip string) {
	t.Helper()
	f = gittest.New(t, trunk)
	tip = f.Commits(topic, "one")[0]
	repo = newRepo(f)
	assert.NilError(t, repo.CreateTag(t.Context(), release, tip))

	return f, repo, tip
}

func TestRepo_PushTag_WhenOriginAccepts_PutsOnlyTheTagOnOrigin(t *testing.T) {
	t.Parallel()
	f, repo, tip := taggedRelease(t)
	before := originRefs(f)
	f.Git("tag", "v0.9.0", tip)

	err := repo.PushTag(t.Context(), release)
	assert.NilError(t, err, "PushTag(%q)", release)

	assert.Check(t, is.Equal(originRefs(f), strings.TrimSpace(before+"\n"+releaseRef)))
	assert.Check(t, is.Equal(f.OriginGit("rev-parse", releaseRef+"^{commit}"), tip))
}

func TestRepo_PushTag_WhenOriginHoldsTheTagElsewhere_ReturnsPushErrorNamingTheTagRef(t *testing.T) {
	t.Parallel()
	f, repo, _ := taggedRelease(t)
	f.OriginTag(release, f.Git("rev-parse", trunk))

	err := repo.PushTag(t.Context(), release)

	pushErr, ok := errors.AsType[*git.PushError](err)
	assert.Assert(t, ok, "PushTag(%q) error = %v, want *git.PushError", release, err)
	assert.Check(t, is.Equal(pushErr.Ref, releaseRef))
	assert.Check(t, is.Contains(pushErr.Detail(), "already exists"))
}

func TestRepo_WhenNotARepository_OriginCallsReturnExitError(t *testing.T) {
	t.Parallel()
	tests := []repoCall{
		{name: "OriginURLs", call: func(ctx context.Context, repo git.Repo) (any, error) { return repo.OriginURLs(ctx) }},
		fastForwardTrunkCall(),
	}

	checkExitErrorOutsideRepo(t, tests)
}
