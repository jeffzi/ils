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

func originBranch(t *testing.T, f *gittest.Fixture, branch string) string {
	t.Helper()

	return gitStdout(t, f, f.OriginDir(), "rev-parse", "--verify", "refs/heads/"+branch)
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

func TestFixture_Sibling_WhenBranchHasTwoCommits_SharesFirstCommitPastTrunk(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change", "second change")

	tip := f.Sibling("other", work)

	assert.Check(t, is.Equal(gitStdout(t, f, f.Dir(), "rev-parse", "--verify", "refs/heads/other"), tip))
	assert.Check(t, is.Equal(gitStdout(t, f, f.Dir(), "merge-base", work, "other"), shas[0]))
	assert.Check(t, tip != shas[0], "other tip = %q, want a commit of its own", tip)
}

const sshHost = "work"

const aliasedHost = "git.example.org"

const aliases = "Host " + sshHost + "\n  HostName " + aliasedHost + "\n"

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

	assert.Check(t, is.Equal(hostnameIn(got.Stdout), aliasedHost))
	_, statErr := os.Stat(marker)
	assert.Check(t, is.ErrorIs(statErr, fs.ErrNotExist), "the serving script ran for a config read")
}

func TestFixture_SquashOnOrigin_WhenBodyIsGiven_AddsTrunkCommitOnOrigin(t *testing.T) {
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

func TestFixture_AdvanceTrunk_WhenOnABranch_AddsALocalTrunkCommitWithoutSwitchingToIt(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	f.Commits(work, "first change")

	ahead := f.AdvanceTrunk()

	assert.Check(t, is.Equal(gitStdout(t, f, f.Dir(), "log", "--format=%H", origin+"/"+mainBranch+".."+mainBranch), ahead))
	assert.Check(t, is.Equal(gitStdout(t, f, f.Dir(), "symbolic-ref", "--short", "HEAD"), work))
}

const pullNumber = 7

func TestFixture_LandPull_WhenBranchHasACommit_SquashesTheCommitOntoOriginTrunkAsThePullRequest(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")

	f.LandPull(pullNumber, work)

	assert.Check(t, is.Equal(gitStdout(t, f, f.OriginDir(), "rev-parse", "--verify", "refs/pull/"+strconv.Itoa(pullNumber)+"/head"), shas[0]))
	subject := gitStdout(t, f, f.OriginDir(), "log", "-1", "--format=%s", mainBranch)
	assert.Check(t, strings.HasPrefix(subject, "first change"), "subject %q does not start with the branch's subject", subject)
	assert.Check(t, is.Contains(subject, strconv.Itoa(pullNumber)))
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
	t.Setenv("GIT_DIR", t.TempDir())

	env := gittest.Env(t)

	_, hasGitDir := git.EnvValue(env, "GIT_DIR")
	assert.Check(t, !hasGitDir, "gittest.Env carries GIT_DIR from the process environment")
}

func TestFixture_Git_WhenGitExitsNonZero_FailsTestWithStderr(t *testing.T) {
	t.Parallel()

	spy := failtest.New(t)
	args := []string{"rev-parse", "--verify", "refs/heads/absent"}
	var f *gittest.Fixture

	spy.Run(func() {
		f = gittest.New(spy, mainBranch)
		f.Git(args...)
	})

	assert.Assert(t, f != nil, "the fixture's setup stopped the test: %q", spy.Messages())
	stderr := runGit(t, f, f.Dir(), args...).Stderr
	assert.Assert(t, stderr != "", "git %v wrote nothing to stderr, want a message to look for", args)
	assert.Check(t, spy.Stopped(), "Git(%v) did not stop the test", args)
	assert.Check(t, is.Contains(strings.Join(spy.Messages(), "\n"), stderr))
}
