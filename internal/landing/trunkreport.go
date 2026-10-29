package landing

import (
	"context"
	"regexp"
	"slices"

	"golang.org/x/mod/semver"

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

// releaseSubject matches the subject of a release commit, as squashed from its pull request or not.
var releaseSubject = regexp.MustCompile(`^chore: release v\d+\.\d+\.\d+( \(#\d+\))?$`)

// readOnTrunk reads everything a status reports of the trunk HEAD stands on.
func (r *reading) readOnTrunk(ctx context.Context) {
	r.status.Standing, r.status.Branch = result.StandingTrunk, r.status.Trunk
	r.say(result.Say("On the trunk %s", result.Branch(r.status.Trunk)))

	r.readTrunkApart(ctx)
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
		r.failed(readLocalTrunk, err)

		return
	}

	r.status.Ahead, r.status.Behind = apart.Ahead, apart.Behind

	switch ahead, behind := commitCount(apart.Ahead), commitCount(apart.Behind); {
	case apart.Ahead > 0 && apart.Behind > 0:
		r.say(result.Say("The trunk and origin's trunk have diverged: %s ahead, %s behind", ahead, behind))
	case apart.Ahead > 0:
		r.say(result.Say("The trunk is ahead of origin's trunk by %s", ahead))
	case apart.Behind > 0:
		r.say(result.Say("The trunk is behind origin's trunk by %s", behind))
	default:
		r.say(result.Say("The trunk is up to date with origin's trunk"))
	}
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
	r.say(result.Say("Origin's trunk has %s since the last version tag %s", commitCount(since.Behind), last.Name))
}

// lastTag returns the tag of tags with the highest version whose commit is on origin's trunk, and
// whether there is one. Only tags that are versions count.
func (r *reading) lastTag(ctx context.Context, tags []git.Tag) (git.Tag, bool, error) {
	versions := slices.DeleteFunc(slices.Clone(tags), func(tag git.Tag) bool { return !semver.IsValid(tag.Name) })
	slices.SortStableFunc(versions, func(a, b git.Tag) int { return semver.Compare(b.Name, a.Name) })

	for _, tag := range versions {
		on, err := r.onOriginTrunk(ctx, tag)
		if err != nil {
			return git.Tag{}, false, err
		}

		if on {
			return tag, true, nil
		}
	}

	return git.Tag{}, false, nil
}

func (r *reading) onOriginTrunk(ctx context.Context, tag git.Tag) (bool, error) {
	on, err := r.deps.Repo.Contains(ctx, r.trunkTip, tag.SHA)
	if err == nil || endedEarly(err) {
		return on, err
	}

	// Fetching origin's trunk brought every commit on it, so a tag whose commit git lacks is not on
	// it, yet git fails the check of a commit it lacks as it fails on a broken repository. Fetching
	// the tag brings its commit, after which only a real failure fails the check.
	if _, err := r.deps.Repo.FetchTag(ctx, tag.Name); err != nil {
		return false, err
	}

	return r.deps.Repo.Contains(ctx, r.trunkTip, tag.SHA)
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

	r.status.ReleaseCommit = releaseSubject.MatchString(subject)
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
