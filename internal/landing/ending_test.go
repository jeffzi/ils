package landing_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

const (
	switchStep = "`git switch " + trunk + "`"
	dropStep   = "`git branch -D " + topic + "`"
	pullStep   = "`git pull --ff-only origin " + trunk + "`"
	fetchStep  = "`git fetch origin " + trunk + "`"
	detachStep = "`git switch --detach origin/" + trunk + "`"

	detachedLine  = "Detached at origin's tip of " + trunk + ", with " + topic + " deleted locally\n"
	elsewhereLine = "The trunk " + trunk + " is checked out in another worktree and updates there on its next " +
		"`git pull --ff-only`\n"
	resumedLine = "Pull request #7 was already merged into " + trunk + "\n"

	readStillOnOrigin = "whether origin still has the branch"

	mergedPullURL = "https://github.com/Acme/Widgets/pull/7"

	// landedBody is the body of the message of pull request 12 that toLand reads.
	landedBody = "Tidy the readme."

	nextNone = "Next: none\n"

	mergedLine  = "Merged pull request #12 into " + trunk + ": " + worded + " (#12)\n"
	deletedLine = "Deleted the branch " + topic + " on origin\n"
	goneLine    = "The branch " + topic + " was already gone from origin\n"
	onTrunkLine = "On " + trunk + " at origin's tip, with " + topic + " deleted locally\n"
	stillThere  = "Could not delete the branch " + topic + " on origin, so it is still there: HTTP 502\n"
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
	on      string
	set     setting
	outcome result.Outcome
	// kept is whether the run must write nothing.
	kept bool
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

				if tt.kept {
					assert.Check(t, is.Equal(run.written(), run.before))
				}

				if tt.on != "" {
					assert.Check(t, is.Equal(run.fixture.Git("rev-parse", "--abbrev-ref", "HEAD"), tt.on))
				}
			})
		})
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

// writes returns the calls of calls that write on the forge.
func writes(calls []forgetest.Call) []forgetest.Call {
	return slices.DeleteFunc(calls, func(call forgetest.Call) bool {
		return call.Op != forgetest.OpLand && call.Op != forgetest.OpDeleteBranch
	})
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

func TestMerge_WhenEveryCheckHolds_LandsThePullRequest(t *testing.T) {
	t.Parallel()

	// unverified is the payload of a run on a repository with no CI, where no check reported.
	unverified := sent(landedBody, true, result.EndingTrunk)
	unverified.Verdict, unverified.Checks = string(forge.VerdictNone), nil
	earlierArrange, earlierScript := listedEarlier()

	tests := []struct {
		arrange func(*gittest.Fixture)
		script  forgeFor
		payload *result.Merge
		name    string
		// lead is the text before the line that says the pull request merged.
		lead      string
		unbounded bool
	}{
		{name: "under a deadline", script: toLand(nil, nil)},
		{
			// The wait polls the check until it passes, with no deadline to end it first.
			name:   "under a context with no deadline, with a check still running at the first read",
			script: waiting(running(), passing()).saying(titledAfter(landedBody)).landing(nil, nil), unbounded: true,
		},
		{
			name: "nothing reported and no CI on the trunk", lead: noCILine + cleanLine, payload: unverified,
			script: waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{}).saying(titledAfter(landedBody)).
				landing(nil, nil),
		},
		{
			name:    "the forge lists an earlier head while origin's ref of the pull request is the tip",
			arrange: earlierArrange, script: earlierScript,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				run := runMergeOn(t, setting{arrange: tt.arrange, script: tt.script, unbounded: tt.unbounded})

				assertStoppedBy(t, run.got, result.Done, mergeRequest())

				text, _ := shown(t, run.got, run.gitDir)
				lead := cmp.Or(tt.lead, countLine+cleanLine)
				assert.Check(t, is.Equal(text, mark(lead, run.tip)+mergedLine+deletedLine+onTrunkLine+nextNone))

				want := *cmp.Or(tt.payload, sent(landedBody, true, result.EndingTrunk))
				want.SHA = run.tip
				assert.Check(t, is.DeepEqual(mergePayload(t, run.got), &want, cmpopts.EquateEmpty()))
				assertChecksReadOn(t, run.forge.Calls(), run.tip)

				landed := forge.Landing{Number: opened, HeadSHA: run.tip, Subject: worded + " (#12)", Body: landedBody}
				assert.Check(t, is.DeepEqual(writes(run.forge.Calls()), []forgetest.Call{
					{Op: forgetest.OpLand, Repo: origin(), Landing: landed},
					{Op: forgetest.OpDeleteBranch, Repo: origin(), Branch: topic},
				}, whichRepo()))
				assertOnTrunk(t, run.fixture, run.fixture.Dir())
			})
		})
	}
}

