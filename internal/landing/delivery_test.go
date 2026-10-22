package landing_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
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
	pullURL = "https://github.com/Acme/Widgets/pull/12"
	lintURL = "https://github.com/Acme/Widgets/actions/runs/7/job/9"
	// repoArg is the repository as the --repo flag of gh takes it.
	repoArg = host + "/" + owner + "/" + repoName

	// tipMark stands, in what a push must print, for the sha of the branch's tip.
	tipMark = "<tip>"

	pushedLine  = "Pushed " + topic + " at " + tipMark + "\n"
	alreadyLine = topic + " is already on origin at " + tipMark + "; nothing pushed\n"
	openedLine  = "Opened pull request #12 against " + trunk + ", titled " + topic + " with no body: " + pullURL + "\n"
	foundLine   = "Pull request #12: " + pullURL + "\n"
	countLine   = "Checks for " + tipMark + ": 1 passed, 0 skipped\n"
	cleanLine   = "GitHub: CLEAN (clean)\n"

	mergeSteps = "write the pull request's title and body as the squash commit's message with " +
		"`gh pr edit --repo " + repoArg + " 12 --title … --body …`, then run ils merge\n"
	nextMerge  = "Next: " + mergeSteps
	nextReview = "Next: request a review of the pull request first, then " + mergeSteps
	nextRed    = "Next: fix the failing check, commit, and run ils push again\n"
	nextTrunk  = "Next: merge the trunk into the branch with `git merge origin/" + trunk +
		"`, resolve the conflicts, commit, and run ils push again\n"
	nextNever   = "Next: report that a required check never reported, and stop\n"
	nextNothing = "Next: report that no check or status reported for the pushed commit, and stop\n"

	readProtection = "the trunk's required checks"
	readCI         = "whether the repository has CI"

	noCILine = "No CI is configured on " + trunk + ", so nothing verified " + tipMark + "\n"
)

// forgeFor builds the forge's answers once the branch's tip is known.
type forgeFor func(tip string) forgetest.Script

// with returns build after edit has changed its script for the tip.
func (build forgeFor) with(edit func(script *forgetest.Script, tip string)) forgeFor {
	return func(tip string) forgetest.Script {
		script := build(tip)
		edit(&script, tip)

		return script
	}
}

// showing returns build with a forge that shows heads in turn, each on the tip unless it names
// a commit of its own.
func (build forgeFor) showing(heads ...forge.Head) forgeFor {
	return build.with(func(script *forgetest.Script, tip string) {
		placed := slices.Clone(heads)
		for i := range placed {
			if placed[i].SHA == "" {
				placed[i].SHA = tip
			}
		}

		script.Head = shows(placed...)
	})
}

// thenFailingHead returns build with a head read that fails with err after the heads it shows.
func (build forgeFor) thenFailingHead(err error) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.Head = append(script.Head, forgetest.Answer[forge.Head]{Err: err})
	})
}

// requiring returns build with a trunk whose protection requires the checks names.
func (build forgeFor) requiring(names ...string) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.TrunkProtection = []forgetest.Answer[forge.Protection]{{Value: forge.Protection{RequiredChecks: names}}}
	})
}

// answeringCI returns build with a forge that answers the read of whether the repository has CI
// on its trunk with answer.
func (build forgeFor) answeringCI(answer forgetest.Answer[bool]) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.HasTrunkCI = []forgetest.Answer[bool]{answer}
	})
}

// inState returns a head in the merge state state, on the tip once showing has placed it.
func inState(state forge.MergeState, raw string) forge.Head {
	return forge.Head{MergeState: state, RawMergeState: raw}
}

// standing is pull request 12, open from the topic branch against the trunk.
func standing() forge.Change {
	found := change(opened, trunk, forge.ChangeOpen)
	found.URL = pullURL

	return found
}

// listing returns a find-changes sequence that answers with each of lists in turn.
func listing(lists ...[]forge.Change) []forgetest.Answer[[]forge.Change] { return inTurn(lists...) }

