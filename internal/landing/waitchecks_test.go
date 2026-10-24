package landing_test

import (
	"slices"
	"testing"
	"time"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/landing"
)

// everyCheckRows are the verdict waits where the trunk names no required check.
func everyCheckRows() []vigilRow {
	started := forge.Verdict{Runs: ran(forge.RunInProgress)}
	finishing := forge.Verdict{Checks: passing().Checks, Runs: ran(forge.RunInProgress)}
	approving := forge.Verdict{Checks: passing().Checks, Runs: ran(forge.RunAwaitingPerson)}

	return []vigilRow{
		{
			name:     "checks pending then green",
			verdicts: says(running(), running(), passing()),
			want:     green(), ops: readsOf(1, 3, 1), waits: polls(2, verdictPoll),
		},
		{
			name:     "a run in progress with no check yet keeps the wait past 20 seconds",
			verdicts: says(started, started, started, started, started, started, passing()),
			want:     green(), ops: readsOf(1, 7, 1), waits: polls(6, verdictPoll),
		},
		{
			name:     "every check concluded while a run is in progress",
			verdicts: says(finishing, passing()),
			want:     green(), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			name:     "every check concluded while a run awaits a person",
			verdicts: says(approving),
			want:     concluded(landing.WaitGreen, approving, 1, 0), ops: readsOf(1, 1, 1),
		},
		{
			name:     "nothing reported for 20 seconds",
			verdicts: says(forge.Verdict{}),
			want:     concluded(landing.WaitNothingReported, forge.Verdict{}, 0, 0),
			ops:      readsOf(1, 5, 1), waits: polls(4, verdictPoll),
		},
		{
			name:     "nothing reported, then a check appears within the retry",
			verdicts: says(forge.Verdict{}, forge.Verdict{}, passing()),
			want:     green(), ops: readsOf(1, 3, 1), waits: polls(2, verdictPoll),
		},
	}
}

// lintFailed is the failed lint check of the ci workflow, which the trunk does not require.
func lintFailed() forge.Check {
	return forge.Check{Name: "lint", Bucket: forge.BucketFail, Workflow: "ci", Link: "https://example.com/acme/widgets/runs/7"}
}

// lintRed is the verdict of a completed run whose lint check failed, its log read, while test
// passed.
func lintRed() forge.Verdict {
	return forge.Verdict{
		Checks:    []forge.Check{lintFailed(), checked("test", forge.BucketPass)},
		Runs:      ran(forge.RunCompleted),
		FailedLog: forge.FailedLog{Check: "lint", Text: "lint: unused variable", State: forge.FailedLogRead},
	}
}

// lintHeldBack is the verdict of a run still going whose lint check failed while test still runs:
// the run has not completed, so the forge holds lint's log back.
func lintHeldBack() forge.Verdict {
	return forge.Verdict{
		Checks:    []forge.Check{lintFailed(), checked("test", forge.BucketPending)},
		Runs:      ran(forge.RunInProgress),
		FailedLog: forge.FailedLog{Check: "lint", State: forge.FailedLogInProgress},
	}
}

// redRows are the verdict waits where a check failed: they end once its log is settled, or at
// the deadline.
func redRows() []vigilRow {
	unfinished := lintHeldBack()
	completed := lintRed()
	gone := forge.Verdict{
		Checks:    unfinished.Checks,
		Runs:      ran(forge.RunInProgress),
		FailedLog: forge.FailedLog{Check: "lint", State: forge.FailedLogUnreadable},
	}
	status := forge.Verdict{
		Checks:    []forge.Check{{Name: "scan", Bucket: forge.BucketFail}, checked("test", forge.BucketPending)},
		Runs:      ran(forge.RunInProgress),
		FailedLog: forge.FailedLog{Check: "scan", State: forge.FailedLogNoRun},
	}

	return []vigilRow{
		{
			name:     "a check failed while another runs, then its run completed",
			verdicts: says(unfinished, completed),
			want:     concluded(landing.WaitRed, completed, 1, 0), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			// A third poll would leave too little time for the head read after it.
			name:     "a check failed and its run had not completed at the deadline",
			verdicts: says(unfinished), left: 15 * time.Second,
			want: concluded(landing.WaitRed, unfinished, 0, 0), ops: readsOf(1, 3, 1), waits: polls(2, verdictPoll),
		},
		{
			name: "a check failed and the time limit ended inside the read after it",
			verdicts: []forgetest.Answer[forge.Verdict]{
				{Value: unfinished}, {Err: byTimeLimit().of("gh")},
			},
			want: concluded(landing.WaitRed, unfinished, 0, 0), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			name: "a check failed and an interrupt ended the read after it",
			verdicts: []forgetest.Answer[forge.Verdict]{
				{Value: unfinished}, {Err: byInterrupt().of("gh")},
			},
			want: concluded(landing.WaitRed, unfinished, 0, 0), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			name:     "a check failed while its run goes on, then its log could not be read",
			verdicts: says(unfinished, gone),
			want:     concluded(landing.WaitRed, gone, 0, 0), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			name:     "a check failed and its log was read",
			verdicts: says(completed),
			want:     concluded(landing.WaitRed, completed, 1, 0), ops: readsOf(1, 1, 1),
		},
		{
			name:     "a check failed and its log could not be read while its run goes on",
			verdicts: says(gone),
			want:     concluded(landing.WaitRed, gone, 0, 0), ops: readsOf(1, 1, 1),
		},
		{
			name:     "a check failed that no run stands behind while a run goes on",
			verdicts: says(status),
			want:     concluded(landing.WaitRed, status, 0, 0), ops: readsOf(1, 1, 1),
		},
	}
}

