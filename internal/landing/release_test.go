package landing_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

const (
	// version is the version the tests release, releaseTag its tag, and releaseSubject the subject
	// of its release commit.
	version        = "1.2.0"
	releaseTag     = "v" + version
	releaseSubject = "chore: release " + releaseTag

	// previousTag is the version tag below version that the tests put on origin.
	previousTag = "v1.1.0"

	readTags          = "origin's tags"
	readHistory       = "the first-parent history of origin's trunk"
	readLocalTag      = "the local tag of the version"
	readChangelog     = "the changelog of the release commit"
	readReleaseChecks = "the checks of the release commit"
)

// releaseRequest is the command, words and argument of `ils release v`, with --tag-only when
// tagOnly is set.
func releaseRequest(v string, tagOnly bool) landing.Request {
	words := []string{landing.ReleaseCommand, v}
	if tagOnly {
		words = append(words, "--tag-only")
	}

	return landing.Request{Command: landing.ReleaseCommand, Words: words, Args: []string{v}, TagOnly: tagOnly}
}

// forced returns req with --force typed after its other words.
func forced(req landing.Request) landing.Request {
	req.Words = append(slices.Clone(req.Words), "--force")
	req.Force = true

	return req
}

// releaseURL is the link of the GitHub release of version.
const releaseURL = "https://github.com/Acme/Widgets/releases/tag/" + releaseTag

// releasable is a forge for a release: the user can push to Acme/Widgets, the tag has no GitHub
// release yet, and the one created answers releaseURL. It has no answer for the checks of the
// release commit.
func releasable() forgetest.Script {
	script := allowed()
	script.FindRelease = forgetest.Values("")
	script.PublishRelease = forgetest.Values(releaseURL)

	return script
}

// gated is releasable with the checks of the release commit answering each of verdicts in turn. It
// has no answer for the trunk's protection or its CI.
func gated(verdicts ...forge.Verdict) forgetest.Script {
	script := releasable()
	script.CommitVerdict = says(verdicts...)

	return script
}

// peeled is the format for-each-ref prints a tag in: the commit it points at, through the object of
// an annotated tag.
const peeled = "--format=%(if)%(*objectname)%(then)%(*objectname)%(else)%(objectname)%(end)"

// localTag returns the commit the clone's tag named tag points at, empty when the clone has none.
func (s *scene) localTag(tag string) string {
	return s.fixture.Git("for-each-ref", peeled, "refs/tags/"+tag)
}

// originTag returns the commit origin's tag named tag points at, empty when origin has none.
func (s *scene) originTag(tag string) string {
	return s.fixture.OriginGit("for-each-ref", peeled, "refs/tags/"+tag)
}

// releaseCalls returns the calls of s's forge about the GitHub release of a tag.
func releaseCalls(s *scene) []forgetest.Call {
	return callsOf(s.forge.Calls(), forgetest.OpFindRelease, forgetest.OpPublishRelease)
}

// released is the payload of st's release of version once its gate ended on verdict, with url as
// the link of its GitHub release.
func released(st staged, verdict, url string) *result.Release {
	return &result.Release{Version: version, Tag: releaseTag, SHA: st.release, Verdict: verdict, URL: url}
}

// releasePayload returns the release payload got carries once rendered as JSON in gitDir, or nil
// when it carries none.
func releasePayload(t *testing.T, got result.Result, gitDir string) *result.Release {
	t.Helper()

	_, decoded := rendered(t, got, gitDir)
	if decoded.Payload == nil {
		return nil
	}

	return decoded.Payload.Release
}

// releaseCommand is `ils release v` with the `Next:` lines that name it.
func releaseCommand(v string) landingCommand {
	line := "ils release " + v

	return landingCommand{
		name:  "release " + v,
		req:   releaseRequest(v, false),
		again: expect(resulttest.NextLine(line) + "\n"),
		fixed: expect("fix what failed", line),
	}
}

// section is the changelog section of the version v, holding body.
func section(v, body string) string {
	return "## [" + v + "] - 2026-10-30\n\n" + body
}

// notes is the body of a changelog section with something in it.
const notes = "### Added\n\n- A logo.\n\n"

// changelog is a CHANGELOG.md holding sections, newest first, under an empty Unreleased section,
// and ending on link reference definitions.
func changelog(sections ...string) string {
	return "# Changelog\n\n## [Unreleased]\n\n" + strings.Join(sections, "") +
		"[1.2.0]: https://github.com/Acme/Widgets/compare/v1.1.0...v1.2.0\n" +
		"[1.1.0]: https://github.com/Acme/Widgets/releases/tag/v1.1.0\n"
}

