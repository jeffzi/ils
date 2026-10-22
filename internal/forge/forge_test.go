package forge_test

import (
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
)

// unknownBucket must match no forge.Bucket constant, or the tests that use it stop reaching the
// unknown-bucket case.
const unknownBucket = forge.Bucket("stale")

// link is the CI page of the check called name.
func link(name string) string {
	return "https://ci.example.com/checks/" + name
}

// optional is a check the trunk's protection does not name.
func optional(name string, bucket forge.Bucket) forge.Check {
	return forge.Check{Name: name, Bucket: bucket, Workflow: "ci", Link: link(name)}
}

// required is a check the trunk's protection names.
func required(name string, bucket forge.Bucket) forge.Check {
	check := optional(name, bucket)
	check.Required = true

	return check
}

// verdictOf is a verdict over the given checks, with no required name missing.
func verdictOf(checks ...forge.Check) forge.Verdict {
	return forge.Verdict{Checks: checks}
}

// missingBuild is a verdict over the given checks, with the required name "build" missing.
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
		{name: "one passing", verdict: verdictOf(optional("build", forge.BucketPass)), want: forge.VerdictGreen},
		{name: "passing and skipped", verdict: verdictOf(optional("build", forge.BucketPass), optional("docs", forge.BucketSkipped)), want: forge.VerdictGreen},
		{name: "only skipped", verdict: verdictOf(optional("docs", forge.BucketSkipped)), want: forge.VerdictGreen},
		{name: "one failing", verdict: verdictOf(optional("build", forge.BucketPass), optional("test", forge.BucketFail)), want: forge.VerdictRed},
		{name: "one pending", verdict: verdictOf(optional("build", forge.BucketPass), optional("test", forge.BucketPending)), want: forge.VerdictPending},
		{name: "passing beside a bucket this version does not know", verdict: verdictOf(optional("build", forge.BucketPass), optional("test", unknownBucket)), want: forge.VerdictPending},
		{name: "failing and pending", verdict: verdictOf(optional("build", forge.BucketPending), optional("test", forge.BucketFail)), want: forge.VerdictRed},
		{name: "required passing, optional failing", verdict: verdictOf(optional("lint", forge.BucketFail), required("test", forge.BucketPass)), want: forge.VerdictGreen},
		{name: "required passing, optional pending", verdict: verdictOf(optional("lint", forge.BucketPending), required("test", forge.BucketPass)), want: forge.VerdictGreen},
		{name: "required pending, optional failing", verdict: verdictOf(optional("lint", forge.BucketFail), required("test", forge.BucketPending)), want: forge.VerdictPending},
		{name: "required failing, optional passing", verdict: verdictOf(optional("lint", forge.BucketPass), required("test", forge.BucketFail)), want: forge.VerdictRed},
		{
			name:    "required passing, another required name missing",
			verdict: missingBuild(required("test", forge.BucketPass)),
			want:    forge.VerdictGreen,
		},
		{
			name:    "every required name missing, optional passing",
			verdict: missingBuild(optional("lint", forge.BucketPass)),
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
		{name: "failing check", verdict: verdictOf(optional("test", forge.BucketFail)), want: failed},
		{name: "canceled check", verdict: verdictOf(optional("build", forge.BucketCanceled)), want: canceled},
		{name: "passing ahead of failing", verdict: verdictOf(optional("lint", forge.BucketPass), optional("test", forge.BucketFail)), want: failed},
		{name: "pending ahead of failing", verdict: verdictOf(optional("lint", forge.BucketPending), optional("test", forge.BucketFail)), want: failed},
		{name: "failing ahead of canceled", verdict: verdictOf(optional("test", forge.BucketFail), optional("build", forge.BucketCanceled)), want: failed},
		{name: "canceled ahead of failing", verdict: verdictOf(optional("build", forge.BucketCanceled), optional("test", forge.BucketFail)), want: canceled},
		{name: "optional failing ahead of required canceled", verdict: verdictOf(optional("lint", forge.BucketFail), required("build", forge.BucketCanceled)), want: canceled},
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

func TestVerdict_Failed_WhenNoDecidingCheckFailed_ReportsNone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		verdict forge.Verdict
	}{
		{name: "no check", verdict: verdictOf()},
		{name: "passing and skipped", verdict: verdictOf(optional("build", forge.BucketPass), optional("docs", forge.BucketSkipped))},
		{name: "only pending", verdict: verdictOf(optional("build", forge.BucketPending))},
		{name: "only an optional check failing", verdict: verdictOf(optional("lint", forge.BucketFail), required("test", forge.BucketPass))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := tt.verdict.Failed()

			assert.Check(t, !ok, "Failed() = %+v, true, want no failed check", got)
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
		{name: "no check", verdict: verdictOf()},
		{name: "every check concluded", verdict: verdictOf(optional("build", forge.BucketPass), optional("test", forge.BucketFail))},
		{
			name:    "pending among concluded, in order",
			verdict: verdictOf(optional("build", forge.BucketPending), optional("lint", forge.BucketPass), optional("test", forge.BucketPending)),
			want:    []forge.Check{optional("build", forge.BucketPending), optional("test", forge.BucketPending)},
		},
		{
			name:    "pending beside a failing check",
			verdict: verdictOf(optional("build", forge.BucketFail), optional("test", forge.BucketPending)),
			want:    []forge.Check{optional("test", forge.BucketPending)},
		},
		{
			name:    "bucket this version does not know",
			verdict: verdictOf(optional("build", forge.BucketPass), optional("test", unknownBucket)),
			want:    []forge.Check{optional("test", unknownBucket)},
		},
		{
			name:    "optional pending left out",
			verdict: verdictOf(optional("lint", forge.BucketPending), required("test", forge.BucketPending), required("build", forge.BucketPass)),
			want:    []forge.Check{required("test", forge.BucketPending)},
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

func TestVerdict_Deciding(t *testing.T) {
	t.Parallel()

	optionalLint, requiredTest := optional("lint", forge.BucketPass), required("test", forge.BucketPass)
	tests := []struct {
		name    string
		verdict forge.Verdict
		want    []forge.Check
	}{
		{name: "nothing required, every check decides", verdict: verdictOf(optionalLint), want: []forge.Check{optionalLint}},
		{name: "a required check decides alone", verdict: verdictOf(optionalLint, requiredTest), want: []forge.Check{requiredTest}},
		{name: "a required name missing, no optional check decides", verdict: missingBuild(optionalLint), want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.verdict.Deciding()

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
		{name: "completed", status: forge.RunCompleted, want: false},
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
