package landing_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

const (
	// version is the version the tests release, releaseTag its tag, and releaseSubject the subject
	// of its release commit.
	version        = "1.2.0"
	releaseTag     = "v" + version
	releaseSubject = "chore: release " + releaseTag

	// previousTag is the version tag below version that the tests put on origin.
	previousTag = "v1.1.0"

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
func released(st staged, verdict result.ReleaseVerdict, url string) *result.Release {
	return &result.Release{Version: version, Tag: releaseTag, SHA: st.release, Verdict: verdict, URL: url}
}

// uncheckedIf is the payload of st's release stopped before its gate read the checks when carried
// is set, and nil otherwise.
func uncheckedIf(st staged, carried bool) *result.Release {
	if !carried {
		return nil
	}

	return released(st, "unchecked", "")
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
	return commandNamed("release "+v, releaseRequest(v, false))
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

// releaseRefusal is what a refused release says: its text, and its release payload, nil for none.
type releaseRefusal struct {
	payload *result.Release
	text    string
}

// releaseRun is one run of req on a release scene: what the scene had written before it, and the
// result it gave.
type releaseRun struct {
	req    landing.Request
	before string
	got    result.Result
}

// assertReleaseRefused fails unless run ends refused as want says, and wrote nothing.
func assertReleaseRefused(t *testing.T, st staged, run releaseRun, want releaseRefusal) {
	t.Helper()

	assertEndedBy(t, run.got, st.s.gitDir, ended{outcome: result.Refused, req: run.req, text: want.text})
	assert.Check(t, is.Equal(st.s.written(), run.before))
	assert.Check(t, is.DeepEqual(releasePayload(t, run.got, st.s.gitDir), want.payload))
}

// ---------------------------------------------------------------------------
// The version
// ---------------------------------------------------------------------------

func TestRelease_WhenTheVersionIsNotXYZ_RefusesNamingIt(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, in string }{
		{name: "leading v", in: "v1.2.0"},
		{name: "missing patch", in: "1.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := releaseRequest(tt.in, false)

			got := landing.Release(t.Context(), landing.Deps{}, req)

			assertEndedBy(t, got, "", ended{outcome: result.Refused, req: req, text: expect(tt.in)})
		})
	}
}

// orderRow is one set of tags origin or the clone holds, which version is released over.
type orderRow struct {
	arrange func(f *gittest.Fixture, base string)
	name    string
	// highest is the tag the refusal names.
	highest string
}

// build arranges row's tags and commits the release commit of version, as releaseScene's build
// does.
func (row orderRow) build(f *gittest.Fixture, base string) string {
	row.arrange(f, base)

	return releaseOf(f, version, "chore: release v"+version)
}