// releaseOf commits to the trunk the release commit of the version v, whose changelog holds the
// section of v above that of 1.1.0, with subject, and returns its sha.
func releaseOf(f *gittest.Fixture, v, subject string) string {
	return f.CommitChangelog(trunk, subject, changelog(section(v, notes), section("1.1.0", notes)))
}

// releaseCommit commits to the trunk the release commit of version and returns its sha.
func releaseCommit(f *gittest.Fixture) string {
	return releaseOf(f, version, releaseSubject)
}

// tagged puts previousTag on the trunk's first commit, base, on origin and commits the release
// commit of version; it returns the release commit.
func tagged(f *gittest.Fixture, base string) string {
	f.OriginTag(previousTag, base)

	return releaseCommit(f)
}

// staged is a release scene: the commit build expects to be the release commit, and the trunk's
// first commit, which the local trunk stays at.
type staged struct {
	s       *scene
	release string
	base    string
}

// releaseScene returns a scene whose origin's trunk holds what build commits on the trunk past its
// first commit, base, and whose local trunk holds none of it: it stays at base, with HEAD on the
// topic branch. build may tag origin too, and returns the commit it means as the release commit.
// Its forge says the checks of the release commit passed.
func releaseScene(t *testing.T, build func(f *gittest.Fixture, base string) string) staged {
	t.Helper()

	var st staged

	st.s = newScene(t, func(f *gittest.Fixture) {
		st.base = f.Git("rev-parse", trunk)
		st.release = build(f, st.base)
		f.Publish(trunk)
		f.Git("switch", "--quiet", topic)
		f.Git("branch", "--force", trunk, st.base)
	}, gated(passing()), false)

	return st
}

// runRelease runs req on the scene with the whole time limit left.
func runRelease(t *testing.T, s *scene, req landing.Request) result.Result {
	t.Helper()

	return landing.Release(within(t, 0), s.deps, req)
}

// assertReleaseRead fails unless got is the end of `ils release v` once every check held: done,
// naming st's release commit and the tag.
func assertReleaseRead(t *testing.T, st staged, got result.Result, v string) {
	t.Helper()

	assertEndedBy(t, got, st.s.gitDir, ended{
		outcome: result.Done, req: releaseRequest(v, false), text: expect(shortSHA(st.release), "v"+v),
	})
}

// assertReleaseRefused fails unless running req on st's scene ends refused with text holding want
// and writes nothing.
func assertReleaseRefused(t *testing.T, st staged, req landing.Request, want string) {
	t.Helper()

	before := st.s.written()

	got := runRelease(t, st.s, req)

	assertEndedBy(t, got, st.s.gitDir, ended{outcome: result.Refused, req: req, text: want})
	assert.Check(t, is.Equal(st.s.written(), before))
}

// ---------------------------------------------------------------------------
// The version
// ---------------------------------------------------------------------------

func TestRelease_WhenTheVersionIsNotXYZ_RefusesNamingItBeforeReadingOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, in string }{
		{name: "leading v", in: "v1.2.0"},
		{name: "missing patch", in: "1.2"},
		{name: "build metadata", in: "1.2.0+build"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := releaseScene(t, tagged)
			req := releaseRequest(tt.in, false)

			got := runRelease(t, st.s, req)

			assertEndedBy(t, got, st.s.gitDir, ended{outcome: result.Refused, req: req, text: expect(tt.in)})
			assert.Check(t, !st.s.gitRan("ls-remote"), "Release() read origin before refusing %q", tt.in)
		})
	}
}

// orderRow is one set of tags origin or the clone holds, and the version released over them.
type orderRow struct {
	arrange func(f *gittest.Fixture, base string)
	name    string
	version string
	// highest is the tag the refusal names; empty when the version passes.
	highest string
}

// build arranges row's tags and commits the release commit of row's version, as releaseScene's
// build does.
func (row orderRow) build(f *gittest.Fixture, base string) string {
	row.arrange(f, base)

	return releaseOf(f, row.version, "chore: release v"+row.version)
}

func TestRelease_WhenOriginHoldsAVersionTagNotBelowIt_RefusesNamingTheHighest(t *testing.T) {
	t.Parallel()

	tests := []orderRow{
		{
			name: "a version below two higher ones", version: version, highest: "v1.10.0",
			arrange: func(f *gittest.Fixture, base string) {
				f.OriginTag("v1.10.0", base)
				f.OriginTag("v1.3.0", base)
			},
		},
		{
			name: "the same version under a shorter name", version: version, highest: "v1.2",
			arrange: func(f *gittest.Fixture, base string) { f.OriginTag("v1.2", base) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := releaseScene(t, tt.build)

			assertReleaseRefused(t, st, releaseRequest(tt.version, false), expect(tt.highest))
		})
	}
}

