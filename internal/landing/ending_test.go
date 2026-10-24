package landing_test

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

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
	switchStep = "`git switch " + trunk + "`"
	dropStep   = "`git branch -D " + topic + "`"
	pullStep   = "`git pull --ff-only origin " + trunk + "`"
	fetchStep  = "`git fetch origin " + trunk + "`"
	detachStep = "`git switch --detach refs/remotes/origin/" + trunk + "`"

	detachedLine  = "Detached at origin's tip of " + trunk + ", with " + topic + " deleted locally\n"
	elsewhereLine = "The trunk " + trunk + " is checked out in another worktree and updates there on its next " +
		"`git pull --ff-only`\n"
	resumedLine = "Pull request #7 was already merged into " + trunk + "\n"

	interruptedResumeText = resumedLine + "ils was interrupted before finishing the merge, so nothing was written\n" +
		"Pending: ils was interrupted before finishing the merge\n" + nextMergeAgain

	readStillOnOrigin = "whether origin still has the branch"

	mergedPullURL = "https://github.com/Acme/Widgets/pull/7"

	// landedBody is the body of the message of pull request 12 that toLand reads.
	landedBody = "Tidy the readme."

	mergedLine  = "Merged pull request #12 into " + trunk + ": " + worded + " (#12)\n"
	deletedLine = "Deleted the branch " + topic + " on origin\n"
	goneLine    = "The branch " + topic + " was already gone from origin\n"
	onTrunkLine = "On " + trunk + " at origin's tip, with " + topic + " deleted locally\n"
	stillThere  = "Could not delete the branch " + topic + " on origin, so it is still there: HTTP 502\n"

	stillThereReason = "Broke: the branch is still on origin\n"
	refusedMerge     = "HTTP 405: Head branch was modified"
)

// landingRow is one run of `ils merge` that reaches its writes or a resume, and how it ends.
type landingRow struct {
	// payload is the merge payload the run carries; an empty sha is the tip's.
	payload *result.Merge
	name    string
	// text is the whole text of the result, with tipMark where the sha of the tip is printed. With
	// after set, it is the text before a message of git's or gh's own.
	text string
	// after is the text after that message; empty when the text holds none.
	after string
	// on is where the run leaves HEAD: the name of the branch it is on, or HEAD when it is detached;
	// empty leaves it unchecked.
	on  string
	set setting
	// drawn are parts of the text and how a terminal with color draws them; empty leaves them
	// unchecked.
	drawn []drawnPart
	// told is every event the progress hook is told, with tipMark where the sha of the tip is
	// printed; empty leaves them unchecked.
	told    []string
	outcome result.Outcome
	// kept is whether the run must write nothing.
	kept bool
}

