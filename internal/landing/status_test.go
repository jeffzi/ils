package landing_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"
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

	standingBranch = "branch"
)

// statusRequest is the command and words of a bare `ils status`.
func statusRequest() landing.Request {
	return landing.Request{Command: "status", Words: []string{"status"}}
}

// conventionalPull is pull request 12, open against the trunk, with a conventional title.
func conventionalPull() forge.Change {
	pull := standing()
	pull.Title = conventional

	return pull
}

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

// seen is what a status showed: its text and the status object its JSON carries.
type seen struct {
	status *result.Status
	text   string
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

	text, err := result.RenderText(got, s.gitDir)
	assert.NilError(t, err)

	out, err := result.RenderJSON(got, s.gitDir)
	assert.NilError(t, err)

	payload := resulttest.Decode(t, out).Payload
	assert.Assert(t, payload != nil && payload.Status != nil, "RenderJSON() = %s, want a status object", out)

	return seen{text: text, status: payload.Status}
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
		{
			name: "up to date", said: "up to date", onOrigin: true,
			arrange: func(f *gittest.Fixture) { f.Publish(topic) },
		},
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
// The branch's pull requests
// ---------------------------------------------------------------------------

func TestStatus_WhenTheBranchHasPullRequests_ListsEachPullRequest(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) {
		// The merged pull request came from another branch's commit, so it carries none of the topic's.
		f.Commits(spare, "earlier work")
		f.LandPull(merged, spare)
		f.Git("switch", "--quiet", topic)
		f.Publish(topic)
	}, viewing(cleanHead(), passing(),
		change(closed, trunk, forge.ChangeClosed), change(merged, trunk, forge.ChangeMerged), conventionalPull()))
	want := result.Status{
		Standing: standingBranch, Branch: topic, Trunk: trunk, OnOrigin: true,
		PullRequest: &result.OpenPull{
			Number: opened, URL: pullURL, Title: conventional, Body: landedBody, TitleOK: true,
			Verdict: string(forge.VerdictGreen), MergeState: string(forge.MergeClean),
			Checks: detail(forge.VerdictGreen, forge.MergeClean, passing().Checks...).Checks,
		},
		PastPullRequests: []result.PastPull{
			{Number: closed, State: string(forge.ChangeClosed)},
			{Number: merged, State: string(forge.ChangeMerged)},
		},
	}

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.DeepEqual(*got.status, want, cmpopts.EquateEmpty()))
	assertSays(t, got.text, expect("#12", pullURL))
	assertSays(t, got.text, expect(conventional))
	assertSays(t, got.text, expect(landedBody))
	assertNoStateLine(t, got.text)
	assertSays(t, got.text, expect("#5", string(forge.ChangeClosed)))
	assertSays(t, got.text, expect("#7", string(forge.ChangeMerged)))
}

func TestStatus_WhenTheOpenPullRequestsHeadIsNotClean_SaysItsMergeState(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) { f.Publish(topic) },
		viewing(inState(forge.MergeConflicting), passing(), standing()))

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, stateLine(forge.MergeConflicting))
}

func TestStatus_WhenTheBranchHasNoOpenPullRequest_SaysSo(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) { f.Publish(topic) }, viewing(cleanHead(), passing()))

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, got.status.PullRequest == nil, "pull request = %+v, want none", got.status.PullRequest)
	assertSays(t, got.text, expect("no open pull request against the trunk "+trunk))
}

func TestStatus_WhenTheOpenPullRequestsTitleIsNotAConventionalSubject_SaysSo(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) { f.Publish(topic) }, viewing(cleanHead(), passing(), standing()))

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Assert(t, got.status.PullRequest != nil)
	assert.Check(t, !got.status.PullRequest.TitleOK)
	assertSays(t, got.text, expect("\""+topic+"\"", "not a conventional commit subject"))
}

// verdictRow is one verdict of the open pull request's head.
type verdictRow struct {
	name string
	// said is a phrase the text must hold.
	said    string
	verdict forge.Verdict
}

