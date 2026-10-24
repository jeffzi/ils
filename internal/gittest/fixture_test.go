package gittest_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/gittest"
)

const (
	mainBranch = "main"
	work       = "work"
	fixedDate  = "2001-02-03T04:05:06Z"
	origin     = "origin"
	// elsewhere is an origin URL that names a host no test reaches.
	elsewhere = "ssh://git@example.com/owner/project.git"
)

// spyTB records failures reported through it instead of failing the real test.
type spyTB struct {
	testing.TB

	messages []string
	stopped  bool
}

func (s *spyTB) Logf(format string, args ...any) {
	s.messages = append(s.messages, fmt.Sprintf(format, args...))
}

func (s *spyTB) Fatalf(format string, args ...any) {
	s.Logf(format, args...)
	s.FailNow()
}

func (s *spyTB) FailNow() {
	s.stopped = true

	runtime.Goexit()
}

// runGit runs git in dir with the fixture's environment, independent of the fixture's own runner,
// and returns what it did with the line ends trimmed from its stdout and the spaces around its
// stderr.
func runGit(t *testing.T, f *gittest.Fixture, dir string, args ...string) faked {
	t.Helper()

	got := callGit(t, f.Env(), dir, args...)
	got.Stdout = strings.TrimRight(got.Stdout, "\r\n")
	got.Stderr = strings.TrimSpace(got.Stderr)

	return got
}

// git runs git in dir and fails the test when git exits non-zero.
func git(t *testing.T, f *gittest.Fixture, dir string, args ...string) string {
	t.Helper()

	got := runGit(t, f, dir, args...)
	assert.Equal(t, got.Code, 0, "git %v in %s, stderr: %s", args, dir, got.Stderr)

	return got.Stdout
}

// logged returns one line per commit of the work branch past the trunk, oldest first, each
// printed in format.
func logged(t *testing.T, f *gittest.Fixture, format string) []string {
	t.Helper()

	return strings.Split(git(t, f, f.Dir(), "log", "--reverse", "--format="+format, mainBranch+".."+work), "\n")
}

// hasCommit reports whether the repository at dir holds the commit sha, and fails the test when
// git cannot answer.
func hasCommit(t *testing.T, f *gittest.Fixture, dir, sha string) bool {
	t.Helper()

	// With --quiet, rev-parse --verify exits 1 and prints nothing when the commit is absent.
	got := runGit(t, f, dir, "rev-parse", "--verify", "--quiet", sha+"^{commit}")
	assert.Assert(t, got.Code == 0 || got.Code == 1, "git rev-parse --verify %s in %s printed %+v", sha, dir, got)

	return got.Code == 0
}

func originBranch(t *testing.T, f *gittest.Fixture, branch string) string {
	t.Helper()

	return git(t, f, f.OriginDir(), "rev-parse", "--verify", "refs/heads/"+branch)
}

// envValue returns the value the environment gives key; the last entry wins, as it does for a child process.
func envValue(env []string, key string) (string, bool) {
	value, found := "", false

	for _, entry := range env {
		if rest, ok := strings.CutPrefix(entry, key+"="); ok {
			value, found = rest, true
		}
	}

	return value, found
}

// isUnder reports whether child lies inside parent once symbolic links are resolved.
func isUnder(t *testing.T, parent, child string) bool {
	t.Helper()

	rel, err := filepath.Rel(resolved(t, parent), resolved(t, child))

	return err == nil && filepath.IsLocal(rel)
}

func TestNew_WhenTrunkIsNotMain_OriginHeadNamesIt(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "develop")

	got := git(t, f, f.Dir(), "ls-remote", "--symref", origin, "HEAD")

	assert.Check(t, is.Contains(got, "ref: refs/heads/develop\tHEAD"))
}

func TestNew_WhenFresh_PushesTrunkCommitToBareOrigin(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "develop")

	local := git(t, f, f.Dir(), "rev-parse", "--verify", "refs/heads/develop")

	assert.Check(t, is.Equal(originBranch(t, f, "develop"), local))
}

func TestNewServed_WhenFresh_StandsOnTheBranchWithOriginServedFromTheBareOrigin(t *testing.T) {
	t.Parallel()

	f, env := gittest.NewServed(t, mainBranch, work, elsewhere)

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "symbolic-ref", "--short", "HEAD"), work))
	assert.Check(t, is.Len(logged(t, f, "%H"), 1))
	assert.Check(t, is.Equal(git(t, f, f.Dir(), "remote", "get-url", origin), elsewhere))
	assert.Check(t, is.DeepEqual(env, f.ServedEnv(f.OriginDir())))
}

