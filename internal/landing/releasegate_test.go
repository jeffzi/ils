package landing_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// ---------------------------------------------------------------------------
// The CI gate
// ---------------------------------------------------------------------------

// gate is a release scene whose release commit follows previousTag on origin, and what origin and
// the local branches held before the release ran.
type gate struct {
	st     staged
	before string
}

// gateScene returns a release scene whose forge answers from script.
func gateScene(t *testing.T, script forgetest.Script) gate {
	t.Helper()

	st := releaseScene(t, tagged)
	st.s.useForge(t, script)

	return gate{st: st, before: st.s.written()}
}

// nextRelease is the `Next:` line that runs words, the command as typed, again as it is.
func nextRelease(words string) string {
	return expect(resulttest.NextLine("ils "+words) + "\n")
}

// commitVerdicts returns the reads of the checks of a commit among the forge calls of s.
func commitVerdicts(s *scene) []forgetest.Call {
	return callsOf(s.forge.Calls(), forgetest.OpCommitVerdict)
}

// redCheck is the lint check of the ci workflow, ended in bucket with lintURL as its link, beside a
// passing test check, once the run completed.
func redCheck(bucket forge.Bucket) forge.Verdict {
	lint := lintFailed()
	lint.Bucket = bucket

	return forge.Verdict{Checks: []forge.Check{lint, forgetest.Check("test", forge.BucketPass)}, Runs: ran(forge.RunCompleted)}
}

func TestRelease_WhenTheChecksOfTheReleaseCommitPass_GoesOnCountingThem(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		verdict := forge.Verdict{
			Checks: []forge.Check{forgetest.Check("test", forge.BucketPass), forgetest.Check("docs", forge.BucketSkipped)},
			Runs:   ran(forge.RunCompleted),
		}
		run := gateScene(t, gated(verdict))

		got := runRelease(t, run.st.s, releaseRequest(version, false))

		assertEndedBy(t, got, run.st.s.gitDir, ended{
			outcome: result.Done, req: releaseRequest(version, false),
			text: expect(shortSHA(run.st.release), "1 passed, 1 skipped"),
		})
		want := []forgetest.Call{{Op: forgetest.OpCommitVerdict, Repo: origin(), SHA: run.st.release}}
		assert.Check(t, is.DeepEqual(commitVerdicts(run.st.s), want, whichRepo()))
	})
}

// gateRow is a release whose release commit's checks the forge answers with verdicts in turn, and
// the parts of the refusal it ends with.
type gateRow struct {
	name     string
	want     string
	verdicts []forge.Verdict
}

// runGateRefusals runs each row as a test in a bubble of its own: the release is refused as the row
// says, with nothing written, carrying the payload of a red gate.
func runGateRefusals(t *testing.T, rows []gateRow) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				req := releaseRequest(version, false)
				run := gateScene(t, gated(tt.verdicts...))

				got := runRelease(t, run.st.s, req)

				assertEndedBy(t, got, run.st.s.gitDir, ended{outcome: result.Refused, req: req, text: tt.want})
				assert.Check(t, is.Equal(run.st.s.written(), run.before))
				assert.Check(t, is.DeepEqual(releasePayload(t, got, run.st.s.gitDir), released(run.st, "red", "")))
			})
		})
	}
}

func TestRelease_WhenACheckOfTheReleaseCommitDidNotPass_RefusesNamingIt(t *testing.T) {
	t.Parallel()

	runGateRefusals(t, []gateRow{
		{name: "a check failed", verdicts: []forge.Verdict{redCheck(forge.BucketFail)}, want: expect("lint", lintURL)},
		{
			name: "a check was canceled", verdicts: []forge.Verdict{redCheck(forge.BucketCanceled)},
			want: expect("lint", "gh run rerun"),
		},
	})
}

func TestRelease_WhenTheChecksOfTheReleaseCommitGoOnAfterARead_WaitsForThemToConclude(t *testing.T) {
	t.Parallel()

	// The read after the wait goes on finds lint failed, so a release that stopped at the first read
	// would not be refused.
	failed := expect("lint", lintURL)
	runGateRefusals(t, []gateRow{
		{name: "a check is pending", verdicts: []forge.Verdict{running(), redCheck(forge.BucketFail)}, want: failed},
	})
}

// stillRunningRow is a way the wait for checks that never conclude ends: the checks the forge
// answers with, and the parts of the text before the `Next:` line.
type stillRunningRow struct {
	// under returns the context the release runs under, with the whole time limit left.
	under    func(t *testing.T) context.Context
	name     string
	text     string
	verdicts []forgetest.Answer[forge.Verdict]
}

