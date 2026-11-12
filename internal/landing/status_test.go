package landing_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

const (
	// closed is the number of a pull request from the topic branch that was closed without merging.
	closed = 5

	standingBranch result.Standing = "branch"
)

// statusRequest is the command and words of a bare `ils status`.
func statusRequest() landing.Request {
	return landing.Request{Command: "status", Words: []string{"status"}}
}

func publishTopic(f *gittest.Fixture) { f.Publish(topic) }

// greenOpen is viewing with the open pull request 12 on a clean head and passing checks.
func greenOpen() forgeFor { return viewing(cleanHead(), passing(), standing()) }

// viewing is a forge that answers every read a status may make: Acme/Widgets as the user's to push
// to, found as the branch's pull requests, a trunk that requires no check and has CI, head as the
// open pull request's head on the tip, verdict as its checks, and a message titled as the open pull
// request is, with the body landedBody.
func viewing(head forge.Head, verdict forge.Verdict, found ...forge.Change) forgeFor {
	return func(tip string) forgetest.Script {
		placed := slices.Clone(found)
		title := topic

		for i := range placed {
			if placed[i].State == forge.ChangeOpen {
				placed[i].HeadSHA = tip
				title = placed[i].Title
			}
		}

		// Parallel tests may share one script builder, so head is copied, never written.
		shown := head
		shown.SHA = tip
		script := finding(placed...)
		script.TrunkProtection = forgetest.Values(forge.Protection{})
		script.HasTrunkCI = forgetest.Values(true)
		script.Head = shows(shown)
		script.Verdict = says(verdict)
		script.Message = forgetest.Values(forge.Message{Title: title, Body: landedBody})

		return script
	}
}

// statusScene is a scene changed by arrange whose forge answers as build says for the commit HEAD
// is on once arrange has run.
func statusScene(t *testing.T, arrange func(*gittest.Fixture), build forgeFor) *scene {
	t.Helper()

	return statusSceneAt(t, arrange, build, false)
}

// offlineStatusScene is statusScene with an origin that does not answer.
func offlineStatusScene(t *testing.T, arrange func(*gittest.Fixture), build forgeFor) *scene {
	t.Helper()

	return statusSceneAt(t, arrange, build, true)
}

// statusSceneAt is statusScene whose origin does not answer when offline is set.
func statusSceneAt(t *testing.T, arrange func(*gittest.Fixture), build forgeFor, offline bool) *scene {
	t.Helper()

	s := newScene(t, arrange, forgetest.Script{}, offline)
	s.useForge(t, build(s.fixture.Git("rev-parse", "HEAD")))

	return s
}

// seen is what a status showed: its text, and the status object and `next` its JSON carries.
type seen struct {
	status *result.Status
	text   string
	next   string
}

// seeStatus runs `ils status` on the scene under the context under makes for t, and fails unless it
// ended done, carrying the command and words of statusRequest, with nothing a user owns written. It
// returns the text and the JSON's status object, and stops the test when the JSON carries none.
func seeStatus(t *testing.T, s *scene, under func(*testing.T) context.Context) seen {
	t.Helper()

	before := s.written()

	got := landing.Status(under(t), s.deps, statusRequest())

	assertStoppedBy(t, got, result.Done, statusRequest())
	assert.Check(t, is.Equal(s.written(), before))

	text, err := result.RenderStyled(got, s.gitDir, colorprofile.NoTTY)
	assert.NilError(t, err)

	out, err := result.RenderJSON(got, s.gitDir)
	assert.NilError(t, err)

	decoded := resulttest.Decode(t, out)
	payload := decoded.Payload
	assert.Assert(t, payload != nil && payload.Status != nil, "RenderJSON() = %s, want a status object", out)

	return seen{text: text, status: payload.Status, next: decoded.Next}
}

// statusCommand is `ils status` with the `Next:` lines that name it.
func statusCommand() landingCommand { return commandNamed("status", statusRequest()) }

// assertEndsOn fails unless both the `Next:` line of the text and the `next` of the JSON hold want.
func assertEndsOn(t *testing.T, got seen, want string) {
	t.Helper()

	assertSays(t, nextOf(t, got.text), want)
	assertSays(t, got.next, want)
}

// ---------------------------------------------------------------------------
// The branch against origin's copy
// ---------------------------------------------------------------------------

