package gittest_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // argv is written by the tests in this file
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	code := 0
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		code = exitErr.ExitCode()
	} else {
		assert.NilError(t, err, "git %q", args)
	}

	return faked{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}
}

func TestFakeGit_WhenVersionIsGiven_PrintsItForGitVersion(t *testing.T) {
	const version = "git version 2.34.1\ncpu: x86_64"

	tests := []struct {
		// env builds the environment of the call from the variables gittest.FakeGit returned and
		// the environment from before it; nil inherits this process's.
		env  func(base, settings []string) []string
		name string
	}{
		{name: "inherited environment", env: func([]string, []string) []string { return nil }},
		{name: "whole environment from before the fake", env: func(base, settings []string) []string {
			return append(slices.Clone(base), settings...)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := gittest.Env(t)
			settings := gittest.FakeGit(t, gittest.Fake{Version: version})

			got := callGit(t, tt.env(base, settings), t.TempDir(), "--version")

			assert.Check(t, is.DeepEqual(got, faked{Stdout: version + "\n"}))
		})
	}
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

func TestFakeGit_WhenCallIsNotFaked_RunsTheRealGit(t *testing.T) {
	gittest.FakeGit(t, gittest.Fake{Version: "git version 2.34.1", FailOn: []string{"show-ref"}})
	dir := t.TempDir()

	got := callGit(t, gittest.Env(t), dir, "init", "--quiet")

	assert.Check(t, is.DeepEqual(got, faked{}))
	_, err := os.Stat(filepath.Join(dir, ".git"))
	assert.Check(t, err, "git init made no repository in %s", dir)
}

func TestFakeGit_WhenCallIsAPrefixOfFailingWords_RunsTheRealGit(t *testing.T) {
	gittest.FakeGit(t, gittest.Fake{FailOn: []string{"show-ref", "--verify"}})
	env := gittest.Env(t)
	dir := initRepo(t, env)

	got := callGit(t, env, dir, "show-ref")

	// The real git finds no refs in a fresh repository: it prints nothing and exits 1.
	assert.Check(t, is.DeepEqual(got, faked{Code: 1}))
}

func TestFakeGit_WhenRealGitFails_ExitsWithItsCode(t *testing.T) {
	gittest.FakeGit(t, gittest.Fake{FailOn: []string{"show-ref"}})
	env := gittest.Env(t)
	dir := initRepo(t, env)

	got := callGit(t, env, dir, "remote", "get-url", "absent")

	assert.Check(t, is.Equal(got.Code, 2), "git remote get-url absent printed %+v", got)
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
	assert.Check(t, strings.HasPrefix(got.Stderr, "gittest: "), "git status printed %+v", got)
}

func TestFakeGit_WhenCallStartsWithStallingWords_WaitsAtTheAddressUntilKilled(t *testing.T) {
	addr, connected := gittest.SilentListener(t)
	gittest.FakeGit(t, gittest.Fake{StallOn: []string{"show-ref", "--verify"}, StallAt: addr})
	ctx := gittest.Ending(t.Context(), connected, context.Canceled)
	cmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/main")
	cmd.Dir = t.TempDir()

	err := cmd.Run()

	assert.Check(t, is.ErrorIs(ctx.Err(), context.Canceled), "git show-ref ended with %v before it connected", err)
	assert.Check(t, err != nil, "git show-ref exited 0, want it killed while it waited")
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