func TestRelease_WhenOriginHoldsAVersionTagNotBelowIt_RefusesNamingTheHighest(t *testing.T) {
	t.Parallel()

	tests := []orderRow{
		{
			name: "a version below two higher ones", highest: "v1.10.0",
			arrange: func(f *gittest.Fixture, base string) {
				f.OriginTag("v1.10.0", base)
				f.OriginTag("v1.3.0", base)
			},
		},
		{
			name: "the same version under a shorter name", highest: "v1.2",
			arrange: func(f *gittest.Fixture, base string) { f.OriginTag("v1.2", base) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := releaseScene(t, tt.build)
			req := releaseRequest(version, false)
			before := st.s.written()

			got := runRelease(t, st.s, req)

			assertReleaseRefused(t, st, releaseRun{req, before, got}, releaseRefusal{text: expect(tt.highest)})
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
			req := releaseRequest(version, false)
			before := st.s.written()

			got := runRelease(t, st.s, req)

			assertReleaseRefused(t, st, releaseRun{req, before, got}, releaseRefusal{text: tt.want})
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
	req := releaseRequest(version, false)
	before := st.s.written()

	got := runRelease(t, st.s, req)

	assertReleaseRefused(t, st, releaseRun{req, before, got}, releaseRefusal{
		text: expect(releaseTag, shortSHA(st.base)), payload: released(st, "unchecked", ""),
	})
}

func TestRelease_WhenTheLocalTagIsAtAnotherCommit_RefusesWithANextThatDeletesIt(t *testing.T) {
	t.Parallel()
	st := releaseScene(t, tagged)
	st.s.fixture.Git("tag", releaseTag, st.base)
	req := releaseRequest(version, false)
	before := st.s.written()

	got := runRelease(t, st.s, req)

	assertReleaseRefused(t, st, releaseRun{req, before, got}, releaseRefusal{
		text:    expect(releaseTag, shortSHA(st.base), "Next:", "git tag --delete "+releaseTag, "ils release "+version),
		payload: released(st, "unchecked", ""),
	})
	assert.Check(t, is.Equal(st.s.fixture.Git("rev-parse", releaseTag+"^{commit}"), st.base))
}

func TestRelease_WhenOriginsTagIsAtTheReleaseCommitAndTheLocalOneElsewhere_EndsDoneDespiteTheLocalTag(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		st := releaseScene(t, tagged)
		st.s.fixture.OriginTag(releaseTag, st.release)
		st.s.fixture.Git("tag", releaseTag, st.base)

		got := runRelease(t, st.s, releaseRequest(version, false))

		assertReleaseRead(t, st, got, version)
	})
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
		{
			name:  "only a section for a pre-release of the version",
			build: withChangelog(changelog(section("1.2.0-rc.1", notes), section("1.1.0", notes))),
		},
		{
			name:  "an empty section before the next one",
			build: withChangelog(changelog(section(version, ""), section("1.1.0", notes))),
		},
		{name: "an empty section before the link references", build: withChangelog(changelog(section(version, "")))},
		{
			name: "no section for the version, on a run resumed with origin's tag at it",
			build: func(f *gittest.Fixture) string {
				release := withChangelog(changelog(section("1.1.0", notes)))(f)
				f.OriginTag(releaseTag, release)

				return release
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := releaseScene(t, func(f *gittest.Fixture, base string) string {
				f.OriginTag(previousTag, base)

				return tt.build(f)
			})

			req := releaseRequest(version, false)
			before := st.s.written()

			got := runRelease(t, st.s, req)

			assertReleaseRefused(t, st, releaseRun{req, before, got}, releaseRefusal{
				text: expect(shortSHA(st.release)), payload: released(st, "unchecked", ""),
			})
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

	before := st.s.written()

	got := runRelease(t, st.s, cmd.req)

	assertReleaseRefused(t, st, releaseRun{cmd.req, before, got}, releaseRefusal{text: unreached(cmd, "ask for its tags")})
}

// ---------------------------------------------------------------------------
// The writes
// ---------------------------------------------------------------------------

func TestRelease_WhenOriginHoldsTheTagAtTheReleaseCommit_GoesOnToTheGitHubReleaseWithoutTheGateOrATag(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
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
		assert.Check(t, is.DeepEqual(releasePayload(t, got, st.s.gitDir), released(st, "skipped", releaseURL)))
	})
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
		assert.Check(t, is.Len(releaseCalls(st.s), 0))
		assert.Check(t, is.DeepEqual(releasePayload(t, got, st.s.gitDir), released(st, "green", "")))
	})
}

func TestRelease_WhenTheTagCannotBeCreated_BrokeSayingNothingWasWritten(t *testing.T) {
	// Not parallel: failGit puts a failing git first on the scene's PATH.
	synctest.Test(t, func(t *testing.T) {
		cmd := releaseCommand(version)
		st := releaseScene(t, tagged)
		failGit(t, st.s, []string{"tag", "--annotate"})
		before := st.s.written()

		got := runRelease(t, st.s, cmd.req)

		assertEndedBy(t, got, st.s.gitDir, ended{
			outcome: result.Broke, req: cmd.req, text: expect(releaseTag, gittest.FailedSaid) + cmd.fixed,
		})
		assert.Check(t, is.Equal(st.s.written(), before))
		assert.Check(t, is.Equal(st.s.localTag(releaseTag), ""))
		assert.Check(t, is.Len(releaseCalls(st.s), 0))
	})
}

func TestRelease_WhenAnInterruptLandsBeforeTheFirstWrite_EndsPendingWithNothingWritten(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		cmd := releaseCommand(version)
		// The interrupt lands as the gate reads the checks, the last read before the tag is made.
		done := make(chan struct{})
		interrupt := sync.OnceFunc(func() { close(done) })
		script := gated(passing())
		script.OnRead = func(call forgetest.Call) {
			if call.Op == forgetest.OpCommitVerdict {
				interrupt()
			}
		}
		run := gateScene(t, script)

		got := landing.Release(byInterrupt().once(within(t, 0), done), run.st.s.deps, cmd.req)

		assertEndedBy(t, got, run.st.s.gitDir, ended{
			outcome: result.Pending, req: cmd.req, text: expect("interrupted") + cmd.again,
		})
		assert.Check(t, is.Equal(run.st.s.written(), run.before))
		assert.Check(t, is.Equal(run.st.s.localTag(releaseTag), ""))
		assert.Check(t, is.Len(releaseCalls(run.st.s), 0))
		assert.Check(t, is.DeepEqual(releasePayload(t, got, run.st.s.gitDir), released(run.st, "green", "")))
	})
}

