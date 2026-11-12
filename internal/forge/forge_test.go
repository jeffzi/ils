package forge_test

import (
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
)

// unknownBucket must match no forge.Bucket constant, or the tests that use it stop reaching the
// unknown-bucket case.
const unknownBucket = forge.Bucket("stale")

// verdictOf returns a verdict of checks with no required check missing.
func verdictOf(checks ...forge.Check) forge.Verdict {
	return forge.Verdict{Checks: checks}
}

// missingBuild returns a verdict of checks where the trunk requires a "build" check that the commit
// does not have, so only the required checks decide.
func missingBuild(checks ...forge.Check) forge.Verdict {
	return forge.Verdict{Checks: checks, MissingRequired: []string{"build"}}
}

func TestVerdict_State(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		want    forge.VerdictState
		verdict forge.Verdict
	}{
		{
			name:    "only skipped",
			verdict: verdictOf(forgetest.Check("docs", forge.BucketSkipped)),
			want:    forge.VerdictGreen,
		},
		{
			name:    "one pending",
			verdict: verdictOf(forgetest.Check("build", forge.BucketPass), forgetest.Check("test", forge.BucketPending)),
			want:    forge.VerdictPending,
		},
		{
			name:    "failing and pending",
			verdict: verdictOf(forgetest.Check("build", forge.BucketPending), forgetest.Check("test", forge.BucketFail)),
			want:    forge.VerdictRed,
		},
		{
			name:    "every required name missing, optional passing",
			verdict: missingBuild(forgetest.Check("lint", forge.BucketPass)),
			want:    forge.VerdictNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.verdict.State()

			assert.Check(t, is.Equal(got, tt.want))
		})
	}
}

func TestVerdict_Failed(t *testing.T) {
	t.Parallel()

	const failed, canceled = "test", "build"
	tests := []struct {
		name    string
		want    string
		verdict forge.Verdict
	}{
		{
			name:    "passing ahead of failing",
			verdict: verdictOf(forgetest.Check("lint", forge.BucketPass), forgetest.Check("test", forge.BucketFail)),
			want:    failed,
		},
		{
			name:    "failing ahead of canceled",
			verdict: verdictOf(forgetest.Check("test", forge.BucketFail), forgetest.Check("build", forge.BucketCanceled)),
			want:    failed,
		},
		{
			name:    "canceled ahead of failing",
			verdict: verdictOf(forgetest.Check("build", forge.BucketCanceled), forgetest.Check("test", forge.BucketFail)),
			want:    canceled,
		},
		{
			name:    "optional failing ahead of required canceled",
			verdict: verdictOf(forgetest.Check("lint", forge.BucketFail), forgetest.RequiredCheck("build", forge.BucketCanceled)),
			want:    canceled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := tt.verdict.Failed()
			assert.Assert(t, ok, "Failed() = %+v, false, want a failed check", got)

			assert.Check(t, is.Equal(got.Name, tt.want))
		})
	}
}

func TestVerdict_Pending(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		verdict forge.Verdict
		want    []forge.Check
	}{
		{
			name: "every check concluded",
			verdict: verdictOf(
				forgetest.Check("build", forge.BucketPass),
				forgetest.Check("test", forge.BucketFail),
				forgetest.Check("lint", forge.BucketCanceled),
			),
		},
		{
			name:    "bucket this version does not know",
			verdict: verdictOf(forgetest.Check("build", forge.BucketPass), forgetest.Check("test", unknownBucket)),
			want:    []forge.Check{forgetest.Check("test", unknownBucket)},
		},
		{
			name: "optional pending left out",
			verdict: verdictOf(
				forgetest.Check("lint", forge.BucketPending),
				forgetest.RequiredCheck("test", forge.BucketPending),
				forgetest.RequiredCheck("build", forge.BucketPass),
			),
			want: []forge.Check{forgetest.RequiredCheck("test", forge.BucketPending)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.verdict.Pending()

			assert.Check(t, is.DeepEqual(got, tt.want, cmpopts.EquateEmpty()))
		})
	}
}

func TestVerdict_Running(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status forge.RunStatus
		want   bool
	}{
		{name: "queued", status: forge.RunQueued, want: true},
		{name: "in progress", status: forge.RunInProgress, want: true},
		{name: "awaiting a person", status: forge.RunAwaitingPerson, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			verdict := forge.Verdict{Runs: []forge.Run{
				{Name: "ci", Status: forge.RunCompleted},
				{Name: "docs", Status: tt.status},
			}}

			got := verdict.Running()

			assert.Check(t, is.Equal(got, tt.want))
		})
	}
}
