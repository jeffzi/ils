package git_test

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
		FetchURL: httpsURL,
		Push:     []git.PushURL{{Origin: toolsOrigin(), URL: sshURL}},
	}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestRepo_OriginURLs_WhenSeveralPushURLs_ReturnsEachParsedInTheOrderGitLists(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(httpsURL)
	f.AddPushURL(sshURL)
	f.AddPushURL(httpsURL)

	got, err := newRepo(f).OriginURLs(t.Context())
	assert.NilError(t, err)

	want := []git.PushURL{{Origin: toolsOrigin(), URL: sshURL}, {Origin: ilsOrigin(), URL: httpsURL}}
	assert.Check(t, is.DeepEqual(got.Push, want))
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

	got, err := repo.OriginURLs(t.Context())

	originErr, ok := errors.AsType[*git.OriginError](err)
	assert.Assert(t, ok, "OriginURLs() of %q error = %v, want *git.OriginError", scpLike, err)
	assert.Check(t, is.Equal(originErr.URL, scpLike))
	assert.Assert(t, is.Len(got.Push, 1))
	pushErr, ok := errors.AsType[*git.OriginError](got.Push[0].Err)
	assert.Assert(t, ok, "PushURL %q Err = %v, want *git.OriginError", scpLike, got.Push[0].Err)
	assert.Check(t, is.Equal(pushErr.URL, scpLike))
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
				FetchURL: localPath,
				Push:     []git.PushURL{{Origin: toolsOrigin(), URL: sshURL}},
			},
		},
		{
			name: "push url",
			want: git.OriginURLs{
				Fetch:    ilsOrigin(),
				FetchURL: httpsURL,
				Push:     []git.PushURL{{URL: localPath, Err: &git.OriginError{URL: localPath}}},
			},
		},
		{
			name: "push url after one that parses",
			want: git.OriginURLs{
				Fetch:    ilsOrigin(),
				FetchURL: httpsURL,
				Push:     []git.PushURL{{Origin: toolsOrigin(), URL: sshURL}, {URL: localPath, Err: &git.OriginError{URL: localPath}}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.SetOriginURL(tt.want.FetchURL)
			for _, push := range tt.want.Push {
				f.AddPushURL(push.URL)
			}

			got, err := newRepo(f).OriginURLs(t.Context())

			originErr, ok := errors.AsType[*git.OriginError](err)
			assert.Assert(t, ok, "OriginURLs() error = %v, want *git.OriginError", err)
			assert.Check(t, is.Equal(originErr.URL, localPath))
			assert.Check(t, is.DeepEqual(got, tt.want))
		})
	}
}

func TestRepo_OriginURLs_WhenSSHFailsForURLListedTwice_NamesTheFailureOnceAndOnEveryPushURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		fetch string
		push  []string
	}{
		{name: "fetch url is the push url", fetch: sshURL},
		{name: "push url listed twice", fetch: httpsURL, push: []string{sshURL, sshURL}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.SetOriginURL(tt.fetch)
			for _, push := range tt.push {
				f.AddPushURL(push)
			}
			repo := plainSSH(t, newRepo(f), standInRefuse)

			got, err := repo.OriginURLs(t.Context())

			exitErr, ok := errors.AsType[*child.ExitError](err)
			assert.Assert(t, ok, "OriginURLs() error = %v, want *child.ExitError", err)
			assert.Check(t, is.Equal(strings.Count(err.Error(), exitErr.Error()), 1), "OriginURLs() error = %q", err)
			assert.Check(t, is.Len(got.Push, max(len(tt.push), 1)))
			for _, push := range got.Push {
				assert.Check(t, is.ErrorIs(push.Err, exitErr), "PushURL %q Err = %v", push.URL, push.Err)
			}
		})
	}
}