func TestRelease_WhenEveryVersionTagOfOriginIsBelowIt_ReadsTheRelease(t *testing.T) {
	t.Parallel()

	tests := []orderRow{
		{
			name: "a higher tag only the clone holds", version: version,
			arrange: func(f *gittest.Fixture, base string) { f.Git("tag", "v9.0.0", base) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				st := releaseScene(t, tt.build)

				got := runRelease(t, st.s, releaseRequest(tt.version, false))

				assertReleaseRead(t, st, got, tt.version)
			})
		})
	}
}

// ---------------------------------------------------------------------------
// The release commit
// ---------------------------------------------------------------------------

// commitRow is a trunk on origin, built past its first commit, base, and the release commit it
// holds, which build returns.
type commitRow struct {
	build func(f *gittest.Fixture, base string) string
	name  string
}

// releaseCommitRows are the places on origin's trunk a release commit can sit.
func releaseCommitRows() []commitRow {
	return []commitRow{
		{name: "at the tip", build: tagged},
		{
			name: "with commits after it, left out of the tag",
			build: func(f *gittest.Fixture, base string) string {
				release := tagged(f, base)
				f.Commits(trunk, "feat: add a banner")

				return release
			},
		},
		{
			name: "merged as a pull request",
			build: func(f *gittest.Fixture, base string) string {
				f.OriginTag(previousTag, base)

				return releaseOf(f, version, releaseSubject+" (#14)")
			},
		},
		{
			name: "twice, the newer meant",
			build: func(f *gittest.Fixture, base string) string {
				tagged(f, base)
				f.Commits(trunk, "fix: correct the logo")
				fixed := section(version, notes+"### Fixed\n\n- The logo.\n\n")

				return f.CommitChangelog(trunk, releaseSubject, changelog(fixed, section("1.1.0", notes)))
			},
		},
		{
			name: "with a later commit that empties the changelog",
			build: func(f *gittest.Fixture, base string) string {
				release := tagged(f, base)
				f.CommitChangelog(trunk, "docs: drop the changelog's sections", changelog())

				return release
			},
		},
	}
}

// previousTagRows are the previous tags, or their lack, a release commit can follow on origin.
func previousTagRows() []commitRow {
	return []commitRow{
		{
			name: "and origin holds no version tag",
			build: func(f *gittest.Fixture, _ string) string {
				f.Commits(trunk, "feat: add a logo")

				return releaseCommit(f)
			},
		},
		{
			name: "and origin holds only a tag that is no version, past it",
			build: func(f *gittest.Fixture, _ string) string {
				release := releaseCommit(f)
				f.OriginTag("vintage", f.Commits(trunk, "feat: add a banner")[0])

				return release
			},
		},
		{name: "past a previous tag on a commit the clone lacks", build: pastATagTheCloneLacks},
	}
}

// pastATagTheCloneLacks puts previousTag on origin on a commit the clone lacks, so that a release
// fetches the tag, and commits the release commit of version; it returns the release commit.
func pastATagTheCloneLacks(f *gittest.Fixture, _ string) string {
	f.Publish(topic)
	f.OriginGit("tag", previousTag, f.AdvanceOrigin(topic))

	return releaseCommit(f)
}

func TestRelease_WhenOriginsTrunkHoldsTheReleaseCommit_TagsItOnOrigin(t *testing.T) {
	t.Parallel()

	for _, tt := range append(releaseCommitRows(), previousTagRows()...) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				st := releaseScene(t, tt.build)

				got := runRelease(t, st.s, releaseRequest(version, false))

				assertReleaseRead(t, st, got, version)
				assert.Check(t, is.Equal(st.s.originTag(releaseTag), st.release))
			})
		})
	}
}

// checkoutRow is a checkout the command runs from.
type checkoutRow struct {
	arrange func(t *testing.T, s *scene)
	name    string
}

func TestRelease_WhenRunFromAnyCheckout_ReadsTheReleaseFromOriginAsFetchedNow(t *testing.T) {
	t.Parallel()

	tests := []checkoutRow{
		{name: "another branch", arrange: func(*testing.T, *scene) {}},
		{name: "a detached HEAD", arrange: func(_ *testing.T, s *scene) { s.fixture.Detach() }},
		{name: "a dirty tree", arrange: func(_ *testing.T, s *scene) { s.fixture.DirtyFile() }},
		{
			name: "a linked worktree",
			arrange: func(_ *testing.T, s *scene) {
				s.deps.Repo.Runner.Dir = s.fixture.BranchWorktree(topic)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				st := releaseScene(t, tagged)
				tt.arrange(t, st.s)

				got := runRelease(t, st.s, releaseRequest(version, false))

				assertReleaseRead(t, st, got, version)
			})
		})
	}
}

