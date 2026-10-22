package cli_test

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/testtarget"
)

// The live suite runs only under the live build tag; the decisions about the git and the gh it
// finds live here, outside the tag, so that the normal suite pins them.

const (
	// liveGhTargetVar says which gh the live suite must find: testtarget.Minimum, testtarget.Latest,
	// or unset for latest.
	liveGhTargetVar = "ILS_LIVE_GH"
	// liveGhVersionVar, when set, is the exact version of gh the live suite must find.
	liveGhVersionVar = "ILS_LIVE_GH_VERSION"
	// liveGitTargetVar says which git the live suite must find: testtarget.Minimum,
	// testtarget.Latest, or unset for latest.
	liveGitTargetVar = "ILS_LIVE_GIT"
	// liveGitVersionVar, when set, is the exact version of git the live suite must find.
	liveGitVersionVar = "ILS_LIVE_GIT_VERSION"
)

// checkLiveGit returns why the output of `git --version` does not suit the live suite, or nil.
func checkLiveGit(target, want, out string) error {
	tool := testtarget.Tool{
		Name: "git", TargetVar: liveGitTargetVar, VersionVar: liveGitVersionVar, MinimumVersion: git.MinimumVersion,
	}

	return tool.CheckOutput(target, want, out, git.VersionOf)
}

// checkLiveGh returns why the output of `gh --version` does not suit the live suite, or nil.
func checkLiveGh(target, want, out string) error {
	tool := testtarget.Tool{
		Name: "gh", TargetVar: liveGhTargetVar, VersionVar: liveGhVersionVar, MinimumVersion: github.MinimumVersion,
	}

	return tool.CheckOutput(target, want, out, github.VersionOf)
}

func TestCheckLiveGit_WhenGitIsTheMinimum_ReturnsNil(t *testing.T) {
	t.Parallel()
	out := "git version " + git.MinimumVersion

	err := checkLiveGit(testtarget.Minimum, "", out)

	assert.NilError(t, err)
}

func TestCheckLiveGit_WhenGitDoesNotSuitTarget_ReturnsError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target string
		want   string
		out    string
		err    string
	}{
		{
			name: "minimum target and a newer git", target: testtarget.Minimum, out: "git version 2.56.0",
			err: "ILS_LIVE_GIT is minimum and the git found is 2.56.0, want the minimum ils enforces, 2.34.1",
		},
		{
			name: "version wanted is not the git found", target: testtarget.Latest, want: "2.56.0",
			out: "git version 2.55.0", err: "the git found is 2.55.0, and ILS_LIVE_GIT_VERSION wants 2.56.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkLiveGit(tt.target, tt.want, tt.out)

			assert.Error(t, err, tt.err, "checkLiveGit(%q, %q, %q)", tt.target, tt.want, tt.out)
		})
	}
}

func TestCheckLiveGh_WhenGhIsTheMinimum_ReturnsNil(t *testing.T) {
	t.Parallel()
	out := "gh version " + github.MinimumVersion + " (2024-05-29)"

	err := checkLiveGh(testtarget.Minimum, "", out)

	assert.NilError(t, err)
}

func TestCheckLiveGh_WhenGhDoesNotSuitTarget_ReturnsError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target string
		want   string
		out    string
		err    string
	}{
		{
			name: "minimum target and a newer gh", target: testtarget.Minimum, out: "gh version 2.83.2 (2025-12-10)",
			err: "ILS_LIVE_GH is minimum and the gh found is 2.83.2, want the minimum ils enforces, 2.50.0",
		},
		{
			name: "version wanted is not the gh found", target: testtarget.Latest, want: "2.83.2",
			out: "gh version 2.83.1 (2025-11-13)", err: "the gh found is 2.83.1, and ILS_LIVE_GH_VERSION wants 2.83.2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkLiveGh(tt.target, tt.want, tt.out)

			assert.Error(t, err, tt.err, "checkLiveGh(%q, %q, %q)", tt.target, tt.want, tt.out)
		})
	}
}