// opening is a forge for a branch with no pull request: it opens number 12, requires no check on
// the trunk, shows the tip as a clean head, and answers the checks with each of verdicts in turn.
func opening(verdicts ...forge.Verdict) forgeFor {
	return func(tip string) forgetest.Script {
		script := finding()
		script.OpenChange = []forgetest.Answer[forge.ChangeRef]{{Value: forge.ChangeRef{URL: pullURL, Number: opened}}}
		script.TrunkProtection = []forgetest.Answer[forge.Protection]{{}}
		script.Head = shows(forge.Head{SHA: tip, MergeState: forge.MergeClean, RawMergeState: "CLEAN"})
		script.Verdict = says(verdicts...)

		return script
	}
}

// reusing is opening for a branch whose pull request 12 is already open against the trunk; it
// has no answer for an open-change, so that opening one fails the test.
func reusing(verdicts ...forge.Verdict) forgeFor {
	return opening(verdicts...).with(func(script *forgetest.Script, _ string) {
		script.FindChanges = listing([]forge.Change{standing()})
		script.OpenChange = nil
	})
}

// at returns end with the tip's sha in place of tipMark in its text and its reason, and as the
// sha of its payload.
func (end finish) at(tip string) finish {
	end.text = strings.ReplaceAll(end.text, tipMark, tip)
	end.reason = strings.ReplaceAll(end.reason, tipMark, tip)

	if end.payload != nil {
		filled := *end.payload
		filled.SHA = tip
		end.payload = &filled
	}

	return end
}

// detail returns the payload of a push to pull request 12, but for the sha: the verdict's name,
// the merge state in the words of ils, and the deciding checks.
func detail(verdict forge.VerdictState, state forge.MergeState, checks ...forge.Check) *result.Push {
	push := &result.Push{URL: pullURL, Number: opened, Verdict: string(verdict), MergeState: string(state)}
	for _, check := range checks {
		push.Checks = append(push.Checks, result.Check{
			Name: check.Name, Bucket: string(check.Bucket), Workflow: check.Workflow, Link: check.Link,
		})
	}

	return push
}

// greenAfter is a push whose one check passed on a clean pull request, printed after the lines before.
func greenAfter(before string) finish {
	return finish{
		outcome: result.Done,
		text:    before + countLine + cleanLine + nextMerge,
		payload: detail(forge.VerdictGreen, forge.MergeClean, passing().Checks...),
	}
}

// assertDelivered is assertEnded for a result of the push sequence: it also fails unless got
// carries the reason of end and its payload, or no payload when end names none.
func assertDelivered(t *testing.T, s *scene, got result.Result, end finish) {
	t.Helper()

	assertEnded(t, s, got, end)

	_, reason := shown(t, got, s.gitDir)
	assert.Check(t, is.Equal(reason, end.reason))

	var payload *result.Push
	if got.Payload != nil && got.Payload.Push != nil {
		payload = got.Payload.Push
		assertChecksArray(t, payload.Checks)
	}

	assert.Check(t, is.DeepEqual(payload, end.payload, cmpopts.EquateEmpty()))
}

// onOrigin returns the commit origin's copy of the topic branch is on, or nothing when origin
// has no such branch.
func (s *scene) onOrigin() string {
	// Each line of for-each-ref starts with the commit the ref is on.
	sha, _, _ := strings.Cut(s.fixture.OriginGit("for-each-ref", "refs/heads/"+topic), " ")

	return sha
}

// pushRow is one push whose preconditions hold: the repository, the forge, and how it must end.
type pushRow struct {
	// arrange changes the repository before the scene reads it.
	arrange func(*gittest.Fixture)
	// script returns the forge's answers once the branch's tip is known.
	script forgeFor
	name   string
	// want is how the push must end, with tipMark where the sha of the tip is printed.
	want finish
	// left is the time before the deadline; zero is the whole bound.
	left time.Duration
	// quiet is whether origin already has the tip, so that no `git push` runs.
	quiet bool
}

// delivery is a scene whose branch Push can push: the tip and the deadline the push runs under.
type delivery struct {
	*scene

	ctx context.Context
	tip string
}

// newDelivery returns the scene of tt on the topic branch, with a deadline the time tt has left
// away from now: inside a bubble that is fake time, which passes only while the wait polls.
func newDelivery(t *testing.T, tt pushRow) *delivery {
	t.Helper()

	s := newScene(t, tt.arrange, forgetest.Script{}, false)
	sha := s.fixture.Git("rev-parse", topic)

	s.useForge(t, tt.script(sha))

	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(orBound(tt.left)))
	t.Cleanup(cancel)

	return &delivery{scene: s, ctx: ctx, tip: sha}
}

