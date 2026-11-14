package landing_test

import (
	"cmp"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/result"
)

const (
	switchStep = "`git switch " + trunk + "`"
	dropStep   = "`git branch -D " + topic + "`"
	pullStep   = "`git pull --ff-only origin " + trunk + "`"
	fetchStep  = "`git fetch origin " + trunk + "`"
	detachStep = "`git switch --detach refs/remotes/origin/" + trunk + "`"

	readStillOnOrigin = "whether origin still has the branch"

	mergedPullURL = "https://github.com/Acme/Widgets/pull/7"

	// landedBody is the body of the message of pull request 12 that toLand reads.
	landedBody = "Tidy the readme."

	refusedMerge = "HTTP 405: Head branch was modified"
)

func detachedLine() string {
	return expect("Detached", trunk, topic)
}

func elsewhereLine() string {
	return expect(trunk, "another worktree", "`git pull --ff-only`")
}

func resumedLine() string {
	return expect("#7", "already merged", trunk)
}

func mergedLine() string {
	return expect("Merged", "#12", trunk, "\""+worded+"\"")
}

func deletedLine() string {
	return expect("Deleted", topic)
}

func goneLine() string {
	return expect(topic, "already gone")
}

func onTrunkLine() string {
	return expect("Switched", trunk, topic)
}

func interruptedResumeText() string {
	return resumedLine() + expect("interrupted before finishing the merge", "interrupted before finishing the merge") +
		mergeCommand().again
}

// landingRow is one run of `ils merge` that reaches its writes or a resume, and how it ends.
type landingRow struct {
	// payload is the merge payload the run carries; an empty sha is the tip's.
	payload *result.Merge
	name    string
	// text is the parts of the result's text, with tipMark where the sha of the tip is printed. With
	// after set, it is the parts before a message of git's or gh's own.
	text string
	// after is the parts after that message; empty when the text holds none.
	after string
	// on is where the run leaves HEAD: the name of the branch it is on, or HEAD when it is detached;
	// empty leaves it unchecked.
	on  string
	set setting
	// told is every event the progress hook is told, with tipMark where the sha of the tip is
	// printed; empty leaves them unchecked.
	told    []string
	outcome result.Outcome
	// kept is whether the run must write nothing.
	kept bool
}

// toldAfterWait returns every event the progress hook is told by a run that waits for its one check
// to pass, and then tells after.
func toldAfterWait(after ...string) []string {
	waited := []string{checkingStep(), findingStep(), waitingStep(), "count 1 of 1", said(countLine())}

	return slices.Concat(waited, after)
}

// runLandings runs each row as a test in a bubble of its own: the run ends as the row says and
// carries the row's payload.
func runLandings(t *testing.T, rows []landingRow) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				run := runMergeOn(t, tt.set)

				assertStoppedBy(t, run.got, tt.outcome, mergeRequest())

				text, _ := shown(t, run.got, run.gitDir)
				assertSaid(t, text, mark(tt.text, run.tip), tt.after)

				want := *tt.payload
				if want.SHA == "" {
					want.SHA = run.tip
				}

				assert.Check(t, is.DeepEqual(mergePayload(t, run.got), &want, cmpopts.EquateEmpty()))

				assertOptional(t, &run, &tt)
			})
		})
	}
}

// assertOptional runs the checks of row that its fields turn on: what the run wrote, where it left
// HEAD and what the progress hook was told.
func assertOptional(t *testing.T, run *mergeRun, row *landingRow) {
	t.Helper()

	if row.kept {
		assert.Check(t, is.Equal(run.written(), run.before))
	}

	if row.on != "" {
		assert.Check(t, is.Equal(run.fixture.Git("rev-parse", "--abbrev-ref", "HEAD"), row.on))
	}

	if len(row.told) > 0 {
		assertTold(t, run.watch.told(t), markAll(row.told, run.tip))
	}
}

// assertSaid fails unless text holds the parts of want, then, when after is not empty, a message
// that ends the line want ends on, then the parts of after on the lines below.
func assertSaid(t *testing.T, text, want, after string) {
	t.Helper()

	assertSays(t, text, want)

	if after == "" {
		return
	}

	rest, _ := past(text, want)
	said, below, ended := strings.Cut(rest, "\n")
	assert.Check(t, ended && strings.Trim(said, ":;, ") != "",
		"RenderStyled() = %q, want a message on the line that holds %q", text, partsOf(want))
	assertSays(t, below, after)
}

// moveOrigin moves the bare origin away, so that origin no longer answers.
func moveOrigin(t *testing.T, f *gittest.Fixture) {
	t.Helper()

	err := os.Rename(f.OriginDir(), f.MissingOriginDir()+"-moved")
	assert.NilError(t, err)
}

