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

func link(name string) string {
	return "https://ci.example.com/checks/" + name
}

func linked(check forge.Check) forge.Check {
	check.Link = link(check.Name)

	return check
}

func verdictOf(checks ...forge.Check) forge.Verdict {
	return forge.Verdict{Checks: checks}
}

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
		{name: "no check", verdict: verdictOf(), want: forge.VerdictNone},
		{name: "only skipped", verdict: verdictOf(forgetest.Check("docs", forge.BucketSkipped)), want: forge.VerdictGreen},
		{name: "one pending", verdict: verdictOf(forgetest.Check("build", forge.BucketPass), forgetest.Check("test", forge.BucketPending)), want: forge.VerdictPending},
		{name: "failing and pending", verdict: verdictOf(forgetest.Check("build", forge.BucketPending), forgetest.Check("test", forge.BucketFail)), want: forge.VerdictRed},
		{
			name:    "required passing, another required name missing",
			verdict: missingBuild(forgetest.RequiredCheck("test", forge.BucketPass)),
			want:    forge.VerdictGreen,
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

	failed := forge.FailedCheck{Name: "test", Link: link("test")}
	canceled := forge.FailedCheck{Name: "build", Link: link("build"), Canceled: true}
	tests := []struct {
		name    string
		want    forge.FailedCheck
		verdict forge.Verdict
	}{
		{name: "passing ahead of failing", verdict: verdictOf(linked(forgetest.Check("lint", forge.BucketPass)), linked(forgetest.Check("test", forge.BucketFail))), want: failed},
		{name: "failing ahead of canceled", verdict: verdictOf(linked(forgetest.Check("test", forge.BucketFail)), linked(forgetest.Check("build", forge.BucketCanceled))), want: failed},
		{name: "canceled ahead of failing", verdict: verdictOf(linked(forgetest.Check("build", forge.BucketCanceled)), linked(forgetest.Check("test", forge.BucketFail))), want: canceled},
		{name: "optional failing ahead of required canceled", verdict: verdictOf(linked(forgetest.Check("lint", forge.BucketFail)), linked(forgetest.RequiredCheck("build", forge.BucketCanceled))), want: canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := tt.verdict.Failed()
			assert.Assert(t, ok, "Failed() = %+v, false, want a failed check", got)

			assert.Check(t, is.DeepEqual(got, tt.want))
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
			name:    "every check concluded",
			verdict: verdictOf(forgetest.Check("build", forge.BucketPass), forgetest.Check("test", forge.BucketFail), forgetest.Check("lint", forge.BucketCanceled)),
		},
		{
			name:    "pending among concluded, in order",
			verdict: verdictOf(forgetest.Check("build", forge.BucketPending), forgetest.Check("lint", forge.BucketPass), forgetest.Check("test", forge.BucketPending)),
			want:    []forge.Check{forgetest.Check("build", forge.BucketPending), forgetest.Check("test", forge.BucketPending)},
		},
		{
			name:    "bucket this version does not know",
			verdict: verdictOf(forgetest.Check("build", forge.BucketPass), forgetest.Check("test", unknownBucket)),
			want:    []forge.Check{forgetest.Check("test", unknownBucket)},
		},
		{
			name:    "optional pending left out",
			verdict: verdictOf(forgetest.Check("lint", forge.BucketPending), forgetest.RequiredCheck("test", forge.BucketPending), forgetest.RequiredCheck("build", forge.BucketPass)),
			want:    []forge.Check{forgetest.RequiredCheck("test", forge.BucketPending)},
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
			verdict := forge.Verdict{Runs: []forge.Run{{Name: "ci", Status: forge.RunCompleted}, {Name: "docs", Status: tt.status}}}

			got := verdict.Running()

			assert.Check(t, is.Equal(got, tt.want))
		})
	}
}