// runPushes runs each row as a test in a bubble of its own: the push ends as the row wants,
// origin's copy of the branch is at the tip, and git pushed unless the row is quiet.
func runPushes(t *testing.T, rows []pushRow) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				d := newDelivery(t, tt)

				got := landing.Push(d.ctx, d.deps, pushRequest())

				assertDelivered(t, d.scene, got, tt.want.at(d.tip))
				assert.Check(t, is.Equal(d.onOrigin(), d.tip))
				assert.Check(t, is.Equal(d.gitRan("push"), !tt.quiet))
			})
		})
	}
}

// pushWriteRows are the pushes of a tip origin does not have yet, each ending green.
func pushWriteRows() []pushRow {
	opens := greenAfter(pushedLine + openedLine)
	skipping := forge.Verdict{
		Checks: []forge.Check{
			checked("lint", forge.BucketPass), checked("test", forge.BucketPass), checked("docs", forge.BucketSkipped),
		},
		Runs: ran(forge.RunCompleted),
	}
	skips := finish{
		outcome: result.Done,
		text:    pushedLine + openedLine + "Checks for " + tipMark + ": 2 passed, 1 skipped\n" + cleanLine + nextMerge,
		payload: detail(forge.VerdictGreen, forge.MergeClean, skipping.Checks...),
	}

	return []pushRow{
		{name: "two checks passed and one was skipped", script: opening(skipping), want: skips},
		{name: "first push of the branch", script: opening(passing()), want: opens},
		{
			name:   "open pull request against the trunk",
			script: reusing(passing()), want: greenAfter(pushedLine + foundLine),
		},
	}
}

func TestPush_WhenEveryPreconditionHolds_ReportsTheVerdictOfThePushedBranch(t *testing.T) {
	t.Parallel()

	runPushes(t, pushWriteRows())
}

func TestPush_WhenTrunkRequiresChecks_PassesThemToEveryForgeRead(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		required := []string{"lint", "test"}
		d := newDelivery(t, pushRow{script: opening(requiredPassing()).requiring(required...)})

		landing.Push(d.ctx, d.deps, pushRequest())

		assertReadsRequire(t, d.forge.Calls(), d.tip, required)
	})
}

// pushExistsRows are the pushes whose open-change reports a pull request that already exists.
func pushExistsRows() []pushRow {
	exists := forgetest.Answer[forge.ChangeRef]{Err: fmt.Errorf("gh pr create: %w", forge.ErrChangeExists)}
	// reporting is a forge whose find-changes answers with each of lists in turn.
	reporting := func(lists ...[]forge.Change) forgeFor {
		return opening(passing()).with(func(script *forgetest.Script, _ string) {
			script.OpenChange = []forgetest.Answer[forge.ChangeRef]{exists}
			script.FindChanges = listing(lists...)
		})
	}

	return []pushRow{
		{
			name:   "and the branch's pull requests now hold one against the trunk",
			script: reporting(nil, []forge.Change{standing()}), want: greenAfter(pushedLine + foundLine),
		},
		{
			name: "and the branch's pull requests hold none against the trunk", script: reporting(nil),
			want: finish{
				outcome: result.Broke, reason: "no pull request was opened or found",
				text: pushedLine + "No pull request was opened or found: GitHub reports one for " + topic +
					" and lists none open against " + trunk + "\n" + nextFixed,
			},
		},
	}
}

func TestPush_WhenOpenChangeReportsAnExistingPullRequest_ReadsThePullRequestsOfTheBranchAgain(t *testing.T) {
	t.Parallel()

	runPushes(t, pushExistsRows())
}

