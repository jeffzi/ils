package landing_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

// conventionalPull is pull request 12, open against the trunk, with a conventional title.
func conventionalPull() forge.Change {
	pull := standing()
	pull.Title = conventional

	return pull
}

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
			Verdict: forge.VerdictGreen, MergeState: forge.MergeClean,
			Checks: detail(forge.VerdictGreen, forge.MergeClean, passing().Checks...).Checks,
		},
		PastPullRequests: []result.PastPull{
			{Number: closed, State: forge.ChangeClosed},
			{Number: merged, State: forge.ChangeMerged},
		},
	}

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.DeepEqual(*got.status, want, cmpopts.EquateEmpty()))
	assertSays(t, got.text, expect("#12", pullURL))
	assertSays(t, got.text, expect(conventional))
	assertSays(t, got.text, expect(landedBody))
	assertSays(t, got.text, expect("#5", string(forge.ChangeClosed)))
}

func TestStatus_WhenTheOpenPullRequestsHeadIsNotClean_SaysItsMergeState(t *testing.T) {
	t.Parallel()
	s := statusScene(t, publishTopic, viewing(inState(forge.MergeConflicting), passing(), standing()))

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, stateLine(forge.MergeConflicting))
}

func TestStatus_WhenTheBranchHasNoOpenPullRequest_SaysSo(t *testing.T) {
	t.Parallel()
	s := statusScene(t, publishTopic, viewing(cleanHead(), passing()))

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, got.status.PullRequest == nil, "pull request = %+v, want none", got.status.PullRequest)
	assertSays(t, got.text, expect("no open pull request against the trunk "+trunk))
}

func TestStatus_WhenTheOpenPullRequestsTitleIsNotAConventionalSubject_SaysSo(t *testing.T) {
	t.Parallel()
	s := statusScene(t, publishTopic, greenOpen())

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
			s := statusScene(t, publishTopic, viewing(cleanHead(), tt.verdict, standing()))

			got := seeStatus(t, s, (*testing.T).Context)

			assert.Assert(t, got.status.PullRequest != nil)
			assertSays(t, got.text, expect(tt.said))
		})
	}
}

func TestStatus_WhenACheckPassedBesideOneStillRunning_ListsOnlyTheRunningOne(t *testing.T) {
	t.Parallel()
	s := statusScene(t, publishTopic, viewing(cleanHead(), builtAndRunning(), standing()))
	head := s.fixture.Git("rev-parse", "HEAD")

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, expect("Still running", shortSHA(head), "1 check", " test\n"))
	assert.Check(t, !strings.Contains(got.text, " build\n"), "text %q, want the passed check left out of the list", got.text)
}

func TestStatus_WhenNoRequiredCheckHasReported_SaysSoWithNoTally(t *testing.T) {
	t.Parallel()
	aside := forge.Verdict{
		Checks:          []forge.Check{forgetest.Check("docs", forge.BucketPass)},
		MissingRequired: []string{"test"}, Runs: ran(forge.RunInProgress),
	}
	s := statusScene(t, publishTopic, viewing(cleanHead(), aside, standing()))
	head := s.fixture.Git("rev-parse", "HEAD")

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, expect("No required check has reported for "+shortSHA(head)+"\n"))
}

func TestStatus_WhenTheOpenPullRequestIsAgainstAnotherBase_ReportsItsBase(t *testing.T) {
	t.Parallel()
	s := statusScene(t, publishTopic, viewing(cleanHead(), passing(), change(opened, otherBase, forge.ChangeOpen)))

	got := seeStatus(t, s, (*testing.T).Context)

	report, _, _ := strings.Cut(got.text, "Next:")
	assertSays(t, report, expect("#12", otherBase))
}

// elsewhereStatus is a status scene of a branch whose one pull request was merged into otherBase.
func elsewhereStatus(t *testing.T) *scene {
	t.Helper()

	return statusScene(t, landElsewhere, viewing(cleanHead(), passing(), elsewhereMerge()))
}

func TestStatus_WhenTheOnlyPullRequestWasMergedIntoAnotherBase_ReportsItMergedIntoThatBase(t *testing.T) {
	t.Parallel()
	s := elsewhereStatus(t)

	got := seeStatus(t, s, (*testing.T).Context)

	report, _, _ := strings.Cut(got.text, "Next:")
	assertSays(t, report, expect("#"+strconv.Itoa(mergedElsewhere), "merged", otherBase))
	assert.Check(t, !strings.Contains(report, "merged into "+trunk), "report %q, want no merge into %s", report, trunk)
}

