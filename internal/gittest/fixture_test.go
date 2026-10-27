package gittest_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/failtest"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
)

const (
	mainBranch = "main"
	work       = "work"
	origin     = "origin"
	// elsewhere is an origin URL that names a host no test reaches.
	elsewhere = "ssh://git@example.com/owner/project.git"
)

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

// gitStdout runs git in dir and returns its trimmed stdout, failing the test when git exits non-zero.
func gitStdout(t *testing.T, f *gittest.Fixture, dir string, args ...string) string {
	t.Helper()

	got := runGit(t, f, dir, args...)
	assert.Equal(t, got.Code, 0, "git %v in %s, stderr: %s", args, dir, got.Stderr)

	return got.Stdout
}

// logged returns one line per commit of the work branch past the trunk, oldest first, each
// printed in format.
func logged(t *testing.T, f *gittest.Fixture, format string) []string {
	t.Helper()

	return strings.Split(gitStdout(t, f, f.Dir(), "log", "--reverse", "--format="+format, mainBranch+".."+work), "\n")
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

	return gitStdout(t, f, f.OriginDir(), "rev-parse", "--verify", "refs/heads/"+branch)
}

// isUnder reports whether child lies inside parent once symbolic links are resolved.
func isUnder(t *testing.T, parent, child string) bool {
	t.Helper()

	rel, err := filepath.Rel(resolved(t, parent), resolved(t, child))

	return err == nil && filepath.IsLocal(rel)
}

func TestNew_WhenTrunkIsNotMain_PushesItToOriginAsItsHead(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "develop")

	local := gitStdout(t, f, f.Dir(), "rev-parse", "--verify", "refs/heads/develop")
	assert.Check(t, is.Equal(originBranch(t, f, "develop"), local))
	assert.Check(t, is.Equal(f.OriginGit("symbolic-ref", "HEAD"), "refs/heads/develop"))
}

func TestFixture_Commits_WhenBranchIsNew_ReturnsCommitsCutFromTrunkInOrder(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	// Another branch is checked out, so a branch cut from HEAD would not start at the trunk.
	f.Commits("other", "unrelated change")
	trunk := gitStdout(t, f, f.Dir(), "rev-parse", mainBranch)
	subjects := []string{"first change", "second change", "third change"}

	shas := f.Commits(work, subjects...)

	assert.Check(t, is.DeepEqual(logged(t, f, "%H"), shas))
	assert.Check(t, is.DeepEqual(logged(t, f, "%s"), subjects))
	assert.Check(t, is.Equal(gitStdout(t, f, f.Dir(), "rev-parse", shas[0]+"^"), trunk))
}

func TestFixture_Commits_WhenBranchExists_AddsCommitsOnTop(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	earlier := f.Commits(work, "first change")

	shas := f.Commits(work, "second change", "third change")

	assert.Check(t, is.DeepEqual(logged(t, f, "%H"), slices.Concat(earlier, shas)))
}

func TestFixture_AdvanceOrigin_WhenBranchIsPushed_AddsCommitTheCloneLacks(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")
	f.Push(work)

	ahead := f.AdvanceOrigin(work)

	assert.Check(t, is.Equal(originBranch(t, f, work), ahead))

	assert.Check(t, is.Equal(gitStdout(t, f, f.OriginDir(), "rev-parse", ahead+"^"), shas[0]))

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
	assert.Check(t, is.Equal(gitStdout(t, f, f.OriginDir(), "rev-list", "--reverse", mainBranch+".."+work), want))
}

func TestFixture_Diverge_WhenBranchIsPushed_GivesEachSideACommitTheOtherLacks(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	f.Commits(work, "first change")
	f.Push(work)

	local, remote := f.Diverge(work)

	assert.Check(t, is.Equal(gitStdout(t, f, f.Dir(), "rev-parse", work), local))
	assert.Check(t, is.Equal(originBranch(t, f, work), remote))
	assert.Check(t, !hasCommit(t, f, f.OriginDir(), local))
}