func TestPush_WhenGitRefusesThePush_BrokeSayingNothingWasPushed(t *testing.T) {
	t.Parallel()
	// Origin hides the branch from pushes alone, so every read passes and the push is refused.
	hide := func(f *gittest.Fixture) { f.OriginGit("config", "receive.hideRefs", "refs/heads/"+topic) }
	d := newDelivery(t, pushRow{arrange: hide, script: opening()})

	got := landing.Push(d.ctx, d.deps, pushRequest())

	assertStopped(t, got, result.Broke)
	assert.Check(t, got.Payload == nil || got.Payload.Push == nil, "Payload = %+v, want none for a push git refused", got.Payload)

	text, reason := shown(t, got, d.gitDir)
	assert.Check(t, is.Equal(reason, "git refused the push"))

	prefix := "Git refused the push of " + topic + ", so nothing was pushed: "
	suffix := "\n" + nextFixed
	said, starts := strings.CutPrefix(text, prefix)
	said, ends := strings.CutSuffix(said, suffix)
	assert.Check(t, starts && ends, "RenderText() = %q, want it to start with %q and end with %q", text, prefix, suffix)
	// The words are git's own, printed on the one line.
	assert.Check(t, said != "", "RenderText() = %q, want git's words between %q and %q", text, prefix, suffix)
	assert.Check(t, !strings.ContainsAny(said, "\r\n"), "git's words = %q, want them on one line", said)

	assert.Check(t, is.Equal(d.onOrigin(), ""))

	assertAsked(t, d.scene, changesRead())
}

// pushOpenFailureRows are the pushes whose open-change fails once the branch is on origin.
func pushOpenFailureRows() []pushRow {
	failing := func(err error) forgeFor {
		return opening().with(func(script *forgetest.Script, _ string) {
			script.OpenChange = []forgetest.Answer[forge.ChangeRef]{{Err: err}}
		})
	}

	return []pushRow{
		{
			name: "for a reason of the forge", script: failing(errors.New("HTTP 502")),
			want: finish{
				outcome: result.Broke, reason: "could not open the pull request",
				text: pushedLine + "No pull request was opened: HTTP 502\n" + nextFixed,
			},
		},
		{
			name:   "because the deadline ended in it",
			script: failing(fmt.Errorf("gh pr create: %w", context.DeadlineExceeded)),
			want: finish{
				outcome: result.Broke, reason: "the time limit ended while opening the pull request",
				text: pushedLine + "Started opening the pull request when the time limit ended; " +
					"it may or may not have been opened\n" + nextPush,
			},
		},
	}
}

func TestPush_WhenOpeningThePullRequestFails_BrokeSayingThePushHappened(t *testing.T) {
	t.Parallel()

	runPushes(t, pushOpenFailureRows())
}

func TestPush_WhenDeadlineEndsDuringTheGitPush_BrokeSayingThePushMayHaveTaken(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, finding(), false)
	// Origin answers about its trunk, the fetch of it, and whether it has the branch; the push is next.
	ctx := stalling(3)(t, s)
	sha := s.fixture.Git("rev-parse", topic)

	got := landing.Push(ctx, s.deps, pushRequest())

	assertDelivered(t, s, got, finish{
		outcome: result.Broke, reason: "the time limit ended during the push",
		text: "Started the push of " + topic + " at " + sha + " when the time limit ended; " +
			"it may or may not have taken\n" + nextPush,
	})
	assertAsked(t, s, changesRead())
}

// revoked is a context that ends when its inner one does, as canceled and not at its deadline.
type revoked struct {
	context.Context
}

// Err returns context.Canceled once the inner context ended.
func (r revoked) Err() error {
	if r.Context.Err() == nil {
		return nil
	}

	return context.Canceled
}

func TestPush_WhenTheGitPushFailsOtherThanByRefusalOrDeadline_BrokeSayingNothingWasPushed(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, finding(), false)
	// Origin answers about its trunk, the fetch of it, and whether it has the branch; the push is next.
	ctx := revoked{Context: stalling(3)(t, s)}

	got := landing.Push(ctx, s.deps, pushRequest())

	assertDelivered(t, s, got, finish{
		outcome: result.Broke, reason: "could not push the branch",
		text: "Could not push " + topic + ", so nothing was pushed: git push -u origin refs/heads/" + topic +
			":refs/heads/" + topic + ": context canceled\n" + nextFixed,
	})
	assertAsked(t, s, changesRead())
}

// pushAgainRows are the pushes of a tip origin already has.
func pushAgainRows() []pushRow {
	again := func(f *gittest.Fixture) { f.Publish(topic) }

	return []pushRow{
		{
			name: "with its pull request open", arrange: again, quiet: true,
			script: reusing(passing()), want: greenAfter(alreadyLine + foundLine),
		},
		{
			name: "with no pull request yet", arrange: again, quiet: true,
			script: opening(passing()), want: greenAfter(alreadyLine + openedLine),
		},
	}
}