func TestStatus_WhenTheOnlyPullRequestWasMergedIntoAnotherBase_EndsOnTheStepOfMergesRefusal(t *testing.T) {
	t.Parallel()
	refusal := elsewhereRefusal(t)
	assertStoppedBy(t, refusal.got, result.Refused, mergeRequest())
	refused, decoded := rendered(t, refusal.got, refusal.gitDir)
	s := elsewhereStatus(t)

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Check(t, is.Equal(nextOf(t, got.text), nextOf(t, refused)))
	assert.Check(t, is.Equal(got.next, decoded.Next))
}

// staleRow is one way the open pull request's head is a commit other than the branch's tip.
type staleRow struct {
	// heads returns the heads the reads of the head show in turn, given the branch's tip and the
	// other commit.
	heads func(tip, other string) []string
	name  string
	// listedBehind is whether the listing of the branch's pull requests names the other commit as
	// the head while the read of the head shows the tip, rather than the read of the head naming it.
	listedBehind bool
}

func TestStatus_WhenThePullRequestsHeadIsNotTheTip_ReportsNoVerdictAndEndsOnAPush(t *testing.T) {
	t.Parallel()

	tests := []staleRow{
		{
			name: "GitHub lags a push", listedBehind: true,
			heads: func(tip, _ string) []string { return []string{tip} },
		},
		{
			name:  "the head moves during the read",
			heads: func(_, other string) []string { return []string{other} },
		},
		{
			name:  "the head moves after the checks are read",
			heads: func(tip, other string) []string { return []string{tip, other} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var other, tip string

			build := greenOpen().with(func(script *forgetest.Script, _ string) {
				if tt.listedBehind {
					script.FindChanges[0].Value[0].HeadSHA = other
				}

				var heads []forge.Head
				for _, sha := range tt.heads(tip, other) {
					heads = append(heads, forge.Head{SHA: sha, MergeState: forge.MergeClean})
				}

				script.Head = shows(heads...)
			})
			s := statusScene(t, func(f *gittest.Fixture) {
				f.Publish(topic)
				other = f.Git("rev-parse", trunk)
				tip = f.Git("rev-parse", topic)
			}, build)

			got := seeStatus(t, s, (*testing.T).Context)

			assert.Assert(t, got.status.PullRequest != nil)
			assert.Check(t, is.Equal(got.status.PullRequest.Verdict, forge.VerdictNone))
			assert.Check(t, !strings.Contains(got.text, "ils merge"), "text %q, want no merge step", got.text)
			assertSays(t, "Next:"+nextOf(t, got.text), nextPush())
		})
	}
}

func TestStatus_WhenThePullRequestsHeadStaysOnTheTip_KeepsTheVerdictWithTheMergeStateReadAfterTheChecks(t *testing.T) {
	t.Parallel()
	build := greenOpen().showing(cleanHead(), inState(forge.MergeConflicting))
	s := statusScene(t, publishTopic, build)

	got := seeStatus(t, s, (*testing.T).Context)

	assert.Assert(t, got.status.PullRequest != nil)
	assert.Check(t, is.Equal(got.status.PullRequest.Verdict, forge.VerdictGreen))
	assert.Check(t, is.Equal(got.status.PullRequest.MergeState, forge.MergeConflicting))
}

func TestStatus_WhenOnlyACheckNotRequiredReported_NamesTheRequiredOneThatNeverReported(t *testing.T) {
	t.Parallel()
	aside := forge.Verdict{
		Checks:          []forge.Check{forgetest.Check("docs", forge.BucketPass)},
		MissingRequired: []string{"audit"}, Runs: ran(forge.RunCompleted),
	}
	build := viewing(inState(forge.MergeBlockedByMissingCheck), aside, standing()).requiring("audit")
	s := statusScene(t, publishTopic, build)

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, expect("never reported", "audit"))
	assert.Check(t, !strings.Contains(got.text, "No check or status reported"),
		"text %q, want no claim that nothing reported", got.text)
}

func TestStatus_WhenARequiredCheckNeverReportedBesideOneThatPassed_NamesItBesideTheTally(t *testing.T) {
	t.Parallel()
	never := forge.Verdict{
		Checks:          []forge.Check{forgetest.RequiredCheck("lint", forge.BucketPass)},
		MissingRequired: []string{"audit"}, Runs: ran(forge.RunCompleted),
	}
	build := viewing(inState(forge.MergeBlockedByMissingCheck), never, standing()).requiring("lint", "audit")
	s := statusScene(t, publishTopic, build)
	head := s.fixture.Git("rev-parse", "HEAD")

	got := seeStatus(t, s, (*testing.T).Context)

	assertSays(t, got.text, expect("never reported", "audit"))
	assertSays(t, got.text, expect(shortSHA(head), "1 passed"))
}

