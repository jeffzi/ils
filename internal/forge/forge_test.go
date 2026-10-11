package forge_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/jeffzi/ils/internal/forge"
)

// link is the page of the check with the given name.
func link(name string) string {
	return "https://ci.example.com/checks/" + name
}

// optional is a check no rule requires.
func optional(name string, bucket forge.Bucket) forge.Check {
	return forge.Check{Name: name, Bucket: bucket, Workflow: "ci", Link: link(name)}
}

// required is a check the trunk's protection names.
func required(name string, bucket forge.Bucket) forge.Check {
	return forge.Check{Name: name, Bucket: bucket, Workflow: "ci", Link: link(name), Required: true}
}

// verdictOf is a verdict over the given checks, with no required name missing.
func verdictOf(checks ...forge.Check) forge.Verdict {
	return forge.Verdict{Checks: checks}
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
		{name: "one canceled", verdict: verdictOf(optional("build", forge.BucketPass), optional("test", forge.BucketCanceled)), want: forge.VerdictRed},
		{name: "one pending", verdict: verdictOf(optional("build", forge.BucketPass), optional("test", forge.BucketPending)), want: forge.VerdictPending},
		{name: "passing beside a bucket this version does not know", verdict: verdictOf(optional("build", forge.BucketPass), optional("test", forge.Bucket("stale"))), want: forge.VerdictPending},
		{name: "required bucket this version does not know", verdict: verdictOf(required("test", forge.Bucket("stale"))), want: forge.VerdictPending},
		{name: "failing and pending", verdict: verdictOf(optional("build", forge.BucketPending), optional("test", forge.BucketFail)), want: forge.VerdictRed},
		{name: "canceled and pending", verdict: verdictOf(optional("build", forge.BucketPending), optional("test", forge.BucketCanceled)), want: forge.VerdictRed},
		{name: "required passing, optional failing", verdict: verdictOf(optional("lint", forge.BucketFail), required("test", forge.BucketPass)), want: forge.VerdictGreen},
		{name: "required passing, optional canceled", verdict: verdictOf(optional("lint", forge.BucketCanceled), required("test", forge.BucketPass)), want: forge.VerdictGreen},
		{name: "required passing, optional pending", verdict: verdictOf(optional("lint", forge.BucketPending), required("test", forge.BucketPass)), want: forge.VerdictGreen},
		{name: "required pending, optional failing", verdict: verdictOf(optional("lint", forge.BucketFail), required("test", forge.BucketPending)), want: forge.VerdictPending},
		{name: "required failing, optional passing", verdict: verdictOf(optional("lint", forge.BucketPass), required("test", forge.BucketFail)), want: forge.VerdictRed},
		{
			name:    "required passing, another required name missing",
			verdict: forge.Verdict{Checks: []forge.Check{required("test", forge.BucketPass)}, MissingRequired: []string{"build"}},
			want:    forge.VerdictGreen,
		},
		{
			name:    "every required name missing, optional passing",
			verdict: forge.Verdict{Checks: []forge.Check{optional("lint", forge.BucketPass)}, MissingRequired: []string{"build"}},
			want:    forge.VerdictNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.verdict.State()

			if got != tt.want {
				t.Errorf("State() = %q, want %q", got, tt.want)
			}
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
			if !ok {
				t.Fatalf("Failed() = %+v, false, want a failed check", got)
			}

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Failed() mismatch (-want +got):\n%s", diff)
			}
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
		{
			name:    "optional failing, every required name missing",
			verdict: forge.Verdict{Checks: []forge.Check{optional("lint", forge.BucketFail)}, MissingRequired: []string{"build"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := tt.verdict.Failed()

			if ok {
				t.Errorf("Failed() = %+v, true, want no failed check", got)
			}
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
			verdict: verdictOf(optional("build", forge.BucketPass), optional("test", forge.Bucket("stale"))),
			want:    []forge.Check{optional("test", forge.Bucket("stale"))},
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

			if diff := cmp.Diff(tt.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Pending() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