func TestStatus_WhenThePullRequestIsOpen_ReportsTheVerdictOfItsHead(t *testing.T) {
	t.Parallel()

	tests := []verdictRow{
		{name: "red", verdict: lintRed(), said: "lint"},
		{name: "none reported", verdict: forge.Verdict{}, said: "No check or status reported"},
		{
			name: "a workflow run queued before any check reported", verdict: forge.Verdict{Runs: ran(forge.RunQueued)},
			said: "a workflow run is still going",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := statusScene(t, func(f *gittest.Fixture) { f.Publish(topic) }, viewing(cleanHead(), tt.verdict, standing()))

			got := seeStatus(t, s, (*testing.T).Context)

			assert.Assert(t, got.status.PullRequest != nil)
			assertSays(t, got.text, expect(tt.said))
		})
	}
}

func TestStatus_WhenChecksOfTheOpenPullRequestStillRun_ListsEachUnderItsHead(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) { f.Publish(topic) }, viewing(cleanHead(), bothRunning(), standing()))
	head := s.fixture.Git("rev-parse", "HEAD")

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, expect("Still running", shortSHA(head)+" (2 checks):\n  ◌ lint\n  ◌ test\n"))
}

func TestStatus_WhenNoRequiredCheckHasReported_SaysSoWithNoTally(t *testing.T) {
	t.Parallel()
	aside := forge.Verdict{
		Checks:          []forge.Check{forgetest.Check("docs", forge.BucketPass)},
		MissingRequired: []string{"test"}, Runs: ran(forge.RunInProgress),
	}
	s := statusScene(t, func(f *gittest.Fixture) { f.Publish(topic) }, viewing(cleanHead(), aside, standing()))
	head := s.fixture.Git("rev-parse", "HEAD")

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, expect("No required check has reported for "+shortSHA(head)+"\n"))
}

func TestStatus_WhenTheOpenPullRequestIsAgainstAnotherBase_ReportsItsBase(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) { f.Publish(topic) },
		viewing(cleanHead(), passing(), change(opened, otherBase, forge.ChangeOpen)))

	got := seeStatus(t, s, (*testing.T).Context)

	report, _, _ := strings.Cut(got.text, "Next:")
	assertSays(t, report, expect("#12", otherBase))
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
	green := viewing(cleanHead(), passing(), standing())

	return []nextRow{
		{
			name: "behind origin's copy with a green pull request", script: green,
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				f.AdvanceOrigin(topic)

				return pushCommand().after("`" + pullFrom(topic) + "`")
			},
		},
		{
			name: "not on origin", script: viewing(cleanHead(), passing()),
			arrange: func(*gittest.Fixture) string { return nextPush() },
		},
		{
			name: "ahead of origin's copy with a green pull request", script: green,
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
		{name: "checks still running", script: viewing(cleanHead(), running(), standing()), arrange: published(nextPush())},
		{
			name: "checks still running while conflicting with the trunk", script: viewing(conflicting, running(), standing()),
			arrange: published(nextPush()),
		},
		{
			name:    "red",
			script:  viewing(inState(forge.MergeBlockedOther), lintRed(), standing()),
			arrange: published(nextRed()),
		},
		{name: "red while conflicting with the trunk", script: viewing(conflicting, lintRed(), standing()), arrange: published(nextRed())},
		{name: "conflicting with the trunk", script: viewing(conflicting, passing(), standing()), arrange: published(nextTrunk())},
		{name: "waiting on a review", script: viewing(review, passing(), standing()), arrange: published(expect("request a review"))},
		{name: "green and mergeable", script: viewing(cleanHead(), passing(), standing()), arrange: published(nextMerge())},
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
	open := viewing(cleanHead(), passing(), standing())

	return []nextRow{
		{
			name: "changed tracked files", script: open,
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
			name: "green and mergeable with a local trunk ahead of origin's", script: open,
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				f.AdvanceTrunk()

				return expect("move those commits off the local trunk by hand", "ils merge again")
			},
		},
	}
}