func TestFixture_Sibling_WhenBranchHasTwoCommits_SharesFirstCommitPastTrunk(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change", "second change")

	tip := f.Sibling("other", work)

	assert.Check(t, is.Equal(gitStdout(t, f, f.Dir(), "rev-parse", "--verify", "refs/heads/other"), tip))

	assert.Check(t, is.Equal(gitStdout(t, f, f.Dir(), "merge-base", work, "other"), shas[0]))

	assert.Check(t, tip != shas[0], "other tip = %q, want a commit of its own", tip)
}

// sshHost is the host alias that aliases defines.
const sshHost = "work"

const aliases = "Host " + sshHost + "\n  HostName git.example.org\n"

// askedForConfig runs the ssh command env names, as git runs a command line, to print the
// configuration ssh would connect to host with, and returns what it did.
func askedForConfig(t *testing.T, f *gittest.Fixture, env []string, host string) faked {
	t.Helper()

	command, _ := git.EnvValue(env, "GIT_SSH_COMMAND")

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

func TestFixture_RemoveSSHConfig_WhenSSHReadsTheConfig_ExitsNonZero(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)

	f.RemoveSSHConfig()

	got := askedForConfig(t, f, f.Env(), sshHost)
	assert.Check(t, got.Code != 0, "ssh -G succeeded with its config file removed, want a non-zero exit")
}

func TestFixture_SquashOnOrigin_WhenBodyIsGiven_AddsTrunkCommitTheCloneLacks(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	trunk := gitStdout(t, f, f.Dir(), "rev-parse", mainBranch)
	shas := f.Commits(work, "first change", "second change")
	tree := gitStdout(t, f, f.Dir(), "rev-parse", shas[1]+"^{tree}")

	squash := f.SquashOnOrigin(shas[1], "squash of work", "first paragraph\n\nsecond paragraph")

	assert.Check(t, is.Equal(originBranch(t, f, mainBranch), squash))

	want := trunk + " " + tree + " squash of work"
	assert.Check(t, is.Equal(gitStdout(t, f, f.OriginDir(), "log", "-1", "--format=%P %T %s", squash), want))

	assert.Check(t, is.Equal(gitStdout(t, f, f.OriginDir(), "log", "-1", "--format=%b", squash), "first paragraph\n\nsecond paragraph"))
	assert.Check(t, !hasCommit(t, f, f.Dir(), squash), "the working repository holds %s, want only origin to", squash)
}

func TestFixture_SquashOnOrigin_WhenBodyIsEmpty_WritesSubjectAlone(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")

	squash := f.SquashOnOrigin(shas[0], "squash of work", "")

	raw := callGit(t, f.Env(), f.OriginDir(), "cat-file", "commit", squash)
	assert.Equal(t, raw.Code, 0, "git cat-file commit, stderr: %s", raw.Stderr)
	_, message, _ := strings.Cut(raw.Stdout, "\n\n")
	assert.Check(t, is.Equal(message, "squash of work\n"))
}

