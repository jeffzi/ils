package landing

// MergeWindow and MergeReserve let the tests build the limits and countdowns they expect from the
// values the wait and the merge branch on, instead of copying them.
const MergeWindow, MergeReserve = mergeWindow, mergeReserve

// LastLines lets the tests cut an excerpt from a text no push or wait produces, such as an empty one.
func LastLines(text string) string { return lastLines(text) }