// missingRow is a trunk on origin, built past its first commit, base, with no release commit to
// find, and the parts of the refusal.
type missingRow struct {
	build func(f *gittest.Fixture, base string)
	name  string
	want  string
}

func TestRelease_WhenNoReleaseCommitFollowsThePreviousTag_RefusesNamingWhereItLooked(t *testing.T) {
	t.Parallel()

	since := expect(trunk, previousTag, releaseSubject)
	tests := []missingRow{
		{
			name: "no commit with the subject", want: since,
			build: func(f *gittest.Fixture, base string) {
				f.OriginTag(previousTag, base)
				f.Commits(trunk, "feat: add a logo")
			},
		},
		{
			name: "the subject of a pre-release of the version", want: since,
			build: func(f *gittest.Fixture, base string) {
				f.OriginTag(previousTag, base)
				releaseOf(f, version, "chore: release v1.2.0-rc.1")
			},
		},
		{
			name: "the subject with words after the version", want: since,
			build: func(f *gittest.Fixture, base string) {
				f.OriginTag(previousTag, base)
				releaseOf(f, version, releaseSubject+" notes")
			},
		},
		{
			name: "a release commit before the previous tag", want: since,
			build: func(f *gittest.Fixture, _ string) {
				releaseCommit(f)
				f.OriginTag(previousTag, f.Commits(trunk, "fix: correct the logo")[0])
			},
		},
		{
			name: "no previous tag", want: expect(trunk, releaseSubject, "no version tag"),
			build: func(f *gittest.Fixture, _ string) { f.Commits(trunk, "feat: add a logo") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := releaseScene(t, func(f *gittest.Fixture, base string) string {
				tt.build(f, base)

				return ""
			})

			assertReleaseRefused(t, st, releaseRequest(version, false), tt.want)
		})
	}
}

// ---------------------------------------------------------------------------
// The tag
// ---------------------------------------------------------------------------

func TestRelease_WhenOriginsTagIsAtAnotherCommit_RefusesNamingIt(t *testing.T) {
	t.Parallel()
	st := releaseScene(t, tagged)
	st.s.fixture.OriginTag(releaseTag, st.base)

	assertReleaseRefused(t, st, releaseRequest(version, false), expect(releaseTag, shortSHA(st.base)))
}

func TestRelease_WhenTheLocalTagIsAtAnotherCommit_RefusesWithANextThatDeletesIt(t *testing.T) {
	t.Parallel()
	st := releaseScene(t, tagged)
	st.s.fixture.Git("tag", releaseTag, st.base)

	assertReleaseRefused(t, st, releaseRequest(version, false),
		expect(releaseTag, shortSHA(st.base), "Next:", "git tag --delete "+releaseTag, "ils release "+version))
	assert.Check(t, is.Equal(st.s.fixture.Git("rev-parse", releaseTag+"^{commit}"), st.base))
}

// tagRow is where the tags of the version stand once the release commit is on origin's trunk.
type tagRow struct {
	arrange func(st staged)
	name    string
}

func TestRelease_WhenTheTagsOfTheVersionAllowIt_ReadsTheRelease(t *testing.T) {
	t.Parallel()

	tests := []tagRow{
		{
			name:    "the local tag at the release commit",
			arrange: func(st staged) { st.s.fixture.Git("tag", releaseTag, st.release) },
		},
		{
			name: "origin's tag at the release commit and the local one elsewhere",
			arrange: func(st staged) {
				st.s.fixture.OriginTag(releaseTag, st.release)
				st.s.fixture.Git("tag", releaseTag, st.base)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				st := releaseScene(t, tagged)
				tt.arrange(st)

				got := runRelease(t, st.s, releaseRequest(version, false))

				assertReleaseRead(t, st, got, version)
			})
		})
	}
}

// ---------------------------------------------------------------------------
// The changelog
// ---------------------------------------------------------------------------

// notesRow is a release commit whose changelog holds no notes for the version.
type notesRow struct {
	// build commits the release commit to the trunk and returns it.
	build func(f *gittest.Fixture) string
	name  string
}