func TestRepo_OriginURLs_WhenFetchAndPushDifferAndSSHFailsForBoth_NamesBothFailures(t *testing.T) {
	t.Parallel()
	const otherHostURL = "git@example.com:jeffzi/ils.git"
	f := gittest.New(t, trunk)
	f.SetOriginURL(sshURL)
	f.SetPushURL(otherHostURL)
	repo := plainSSH(t, newRepo(f), standInRefuse)

	_, err := repo.OriginURLs(t.Context())

	assert.Assert(t, err != nil, "OriginURLs() error = nil, want the failures of both ssh calls")
	assert.Check(t, is.Contains(err.Error(), "github.com"))
	assert.Check(t, is.Contains(err.Error(), "example.com"))
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

	remote := errors.Is(err, git.ErrRemote)
	assert.Check(t, !remote, "FastForwardTrunk(%q) error = %v, want git's refusal, not git.ErrRemote", trunk, err)
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

// sshFailedCode is the code ssh exits with when it fails itself, rather than the command it ran.
const sshFailedCode = 255

// endedBy is one way origin's server ends a call: arrange points the fixture's origin at it and
// returns the environment that reaches it.
type endedBy struct {
	arrange func(t *testing.T, f *gittest.Fixture) (env []string)
	name    string
}

// sshSaying returns an arrangement whose origin is reached over ssh, where every call prints said on
// stderr and exits with code before git hears from the repository.
func sshSaying(said string, code int) func(t *testing.T, f *gittest.Fixture) []string {
	return func(t *testing.T, f *gittest.Fixture) []string {
		t.Helper()
		f.SetOriginURL(sshURL)

		return f.ServedBy("echo '" + said + "' >&2; exit " + strconv.Itoa(code))
	}
}

// httpAnswering returns an arrangement whose origin is an HTTP server that answers every request
// with status, under a URL that carries a user and a password.
func httpAnswering(status int) func(t *testing.T, f *gittest.Fixture) []string {
	return func(t *testing.T, f *gittest.Fixture) []string {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("WWW-Authenticate", `Basic realm="ils"`)
			w.WriteHeader(status)
		}))
		t.Cleanup(srv.Close)

		f.SetOriginURL("http://ils:wrong@" + srv.Listener.Addr().String() + "/jeffzi/ils.git")

		return f.Env()
	}
}

// refusals lists one case for each message refusal in origin.go matches, but the ssh key that
// TestRepo_WhenOriginRefusesTheCall_ReturnsErrRefused refuses: an ssh repository refused, the
// credentials GitHub did not accept, and an HTTP 401, 403 and 404.
func refusals() []endedBy {
	return []endedBy{
		{name: "ssh repository not found", arrange: sshSaying("ERROR: Repository not found.", 1)},
		{
			name:    "credentials GitHub did not accept",
			arrange: sshSaying("remote: Invalid username or token. Password authentication is not supported for Git operations.", 1),
		},
		{name: "http authentication failed", arrange: httpAnswering(http.StatusUnauthorized)},
		{name: "http permission denied", arrange: httpAnswering(http.StatusForbidden)},
		{name: "http repository not found", arrange: httpAnswering(http.StatusNotFound)},
	}
}

// assertRefused fails unless err is origin's refusal of the call name, not origin going unreached,
// and wraps git's exit.
func assertRefused(t *testing.T, name string, err error) {
	t.Helper()

	refused := errors.Is(err, git.ErrRefused)
	assert.Check(t, refused, "%s() error = %v, want git.ErrRefused", name, err)
	remote := errors.Is(err, git.ErrRemote)
	assert.Check(t, !remote, "%s() error = %v, want it not to read as origin not answering", name, err)
	_, exited := errors.AsType[*child.ExitError](err)
	assert.Check(t, exited, "%s() error = %v, want it to wrap *child.ExitError", name, err)
}

func TestRepo_RemoteBranch_WhenOriginRefusesTheCall_ReturnsErrRefused(t *testing.T) {
	t.Parallel()

	for _, tt := range refusals() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			env := tt.arrange(t, f)
			repo := git.Repo{Runner: git.Runner{Dir: f.Dir(), Env: env}}

			_, _, err := repo.RemoteBranch(t.Context(), trunk)

			assertRefused(t, "RemoteBranch", err)
		})
	}
}

