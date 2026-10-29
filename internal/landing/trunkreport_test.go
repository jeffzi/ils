package landing_test

import (
	"errors"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
)

const standingTrunk = "trunk"

// ruledBy is viewing with no pull request found, and a trunk whose rules stand as rules says.
func ruledBy(rules forge.RulesState) forgeFor {
	return func(tip string) forgetest.Script {
		script := viewing(cleanHead(), passing())(tip)
		script.TrunkProtection = forgetest.Values(forge.Protection{Rules: rules})

		return script
	}
}

// standOnTrunk switches the working repository to the trunk.
func standOnTrunk(f *gittest.Fixture) {
	f.Git("switch", "--quiet", trunk)
}

// tagReleases stands on the trunk and gives origin version tags: v1.9.0 on the trunk's first
// commit, v1.10.0 on the next one with two commits after it on origin's trunk, a higher v3.0.0 on
// the topic branch's commit, which origin's trunk lacks, and vintage, which is no version, on
// origin's trunk tip. It then adds a commit to the local trunk alone.
func tagReleases(f *gittest.Fixture) {
	standOnTrunk(f)

	first := f.Git("rev-parse", "HEAD")
	tagged := f.Commits(trunk, "feat: add a logo")[0]
	f.Commits(trunk, "fix: correct a typo", "docs: add an example")
	f.Publish(trunk)
	f.Publish(topic)
	f.OriginGit("tag", "v1.9.0", first)
	f.OriginGit("tag", "v1.10.0", tagged)
	f.OriginGit("tag", "v3.0.0", f.Git("rev-parse", topic))
	f.OriginGit("tag", "vintage", f.Git("rev-parse", trunk))
	f.Commits(trunk, "wip: local work")
}

func TestStatus_WhenOnTheTrunk_ReportsTheLastVersionTagOnOriginsTrunk(t *testing.T) {
	t.Parallel()
	s := statusScene(t, tagReleases, ruledBy(forge.RulesProtect))

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Standing, standingTrunk))
	assert.Check(t, is.Equal(got.status.LastTag, "v1.10.0"))
	assert.Check(t, is.Equal(got.status.SinceTag, 2))
	assertSays(t, got.text, expect("v1.10.0"))
}

func TestStatus_WhenOnTheTrunkAndAHigherTagIsOnACommitOnlyOriginHolds_ReportsTheHighestOnOriginsTrunk(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) {
		tagReleases(f)
		f.OriginGit("tag", "v9.0.0", f.AdvanceOrigin(topic))
	}, ruledBy(forge.RulesProtect))

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.LastTag, "v1.10.0"))
	assert.Check(t, is.Len(got.status.Unread, 0))
}

func TestStatus_WhenOnTheTrunkAndOriginHoldsNoVersionTag_SaysThereIsNone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		arrange func(*gittest.Fixture)
		name    string
	}{
		{
			name: "only a tag that is no version",
			arrange: func(f *gittest.Fixture) {
				standOnTrunk(f)
				f.OriginGit("tag", "vintage", trunk)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := statusScene(t, tt.arrange, ruledBy(forge.RulesProtect))

			got := seeStatus(t, s, (*testing.T).Context)

			assert.Check(t, is.Equal(got.status.LastTag, ""))
			assertSays(t, got.text, expect("no version tag"))
		})
	}
}

func TestStatus_WhenOnTheTrunk_ReportsHowItStandsAgainstOriginsTrunk(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) {
		standOnTrunk(f)
		f.AdvanceOrigin(trunk)
		f.Commits(trunk, "wip: local work")
	}, ruledBy(forge.RulesProtect))

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Ahead, 1))
	assert.Check(t, is.Equal(got.status.Behind, 1))
}

// releaseRow is one subject of origin's trunk tip and whether it is a release commit.
type releaseRow struct {
	name    string
	subject string
	want    bool
}

func TestStatus_WhenOnTheTrunk_ReportsWhetherOriginsTrunkTipIsAReleaseCommit(t *testing.T) {
	t.Parallel()

	tests := []releaseRow{
		{name: "release commit", subject: "chore: release v1.2.0", want: true},
		{name: "release commit merged as a pull request", subject: "chore: release v1.2.0 (#9)", want: true},
		{name: "release of a version short of a patch number", subject: "chore: release v1.2"},
		{name: "release subject with words after the version", subject: "chore: release v1.2.0 notes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := statusScene(t, func(f *gittest.Fixture) {
				standOnTrunk(f)
				f.Commits(trunk, tt.subject)
				f.Publish(trunk)
				// The local tip is no release commit, so only origin's trunk tip decides.
				f.Commits(trunk, "wip: local work")
			}, ruledBy(forge.RulesProtect))

			got := seeStatus(t, s, (*testing.T).Context)

			assert.Check(t, is.Equal(got.status.ReleaseCommit, tt.want))
		})
	}
}

// protectionRow is one way rules stand on the trunk.
type protectionRow struct {
	name  string
	rules forge.RulesState
	want  string
}

func TestStatus_WhenOnTheTrunk_ReportsWhetherRulesProtectIt(t *testing.T) {
	t.Parallel()

	tests := []protectionRow{
		{name: "rules protect it", rules: forge.RulesProtect, want: "protected"},
		{name: "no rule applies", rules: forge.RulesNone, want: "none"},
		{name: "the plan allows no rules", rules: forge.RulesUnavailable, want: "unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := statusScene(t, standOnTrunk, ruledBy(tt.rules))

			got := seeStatus(t, s, (*testing.T).Context)

			assert.Check(t, is.Equal(got.status.Protection, tt.want))
		})
	}
}

func TestStatus_WhenOnTheTrunkAndTheRulesReadFails_ReportsTheFailedReadBesideTheLastTag(t *testing.T) {
	t.Parallel()
	s := statusScene(t, tagReleases, func(tip string) forgetest.Script {
		script := viewing(cleanHead(), passing())(tip)
		script.TrunkProtection = []forgetest.Answer[forge.Protection]{{Err: errors.New(forgeFailure)}}

		return script
	})

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, expect(readRules, forgeFailure))
	assert.Assert(t, is.Len(got.status.Unread, 1))
	assert.Check(t, is.Equal(got.status.Unread[0].Read, readRules))
}

func TestStatus_WhenOnTheTrunk_EndsOnStartingABranch(t *testing.T) {
	t.Parallel()
	s := statusScene(t, standOnTrunk, ruledBy(forge.RulesProtect))

	got := seeStatus(t, s, (*testing.T).Context)

	_, next, found := strings.Cut(got.text, "Next:")
	assert.Check(t, found, "text %q, want a Next: line", got.text)
	assertSays(t, next, nextBranch())
}

func TestStatus_WhenOnTheTrunkAndGhIsMissing_StillReportsWhatGitTells(t *testing.T) {
	t.Parallel()
	s := statusScene(t, tagReleases, func(string) forgetest.Script { return ghMissing() })

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Standing, standingTrunk))
	assert.Check(t, is.Equal(got.status.Ahead, 1))
	assert.Check(t, is.Equal(got.status.LastTag, "v1.10.0"))
	assert.Check(t, is.Equal(got.status.SinceTag, 2))
}
