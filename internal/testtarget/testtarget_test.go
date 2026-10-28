package testtarget_test

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

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

// unsuited is a version found that does not suit a target, and the parts of the error Check
// returns for it that Check computes.
type unsuited struct {
	name   string
	target string
	want   string
	found  string
	parts  []string
}

// unsuitedVersions returns the versions found that do not suit hammer's target, each with the
// parts its error must name.
func unsuitedVersions() []unsuited {
	return []unsuited{
		{
			name: "minimum target and a newer version", target: testtarget.Minimum, found: "1.10.0",
			parts: []string{"HAMMER_TARGET", "1.10.0", "1.4.0"},
		},
		{
			name: "latest target and an older version", target: testtarget.Latest, found: "1.3.9",
			parts: []string{"1.3.9", "1.4.0"},
		},
		{
			name: "version wanted that the minimum target rejects", target: testtarget.Minimum, want: "1.10.0",
			found: "1.10.0", parts: []string{"HAMMER_TARGET", "1.10.0", "1.4.0"},
		},
		{
			name: "version wanted is checked before the minimum target", target: testtarget.Minimum, want: "1.4.0",
			found: "1.10.0", parts: []string{"HAMMER_VERSION", "1.10.0", "1.4.0"},
		},
		{
			name: "unknown target", target: "newest", found: "1.10.0",
			parts: []string{"HAMMER_TARGET", "newest"},
		},
	}
}

func TestTool_Check_WhenFoundDoesNotSuitTarget_ReturnsError(t *testing.T) {
	t.Parallel()
	for _, tt := range unsuitedVersions() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := hammer().Check(tt.target, tt.want, tt.found)

			for _, part := range tt.parts {
				assert.Check(t, is.ErrorContains(err, part), "Check(%q, %q, %q)", tt.target, tt.want, tt.found)
			}
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
		name   string
		target string
		want   string
		out    string
		part   string
	}{
		{name: "first line names no version", out: "built on a bench\nhammer 1.4.0", part: `"built on a bench"`},
		{
			name: "first line names a version other than the one wanted", target: testtarget.Latest,
			want: "1.10.0", out: "hammer 1.9.0", part: "1.9.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := hammer().CheckOutput(tt.target, tt.want, tt.out, hammerVersionOf)

			assert.Check(t, is.ErrorContains(err, tt.part), "CheckOutput(%q, %q, %q)", tt.target, tt.want, tt.out)
		})
	}
}