func TestWriteFile_WhenFileIsNew_PutsContentInTheNamedFileOfDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	gittest.WriteFile(t, dir, "notes.txt", "first line\n")

	got, err := fs.ReadFile(os.DirFS(dir), "notes.txt")
	assert.NilError(t, err)
	assert.Check(t, is.Equal(string(got), "first line\n"))
}

func TestFixture_MissingOriginDir_WhenFresh_NamesNoRepositoryBesideTheOrigin(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)

	got := f.MissingOriginDir()

	_, err := os.Stat(got)
	assert.Check(t, is.ErrorIs(err, os.ErrNotExist))
	assert.Check(t, is.Equal(filepath.Dir(got), filepath.Dir(f.OriginDir())))
}

func TestFixture_Commits_WhenBranchIsNew_ReturnsShasOldestFirstWithGivenSubjects(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	subjects := []string{"first change", "second change", "third change"}

	shas := f.Commits(work, subjects...)

	assert.Check(t, is.DeepEqual(logged(t, f, "%H"), shas))
	assert.Check(t, is.DeepEqual(logged(t, f, "%s"), subjects))
}

func TestFixture_Commits_WhenAnotherBranchIsCheckedOut_CutsBranchFromTrunk(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	f.Commits("other", "unrelated change")
	trunk := git(t, f, f.Dir(), "rev-parse", mainBranch)

	shas := f.Commits(work, "first change")

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "rev-parse", shas[0]+"^"), trunk))
}

func TestFixture_Commits_WhenBranchExists_AddsCommitsOnTop(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	earlier := f.Commits(work, "first change")

	shas := f.Commits(work, "second change", "third change")

	assert.Check(t, is.DeepEqual(logged(t, f, "%H"), slices.Concat(earlier, shas)))
	assert.Check(t, is.DeepEqual(logged(t, f, "%s"), []string{"first change", "second change", "third change"}))
}

func TestFixture_AdvanceOrigin_WhenBranchIsPushed_AddsCommitTheCloneLacks(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")
	f.Push(work)

	ahead := f.AdvanceOrigin(work)

	assert.Check(t, is.Equal(originBranch(t, f, work), ahead))

	assert.Check(t, is.Equal(git(t, f, f.OriginDir(), "rev-parse", ahead+"^"), shas[0]))

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "rev-parse", work), shas[0]))

	assert.Check(t, !hasCommit(t, f, f.Dir(), ahead))
}

func TestFixture_AdvanceOrigin_WhenCalledTwice_StacksSecondCommitOnFirst(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")
	f.Push(work)
	first := f.AdvanceOrigin(work)

	second := f.AdvanceOrigin(work)

	want := strings.Join([]string{shas[0], first, second}, "\n")
	assert.Check(t, is.Equal(git(t, f, f.OriginDir(), "rev-list", "--reverse", mainBranch+".."+work), want))
}

func TestFixture_Diverge_WhenBranchIsPushed_GivesEachSideACommitTheOtherLacks(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")
	f.Push(work)

	local, remote := f.Diverge(work)

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "rev-parse", work), local))
	assert.Check(t, local != shas[0], "local commit = %q, want a new one", local)

	assert.Check(t, is.Equal(originBranch(t, f, work), remote))
	assert.Check(t, remote != shas[0], "origin commit = %q, want a new one", remote)

	assert.Check(t, !hasCommit(t, f, f.Dir(), remote))

	assert.Check(t, !hasCommit(t, f, f.OriginDir(), local))
}

func TestFixture_Sibling_WhenBranchHasTwoCommits_SharesFirstCommitPastTrunk(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change", "second change")

	tip := f.Sibling("other", work)

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "rev-parse", "--verify", "refs/heads/other"), tip))

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "merge-base", work, "other"), shas[0]))

	assert.Check(t, tip != shas[0], "other tip = %q, want a commit of its own", tip)
}

// sshHost is the host alias that aliases defines.
const sshHost = "work"

const aliases = "Host " + sshHost + "\n  HostName git.example.org\n"