// copyRow is one way the branch stands against origin's copy of it.
type copyRow struct {
	arrange func(*gittest.Fixture)
	name    string
	// said is the phrase of the text that tells the standing apart.
	said          string
	ahead, behind int
	onOrigin      bool
}

func TestStatus_WhenOnAWorkBranch_ReportsHowItStandsAgainstOriginsCopy(t *testing.T) {
	t.Parallel()

	tests := []copyRow{
		{name: "not on origin", said: "not on origin"},
		{name: "up to date", said: "up to date", onOrigin: true, arrange: publishTopic},
		{
			name: "ahead", said: "ahead", ahead: 2, onOrigin: true,
			arrange: func(f *gittest.Fixture) {
				f.Publish(topic)
				f.Commits(topic, "fix a typo", "add an example")
			},
		},
		{
			name: "behind", said: "behind origin's copy of it by 1 commit\n", behind: 1, onOrigin: true,
			arrange: func(f *gittest.Fixture) {
				f.Publish(topic)
				f.AdvanceOrigin(topic)
			},
		},
		{
			name: "diverged", said: "diverged", ahead: 2, behind: 1, onOrigin: true,
			arrange: func(f *gittest.Fixture) {
				f.Publish(topic)
				f.Diverge(topic)
				f.Commits(topic, "fix a typo")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := statusScene(t, tt.arrange, viewing(cleanHead(), passing()))

			got := seeStatus(t, s, (*testing.T).Context)

			assertSays(t, got.text, expect(topic, tt.said))
			assert.Check(t, is.Equal(got.status.Standing, standingBranch))
			assert.Check(t, is.Equal(got.status.Branch, topic))
			assert.Check(t, is.Equal(got.status.Ahead, tt.ahead))
			assert.Check(t, is.Equal(got.status.Behind, tt.behind))
			assert.Check(t, is.Equal(got.status.OnOrigin, tt.onOrigin))
		})
	}
}

// ---------------------------------------------------------------------------
// Next
// ---------------------------------------------------------------------------

// nextRow is one state of a work branch and the `Next:` line its status ends on.
type nextRow struct {
	// arrange changes the repository and returns the parts of the `Next:` line.
	arrange func(*gittest.Fixture) string
	script  forgeFor
	name    string
}

// published returns an arrange that publishes the topic branch and ends on next.
func published(next string) func(*gittest.Fixture) string {
	return func(f *gittest.Fixture) string {
		f.Publish(topic)

		return next
	}
}

// landedSteps are the states whose `Next:` line a merged pull request decides.
func landedSteps() []nextRow {
	landed := viewing(cleanHead(), passing(), change(merged, trunk, forge.ChangeMerged))

	return []nextRow{
		{
			name: "merged pull request carries every commit", script: landed,
			arrange: func(f *gittest.Fixture) string {
				f.LandPull(merged, topic)

				return nextBranch()
			},
		},
		{
			name: "merged pull request carries every commit while behind origin's copy", script: landed,
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				f.AdvanceOrigin(topic)
				f.LandPull(merged, topic)

				return nextBranch()
			},
		},
		{
			name: "merged pull request lacks a commit made since", script: landed,
			arrange: func(f *gittest.Fixture) string {
				f.LandPull(merged, topic)
				later := f.Commits(topic, "fix a typo")

				return nextBranch() + expect("`git cherry-pick "+later[0]+"`")
			},
		},
	}
}

// copySteps are the states whose `Next:` line origin's copy of the branch decides.
func copySteps() []nextRow {
	return []nextRow{
		{
			name: "behind origin's copy with a green pull request", script: greenOpen(),
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				f.AdvanceOrigin(topic)

				return pushCommand().after("`" + pullFrom(topic) + "`")
			},
		},
		{
			name: "ahead of origin's copy with a green pull request", script: greenOpen(),
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				f.Commits(topic, "fix a typo")

				return nextPush()
			},
		},
		{
			name: "on origin with no open pull request", script: viewing(cleanHead(), passing()),
			arrange: published(nextPush()),
		},
	}
}