// stillRunningRows are the ways a wait for checks that do not conclude ends pending, one per reason
// the release names.
func stillRunningRows() []stillRunningRow {
	timeLimitEnds := func(t *testing.T) context.Context {
		t.Helper()

		return within(t, 0)
	}
	interrupted := func(t *testing.T) context.Context {
		t.Helper()

		return byInterrupt().after(within(t, 0), 30*time.Second)
	}
	return []stillRunningRow{
		{name: "the time limit", text: expect("test", "still running"), verdicts: says(running()), under: timeLimitEnds},
		{name: "an interrupt", text: expect("test", "interrupted"), verdicts: says(running()), under: interrupted},
		{
			// The interrupt comes before no check reporting for long enough ends the wait.
			name: "an interrupt before any check reported", text: expect("interrupted"),
			verdicts: says(forge.Verdict{}), under: func(t *testing.T) context.Context {
				t.Helper()

				return byInterrupt().after(within(t, 0), 5*time.Second)
			},
		},
		{
			name: "an interrupt inside the first read", text: expect("interrupted"),
			verdicts: []forgetest.Answer[forge.Verdict]{{Hold: true}}, under: interrupted,
		},
		{
			name: "the time limit inside the first read", text: expect("time limit", "ended while reading"),
			verdicts: []forgetest.Answer[forge.Verdict]{{Hold: true}}, under: timeLimitEnds,
		},
		{
			// The time limit ends before no check reporting for long enough ends the wait.
			name: "the time limit before any check reported", text: expect("no check or status had reported", "time limit"),
			verdicts: says(forge.Verdict{}), under: func(t *testing.T) context.Context {
				t.Helper()

				return within(t, landing.MergeReserve+10*time.Second)
			},
		},
		{
			name: "the time limit with a run going and no check", text: expect("had not all reported", "time limit"),
			verdicts: says(forge.Verdict{Runs: ran(forge.RunInProgress)}), under: timeLimitEnds,
		},
		{
			name: "an interrupt with a run going and no check", text: expect("had not all reported", "interrupted"),
			verdicts: says(forge.Verdict{Runs: ran(forge.RunInProgress)}), under: interrupted,
		},
	}
}

func TestRelease_WhenTheChecksStillRunAtTheEndOfTheWait_PendingWithTheCommandAsTyped(t *testing.T) {
	t.Parallel()

	for _, tt := range stillRunningRows() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				req := releaseRequest(version, true)
				script := releasable()
				script.CommitVerdict = tt.verdicts
				run := gateScene(t, script)

				got := landing.Release(tt.under(t), run.st.s.deps, req)

				assertEndedBy(t, got, run.st.s.gitDir, ended{
					outcome: result.Pending, req: req, text: tt.text + nextRelease("release "+version+" --tag-only"),
				})
				assert.Check(t, is.Equal(run.st.s.written(), run.before))
				want := released(run.st, "pending", "")
				want.TagOnly = true
				assert.Check(t, is.DeepEqual(releasePayload(t, got, run.st.s.gitDir), want))
			})
		})
	}
}

func TestRelease_WhenTheChecksNeverConclude_EndsTheWaitTheReserveBeforeTheDeadline(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		run := gateScene(t, gated(running()))
		started := time.Now()

		got := runRelease(t, run.st.s, releaseRequest(version, false))

		waited := time.Since(started)
		latest := bound - landing.MergeReserve
		// The last poll is the first that leaves less than a poll before the wait's deadline.
		earliest := latest - 2*verdictPoll

		assertStoppedBy(t, got, result.Pending, releaseRequest(version, false))
		assert.Check(t, waited <= latest, "the wait ended %s in, want at most %s", waited, latest)
		assert.Check(t, waited > earliest, "the wait ended %s in, want more than %s", waited, earliest)
	})
}

// silentRow is a release for whose release commit no check reported at the first read: the forge's
// script and how the release ends.
type silentRow struct {
	name string
	text string
	// url is the link the release payload carries.
	url     string
	script  forgetest.Script
	outcome result.Outcome
}

// silentRows are the releases whose release commit no check reported for at the first read, with
// tipMark in place of the release commit's short sha in the text.
func silentRows() []silentRow {
	nothing := expect("no check or status reported", tipMark)
	silent := func(protection forge.Protection, hasCI ...bool) forgetest.Script {
		script := gated(forge.Verdict{})
		script.TrunkProtection = []forgetest.Answer[forge.Protection]{{Value: protection}}
		script.HasTrunkCI = forgetest.Values(hasCI...)

		return script
	}

	return []silentRow{
		{
			// A trunk that requires checks has CI, so the forge has no answer for the CI read.
			name: "the trunk requires checks", outcome: result.Refused, text: nothing,
			script: silent(forge.Protection{RequiredChecks: []string{"lint", "test"}}),
		},
		{
			name: "the forge says the trunk has CI", outcome: result.Refused, text: nothing,
			script: silent(forge.Protection{}, true),
		},
		{
			name: "the trunk has no CI", outcome: result.Done, text: expect("No CI", trunk, tipMark), url: releaseURL,
			script: silent(forge.Protection{}, false),
		},
	}
}

func TestRelease_WhenNoCheckReportedAtTheFirstRead_ReadsAgain(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		req := releaseRequest(version, false)
		run := gateScene(t, gated(forge.Verdict{}, passing()))

		got := runRelease(t, run.st.s, req)

		text := mark(expect(tipMark, "1 passed"), run.st.release)
		assertEndedBy(t, got, run.st.s.gitDir, ended{outcome: result.Done, req: req, text: text})
	})
}