// cutTagNext is the `Next:` line a release ends on once c cut short the making or the push of the
// tag: the short time limit names the long wait, as a push whose hooks, or a tag whose signing,
// outlast 100 seconds ends the same way every time under it.
func cutTagNext(c cut) string {
	if errors.Is(c.err, context.DeadlineExceeded) {
		return nextLine("ils release " + version + " --wait")
	}

	return releaseCommand(version).again
}

func TestRelease_WhenACutEndsTheMakingOfTheTag_SaysWhatEndedIt(t *testing.T) {
	// Not parallel: stallingGit puts a stalling git first on PATH with t.Setenv.
	for _, c := range cuts() {
		t.Run(c.name, func(t *testing.T) {
			cmd := releaseCommand(version)
			st := releaseScene(t, tagged)
			ctx := c.stallingGit([]string{"tag", "--annotate"})(t, st.s)

			got := landing.Release(ctx, st.s.deps, cmd.req)

			assertEndedBy(t, got, st.s.gitDir, ended{
				outcome: result.Broke, req: cmd.req, text: expect(releaseTag) + expect(c.ends) + cutTagNext(c),
			})
			_, reason := shown(t, got, st.s.gitDir)
			assertSays(t, reason, expect(c.ends))
			assert.Check(t, is.Len(releaseCalls(st.s), 0))
		})
	}
}

// answersBeforeTagPush is how many calls git makes to origin before the push of the tag: origin
// answers about its trunk, its tags, and the fetch of its trunk; the push is next.
const answersBeforeTagPush = 3

// cutTagPush is a push of the tag that ends before git says whether origin took it: the context to
// run under once it changed how the scene reaches origin, the parts the text holds between the tag
// and the phrase that the push may have taken, the parts it holds after that phrase, and the parts
// the reason holds.
type cutTagPush struct {
	arrange func(t *testing.T, s *scene) context.Context
	name    string
	said    string
	taken   string
	reason  string
	// again is the `Next:` line the run ends on.
	again string
}

// cutTagPushes are the ways the push of the tag can end before git says whether origin took it.
func cutTagPushes() []cutTagPush {
	// dropped is what the ssh of the push prints when origin stops answering.
	const dropped = "Connection closed by remote host."
	again := releaseCommand(version).again
	cutBy := func(c cut, next string) cutTagPush {
		return cutTagPush{
			name: "because " + c.name + " ended it", arrange: c.stalling(answersBeforeTagPush),
			said: expect(c.ends), reason: expect(c.ends), again: next,
		}
	}
	// The short time limit names the long wait, since a push whose hooks outlast 100 seconds ends
	// the same way every time under it.
	rows := []cutTagPush{
		cutBy(byTimeLimit(), nextLine("ils release "+version+" --wait")),
		cutBy(byInterrupt(), again),
	}

	serving := func(after string) func(t *testing.T, s *scene) context.Context {
		return func(t *testing.T, s *scene) context.Context {
			t.Helper()

			s.deps.Repo.Runner.Env = answering(t, s.fixture, answersBeforeTagPush, after)

			return t.Context()
		}
	}

	return append(rows,
		cutTagPush{
			name: "origin stops answering", arrange: serving("echo '" + dropped + "' >&2"),
			taken: expect(dropped), reason: expect("stopped answering"), again: again,
		},
		cutTagPush{
			name: "a signal kills the git push", reason: expect("did not finish"), again: again,
			arrange: serving(killsGitPush),
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
				text: expect(releaseTag) + tt.said + expect("may or may not have taken") + tt.taken + tt.again,
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
	synctest.Test(t, func(t *testing.T) {
		st := releaseScene(t, tagged)
		req := releaseRequest(version, true)

		got := runRelease(t, st.s, req)

		assertEndedBy(t, got, st.s.gitDir, ended{outcome: result.Done, req: req, text: expect(releaseTag)})
		assert.Check(t, is.Equal(st.s.originTag(releaseTag), st.release))
		assert.Check(t, is.Len(releaseCalls(st.s), 0))
	})
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
				tag := "v" + tt.version
				published := forge.NewRelease{Tag: tag, Notes: tt.notes, Prerelease: tt.prerelease}
				want := []forgetest.Call{
					{Op: forgetest.OpFindRelease, Repo: origin(), Tag: tag},
					{Op: forgetest.OpPublishRelease, Repo: origin(), Release: published},
				}
				assert.Check(t, is.DeepEqual(releaseCalls(st.s), want, whichRepo()))
				payload := &result.Release{
					Version: tt.version, Tag: tag, SHA: st.release, Verdict: "green",
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

func TestRelease_WhenACutEndsTheLookupOfTheGitHubRelease_SaysWhatEndedIt(t *testing.T) {
	t.Parallel()

	for _, c := range cuts() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				cmd := releaseCommand(version)
				st := releaseScene(t, tagged)
				script := gated(passing())
				script.FindRelease = []forgetest.Answer[string]{{Err: c.of("gh release view")}}
				script.PublishRelease = nil
				st.s.useForge(t, script)

				got := runRelease(t, st.s, cmd.req)

				assertEndedBy(t, got, st.s.gitDir, ended{
					outcome: result.Broke, req: cmd.req, text: expect(releaseTag, c.ends) + cmd.again,
				})
				_, reason := shown(t, got, st.s.gitDir)
				assertSays(t, reason, expect(c.ends))
				assert.Check(t, is.DeepEqual(opsOf(releaseCalls(st.s)), []forgetest.Op{forgetest.OpFindRelease}))
				assert.Check(t, is.Equal(st.s.originTag(releaseTag), st.release))
				assert.Check(t, is.DeepEqual(releasePayload(t, got, st.s.gitDir), released(st, "green", "")))
			})
		})
	}
}