// worktrees returns, for each worktree `git worktree list` run in dir names, the branch it has
// checked out, keyed by its directory with symbolic links resolved.
func worktrees(t *testing.T, f *gittest.Fixture, dir string) map[string]string {
	t.Helper()

	got := map[string]string{}
	path := ""

	for line := range strings.Lines(gitStdout(t, f, dir, "worktree", "list", "--porcelain")) {
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

	assert.Check(t, is.Equal(gitStdout(t, f, f.Dir(), "log", "--format=%H", origin+"/"+mainBranch+".."+mainBranch), ahead))

	assert.Check(t, is.Equal(gitStdout(t, f, f.Dir(), "symbolic-ref", "--short", "HEAD"), work))
}

func TestFixture_UntrackedOnTrunk_WhenOnABranch_MakesSwitchingToTheTrunkRefuseToOverwriteIt(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	f.Commits(work, "first change")

	path := f.UntrackedOnTrunk()

	assert.Check(t, is.Equal(gitStdout(t, f, f.Dir(), "ls-files", "--others", "--exclude-standard"), path))

	tracked := runGit(t, f, f.OriginDir(), "cat-file", "-e", mainBranch+":"+path)
	assert.Check(t, is.Equal(tracked.Code, 0), "origin's %s does not track %s", mainBranch, path)
	switched := runGit(t, f, f.Dir(), "switch", mainBranch)
	assert.Check(t, switched.Code != 0, "git switch %s printed %+v, want it refused", mainBranch, switched)
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
	tree := gitStdout(t, f, f.Dir(), "rev-parse", shas[0]+"^{tree}")

	f.LandPull(7, work)

	assert.Check(t, is.Equal(gitStdout(t, f, f.OriginDir(), "rev-parse", "--verify", "refs/pull/7/head"), shas[0]))

	landedTree, subject, ok := strings.Cut(gitStdout(t, f, f.OriginDir(), "log", "-1", "--format=%T%n%s", mainBranch), "\n")
	assert.Assert(t, ok, "origin's %s tip has no subject line", mainBranch)
	assert.Check(t, is.Equal(landedTree, tree))
	assert.Check(t, strings.HasPrefix(subject, "first change"), "subject %q does not start with the branch's subject", subject)
	assert.Check(t, is.Contains(subject, strconv.Itoa(7)))
}

func TestFixture_ServedEnv_WhenOriginURLIsSSH_FetchesFromTheRepositoryAtDir(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")
	f.Publish(work)
	f.SetOriginURL(elsewhere)

	got := callGit(t, f.ServedEnv(f.OriginDir()), f.Dir(), "ls-remote", origin, "refs/heads/"+work)
	assert.Equal(t, got.Code, 0, "git ls-remote, stderr: %s", got.Stderr)

	sha, _, _ := strings.Cut(got.Stdout, "\t")
	assert.Check(t, is.Equal(sha, shas[0]))
}

func TestFixture_ServedEnv_WhenOriginURLIsSSH_PushesToTheRepositoryAtDir(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")
	f.SetOriginURL(elsewhere)

	pushed := callGit(t, f.ServedEnv(f.OriginDir()), f.Dir(), "push", "--quiet", origin, work)
	assert.Equal(t, pushed.Code, 0, "git push, stderr: %s", pushed.Stderr)

	assert.Check(t, is.Equal(originBranch(t, f, work), shas[0]))
}

func TestEnv_WhenProcessEnvironmentCarriesGitSetup_LeavesItOut(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_DIR", t.TempDir())

	env := gittest.Env(t)

	_, configHome := git.EnvValue(env, "XDG_CONFIG_HOME")
	_, gitDir := git.EnvValue(env, "GIT_DIR")

	assert.Check(t, !configHome, "gittest.Env carries XDG_CONFIG_HOME from the process environment")
	assert.Check(t, !gitDir, "gittest.Env carries GIT_DIR from the process environment")
}

func TestEnv_WhenCalled_PutsHomeUnderTheTestTempDir(t *testing.T) {
	t.Parallel()

	parent := filepath.Dir(t.TempDir())

	home, _ := git.EnvValue(gittest.Env(t), "HOME")

	assert.Check(t, isUnder(t, parent, home), "HOME = %q, want a directory under %q", home, parent)
}

func TestNew_WhenFresh_KeepsItsPathsUnderTheTestTempDir(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	parent := filepath.Dir(t.TempDir())

	tests := []struct {
		path func() string
		name string
	}{
		{name: "working repository", path: f.Dir},
		{name: "HOME of Fixture.Env", path: func() string {
			home, _ := git.EnvValue(f.Env(), "HOME")

			return home
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.path()

			assert.Check(t, isUnder(t, parent, got), "%s path = %q, want a directory under %q", tt.name, got, parent)
		})
	}
}

func TestFixture_Git_WhenGitExitsNonZero_FailsTestWithStderr(t *testing.T) {
	t.Parallel()

	spy := failtest.New(t)
	f := gittest.New(spy, mainBranch)
	args := []string{"rev-parse", "--verify", "refs/heads/absent"}

	stderr := runGit(t, f, f.Dir(), args...).Stderr
	assert.Assert(t, stderr != "", "git %v wrote nothing to stderr, want a message to look for", args)

	spy.Run(func() { f.Git(args...) })

	assert.Check(t, spy.Stopped(), "Git(%v) did not stop the test", args)
	assert.Check(t, is.Contains(strings.Join(spy.Messages(), "\n"), stderr))
}