func TestPush_WhenTipIsAlreadyOnOrigin_PushesNothing(t *testing.T) {
	t.Parallel()

	runPushes(t, pushAgainRows())
}

// pushUnreportedRows are the pushes whose checks did not all report.
func pushUnreportedRows() []pushRow {
	lint := needed("lint", forge.BucketPass)
	never := forge.Verdict{Checks: []forge.Check{lint}, MissingRequired: []string{"test"}, Runs: ran(forge.RunCompleted)}
	missing := inState(forge.MergeBlockedByMissingCheck, "BLOCKED")
	nothing := finish{
		outcome: result.DeliveredNotGreen,
		reason:  "no check or status reported for " + tipMark + " on pull request #12",
		text: pushedLine + openedLine + "No check or status reported for " + tipMark +
			" on pull request #12\n" + cleanLine + nextNothing,
		payload: detail(forge.VerdictNone, forge.MergeClean),
	}

	return []pushRow{
		{
			name:   "a required check never reported",
			script: opening(never).requiring("lint", "test").showing(missing),
			want: finish{
				outcome: result.DeliveredNotGreen, reason: "a required check never reported",
				text: pushedLine + openedLine + "Required and never reported: test\n" +
					"GitHub: BLOCKED (blocked-by-missing-check)\n" + nextNever,
				payload: detail(forge.VerdictGreen, forge.MergeBlockedByMissingCheck, lint),
			},
		},
		{
			name:   "nothing reported in a repository with CI on its trunk",
			script: opening(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{Value: true}),
			want:   nothing,
		},
		{
			// A trunk that requires checks has CI, so the forge has no answer for the CI read.
			name:   "nothing reported though the trunk requires checks",
			script: opening(forge.Verdict{MissingRequired: []string{"lint", "test"}}).requiring("lint", "test"),
			want:   nothing,
		},
	}
}

// pushNoCIRows are the pushes whose commit nothing reported for, in a repository with no CI on its
// trunk, and those whose read of that fails or is cut short.
func pushNoCIRows() []pushRow {
	silent := opening(forge.Verdict{})
	unverified := silent.answeringCI(forgetest.Answer[bool]{})
	cut := reusing(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{Err: fmt.Errorf("gh: %w", context.DeadlineExceeded)})
	reviewed := inState(forge.MergeBlockedByReview, "BLOCKED")
	// unreadable is the push whose read of whether the repository has CI fails with err, which the
	// push prints as said.
	unreadable := func(name string, err error, said string) pushRow {
		return pushRow{
			name:   name,
			script: silent.answeringCI(forgetest.Answer[bool]{Err: err}),
			want: finish{
				outcome: result.Broke, reason: "could not read " + readCI,
				text:    pushedLine + openedLine + "Could not read " + readCI + ": " + said + "\n" + nextFixed,
				payload: detail(forge.VerdictNone, forge.MergeClean),
			},
		}
	}

	return []pushRow{
		// A merge state after the checks that keeps the push from the trunk ends as a green push in
		// that state does.
		{
			name:   "on a pull request that turns " + string(forge.MergeConflicting) + " after the checks",
			script: unverified.showing(inState(forge.MergeClean, "CLEAN"), inState(forge.MergeConflicting, "DIRTY")),
			want: finish{
				outcome: result.DeliveredNotGreen, reason: "the pull request conflicts with the trunk",
				text:    pushedLine + openedLine + noCILine + "GitHub: DIRTY (" + string(forge.MergeConflicting) + ")\n" + nextTrunk,
				payload: detail(forge.VerdictNone, forge.MergeConflicting),
			},
		},
		{
			name: "on a clean pull request", script: unverified,
			want: finish{
				outcome: result.Done, text: pushedLine + openedLine + noCILine + cleanLine + nextMerge,
				payload: detail(forge.VerdictNone, forge.MergeClean),
			},
		},
		{
			name:   "on a pull request that needs a review",
			script: unverified.showing(inState(forge.MergeClean, "CLEAN"), reviewed),
			want: finish{
				outcome: result.Done,
				text:    pushedLine + openedLine + noCILine + "GitHub: BLOCKED (blocked-by-review)\n" + nextReview,
				payload: detail(forge.VerdictNone, forge.MergeBlockedByReview),
			},
		},
		unreadable("and the CI read fails", errors.New("HTTP 502"), "HTTP 502"),
		// Only the bound's end is pending: a read canceled for any other reason failed.
		unreadable("and the CI read is canceled", fmt.Errorf("gh: %w", context.Canceled), "gh: context canceled"),
		{
			name: "and the deadline ends in the CI read", script: cut,
			want: cutShortRead(readCI),
		},
	}
}