func TestRelease_WhenTheReleaseCommitHoldsNoNotes_RefusesNamingIt(t *testing.T) {
	t.Parallel()

	withChangelog := func(content string) func(f *gittest.Fixture) string {
		return func(f *gittest.Fixture) string { return f.CommitChangelog(trunk, releaseSubject, content) }
	}
	tests := []notesRow{
		{
			name: "no changelog, though a later commit adds one",
			build: func(f *gittest.Fixture) string {
				release := f.Commits(trunk, releaseSubject)[0]
				f.CommitChangelog(trunk, "docs: start a changelog", changelog(section(version, notes)))

				return release
			},
		},
		{name: "no section for the version", build: withChangelog(changelog(section("1.1.0", notes)))},
		{
			name:  "only a section for a pre-release of the version",
			build: withChangelog(changelog(section("1.2.0-rc.1", notes), section("1.1.0", notes))),
		},
		{
			name:  "an empty section before the next one",
			build: withChangelog(changelog(section(version, ""), section("1.1.0", notes))),
		},
		{name: "an empty section before the link references", build: withChangelog(changelog(section(version, "")))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := releaseScene(t, func(f *gittest.Fixture, base string) string {
				f.OriginTag(previousTag, base)

				return tt.build(f)
			})

			assertReleaseRefused(t, st, releaseRequest(version, false), expect(shortSHA(st.release)))
		})
	}
}

// ---------------------------------------------------------------------------
// Reads that do not finish
// ---------------------------------------------------------------------------

func TestRelease_WhenOriginStopsAnsweringAtItsTags_RefusesNamingWhatItWasAskedFor(t *testing.T) {
	t.Parallel()
	cmd := releaseCommand(version)
	st := releaseScene(t, tagged)
	// Origin answers the question about its trunk, and nothing after.
	st.s.stopAnswering(t, 1)

	assertReleaseRefused(t, st, cmd.req, unreached(cmd, "ask for its tags"))
}

func TestRelease_WhenTheTimeLimitEndsTheReadOfOriginsTags_RefusesNamingTheRead(t *testing.T) {
	t.Parallel()
	c := byTimeLimit()
	cmd := releaseCommand(version)
	st := releaseScene(t, tagged)
	// Origin answers the question about its trunk; the next call stalls until the time limit ends.
	ctx := c.stalling(1)(t, st.s)
	before := st.s.written()

	got := landing.Release(ctx, st.s.deps, cmd.req)

	assertEndedBy(t, got, st.s.gitDir, ended{outcome: result.Refused, req: cmd.req, text: overtimeText(cmd, c, readTags)})
	assert.Check(t, is.Equal(st.s.written(), before))
}

// ---------------------------------------------------------------------------
// The writes
// ---------------------------------------------------------------------------

func TestRelease_WhenOriginHoldsTheTagAtTheReleaseCommit_GoesOnToTheGitHubReleaseWithoutTheGateOrATag(t *testing.T) {
	t.Parallel()
	st := releaseScene(t, tagged)
	st.s.fixture.OriginTag(releaseTag, st.release)
	st.s.useForge(t, releasable())
	before := st.s.written()

	got := runRelease(t, st.s, releaseRequest(version, false))

	assertEndedBy(t, got, st.s.gitDir, ended{
		outcome: result.Done, req: releaseRequest(version, false),
		text: expect(releaseTag, "already on origin", releaseURL),
	})
	assert.Check(t, is.Equal(st.s.written(), before))
	assert.Check(t, is.Equal(st.s.localTag(releaseTag), ""))
	assert.Check(t, is.Len(commitVerdicts(st.s), 0))
	assert.Check(t, is.Len(callsOf(st.s.forge.Calls(), forgetest.OpPublishRelease), 1))
	assert.Check(t, is.DeepEqual(releasePayload(t, got, st.s.gitDir), released(st, "skipped", releaseURL)))
}

func TestRelease_WhenTheLocalTagIsAtTheReleaseCommit_PushesItWithoutMakingItAgain(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		st := releaseScene(t, tagged)
		st.s.fixture.Git("tag", "--annotate", "--message", "made by hand", releaseTag, st.release)
		object := st.s.fixture.Git("rev-parse", releaseTag)

		got := runRelease(t, st.s, releaseRequest(version, false))

		assertReleaseRead(t, st, got, version)
		assert.Check(t, is.Equal(st.s.fixture.OriginGit("rev-parse", releaseTag), object))
	})
}

func TestRelease_WhenOriginRefusesTheTag_BrokeKeepingTheLocalTagForTheNextRun(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// Origin's hook declines pushes alone, so every read passes and the push of the tag is refused.
		const policy = "tags wait for the freeze to end"
		cmd := releaseCommand(version)
		st := releaseScene(t, tagged)
		st.s.fixture.OriginHook("pre-receive", "echo '"+policy+"'\nexit 1")

		got := runRelease(t, st.s, cmd.req)

		assertEndedBy(t, got, st.s.gitDir, ended{
			outcome: result.Broke, req: cmd.req, text: expect(releaseTag, "not pushed", policy) + cmd.fixed,
		})
		assert.Check(t, is.Equal(st.s.localTag(releaseTag), st.release))
		assert.Check(t, is.Equal(st.s.originTag(releaseTag), ""))
		assert.Check(t, is.Len(releaseCalls(st.s), 0))
		assert.Check(t, is.DeepEqual(releasePayload(t, got, st.s.gitDir), released(st, "green", "")))
	})
}

