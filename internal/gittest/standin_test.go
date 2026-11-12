package gittest_test

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/gittest"
)

// No test here is parallel: the fake git is found through PATH, which gittest.FakeGit sets for the
// whole process.

// faked is what one call of the git first on PATH did.
type faked struct {
	Stdout string
	Stderr string
	Code   int
}

// callGit runs the git first on PATH with args in dir. A nil env inherits this process's
// environment; otherwise env is the call's whole environment.
func callGit(t *testing.T, env []string, dir string, args ...string) faked {
	t.Helper()

	return ranGit(t, gitCommand(t.Context(), env, dir, args...))
}

// gitCommand is the call callGit makes, killed once ctx ends.
func gitCommand(ctx context.Context, env []string, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // argv is written by the tests of this package
	cmd.Dir = dir
	cmd.Env = env

	return cmd
}

// ranGit runs cmd and returns what it did; an exit code other than zero is part of the result.
func ranGit(t *testing.T, cmd *exec.Cmd) faked {
	t.Helper()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	code := 0
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		code = exitErr.ExitCode()
	} else {
		assert.NilError(t, err, "%q", cmd.Args)
	}

	return faked{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}
}

func TestFakeGit_WhenVersionIsGiven_PrintsItForGitVersion(t *testing.T) {
	const version = "git version 2.34.1\ncpu: x86_64"
	base := gittest.Env(t)
	settings := gittest.FakeGit(t, gittest.Fake{Version: version})

	got := callGit(t, append(slices.Clone(base), settings...), t.TempDir(), "--version")

	assert.Check(t, is.DeepEqual(got, faked{Stdout: version + "\n"}))
}

func TestFakeGit_WhenCallStartsWithFailingWords_FailsAsAGitRead(t *testing.T) {
	words := []string{"show-ref", "--verify"}

	tests := []struct {
		name string
		args []string
	}{
		{name: "exactly the words", args: words},
		{name: "the words then more", args: append(slices.Clone(words), "--quiet", "refs/heads/main")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gittest.FakeGit(t, gittest.Fake{FailOn: words})

			got := callGit(t, nil, t.TempDir(), tt.args...)

			want := faked{Stderr: gittest.FailedSaid + "\n", Code: gittest.FailedCode}
			assert.Check(t, is.DeepEqual(got, want))
		})
	}
}

