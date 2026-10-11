package gittest_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/gittest"
)

const (
	mainBranch = "main"
	work       = "work"
	fixedDate  = "2001-02-03T04:05:06Z"
)

// spyTB records failures reported through it instead of failing the real test.
type spyTB struct {
	testing.TB

	messages []string
	stopped  bool
}

func (s *spyTB) Log(args ...any) { s.messages = append(s.messages, fmt.Sprint(args...)) }

func (s *spyTB) Logf(format string, args ...any) {
	s.messages = append(s.messages, fmt.Sprintf(format, args...))
}

func (s *spyTB) Error(args ...any) { s.Log(args...) }

func (s *spyTB) Errorf(format string, args ...any) { s.Logf(format, args...) }

func (s *spyTB) Fatal(args ...any) {
	s.Log(args...)
	s.FailNow()
}

func (s *spyTB) Fatalf(format string, args ...any) {
	s.Logf(format, args...)
	s.FailNow()
}

func (s *spyTB) FailNow() {
	s.stopped = true

	runtime.Goexit()
}

// runGit runs git in dir with the fixture's environment, independent of the fixture's own runner.
func runGit(t *testing.T, f *gittest.Fixture, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	path, err := exec.LookPath("git")
	assert.NilError(t, err)

	var out, errOut bytes.Buffer

	cmd := exec.CommandContext(t.Context(), path, args...) //nolint:gosec // argv is written by the tests in this file
	cmd.Dir = dir
	cmd.Env = f.Env()
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err = cmd.Run()

	return strings.TrimRight(out.String(), "\r\n"), strings.TrimSpace(errOut.String()), err
}

// git runs git in dir and fails the test when git exits non-zero.
func git(t *testing.T, f *gittest.Fixture, dir string, args ...string) string {
	t.Helper()

	stdout, stderr, err := runGit(t, f, dir, args...)
	assert.NilError(t, err, "git %v in %s, stderr: %s", args, dir, stderr)

	return stdout
}

// hasCommit reports whether the repository at dir holds the commit sha.
func hasCommit(t *testing.T, f *gittest.Fixture, dir, sha string) bool {
	t.Helper()

	_, _, err := runGit(t, f, dir, "cat-file", "-e", sha+"^{commit}")

	return err == nil
}

// originBranch returns the sha origin holds for branch.
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

	resolvedParent, err := filepath.EvalSymlinks(parent)
	assert.NilError(t, err)

	resolvedChild, err := filepath.EvalSymlinks(child)
	assert.NilError(t, err)

	rel, err := filepath.Rel(resolvedParent, resolvedChild)

	return err == nil && filepath.IsLocal(rel)
}

func TestNew_OriginHeadNamesTrunk(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		trunk string
	}{
		{name: "main", trunk: "main"},
		{name: "trunk", trunk: "trunk"},
		{name: "develop", trunk: "develop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := gittest.New(t, tt.trunk)

			got := git(t, f, f.Dir(), "ls-remote", "--symref", "origin", "HEAD")

			want := "ref: refs/heads/" + tt.trunk + "\tHEAD"
			assert.Check(t, is.Contains(got, want))
		})
	}
}

func TestNew_PushesTrunkCommitToBareOrigin(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "develop")

	local := git(t, f, f.Dir(), "rev-parse", "--verify", "refs/heads/develop")

	assert.Check(t, is.Equal(originBranch(t, f, "develop"), local))

	assert.Check(t, is.Equal(git(t, f, f.OriginDir(), "rev-parse", "--is-bare-repository"), "true"))
}

func TestFixture_Commits_ReturnsShasOldestFirstWithGivenSubjects(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	subjects := []string{"first change", "second change", "third change"}

	shas := f.Commits(work, subjects...)

	want := make([]string, 0, len(subjects))
	for i, subject := range subjects {
		if i < len(shas) {
			want = append(want, shas[i]+" "+subject)
		}
	}

	got := strings.Split(git(t, f, f.Dir(), "log", "--reverse", "--format=%H %s", mainBranch+".."+work), "\n")
	assert.Check(t, is.Len(shas, len(subjects)))
	assert.Check(t, is.DeepEqual(got, want))
}

