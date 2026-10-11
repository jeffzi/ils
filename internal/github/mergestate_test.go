package github_test

import (
	"testing"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/github"
)

func TestMergeStateOf(t *testing.T) {
	t.Parallel()

	const mergeable = "MERGEABLE"

	tests := []struct {
		name  string
		want  forge.MergeState
		facts github.MergeFacts
	}{
		{name: "conflicting wins over a clean status", facts: github.MergeFacts{Mergeable: "CONFLICTING", Status: "CLEAN"}, want: forge.MergeConflicting},
		{name: "conflicting wins over a blocked status", facts: github.MergeFacts{Mergeable: "CONFLICTING", Status: "BLOCKED"}, want: forge.MergeConflicting},
		{name: "mergeable not worked out yet", facts: github.MergeFacts{Mergeable: "UNKNOWN", Status: "CLEAN"}, want: forge.MergeUnknown},
		{name: "clean", facts: github.MergeFacts{Mergeable: mergeable, Status: "CLEAN"}, want: forge.MergeClean},
		{name: "clean with hooks", facts: github.MergeFacts{Mergeable: mergeable, Status: "HAS_HOOKS"}, want: forge.MergeClean},
		{name: "unstable still lands", facts: github.MergeFacts{Mergeable: mergeable, Status: "UNSTABLE"}, want: forge.MergeClean},
		{name: "behind the base", facts: github.MergeFacts{Mergeable: mergeable, Status: "BEHIND"}, want: forge.MergeBehind},
		{name: "dirty", facts: github.MergeFacts{Mergeable: mergeable, Status: "DIRTY"}, want: forge.MergeConflicting},
		{name: "draft status", facts: github.MergeFacts{Mergeable: mergeable, Status: "DRAFT"}, want: forge.MergeDraft},
		{name: "draft flag on a blocked status", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED", Draft: true}, want: forge.MergeDraft},
		{name: "draft flag on a behind status", facts: github.MergeFacts{Mergeable: mergeable, Status: "BEHIND", Draft: true}, want: forge.MergeDraft},
		{name: "draft flag on a dirty status", facts: github.MergeFacts{Mergeable: mergeable, Status: "DIRTY", Draft: true}, want: forge.MergeDraft},
		{name: "draft flag on a clean status", facts: github.MergeFacts{Mergeable: mergeable, Status: "CLEAN", Draft: true}, want: forge.MergeDraft},
		{name: "draft flag on a clean status with hooks", facts: github.MergeFacts{Mergeable: mergeable, Status: "HAS_HOOKS", Draft: true}, want: forge.MergeDraft},
		{name: "draft flag on an unstable status", facts: github.MergeFacts{Mergeable: mergeable, Status: "UNSTABLE", Draft: true}, want: forge.MergeDraft},
		{name: "draft flag on a status not worked out yet", facts: github.MergeFacts{Mergeable: mergeable, Status: "UNKNOWN", Draft: true}, want: forge.MergeDraft},
		{name: "conflicting wins over the draft flag", facts: github.MergeFacts{Mergeable: "CONFLICTING", Status: "DIRTY", Draft: true}, want: forge.MergeConflicting},
		{name: "conflicting wins over a draft status", facts: github.MergeFacts{Mergeable: "CONFLICTING", Status: "DRAFT", Draft: true}, want: forge.MergeConflicting},
		{name: "mergeable not worked out yet wins over the draft flag", facts: github.MergeFacts{Mergeable: "UNKNOWN", Status: "UNKNOWN", Draft: true}, want: forge.MergeUnknown},
		{name: "mergeable not worked out yet on a blocked status", facts: github.MergeFacts{Mergeable: "UNKNOWN", Status: "BLOCKED"}, want: forge.MergeUnknown},
		{name: "mergeable not worked out yet on a behind status", facts: github.MergeFacts{Mergeable: "UNKNOWN", Status: "BEHIND"}, want: forge.MergeUnknown},
		{name: "mergeable not worked out yet on a dirty status", facts: github.MergeFacts{Mergeable: "UNKNOWN", Status: "DIRTY"}, want: forge.MergeUnknown},
		{name: "mergeable not worked out yet on a draft status", facts: github.MergeFacts{Mergeable: "UNKNOWN", Status: "DRAFT", Draft: true}, want: forge.MergeUnknown},
		{name: "blocked, review required", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED", Review: "REVIEW_REQUIRED"}, want: forge.MergeBlockedByReview},
		{name: "blocked, changes requested", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED", Review: "CHANGES_REQUESTED"}, want: forge.MergeBlockedByReview},
		{name: "blocked, review required and a check missing", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED", Review: "REVIEW_REQUIRED", MissingCheck: true}, want: forge.MergeBlockedByReview},
		{name: "blocked, approved and a check missing", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED", Review: "APPROVED", MissingCheck: true}, want: forge.MergeBlockedByMissingCheck},
		{name: "blocked, no review asked and a check missing", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED", MissingCheck: true}, want: forge.MergeBlockedByMissingCheck},
		{name: "blocked, approved and no check missing", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED", Review: "APPROVED"}, want: forge.MergeBlockedOther},
		{name: "blocked, no review asked and no check missing", facts: github.MergeFacts{Mergeable: mergeable, Status: "BLOCKED"}, want: forge.MergeBlockedOther},
		{name: "status not worked out yet", facts: github.MergeFacts{Mergeable: mergeable, Status: "UNKNOWN"}, want: forge.MergeUnknown},
		{name: "status this version does not know", facts: github.MergeFacts{Mergeable: mergeable, Status: "QUEUED_FOR_MERGE"}, want: forge.MergeUnknown},
		{name: "no status at all", facts: github.MergeFacts{Mergeable: mergeable}, want: forge.MergeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := github.MergeStateOf(tt.facts)

			if got != tt.want {
				t.Errorf("MergeStateOf(%+v) = %q, want %q", tt.facts, got, tt.want)
			}
		})
	}
}