// toldAfterWait returns every event the progress hook is told by a run that waits for its one check
// to pass, ends the wait 20 seconds before the deadline, and then tells after.
func toldAfterWait(after ...string) []string {
	waited := []string{checkingStep, findingStep, waitingStep(bound - 20*time.Second), "count 1 of 1", said(countLine)}

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
// HEAD, how the text is drawn and what the progress hook was told.
func assertOptional(t *testing.T, run *mergeRun, row *landingRow) {
	t.Helper()

	if row.kept {
		assert.Check(t, is.Equal(run.written(), run.before))
	}

	if row.on != "" {
		assert.Check(t, is.Equal(run.fixture.Git("rev-parse", "--abbrev-ref", "HEAD"), row.on))
	}

	assertDrawn(t, run.got, run.gitDir, row.drawn)

	if len(row.told) > 0 {
		assert.Check(t, is.DeepEqual(run.watch.told(t), markAll(row.told, run.tip)))
	}
}

// assertSaid fails unless text is want, or, when after is not empty, unless text is want, then a
// message on one line, then after.
func assertSaid(t *testing.T, text, want, after string) {
	t.Helper()

	if after == "" {
		assert.Check(t, is.Equal(text, want))

		return
	}

	said, starts := strings.CutPrefix(text, want)
	said, ends := strings.CutSuffix(said, after)
	assert.Check(t, starts && ends && said != "" && !strings.Contains(said, "\n"),
		"RenderText() = %q, want %q, then a message on one line, then %q", text, want, after)
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
	// drawn are parts of the text and how a terminal with color draws them, with tipMark where
	// the sha of the tip is printed; empty leaves them unchecked.
	drawn []drawnPart
	// told is every event the progress hook is told, with tipMark where the sha of the tip is
	// printed; empty leaves them unchecked.
	told      []string
	unbounded bool
}

// landedRows returns the runs that land the pull request because every check holds.
func landedRows() []landedRow {
	// unverified is the payload of a run on a repository with no CI, where no check reported.
	unverified := sent(landedBody, true, result.EndingTrunk)
	unverified.Verdict, unverified.Checks = string(forge.VerdictNone), nil
	earlierArrange, earlierScript := listedEarlier()

	return []landedRow{
		{
			name: "under a deadline", script: toLand(nil, nil),
			drawn: []drawnPart{
				{within: "Checks for " + tipMark, part: tipMark, want: resulttest.SHALook()},
				{within: "Merged pull request #12", part: "#12", want: resulttest.NameLook()},
				{within: "into " + trunk, part: trunk, want: resulttest.NameLook()},
				{within: "Deleted the branch " + topic, part: topic, want: resulttest.NameLook()},
				{within: "On " + trunk, part: trunk, want: resulttest.NameLook()},
				{within: "with " + topic + " deleted", part: topic, want: resulttest.NameLook()},
			},
			told: toldAfterWait(mergingStep, said(mergedLine), deletingStep, said(deletedLine), movingStep, said(onTrunkLine)),
		},
		{
			// The wait polls the check until it passes, with no deadline to end it first.
			name:   "under a context with no deadline, with a check still running at the first read",
			script: waiting(running(), passing()).saying(titledAfter(landedBody)).landing(nil, nil), unbounded: true,
		},
		{
			name: "nothing reported and no CI on the trunk", lead: noCILine + cleanLine(), payload: unverified,
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
			script: toLand(nil, nil).showing(inState(forge.MergeClean, "CLEAN"), inState(forge.MergeUnknown, "UNKNOWN"),
				inState(forge.MergeClean, "CLEAN")),
			told: []string{
				checkingStep, findingStep, waitingStep(bound - 20*time.Second), "count 1 of 1",
				mergeStateStep, waitingStep(bound - 20*time.Second - mergePoll), said(countLine),
				mergingStep, said(mergedLine), deletingStep, said(deletedLine), movingStep, said(onTrunkLine),
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

				assertStoppedBy(t, run.got, result.Done, mergeRequest())

				text, _ := shown(t, run.got, run.gitDir)
				lead := cmp.Or(tt.lead, countLine+cleanLine())
				assert.Check(t, is.Equal(text, mark(lead, run.tip)+mergedLine+deletedLine+onTrunkLine))

				want := *cmp.Or(tt.payload, sent(landedBody, true, result.EndingTrunk))
				want.SHA = run.tip
				assert.Check(t, is.DeepEqual(mergePayload(t, run.got), &want, cmpopts.EquateEmpty()))
				assertChecksReadOn(t, run.forge.Calls(), run.tip)

				landed := forge.Landing{Number: opened, HeadSHA: run.tip, Subject: worded + " (#12)", Body: landedBody}
				assert.Check(t, is.DeepEqual(callsOf(run.forge.Calls(), forgetest.OpLand, forgetest.OpDeleteBranch), []forgetest.Call{
					{Op: forgetest.OpLand, Repo: origin(), Landing: landed},
					{Op: forgetest.OpDeleteBranch, Repo: origin(), Branch: topic},
				}, whichRepo()))
				assertOnTrunk(t, run.fixture, run.fixture.Dir())

				assertDrawn(t, run.got, run.gitDir, atTip(tt.drawn, run.tip))

				if len(tt.told) > 0 {
					assert.Check(t, is.DeepEqual(run.watch.told(t), markAll(tt.told, run.tip)))
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
		gittest.WriteFile(t, f.Git("rev-parse", "--absolute-git-dir"), filepath.FromSlash(ref)+".lock", "")
	}
}

func TestMerge_WhenTheTrunkIsCheckedOutInAnotherWorktree_EndsDetachedAtOriginsTrunk(t *testing.T) {
	t.Parallel()

	tests := []struct {
		// arrange checks the trunk out in a worktree other than the one it returns, where the run is.
		arrange func(*gittest.Fixture) string
		name    string
	}{
		{
			name: "run in the main checkout, with the trunk in a linked worktree",
			arrange: func(f *gittest.Fixture) string {
				f.TrunkWorktree()

				return f.Dir()
			},
		},
		{
			name:    "run in a linked worktree, with the trunk in the main checkout",
			arrange: func(f *gittest.Fixture) string { return f.BranchWorktree(topic) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				var dir, was string

				run := runMergeOn(t, setting{
					arrange: func(f *gittest.Fixture) {
						dir = tt.arrange(f)
						was = f.Git("rev-parse", trunk)
					},
					rewire: func(_ *testing.T, s *scene) { s.deps.Repo.Runner.Dir = dir },
					script: toLand(nil, nil),
				})

				assertStoppedBy(t, run.got, result.Done, mergeRequest())

				text, _ := shown(t, run.got, run.gitDir)
				assert.Check(t, is.Equal(text, mark(countLine, run.tip)+cleanLine()+mergedLine+deletedLine+
					detachedLine+elsewhereLine))

				want := sent(landedBody, true, result.EndingDetached)
				want.SHA = run.tip
				assert.Check(t, is.DeepEqual(mergePayload(t, run.got), want))
				assertDetached(t, run.fixture, dir, was)
			})
		})
	}
}

// endingBreaks are the runs of `ils merge` whose local ending fails after the merge and the delete
// of the branch on origin went through.
func endingBreaks() []landingRow {
	lead := countLine + cleanLine() + mergedLine + deletedLine
	const (
		failed   = " failed: "
		notRun   = "Not run: "
		byHand   = " by hand\n"
		nextHand = "Next: run "
	)

	// row is a run whose ending fails as text and after say, leaving HEAD where on says.
	row := func(name string, set setting, on, text, after string) landingRow {
		set.script = toLand(nil, nil)

		return landingRow{
			name: name, set: set, outcome: result.Broke, text: lead + text, after: after, on: on,
			payload: sent(landedBody, true, ""),
		}
	}
	trunkElsewhere := func(f *gittest.Fixture) { f.TrunkWorktree() }
	// broke is the reason line of an ending whose step failed.
	broke := func(step string) string { return "Broke: " + step + " failed\n" }

	switching := row("the switch to the trunk", setting{arrange: func(f *gittest.Fixture) { f.UntrackedOnTrunk() }}, topic,
		switchStep+failed, "\n"+notRun+dropStep+"\n"+notRun+pullStep+"\n"+broke(switchStep)+nextMergeFixed)
	switching.told = toldAfterWait(mergingStep, said(mergedLine), deletingStep, said(deletedLine), movingStep)

	return []landingRow{
		switching,
		drawnRow(row("the delete of the branch on the trunk", setting{landed: lockRef("refs/heads/" + topic)}, trunk,
			"Ran "+switchStep+"\n"+dropStep+failed,
			"\n"+notRun+pullStep+"\n"+broke(dropStep)+nextHand+dropStep+" and "+pullStep+byHand),
			commandIn("Ran "+switchStep, switchStep),
			commandIn(dropStep+failed, dropStep),
			commandIn(notRun+pullStep, pullStep),
			commandIn("Broke: "+dropStep, dropStep),
			commandIn(nextHand+dropStep, dropStep),
			commandIn(pullStep+byHand, pullStep),
		),
		drawnRow(row("the pull of the trunk", setting{landed: func(_ *testing.T, f *gittest.Fixture) { f.AdvanceTrunk() }}, trunk,
			"Ran "+switchStep+"\nRan "+dropStep+"\n"+pullStep+failed, "\n"+broke(pullStep)+nextHand+pullStep+byHand),
			commandIn(pullStep+byHand, pullStep),
		),
		row("the fetch of origin's trunk", setting{arrange: trunkElsewhere, landed: moveOrigin}, topic,
			fetchStep+failed, "\n"+notRun+detachStep+"\n"+notRun+dropStep+"\n"+broke(fetchStep)+nextMergeFixed),
		row("the detach at origin's trunk", setting{arrange: trunkElsewhere, landed: lockRef("HEAD")}, topic,
			"Ran "+fetchStep+"\n"+detachStep+failed, "\n"+notRun+dropStep+"\n"+broke(detachStep)+nextMergeFixed),
		drawnRow(row("the delete of the branch when detached",
			setting{arrange: trunkElsewhere, landed: lockRef("refs/heads/" + topic)}, "HEAD",
			"Ran "+fetchStep+"\nRan "+detachStep+"\n"+dropStep+failed, "\n"+broke(dropStep)+nextHand+dropStep+byHand),
			commandIn(dropStep+byHand, dropStep),
		),
	}
}

// drawnRow returns row with parts, how a terminal with color draws parts of its text.
func drawnRow(row landingRow, parts ...drawnPart) landingRow {
	row.drawn = parts

	return row
}

func TestMerge_WhenAStepOfTheLocalEndingFails_SaysWhichStepsRan(t *testing.T) {
	t.Parallel()

	runLandings(t, endingBreaks())
}

// mark returns text with the sha tip in place of tipMark.
func mark(text, tip string) string {
	return strings.ReplaceAll(text, tipMark, tip)
}

// resuming is a forge for a branch whose pull request 7 against the trunk is merged. The pull
// request is titled after its branch and the repository's settings forbid squash merges: a resume
// reads neither, nor the trunk's rules, for which the forge has no answer.
func resuming() forgeFor {
	return func(string) forgetest.Script {
		pull := change(merged, trunk, forge.ChangeMerged)
		pull.URL = mergedPullURL

		return forgetest.Script{
			Identity:    []forgetest.Answer[forge.Repo]{{Value: squashing(false)}},
			FindChanges: listing([]forge.Change{pull}),
		}
	}
}

// deleting returns build with a forge whose branch delete answers remove; nil is a delete that
// succeeds.
func (build forgeFor) deleting(remove error) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.DeleteBranch = []error{remove}
	})
}