func TestPush_WhenNothingReportedForTheCommit_EndsByWhatTheCIReadAnswers(t *testing.T) {
	t.Parallel()

	runPushes(t, pushNoCIRows())
}

func TestPush_WhenTheDeadlineEndsBeforeTheCIRead_PendingWithoutAskingIt(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ended := make(chan struct{})
		// The deadline ends once the merge state after the checks is read; the forge has no
		// answer for the CI read.
		ending := reusing(forge.Verdict{}).with(func(script *forgetest.Script, _ string) {
			checksRead := false
			once := sync.OnceFunc(func() { close(ended) })
			script.OnRead = func(call forgetest.Call) {
				if call.Op == forgetest.OpVerdict {
					checksRead = true
				}

				if call.Op == forgetest.OpHead && checksRead {
					once()
				}
			}
		})
		d := newDelivery(t, pushRow{script: ending})
		ctx := gittest.Ending(d.ctx, ended, context.DeadlineExceeded)

		got := landing.Push(ctx, d.deps, pushRequest())

		assertDelivered(t, d.scene, got, cutShortRead(readCI).at(d.tip))
		assert.Check(t, is.Equal(d.onOrigin(), d.tip))
	})
}

// ciReading is a forge that keeps what its CI read's context was: its deadline, and whether it
// ends with the push's. The wrapped forge records the repository and trunk the read was about.
type ciReading struct {
	landing.Forge

	deadline time.Time
	// endPush ends the push's context while the read is in flight.
	endPush       context.CancelFunc
	endedWithPush bool
}

// HasTrunkCI keeps what its context was, then answers as the forge it wraps.
func (f *ciReading) HasTrunkCI(ctx context.Context, repo forge.Repo, trunk string) (bool, error) {
	f.deadline, _ = ctx.Deadline()
	f.endPush()
	f.endedWithPush = ctx.Err() != nil

	return f.Forge.HasTrunkCI(ctx, repo, trunk)
}

func TestPush_WhenNothingReportedForTheCommit_AsksAboutTheTrunkOfOriginWithinTheDeadline(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		d := newDelivery(t, pushRow{script: opening(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{})})
		ctx, cancel := context.WithCancel(d.ctx)
		t.Cleanup(cancel)
		reading := &ciReading{Forge: d.deps.Forge, endPush: cancel}
		d.deps.Forge = reading
		deadline, _ := d.ctx.Deadline()

		landing.Push(ctx, d.deps, pushRequest())

		wantAsk := forgetest.Call{Op: forgetest.OpHasTrunkCI, Repo: origin(), Branch: trunk}
		assert.Check(t, is.DeepEqual(callsOf(d.forge.Calls(), forgetest.OpHasTrunkCI), []forgetest.Call{wantAsk}, whichRepo()))
		assert.Check(t, reading.deadline.Equal(deadline), "CI read deadline = %v, want the push's %v", reading.deadline, deadline)
		assert.Check(t, reading.endedWithPush, "CI read context outlived the push's context")
	})
}

// callsOf returns the calls of calls to the operation op.
func callsOf(calls []forgetest.Call, op forgetest.Op) []forgetest.Call {
	return slices.DeleteFunc(calls, func(call forgetest.Call) bool { return call.Op != op })
}

// pushWaitRows are the pushes whose wait ends without an error, one per result it maps to.
func pushWaitRows() []pushRow {
	reviewed := greenAfter(pushedLine + foundLine)
	reviewed.text = pushedLine + foundLine + countLine + "GitHub: BLOCKED (blocked-by-review)\n" + nextReview
	reviewed.payload = detail(forge.VerdictGreen, forge.MergeBlockedByReview, passing().Checks...)

	rows := []pushRow{
		{
			name:   "green on a pull request that needs a review",
			script: reusing(passing()).showing(inState(forge.MergeClean, "CLEAN"), inState(forge.MergeBlockedByReview, "BLOCKED")),
			want:   reviewed,
		},
	}

	return slices.Concat(rows, pushUnreportedRows())
}