func TestAwaitVerdict_WhenACheckFailed_EndsRedOnceItsLogIsSettled(t *testing.T) {
	t.Parallel()

	rows := redRows()
	for i := range rows {
		rows[i].heads = shows(clean())
	}

	runVigils(t, rows)
}

// heldRows are the verdict waits a check that reported and still runs holds, though no run does.
func heldRows() []vigilRow {
	lingering := forge.Verdict{Checks: running().Checks, Runs: ran(forge.RunCompleted)}
	external := forge.Verdict{Checks: []forge.Check{{Name: "deploy", Bucket: forge.BucketPending}}}
	deployed := forge.Verdict{Checks: []forge.Check{{Name: "deploy", Bucket: forge.BucketPass}}}

	return []vigilRow{
		{
			name:     "a check pending after every run completed, then green",
			verdicts: says(lingering, passing()),
			want:     green(), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			// deploy passes ten seconds in; no run ever starts, so the wait ends 20 seconds in.
			name:     "a check pending that no run stands behind, then green once 20 seconds passed",
			verdicts: says(external, external, deployed),
			want:     concluded(landing.WaitGreen, deployed, 1, 0), ops: readsOf(1, 5, 1), waits: polls(4, verdictPoll),
		},
	}
}

// earlyRows are the verdict waits whose checks concluded before any workflow run started.
func earlyRows() []vigilRow {
	// scanned is a check of an app, which no workflow run stands behind.
	scanned := forge.Verdict{Checks: []forge.Check{{Name: "scan", Bucket: forge.BucketPass}}}
	refused := forge.Verdict{
		Checks:    []forge.Check{{Name: "scan", Bucket: forge.BucketFail}},
		FailedLog: forge.FailedLog{Check: "scan", State: forge.FailedLogUnreadable},
	}
	failed := lintRed()
	failed.Checks = append(failed.Checks, scanned.Checks...)
	started := forge.Verdict{Checks: slices.Concat(scanned.Checks, running().Checks), Runs: ran(forge.RunInProgress)}
	finished := forge.Verdict{Checks: slices.Concat(scanned.Checks, passing().Checks), Runs: ran(forge.RunCompleted)}

	return []vigilRow{
		{
			name:     "a check passed before any run started, then a run failed",
			verdicts: says(scanned, failed),
			want:     concluded(landing.WaitRed, failed, 2, 0), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			name:     "a check passed before any run started, then a run started and passed",
			verdicts: says(scanned, scanned, started, finished),
			want:     concluded(landing.WaitGreen, finished, 2, 0), ops: readsOf(1, 4, 1), waits: polls(3, verdictPoll),
		},
		{
			name:     "a check of an app failed before any run started",
			verdicts: says(refused),
			want:     concluded(landing.WaitRed, refused, 0, 0), ops: readsOf(1, 1, 1),
		},
		{
			name:     "a check passed and no run started for 20 seconds",
			verdicts: says(scanned),
			want:     concluded(landing.WaitGreen, scanned, 1, 0), ops: readsOf(1, 5, 1), waits: polls(4, verdictPoll),
		},
	}
}

