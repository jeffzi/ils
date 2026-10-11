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

	"github.com/google/go-cmp/cmp"

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
	if err != nil {
		t.Fatalf("exec.LookPath(git) error = %v, want nil", err)
	}

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
	if err != nil {
		t.Fatalf("git %v in %s error = %v, stderr: %s", args, dir, err, stderr)
	}

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
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks(%q) error = %v, want nil", parent, err)
	}

	resolvedChild, err := filepath.EvalSymlinks(child)
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks(%q) error = %v, want nil", child, err)
	}

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
			if !strings.Contains(got, want) {
				t.Errorf("ls-remote --symref origin HEAD = %q, want a line %q", got, want)
			}
		})
	}
}

func TestNew_PushesTrunkCommitToBareOrigin(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "develop")

	local := git(t, f, f.Dir(), "rev-parse", "--verify", "refs/heads/develop")

	if got := originBranch(t, f, "develop"); got != local {
		t.Errorf("origin develop = %q, want local tip %q", got, local)
	}

	if got := git(t, f, f.OriginDir(), "rev-parse", "--is-bare-repository"); got != "true" {
		t.Errorf("origin is-bare-repository = %q, want %q", got, "true")
	}
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
	if diff := cmp.Diff(want, got); diff != "" || len(shas) != len(subjects) {
		t.Errorf("Commits(%q, %q) returned %d shas, log mismatch (-want +got):\n%s", work, subjects, len(shas), diff)
	}
}

func TestFixture_Commits_CutsBranchFromTrunk(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	trunk := git(t, f, f.Dir(), "rev-parse", mainBranch)

	shas := f.Commits(work, "first change")

	if got := git(t, f, f.Dir(), "rev-parse", shas[0]+"^"); got != trunk {
		t.Errorf("parent of first %s commit = %q, want trunk tip %q", work, got, trunk)
	}
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
	if diff := cmp.Diff(want, got); diff != "" || len(shas) != 2 {
		t.Errorf("second Commits(%q) returned %d shas, log mismatch (-want +got):\n%s", work, len(shas), diff)
	}
}

func TestFixture_Push_PutsLocalTipOnOrigin(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change", "second change")

	f.Push(work)

	if got := originBranch(t, f, work); got != shas[1] {
		t.Errorf("origin %s = %q, want local tip %q", work, got, shas[1])
	}
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

			if got := git(t, f, f.Dir(), "ls-remote", "--heads", "origin", work); got != "" {
				t.Errorf("ls-remote --heads origin %s = %q, want no such branch", work, got)
			}
		})
	}
}

func TestFixture_AdvanceOrigin_AddsCommitTheCloneLacks(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")
	f.Push(work)

	ahead := f.AdvanceOrigin(work)

	if got := originBranch(t, f, work); got != ahead {
		t.Errorf("origin %s = %q, want returned sha %q", work, got, ahead)
	}

	if got := git(t, f, f.OriginDir(), "rev-parse", ahead+"^"); got != shas[0] {
		t.Errorf("parent of origin's new commit = %q, want local tip %q", got, shas[0])
	}

	if got := git(t, f, f.Dir(), "rev-parse", work); got != shas[0] {
		t.Errorf("local %s = %q, want it left at %q", work, got, shas[0])
	}

	if hasCommit(t, f, f.Dir(), ahead) {
		t.Errorf("clone holds origin's new commit %s, want it not fetched", ahead)
	}
}

func TestFixture_AdvanceOrigin_WhenCalledTwice_StacksSecondCommitOnFirst(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")
	f.Push(work)
	first := f.AdvanceOrigin(work)

	second := f.AdvanceOrigin(work)

	want := strings.Join([]string{shas[0], first, second}, "\n")
	if got := git(t, f, f.OriginDir(), "rev-list", "--reverse", mainBranch+".."+work); got != want {
		t.Errorf("origin %s commits past %s = %q, want %q", work, mainBranch, got, want)
	}
}