func TestFixture_Commits_CutsBranchFromTrunk(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	trunk := git(t, f, f.Dir(), "rev-parse", mainBranch)

	shas := f.Commits(work, "first change")

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "rev-parse", shas[0]+"^"), trunk))
}

func TestFixture_Commits_WhenBranchExists_AddsCommitsOnTop(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	earlier := f.Commits(work, "first change")

	shas := f.Commits(work, "second change", "third change")

	want := []string{earlier[0] + " first change"}
	for i, subject := range []string{"second change", "third change"} {
		if i < len(shas) {
			want = append(want, shas[i]+" "+subject)
		}
	}

	got := strings.Split(git(t, f, f.Dir(), "log", "--reverse", "--format=%H %s", mainBranch+".."+work), "\n")
	assert.Check(t, is.Len(shas, 2))
	assert.Check(t, is.DeepEqual(got, want))
}

func TestFixture_Push_PutsLocalTipOnOrigin(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change", "second change")

	f.Push(work)

	assert.Check(t, is.Equal(originBranch(t, f, work), shas[1]))
}

func TestFixture_OriginLacksBranch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		arrange func(f *gittest.Fixture)
		name    string
	}{
		{name: "never pushed", arrange: func(*gittest.Fixture) {}},
		{name: "pushed then removed from origin", arrange: func(f *gittest.Fixture) {
			f.Push(work)
			f.RemoveFromOrigin(work)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := gittest.New(t, mainBranch)
			f.Commits(work, "first change")

			tt.arrange(f)

			assert.Check(t, is.Equal(git(t, f, f.Dir(), "ls-remote", "--heads", "origin", work), ""))
		})
	}
}

func TestFixture_AdvanceOrigin_AddsCommitTheCloneLacks(t *testing.T) {
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

func TestFixture_Diverge_GivesEachSideACommitTheOtherLacks(t *testing.T) {
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

func TestFixture_Detach_LeavesHeadDetachedAtSameCommit(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")

	f.Detach()

	_, _, err := runGit(t, f, f.Dir(), "symbolic-ref", "--quiet", "HEAD")
	assert.Check(t, err != nil, "symbolic-ref HEAD succeeded, want HEAD detached")

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "rev-parse", "HEAD"), shas[0]))
}

func TestFixture_WorkingTreeStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		act    func(f *gittest.Fixture) string
		name   string
		status string
	}{
		{name: "dirty tracked file", act: (*gittest.Fixture).DirtyFile, status: " M "},
		{name: "staged change", act: (*gittest.Fixture).StagedFile, status: "M  "},
		{name: "file git does not track", act: (*gittest.Fixture).UntrackedFile, status: "?? "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := gittest.New(t, mainBranch)

			path := tt.act(f)

			want := tt.status + path

			got := git(t, f, f.Dir(), "status", "--porcelain=v1", "--untracked-files=all")
			assert.Check(t, is.Equal(got, want))
		})
	}
}

func TestFixture_Sibling_SharesFirstCommitPastTrunk(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change", "second change")

	tip := f.Sibling("other", work)

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "rev-parse", "--verify", "refs/heads/other"), tip))

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "merge-base", work, "other"), shas[0]))

	assert.Check(t, tip != shas[0], "other tip = %q, want a commit of its own", tip)
}

func TestFixture_SetPushURL_LeavesFetchURLUnchanged(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	fetch := git(t, f, f.Dir(), "remote", "get-url", "origin")
	const push = "git@example.com:owner/fork.git"

	f.SetPushURL(push)

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "remote", "get-url", "--push", "origin"), push))

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "remote", "get-url", "origin"), fetch))
}

func TestFixture_SetOriginURL_StoresURLVerbatim(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	const url = "ssh://git@example.com:2222/owner/project.git"

	f.SetOriginURL(url)

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "remote", "get-url", "origin"), url))
}

func TestFixture_RemoveOrigin_LeavesNoRemoteNamedOrigin(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)

	f.RemoveOrigin()

	assert.Check(t, is.Equal(git(t, f, f.Dir(), "remote"), ""))
}