// pullSteps are the states whose `Next:` line the open pull request decides.
func pullSteps() []nextRow {
	conflicting := inState(forge.MergeConflicting)
	review := inState(forge.MergeBlockedByReview)

	return []nextRow{
		{
			name: "checks still running while conflicting with the trunk", script: viewing(conflicting, running(), standing()),
			arrange: published(nextPush()),
		},
		{name: "red while conflicting with the trunk", script: viewing(conflicting, lintRed(), standing()), arrange: published(nextRed())},
		{name: "conflicting with the trunk", script: viewing(conflicting, passing(), standing()), arrange: published(nextTrunk())},
		{name: "waiting on a review", script: viewing(review, passing(), standing()), arrange: published(expect("request a review"))},
		{name: "green and mergeable", script: greenOpen(), arrange: published(nextMerge())},
	}
}

// elsewhereSteps are the states whose `Next:` line an open pull request against a base other than
// the trunk decides, ahead of every step push would refuse after it, and one push refuses before it.
func elsewhereSteps() []nextRow {
	elsewhere := pushCommand().after("`" + baseCommand + "`")
	against := viewing(cleanHead(), passing(), change(opened, otherBase, forge.ChangeOpen))

	return []nextRow{
		{name: "open pull request against another base", script: against, arrange: published(elsewhere)},
		{
			// Push refuses a diverged branch before it looks for the pull request.
			name: "diverged from origin's copy beside an open pull request against another base", script: against,
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				f.Diverge(topic)

				return pushCommand().after("`" + originMerge + topic + "`")
			},
		},
		{
			name: "open pull request against another base beside a merged one that shares a commit",
			script: viewing(cleanHead(), passing(),
				change(merged, trunk, forge.ChangeMerged), change(opened, otherBase, forge.ChangeOpen)),
			arrange: func(f *gittest.Fixture) string {
				f.LandPull(merged, topic)

				return elsewhere
			},
		},
	}
}

// precedenceSteps are the states where a step push takes before every other decides `Next:`: the
// refusal of a repository ils does not push to, and the lack of a commit past the trunk ahead of an
// open pull request against another base.
func precedenceSteps() []nextRow {
	return []nextRow{
		{
			name: "fork while behind origin's copy", script: func(string) forgetest.Script { return answers(widgets(true, true)) },
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				f.AdvanceOrigin(topic)

				return expect("stop and tell the user to push by hand")
			},
		},
		{
			name:   "no commit past the trunk beside an open pull request against another base",
			script: viewing(cleanHead(), passing(), change(opened, otherBase, forge.ChangeOpen)),
			arrange: func(f *gittest.Fixture) string {
				f.Git("switch", "--quiet", "--create", spare, trunk)

				return expect("commit the work", "ils push")
			},
		},
	}
}

// refusedSteps are the states in which push or merge would refuse a step the list above names, so
// that status ends on the refusal's own step instead.
func refusedSteps() []nextRow {
	return []nextRow{
		{
			name: "changed tracked files", script: greenOpen(),
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				f.DirtyFile()

				return expect("commit them", "ils push again")
			},
		},
		{
			name: "unpublished branch whose first commit another local branch carries", script: viewing(cleanHead(), passing()),
			arrange: func(f *gittest.Fixture) string {
				f.Git("switch", "--quiet", "--create", spare, topic)
				f.Commits(spare, "more work")

				return expect("run ils push on the other branch")
			},
		},
		{
			name: "green and mergeable with a local trunk ahead of origin's", script: greenOpen(),
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				f.AdvanceTrunk()

				return expect("move those commits off the local trunk by hand", "ils merge again")
			},
		},
	}
}

// nextOf returns the text after the `Next:` marker of text, and fails the test when text has none.
func nextOf(t *testing.T, text string) string {
	t.Helper()

	_, next, found := strings.Cut(text, "Next:")
	assert.Check(t, found, "text %q, want a Next: line", text)

	return next
}

func TestStatus_WhenOnAWorkBranch_EndsOnTheFirstNextThatHolds(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(precedenceSteps(), refusedSteps(), elsewhereSteps(), landedSteps(), copySteps(), pullSteps()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var want string

			s := statusScene(t, func(f *gittest.Fixture) { want = tt.arrange(f) }, tt.script)

			got := seeStatus(t, s, (*testing.T).Context)

			assertSays(t, "Next:"+nextOf(t, got.text), want)
		})
	}
}

// ---------------------------------------------------------------------------
// Reads that fail
// ---------------------------------------------------------------------------