func TestAwaitVerdict_WhenTrunkRequiresNoCheck_WaitsUntilNothingStillRuns(t *testing.T) {
	t.Parallel()

	rows := slices.Concat(everyCheckRows(), heldRows(), earlyRows())
	for i := range rows {
		rows[i].heads = shows(clean())
	}

	runVigils(t, rows)
}

// requiredRows are the verdict waits where the trunk requires the checks lint and test.
func requiredRows() []vigilRow {
	lint := needed("lint", forge.BucketPass)
	absent := forge.Verdict{Checks: []forge.Check{lint}, MissingRequired: []string{"test"}, Runs: ran(forge.RunQueued)}
	both := requiredPassing()
	optional := forge.Verdict{
		Checks: []forge.Check{lint, needed("test", forge.BucketSkipped), checked("docs", forge.BucketPending)},
		Runs:   ran(forge.RunInProgress),
	}
	decided := concluded(landing.WaitGreen, optional, 1, 1)
	decided.Deciding = optional.Checks[:2]
	never := forge.Verdict{Checks: []forge.Check{lint}, MissingRequired: []string{"test"}, Runs: ran(forge.RunCompleted)}
	unapproved := forge.Verdict{MissingRequired: []string{"lint", "test"}, Runs: ran(forge.RunAwaitingPerson)}
	silence := forge.Verdict{MissingRequired: []string{"lint", "test"}}
	held := forge.Verdict{Checks: []forge.Check{lint, needed("test", forge.BucketPending)}, Runs: ran(forge.RunCompleted)}
	holding := concluded(landing.WaitPending, held, 1, 0)
	holding.Stage = landing.StageVerdict

	return []vigilRow{
		{
			// A third poll would leave too little time for the head read after it.
			name:     "required check pending after every run completed, until the deadline",
			verdicts: says(held), left: 15 * time.Second,
			want: holding, ops: readsOf(1, 3, 1), waits: polls(2, verdictPoll),
		},
		{
			name:     "required name absent with a queued run, then concluded",
			verdicts: says(absent, both),
			want:     concluded(landing.WaitGreen, both, 2, 0), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
		{
			name:     "every required name concluded while another check runs",
			verdicts: says(optional),
			want:     decided, ops: readsOf(1, 1, 1),
		},
		{
			name:     "required name absent, run completed",
			verdicts: says(never),
			want:     concluded(landing.WaitNeverReported, never, 1, 0), ops: readsOf(1, 1, 1),
		},
		{
			name:     "required name absent, run awaiting a person",
			verdicts: says(unapproved),
			want:     concluded(landing.WaitNeverReported, unapproved, 0, 0), ops: readsOf(1, 1, 1),
		},
		{
			name:     "nothing reported for 20 seconds",
			verdicts: says(silence),
			want:     concluded(landing.WaitNothingReported, silence, 0, 0),
			ops:      readsOf(1, 5, 1), waits: polls(4, verdictPoll),
		},
	}
}

// runlessRequiredRows are the verdict waits on a trunk that requires checks, where checks reported
// before any workflow run appeared.
func runlessRequiredRows() []vigilRow {
	aside := forge.Verdict{Checks: []forge.Check{checked("docs", forge.BucketPass)}, MissingRequired: []string{"test"}}
	unmet := concluded(landing.WaitNeverReported, aside, 0, 0)
	unmet.Deciding = nil
	both := requiredPassing()
	statuses := forge.Verdict{Checks: both.Checks}

	return []vigilRow{
		{
			name:     "every required name passed with no run for 20 seconds",
			verdicts: says(statuses),
			want:     concluded(landing.WaitGreen, statuses, 2, 0), ops: readsOf(1, 5, 1), waits: polls(4, verdictPoll),
		},
		{
			name:     "required name absent beside a check that is not required, no run for 20 seconds",
			verdicts: says(aside),
			want:     unmet, ops: readsOf(1, 5, 1), waits: polls(4, verdictPoll),
		},
		{
			name:     "required name absent beside a check that is not required, then a run reports it",
			verdicts: says(aside, both),
			want:     concluded(landing.WaitGreen, both, 2, 0), ops: readsOf(1, 2, 1), waits: polls(1, verdictPoll),
		},
	}
}

func TestAwaitVerdict_WhenTrunkRequiresChecks_WaitsForEveryRequiredName(t *testing.T) {
	t.Parallel()

	rows := slices.Concat(requiredRows(), runlessRequiredRows())
	for i := range rows {
		rows[i].heads = shows(clean())
		rows[i].required = []string{"lint", "test"}
	}

	runVigils(t, rows)
}