func TestFixture_PullRef_PointsOriginPullHeadAtCommit(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")

	f.PullRef(7, shas[0])

	assert.Check(t, is.Equal(git(t, f, f.OriginDir(), "rev-parse", "--verify", "refs/pull/7/head"), shas[0]))
}

func TestFixture_SquashOnOrigin_AddsTrunkCommitTheCloneLacks(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	trunk := git(t, f, f.Dir(), "rev-parse", mainBranch)
	shas := f.Commits(work, "first change", "second change")
	tree := git(t, f, f.Dir(), "rev-parse", shas[1]+"^{tree}")

	squash := f.SquashOnOrigin(shas[1], "squash of work")

	assert.Check(t, is.Equal(originBranch(t, f, mainBranch), squash))

	want := trunk + " " + tree + " squash of work"
	assert.Check(t, is.Equal(git(t, f, f.OriginDir(), "log", "-1", "--format=%P %T %s", squash), want))

	assert.Check(t, !hasCommit(t, f, f.Dir(), squash))
}

func TestFixture_Env_PointsGitAwayFromUserConfig(t *testing.T) {
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
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()

			f := gittest.New(t, mainBranch)

			got, ok := envValue(f.Env(), tt.key)

			assert.Check(t, ok, "Env() does not set %s", tt.key)
			assert.Check(t, is.Equal(got, tt.want), "Env() %s", tt.key)
		})
	}
}

func TestFixture_Commits_CarryFixedAuthorAndCommitterDate(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	date, err := time.Parse(time.RFC3339, fixedDate)
	assert.NilError(t, err)

	shas := f.Commits(work, "first change")

	want := fmt.Sprintf("%[1]d %[1]d", date.Unix())
	assert.Check(t, is.Equal(git(t, f, f.Dir(), "log", "-1", "--format=%at %ct", shas[0]), want))
}

func TestFixture_KeepsEveryPathUnderTempDir(t *testing.T) {
	t.Parallel()

	fromEnv := func(key string) func(f *gittest.Fixture) string {
		return func(f *gittest.Fixture) string {
			value, _ := envValue(f.Env(), key)

			return value
		}
	}
	tests := []struct {
		path func(f *gittest.Fixture) string
		name string
	}{
		{name: "working repository", path: (*gittest.Fixture).Dir},
		{name: "bare origin", path: (*gittest.Fixture).OriginDir},
		{name: "HOME", path: fromEnv("HOME")},
		{name: "USERPROFILE", path: fromEnv("USERPROFILE")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := gittest.New(t, mainBranch)
			parent := filepath.Dir(t.TempDir())

			got := tt.path(f)

			assert.Check(t, got != "", "%s path is empty", tt.name)
			assert.Check(t, isUnder(t, parent, got), "%s path = %q, want a directory under %q", tt.name, got, parent)
		})
	}
}

func TestNew_SetsIdentityAndSigningInLocalConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key  string
		want string
	}{
		{key: "user.name", want: gittest.AuthorName},
		{key: "user.email", want: gittest.AuthorEmail},
		{key: "commit.gpgsign", want: "false"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()

			f := gittest.New(t, mainBranch)

			got, _, err := runGit(t, f, f.Dir(), "config", "--local", "--get", tt.key)

			assert.Check(t, err, "config --local --get %s", tt.key)
			assert.Check(t, is.Equal(got, tt.want), "config --local --get %s", tt.key)
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

func TestFixture_Git_ReturnsTrimmedStdoutFromWorkingRepository(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "develop")

	got := f.Git("rev-parse", "--abbrev-ref", "HEAD")

	assert.Check(t, is.Equal(got, "develop"))
}

func TestFixture_OriginGit_ReturnsTrimmedStdoutFromOrigin(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)

	got := f.OriginGit("rev-parse", "--is-bare-repository")

	assert.Check(t, is.Equal(got, "true"))
}

func TestFixture_Git_WhenGitExitsNonZero_FailsTestWithStderr(t *testing.T) {
	t.Parallel()

	spy := &spyTB{TB: t}
	f := gittest.New(spy, mainBranch)
	args := []string{"rev-parse", "--verify", "refs/heads/absent"}

	_, stderr, _ := runGit(t, f, f.Dir(), args...)
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
