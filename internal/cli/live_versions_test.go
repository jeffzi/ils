package cli_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

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

func TestCheckLiveTool_WhenToolDoesNotSuitTarget_ReturnsError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		check  func(target, want, out string) error
		name   string
		target string
		want   string
		out    string
		// named are what the check hands testtarget and the error must name: the variable that
		// decides and the versions compared.
		named []string
	}{
		{
			name: "minimum target and a newer git", check: checkLiveGit, target: testtarget.Minimum, out: "git version 99.0.0",
			named: []string{liveGitTargetVar, "99.0.0", git.MinimumVersion},
		},
		{
			name: "version wanted is not the git found", check: checkLiveGit, target: testtarget.Latest, want: "2.56.0",
			out: "git version 2.55.0", named: []string{liveGitVersionVar, "2.55.0", "2.56.0"},
		},
		{
			name: "minimum target and a newer gh", check: checkLiveGh, target: testtarget.Minimum, out: "gh version 99.0.0",
			named: []string{liveGhTargetVar, "99.0.0", github.MinimumVersion},
		},
		{
			name: "version wanted is not the gh found", check: checkLiveGh, target: testtarget.Latest, want: "2.83.2",
			out: "gh version 2.83.1 (2025-11-13)", named: []string{liveGhVersionVar, "2.83.1", "2.83.2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.check(tt.target, tt.want, tt.out)

			assert.Assert(t, err != nil, "check(%q, %q, %q) = nil, want the tool found refused", tt.target, tt.want, tt.out)
			for _, part := range tt.named {
				assert.Check(t, is.ErrorContains(err, part))
			}
		})
	}
}
