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

func TestTool_Check_WhenTargetUnsetAndFoundNewer_ReturnsNil(t *testing.T) {
	t.Parallel()

	err := hammer().Check("", "", "1.10.0")

	assert.NilError(t, err)
}

func TestTool_Check_WhenFoundDoesNotSuitTarget_ReturnsError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target string
		want   string
		found  string
		parts  []string
	}{
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
			name: "version wanted is checked before the minimum target", target: testtarget.Minimum, want: "1.9.0",
			found: "1.10.0", parts: []string{"HAMMER_VERSION", "1.10.0", "1.9.0"},
		},
		{
			name: "unknown target", target: "newest", found: "1.10.0",
			parts: []string{"HAMMER_TARGET", "newest"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := hammer().Check(tt.target, tt.want, tt.found)

			for _, part := range tt.parts {
				assert.Check(t, is.ErrorContains(err, part), "Check(%q, %q, %q)", tt.target, tt.want, tt.found)
			}
		})
	}
}

func hammerVersionOf(line string) string {
	version, ok := strings.CutPrefix(line, "hammer ")
	if !ok {
		return ""
	}

	return version
}

func TestTool_CheckOutput_WhenFirstLineNamesASuitedVersion_ReturnsNil(t *testing.T) {
	t.Parallel()
	out := "hammer 1.4.0"

	err := hammer().CheckOutput(testtarget.Minimum, "", out, hammerVersionOf)

	assert.NilError(t, err)
}

func TestTool_CheckOutput_WhenFirstLineNamesNoVersion_ReturnsErrorQuotingIt(t *testing.T) {
	t.Parallel()
	const out = "built on a bench"

	err := hammer().CheckOutput("", "", out, hammerVersionOf)

	assert.Check(t, is.ErrorContains(err, `"built on a bench"`), "CheckOutput(%q)", out)
}

func TestTool_CheckEnvOutput_WhenTargetVarIsMinimum_RejectsANewerVersion(t *testing.T) {
	// Not parallel: t.Setenv sets the environment of the whole process.
	const out = "hammer 1.10.0\n"
	t.Setenv("HAMMER_TARGET", testtarget.Minimum)
	t.Setenv("HAMMER_VERSION", "")

	err := hammer().CheckEnvOutput(out, hammerVersionOf)

	assert.Check(t, is.ErrorContains(err, "HAMMER_TARGET"), "CheckEnvOutput(%q)", out)
}

func TestTool_CheckEnvOutput_WhenVersionVarIsSet_RejectsAnotherVersion(t *testing.T) {
	// Not parallel: t.Setenv sets the environment of the whole process.
	const out = "hammer 1.10.0\n"
	t.Setenv("HAMMER_TARGET", testtarget.Latest)
	t.Setenv("HAMMER_VERSION", "1.9.0")

	err := hammer().CheckEnvOutput(out, hammerVersionOf)

	assert.Check(t, is.ErrorContains(err, "HAMMER_VERSION"), "CheckEnvOutput(%q)", out)
	assert.Check(t, is.ErrorContains(err, "1.9.0"), "CheckEnvOutput(%q)", out)
}
