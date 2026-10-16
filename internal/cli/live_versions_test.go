package cli_test

import (
	"fmt"
	"slices"
	"strings"
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
	return liveTool{
		versionOf: git.VersionOf,
		tool: testtarget.Tool{
			Name: "git", TargetVar: liveGitTargetVar, VersionVar: liveGitVersionVar, Minimum: git.MinimumVersion,
		},
	}.check(target, want, out)
}

// checkLiveGh returns why the output of `gh --version` does not suit the live suite, or nil.
func checkLiveGh(target, want, out string) error {
	return liveTool{
		versionOf: github.VersionOf,
		tool: testtarget.Tool{
			Name: "gh", TargetVar: liveGhTargetVar, VersionVar: liveGhVersionVar, Minimum: github.MinimumVersion,
		},
	}.check(target, want, out)
}

// liveTool pairs a tool the live suite needs with the function that reads its version from the
// first line of `<name> --version`.
type liveTool struct {
	versionOf func(line string) string
	tool      testtarget.Tool
}

// check returns why a tool whose `<name> --version` prints out does not suit the live suite, or
// nil when it does. The first line of out must name a version that versionOf reads, and that
// version must suit the target and the version wanted, as testtarget.Tool.Check decides.
func (l liveTool) check(target, want, out string) error {
	line, _, _ := strings.Cut(out, "\n")

	found := l.versionOf(line)
	if found == "" {
		return fmt.Errorf("%s --version printed %q, which names no version the suite can read", l.tool.Name, line)
	}

	return l.tool.Check(target, want, found)
}

func TestCheckLiveTool_WhenToolSuitsTarget_ReturnsNil(t *testing.T) {
	t.Parallel()
	ghMinimum := "gh version " + github.MinimumVersion + " (2024-05-29)"
	tests := []struct {
		name   string
		check  func(target, want, out string) error
		target string
		want   string
		out    string
	}{
		{
			name: "git: minimum target and the minimum", check: checkLiveGit,
			target: "minimum", out: "git version " + git.MinimumVersion,
		},
		{
			name: "git: minimum on the first of several lines", check: checkLiveGit,
			target: "minimum", out: "git version " + git.MinimumVersion + "\ncpu: x86_64",
		},
		{
			name: "gh: minimum target and the minimum", check: checkLiveGh,
			target: "minimum", out: ghMinimum,
		},
		{
			name: "gh: minimum on the first of several lines", check: checkLiveGh,
			target: "minimum", out: ghMinimum + "\nhttps://github.com/cli/cli/releases/tag/v" + github.MinimumVersion,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.check(tt.target, tt.want, tt.out)

			assert.NilError(t, err, "check(%q, %q, %q)", tt.target, tt.want, tt.out)
		})
	}
}

// unsuitedLiveToolCase is a tool output the live suite must reject, with the error it must give.
type unsuitedLiveToolCase struct {
	name   string
	check  func(target, want, out string) error
	target string
	want   string
	out    string
	// err is the text of the error the check returns.
	err string
}

// unsuitedGitCases returns the outputs of `git --version` the live suite rejects.
func unsuitedGitCases() []unsuitedLiveToolCase {
	const (
		gitNotMinimum = "ILS_LIVE_GIT is minimum and the git found is %s, want the minimum ils enforces, 2.34.1"
		gitUnreadable = "git --version printed %q, which names no version the suite can read"
	)

	return []unsuitedLiveToolCase{
		{
			name: "git: minimum target and a newer git", check: checkLiveGit,
			target: "minimum", out: "git version 2.56.0",
			err: fmt.Sprintf(gitNotMinimum, "2.56.0"),
		},
		{
			name: "git: version wanted is not the git found", check: checkLiveGit,
			target: "latest", want: "2.56.0", out: "git version 2.55.0",
			err: "the git found is 2.55.0, and ILS_LIVE_GIT_VERSION wants 2.56.0",
		},
		{
			name: "git: development build", check: checkLiveGit,
			out: "git version DEV",
			err: fmt.Sprintf(gitUnreadable, "git version DEV"),
		},
		{
			name: "git: development build on the first of several lines", check: checkLiveGit,
			out: "git version DEV\ncpu: x86_64",
			err: fmt.Sprintf(gitUnreadable, "git version DEV"),
		},
		{
			name: "git: no output", check: checkLiveGit,
			target: "minimum",
			err:    fmt.Sprintf(gitUnreadable, ""),
		},
	}
}

// unsuitedGhCases returns the outputs of `gh --version` the live suite rejects.
func unsuitedGhCases() []unsuitedLiveToolCase {
	const (
		ghNotMinimum = "ILS_LIVE_GH is minimum and the gh found is %s, want the minimum ils enforces, 2.50.0"
		ghUnreadable = "gh --version printed %q, which names no version the suite can read"
	)

	return []unsuitedLiveToolCase{
		{
			name: "gh: minimum target and a newer gh", check: checkLiveGh,
			target: "minimum", out: "gh version 2.83.2 (2025-12-10)",
			err: fmt.Sprintf(ghNotMinimum, "2.83.2"),
		},
		{
			name: "gh: version wanted is not the gh found", check: checkLiveGh,
			target: "latest", want: "2.83.2", out: "gh version 2.83.1 (2025-11-13)",
			err: "the gh found is 2.83.1, and ILS_LIVE_GH_VERSION wants 2.83.2",
		},
		{
			name: "gh: development build", check: checkLiveGh,
			out: "gh version DEV",
			err: fmt.Sprintf(ghUnreadable, "gh version DEV"),
		},
		{
			name: "gh: development build on the first of several lines", check: checkLiveGh,
			out: "gh version DEV\nhttps://github.com/cli/cli",
			err: fmt.Sprintf(ghUnreadable, "gh version DEV"),
		},
		{
			name: "gh: no output", check: checkLiveGh,
			target: "minimum",
			err:    fmt.Sprintf(ghUnreadable, ""),
		},
	}
}

func TestCheckLiveTool_WhenToolDoesNotSuitTarget_ReturnsError(t *testing.T) {
	t.Parallel()
	tests := slices.Concat(unsuitedGitCases(), unsuitedGhCases())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.check(tt.target, tt.want, tt.out)

			assert.Error(t, err, tt.err, "check(%q, %q, %q)", tt.target, tt.want, tt.out)
		})
	}
}