func TestStatus_WhenOnAWorkBranch_EndsOnTheFirstNextThatHolds(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(precedenceSteps(), refusedSteps(), elsewhereSteps(), landedSteps(), copySteps(), pullSteps()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var want string

			s := statusScene(t, func(f *gittest.Fixture) { want = tt.arrange(f) }, tt.script)

			got := seeStatus(t, s, (*testing.T).Context)

			_, next, found := strings.Cut(got.text, "Next:")
			assert.Check(t, found, "text %q, want a Next: line", got.text)
			assertSays(t, "Next:"+next, want)
		})
	}
}

// ---------------------------------------------------------------------------
// Reads that fail
// ---------------------------------------------------------------------------

func TestStatus_WhenAForgeReadFails_ReportsTheFailedReadBesideTheRest(t *testing.T) {
	t.Parallel()
	s := statusScene(t, func(f *gittest.Fixture) { f.Publish(topic) },
		func(string) forgetest.Script { return failingToFind(errors.New(forgeFailure)) })

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, expect(readChanges, forgeFailure))
	assert.Assert(t, is.Len(got.status.Unread, 1))
	assert.Check(t, is.Equal(got.status.Unread[0].Read, readChanges))
	assert.Check(t, is.Contains(got.status.Unread[0].Error, forgeFailure))
}

func TestStatus_WhenAGitReadFails_ReportsTheFailedReadBesideTheRest(t *testing.T) {
	// Not parallel: failGit puts a failing git first on PATH with t.Setenv.
	s := statusScene(t, func(f *gittest.Fixture) { f.Publish(topic) }, viewing(cleanHead(), passing(), standing()))
	failGit(t, s, []string{"rev-list", "--left-right", "--count"})

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, expect(readCopy, gittest.FailedSaid))
	assert.Assert(t, is.Len(got.status.Unread, 1))
	assert.Check(t, is.Equal(got.status.Unread[0].Read, readCopy))
	assert.Assert(t, got.status.PullRequest != nil)
}

func TestStatus_WhenTheFirstReadIsCutShort_EndsDoneSkippingEveryReadAfterIt(t *testing.T) {
	t.Parallel()

	for _, c := range cuts() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := statusScene(t, nil, viewing(cleanHead(), passing(), standing()))

			got := seeStatus(t, s, c.spent)

			assertSays(t, got.text, expect(c.ends))
			assert.Check(t, is.Len(got.status.Unread, 1))
			assert.Check(t, !s.gitRan("fetch"), "git fetched from origin after the cut")
		})
	}
}

func TestStatus_WhenAReadOfOriginIsCutShort_EndsDoneSkippingTheForgeReadsAfterIt(t *testing.T) {
	t.Parallel()
	c := byTimeLimit()
	s := statusScene(t, func(f *gittest.Fixture) { f.Publish(topic) }, viewing(cleanHead(), passing(), standing()))
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
	standingDetached     = "detached"
	standingNoOrigin     = "no-origin"
	standingNoRepository = "no-repository"
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
	assertSays(t, got.text, expect(shortSHA(own[0]), sharpen, "`git switch -c <name>`", "ils push"))
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
			s := statusScene(t, func(f *gittest.Fixture) {
				f.Publish(topic)
				f.Commits(topic, "fix a typo")
			}, func(string) forgetest.Script { return tt.script })

			got := seeStatus(t, s, (*testing.T).Context)

			assertSays(t, got.text, tt.said)
			assert.Check(t, is.Equal(got.status.Standing, standingBranch))
			assert.Check(t, is.Equal(got.status.Branch, topic))
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
	}, viewing(cleanHead(), passing(), standing()))

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Ahead, 1))
	assert.Check(t, got.status.OnOrigin)
	assertSays(t, got.text, expect("last fetch"))
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
	}, viewing(cleanHead(), passing(), standing()))

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(got.status.Ahead, 1))
	assert.Check(t, is.Equal(got.status.Behind, 0))
	assert.Check(t, got.status.OnOrigin)
	assertSays(t, got.text, expect("could not be reached"))
	assert.Check(t, is.Len(got.status.Unread, 1))
	assert.Check(t, hasUnread(got.status, "origin's trunk"), "unread %v, want an entry for origin's trunk", got.status.Unread)
}