// resumed returns the payload of a resume of pull request 7 merged at head, which merged nothing
// and ended the checkout at ending.
func resumed(head, ending string) *result.Merge {
	return &result.Merge{
		URL: mergedPullURL, Number: merged, SHA: head,
		Verdict: string(forge.VerdictNone), MergeState: string(forge.MergeUnknown), Ending: ending,
	}
}

// finishedResume is a resume of a branch whose pull request 7 is merged, and how it ends.
type finishedResume struct {
	// arrange merges pull request 7 and returns its head; empty is the tip.
	arrange func(*gittest.Fixture) string
	script  forgeFor
	name    string
	text    string
	ending  string
	calls   []forgetest.Call
	// drawn are parts of the text and how a terminal with color draws them; empty leaves them
	// unchecked.
	drawn []drawnPart
}

// finishedResumes are the resumes that run to their end, done.
func finishedResumes() []finishedResume {
	return []finishedResume{
		{
			name: "branch still on origin", script: resuming().deleting(nil),
			arrange: func(f *gittest.Fixture) string {
				landAndPublish(f)

				return ""
			},
			text: resumedLine + deletedLine + onTrunkLine, ending: result.EndingTrunk,
			calls: append(asked(changesRead()), forgetest.Call{Op: forgetest.OpDeleteBranch, Repo: origin(), Branch: topic}),
			drawn: []drawnPart{
				{within: "Pull request #7", part: "#7", want: resulttest.NameLook()},
				{within: "merged into " + trunk, part: trunk, want: resulttest.NameLook()},
			},
		},
		{
			name: "branch gone from origin and the trunk in a linked worktree", script: resuming(),
			arrange: func(f *gittest.Fixture) string {
				f.LandPull(merged, topic)
				f.TrunkWorktree()

				return ""
			},
			text:   resumedLine + goneLine + detachedLine + elsewhereLine,
			ending: result.EndingDetached, calls: asked(changesRead()),
		},
		{
			// A suggestion committed on the web before the merge gave the merged head a commit the
			// tip lacks.
			name: "merged head past the tip", script: resuming(),
			arrange: func(f *gittest.Fixture) string {
				head := f.Commits(topic, "Apply suggestions from code review")[0]
				f.LandPull(merged, head)
				f.Git("reset", "--quiet", "--hard", "HEAD~1")

				return head
			},
			text: resumedLine + goneLine + onTrunkLine, ending: result.EndingTrunk, calls: asked(changesRead()),
		},
	}
}

