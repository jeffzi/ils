package landing

// MergeWindow and MergeReserve let the tests build the limits and countdowns they expect from the
// values the wait and the merge branch on, instead of copying them.
const MergeWindow, MergeReserve = mergeWindow, mergeReserve

// ExcerptLines lets the tests build a log longer than an excerpt holds, instead of copying its
// length.
const ExcerptLines = excerptLines