// settledAt is a forge for the open pull request 12 whose checks say verdict, on a clean head
// until they are read and in the merge state state after.
func settledAt(state forge.MergeState, verdict forge.Verdict) forgeFor {
	return viewing(cleanHead(), verdict, standing()).showing(cleanHead(), inState(state))
}

// pushEnd runs `ils push` to the end of its wait on the published topic branch, with a forge that
// answers as build says, and returns its `Next:` line and the `next` of its JSON.
func pushEnd(t *testing.T, build forgeFor) (text, next string) {
	t.Helper()

	synctest.Test(t, func(t *testing.T) {
		ctx, d := newDelivery(t, pushRow{arrange: publishTopic, script: build})

		got := landing.Push(ctx, d.deps, pushRequest())

		shown, decoded := rendered(t, got, d.gitDir)
		text, next = nextOf(t, shown), decoded.Next
	})

	return text, next
}

// settledRow is one verdict and merge state of the open pull request at the tip that the end of
// push's wait settles.
type settledRow struct {
	build forgeFor
	name  string
}

func TestStatus_WhenThePullRequestIsOpenAtTheTip_EndsOnTheStepPushEndsItsWaitOn(t *testing.T) {
	t.Parallel()

	lint := forgetest.RequiredCheck("lint", forge.BucketPass)
	beside := forge.Verdict{Checks: []forge.Check{lint}, MissingRequired: []string{"test"}, Runs: ran(forge.RunCompleted)}
	runOnly := forge.Verdict{MissingRequired: []string{"test"}, Runs: ran(forge.RunCompleted)}
	tests := []settledRow{
		{name: "green and clean", build: settledAt(forge.MergeClean, passing())},
		{name: "green and waiting on a review", build: settledAt(forge.MergeBlockedByReview, passing())},
		{name: "green on a draft", build: settledAt(forge.MergeDraft, passing()).drafted()},
		{name: "green and blocked by a missing required check", build: settledAt(forge.MergeBlockedByMissingCheck, passing())},
		{name: "green and blocked otherwise", build: settledAt(forge.MergeBlockedOther, passing())},
		{name: "red", build: settledAt(forge.MergeBlockedOther, lintRed())},
		{name: "in conflict with the trunk", build: settledAt(forge.MergeConflicting, passing())},
		{name: "behind the trunk", build: settledAt(forge.MergeBehind, passing())},
		{
			name:  "a required check never reported beside one that passed",
			build: settledAt(forge.MergeBlockedByMissingCheck, beside).requiring("lint", "test"),
		},
		{
			name:  "a required check never reported beside a workflow run",
			build: settledAt(forge.MergeBlockedByMissingCheck, runOnly).requiring("test"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			wantText, wantNext := pushEnd(t, tt.build)
			s := statusScene(t, publishTopic, tt.build)

			got := seeStatus(t, s, (*testing.T).Context)

			assert.Check(t, is.Equal(nextOf(t, got.text), wantText))
			assert.Check(t, is.Equal(got.next, wantNext))
		})
	}
}

func TestStatus_WhenOneReadOfThePullRequestAtTheTipSettlesNothing_EndsOnPush(t *testing.T) {
	t.Parallel()

	silent := forge.Verdict{MissingRequired: []string{"test"}}
	tests := []settledRow{
		{name: "checks still running", build: settledAt(forge.MergeClean, running())},
		{name: "merge state unknown", build: settledAt(forge.MergeUnknown, passing())},
		{name: "nothing reported and no check required", build: settledAt(forge.MergeClean, forge.Verdict{})},
		{
			name:  "nothing reported while the trunk requires a check",
			build: settledAt(forge.MergeBlockedByMissingCheck, silent).requiring("test"),
		},
		{
			name: "the head could not be read",
			build: settledAt(forge.MergeClean, passing()).with(func(script *forgetest.Script, _ string) {
				script.Head = []forgetest.Answer[forge.Head]{{Err: errors.New("gh: server error")}}
			}),
		},
		{
			name: "the verdict could not be read",
			build: settledAt(forge.MergeClean, passing()).with(func(script *forgetest.Script, _ string) {
				script.Verdict = []forgetest.Answer[forge.Verdict]{{Err: errors.New("gh: server error")}}
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := statusScene(t, publishTopic, tt.build)

			got := seeStatus(t, s, (*testing.T).Context)

			assertSays(t, "Next:"+nextOf(t, got.text), nextPush())
		})
	}
}
