package cli_test

import (
	"fmt"
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

// checkLiveGit returns why a git whose `git --version` prints out does not suit the live suite, or
// nil when it does. Its first line must name a version that ils reads, and that version must suit
// the target and the version wanted, as testtarget.Tool.Check decides.
func checkLiveGit(target, want, out string) error {
	tool := testtarget.Tool{
		Name: "git", TargetVar: liveGitTargetVar, VersionVar: liveGitVersionVar, Minimum: git.MinimumVersion,
	}

	found, err := liveVersion(tool.Name, git.VersionOf, out)
	if err != nil {
		return err
	}

	return tool.Check(target, want, found)
}

// checkLiveGh returns why a gh whose `gh --version` prints out does not suit the live suite, or nil
// when it does. Its first line must name a version that ils reads, and that version must suit the
// target and the version wanted, as testtarget.Tool.Check decides.
func checkLiveGh(target, want, out string) error {
	tool := testtarget.Tool{
		Name: "gh", TargetVar: liveGhTargetVar, VersionVar: liveGhVersionVar, Minimum: github.MinimumVersion,
	}

	found, err := liveVersion(tool.Name, github.VersionOf, out)
	if err != nil {
		return err
	}

	return tool.Check(target, want, found)
}

// liveVersion returns the version that versionOf finds on the first line of out, the output of
// `<name> --version`, or an error quoting that line when it names none.
func liveVersion(name string, versionOf func(line string) string, out string) (string, error) {
	line, _, _ := strings.Cut(out, "\n")

	found := versionOf(line)
	if found == "" {
		return "", fmt.Errorf("%s --version printed %q, which names no version the suite can read", name, line)
	}

	return found, nil
}

func TestCheckLiveGit_WhenGitSuitsTarget_ReturnsNil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target string
		want   string
		out    string
	}{
		{name: "minimum target and the minimum", target: "minimum", out: "git version " + git.MinimumVersion},
		{name: "latest target and the minimum", target: "latest", out: "git version " + git.MinimumVersion},
		{name: "unset target and a newer git", out: "git version 2.54.0"},
		{name: "version wanted is the git found", target: "latest", want: "2.56.0", out: "git version 2.56.0"},
		{name: "minimum on the first of several lines", target: "minimum", out: "git version " + git.MinimumVersion + "\ncpu: x86_64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkLiveGit(tt.target, tt.want, tt.out)

			assert.NilError(t, err, "checkLiveGit(%q, %q, %q)", tt.target, tt.want, tt.out)
		})
	}
}

// unsuitedLiveTool is a git or gh the live suite must refuse, with the target and version it was
// asked for.
type unsuitedLiveTool struct {
	name   string
	target string
	want   string
	out    string
	// err is the text of the error the check of that tool returns.
	err string
}

func unsuitedLiveGits() []unsuitedLiveTool {
	const (
		notMinimum = "ILS_LIVE_GIT is minimum and the git found is %s, want the minimum ils enforces, 2.34.1"
		older      = "the git found is %s, older than the minimum ils enforces, 2.34.1"
		unreadable = "git --version printed %q, which names no version the suite can read"
	)

	return []unsuitedLiveTool{
		{
			name: "minimum target and a newer git", target: "minimum", out: "git version 2.56.0",
			err: fmt.Sprintf(notMinimum, "2.56.0"),
		},
		{
			name: "minimum target and an older git", target: "minimum", out: "git version 2.30.2",
			err: fmt.Sprintf(notMinimum, "2.30.2"),
		},
		{
			name: "latest target and an older git", target: "latest", out: "git version 2.30.2",
			err: fmt.Sprintf(older, "2.30.2"),
		},
		{
			name: "unset target and an older git", out: "git version 2.30.2",
			err: fmt.Sprintf(older, "2.30.2"),
		},
		{
			name: "version wanted is not the git found", target: "latest", want: "2.56.0", out: "git version 2.55.0",
			err: "the git found is 2.55.0, and ILS_LIVE_GIT_VERSION wants 2.56.0",
		},
		{
			name: "development build", out: "git version DEV",
			err: fmt.Sprintf(unreadable, "git version DEV"),
		},
		{
			name: "development build on the first of several lines", out: "git version DEV\ncpu: x86_64",
			err: fmt.Sprintf(unreadable, "git version DEV"),
		},
		{
			name: "no output", target: "minimum",
			err: fmt.Sprintf(unreadable, ""),
		},
		{
			name: "unknown target", target: "newest", out: "git version 2.56.0",
			err: `ILS_LIVE_GIT is "newest", want minimum, latest, or unset`,
		},
	}
}

