package landing

import (
	"context"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// The reads of a status on the trunk, as a result names them.
const (
	readTags     = "origin's version tags"
	readSinceTag = "the commits on origin's trunk since its version tags"
	readTrunkTip = "origin's trunk tip"
)

// readOnTrunk reads everything a status reports of the trunk HEAD stands on.
func (r *reading) readOnTrunk(ctx context.Context) {
	r.status.Standing, r.status.Branch = result.StandingTrunk, r.status.Trunk
	r.say(result.Say("On the trunk %s", result.Branch(r.status.Trunk)))

	r.readTrunkApart(ctx)
	r.readLocalTrunk(ctx)
	r.readLastTag(ctx)
	r.readRelease(ctx)
	r.readRules(ctx)
}

// readTrunkApart counts the commits the trunk and origin's trunk each have that the other lacks.
func (r *reading) readTrunkApart(ctx context.Context) {
	if r.cut || r.trunkTip == "" {
		return
	}

	apart, err := r.deps.Repo.Divergence(ctx, "HEAD", r.trunkTip)
	if err != nil {
		r.failedTrunk(readLocalTrunk, err)

		return
	}

	r.sayApart(apart, result.Concat("trunk"), result.Concat("origin's trunk"))
}

// readLastTag reads the last version tag on origin's trunk and counts the commits since it.
func (r *reading) readLastTag(ctx context.Context) {
	if r.cut || r.trunkTip == "" {
		return
	}

	tags, err := r.deps.Repo.OriginTags(ctx)
	if err != nil {
		r.failed(readTags, err)

		return
	}

	last, found, err := r.lastTag(ctx, tags)

	switch {
	case err != nil:
		r.failed(readSinceTag, err)

		return
	case !found:
		r.say(result.Say("Origin holds no version tag on its trunk"))

		return
	}

	since, err := r.deps.Repo.Divergence(ctx, last.SHA, r.trunkTip)
	if err != nil {
		r.failed(readSinceTag, err)

		return
	}

	r.status.LastTag, r.status.SinceTag = last.Name, since.Behind
	r.say(result.Say("Origin's trunk has %s since the last version tag %s", nounCount(since.Behind, "commit"), last.Name))
}

// lastTag returns the tag of tags with the highest version whose commit is on origin's trunk, and
// whether there is one. Only tags that are versions count.
func (r *reading) lastTag(ctx context.Context, tags []git.Tag) (git.Tag, bool, error) {
	for _, tag := range versionTags(tags) {
		// Fetching origin's trunk brought every commit on it, so a tag whose commit git lacks is not
		// on it; the check fetches the tag as retryAfterTagFetch does.
		on, err := retryAfterTagFetch(ctx, r.deps, tag.Name, func() (bool, error) {
			return r.deps.Repo.Contains(ctx, r.trunkTip, tag.SHA)
		})
		if err != nil {
			return git.Tag{}, false, err
		}

		if on {
			return tag, true, nil
		}
	}

	return git.Tag{}, false, nil
}

func (r *reading) readRelease(ctx context.Context) {
	if r.cut || r.trunkTip == "" {
		return
	}

	subject, err := r.deps.Repo.Subject(ctx, r.trunkTip)
	if err != nil {
		r.failed(readTrunkTip, err)

		return
	}

	_, r.status.ReleaseCommit = releasedTag(subject)
	if r.status.ReleaseCommit {
		r.say(result.Say("Origin's trunk tip %s is a release commit: %s", result.SHA(r.trunkTip), subject))
	} else {
		r.say(result.Say("Origin's trunk tip %s is not a release commit", result.SHA(r.trunkTip)))
	}
}

func (r *reading) readRules(ctx context.Context) {
	if r.cut || r.repo == (forge.Repo{}) {
		return
	}

	protection, err := r.deps.Forge.TrunkProtection(ctx, r.repo, r.status.Trunk)
	if err != nil {
		r.failed(readRules, err)

		return
	}

	trunk := result.Branch(r.status.Trunk)

	switch protection.Rules {
	case forge.RulesProtect:
		r.status.Protection = result.ProtectionProtected
		r.say(result.Say("Rules protect the trunk %s", trunk))
	case forge.RulesNone:
		r.status.Protection = result.ProtectionNone
		r.say(result.Say("The trunk %s has no rule that applies to it", trunk))
	case forge.RulesUnavailable:
		r.status.Protection = result.ProtectionUnavailable
		r.say(result.Say("The repository's plan does not allow rules, so none protect the trunk %s", trunk))
	}
}