// notesLeft is a forge whose release is created while its notes file stays behind: its
// PublishRelease answers as the fake does, then fails with left once the fake gave a link.
type notesLeft struct {
	*forgetest.Fake

	left error
}

// PublishRelease returns the link the fake answers with left, or the fake's error when it fails.
func (f notesLeft) PublishRelease(ctx context.Context, repo forge.Repo, release forge.NewRelease) (string, error) {
	link, err := f.Fake.PublishRelease(ctx, repo, release)
	if err != nil {
		return "", err
	}

	return link, f.left
}

func TestRelease_WhenTheNotesFileOfACreatedReleaseStaysBehind_DoneNamingIt(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const notesFile = "/tmp/ils-release-notes-417.md"
		req := releaseRequest(version, false)
		st := releaseScene(t, tagged)
		left := &fs.PathError{Op: "remove", Path: notesFile, Err: errors.New("directory not empty")}
		st.s.deps.Forge = notesLeft{Fake: st.s.forge, left: fmt.Errorf("remove the release notes: %w", left)}

		got := runRelease(t, st.s, req)

		assertEndedBy(t, got, st.s.gitDir, ended{outcome: result.Done, req: req, text: expect(releaseURL)})
		text, _ := shown(t, got, st.s.gitDir)
		assertSays(t, text, expect(notesFile))
		assert.Check(t, is.DeepEqual(opsOf(releaseCalls(st.s)), []forgetest.Op{forgetest.OpFindRelease, forgetest.OpPublishRelease}))
		assert.Check(t, is.DeepEqual(releasePayload(t, got, st.s.gitDir), released(st, "green", releaseURL)))
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
	// payload is whether the result carries the release payload: once the release commit is found.
	payload bool
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
			name: "local tag", build: tagged, read: readLocalTag, payload: true,
			words: []string{"rev-parse", "--verify", "--quiet", "refs/tags/" + releaseTag + "^{commit}"},
		},
		{name: "changelog", build: tagged, read: readChangelog, words: []string{"cat-file", "blob"}, payload: true},
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
			assert.Check(t, is.DeepEqual(releasePayload(t, got, st.s.gitDir), uncheckedIf(st, tt.payload)))
		})
	}
}

func TestRelease_WhenOriginStopsAnsweringAtThePreviousTag_RefusesNamingTheTag(t *testing.T) {
	t.Parallel()
	cmd := releaseCommand(version)
	st := releaseScene(t, pastATagTheCloneLacks)
	// The fetch of the tag is the first call to origin after those before the push of the tag.
	st.s.stopAnswering(t, answersBeforeTagPush)
	before := st.s.written()

	got := runRelease(t, st.s, cmd.req)

	assertReleaseRefused(t, st, releaseRun{cmd.req, before, got}, releaseRefusal{text: expect("could not be reached", previousTag) + cmd.again})
}