// failedTagWrite is a git write of the tag that fails, and whether the clone holds the tag after it.
type failedTagWrite struct {
	name string
	// words are the first arguments of the git call that is made to fail.
	words []string
	made  bool
	// cut is whether git's failure leaves origin's copy unknown, so the run may be started again.
	cut bool
}

func TestRelease_WhenAGitWriteOfTheTagFails_BrokeSayingWhereTheTagIs(t *testing.T) {
	// Not parallel: fakeGit puts a failing git first on the scene's PATH.
	tests := []failedTagWrite{
		{name: "the tag cannot be created", words: []string{"tag", "--annotate"}},
		{name: "origin cannot be reached to push it", words: []string{"push"}, made: true, cut: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cmd := releaseCommand(version)
				st := releaseScene(t, tagged)
				failGit(t, st.s, tt.words)

				text := expect(releaseTag, gittest.FailedSaid) + cmd.fixed
				if tt.cut {
					text = expect(releaseTag, "may or may not have taken", gittest.FailedSaid) + cmd.again
				}

				got := runRelease(t, st.s, cmd.req)

				assertEndedBy(t, got, st.s.gitDir, ended{outcome: result.Broke, req: cmd.req, text: text})
				want := ""
				if tt.made {
					want = st.release
				}
				assert.Check(t, is.Equal(st.s.localTag(releaseTag), want))
				assert.Check(t, is.Equal(st.s.originTag(releaseTag), ""))
				assert.Check(t, is.Len(releaseCalls(st.s), 0))
			})
		})
	}
}

// answersBeforeTagPush is how many calls git makes to origin before the push of the tag: origin
// answers about its trunk, its tags, and the fetch of its trunk; the push is next.
const answersBeforeTagPush = 3

// cutTagPush is a push of the tag that ends before git says whether origin took it: the context to
// run under once it changed how the scene reaches origin, the parts the text holds between the tag
// and the phrase that the push may have taken, and the parts the reason holds.
type cutTagPush struct {
	arrange func(t *testing.T, s *scene) context.Context
	name    string
	said    string
	reason  string
}

// cutTagPushes are the ways the push of the tag can end before git says whether origin took it.
func cutTagPushes() []cutTagPush {
	rows := make([]cutTagPush, 0, len(cuts())+2)
	for _, c := range cuts() {
		rows = append(rows, cutTagPush{
			name: "because " + c.name + " ended it", arrange: c.stalling(answersBeforeTagPush),
			said: expect(c.ends), reason: expect(c.ends),
		})
	}

	serving := func(after string) func(t *testing.T, s *scene) context.Context {
		return func(t *testing.T, s *scene) context.Context {
			t.Helper()

			s.deps.Repo.Runner.Env = answering(t, s.fixture, answersBeforeTagPush, after)

			return within(t, 0)
		}
	}

	return append(rows,
		cutTagPush{name: "origin stops answering", arrange: serving("true"), reason: expect("stopped answering")},
		cutTagPush{
			name: "a signal kills the git push", reason: expect("did not finish"),
			arrange: func(t *testing.T, s *scene) context.Context {
				t.Helper()
				gittest.NeedSignals(t)

				return serving(killsGitPush)(t, s)
			},
		},
	)
}

func TestRelease_WhenThePushOfTheTagIsCutShort_BrokeSayingItMayHaveTaken(t *testing.T) {
	t.Parallel()

	for _, tt := range cutTagPushes() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := releaseCommand(version)
			st := releaseScene(t, tagged)
			ctx := tt.arrange(t, st.s)

			got := landing.Release(ctx, st.s.deps, cmd.req)

			assertEndedBy(t, got, st.s.gitDir, ended{
				outcome: result.Broke, req: cmd.req,
				text: expect(releaseTag) + tt.said + expect("may or may not have taken") + cmd.again,
			})
			_, reason := shown(t, got, st.s.gitDir)
			assertSays(t, reason, tt.reason)
			assert.Check(t, is.Equal(st.s.localTag(releaseTag), st.release))
			assert.Check(t, is.Len(releaseCalls(st.s), 0))
		})
	}
}