func TestRepo_WhenOriginRefusesTheCall_ReturnsErrRefused(t *testing.T) {
	t.Parallel()
	tests := []repoCall{
		{name: "OriginTags", call: func(ctx context.Context, repo git.Repo) (any, error) { return repo.OriginTags(ctx) }},
		fastForwardTrunkCall(),
		{name: "Push", call: func(ctx context.Context, repo git.Repo) (any, error) {
			sha, err := repo.HeadSHA(ctx)
			if err != nil {
				return nil, err
			}

			return nil, repo.Push(ctx, topic, sha)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.Commits(topic, "one")
			f.Git("switch", "--quiet", trunk)
			f.SetOriginURL(sshURL)
			env := f.DeniedEnv()
			repo := git.Repo{Runner: git.Runner{Dir: f.Dir(), Env: env}}

			got, err := tt.call(t.Context(), repo)

			assertRefused(t, tt.name, err)
			checkNoResult(t, tt.name, got)
		})
	}
}

func TestRepo_RemoteBranch_WhenALocalPolicyDeniesTheSSHConnection_ReturnsErrRemote(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	env := sshSaying("ssh: connect to host github.com port 22: Permission denied", sshFailedCode)(t, f)
	repo := git.Repo{Runner: git.Runner{Dir: f.Dir(), Env: env}}

	_, _, err := repo.RemoteBranch(t.Context(), trunk)

	remote := errors.Is(err, git.ErrRemote)
	assert.Check(t, remote, "RemoteBranch() error = %v, want git.ErrRemote", err)
	refused := errors.Is(err, git.ErrRefused)
	assert.Check(t, !refused, "RemoteBranch() error = %v, want it not to read as origin refusing", err)
}

func TestRepo_Trunk_WhenGitMissingFromPath_ReturnsToolMissingError(t *testing.T) {
	// Not parallel: this empties PATH for the whole process while it runs. RemoteBranch hands a
	// missing git to the same remoteFailure as Trunk does, so Trunk alone stands for both.
	repo := newRepo(gittest.New(t, trunk))
	t.Setenv("PATH", t.TempDir())

	_, err := repo.Trunk(t.Context())

	_, ok := errors.AsType[*child.ToolMissingError](err)
	assert.Assert(t, ok, "Trunk() error = %v, want *child.ToolMissingError", err)
	remote := errors.Is(err, git.ErrRemote)
	assert.Check(t, !remote, "Trunk() error = %v, want it not to read as origin not answering", err)
}

