package cli_test

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/mod/semver"
	"gotest.tools/v3/assert"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
)

// The live suite runs only under the live build tag; the decisions about the git and the gh it
// finds live here, outside the tag, so that the normal suite pins them.

const (
	// liveGhTargetVar says which gh the live suite must find: liveMinimum, liveLatest, or unset for
	// latest.
	liveGhTargetVar = "ILS_LIVE_GH"
	// liveGhVersionVar, when set, is the exact version of gh the live suite must find.
	liveGhVersionVar = "ILS_LIVE_GH_VERSION"
	// liveGitTargetVar says which git the live suite must find: liveMinimum, liveLatest, or unset for
	// latest.
	liveGitTargetVar = "ILS_LIVE_GIT"
	// liveGitVersionVar, when set, is the exact version of git the live suite must find.
	liveGitVersionVar = "ILS_LIVE_GIT_VERSION"

	liveMinimum = "minimum"
	liveLatest  = "latest"
)

// checkLiveGit returns why a git whose `git --version` prints out does not suit the live suite, or
// nil when it does. Its first line must name a version that ils reads, that version must be want
// when want is not empty, and it must be the version the target asks for: exactly the minimum ils
// enforces on the minimum target, and that or newer on the latest target or when target is empty.
func checkLiveGit(target, want, out string) error {
	line, _, _ := strings.Cut(out, "\n")

	found := git.VersionOf(line)
	if found == "" {
		return fmt.Errorf("git --version printed %q, which names no version the suite can read", out)
	}

	if want != "" && found != want {
		return fmt.Errorf("the git found is %s, and %s wants %s", found, liveGitVersionVar, want)
	}

	switch target {
	case liveMinimum:
		if found != git.MinimumVersion {
			return fmt.Errorf("%s is %s and the git found is %s, want the minimum ils enforces, %s",
				liveGitTargetVar, target, found, git.MinimumVersion)
		}
	case "", liveLatest:
		if semver.Compare("v"+found, "v"+git.MinimumVersion) < 0 {
			return fmt.Errorf("the git found is %s, older than the minimum ils enforces, %s", found, git.MinimumVersion)
		}
	default:
		return fmt.Errorf("%s is %q, want %s, %s, or unset", liveGitTargetVar, target, liveMinimum, liveLatest)
	}

	return nil
}

// checkLiveGh returns why a gh whose `gh --version` prints out does not suit the live suite, or nil
// when it does. Its first line must name a version, that version must be want when want is not
// empty, and it must be the version the target asks for: exactly the minimum ils enforces on the
// minimum target, and that or newer on the latest target or when target is empty.
func checkLiveGh(target, want, out string) error {
	line, _, _ := strings.Cut(out, "\n")
	rest, _ := strings.CutPrefix(line, "gh version ")
	found, _, _ := strings.Cut(rest, " ")

	if !semver.IsValid("v" + found) {
		return fmt.Errorf("gh --version printed %q, which names no version the suite can read", out)
	}

	if want != "" && found != want {
		return fmt.Errorf("the gh found is %s, and %s wants %s", found, liveGhVersionVar, want)
	}

	switch target {
	case liveMinimum:
		if found != github.MinimumVersion {
			return fmt.Errorf("%s is %s and the gh found is %s, want the minimum ils enforces, %s",
				liveGhTargetVar, target, found, github.MinimumVersion)
		}
	case "", liveLatest:
		if semver.Compare("v"+found, "v"+github.MinimumVersion) < 0 {
			return fmt.Errorf("the gh found is %s, older than the minimum ils enforces, %s", found, github.MinimumVersion)
		}
	default:
		return fmt.Errorf("%s is %q, want %s, %s, or unset", liveGhTargetVar, target, liveMinimum, liveLatest)
	}

	return nil
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
		{name: "latest target and a newer Git for Windows", target: "latest", out: "git version 2.56.0.windows.2"},
		{name: "unset target and a newer Apple git", out: "git version 2.54.0 (Apple Git-157)"},
		{name: "unset target and a newer release candidate", out: "git version 2.35.0.rc0"},
		{name: "version wanted is the git found", target: "latest", want: "2.56.0", out: "git version 2.56.0.windows.2"},
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
			name: "minimum target and a release candidate of the minimum", target: "minimum", out: "git version 2.34.1.rc0",
			err: fmt.Sprintf(notMinimum, "2.34.1-rc0"),
		},
		{
			name: "latest target and an older git", target: "latest", out: "git version 2.30.2",
			err: fmt.Sprintf(older, "2.30.2"),
		},
		{
			name: "latest target and a release candidate of the minimum", target: "latest", out: "git version 2.34.1.rc0",
			err: fmt.Sprintf(older, "2.34.1-rc0"),
		},
		{
			name: "unset target and an older Git for Windows", out: "git version 2.30.2.windows.1",
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
			name: "no output", target: "minimum",
			err: fmt.Sprintf(unreadable, ""),
		},
		{
			name: "unknown target", target: "newest", out: "git version 2.56.0",
			err: `ILS_LIVE_GIT is "newest", want minimum, latest, or unset`,
		},
		{
			name: "version wanted is checked before the minimum target", target: "minimum", want: "2.34.1", out: "git version 2.56.0",
			err: "the git found is 2.56.0, and ILS_LIVE_GIT_VERSION wants 2.34.1",
		},
		{
			name: "version wanted is checked before an unknown target", target: "newest", want: "2.56.0", out: "git version 2.55.0",
			err: "the git found is 2.55.0, and ILS_LIVE_GIT_VERSION wants 2.56.0",
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
			name: "no output", target: "minimum",
			err: fmt.Sprintf(unreadable, ""),
		},
		{
			name: "unknown target", target: "newest", out: "gh version 2.83.2 (2025-12-10)",
			err: `ILS_LIVE_GH is "newest", want minimum, latest, or unset`,
		},
		{
			name: "version wanted is checked before the minimum target", target: "minimum", want: "2.50.0", out: "gh version 2.83.2 (2025-12-10)",
			err: "the gh found is 2.83.2, and ILS_LIVE_GH_VERSION wants 2.50.0",
		},
		{
			name: "version wanted is checked before an unknown target", target: "newest", want: "2.83.2", out: "gh version 2.83.1 (2025-11-13)",
			err: "the gh found is 2.83.1, and ILS_LIVE_GH_VERSION wants 2.83.2",
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