func TestRelease_WhenNoCheckReportedForTheReleaseCommit_DecidesByTheTrunksCI(t *testing.T) {
	t.Parallel()

	for _, tt := range silentRows() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				req := releaseRequest(version, false)
				run := gateScene(t, tt.script)

				got := runRelease(t, run.st.s, req)

				text := mark(tt.text, run.st.release)
				assertEndedBy(t, got, run.st.s.gitDir, ended{outcome: tt.outcome, req: req, text: text})
				assert.Check(t, is.DeepEqual(releasePayload(t, got, run.st.s.gitDir), released(run.st, "none", tt.url)))
			})
		})
	}
}

func TestRelease_WhenFewerThanTheReserveIsLeftBeforeTheWrites_PendingWithTheCommandAsTyped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		verdict string
		req     landing.Request
	}{
		{verdict: "green", req: releaseRequest(version, false)},
		{verdict: "forced", req: forced(releaseRequest(version, false))},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.req.Words, " "), func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				left := landing.MergeReserve - time.Second
				run := gateScene(t, gated(passing()))

				got := landing.Release(within(t, left), run.st.s.deps, tt.req)

				assertEndedBy(t, got, run.st.s.gitDir, ended{
					outcome: result.Pending, req: tt.req, text: nextRelease(strings.Join(tt.req.Words, " ")),
				})
				assert.Check(t, is.Equal(run.st.s.written(), run.before))
				assert.Check(t, is.DeepEqual(releasePayload(t, got, run.st.s.gitDir), released(run.st, tt.verdict, "")))
			})
		})
	}
}

func TestRelease_WhenForced_SaysTheCIGateWasSkippedWithoutReadingTheChecks(t *testing.T) {
	t.Parallel()
	st := releaseScene(t, tagged)
	st.s.useForge(t, releasable())
	req := forced(releaseRequest(version, false))

	got := runRelease(t, st.s, req)

	assertEndedBy(t, got, st.s.gitDir, ended{outcome: result.Done, req: req, text: expect("skipped", "--force")})
	assert.Check(t, is.Len(commitVerdicts(st.s), 0))
	assert.Check(t, is.DeepEqual(releasePayload(t, got, st.s.gitDir), released(st, "forced", releaseURL)))
}

func TestRelease_WhenForced_RefusesAsWithoutItWithTheCommandAsTyped(t *testing.T) {
	t.Parallel()
	st := releaseScene(t, tagged)
	st.s.fixture.Git("tag", releaseTag, st.base)
	req := forced(releaseRequest(version, false))

	got := runRelease(t, st.s, req)

	assertEndedBy(t, got, st.s.gitDir, ended{
		outcome: result.Refused, req: req,
		text: expect(releaseTag, "git tag --delete "+releaseTag, "ils release "+version+" --force"),
	})
	assert.Check(t, is.DeepEqual(releasePayload(t, got, st.s.gitDir), released(st, "unchecked", "")))
}

func TestRelease_WhenACheckWithNoWorkflowRunBehindItFailed_RefusesAtTheFirstRead(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		red := redCheck(forge.BucketFail)
		red.Runs = nil
		run := gateScene(t, gated(red))
		req := releaseRequest(version, false)

		got := runRelease(t, run.st.s, req)

		assertEndedBy(t, got, run.st.s.gitDir, ended{outcome: result.Refused, req: req, text: expect("lint", lintURL)})
		assert.Check(t, is.Len(commitVerdicts(run.st.s), 1))
	})
}

// unreadGateRow is a forge read of the gate that fails, and the verdict the release payload carries.
type unreadGateRow struct {
	name    string
	read    string
	verdict string
	script  forgetest.Script
}

func TestRelease_WhenAReadOfTheGateFails_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()

	failed := errors.New(forgeFailure)
	checks := gated()
	checks.CommitVerdict = []forgetest.Answer[forge.Verdict]{{Err: failed}}
	protection := gated(forge.Verdict{})
	protection.TrunkProtection = []forgetest.Answer[forge.Protection]{{Err: failed}}
	ci := gated(forge.Verdict{})
	ci.TrunkProtection = forgetest.Values(forge.Protection{})
	ci.HasTrunkCI = []forgetest.Answer[bool]{{Err: failed}}
	tests := []unreadGateRow{
		{name: "the checks of the release commit", read: readReleaseChecks, verdict: "unchecked", script: checks},
		{name: "the trunk's protection once no check reported", read: readProtection, verdict: "none", script: protection},
		{name: "the trunk's CI once no check reported", read: readCI, verdict: "none", script: ci},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				cmd := releaseCommand(version)
				run := gateScene(t, tt.script)

				got := runRelease(t, run.st.s, cmd.req)

				said := assertBrokeReadBy(t, got, cmd, tt.read)
				assert.Check(t, is.Contains(said, forgeFailure))
				assert.Check(t, is.Equal(run.st.s.written(), run.before))
				assert.Check(t, is.DeepEqual(releasePayload(t, got, run.st.s.gitDir), released(run.st, tt.verdict, "")))
			})
		})
	}
}