func TestMerge_WhenThePullRequestIsAlreadyMerged_FinishesWithoutMerging(t *testing.T) {
	t.Parallel()

	for _, tt := range finishedResumes() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				var was, head string

				run := runMergeOn(t, setting{
					arrange: func(f *gittest.Fixture) {
						head = tt.arrange(f)
						was = f.Git("rev-parse", trunk)
					},
					script: tt.script,
				})

				assertStoppedBy(t, run.got, result.Done, mergeRequest())

				text, _ := shown(t, run.got, run.gitDir)
				assert.Check(t, is.Equal(text, tt.text))
				assert.Check(t, is.DeepEqual(mergePayload(t, run.got), resumed(cmp.Or(head, run.tip), tt.ending),
					cmpopts.EquateEmpty()))
				assert.Check(t, is.DeepEqual(run.forge.Calls(), tt.calls, whichRepo()))
				assertDrawn(t, run.got, run.gitDir, tt.drawn)

				if tt.ending == result.EndingTrunk {
					assertOnTrunk(t, run.fixture, run.fixture.Dir())
				} else {
					assertDetached(t, run.fixture, run.fixture.Dir(), was)
				}
			})
		})
	}
}

// resumeStops are the resumes that end before their local ending, having written nothing.
func resumeStops() []landingRow {
	late := resumedLine + "Fewer than 20 seconds of the time limit were left to finish the merge, " +
		"so nothing was written\n" + "Pending: fewer than 20 seconds of the time limit were left to finish the merge\n" +
		nextMergeAgain
	gone := func(f *gittest.Fixture) { f.LandPull(merged, topic) }

	return []landingRow{
		{
			name: "the delete fails and origin still lists the branch", outcome: result.Broke, kept: true,
			set:  setting{arrange: landAndPublish, script: resuming().deleting(errors.New("HTTP 502"))},
			text: resumedLine + stillThere + stillThereReason + nextMergeAgain, payload: resumed("", ""),
		},
		{
			name: "fewer than 20 seconds are left before the first write", outcome: result.Pending, kept: true,
			set:  setting{arrange: landAndPublish, script: resuming(), left: 15 * time.Second},
			text: late, payload: resumed("", ""),
		},
		{
			name: "fewer than 20 seconds are left and origin no longer lists the branch", outcome: result.Pending,
			kept: true, set: setting{arrange: gone, script: resuming(), left: 15 * time.Second},
			text: late, payload: resumed("", ""),
		},
		{
			name: "an interrupt lands once origin was asked for the branch it still lists", outcome: result.Pending,
			kept: true, set: setting{arrange: landAndPublish, script: resuming(), runCtx: interruptAfterAskingForTheBranch},
			text: interruptedResumeText, payload: resumed("", ""),
		},
		{
			name: "an interrupt lands once origin was asked for the branch it no longer lists", outcome: result.Pending,
			kept: true, set: setting{arrange: gone, script: resuming(), runCtx: interruptAfterAskingForTheBranch},
			text: interruptedResumeText, payload: resumed("", ""),
		},
	}
}