// askedForConfig runs the ssh command env names, as git runs a command line, to print the
// configuration ssh would connect to host with, and returns what it did.
func askedForConfig(t *testing.T, f *gittest.Fixture, env []string, host string) faked {
	t.Helper()

	command, ok := envValue(env, "GIT_SSH_COMMAND")
	assert.Assert(t, ok, "environment sets no GIT_SSH_COMMAND")

	return callGit(t, env, f.Dir(), "-c", "alias.read-config=!"+command, "read-config", "-G", host)
}

// hostnameIn returns the host an `ssh -G` output says ssh connects to, or "" when it names none.
func hostnameIn(config string) string {
	for line := range strings.Lines(config) {
		if host, ok := strings.CutPrefix(strings.TrimSpace(line), "hostname "); ok {
			return host
		}
	}

	return ""
}

func TestMarking_WhenAShellRunsIt_CreatesTheMarkerWhateverArgumentsFollow(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, mainBranch)
	marker := filepath.Join(t.TempDir(), "ran")

	f.Git("-c", "alias.mark=!"+gittest.Marking(marker), "mark", "-G", "--", sshHost)

	_, err := os.Stat(marker)
	assert.NilError(t, err)
}

func TestFixture_ServedBy_WhenAskedForConfig_AnswersFromTheSSHConfigWithoutServing(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	f.SetSSHConfig(aliases)
	marker := filepath.Join(t.TempDir(), "ran")

	got := askedForConfig(t, f, f.ServedBy(gittest.Marking(marker)), sshHost)
	assert.Equal(t, got.Code, 0, "stderr: %s", got.Stderr)

	assert.Check(t, is.Equal(hostnameIn(got.Stdout), "git.example.org"))

	_, statErr := os.Stat(marker)
	assert.Check(t, is.ErrorIs(statErr, fs.ErrNotExist), "the serving script ran for a config read")
}

func TestFixture_ServedBy_WhenFresh_PutsTheStandInDirectoryBeforeThePath(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	before, _ := envValue(f.Env(), "PATH")

	got, _ := envValue(f.ServedBy("exit 0"), "PATH")

	dir := filepath.SplitList(got)[0]
	_, err := os.Stat(filepath.Join(dir, "ssh"))
	assert.Check(t, err, "the first PATH entry %s holds no stand-in for ssh", dir)
	assert.Check(t, is.Equal(got, dir+string(os.PathListSeparator)+before))
}

// fixtureEnv is one way a fixture builds the environment of a git command.
type fixtureEnv struct {
	env  func(f *gittest.Fixture) []string
	name string
}

// fixtureEnvs are the environments of a fixture whose ssh command reads the fixture's ssh config.
func fixtureEnvs() []fixtureEnv {
	return []fixtureEnv{
		{name: "Env", env: (*gittest.Fixture).Env},
		{name: "ServedEnv", env: func(f *gittest.Fixture) []string { return f.ServedEnv(f.OriginDir()) }},
	}
}

func TestNew_WhenSSHConfigNeverSet_AnswersAsAnEmptyConfig(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	dir := t.TempDir()
	const configName = "config"
	gittest.WriteFile(t, dir, configName, "")
	emptyConfigCommand := "GIT_SSH_COMMAND=ssh -F '" + filepath.ToSlash(filepath.Join(dir, configName)) + "'"
	want := askedForConfig(t, f, append(f.Env(), emptyConfigCommand), sshHost)
	assert.Equal(t, want.Code, 0, "stderr: %s", want.Stderr)

	got := askedForConfig(t, f, f.Env(), sshHost)
	assert.Equal(t, got.Code, 0, "stderr: %s", got.Stderr)

	assert.Check(t, is.Equal(got.Stdout, want.Stdout))
}

func TestFixture_SetSSHConfig_WhenCalledTwice_KeepsTheSecondContentOnly(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	f.SetSSHConfig("Host " + sshHost + "\n  HostName first.example\n")

	f.SetSSHConfig("Host " + sshHost + "\n  HostName second.example\n")

	got := askedForConfig(t, f, f.Env(), sshHost)
	assert.Equal(t, got.Code, 0, "stderr: %s", got.Stderr)
	assert.Check(t, is.Equal(hostnameIn(got.Stdout), "second.example"))
}

func TestFixture_RemoveSSHConfig_MakesTheConfigReadExitNonZero(t *testing.T) {
	t.Parallel()
	for _, tt := range fixtureEnvs() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, mainBranch)

			f.RemoveSSHConfig()

			got := askedForConfig(t, f, tt.env(f), sshHost)
			assert.Check(t, got.Code != 0, "ssh -G succeeded with its config file removed, want a non-zero exit")
		})
	}
}