func TestFakeGit_WhenTheLastWordEndsInAStar_MatchesAnArgumentStartingWithWhatComesBefore(t *testing.T) {
	gittest.FakeGit(t, gittest.Fake{FailOn: []string{"rev-parse", "--verify", "refs/ils/fetched/*"}})

	got := callGit(t, nil, t.TempDir(), "rev-parse", "--verify", "refs/ils/fetched/Q7/heads/main")

	want := faked{Stderr: gittest.FailedSaid + "\n", Code: gittest.FailedCode}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestFakeGit_WhenTheLastWordEndsInAStar_RunsTheRealGitForAnArgumentStartingOtherwise(t *testing.T) {
	base := gittest.Env(t)
	dir := initRepo(t, base)
	args := []string{"rev-parse", "--verify", "refs/heads/main"}
	want := callGit(t, base, dir, args...)
	settings := gittest.FakeGit(t, gittest.Fake{FailOn: []string{"rev-parse", "--verify", "refs/ils/fetched/*"}})

	got := callGit(t, slices.Concat(base, settings), dir, args...)

	assert.Check(t, is.DeepEqual(got, want))
}

func TestFakeGit_WhenCallStartsWithWordsThatFailAfterward_RunsTheRealGitThenFails(t *testing.T) {
	f := gittest.New(t, "main")
	settings := gittest.FakeGit(t, gittest.Fake{FailAfterOn: []string{"update-ref"}})

	got := callGit(t, slices.Concat(f.Env(), settings), f.Dir(), "update-ref", "refs/heads/kept", "HEAD")

	want := faked{Stderr: gittest.FailedSaid + "\n", Code: gittest.FailedCode}
	assert.Check(t, is.DeepEqual(got, want))
	assert.Check(t, is.Equal(f.Git("rev-parse", "refs/heads/kept"), f.Git("rev-parse", "HEAD")), "the real git did not run")
}

func TestFakeGit_WhenCallStartsWithAnsweredWords_PrintsTheAnswer(t *testing.T) {
	gittest.FakeGit(t, gittest.Fake{AnswerOn: []string{"rev-list"}, Answer: "not counts"})

	got := callGit(t, nil, t.TempDir(), "rev-list", "--count", "HEAD")

	assert.Check(t, is.DeepEqual(got, faked{Stdout: "not counts\n"}))
}

func TestFakeGit_WhenCallIsNotFaked_RunsTheRealGit(t *testing.T) {
	gittest.FakeGit(t, gittest.Fake{Version: "git version 2.34.1", FailOn: []string{"show-ref"}})
	dir := t.TempDir()

	callGit(t, gittest.Env(t), dir, "init", "--quiet")

	_, err := os.Stat(filepath.Join(dir, ".git"))
	assert.Check(t, err, "git init made no repository in %s", dir)
}

// errStalled ends a call that stalled at the address of a fake it should no longer follow.
var errStalled = errors.New("the call stalled at the address of an earlier fake")

// onlyFirstFakes lists a call for each setting of firstFake, which a second, empty call to FakeGit
// leaves unset.
func onlyFirstFakes() []struct {
	name string
	args []string
} {
	return []struct {
		name string
		args []string
	}{
		{name: "version", args: []string{"--version"}},
		{name: "fail", args: []string{"show-ref"}},
		{name: "stall", args: []string{"rev-parse", "--is-inside-work-tree"}},
		{name: "answer", args: []string{"rev-list", "--all"}},
	}
}

// firstFake fakes every call of onlyFirstFakes, stalling at addr.
func firstFake(addr string) gittest.Fake {
	return gittest.Fake{
		Version:  "git version 0.0.1",
		FailOn:   []string{"show-ref"},
		StallOn:  []string{"rev-parse"},
		StallAt:  addr,
		AnswerOn: []string{"rev-list"},
		Answer:   "not counts",
	}
}

func TestFakeGit_WhenInstalledAgain_StopsFakingWhatOnlyTheFirstCallFaked(t *testing.T) {
	// The calls inherit this process's environment, which FakeGit's own settings are read from, so
	// that environment is cut off from the developer's git setup first.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE"} {
		t.Setenv(key, "")
		assert.NilError(t, os.Unsetenv(key))
	}
	for _, tt := range onlyFirstFakes() {
		t.Run(tt.name, func(t *testing.T) {
			dir := initRepo(t, nil)
			want := callGit(t, nil, dir, tt.args...)
			addr, connected := gittest.SilentListener(t)
			gittest.FakeGit(t, firstFake(addr))
			gittest.FakeGit(t, gittest.Fake{})
			ctx := gittest.Ending(t.Context(), connected, errStalled)

			got := ranGit(t, gitCommand(ctx, nil, dir, tt.args...))

			assert.Check(t, is.DeepEqual(got, want))
		})
	}
}

func TestFakeGit_WhenInstalledAgain_ReturnsSettingsThatOverrideTheFirstCalls(t *testing.T) {
	for _, tt := range onlyFirstFakes() {
		t.Run(tt.name, func(t *testing.T) {
			base := gittest.Env(t)
			dir := initRepo(t, base)
			want := callGit(t, base, dir, tt.args...)
			addr, connected := gittest.SilentListener(t)
			first := gittest.FakeGit(t, firstFake(addr))
			second := gittest.FakeGit(t, gittest.Fake{})
			env := slices.Concat(base, first, second)
			ctx := gittest.Ending(t.Context(), connected, errStalled)

			got := ranGit(t, gitCommand(ctx, env, dir, tt.args...))

			assert.Check(t, is.DeepEqual(got, want))
		})
	}
}

func TestFakeGit_WhenCallIsAPrefixOfFailingWords_RunsTheRealGit(t *testing.T) {
	plainEnv := gittest.Env(t)
	dir := initRepo(t, plainEnv)
	want := callGit(t, plainEnv, dir, "show-ref")
	gittest.FakeGit(t, gittest.Fake{FailOn: []string{"show-ref", "--verify"}})

	got := callGit(t, gittest.Env(t), dir, "show-ref")

	assert.Check(t, is.DeepEqual(got, want))
}

