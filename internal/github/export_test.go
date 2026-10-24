package github

import "github.com/jeffzi/ils/internal/forge"

// ErrNoPullRequest is errNoPullRequest, for tests that check an answer naming no pull request is
// refused for that reason.
var ErrNoPullRequest = errNoPullRequest

// MergeFacts is mergeFacts, for the table that pins how GitHub's values map to a merge state.
type MergeFacts = mergeFacts

// MergeStateOf calls mergeStateOf, for the same table.
func MergeStateOf(facts MergeFacts) forge.MergeState { return mergeStateOf(facts) }