func TestFixture_PullRef_WhenCommitIsLocal_PointsOriginPullHeadAtCommit(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")

	f.PullRef(7, shas[0])

	assert.Check(t, is.Equal(git(t, f, f.OriginDir(), "rev-parse", "--verify", "refs/pull/7/head"), shas[0]))
}

func TestFixture_SquashOnOrigin_WhenBodyIsGiven_AddsTrunkCommitTheCloneLacks(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	trunk := git(t, f, f.Dir(), "rev-parse", mainBranch)
	shas := f.Commits(work, "first change", "second change")
	tree := git(t, f, f.Dir(), "rev-parse", shas[1]+"^{tree}")

	squash := f.SquashOnOrigin(shas[1], "squash of work", "first paragraph\n\nsecond paragraph")

	assert.Check(t, is.Equal(originBranch(t, f, mainBranch), squash))

	want := trunk + " " + tree + " squash of work"
	assert.Check(t, is.Equal(git(t, f, f.OriginDir(), "log", "-1", "--format=%P %T %s", squash), want))

	assert.Check(t, is.Equal(git(t, f, f.OriginDir(), "log", "-1", "--format=%b", squash), "first paragraph\n\nsecond paragraph"))

	assert.Check(t, !hasCommit(t, f, f.Dir(), squash))
}

func TestFixture_SquashOnOrigin_WhenBodyIsEmpty_WritesSubjectAlone(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")

	squash := f.SquashOnOrigin(shas[0], "squash of work", "")

	assert.Check(t, is.Equal(git(t, f, f.OriginDir(), "log", "-1", "--format=%B", squash), "squash of work"))
}

// worktrees returns, for each worktree `git worktree list` run in dir names, the branch it has
// checked out, keyed by its directory with symbolic links resolved.
func worktrees(t *testing.T, f *gittest.Fixture, dir string) map[string]string {
	t.Helper()

	got := map[string]string{}
	path := ""

	for line := range strings.Lines(git(t, f, dir, "worktree", "list", "--porcelain")) {
		line = strings.TrimRight(line, "\n")
		if rest, ok := strings.CutPrefix(line, "worktree "); ok {
			path = resolved(t, rest)
		}

		if rest, ok := strings.CutPrefix(line, "branch "); ok {
			got[path] = rest
		}
	}

	return got
}

func resolved(t *testing.T, path string) string {
	t.Helper()

	got, err := filepath.EvalSymlinks(path)
	assert.NilError(t, err)

	return got
}

func TestFixture_TrunkWorktree_WhenOnABranch_ChecksOutTheTrunkInALinkedWorktree(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	f.Commits(work, "first change")

	dir := f.TrunkWorktree()

	want := map[string]string{
		resolved(t, f.Dir()): "refs/heads/" + work,
		resolved(t, dir):     "refs/heads/" + mainBranch,
	}
	assert.Check(t, is.DeepEqual(worktrees(t, f, f.Dir()), want))
}

func TestFixture_BranchWorktree_WhenBranchExists_LeavesTheTrunkInTheMainCheckoutSeenFromTheLinkedOne(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	f.Commits(work, "first change")

	dir := f.BranchWorktree(work)

	want := map[string]string{
		resolved(t, f.Dir()): "refs/heads/" + mainBranch,
		resolved(t, dir):     "refs/heads/" + work,
	}
	assert.Check(t, is.DeepEqual(worktrees(t, f, dir), want))
}

func TestFixture_AdvanceTrunk_WhenOnABranch_AddsALocalTrunkCommitOutsideTheCheckout(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	f.Commits(work, "first change")

	ahead := f.AdvanceTrunk()

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "log", "--format=%H", origin+"/"+mainBranch+".."+mainBranch), ahead))

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "symbolic-ref", "--short", "HEAD"), work))
}