// interruptAfterAskingForTheBranch returns the context of a run that an interrupt cancels once
// git's call that asks origin whether it has the branch has ended, and not before.
func interruptAfterAskingForTheBranch(t *testing.T, s *scene) context.Context {
	t.Helper()

	done := make(chan struct{})
	asked := func(args []string) bool {
		return slices.Contains(args, "ls-remote") &&
			slices.ContainsFunc(args, func(arg string) bool { return strings.HasSuffix(arg, topic) })
	}
	s.deps.Repo.Runner.Logger = slog.New(afterCall{ended: asked, fire: sync.OnceFunc(func() { close(done) })})

	return byInterrupt().once(t.Context(), done)
}

// afterCall is a log handler for the git calls of a run that calls fire once a call whose
// arguments ended says yes to has ended.
type afterCall struct {
	ended func(args []string) bool
	fire  func()
}

// Enabled reports true for every level so that Handle sees each git call's end record regardless
// of the logger's level.
func (afterCall) Enabled(context.Context, slog.Level) bool { return true }

// Handle calls fire when record is the end of a call whose arguments ended says yes to.
func (h afterCall) Handle(_ context.Context, record slog.Record) error {
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key != child.KeyArgs {
			return true
		}

		if args, ok := attr.Value.Resolve().Any().([]string); ok && h.ended(args) {
			h.fire()
		}

		return false
	})

	return nil
}

