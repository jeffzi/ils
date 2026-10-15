package cli_test

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/mod/semver"
	"gotest.tools/v3/assert"

	"github.com/jeffzi/ils/internal/git"
)

// The live suite runs only under the live build tag; the decision about the git it finds lives
// here, outside the tag, so that the normal suite pins it.

const (
	// liveGitLegVar says which git the live suite must find: liveMinimum, liveLatest, or unset for
	// latest.
	liveGitLegVar = "ILS_LIVE_GIT"
	// liveGitVersionVar, when set, is the exact version of git the live suite must find.
	liveGitVersionVar = "ILS_LIVE_GIT_VERSION"

	liveMinimum = "minimum"
	liveLatest  = "latest"
)

// checkLiveGit returns why a git whose `git --version` prints out does not suit the live suite, or
// nil when it does. Its first line must name a version that ils reads, that version must be want
// when want is not empty, and it must be the version leg asks for: exactly the minimum ils
// enforces on the minimum leg, and that or newer on the latest leg or when leg is empty.
func checkLiveGit(leg, want, out string) error {
	line, _, _ := strings.Cut(out, "\n")

	found := git.VersionOf(line)
	if found == "" {
		return fmt.Errorf("git --version printed %q, which names no version the suite can read", out)
	}

	if want != "" && found != want {
		return fmt.Errorf("the git found is %s, and %s wants %s", found, liveGitVersionVar, want)
	}

	switch leg {
	case liveMinimum:
		if found != git.MinimumVersion {
			return fmt.Errorf("%s is %s and the git found is %s, want the minimum ils enforces, %s",
				liveGitLegVar, leg, found, git.MinimumVersion)
		}
	case "", liveLatest:
		if semver.Compare("v"+found, "v"+git.MinimumVersion) < 0 {
			return fmt.Errorf("the git found is %s, older than the minimum ils enforces, %s", found, git.MinimumVersion)
		}
	default:
		return fmt.Errorf("%s is %q, want %s, %s, or unset", liveGitLegVar, leg, liveMinimum, liveLatest)
	}

	return nil
}

func TestCheckLiveGit_WhenGitSuitsLeg_ReturnsNil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		leg  string
		want string
		out  string
	}{
		{name: "minimum leg and the minimum", leg: "minimum", out: "git version " + git.MinimumVersion},
		{name: "latest leg and the minimum", leg: "latest", out: "git version " + git.MinimumVersion},
		{name: "latest leg and a newer Git for Windows", leg: "latest", out: "git version 2.56.0.windows.2"},
		{name: "unset leg and a newer Apple git", out: "git version 2.54.0 (Apple Git-157)"},
		{name: "unset leg and a newer release candidate", out: "git version 2.35.0.rc0"},
		{name: "version wanted is the git found", leg: "latest", want: "2.56.0", out: "git version 2.56.0.windows.2"},
		{name: "minimum on the first of several lines", leg: "minimum", out: "git version " + git.MinimumVersion + "\ncpu: x86_64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkLiveGit(tt.leg, tt.want, tt.out)

			assert.NilError(t, err, "checkLiveGit(%q, %q, %q)", tt.leg, tt.want, tt.out)
		})
	}
}

// unsuitedLiveGit is a git the live suite must refuse, with the leg and version it was asked for.
type unsuitedLiveGit struct {
	name string
	leg  string
	want string
	out  string
	// err is the text of the error checkLiveGit returns.
	err string
}

func unsuitedLiveGits() []unsuitedLiveGit {
	const (
		notMinimum = "ILS_LIVE_GIT is minimum and the git found is %s, want the minimum ils enforces, 2.34.1"
		older      = "the git found is %s, older than the minimum ils enforces, 2.34.1"
		unreadable = "git --version printed %q, which names no version the suite can read"
	)

	return []unsuitedLiveGit{
		{
			name: "minimum leg and a newer git", leg: "minimum", out: "git version 2.56.0",
			err: fmt.Sprintf(notMinimum, "2.56.0"),
		},
		{
			name: "minimum leg and an older git", leg: "minimum", out: "git version 2.30.2",
			err: fmt.Sprintf(notMinimum, "2.30.2"),
		},
		{
			name: "minimum leg and a release candidate of the minimum", leg: "minimum", out: "git version 2.34.1.rc0",
			err: fmt.Sprintf(notMinimum, "2.34.1-rc0"),
		},
		{
			name: "latest leg and an older git", leg: "latest", out: "git version 2.30.2",
			err: fmt.Sprintf(older, "2.30.2"),
		},
		{
			name: "latest leg and a release candidate of the minimum", leg: "latest", out: "git version 2.34.1.rc0",
			err: fmt.Sprintf(older, "2.34.1-rc0"),
		},
		{
			name: "unset leg and an older Git for Windows", out: "git version 2.30.2.windows.1",
			err: fmt.Sprintf(older, "2.30.2"),
		},
		{
			name: "version wanted is not the git found", leg: "latest", want: "2.56.0", out: "git version 2.55.0",
			err: "the git found is 2.55.0, and ILS_LIVE_GIT_VERSION wants 2.56.0",
		},
		{
			name: "development build", out: "git version DEV",
			err: fmt.Sprintf(unreadable, "git version DEV"),
		},
		{
			name: "no output", leg: "minimum",
			err: fmt.Sprintf(unreadable, ""),
		},
		{
			name: "unknown leg", leg: "newest", out: "git version 2.56.0",
			err: `ILS_LIVE_GIT is "newest", want minimum, latest, or unset`,
		},
		{
			name: "version wanted is checked before the minimum leg", leg: "minimum", want: "2.34.1", out: "git version 2.56.0",
			err: "the git found is 2.56.0, and ILS_LIVE_GIT_VERSION wants 2.34.1",
		},
		{
			name: "version wanted is checked before an unknown leg", leg: "newest", want: "2.56.0", out: "git version 2.55.0",
			err: "the git found is 2.55.0, and ILS_LIVE_GIT_VERSION wants 2.56.0",
		},
	}
}

func TestCheckLiveGit_WhenGitDoesNotSuitLeg_ReturnsError(t *testing.T) {
	t.Parallel()
	for _, tt := range unsuitedLiveGits() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkLiveGit(tt.leg, tt.want, tt.out)

			assert.Error(t, err, tt.err, "checkLiveGit(%q, %q, %q)", tt.leg, tt.want, tt.out)
		})
	}
}
