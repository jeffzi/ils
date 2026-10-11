package github

import "github.com/jeffzi/ils/internal/forge"

// MergeFacts is what GitHub says about whether a pull request can merge.
type MergeFacts struct {
	// Mergeable is GitHub's `mergeable`: MERGEABLE, CONFLICTING, or UNKNOWN.
	Mergeable string
	// Status is GitHub's `mergeStateStatus`.
	Status string
	// Review is GitHub's `reviewDecision`; it is empty when no review is asked for.
	Review string
	Draft  bool
	// MissingCheck reports that a required check has no check on the head commit
	// and that nothing still running could report it.
	MissingCheck bool
}

// MergeStateOf maps GitHub's values to the merge state a command switches on.
//
// A value of `mergeStateStatus` it does not know is forge.MergeUnknown.
func MergeStateOf(facts MergeFacts) forge.MergeState {
	switch facts.Mergeable {
	case "CONFLICTING":
		return forge.MergeConflicting
	case "UNKNOWN":
		return forge.MergeUnknown
	}

	// GitHub reports a draft as BLOCKED as often as DRAFT, so the flag decides
	// ahead of every status a draft can still carry. Only `mergeable` goes first:
	// a draft that conflicts still needs the trunk merged in.
	if facts.Draft || facts.Status == "DRAFT" {
		return forge.MergeDraft
	}

	switch facts.Status {
	case "BEHIND":
		return forge.MergeBehind
	case "DIRTY":
		return forge.MergeConflicting
	case "CLEAN", "HAS_HOOKS", "UNSTABLE":
		return forge.MergeClean
	case "BLOCKED":
		return blockedState(facts)
	default:
		return forge.MergeUnknown
	}
}

// blockedState says what blocks a pull request whose status is BLOCKED.
func blockedState(facts MergeFacts) forge.MergeState {
	switch {
	case facts.Review == "REVIEW_REQUIRED" || facts.Review == "CHANGES_REQUESTED":
		return forge.MergeBlockedByReview
	case facts.MissingCheck:
		return forge.MergeBlockedByMissingCheck
	default:
		return forge.MergeBlockedOther
	}
}