func TestRelease_WhenTagOnly_EndsOnceOriginHoldsTheTagWithoutAGitHubRelease(t *testing.T) {
	t.Parallel()

	tests := []tagRow{
		{name: "the tag made and pushed", arrange: func(staged) {}},
		{name: "the tag already on origin", arrange: func(st staged) { st.s.fixture.OriginTag(releaseTag, st.release) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				st := releaseScene(t, tagged)
				tt.arrange(st)
				req := releaseRequest(version, true)

				got := runRelease(t, st.s, req)

				assertEndedBy(t, got, st.s.gitDir, ended{outcome: result.Done, req: req, text: expect(releaseTag)})
				assert.Check(t, is.Equal(st.s.originTag(releaseTag), st.release))
				assert.Check(t, is.Len(releaseCalls(st.s), 0))
				payload := releasePayload(t, got, st.s.gitDir)
				assert.Assert(t, payload != nil, "Release() carries no release payload")
				assert.Check(t, payload.TagOnly, "release payload %+v, want tag_only set", payload)
			})
		})
	}
}

func TestRelease_WhenTheTagHasAGitHubRelease_ReportsTheExistingReleaseWithoutPublishing(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		st := releaseScene(t, tagged)
		script := gated(passing())
		script.FindRelease, script.PublishRelease = forgetest.Values(releaseURL), nil
		st.s.useForge(t, script)

		got := runRelease(t, st.s, releaseRequest(version, false))

		assertEndedBy(t, got, st.s.gitDir, ended{outcome: result.Done, req: releaseRequest(version, false), text: expect(releaseURL)})
		want := []forgetest.Call{{Op: forgetest.OpFindRelease, Repo: origin(), Tag: releaseTag}}
		assert.Check(t, is.DeepEqual(releaseCalls(st.s), want, whichRepo()))
		assert.Check(t, is.DeepEqual(releasePayload(t, got, st.s.gitDir), released(st, "green", releaseURL)))
	})
}

// publishRow is a version released with no GitHub release yet, and the changelog of its release
// commit.
type publishRow struct {
	name       string
	version    string
	changelog  string
	notes      string
	prerelease bool
}

func TestRelease_WhenTheTagHasNoGitHubRelease_CreatesOneWithTheChangelogSectionAsItsNotes(t *testing.T) {
	t.Parallel()

	const (
		candidate = "1.2.0-rc.1"
		linked    = "### Added\n\n- A logo, as the [guide] shows.\n\n[guide]: https://example.com/guide\n\n### Fixed\n\n- The banner."
	)
	tests := []publishRow{
		{
			name: "a release, its section before the next one", version: version, notes: "### Added\n\n- A logo.",
			changelog: changelog(section(version, notes), section("1.1.0", notes)),
		},
		{
			name: "a pre-release, its section before the link references", version: candidate, prerelease: true,
			notes: "### Added\n\n- A logo.", changelog: changelog(section(candidate, notes)),
		},
		{
			name: "a section that defines a link of its own", version: version, notes: linked,
			changelog: changelog(section(version, linked+"\n\n"), section("1.1.0", notes)),
		},
		{
			name: "a section whose heading has no date", version: version, notes: "### Added\n\n- A logo.",
			changelog: changelog("## ["+version+"]\n\n"+notes, section("1.1.0", notes)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				st := releaseScene(t, func(f *gittest.Fixture, base string) string {
					f.OriginTag(previousTag, base)

					return f.CommitChangelog(trunk, "chore: release v"+tt.version, tt.changelog)
				})
				req := releaseRequest(tt.version, false)

				got := runRelease(t, st.s, req)

				assertEndedBy(t, got, st.s.gitDir, ended{outcome: result.Done, req: req, text: expect(releaseURL)})
				published := forge.NewRelease{Tag: "v" + tt.version, Notes: tt.notes, Prerelease: tt.prerelease}
				want := []forgetest.Call{
					{Op: forgetest.OpFindRelease, Repo: origin(), Tag: "v" + tt.version},
					{Op: forgetest.OpPublishRelease, Repo: origin(), Release: published},
				}
				assert.Check(t, is.DeepEqual(releaseCalls(st.s), want, whichRepo()))
				payload := &result.Release{
					Version: tt.version, Tag: "v" + tt.version, SHA: st.release, Verdict: "green",
					Prerelease: tt.prerelease, URL: releaseURL,
				}
				assert.Check(t, is.DeepEqual(releasePayload(t, got, st.s.gitDir), payload))
			})
		})
	}
}

// unpublishedRow is a forge call of the GitHub release that fails, and the calls of the release
// made by then.
type unpublishedRow struct {
	fail func(script *forgetest.Script)
	name string
	ops  []forgetest.Op
}