// listedEarlier returns the arrange and the forge of a run whose forge lists pull request 12 at the
// trunk's commit, while origin's ref of the pull request is already the tip.
func listedEarlier() (func(*gittest.Fixture), forgeFor) {
	var earlier string

	arrange := func(f *gittest.Fixture) {
		f.PullRef(opened, f.Git("rev-parse", topic))
		earlier = f.Git("rev-parse", trunk)
	}

	return arrange, toLand(nil, nil).pulling(func(pull *forge.Change) { pull.HeadSHA = earlier })
}

// landedRow is one run of `ils merge` that lands the pull request.
type landedRow struct {
	arrange func(*gittest.Fixture)
	script  forgeFor
	payload *result.Merge
	name    string
	// lead is the text before the line that says the pull request merged.
	lead string
	// told is every event the progress hook is told, with tipMark where the sha of the tip is
	// printed; empty leaves them unchecked.
	told      []string
	unbounded bool
}

// landedRows returns the runs that land the pull request because every check holds.
func landedRows() []landedRow {
	// unverified is the payload of a run on a repository with no CI, where no check reported.
	unverified := sent(landedBody, true, result.EndingTrunk)
	unverified.Verdict, unverified.Checks = forge.VerdictNone, nil
	earlierArrange, earlierScript := listedEarlier()

	return []landedRow{
		{
			name: "under a deadline", script: toLand(nil, nil),
			told: toldAfterWait(mergingStep(), said(mergedLine()), deletingStep(), said(deletedLine()), movingStep(), said(onTrunkLine())),
		},
		{
			// The wait polls the check until it passes, with no deadline to end it first.
			name:   "under a context with no deadline, with a check still running at the first read",
			script: waiting(running(), passing()).saying(titledAfter(landedBody)).landing(nil, nil), unbounded: true,
		},
		{
			name: "nothing reported and no CI on the trunk", lead: noCILine(), payload: unverified,
			script: waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{}).saying(titledAfter(landedBody)).
				landing(nil, nil),
		},
		{
			name:    "the forge lists an earlier head while origin's ref of the pull request is the tip",
			arrange: earlierArrange, script: earlierScript,
		},
		{
			// The poll of the merge state takes the wait's step, so the step of the checks starts again
			// before their line is said.
			name: "the merge state unknown after the checks and clean at the next poll",
			script: toLand(nil, nil).showing(cleanHead(), inState(forge.MergeUnknown),
				cleanHead()),
			told: []string{
				checkingStep(), findingStep(), waitingStep(), "count 1 of 1",
				mergeStateStep(), waitingStep(), said(countLine()),
				mergingStep(), said(mergedLine()), deletingStep(), said(deletedLine()), movingStep(), said(onTrunkLine()),
			},
		},
	}
}

func TestMerge_WhenEveryCheckHolds_LandsThePullRequest(t *testing.T) {
	t.Parallel()

	for _, tt := range landedRows() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				run := runMergeOn(t, setting{arrange: tt.arrange, script: tt.script, unbounded: tt.unbounded})

				lead := cmp.Or(tt.lead, countLine())
				assertEndedBy(t, run.got, run.gitDir, ended{text: mark(lead, run.tip) + mergedLine() + deletedLine() + onTrunkLine(), req: mergeRequest(), outcome: result.Done})

				want := *cmp.Or(tt.payload, sent(landedBody, true, result.EndingTrunk))
				want.SHA = run.tip
				assert.Check(t, is.DeepEqual(mergePayload(t, run.got), &want, cmpopts.EquateEmpty()))
				assertChecksReadOn(t, run.forge.Calls(), run.tip)

				landed := forge.Landing{Number: opened, HeadSHA: run.tip, Subject: squashSubject, Body: landedBody}
				assert.Check(t, is.DeepEqual(callsOf(run.forge.Calls(), forgetest.OpLand, forgetest.OpDeleteBranch), []forgetest.Call{
					{Op: forgetest.OpLand, Repo: origin(), Landing: landed},
					{Op: forgetest.OpDeleteBranch, Repo: origin(), Branch: topic},
				}, whichRepo()))
				assertOnTrunk(t, run.fixture, run.fixture.Dir())

				if len(tt.told) > 0 {
					assertTold(t, run.watch.told(t), markAll(tt.told, run.tip))
				}
			})
		})
	}
}

// assertChecksReadOn fails unless every read of the checks among calls is about the commit sha.
func assertChecksReadOn(t *testing.T, calls []forgetest.Call, sha string) {
	t.Helper()

	for _, call := range calls {
		if call.Op == forgetest.OpVerdict {
			assert.Check(t, is.Equal(call.VerdictQuery.SHA, sha))
		}
	}
}