func TestFixture_UntrackedOnTrunk_WhenOnABranch_MakesSwitchingToTheTrunkRefuseToOverwriteIt(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	f.Commits(work, "first change")

	path := f.UntrackedOnTrunk()

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "status", "--porcelain=v1", "--untracked-files=all"), "?? "+path))

	tracked := runGit(t, f, f.OriginDir(), "cat-file", "-e", mainBranch+":"+path)
	assert.Check(t, is.Equal(tracked.Code, 0), "origin's %s does not track %s", mainBranch, path)

	onTrunk := git(t, f, f.Dir(), "show", mainBranch+":"+path)
	here, err := fs.ReadFile(os.DirFS(f.Dir()), path)
	assert.NilError(t, err)
	assert.Check(t, onTrunk != strings.TrimRight(string(here), "\r\n"), "the local %s holds %s as it is here, want other content", mainBranch, path)

	switched := runGit(t, f, f.Dir(), "switch", mainBranch)
	assert.Check(t, switched.Code != 0, "git switch %s succeeded, want it refused", mainBranch)
	assert.Check(t, is.Equal(git(t, f, f.Dir(), "symbolic-ref", "--short", "HEAD"), work))
}

func TestFixture_Publish_WhenOriginURLNamesAnotherPlace_PutsLocalTipOnOrigin(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")
	f.SetOriginURL(elsewhere)

	f.Publish(work)

	assert.Check(t, is.Equal(originBranch(t, f, work), shas[0]))
}

func TestFixture_LandPull_WhenBranchHasACommit_SquashesTheCommitOntoOriginTrunkAsThePullRequest(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")
	tree := git(t, f, f.Dir(), "rev-parse", shas[0]+"^{tree}")

	f.LandPull(7, work)

	assert.Check(t, is.Equal(git(t, f, f.OriginDir(), "rev-parse", "--verify", "refs/pull/7/head"), shas[0]))

	want := tree + " first change (#7)"
	assert.Check(t, is.Equal(git(t, f, f.OriginDir(), "log", "-1", "--format=%T %s", mainBranch), want))
}

type sshURL struct{ name, url string }

// sshURLs are origin URLs over ssh in each shape git hands its ssh command differently.
func sshURLs() []sshURL {
	return []sshURL{
		{name: "ssh url", url: elsewhere},
		{name: "ssh url with a port", url: "ssh://git@example.com:2222/owner/project.git"},
		{name: "scp-like url", url: "git@example.com:owner/project.git"},
	}
}

func TestFixture_ServedEnv_WhenOriginURLIsSSH_FetchesFromTheRepositoryAtDir(t *testing.T) {
	t.Parallel()

	for _, tt := range sshURLs() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := gittest.New(t, mainBranch)
			shas := f.Commits(work, "first change")
			f.Publish(work)
			f.SetOriginURL(tt.url)

			got := callGit(t, f.ServedEnv(f.OriginDir()), f.Dir(), "ls-remote", origin, "refs/heads/"+work)
			assert.Equal(t, got.Code, 0, "git ls-remote, stderr: %s", got.Stderr)

			assert.Check(t, is.Equal(got.Stdout, shas[0]+"\trefs/heads/"+work+"\n"))
		})
	}
}

func TestFixture_ServedEnv_WhenOriginURLIsSSH_PushesToTheRepositoryAtDir(t *testing.T) {
	t.Parallel()

	for _, tt := range sshURLs() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := gittest.New(t, mainBranch)
			shas := f.Commits(work, "first change")
			f.SetOriginURL(tt.url)

			pushed := callGit(t, f.ServedEnv(f.OriginDir()), f.Dir(), "push", "--quiet", origin, work)
			assert.Equal(t, pushed.Code, 0, "git push, stderr: %s", pushed.Stderr)

			assert.Check(t, is.Equal(originBranch(t, f, work), shas[0]))
		})
	}
}

func TestEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key  string
		want string
	}{
		{key: "GIT_CONFIG_NOSYSTEM", want: "1"},
		{key: "GIT_CONFIG_GLOBAL", want: os.DevNull},
		{key: "GIT_AUTHOR_NAME", want: gittest.AuthorName},
		{key: "GIT_AUTHOR_EMAIL", want: gittest.AuthorEmail},
		{key: "GIT_COMMITTER_NAME", want: gittest.AuthorName},
		{key: "GIT_COMMITTER_EMAIL", want: gittest.AuthorEmail},
		{key: "GIT_AUTHOR_DATE", want: fixedDate},
		{key: "GIT_COMMITTER_DATE", want: fixedDate},
	}
	sources := []struct {
		name string
		env  []string
	}{
		{name: "gittest.Env", env: gittest.Env(t)},
		{name: "Fixture.Env", env: gittest.New(t, mainBranch).Env()},
	}

	for _, source := range sources {
		t.Run(source.name, func(t *testing.T) {
			t.Parallel()

			for _, tt := range tests {
				t.Run(tt.key, func(t *testing.T) {
					t.Parallel()

					got, ok := envValue(source.env, tt.key)

					assert.Check(t, ok, "%s does not set %s", source.name, tt.key)
					assert.Check(t, is.Equal(got, tt.want), "%s %s", source.name, tt.key)
				})
			}
		})
	}
}