func TestCheckLiveGit_WhenGitDoesNotSuitTarget_ReturnsError(t *testing.T) {
	t.Parallel()
	for _, tt := range unsuitedLiveGits() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkLiveGit(tt.target, tt.want, tt.out)

			assert.Error(t, err, tt.err, "checkLiveGit(%q, %q, %q)", tt.target, tt.want, tt.out)
		})
	}
}

func TestCheckLiveGh_WhenGhSuitsTarget_ReturnsNil(t *testing.T) {
	t.Parallel()
	minimum := "gh version " + github.MinimumVersion + " (2024-05-29)"
	tests := []struct {
		name   string
		target string
		want   string
		out    string
	}{
		{name: "minimum target and the minimum", target: "minimum", out: minimum},
		{name: "latest target and the minimum", target: "latest", out: minimum},
		{name: "latest target and a newer gh", target: "latest", out: "gh version 2.83.2 (2025-12-10)"},
		{name: "unset target and a newer gh", out: "gh version 2.83.2 (2025-12-10)"},
		{name: "version wanted is the gh found", target: "latest", want: "2.83.2", out: "gh version 2.83.2 (2025-12-10)"},
		{
			name: "minimum on the first of several lines", target: "minimum",
			out: minimum + "\nhttps://github.com/cli/cli/releases/tag/v" + github.MinimumVersion,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkLiveGh(tt.target, tt.want, tt.out)

			assert.NilError(t, err, "checkLiveGh(%q, %q, %q)", tt.target, tt.want, tt.out)
		})
	}
}

func unsuitedLiveGhs() []unsuitedLiveTool {
	const (
		notMinimum = "ILS_LIVE_GH is minimum and the gh found is %s, want the minimum ils enforces, 2.50.0"
		older      = "the gh found is %s, older than the minimum ils enforces, 2.50.0"
		unreadable = "gh --version printed %q, which names no version the suite can read"
	)

	return []unsuitedLiveTool{
		{
			name: "minimum target and a newer gh", target: "minimum", out: "gh version 2.83.2 (2025-12-10)",
			err: fmt.Sprintf(notMinimum, "2.83.2"),
		},
		{
			name: "minimum target and an older gh", target: "minimum", out: "gh version 2.40.1 (2023-12-13)",
			err: fmt.Sprintf(notMinimum, "2.40.1"),
		},
		{
			name: "latest target and an older gh", target: "latest", out: "gh version 2.40.1 (2023-12-13)",
			err: fmt.Sprintf(older, "2.40.1"),
		},
		{
			name: "unset target and a release candidate of the minimum", out: "gh version 2.50.0-rc.1 (2024-05-20)",
			err: fmt.Sprintf(older, "2.50.0-rc.1"),
		},
		{
			name: "version wanted is not the gh found", target: "latest", want: "2.83.2", out: "gh version 2.83.1 (2025-11-13)",
			err: "the gh found is 2.83.1, and ILS_LIVE_GH_VERSION wants 2.83.2",
		},
		{
			name: "development build", out: "gh version DEV",
			err: fmt.Sprintf(unreadable, "gh version DEV"),
		},
		{
			name: "development build on the first of several lines", out: "gh version DEV\nhttps://github.com/cli/cli",
			err: fmt.Sprintf(unreadable, "gh version DEV"),
		},
		{
			name: "no output", target: "minimum",
			err: fmt.Sprintf(unreadable, ""),
		},
		{
			name: "unknown target", target: "newest", out: "gh version 2.83.2 (2025-12-10)",
			err: `ILS_LIVE_GH is "newest", want minimum, latest, or unset`,
		},
	}
}

func TestCheckLiveGh_WhenGhDoesNotSuitTarget_ReturnsError(t *testing.T) {
	t.Parallel()
	for _, tt := range unsuitedLiveGhs() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkLiveGh(tt.target, tt.want, tt.out)

			assert.Error(t, err, tt.err, "checkLiveGh(%q, %q, %q)", tt.target, tt.want, tt.out)
		})
	}
}