func TestRepo_Trunk_WhenContextEnded_ReturnsErrRemote(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got, err := newRepo(f).Trunk(ctx)

	ok := errors.Is(err, git.ErrRemote)
	assert.Check(t, ok, "Trunk() = %q, error = %v, want git.ErrRemote", got, err)
	assert.Check(t, is.ErrorIs(err, context.Canceled))
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

// lastFetchedAt records branch as origin's default branch in the clone, as a clone or a plain fetch
// would, without asking origin.
func lastFetchedAt(t *testing.T, f *gittest.Fixture, branch string) {
	t.Helper()
	f.Git("remote", "set-head", "origin", branch)
}

// movedTrunk is the branch origin's default branch moves to after the clone last fetched.
const movedTrunk = "main"

// moveOriginTrunk makes movedTrunk origin's default branch, without the clone fetching it.
func moveOriginTrunk(t *testing.T, f *gittest.Fixture) {
	t.Helper()
	f.OriginGit("branch", movedTrunk, trunk)
	f.OriginGit("symbolic-ref", "HEAD", "refs/heads/"+movedTrunk)
}

func TestRepo_TrunkOrLastFetched_WhenOriginAnswers_ReturnsOriginsTrunkOverTheLastFetched(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	lastFetchedAt(t, f, trunk)
	moveOriginTrunk(t, f)

	got, found, err := newRepo(f).TrunkOrLastFetched(t.Context())
	assert.NilError(t, err)

	assert.Check(t, found, "TrunkOrLastFetched() found = false, want true")
	assert.Check(t, is.Equal(got, movedTrunk))
}

func TestRepo_TrunkOrLastFetched_WhenOriginDoesNotAnswer_ReturnsTheLastFetchedTrunk(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	lastFetchedAt(t, f, trunk)
	moveOriginTrunk(t, f)
	ctx, cancel := context.WithTimeout(t.Context(), 0)
	t.Cleanup(cancel)

	got, found, err := newRepo(f).TrunkOrLastFetched(ctx)
	assert.NilError(t, err)

	assert.Check(t, found, "TrunkOrLastFetched() found = false, want true")
	assert.Check(t, is.Equal(got, trunk))
}

func TestRepo_TrunkOrLastFetched_WhenOriginRefusesTheCaller_ReturnsTheLastFetchedTrunk(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	lastFetchedAt(t, f, trunk)
	f.SetOriginURL(sshURL)
	env := f.DeniedEnv()
	repo := git.Repo{Runner: git.Runner{Dir: f.Dir(), Env: env}}

	got, found, err := repo.TrunkOrLastFetched(t.Context())
	assert.NilError(t, err)

	assert.Check(t, found, "TrunkOrLastFetched() found = false, want true")
	assert.Check(t, is.Equal(got, trunk))
}

func TestRepo_TrunkOrLastFetched_WhenOriginDoesNotAnswerAndNothingWasFetched_ReportsNotFound(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.CutOffOrigin()

	got, found, err := newRepo(f).TrunkOrLastFetched(t.Context())
	assert.NilError(t, err)

	assert.Check(t, !found, "TrunkOrLastFetched() = %q, found = true, want not found", got)
}

func TestRepo_TrunkOrLastFetched_WhenOriginNamesNoBranch_ReturnsErrNoSymref(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	lastFetchedAt(t, f, trunk)
	f.DetachOriginHead()

	_, _, err := newRepo(f).TrunkOrLastFetched(t.Context())

	assert.Check(t, is.ErrorIs(err, git.ErrNoSymref))
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

func localTip(f *gittest.Fixture) string { return f.Git("rev-parse", "refs/heads/"+topic) }

// killsReceivePack kills the hook's parent: git runs a hook as a child of receive-pack, so `$PPID`
// is the process that must die mid-push.
const killsReceivePack = "kill -KILL $PPID"

// specMark stands, in a hook's script and in what a test wants, for the pair of refs Push hands git
// for topic's tip, which git's report names on its status line.
const specMark = "<spec>"

// otherSpec is the pair of refs for a branch Push never pushes, which a hook's report-shaped lines name.
const otherSpec = "refs/heads/other:refs/heads/other"

// withSpec returns text with the pair of refs Push hands git for topic's tip in place of specMark.
func withSpec(f *gittest.Fixture, text string) string {
	return strings.ReplaceAll(text, specMark, localTip(f)+":refs/heads/"+topic)
}

func pushTip(ctx context.Context, f *gittest.Fixture) error {
	return newRepo(f).Push(ctx, topic, localTip(f))
}

// originTip returns the sha origin holds for branch, or nothing when origin lacks it.
func originTip(f *gittest.Fixture, branch string) string {
	return f.OriginGit("for-each-ref", "--format=%(objectname)", "refs/heads/"+branch)
}

func TestRepo_Push_WhenTheBranchMovedPastTheGivenCommit_PublishesTheGivenCommit(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	checked := f.Commits(topic, "one", "two")[0]

	err := newRepo(f).Push(t.Context(), topic, checked)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(originTip(f, topic), checked))
}

func TestRepo_Push_WhenOriginAccepts_SetsOriginsBranchAsTheUpstream(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")

	err := pushTip(t.Context(), f)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(f.Git("for-each-ref", "--format=%(upstream)", "refs/heads/"+topic), "refs/remotes/origin/"+topic))
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

func TestRepo_Push_WhenOriginHangsUpAfterReportingTheRefUpdated_PublishesTheBranch(t *testing.T) {
	t.Parallel()
	gittest.NeedSignals(t)
	f := gittest.New(t, trunk)
	tip := f.Commits(topic, "one")[0]
	f.OriginHook("post-receive", killsReceivePack)

	err := pushTip(t.Context(), f)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(originTip(f, topic), tip))
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

	err := pushTip(t.Context(), f)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(originTip(f, topic), tip))
}