func TestTempPaths_StayUnderTestTempDir(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	standalone := gittest.Env(t)
	parent := filepath.Dir(t.TempDir())

	fromEnv := func(env func() []string, key string) func() string {
		return func() string {
			value, _ := envValue(env(), key)

			return value
		}
	}
	tests := []struct {
		path func() string
		name string
	}{
		{name: "working repository", path: f.Dir},
		{name: "bare origin", path: f.OriginDir},
		{name: "HOME of Fixture.Env", path: fromEnv(f.Env, "HOME")},
		{name: "USERPROFILE of Fixture.Env", path: fromEnv(f.Env, "USERPROFILE")},
		{name: "HOME of gittest.Env", path: fromEnv(func() []string { return standalone }, "HOME")},
		{name: "USERPROFILE of gittest.Env", path: fromEnv(func() []string { return standalone }, "USERPROFILE")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.path()

			assert.Check(t, got != "", "%s path is empty", tt.name)
			assert.Check(t, isUnder(t, parent, got), "%s path = %q, want a directory under %q", tt.name, got, parent)
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key  string
		want string
	}{
		{key: "user.name", want: gittest.AuthorName},
		{key: "user.email", want: gittest.AuthorEmail},
		{key: "commit.gpgsign", want: "false"},
	}
	f := gittest.New(t, mainBranch)

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()

			got := runGit(t, f, f.Dir(), "config", "--local", "--get", tt.key)

			assert.Check(t, is.Equal(got.Code, 0), "config --local --get %s", tt.key)
			assert.Check(t, is.Equal(got.Stdout, tt.want), "config --local --get %s", tt.key)
		})
	}
}

func TestFixture_Commits_WhenProcessEnvironmentSetsIdentityAndSigning_AreUnsignedWithFixtureAuthor(t *testing.T) {
	config := filepath.Join(t.TempDir(), "gitconfig")

	err := os.WriteFile(config, []byte("[commit]\n\tgpgsign = true\n[user]\n\tname = Someone Else\n"), 0o600)
	assert.NilError(t, err)

	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_AUTHOR_NAME", "Someone Else")
	t.Setenv("GIT_AUTHOR_EMAIL", "someone@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Someone Else")
	t.Setenv("GIT_COMMITTER_EMAIL", "someone@example.com")

	f := gittest.New(t, mainBranch)

	shas := f.Commits(work, "first change")

	want := fmt.Sprintf("%[1]s <%[2]s> %[1]s <%[2]s> N", gittest.AuthorName, gittest.AuthorEmail)
	assert.Check(t, is.Equal(git(t, f, f.Dir(), "log", "-1", "--format=%an <%ae> %cn <%ce> %G?", shas[0]), want))
}

func TestFixture_GitRunners_RunInTheirOwnRepository(t *testing.T) {
	t.Parallel()

	tests := []struct {
		run  func(f *gittest.Fixture, args ...string) string
		name string
		want string
	}{
		{name: "working repository", run: (*gittest.Fixture).Git, want: "false"},
		{name: "bare origin", run: (*gittest.Fixture).OriginGit, want: "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := gittest.New(t, mainBranch)

			got := tt.run(f, "rev-parse", "--is-bare-repository")

			assert.Check(t, is.Equal(got, tt.want))
		})
	}
}

func TestFixture_Git_WhenGitExitsNonZero_FailsTestWithStderr(t *testing.T) {
	t.Parallel()

	spy := &spyTB{TB: t}
	f := gittest.New(spy, mainBranch)
	args := []string{"rev-parse", "--verify", "refs/heads/absent"}

	stderr := runGit(t, f, f.Dir(), args...).Stderr
	assert.Assert(t, stderr != "", "git %v wrote nothing to stderr, want a message to look for", args)

	done := make(chan struct{})

	go func() {
		defer close(done)

		f.Git(args...)
	}()
	<-done

	assert.Check(t, spy.stopped, "Git(%v) did not stop the test", args)
	assert.Check(t, is.Contains(strings.Join(spy.messages, "\n"), stderr))
}
