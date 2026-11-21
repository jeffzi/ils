package landing_test

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
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
)

// resuming is a forge for a branch whose pull request 7 against the trunk is merged. The pull
// request is titled after its branch and the repository's settings forbid squash merges: a resume
// reads neither, nor the trunk's rules, for which the forge has no answer.
func resuming() forgeFor {
	return func(string) forgetest.Script {
		pull := change(merged, trunk, forge.ChangeMerged)
		pull.URL = mergedPullURL

		return forgetest.Script{
			Identity:    forgetest.Values(squashing(false)),
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
func resumed(head string, ending result.Ending) *result.Merge {
	return &result.Merge{
		URL: mergedPullURL, Number: merged, SHA: head,
		Verdict: forge.VerdictNone, MergeState: forge.MergeUnknown, Ending: ending,
	}
}

// finishedResume is a resume of a branch whose pull request 7 is merged, and how it ends.
type finishedResume struct {
	// arrange merges pull request 7 and returns its head; empty is the tip.
	arrange func(*gittest.Fixture) string
	script  forgeFor
	name    string
	text    string
	ending  result.Ending
	calls   []forgetest.Call
}

// deleted returns the forge calls of a resume that read the pull requests and deleted the branch on
// origin.
func deleted() []forgetest.Call {
	return append(asked(changesRead()), forgetest.Call{Op: forgetest.OpDeleteBranch, Repo: origin(), Branch: topic})
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
			text: resumedLine() + deletedLine() + onTrunkLine(), ending: result.EndingTrunk,
			calls: deleted(),
		},
		{
			name: "branch gone from origin and the trunk in a linked worktree", script: resuming(),
			arrange: func(f *gittest.Fixture) string {
				f.LandPull(merged, topic)
				f.TrunkWorktree()

				return ""
			},
			text:   resumedLine() + goneLine() + detachedLine() + elsewhereLine(),
			ending: result.EndingDetached, calls: asked(changesRead()),
		},
		{
			// Origin's branch stayed at the tip, which the merged head carries.
			name: "origin's branch behind the merged head", script: resuming().deleting(nil),
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				head := f.Commits(topic, "Apply suggestions from code review")[0]
				f.LandPull(merged, head)
				f.Git("reset", "--quiet", "--hard", "HEAD~1")

				return head
			},
			text: resumedLine() + deletedLine() + onTrunkLine(), ending: result.EndingTrunk,
			calls: deleted(),
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

				assertEndedBy(t, run.got, run.gitDir, ended{text: tt.text, req: mergeRequest(), outcome: result.Done})
				assert.Check(t, is.DeepEqual(mergePayload(t, run.got), resumed(cmp.Or(head, run.tip), tt.ending),
					cmpopts.EquateEmpty()))
				assert.Check(t, sameCalls(run.forge.Calls(), tt.calls))

				if tt.ending == result.EndingTrunk {
					assertOnTrunk(t, run.fixture, run.fixture.Dir())
				} else {
					assertDetached(t, run.fixture, run.fixture.Dir(), was)
				}
			})
		})
	}
}

func TestMerge_WhenTheMergedPullRequestLacksCommitsOfTheBranch_RefusesNamingTheMergedPullRequest(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var head string

		run := runMergeOn(t, setting{
			arrange: func(f *gittest.Fixture) {
				f.LandPull(merged, topic)
				head = f.Git("rev-parse", topic)
				f.Commits(topic, "fix a typo", "add an example")
			},
			script: resuming(),
		})

		assertStoppedBy(t, run.got, result.Refused, mergeRequest())

		_, reason := shown(t, run.got, run.gitDir)
		assertSays(t, reason, expect("#"+strconv.Itoa(merged), topic, "merged without the commits"))
		assert.Check(t, is.DeepEqual(mergePayload(t, run.got), resumed(head, ""), cmpopts.EquateEmpty()))
		assert.Check(t, sameOps(run.forge.Calls(), changesRead()))
	})
}

func TestMerge_WhenAPullRequestMergedIntoTheTrunkSitsBesideOneMergedElsewhere_FinishesWithoutMerging(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		both := resuming().deleting(nil).with(func(script *forgetest.Script, _ string) {
			script.FindChanges[0].Value = slices.Insert(script.FindChanges[0].Value, 0, elsewhereMerge())
		})
		arrange := func(f *gittest.Fixture) {
			f.PullRef(mergedElsewhere, f.Git("rev-parse", topic))
			landAndPublish(f)
		}

		run := runMergeOn(t, setting{arrange: arrange, script: both})

		assertEndedBy(t, run.got, run.gitDir,
			ended{text: resumedLine() + deletedLine() + onTrunkLine(), req: mergeRequest(), outcome: result.Done})
		assert.Check(t, is.DeepEqual(mergePayload(t, run.got), resumed(run.tip, result.EndingTrunk), cmpopts.EquateEmpty()))
	})
}

