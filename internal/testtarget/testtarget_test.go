package testtarget_test

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/jeffzi/ils/internal/testtarget"
)

// hammer is a tool whose minimum compares differently as text and as a version: 1.10.0 is newer
// than 1.4.0 though it sorts before it.
func hammer() testtarget.Tool {
	return testtarget.Tool{Name: "hammer", TargetVar: "HAMMER_TARGET", VersionVar: "HAMMER_VERSION", MinimumVersion: "1.4.0"}
}

func TestTool_Check_WhenFoundSuitsTarget_ReturnsNil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target string
		want   string
		found  string
	}{
		{name: "minimum target and the minimum", target: testtarget.Minimum, found: "1.4.0"},
		{name: "latest target and the minimum", target: testtarget.Latest, found: "1.4.0"},
		{name: "latest target and a newer version", target: testtarget.Latest, found: "1.10.0"},
		{name: "unset target and a newer version", found: "1.10.0"},
		{name: "version wanted is the one found", target: testtarget.Latest, want: "1.10.0", found: "1.10.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := hammer().Check(tt.target, tt.want, tt.found)

			assert.NilError(t, err, "Check(%q, %q, %q)", tt.target, tt.want, tt.found)
		})
	}
}

// unsuited is a version found that does not suit a target, and the error Check returns for it.
type unsuited struct {
	name    string
	target  string
	want    string
	found   string
	errText string
}

// unsuitedVersions returns the versions found that do not suit hammer's target, each with its error.
func unsuitedVersions() []unsuited {
	return []unsuited{
		{
			name: "minimum target and a newer version", target: testtarget.Minimum, found: "1.10.0",
			errText: "HAMMER_TARGET is minimum and the hammer found is 1.10.0, want the minimum ils enforces, 1.4.0",
		},
		{
			name: "minimum target and an older version", target: testtarget.Minimum, found: "1.3.9",
			errText: "HAMMER_TARGET is minimum and the hammer found is 1.3.9, want the minimum ils enforces, 1.4.0",
		},
		{
			name: "latest target and an older version", target: testtarget.Latest, found: "1.3.9",
			errText: "the hammer found is 1.3.9, older than the minimum ils enforces, 1.4.0",
		},
		{
			name: "unset target and an older version", found: "1.3.9",
			errText: "the hammer found is 1.3.9, older than the minimum ils enforces, 1.4.0",
		},
		{
			name: "unset target and a pre-release of the minimum", found: "1.4.0-rc.1",
			errText: "the hammer found is 1.4.0-rc.1, older than the minimum ils enforces, 1.4.0",
		},
		{
			name: "latest target and a version that is not semver", target: testtarget.Latest, found: "DEV",
			errText: "the hammer found is DEV, older than the minimum ils enforces, 1.4.0",
		},
		{
			name: "version wanted that the minimum target rejects", target: testtarget.Minimum, want: "1.10.0",
			found:   "1.10.0",
			errText: "HAMMER_TARGET is minimum and the hammer found is 1.10.0, want the minimum ils enforces, 1.4.0",
		},
		{
			name: "version wanted is not the one found", target: testtarget.Latest, want: "1.10.0", found: "1.9.0",
			errText: "the hammer found is 1.9.0, and HAMMER_VERSION wants 1.10.0",
		},
		{
			name: "version wanted is checked before the minimum target", target: testtarget.Minimum, want: "1.4.0",
			found: "1.10.0", errText: "the hammer found is 1.10.0, and HAMMER_VERSION wants 1.4.0",
		},
		{
			name: "version wanted is checked before an unknown target", target: "newest", want: "1.10.0",
			found: "1.9.0", errText: "the hammer found is 1.9.0, and HAMMER_VERSION wants 1.10.0",
		},
		{
			name: "unknown target", target: "newest", found: "1.10.0",
			errText: `HAMMER_TARGET is "newest", want minimum, latest, or unset`,
		},
	}
}

func TestTool_Check_WhenFoundDoesNotSuitTarget_ReturnsError(t *testing.T) {
	t.Parallel()
	for _, tt := range unsuitedVersions() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := hammer().Check(tt.target, tt.want, tt.found)

			assert.Error(t, err, tt.errText, "Check(%q, %q, %q)", tt.target, tt.want, tt.found)
		})
	}
}

// hammerVersionOf reads the version from a line `hammer <version>`, and returns "" for any other
// line.
func hammerVersionOf(line string) string {
	version, ok := strings.CutPrefix(line, "hammer ")
	if !ok {
		return ""
	}

	return version
}

func TestTool_CheckOutput_WhenFirstLineNamesASuitedVersion_ReturnsNil(t *testing.T) {
	t.Parallel()
	out := "hammer 1.4.0\nhammer 1.3.9"

	err := hammer().CheckOutput(testtarget.Minimum, "", out, hammerVersionOf)

	assert.NilError(t, err)
}

func TestTool_CheckOutput_WhenFirstLineDoesNotSuitTarget_ReturnsError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		target  string
		want    string
		out     string
		errText string
	}{
		{
			name: "first line names no version", out: "built on a bench\nhammer 1.4.0",
			errText: `hammer --version printed "built on a bench", which names no version the suite can read`,
		},
		{
			name: "no output", target: testtarget.Minimum,
			errText: `hammer --version printed "", which names no version the suite can read`,
		},
		{
			name: "first line names a version the target rejects", target: testtarget.Minimum,
			out:     "hammer 1.10.0\nhammer 1.4.0",
			errText: "HAMMER_TARGET is minimum and the hammer found is 1.10.0, want the minimum ils enforces, 1.4.0",
		},
		{
			name: "first line names a version other than the one wanted", target: testtarget.Latest,
			want: "1.10.0", out: "hammer 1.9.0",
			errText: "the hammer found is 1.9.0, and HAMMER_VERSION wants 1.10.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := hammer().CheckOutput(tt.target, tt.want, tt.out, hammerVersionOf)

			assert.Error(t, err, tt.errText, "CheckOutput(%q, %q, %q)", tt.target, tt.want, tt.out)
		})
	}
}