func TestStatus_WhenAForgeReadFails_ReportsTheFailedReadBesideTheRest(t *testing.T) {
	t.Parallel()
	s := statusScene(t, publishTopic,
		func(string) forgetest.Script { return failingToFind(errors.New(forgeFailure)) })

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, expect(readChanges, forgeFailure))
	assert.Assert(t, is.Len(got.status.Unread, 1))
	assert.Check(t, is.Equal(got.status.Unread[0].Read, readChanges))
	assert.Check(t, is.Contains(got.status.Unread[0].Error, forgeFailure))
	assert.Check(t, got.status.OnOrigin, "the branch's copy on origin went unreported beside the failed read")
}

func TestStatus_WhenAGitReadFails_ReportsTheFailedReadBesideTheRest(t *testing.T) {
	// Not parallel: failGit puts a failing git first on PATH with t.Setenv.
	s := statusScene(t, publishTopic, greenOpen())
	failGit(t, s, []string{"rev-list", "--left-right", "--count"})

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, expect(readCopy, gittest.FailedSaid))
	assert.Assert(t, is.Len(got.status.Unread, 1))
	assert.Check(t, is.Equal(got.status.Unread[0].Read, readCopy))
	assert.Assert(t, got.status.PullRequest != nil)
}

// aheadOfCopy publishes the topic branch and adds a commit past origin's copy of it.
func aheadOfCopy(f *gittest.Fixture) {
	f.Publish(topic)
	f.Commits(topic, "fix a typo")
}

// failCopyFetch makes origin answer the questions about its trunk and whether it has the branch, and
// fail the fetch of the branch's copy after them.
func failCopyFetch(t *testing.T, s *scene) {
	t.Helper()

	s.stopAnswering(t, 3)
}

func TestStatus_WhenTheFetchOfOriginsCopyFails_ListsTheReadWithoutCountingTheCopy(t *testing.T) {
	t.Parallel()
	s := statusScene(t, aheadOfCopy, greenOpen())
	failCopyFetch(t, s)

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, hasUnread(got.status, readCopy), "unread %v, want %s listed", got.status.Unread, readCopy)
	assert.Check(t, !got.status.OnOrigin, "status carries the copy on origin, %d ahead and %d behind, though its fetch failed",
		got.status.Ahead, got.status.Behind)
}

// failingGit returns a fail that makes the scene's git calls whose arguments start with words fail.
func failingGit(words ...string) func(t *testing.T, s *scene) {
	return func(t *testing.T, s *scene) {
		t.Helper()
		failGit(t, s, words)
	}
}

func TestStatus_WhenAReadOfOriginsCopyFails_EndsOnFixingWhatFailed(t *testing.T) {
	// Not parallel: a row puts a failing git first on PATH with t.Setenv.
	tests := []struct {
		fail func(t *testing.T, s *scene)
		name string
	}{
		{name: "the fetch of the copy", fail: failCopyFetch},
		{name: "the divergence from the copy", fail: failingGit("rev-list", "--left-right", "--count")},
		{name: "whether the copy carries the branch's first commit", fail: failingGit("merge-base", "--is-ancestor")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := statusScene(t, aheadOfCopy, greenOpen())
			tt.fail(t, s)

			got := seeStatus(t, s, (*testing.T).Context)

			assertEndsOn(t, got, statusCommand().fixed)
			assert.Check(t, hasUnread(got.status, readCopy), "unread %v, want %s listed", got.status.Unread, readCopy)
		})
	}
}

func TestStatus_WhenAReadOfTheBranchFails_EndsOnFixingWhatFailed(t *testing.T) {
	// Not parallel: failGit puts a failing git first on PATH with t.Setenv.
	tests := []struct {
		name  string
		read  string
		words []string
	}{
		{name: "the tip", read: readTip, words: []string{"rev-parse", "HEAD"}},
		{name: "the commits past origin's trunk", read: readCommits, words: history()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := statusScene(t, publishTopic, greenOpen())
			failGit(t, s, tt.words)

			got := seeStatus(t, s, (*testing.T).Context)

			assertEndsOn(t, got, statusCommand().fixed)
			assert.Check(t, hasUnread(got.status, tt.read), "unread %v, want %s listed", got.status.Unread, tt.read)
		})
	}
}

// localRead is one read of the repository's own state that a status makes on a work branch.
type localRead struct {
	name  string
	read  string
	words []string
}