// resumeStops are the resumes that end before their local ending, having written nothing.
func resumeStops() []landingRow {
	// short is under landing.MergeReserve, so no write fits.
	const short = 15 * time.Second

	late := resumedLine() + expect("finish the merge", "nothing was written", "finish the merge") +
		mergeCommand().again
	gone := func(f *gittest.Fixture) { f.LandPull(merged, topic) }

	stop := func(name string, set setting, text string) landingRow {
		set.script = resuming()

		return landingRow{name: name, set: set, text: text, outcome: result.Pending, kept: true, payload: resumed("", "")}
	}

	return []landingRow{
		stop("fewer than 20 seconds are left before the first write",
			setting{arrange: landAndPublish, left: short}, late),
		stop("fewer than 20 seconds are left and origin no longer lists the branch",
			setting{arrange: gone, left: short}, late),
		stop("an interrupt lands once origin was asked for the branch it still lists",
			setting{arrange: landAndPublish, runCtx: interruptAfterAskingForTheBranch}, interruptedResumeText()),
		stop("an interrupt lands once origin was asked for the branch it no longer lists",
			setting{arrange: gone, runCtx: interruptAfterAskingForTheBranch}, interruptedResumeText()),
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

// assertResumeUnread fails unless run broke on a resume that could not read read, naming it, with
// the merge payload of a resume that wrote nothing.
func assertResumeUnread(t *testing.T, run mergeRun, read string) {
	t.Helper()

	assertStoppedBy(t, run.got, result.Broke, mergeRequest())

	text, reason := shown(t, run.got, run.gitDir)
	assertSays(t, text, resumedLine()+expect(read, nothingWritten)+mergeCommand().fixed)
	between(t, text, read, nothingWritten)
	assert.Check(t, is.Contains(reason, read))
	assert.Check(t, is.DeepEqual(mergePayload(t, run.got), resumed(run.tip, ""), cmpopts.EquateEmpty()))
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

	assertResumeUnread(t, run, readStillOnOrigin)
	assert.Check(t, is.Equal(run.written(), run.before))
}

func TestMerge_WhenOriginsBranchHoldsCommitsTheMergedHeadLacks_RefusesListingThem(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var pushed string

		run := runMergeOn(t, setting{
			arrange: func(f *gittest.Fixture) {
				landAndPublish(f)
				pushed = f.AdvanceOrigin(topic)
			},
			script: resuming(),
		})

		assertStoppedBy(t, run.got, result.Refused, mergeRequest())

		text, reason := shown(t, run.got, run.gitDir)
		assertSays(t, text, expect(shortSHA(pushed), "origin change on "+topic))
		assertSays(t, reason, expect(topic))
		assert.Check(t, is.DeepEqual(mergePayload(t, run.got), resumed(run.tip, ""), cmpopts.EquateEmpty()))
		assert.Check(t, sameOps(run.forge.Calls(), changesRead()))
		assert.Check(t, is.Equal(run.written(), run.before))
	})
}

// readBranchOnOrigin is the read of the commits origin's copy of the branch holds.
const (
	readBranchOnOrigin = "origin's copy of the branch"

	refusedMerge = "HTTP 405: Head branch was modified"
)

func TestMerge_WhenTheResumeCannotReadOriginsBranch_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()

	run := runMergeOn(t, setting{
		arrange: func(f *gittest.Fixture) {
			landAndPublish(f)
			f.AdvanceOrigin(topic)
		},
		script: resuming(), unbounded: true,
		rewire: func(t *testing.T, s *scene) {
			t.Helper()
			// Origin answers about its trunk, the fetch of it, the fetch of the merged head, and whether
			// it still has the branch, and not the fetch of the commits its branch holds.
			s.stopAnswering(t, 4)
		},
	})

	assertResumeUnread(t, run, readBranchOnOrigin)
	assert.Check(t, sameOps(run.forge.Calls(), changesRead()))
	assert.Check(t, is.Equal(run.written(), run.before))
}