// WithAttrs returns h, which reads only the attributes of each record.
func (h afterCall) WithAttrs([]slog.Attr) slog.Handler { return h }

// WithGroup returns h, which reads only the attributes of each record.
func (h afterCall) WithGroup(string) slog.Handler { return h }

func TestMerge_WhenAResumeCannotFinish_EndsWithNothingWritten(t *testing.T) {
	t.Parallel()

	runLandings(t, resumeStops())
}

func TestMerge_WhenTheResumeCannotAskOriginForTheBranch_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()

	run := runMergeOn(t, setting{
		arrange: func(f *gittest.Fixture) { f.LandPull(merged, topic) }, script: resuming(), unbounded: true,
		rewire: func(t *testing.T, s *scene) {
			t.Helper()
			// Origin answers about its trunk, the fetch of it, and the fetch of the merged head, and not
			// whether it still has the branch.
			s.stopAnswering(t, 3)
		},
	})

	assertStoppedBy(t, run.got, result.Broke, mergeRequest())

	text, reason := shown(t, run.got, run.gitDir)
	prefix := resumedLine + "Could not read " + readStillOnOrigin + ": "
	suffix := "\nNothing was written.\nBroke: could not read " + readStillOnOrigin + "\n" + nextMergeFixed
	assert.Check(t, strings.HasPrefix(text, prefix), "RenderText() = %q, want it to start with %q", text, prefix)
	assert.Check(t, strings.HasSuffix(text, suffix), "RenderText() = %q, want it to end with %q", text, suffix)
	assert.Check(t, is.Equal(reason, "could not read "+readStillOnOrigin))
	assert.Check(t, is.DeepEqual(mergePayload(t, run.got), resumed(run.tip, ""), cmpopts.EquateEmpty()))
	assert.Check(t, is.Equal(run.written(), run.before))
}

func TestMerge_WhenTheResumeIsCutShortAskingOriginForTheBranch_RefusesNamingTheRead(t *testing.T) {
	t.Parallel()

	for _, c := range cuts() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			// Origin answers about its trunk, the fetch of it, and the fetch of the merged head.
			run := runMergeOn(t, setting{arrange: landAndPublish, script: resuming(), runCtx: c.stalling(3)})

			assertStoppedBy(t, run.got, result.Refused, mergeRequest())

			text, _ := shown(t, run.got, run.gitDir)
			assert.Check(t, is.Equal(text, resumedLine+overtimeText(mergeCommand(), c, readStillOnOrigin)))
			assert.Check(t, is.DeepEqual(mergePayload(t, run.got), resumed(run.tip, ""), cmpopts.EquateEmpty()))
			assert.Check(t, is.Equal(run.written(), run.before))
		})
	}
}

// sent returns the payload of a green run on a clean pull request 12 that sent its merge with a
// body of body, made the merge when merged says so, and ended the checkout at ending.
func sent(body string, merged bool, ending string) *result.Merge {
	payload := composed(worded+" (#12)", body)
	payload.Merged = merged
	payload.Ending = ending

	return payload
}