// localReads are the reads of the working tree and of the local trunk, each with the arguments of
// the git call that makes it.
func localReads() []localRead {
	return []localRead{
		{name: "the working tree's changes", read: readTree, words: []string{"status", "--porcelain=v1"}},
		{name: "the local trunk", read: readLocalTrunk, words: []string{"show-ref", "--verify", "--quiet", "refs/heads/" + trunk}},
	}
}

func TestStatus_WhenALocalReadFails_EndsOnFixingWhatFailed(t *testing.T) {
	// Not parallel: failGit puts a failing git first on PATH with t.Setenv.
	for _, tt := range localReads() {
		t.Run(tt.name, func(t *testing.T) {
			s := statusScene(t, publishTopic, greenOpen())
			failGit(t, s, tt.words)

			got := seeStatus(t, s, (*testing.T).Context)

			assertEndsOn(t, got, statusCommand().fixed)
			assert.Check(t, hasUnread(got.status, tt.read), "unread %v, want %s listed", got.status.Unread, tt.read)
		})
	}
}

// ---------------------------------------------------------------------------
// The tools the landing commands check first
// ---------------------------------------------------------------------------

func TestStatus_WhenTheSSHOriginsURLsNeedIsMissing_EndsOnTheStepThatFixesIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		hide func(tb testing.TB, env []string) []string
		name string
		// step is the step the `Next:` line names before it runs ils status again.
		step string
	}{
		{name: "no ssh on PATH", hide: gittest.WithoutSSH, step: "install ssh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := statusScene(t, publishTopic, greenOpen())
			s.deps.Repo.Runner.Env = tt.hide(t, s.deps.Repo.Runner.Env)

			got := seeStatus(t, s, (*testing.T).Context)

			assertEndsOn(t, got, statusCommand().after(tt.step))
			assert.Check(t, hasUnread(got.status, readURLs), "unread %v, want %s listed", got.status.Unread, readURLs)
		})
	}
}

func TestStatus_WhenGitsVersionIsNotOneIlsWorksWith_EndsOnUpgradingGit(t *testing.T) {
	// Not parallel: fakeGit puts a git that prints its own version first on PATH with t.Setenv.
	tests := []struct {
		arrange func(*gittest.Fixture)
		name    string
		version string
	}{
		{name: "older than the minimum", arrange: publishTopic, version: "git version 1.0.0"},
		{name: "older than the minimum, on the trunk", arrange: standOnTrunk, version: "git version 1.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := statusScene(t, tt.arrange, greenOpen())
			fakeGit(t, s, gittest.Fake{Version: tt.version})

			got := seeStatus(t, s, (*testing.T).Context)

			assertEndsOn(t, got, statusCommand().after("upgrade git"))
			assert.Check(t, hasUnread(got.status, readGitVersion), "unread %v, want %s listed", got.status.Unread, readGitVersion)
		})
	}
}

func TestStatus_WhenGitsVersionCannotBeRead_EndsOnFixingWhatFailed(t *testing.T) {
	// Not parallel: failGit puts a failing git first on PATH with t.Setenv.
	s := statusScene(t, publishTopic, greenOpen())
	failGit(t, s, []string{"--version"})

	got := seeStatus(t, s, (*testing.T).Context)

	assertEndsOn(t, got, statusCommand().fixed)
	assert.Check(t, hasUnread(got.status, readGitVersion), "unread %v, want %s listed", got.status.Unread, readGitVersion)
}

func TestStatus_WhenAReadOfTheBranchIsCutShort_DoesNotEndOnFixingWhatFailed(t *testing.T) {
	// Not parallel: stallingGit puts a git that stalls first on PATH with t.Setenv.
	s := statusScene(t, publishTopic, greenOpen())
	ctx := byTimeLimit().stallingGit([]string{"rev-parse", "HEAD"})(t, s)

	got := seeStatus(t, s, func(*testing.T) context.Context { return ctx })

	assert.Assert(t, hasUnread(got.status, readTip), "unread %v, want %s listed", got.status.Unread, readTip)
	assert.Check(t, !strings.Contains(got.next, "fix what failed"), "next = %q, want a step other than fixing what failed", got.next)
}

func TestStatus_WhenTheFirstReadIsCutShort_EndsDoneSkippingEveryReadAfterIt(t *testing.T) {
	t.Parallel()

	for _, c := range cuts() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := statusScene(t, nil, greenOpen())

			got := seeStatus(t, s, c.spent)

			assertSays(t, got.text, expect(c.ends))
			assert.Check(t, is.Len(got.status.Unread, 1))
		})
	}
}