// sent returns the payload of a green run on a clean pull request 12 that sent its merge with a
// body of body, made the merge when merged says so, and ended the checkout at ending.
func sent(body string, merged bool, ending result.Ending) *result.Merge {
	payload := composed(squashSubject, body)
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
	failed := errors.New(forgeFailure)
	lead := countLine()
	row := func(name string, script forgeFor, outcome result.Outcome, text string) landingRow {
		return landingRow{
			name: name, set: setting{script: script}, outcome: outcome, text: lead + text,
			payload: sent(landedBody, true, ""),
		}
	}
	refused := row("the merge fails", toLand(errors.New(refusedMerge), nil), result.Broke,
		expect("did not report the merge", "#12", refusedMerge))
	refused.payload, refused.kept = sent(landedBody, false, ""), true
	refused.told = toldAfterWait(mergingStep())
	cutRows := make([]landingRow, 0, len(cuts()))
	for _, c := range cuts() {
		ended := row(c.name+" ends the merge", toLand(c.of("gh pr merge"), nil),
			result.Broke, expect("merge", "#12", c.ends, "may or may not have gone through", c.ends, "during the merge")+
				mergeCommand().again)
		ended.payload, ended.kept = sent(landedBody, false, ""), true
		cutRows = append(cutRows, ended)
	}

	gone := row("the delete fails and origin no longer lists the branch", toLand(nil, failed), result.Done,
		mergedLine()+goneLine()+onTrunkLine())
	gone.payload = sent(landedBody, true, result.EndingTrunk)
	listed := row("the delete fails and origin still lists the branch", toLand(nil, failed), result.Broke,
		mergedLine()+expect("Could not delete", topic, "still there", forgeFailure)+expect("still on origin")+mergeCommand().again)
	listed.set.arrange = func(f *gittest.Fixture) { f.Publish(topic) }
	// The question fails with git's own message, which the text holds on one line before the reason line.
	unasked := row("the delete fails and origin cannot be asked", toLand(nil, failed), result.Broke,
		mergedLine()+expect("Could not delete", topic, forgeFailure, readStillOnOrigin))
	unasked.set.landed = moveOrigin
	interrupted := row("an interrupt ends the delete", toLand(nil, byInterrupt().of("gh")), result.Broke,
		mergedLine()+expect("delete", topic, "interrupted", "may or may not have gone through", "interrupted",
			"deleting the branch")+mergeCommand().again)
	interrupted.set.runCtx = byInterrupt().endOnStart(deletingStep())
	limit := byTimeLimit()
	ranOut := row("the time limit ends the delete", toLand(nil, limit.of("gh")), result.Broke,
		mergedLine()+expect("delete", topic, limit.before, "may or may not have gone through", limit.before,
			"deleting the branch")+mergeCommand().again)
	ranOut.set.runCtx = limit.endOnStart(deletingStep())

	return slices.Concat([]landingRow{refused}, cutRows, []landingRow{gone, listed, unasked, interrupted, ranOut})
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
func (h stepHook) Started(label result.Phrase) {
	h.Progress.Started(label)

	if holds("start "+plainText(h.t, label), h.step) {
		h.fire()
	}
}

// endOnStart returns the context of a run that c ends once the run starts the step whose start
// reads step as the watch tells it.
func (c cut) endOnStart(step string) func(t *testing.T, s *scene) context.Context {
	return func(t *testing.T, s *scene) context.Context {
		t.Helper()

		done := make(chan struct{})
		s.deps.Progress = stepHook{Progress: s.deps.Progress, t: t, step: step, fire: sync.OnceFunc(func() { close(done) })}

		return c.once(t.Context(), done)
	}
}

// interruptedWrites are the runs of `ils merge` that an interrupt reaches as the first write
// starts, and which end as the time limit ends them when it leaves too little time for that write.
func interruptedWrites() []landingRow {
	return []landingRow{
		{
			name: "the merge", outcome: result.Pending, kept: true,
			set: setting{script: toLand(nil, nil), runCtx: byInterrupt().endOnStart(mergingStep())},
			text: countLine() + expect("interrupted before the merge", "interrupted before the merge") +
				nextPush(),
			payload: sent(landedBody, false, ""),
		},
	}
}

func TestMerge_WhenAnInterruptLandsAsTheFirstWriteStarts_EndsPendingWithNothingWritten(t *testing.T) {
	t.Parallel()

	runLandings(t, interruptedWrites())
}

// stallingGit returns a context that c ends once the scene's git call whose arguments start with
// words is under way, a call that waits until the end kills it.
func (c cut) stallingGit(words []string) func(t *testing.T, s *scene) context.Context {
	return func(t *testing.T, s *scene) context.Context {
		t.Helper()

		addr, connected := gittest.SilentListener(t)
		fakeGit(s, gittest.Fake{StallOn: words, StallAt: addr})

		return c.once(t.Context(), connected)
	}
}

func TestMerge_WhenAWriteOnGitHubFails_SaysWhatWentThrough(t *testing.T) {
	t.Parallel()

	runLandings(t, writeStops())
}

// landAndPublish lands the topic branch as pull request 7 and publishes it.
func landAndPublish(f *gittest.Fixture) {
	f.LandPull(merged, topic)
	f.Publish(topic)
}