func TestRepo_Push_WhenOriginRefusesTheRef_ReturnsPushErrorNamingTheReason(t *testing.T) {
	t.Parallel()
	const said = "policy forbids this push"
	f := originRefusing(t, said)

	err := pushTip(t.Context(), f)

	pushErr, ok := errors.AsType[*git.PushError](err)
	assert.Assert(t, ok, "Push(%q) error = %v, want *git.PushError", topic, err)
	assert.Check(t, is.Equal(pushErr.Ref, "refs/heads/"+topic))
	assert.Check(t, pushErr.Reason != "" && pushErr.Reason != "!", "Reason = %q, want the status line's summary", pushErr.Reason)
	assert.Check(t, !strings.Contains(pushErr.Reason, "refs/heads/"), "Reason = %q, want the summary, not the ref it names",
		pushErr.Reason)
	assert.Check(t, is.Contains(pushErr.Stderr, said))
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
			script: "printf '!\\t" + specMark + "\\tblocked by policy\\n'\nexit 1",
			want:   "!\t" + specMark + "\tblocked by policy\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.Commits(topic, "one")
			f.Hook("pre-push", withSpec(f, tt.script))
			want := withSpec(f, tt.want)

			err := pushTip(t.Context(), f)

			pushErr, ok := errors.AsType[*git.PushError](err)
			assert.Assert(t, ok, "Push(%q) error = %v, want *git.PushError", topic, err)
			assert.Check(t, is.Equal(pushErr.Reason, ""))
			assert.Check(t, strings.HasPrefix(pushErr.Output(), want), "Output() = %q, want it to start with %q",
				pushErr.Output(), want)
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
			script: "printf '*\\t" + specMark + "\\t[new branch]\\n'",
			want:   "*\t" + specMark + "\t[new branch]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := originRefusing(t, "policy forbids this push")
			f.Hook("pre-push", withSpec(f, tt.script))

			err := pushTip(t.Context(), f)

			pushErr, ok := errors.AsType[*git.PushError](err)
			assert.Assert(t, ok, "Push(%q) error = %v, want *git.PushError", topic, err)
			assert.Check(t, is.Equal(strings.TrimSpace(pushErr.Stdout), withSpec(f, tt.want)))
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

func TestRepo_Push_WhenOriginNeverAnswers_ReturnsErrRemote(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.Commits(topic, "one")
	f.CutOffOrigin()

	err := pushTip(t.Context(), f)

	remote := errors.Is(err, git.ErrRemote)
	assert.Check(t, remote, "Push(%q) error = %v, want git.ErrRemote", topic, err)
}

func taggedRelease(t *testing.T) (f *gittest.Fixture, repo git.Repo, tip string) {
	t.Helper()
	f = gittest.New(t, trunk)
	tip = f.Commits(topic, "one")[0]
	repo = newRepo(f)
	assert.NilError(t, repo.CreateTag(t.Context(), release, tip))

	return f, repo, tip
}

func TestRepo_PushTag_WhenContextCanceled_ReturnsNeitherPushErrorNorErrRemote(t *testing.T) {
	t.Parallel()
	_, repo, _ := taggedRelease(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := repo.PushTag(ctx, release)

	assert.Check(t, is.ErrorIs(err, context.Canceled))
	_, refused := errors.AsType[*git.PushError](err)
	assert.Check(t, !refused, "PushTag(%q) error = %v, want no *git.PushError", release, err)
	remote := errors.Is(err, git.ErrRemote)
	assert.Check(t, !remote, "PushTag(%q) error = %v, want no git.ErrRemote", release, err)
}

func TestRepo_PushTag_WhenOriginAccepts_PutsTheTagOnOrigin(t *testing.T) {
	t.Parallel()
	f, repo, tip := taggedRelease(t)

	err := repo.PushTag(t.Context(), release)
	assert.NilError(t, err, "PushTag(%q)", release)

	assert.Check(t, is.Equal(f.OriginGit("rev-parse", releaseRef+"^{commit}"), tip))
}

// emptyBare creates an empty bare repository and returns its location in the slash form git
// accepts on every platform.
func emptyBare(t *testing.T, f *gittest.Fixture) string {
	t.Helper()

	dir := filepath.ToSlash(filepath.Join(t.TempDir(), "mirror.git"))
	f.Git("init", "--quiet", "--bare", dir)

	return dir
}

// pushCalls returns Push of topic's tip and PushTag of release, both of which a fixture made by
// taggedRelease holds.
func pushCalls() []repoCall {
	return []repoCall{
		{name: "Push", call: func(ctx context.Context, repo git.Repo) (any, error) {
			sha, err := repo.HeadSHA(ctx)
			if err != nil {
				return nil, err
			}

			return nil, repo.Push(ctx, topic, sha)
		}},
		{name: "PushTag", call: func(ctx context.Context, repo git.Repo) (any, error) {
			return nil, repo.PushTag(ctx, release)
		}},
	}
}

// refusedPush is a push to two push URLs, one of which refuses it.
type refusedPush struct {
	call          repoCall
	name          string
	refusingFirst bool
}

func TestRepo_WhenOnePushURLRefuses_ReturnsPushErrorWhereverGitListsIt(t *testing.T) {
	t.Parallel()
	var tests []refusedPush
	for _, call := range pushCalls() {
		tests = append(tests,
			refusedPush{call: call, name: call.name + " refused by the first push url", refusingFirst: true},
			refusedPush{call: call, name: call.name + " refused by the last push url"},
		)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, repo, _ := taggedRelease(t)
			f.OriginHook("pre-receive", "exit 1")
			pushURLs := []string{filepath.ToSlash(f.OriginDir()), emptyBare(t, f)}
			if !tt.refusingFirst {
				slices.Reverse(pushURLs)
			}
			for _, pushURL := range pushURLs {
				f.AddPushURL(pushURL)
			}

			_, err := tt.call.call(t.Context(), repo)

			pushErr, ok := errors.AsType[*git.PushError](err)
			assert.Assert(t, ok, "%s() error = %v, want *git.PushError", tt.call.name, err)
			assert.Check(t, strings.Contains(pushErr.Reason, "pre-receive hook declined"), "Reason %q, want the refusing push URL's reason", pushErr.Reason)
		})
	}
}