func TestPush_WhenTheWaitEnds_ReportsItsOutcome(t *testing.T) {
	t.Parallel()

	runPushes(t, pushWaitRows())
}

func TestPush_WhenTheHeadMovesPastThePushedCommit_RefusesNamingTheNewHead(t *testing.T) {
	t.Parallel()

	refused := finish{
		outcome: result.Refused, reason: "the head of pull request #12 moved to " + movedSHA,
		text: "Refused: the head of pull request #12 moved to " + movedSHA + "\n" + pushedLine + foundLine +
			"Next: pull the new commit, then run ils push again\n",
		payload: detail(forge.VerdictNone, forge.MergeClean),
	}
	movedAfter := func(verdict forge.Verdict) forgeFor {
		return reusing(verdict).showing(inState(forge.MergeClean, "CLEAN"), moved())
	}

	runPushes(t, []pushRow{
		{name: "after a green verdict", script: movedAfter(passing()), want: refused},
		{name: "after a red verdict", script: movedAfter(lintRed()), want: refused},
		{name: "after nothing reported", script: movedAfter(forge.Verdict{}), want: refused},
		{
			// Seven seconds leave room for one poll and then for the head read.
			name: "after checks still running at the deadline", script: movedAfter(running()), want: refused,
			left: 7 * time.Second,
		},
	})
}

// failingRead is a read of the push sequence that follows its writes: the forge that fails it, its
// name, the lines printed before it, and whether the pull request was known by then, and so is in
// the payload.
type failingRead struct {
	script forgeFor
	name   string
	before string
	known  bool
}

// row returns the push whose read ends as want says.
func (read failingRead) row(want finish) pushRow {
	if read.known {
		want.payload = detail(forge.VerdictNone, forge.MergeUnknown)
	}

	return pushRow{name: read.name, script: read.script, want: want}
}

// breaking is a first push to a clean pull request whose one check passes, once edit has changed
// the forge's answers.
func breaking(edit func(*forgetest.Script)) forgeFor {
	return opening(passing()).with(func(script *forgetest.Script, _ string) { edit(script) })
}

// readsAfterWrites returns the reads of a push that follow its writes and come before its wait,
// in the order the push makes them, each failing with err.
func readsAfterWrites(err error) []failingRead {
	return []failingRead{
		{
			name: readChanges, before: pushedLine,
			script: breaking(func(script *forgetest.Script) {
				script.OpenChange = []forgetest.Answer[forge.ChangeRef]{{Err: forge.ErrChangeExists}}
				script.FindChanges = append(listing(nil), forgetest.Answer[[]forge.Change]{Err: err})
			}),
		},
		{
			name: readProtection, before: pushedLine + openedLine, known: true,
			script: breaking(func(script *forgetest.Script) {
				script.TrunkProtection = []forgetest.Answer[forge.Protection]{{Err: err}}
			}),
		},
	}
}

// brokenAfter returns the pushes that end broke on each of reads, failing as HTTP 502.
func brokenAfter(reads []failingRead) []pushRow {
	rows := make([]pushRow, 0, len(reads))

	for _, read := range reads {
		rows = append(rows, read.row(finish{
			outcome: result.Broke, reason: "could not read " + read.name,
			text: read.before + "Could not read " + read.name + ": HTTP 502\n" + nextFixed,
		}))
	}

	return rows
}

func TestPush_WhenDeadlineEndsInAReadAfterTheWrites_PendingNamingTheRead(t *testing.T) {
	t.Parallel()

	reads := readsAfterWrites(fmt.Errorf("gh: %w", context.DeadlineExceeded))
	rows := make([]pushRow, 0, len(reads))

	for _, read := range reads {
		rows = append(rows, read.row(finish{
			outcome: result.Pending, reason: "the time limit ended while reading " + read.name,
			text: read.before + "The time limit ended while reading " + read.name + "; nothing more will change\n" + nextPush,
		}))
	}

	runPushes(t, rows)
}

func TestPush_WhenAReadFailsAfterTheWrites_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()

	runPushes(t, brokenAfter(readsAfterWrites(errors.New("HTTP 502"))))
}