func TestStatus_WhenAReadOfOriginIsCutShort_EndsDoneSkippingTheForgeReadsAfterIt(t *testing.T) {
	t.Parallel()
	c := byTimeLimit()
	s := statusScene(t, publishTopic, greenOpen())
	// Origin answers the question about its trunk and the fetch of it, and the next call stalls.
	ctx := c.stalling(2)(t, s)

	got := seeStatus(t, s, func(*testing.T) context.Context { return ctx })

	assertSays(t, got.text, expect(c.ends))
	assert.Check(t, is.Len(got.status.Unread, 1))
	assert.Check(t, !slices.Contains(opsOf(s.forge.Calls()), forgetest.OpFindChanges), "forge reads = %v, want no find-changes", opsOf(s.forge.Calls()))
}

// ---------------------------------------------------------------------------
// Where HEAD stands outside a work branch or the trunk
// ---------------------------------------------------------------------------

const (
	standingDetached     result.Standing = "detached"
	standingNoOrigin     result.Standing = "no-origin"
	standingNoRepository result.Standing = "no-repository"
)

// noForge is a forge with no answer, so that any call to it is unscripted.
func noForge(string) forgetest.Script { return forgetest.Script{} }

func TestStatus_WhenHEADIsDetachedWithNoCommitOfItsOwn_EndsOnStartingABranch(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) {
		f.Git("switch", "--quiet", "--detach", trunk)
		f.AdvanceOrigin(trunk)
	}, viewing(cleanHead(), passing()))

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Standing, standingDetached))
	assert.Check(t, is.Len(got.status.Unlanded, 0))
	assertSays(t, got.text, detached())
}

func TestStatus_WhenDetachedHEADHasCommitsOriginsTrunkLacks_ListsThemToKeepOnABranch(t *testing.T) {
	t.Parallel()

	var own []string

	s := statusScene(t, func(f *gittest.Fixture) {
		own = f.Commits(spare, sharpen)
		f.Detach()
		f.Git("branch", "--quiet", "--delete", "--force", spare)
	}, viewing(cleanHead(), passing()))

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Standing, standingDetached))
	assert.Check(t, is.DeepEqual(got.status.Unlanded, own))
	assertSays(t, got.text, expect(shortSHA(own[0]), sharpen)+keepDetached())
}

func TestStatus_WhenOnTheTrunk_EndsOnStartingABranch(t *testing.T) {
	t.Parallel()
	s := statusScene(t, standOnTrunk, ruledBy(forge.RulesProtect))

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, nextOf(t, got.text), nextBranch())
}

func TestStatus_WhenThereIsNoOrigin_EndsOnCreatingIt(t *testing.T) {
	t.Parallel()
	s := statusScene(t, (*gittest.Fixture).RemoveOrigin, noForge)

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Standing, standingNoOrigin))

	_, next, _ := strings.Cut(got.text, "Next:")
	assertSays(t, next, expect("gh repo create --source . --push"))
}

func TestStatus_WhenOutsideAGitRepository_SaysSo(t *testing.T) {
	t.Parallel()
	s := statusScene(t, nil, noForge)
	s.deps.Repo.Runner.Dir = t.TempDir()
	missing := s.deps.Repo.CheckRepository(t.Context())
	assert.Assert(t, missing != nil, "CheckRepository() = nil outside a repository")

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Standing, standingNoRepository))
	assertSays(t, got.text, expect(missing.Error()))
}

// ---------------------------------------------------------------------------
// GitHub or origin out of reach
// ---------------------------------------------------------------------------

// ghRow is one way gh, or origin's repository on GitHub, keeps a status from reading pull requests.
type ghRow struct {
	name string
	// said is the parts of the line that names the fix.
	said string
	// unread is a read the status lists as unread besides the pull requests, if any.
	unread string
	script forgetest.Script
}

// ghMissing is a forge whose identity read finds no gh installed.
func ghMissing() forgetest.Script {
	return fails(&child.ToolMissingError{Tool: "gh", InstallURL: "https://cli.github.com"})
}