// assertOnTrunk fails unless the worktree at dir is on the trunk at origin's tip and the topic
// branch is gone from the repository.
func assertOnTrunk(t *testing.T, f *gittest.Fixture, dir string) {
	t.Helper()

	assert.Check(t, is.Equal(f.Git("-C", dir, "rev-parse", "--abbrev-ref", "HEAD"), trunk))
	assert.Check(t, is.Equal(f.Git("rev-parse", trunk), f.OriginGit("rev-parse", trunk)))
	assert.Check(t, is.Equal(f.Git("branch", "--list", topic), ""))
}

// assertDetached fails unless the worktree at dir is detached at origin's tip, the local trunk is
// still at was, and the topic branch is gone from the repository.
func assertDetached(t *testing.T, f *gittest.Fixture, dir, was string) {
	t.Helper()

	assert.Check(t, is.Equal(f.Git("-C", dir, "rev-parse", "--abbrev-ref", "HEAD"), "HEAD"))
	assert.Check(t, is.Equal(f.Git("-C", dir, "rev-parse", "HEAD"), f.OriginGit("rev-parse", trunk)))
	assert.Check(t, is.Equal(f.Git("rev-parse", trunk), was))
	assert.Check(t, is.Equal(f.Git("branch", "--list", topic), ""))
}

// lockRef leaves the lock file git takes to write ref, so that git cannot write it.
func lockRef(ref string) func(*testing.T, *gittest.Fixture) {
	return func(t *testing.T, f *gittest.Fixture) {
		t.Helper()
		gittest.WriteFile(t, f.Git("rev-parse", "--absolute-git-dir"), ref+".lock", "")
	}
}

func TestMerge_WhenTheTrunkIsCheckedOutInAnotherWorktree_EndsDetachedAtOriginsTrunk(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var was string

		run := runMergeOn(t, setting{
			arrange: func(f *gittest.Fixture) {
				f.TrunkWorktree()
				was = f.Git("rev-parse", trunk)
			},
			script: toLand(nil, nil),
		})

		assertEndedBy(t, run.got, run.gitDir, ended{text: mark(countLine(), run.tip) + mergedLine() + deletedLine() + detachedLine() + elsewhereLine(), req: mergeRequest(), outcome: result.Done})

		want := sent(landedBody, true, result.EndingDetached)
		want.SHA = run.tip
		assert.Check(t, is.DeepEqual(mergePayload(t, run.got), want))
		assertDetached(t, run.fixture, run.fixture.Dir(), was)
	})
}

// endingBreaks are the runs of `ils merge` whose local ending fails after the merge and the delete
// of the branch on origin went through.
func endingBreaks() []landingRow {
	lead := countLine() + mergedLine() + deletedLine()
	ran := func(step string) string { return expect("Ran", step) }
	notRun := func(step string) string { return expect("Not run", step) }
	failed := func(step string) string { return expect(step, "failed") }
	byHand := func(steps ...string) string { return expect(append(steps, "by hand")...) }

	// row is a run whose ending fails as text and after say, leaving HEAD where on says.
	row := func(name string, set setting, on, text, after string) landingRow {
		set.script = toLand(nil, nil)

		return landingRow{
			name: name, set: set, outcome: result.Broke, text: lead + text, after: after, on: on,
			payload: sent(landedBody, true, ""),
		}
	}
	trunkElsewhere := func(f *gittest.Fixture) { f.TrunkWorktree() }
	switching := row("the switch to the trunk", setting{arrange: func(f *gittest.Fixture) { f.UntrackedOnTrunk() }}, topic,
		failed(switchStep), notRun(dropStep)+notRun(pullStep)+failed(switchStep)+mergeCommand().fixed)
	switching.told = toldAfterWait(mergingStep(), said(mergedLine()), deletingStep(), said(deletedLine()), movingStep())

	return []landingRow{
		switching,
		row("the delete of the branch on the trunk", setting{landed: lockRef("refs/heads/" + topic)}, trunk,
			ran(switchStep)+failed(dropStep), notRun(pullStep)+failed(dropStep)+byHand(dropStep, pullStep)),
		row("the delete of the branch when detached",
			setting{arrange: trunkElsewhere, landed: lockRef("refs/heads/" + topic)}, "HEAD",
			ran(fetchStep)+ran(detachStep)+failed(dropStep), failed(dropStep)+byHand(dropStep)),
	}
}

func TestMerge_WhenAStepOfTheLocalEndingFails_SaysWhichStepsRan(t *testing.T) {
	t.Parallel()

	runLandings(t, endingBreaks())
}