// toLand is a forge on which pull request 12 is green and clean at the tip, its message is titled
// worded with the body landedBody, and its landing and branch delete answer as land and remove do.
func toLand(land, remove error) forgeFor {
	return waiting(passing()).saying(titledAfter(landedBody)).landing(land, remove)
}

// writeStops are the runs of `ils merge` whose merge, or whose delete of the branch on origin,
// does not go as asked.
func writeStops() []landingRow {
	failed := errors.New("HTTP 502")
	lead := countLine + cleanLine()
	row := func(name string, script forgeFor, outcome result.Outcome, text string) landingRow {
		return landingRow{
			name: name, set: setting{script: script}, outcome: outcome, text: lead + text,
			payload: sent(landedBody, true, ""),
		}
	}
	refused := row("the merge fails", toLand(errors.New(refusedMerge), nil), result.Broke,
		"GitHub did not report the merge of pull request #12 as done: "+refusedMerge+"\n"+
			"Broke: GitHub did not report the merge as done\n"+nextMergeAgain)
	refused.payload, refused.kept = sent(landedBody, false, ""), true
	refused.told = toldAfterWait(mergingStep)
	cutRows := make([]landingRow, 0, len(cuts()))
	for _, c := range cuts() {
		ended := row(c.name+" ends the merge", toLand(c.of("gh pr merge"), nil),
			result.Broke, "Started the merge of pull request #12 when "+c.ends+"; "+
				"it may or may not have gone through\n"+"Broke: "+c.ends+" during the merge\n"+nextMergeAgain)
		ended.payload, ended.kept = sent(landedBody, false, ""), true
		cutRows = append(cutRows, ended)
	}

	gone := row("the delete fails and origin no longer lists the branch", toLand(nil, failed), result.Done,
		mergedLine+goneLine+onTrunkLine)
	gone.payload = sent(landedBody, true, result.EndingTrunk)
	listed := row("the delete fails and origin still lists the branch", toLand(nil, failed), result.Broke,
		mergedLine+stillThere+stillThereReason+nextMergeAgain)
	listed.set.arrange = func(f *gittest.Fixture) { f.Publish(topic) }
	// The question fails with git's own message, which the text holds on one line before the reason line.
	unasked := row("the delete fails and origin cannot be asked", toLand(nil, failed), result.Broke,
		mergedLine+"Could not delete the branch "+topic+" on origin: HTTP 502\n"+
			"Could not read "+readStillOnOrigin+": ")
	unasked.after = "\nBroke: the branch could not be deleted on origin\n" + nextMergeAgain
	unasked.set.landed = moveOrigin
	interrupted := row("an interrupt ends the delete", toLand(nil, byInterrupt().of("gh")), result.Broke,
		mergedLine+"Started the delete of the branch "+topic+" on origin when ils was interrupted; "+
			"it may or may not have gone through\n"+
			"Broke: ils was interrupted while deleting the branch on origin\n"+nextMergeAgain)
	interrupted.set.runCtx = interruptOnStart(deletingStep)

	return slices.Concat([]landingRow{refused}, cutRows, []landingRow{gone, listed, unasked, interrupted})
}

// stepHook is a progress hook that tells next everything it is told, and calls fire once the run
// starts the step whose start reads step as the watch tells it.
type stepHook struct {
	landing.Progress

	t    *testing.T
	fire func()
	step string
}

// Started tells the step to next, then calls fire when it is the step of the hook.
func (h stepHook) Started(label result.Phrase, limit time.Duration) {
	h.Progress.Started(label, limit)

	if "start "+plainText(h.t, label) == h.step {
		h.fire()
	}
}

// interruptOnStart returns the context of a run that an interrupt cancels once the run starts the
// step whose start reads step as the watch tells it.
func interruptOnStart(step string) func(t *testing.T, s *scene) context.Context {
	return func(t *testing.T, s *scene) context.Context {
		t.Helper()

		done := make(chan struct{})
		s.deps.Progress = stepHook{Progress: s.deps.Progress, t: t, step: step, fire: sync.OnceFunc(func() { close(done) })}

		return byInterrupt().once(t.Context(), done)
	}
}