// ghFailures are the answers of the identity read that leave a status with git's facts alone.
func ghFailures() []ghRow {
	return []ghRow{
		{
			name: "gh is not installed", script: ghMissing(), unread: readIdentity,
			said: expect("gh", "not installed", "https://cli.github.com"),
		},
		{name: "fork", script: answers(widgets(true, true)), said: expect(owner+"/"+repoName, "is a fork", "push by hand")},
	}
}

func TestStatus_WhenOnAWorkBranchAndGitHubCannotBeRead_NamesTheFixBesideWhatGitTells(t *testing.T) {
	t.Parallel()

	for _, tt := range ghFailures() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := statusScene(t, aheadOfCopy, func(string) forgetest.Script { return tt.script })

			got := seeStatus(t, s, (*testing.T).Context)

			assertSays(t, got.text, tt.said)
			assert.Check(t, is.Equal(got.status.Ahead, 1))
			assert.Check(t, got.status.OnOrigin)
			assert.Check(t, got.status.PullRequest == nil, "pull request = %+v, want none", got.status.PullRequest)
			assert.Check(t, hasUnread(got.status, readChanges), "unread %v, want the pull requests listed", got.status.Unread)

			if tt.unread != "" {
				assert.Check(t, hasUnread(got.status, tt.unread), "unread %v, want %s listed", got.status.Unread, tt.unread)
			}
		})
	}
}

func TestStatus_WhenOriginsURLsNameTwoRepositories_EndsOnPointingThemAtOne(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) {
		f.Publish(topic)
		f.Git("remote", "set-url", "--push", "origin", "https://github.com/someone/else.git")
	}, viewing(cleanHead(), passing()))

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, expect("do not name one GitHub repository", "Next:", "`git remote set-url`"))
	assert.Check(t, hasUnread(got.status, readChanges), "unread %v, want the pull requests listed", got.status.Unread)
}

func hasUnread(status *result.Status, read string) bool {
	return slices.ContainsFunc(status.Unread, func(u result.Unread) bool { return u.Read == read })
}

func TestStatus_WhenOriginCannotBeReachedAndItsTrunkWasNeverLearned_CountsTheBranchAgainstTheLastFetch(t *testing.T) {
	t.Parallel()
	s := offlineStatusScene(t, func(f *gittest.Fixture) {
		f.Publish(topic)
		f.Git("remote", "set-head", "origin", "--delete")
		f.Git("update-ref", "refs/remotes/origin/"+topic, topic)
		f.Commits(topic, "fix a typo")
	}, greenOpen())

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Ahead, 1))
	assert.Check(t, got.status.OnOrigin)
	assertSays(t, got.text, expect("last fetch"))
}

func TestStatus_WhenOriginRefusesTheCaller_SaysWhatGitSaidNotThatOriginWasUnreached(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) {
		f.Git("remote", "set-head", "origin", trunk)
		f.Publish(topic)
	}, greenOpen())
	s.refuse(t)

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, expect(gittest.SSHDenied))
	assert.Check(t, !strings.Contains(got.text, "could not be reached"), "text %q, want origin's refusal, not unreached", got.text)
	i := slices.IndexFunc(got.status.Unread, func(u result.Unread) bool { return u.Read == readTrunk })
	assert.Assert(t, i >= 0, "unread %v, want an entry for %s", got.status.Unread, readTrunk)
	assert.Check(t, is.Contains(got.status.Unread[i].Error, gittest.SSHDenied))
}

func TestStatus_WhenOriginCannotBeReached_CountsAgainstTheLastFetch(t *testing.T) {
	t.Parallel()
	s := offlineStatusScene(t, func(f *gittest.Fixture) {
		f.Git("remote", "set-head", "origin", trunk)
		f.Publish(topic)
		// The last fetch saw origin's copy at the published tip; origin moves on after it.
		f.Git("update-ref", "refs/remotes/origin/"+topic, topic)
		f.Commits(topic, "fix a typo")
		f.AdvanceOrigin(topic)
	}, greenOpen())

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Ahead, 1))
	assert.Check(t, is.Equal(got.status.Behind, 0))
	assert.Check(t, got.status.OnOrigin)
	assertSays(t, got.text, expect("could not be reached"))
	assert.Check(t, is.Len(got.status.Unread, 1))
	assert.Check(t, hasUnread(got.status, "origin's trunk"), "unread %v, want an entry for origin's trunk", got.status.Unread)
}