func TestRelease_WhenTheGitHubReleaseCannotBeCreated_BrokeWithTheTagOnOrigin(t *testing.T) {
	t.Parallel()

	failed := []forgetest.Answer[string]{{Err: errors.New(forgeFailure)}}
	tests := []unpublishedRow{
		{
			name: "the release of the tag cannot be looked up",
			fail: func(script *forgetest.Script) { script.FindRelease = failed },
			ops:  []forgetest.Op{forgetest.OpFindRelease},
		},
		{
			name: "the release cannot be created",
			fail: func(script *forgetest.Script) { script.PublishRelease = failed },
			ops:  []forgetest.Op{forgetest.OpFindRelease, forgetest.OpPublishRelease},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				cmd := releaseCommand(version)
				st := releaseScene(t, tagged)
				script := gated(passing())
				tt.fail(&script)
				st.s.useForge(t, script)

				got := runRelease(t, st.s, cmd.req)

				assertEndedBy(t, got, st.s.gitDir, ended{
					outcome: result.Broke, req: cmd.req,
					text: expect(releaseTag, "on origin", "not created", forgeFailure) + cmd.again,
				})
				assert.Check(t, is.DeepEqual(opsOf(releaseCalls(st.s)), tt.ops))
				assert.Check(t, is.Equal(st.s.originTag(releaseTag), st.release))
				assert.Check(t, is.DeepEqual(releasePayload(t, got, st.s.gitDir), released(st, "green", "")))
			})
		})
	}
}

func TestRelease_WhenPublishingTheGitHubReleaseIsCutShort_BrokeSayingItMayHaveGoneThrough(t *testing.T) {
	t.Parallel()

	for _, c := range cuts() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				cmd := releaseCommand(version)
				st := releaseScene(t, tagged)
				script := gated(passing())
				script.PublishRelease = []forgetest.Answer[string]{{Err: c.of("gh release create")}}
				st.s.useForge(t, script)

				got := runRelease(t, st.s, cmd.req)

				assertEndedBy(t, got, st.s.gitDir, ended{
					outcome: result.Broke, req: cmd.req,
					text: expect(releaseTag, c.ends, "may or may not have gone through") + cmd.again,
				})
				assert.Check(t, is.Equal(st.s.originTag(releaseTag), st.release))
			})
		})
	}
}

func TestRelease_WhenTheTrunkHasNoCI_CarriesAPayloadNamingNoneAsTheVerdict(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		script := gated(forge.Verdict{})
		script.TrunkProtection = forgetest.Values(forge.Protection{})
		script.HasTrunkCI = forgetest.Values(false)
		st := releaseScene(t, tagged)
		st.s.useForge(t, script)
		req := releaseRequest(version, false)

		got := runRelease(t, st.s, req)

		assertStoppedBy(t, got, result.Done, req)
		assert.Check(t, is.DeepEqual(releasePayload(t, got, st.s.gitDir), released(st, "none", releaseURL)))
	})
}

// failedRelease is one git read of a release that fails for a reason no refusal covers.
type failedRelease struct {
	// build commits the release commit on the trunk, as releaseScene's build does.
	build func(f *gittest.Fixture, base string) string
	name  string
	read  string
	// words are the first arguments of the git call that is made to fail.
	words []string
}

func TestRelease_WhenAGitReadFailsForNoRefusal_BrokeNamingTheRead(t *testing.T) {
	// Not parallel: each row puts a failing git first on PATH with t.Setenv.
	firstParentLog := []string{"log", "--reverse", "--topo-order", "--format=%H %s", "--first-parent"}
	tests := []failedRelease{
		{name: "history since the previous tag", build: tagged, read: readHistory, words: firstParentLog},
		{
			name: "history with no previous tag", read: readHistory, words: firstParentLog,
			build: func(f *gittest.Fixture, _ string) string { return releaseCommit(f) },
		},
		{
			name: "local tag", build: tagged, read: readLocalTag,
			words: []string{"rev-parse", "--verify", "--quiet", "refs/tags/" + releaseTag + "^{commit}"},
		},
		{name: "changelog", build: tagged, read: readChangelog, words: []string{"cat-file", "blob"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := releaseCommand(version)
			st := releaseScene(t, tt.build)
			failGit(t, st.s, tt.words)
			before := st.s.written()

			got := runRelease(t, st.s, cmd.req)

			assertFailedGit(t, got, cmd, tt.read)
			assert.Check(t, is.Equal(st.s.written(), before))
		})
	}
}

func TestRelease_WhenOriginStopsAnsweringAtThePreviousTag_RefusesNamingTheTag(t *testing.T) {
	t.Parallel()
	cmd := releaseCommand(version)
	st := releaseScene(t, pastATagTheCloneLacks)
	// The fetch of the tag is the first call to origin after those before the push of the tag.
	st.s.stopAnswering(t, answersBeforeTagPush)

	assertReleaseRefused(t, st, cmd.req, expect("could not be reached", previousTag)+cmd.again)
}