func TestFakeGit_WhenRealGitCannotStart_ExitsBroken(t *testing.T) {
	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}
	notRunnable := t.TempDir()
	path := filepath.Join(notRunnable, name)
	assert.NilError(t, os.WriteFile(path, []byte("not a program\n"), 0o755)) //nolint:gosec // must be executable to be found as git
	t.Setenv("PATH", notRunnable+string(os.PathListSeparator)+os.Getenv("PATH"))
	gittest.FakeGit(t, gittest.Fake{FailOn: []string{"show-ref"}})

	got := callGit(t, nil, t.TempDir(), "status")

	assert.Check(t, is.Equal(got.Code, gittest.BrokenCode), "git status printed %+v", got)
	assert.Check(t, got.Stderr != "", "git status printed %+v", got)
}

func TestFakeGit_WhenCallStartsWithStallingWords_WaitsAtTheAddressUntilKilled(t *testing.T) {
	addr, connected := gittest.SilentListener(t)
	gittest.FakeGit(t, gittest.Fake{StallOn: []string{"show-ref", "--verify"}, StallAt: addr})
	ctx := gittest.Ending(t.Context(), connected, context.Canceled)
	cmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/main")
	cmd.Dir = t.TempDir()
	// A process killed by a signal reports -1 on Unix; on Windows, Kill terminates it with code 1.
	killed := -1
	if runtime.GOOS == "windows" {
		killed = 1
	}

	err := cmd.Run()

	exitErr, ok := errors.AsType[*exec.ExitError](err)
	assert.Assert(t, ok, "git show-ref ended with %v, want it killed while it waited", err)
	assert.Check(t, is.Equal(exitErr.ExitCode(), killed), "git show-ref ended on its own, want it killed while it waited")
}

func TestLinkAs(t *testing.T) {
	const tool = "linked-tool"

	tests := []struct {
		// setup gives the process the PATH it has before the call, and returns it.
		setup func(t *testing.T) string
		// want builds the PATH after the call from the directory the tool is linked in and the
		// PATH before it.
		want func(dir, before string) string
		name string
	}{
		{
			name: "PATH unset",
			setup: func(t *testing.T) string {
				t.Setenv("PATH", "")
				assert.NilError(t, os.Unsetenv("PATH"))

				return ""
			},
			want: func(dir, _ string) string { return dir },
		},
		{
			name: "PATH set",
			setup: func(t *testing.T) string {
				before := t.TempDir()
				t.Setenv("PATH", before)

				return before
			},
			want: func(dir, before string) string { return dir + string(os.PathListSeparator) + before },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.setup(t)

			gittest.LinkAs(t, tool)

			found, err := exec.LookPath(tool)
			assert.NilError(t, err)
			got, want := os.Getenv("PATH"), tt.want(filepath.Dir(found), before)
			assert.Check(t, is.Equal(got, want), "PATH = %q, want %q", got, want)
		})
	}
}

// initRepo makes a repository in a new temporary directory with the git first on PATH, and
// returns its directory.
func initRepo(t *testing.T, env []string) string {
	t.Helper()

	dir := t.TempDir()
	got := callGit(t, env, dir, "init", "--quiet")
	assert.Assert(t, is.DeepEqual(got, faked{}), "git init in %s", dir)

	return dir
}

func TestRaceSetting(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{name: "no options in the environment", env: "", want: "atexit_sleep_ms=0"},
		{name: "options in the environment", env: "halt_on_error=1", want: "halt_on_error=1 atexit_sleep_ms=0"},
	}
	// Not parallel at either level: each row sets GORACE with t.Setenv.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GORACE", tt.env)

			got := gittest.RaceSetting()

			assert.Check(t, is.Equal(got, tt.want))
		})
	}
}

func TestCoverSetting(t *testing.T) {
	coverDirFlag := flag.Lookup("test.gocoverdir")
	if coverDirFlag == nil {
		t.Skip("the test binary has no test.gocoverdir flag")
	}
	was := coverDirFlag.Value.String()
	t.Cleanup(func() { assert.Check(t, coverDirFlag.Value.Set(was)) })
	tests := []struct {
		name string
		dir  string
		want string
	}{
		{name: "coverage collected", dir: "/tmp/cover", want: "GOCOVERDIR=/tmp/cover"},
		{name: "no coverage", dir: "", want: ""},
	}
	// Not parallel at either level: each row sets the flag of the whole test binary.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NilError(t, coverDirFlag.Value.Set(tt.dir))

			got := gittest.CoverSetting()

			assert.Check(t, is.Equal(got, tt.want))
		})
	}
}