func TestFixture_Diverge_GivesEachSideACommitTheOtherLacks(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")
	f.Push(work)

	local, remote := f.Diverge(work)

	if got := git(t, f, f.Dir(), "rev-parse", work); got != local || local == shas[0] {
		t.Errorf("local %s = %q, want new local commit %q past %q", work, got, local, shas[0])
	}

	if got := originBranch(t, f, work); got != remote || remote == shas[0] {
		t.Errorf("origin %s = %q, want new origin commit %q past %q", work, got, remote, shas[0])
	}

	if hasCommit(t, f, f.Dir(), remote) {
		t.Errorf("clone holds origin's commit %s, want it not fetched", remote)
	}

	if hasCommit(t, f, f.OriginDir(), local) {
		t.Errorf("origin holds local commit %s, want it not pushed", local)
	}
}

func TestFixture_Detach_LeavesHeadDetachedAtSameCommit(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")

	f.Detach()

	if _, _, err := runGit(t, f, f.Dir(), "symbolic-ref", "--quiet", "HEAD"); err == nil {
		t.Error("symbolic-ref HEAD succeeded, want HEAD detached")
	}

	if got := git(t, f, f.Dir(), "rev-parse", "HEAD"); got != shas[0] {
		t.Errorf("HEAD = %q, want %q", got, shas[0])
	}
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
			if got != want {
				t.Errorf("status --porcelain = %q, want %q", got, want)
			}
		})
	}
}

func TestFixture_Sibling_SharesFirstCommitPastTrunk(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change", "second change")

	tip := f.Sibling("other", work)

	if got := git(t, f, f.Dir(), "rev-parse", "--verify", "refs/heads/other"); got != tip {
		t.Errorf("local other = %q, want returned tip %q", got, tip)
	}

	if got := git(t, f, f.Dir(), "merge-base", work, "other"); got != shas[0] {
		t.Errorf("merge-base %s other = %q, want first %s commit %q", work, got, work, shas[0])
	}

	if tip == shas[0] {
		t.Errorf("other tip = %q, want a commit of its own past %q", tip, shas[0])
	}
}

func TestFixture_SetPushURL_LeavesFetchURLUnchanged(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	fetch := git(t, f, f.Dir(), "remote", "get-url", "origin")
	const push = "git@example.com:owner/fork.git"

	f.SetPushURL(push)

	if got := git(t, f, f.Dir(), "remote", "get-url", "--push", "origin"); got != push {
		t.Errorf("origin push URL = %q, want %q", got, push)
	}

	if got := git(t, f, f.Dir(), "remote", "get-url", "origin"); got != fetch {
		t.Errorf("origin fetch URL = %q, want it unchanged at %q", got, fetch)
	}
}

func TestFixture_SetOriginURL_StoresURLVerbatim(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	const url = "ssh://git@example.com:2222/owner/project.git"

	f.SetOriginURL(url)

	if got := git(t, f, f.Dir(), "remote", "get-url", "origin"); got != url {
		t.Errorf("origin URL = %q, want %q", got, url)
	}
}

func TestFixture_RemoveOrigin_LeavesNoRemoteNamedOrigin(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)

	f.RemoveOrigin()

	if got := git(t, f, f.Dir(), "remote"); got != "" {
		t.Errorf("git remote = %q, want no remotes", got)
	}
}

func TestFixture_PullRef_PointsOriginPullHeadAtCommit(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	shas := f.Commits(work, "first change")

	f.PullRef(7, shas[0])

	if got := git(t, f, f.OriginDir(), "rev-parse", "--verify", "refs/pull/7/head"); got != shas[0] {
		t.Errorf("origin refs/pull/7/head = %q, want %q", got, shas[0])
	}
}