func TestRepo_WhenGitFailsAfterSeveralPushURLsAccept_ReturnsAnError(t *testing.T) {
	t.Parallel()
	for _, call := range pushCalls() {
		t.Run(call.name, func(t *testing.T) {
			t.Parallel()
			f, repo, _ := taggedRelease(t)
			// git stops at the push url that holds no repository, after the two ahead of it accepted.
			pushURLs := []string{emptyBare(t, f), emptyBare(t, f), filepath.ToSlash(f.MissingOriginDir())}
			for _, pushURL := range pushURLs {
				f.AddPushURL(pushURL)
			}

			_, err := call.call(t.Context(), repo)

			assert.Check(t, err != nil, "%s() error = nil, want the failure of the last push url", call.name)
		})
	}
}

func TestRepo_WhenNotARepository_OriginCallsReturnExitError(t *testing.T) {
	t.Parallel()
	tests := []repoCall{
		{name: "OriginURLs", call: func(ctx context.Context, repo git.Repo) (any, error) { return repo.OriginURLs(ctx) }},
		fastForwardTrunkCall(),
	}

	checkExitErrorOutsideRepo(t, tests)
}

func FuzzSplitPushReport(f *testing.F) {
	const spec = "refs/heads/topic:refs/heads/topic"
	for _, seed := range []string{
		"To origin\n*\t" + spec + "\t[new branch]\nDone\n",
		"To origin\n!\t" + spec + "\t[remote rejected] (pre-receive hook declined)\nDone\n",
		"hook said\nTo elsewhere\n*\t" + spec + "\t[new branch]\nDone\n",
		"!\t" + spec + "\tblocked by policy\n",
		"To origin\nTo\n=\t" + spec + "\t[up to date]\nDone\n",
		"",
		"\n",
	} {
		f.Add(seed, spec)
	}

	f.Fuzz(func(t *testing.T, stdout, spec string) {
		hook, _ := git.SplitPushReport(stdout, spec)

		assert.Check(t, strings.HasPrefix(stdout, hook), "SplitPushReport(%q) hook = %q, not a prefix of stdout", stdout, hook)
	})
}