// interruptedWrites are the runs of `ils merge` that an interrupt reaches as the first write
// starts, and which end as the time limit ends them when it leaves too little time for that write.
func interruptedWrites() []landingRow {
	return []landingRow{
		{
			name: "the merge", outcome: result.Pending, kept: true,
			set: setting{script: toLand(nil, nil), runCtx: interruptOnStart(mergingStep)},
			text: countLine + cleanLine() + "ils was interrupted before the merge, so nothing was written\n" +
				"Pending: ils was interrupted before the merge\n" + nextPush,
			payload: sent(landedBody, false, ""),
		},
		{
			name: "the delete of a resume", outcome: result.Pending, kept: true,
			set:     setting{arrange: landAndPublish, script: resuming(), runCtx: interruptOnStart(deletingStep)},
			text:    interruptedResumeText,
			payload: resumed("", ""),
		},
	}
}

func TestMerge_WhenAnInterruptLandsAsTheFirstWriteStarts_EndsPendingWithNothingWritten(t *testing.T) {
	t.Parallel()

	runLandings(t, interruptedWrites())
}

// stallingAfterLand returns the arrange of a run whose git calls whose arguments start with words
// stall, as stallingGit makes them, once the merge went through, and the hook that turns that on
// as the merge lands. An interrupt ends the context once a stalled call is under way. It cannot run
// in a parallel test.
func stallingAfterLand(
	words []string,
) (runCtx func(*testing.T, *scene) context.Context, landed func(*testing.T, *gittest.Fixture)) {
	var stalled string

	runCtx = func(t *testing.T, s *scene) context.Context {
		t.Helper()

		plain := os.Getenv("PATH")
		ctx := byInterrupt().stallingGit(words)(t, s)
		stalled = os.Getenv("PATH")
		t.Setenv("PATH", plain)

		return ctx
	}
	landed = func(t *testing.T, _ *gittest.Fixture) {
		t.Helper()
		t.Setenv("PATH", stalled)
	}

	return runCtx, landed
}

func TestMerge_WhenAnInterruptEndsTheFetchOfTheLocalEnding_BrokeNamingTheInterrupt(t *testing.T) {
	lead := countLine + cleanLine() + mergedLine + deletedLine
	tests := []struct {
		arrange func(*gittest.Fixture)
		name    string
		text    string
		on      string
	}{
		{
			name: "on the trunk", on: trunk,
			text: lead + "Ran " + switchStep + "\nRan " + dropStep + "\n" + pullStep + " stopped when ils was interrupted\n" +
				"Broke: ils was interrupted during " + pullStep + "\nNext: run " + pullStep + " by hand\n",
		},
		{
			name: "with the trunk in another worktree", on: topic,
			arrange: func(f *gittest.Fixture) { f.TrunkWorktree() },
			text: lead + fetchStep + " stopped when ils was interrupted\nNot run: " + detachStep + "\nNot run: " + dropStep +
				"\nBroke: ils was interrupted during " + fetchStep + "\n" + nextMergeFixed,
		},
	}
	// Not parallel: each row puts a stalling git first on PATH with t.Setenv.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runCtx, landed := stallingAfterLand([]string{"fetch"})

			run := runMergeOn(t, setting{arrange: tt.arrange, script: toLand(nil, nil), runCtx: runCtx, landed: landed})

			assertStoppedBy(t, run.got, result.Broke, mergeRequest())

			text, _ := shown(t, run.got, run.gitDir)
			assert.Check(t, is.Equal(text, mark(tt.text, run.tip)))

			want := sent(landedBody, true, "")
			want.SHA = run.tip
			assert.Check(t, is.DeepEqual(mergePayload(t, run.got), want, cmpopts.EquateEmpty()))
			assert.Check(t, is.Equal(run.fixture.Git("rev-parse", "--abbrev-ref", "HEAD"), tt.on))
		})
	}
}

func TestMerge_WhenAWriteOnGitHubFails_SaysWhatWentThrough(t *testing.T) {
	t.Parallel()

	runLandings(t, writeStops())
}
