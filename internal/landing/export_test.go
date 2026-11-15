package landing

import "context"

// MergeWindow and MergeReserve let the tests build the limits and countdowns they expect from the
// values the wait and the merge branch on, instead of copying them.
const MergeWindow, MergeReserve = mergeWindow, mergeReserve

// ExcerptLines lets the tests build a log longer than an excerpt holds, instead of copying its
// length.
const ExcerptLines = excerptLines

// NewSurvey lets the tests ask one record for a read more than once, which no command does before
// its first write.
func NewSurvey(deps Deps) *survey { return newSurvey(deps) }

// OriginsTrunk lets the tests ask the record for origin's trunk.
func (s *survey) OriginsTrunk(ctx context.Context) (string, error) { return s.trunk(ctx) }

// TrunkFetch lets the tests ask the record for the fetch of origin's trunk, which needs the trunk.
func (s *survey) TrunkFetch(ctx context.Context) (string, error) { return s.trunkFetch(ctx) }

// MergedHead lets the tests ask the record for the head of more than one pull request.
func (s *survey) MergedHead(ctx context.Context, number int) (string, error) {
	return s.mergedHead(ctx, number)
}
