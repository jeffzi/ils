package landing_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
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