func TestFixture_SquashOnOrigin_AddsTrunkCommitTheCloneLacks(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	trunk := git(t, f, f.Dir(), "rev-parse", mainBranch)
	shas := f.Commits(work, "first change", "second change")
	tree := git(t, f, f.Dir(), "rev-parse", shas[1]+"^{tree}")

	squash := f.SquashOnOrigin(shas[1], "squash of work")

	if got := originBranch(t, f, mainBranch); got != squash {
		t.Errorf("origin main = %q, want returned sha %q", got, squash)
	}

	want := trunk + " " + tree + " squash of work"
	if got := git(t, f, f.OriginDir(), "log", "-1", "--format=%P %T %s", squash); got != want {
		t.Errorf("origin squash commit parent, tree, subject = %q, want %q", got, want)
	}

	if hasCommit(t, f, f.Dir(), squash) {
		t.Errorf("clone holds origin's squash commit %s, want it not fetched", squash)
	}
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

			if !ok || got != tt.want {
				t.Errorf("Env() %s = %q (set %t), want %q", tt.key, got, ok, tt.want)
			}
		})
	}
}

func TestFixture_Commits_CarryFixedAuthorAndCommitterDate(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)
	date, err := time.Parse(time.RFC3339, fixedDate)
	if err != nil {
		t.Fatalf("time.Parse(%q) error = %v, want nil", fixedDate, err)
	}

	shas := f.Commits(work, "first change")

	want := fmt.Sprintf("%[1]d %[1]d", date.Unix())
	if got := git(t, f, f.Dir(), "log", "-1", "--format=%at %ct", shas[0]); got != want {
		t.Errorf("author and committer time of fixture commit = %q, want %q", got, want)
	}
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

			if got == "" || !isUnder(t, parent, got) {
				t.Errorf("%s path = %q, want a directory under %q", tt.name, got, parent)
			}
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

			if err != nil || got != tt.want {
				t.Errorf("config --local --get %s = %q (error %v), want %q", tt.key, got, err, tt.want)
			}
		})
	}
}

func TestFixture_Commits_WhenProcessEnvironmentSetsIdentityAndSigning_AreUnsignedWithFixtureAuthor(t *testing.T) {
	config := filepath.Join(t.TempDir(), "gitconfig")

	err := os.WriteFile(config, []byte("[commit]\n\tgpgsign = true\n[user]\n\tname = Someone Else\n"), 0o600)
	if err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v, want nil", config, err)
	}

	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_AUTHOR_NAME", "Someone Else")
	t.Setenv("GIT_AUTHOR_EMAIL", "someone@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Someone Else")
	t.Setenv("GIT_COMMITTER_EMAIL", "someone@example.com")

	f := gittest.New(t, mainBranch)

	shas := f.Commits(work, "first change")

	want := fmt.Sprintf("%[1]s <%[2]s> %[1]s <%[2]s> N", gittest.AuthorName, gittest.AuthorEmail)
	if got := git(t, f, f.Dir(), "log", "-1", "--format=%an <%ae> %cn <%ce> %G?", shas[0]); got != want {
		t.Errorf("author, committer, signature of fixture commit = %q, want %q", got, want)
	}
}

func TestFixture_Git_ReturnsTrimmedStdoutFromWorkingRepository(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "develop")

	got := f.Git("rev-parse", "--abbrev-ref", "HEAD")

	if got != "develop" {
		t.Errorf("Git(rev-parse --abbrev-ref HEAD) = %q, want %q", got, "develop")
	}
}

func TestFixture_OriginGit_ReturnsTrimmedStdoutFromOrigin(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, mainBranch)

	got := f.OriginGit("rev-parse", "--is-bare-repository")

	if got != "true" {
		t.Errorf("OriginGit(rev-parse --is-bare-repository) = %q, want %q", got, "true")
	}
}

func TestFixture_Git_WhenGitExitsNonZero_FailsTestWithStderr(t *testing.T) {
	t.Parallel()

	spy := &spyTB{TB: t}
	f := gittest.New(spy, mainBranch)
	args := []string{"rev-parse", "--verify", "refs/heads/absent"}

	_, stderr, _ := runGit(t, f, f.Dir(), args...)
	if stderr == "" {
		t.Fatalf("git %v wrote nothing to stderr, want a message to look for", args)
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		f.Git(args...)
	}()
	<-done

	if got := strings.Join(spy.messages, "\n"); !spy.stopped || !strings.Contains(got, stderr) {
		t.Errorf("Git(%v) stopped the test = %t with messages %q, want a stop carrying stderr %q",
			args, spy.stopped, got, stderr)
	}
}