// assertChecksReadOn fails unless every read of the checks among calls is about the commit sha.
func assertChecksReadOn(t *testing.T, calls []forgetest.Call, sha string) {
	t.Helper()

	for _, call := range calls {
		if call.Op == forgetest.OpVerdict {
			assert.Check(t, is.Equal(call.Query.SHA, sha))
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
				assert.Check(t, is.Equal(text, mark(countLine, run.tip)+cleanLine+mergedLine+deletedLine+
					detachedLine+elsewhereLine+nextNone))

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
	const (
		lead     = countLine + cleanLine + mergedLine + deletedLine
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

	return []landingRow{
		row("the switch to the trunk", setting{arrange: func(f *gittest.Fixture) { f.UntrackedOnTrunk() }}, topic,
			switchStep+failed, "\n"+notRun+dropStep+"\n"+notRun+pullStep+"\n"+nextMergeFixed),
		row("the delete of the branch on the trunk", setting{landed: lockRef("refs/heads/" + topic)}, trunk,
			"Ran "+switchStep+"\n"+dropStep+failed,
			"\n"+notRun+pullStep+"\n"+nextHand+dropStep+" and "+pullStep+byHand),
		row("the pull of the trunk", setting{landed: func(_ *testing.T, f *gittest.Fixture) { f.AdvanceTrunk() }}, trunk,
			"Ran "+switchStep+"\nRan "+dropStep+"\n"+pullStep+failed, "\n"+nextHand+pullStep+byHand),
		row("the fetch of origin's trunk", setting{arrange: trunkElsewhere, landed: moveOrigin}, topic,
			fetchStep+failed, "\n"+notRun+detachStep+"\n"+notRun+dropStep+"\n"+nextMergeFixed),
		row("the detach at origin's trunk", setting{arrange: trunkElsewhere, landed: lockRef("HEAD")}, topic,
			"Ran "+fetchStep+"\n"+detachStep+failed, "\n"+notRun+dropStep+"\n"+nextMergeFixed),
		row("the delete of the branch when detached",
			setting{arrange: trunkElsewhere, landed: lockRef("refs/heads/" + topic)}, "HEAD",
			"Ran "+fetchStep+"\nRan "+detachStep+"\n"+dropStep+failed, "\n"+nextHand+dropStep+byHand),
	}
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
			text: resumedLine + deletedLine + onTrunkLine + nextNone, ending: result.EndingTrunk,
			calls: append(asked(changesRead()), forgetest.Call{Op: forgetest.OpDeleteBranch, Repo: origin(), Branch: topic}),
		},
		{
			name: "branch gone from origin and the trunk in a linked worktree", script: resuming(),
			arrange: func(f *gittest.Fixture) string {
				f.LandPull(merged, topic)
				f.TrunkWorktree()

				return ""
			},
			text:   resumedLine + goneLine + detachedLine + elsewhereLine + nextNone,
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
			text: resumedLine + goneLine + onTrunkLine + nextNone, ending: result.EndingTrunk, calls: asked(changesRead()),
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
	return []landingRow{
		{
			name: "the delete fails and origin still lists the branch", outcome: result.Broke, kept: true,
			set:  setting{arrange: landAndPublish, script: resuming().deleting(errors.New("HTTP 502"))},
			text: resumedLine + stillThere + nextMergeAgain, payload: resumed("", ""),
		},
		{
			name: "fewer than 20 seconds are left before the first write", outcome: result.Pending, kept: true,
			set: setting{arrange: landAndPublish, script: resuming(), left: 15 * time.Second},
			text: resumedLine + "Fewer than 20 seconds of the time limit were left to finish the merge, " +
				"so nothing was written\n" + nextMergeAgain,
			payload: resumed("", ""),
		},
	}
}

func TestMerge_WhenAResumeCannotFinish_EndsWithNothingWritten(t *testing.T) {
	t.Parallel()

	runLandings(t, resumeStops())
}

func TestMerge_WhenTheResumeCannotAskOriginForTheBranch_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()

	s := newScene(t, func(f *gittest.Fixture) { f.LandPull(merged, topic) }, forgetest.Script{}, false)
	tip := s.fixture.Git("rev-parse", topic)
	s.useForge(t, resuming()(tip))
	// Origin answers about its trunk, the fetch of it, and the fetch of the merged head, and not
	// whether it still has the branch.
	s.stopAnswering(t, 3)
	before := s.written()

	got := landing.Merge(t.Context(), s.deps, mergeRequest())

	assertStoppedBy(t, got, result.Broke, mergeRequest())

	text, reason := shown(t, got, s.gitDir)
	prefix := resumedLine + "Could not read " + readStillOnOrigin + ": "
	suffix := "\nNothing was written.\n" + nextMergeFixed
	assert.Check(t, strings.HasPrefix(text, prefix), "RenderText() = %q, want it to start with %q", text, prefix)
	assert.Check(t, strings.HasSuffix(text, suffix), "RenderText() = %q, want it to end with %q", text, suffix)
	assert.Check(t, is.Equal(reason, "could not read "+readStillOnOrigin))
	assert.Check(t, is.DeepEqual(mergePayload(t, got), resumed(tip, ""), cmpopts.EquateEmpty()))
	assert.Check(t, is.Equal(s.written(), before))
}

func TestMerge_WhenTheDeadlineEndsAsTheResumeAsksOriginForTheBranch_RefusesNamingTheRead(t *testing.T) {
	t.Parallel()

	s := newScene(t, landAndPublish, forgetest.Script{}, false)
	tip := s.fixture.Git("rev-parse", topic)
	s.useForge(t, resuming()(tip))
	// Origin answers about its trunk, the fetch of it, and the fetch of the merged head.
	ctx := stalling(3)(t, s)
	before := s.written()

	got := landing.Merge(ctx, s.deps, mergeRequest())

	assertStoppedBy(t, got, result.Refused, mergeRequest())

	text, _ := shown(t, got, s.gitDir)
	assert.Check(t, is.Equal(text, withLines(overtimeText(mergeCommand(), readStillOnOrigin), resumedLine)))
	assert.Check(t, is.DeepEqual(mergePayload(t, got), resumed(tip, ""), cmpopts.EquateEmpty()))
	assert.Check(t, is.Equal(s.written(), before))
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
	lead := countLine + cleanLine
	row := func(name string, script forgeFor, outcome result.Outcome, text string) landingRow {
		return landingRow{
			name: name, set: setting{script: script}, outcome: outcome, text: lead + text,
			payload: sent(landedBody, true, ""),
		}
	}
	refused := row("the merge fails", toLand(errors.New("HTTP 405: Head branch was modified"), nil), result.Broke,
		"GitHub did not report the merge of pull request #12 as done: HTTP 405: Head branch was modified\n"+nextMergeAgain)
	refused.payload, refused.kept = sent(landedBody, false, ""), true
	cut := row("the deadline ends in the merge", toLand(fmt.Errorf("gh pr merge: %w", context.DeadlineExceeded), nil),
		result.Broke, "Started the merge of pull request #12 when the time limit ended; "+
			"it may or may not have gone through\n"+nextMergeAgain)
	cut.payload, cut.kept = sent(landedBody, false, ""), true
	gone := row("the delete fails and origin no longer lists the branch", toLand(nil, failed), result.Done,
		mergedLine+goneLine+onTrunkLine+nextNone)
	gone.payload = sent(landedBody, true, result.EndingTrunk)
	listed := row("the delete fails and origin still lists the branch", toLand(nil, failed), result.Broke,
		mergedLine+stillThere+nextMergeAgain)
	listed.set.arrange = func(f *gittest.Fixture) { f.Publish(topic) }
	// The question fails with git's own message, which the text holds on one line before the `Next:` line.
	unasked := row("the delete fails and origin cannot be asked", toLand(nil, failed), result.Broke,
		mergedLine+"Could not delete the branch "+topic+" on origin: HTTP 502\n"+
			"Could not read "+readStillOnOrigin+": ")
	unasked.after = "\n" + nextMergeAgain
	unasked.set.landed = moveOrigin

	return []landingRow{refused, cut, gone, listed, unasked}
}

func TestMerge_WhenAWriteOnGitHubFails_SaysWhatWentThrough(t *testing.T) {
	t.Parallel()

	runLandings(t, writeStops())
}
