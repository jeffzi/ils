package landing_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
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
	conventional = "docs: tidy the readme"

	nextMergeAgain = "Next: ils merge\n"
	nextMergeFixed = "Next: fix what failed, then run ils merge again\n"

	readRules      = "the trunk's rules"
	readLocalTrunk = "the local trunk"
	readPullHead   = "the pull request's head"
	readWorktrees  = "where the trunk is checked out"

	readyCommand = "gh pr ready --repo " + repoArg + " 12"
	nextReady    = "Next: mark it ready with `" + readyCommand + "`, then run ils merge again\n"
	draftText    = "Refused: pull request #12 is a draft\n" + nextReady
	settingsText = "Refused: the settings of " + owner + "/" + repoName +
		" forbid squash merges: Allow squash merging is off\n" +
		"Next: allow squash merges in the repository's settings, then run ils merge again\n"
	nextRuleset = "Next: allow squash merges in the trunk's ruleset, then run ils merge again\n"
	rulesetText = "Refused: the ruleset 42 on the trunk " + trunk + " forbids squash merges\n" + nextRuleset
	noPullText  = "Refused: the branch " + topic + " has no open pull request against the trunk " + trunk + "\n" +
		nextPush
	behindHeadText = "Refused: the tip of " + topic +
		" is behind the head of pull request #12: the pull request has commits the tip lacks\n" + nextPush
	pastHeadText = "Refused: the tip of " + topic +
		" is past the head of pull request #12: the tip has commits the pull request lacks, so they were never pushed\n" +
		nextPush
	divergedHeadText = "Refused: the tip of " + topic +
		" and the head of pull request #12 have diverged: each has commits the other lacks\n" + nextPush
)

// titleText is the whole text of the refusal of pull request 12 titled title.
func titleText(title string) string {
	return "Refused: the title of pull request #12, \"" + title +
		"\", is not a conventional commit subject of the form type(scope)!: summary\n" +
		"Next: " + editSteps + " again\n"
}

// trunkAheadText is the whole text of the refusal of a local trunk whose one commit origin's trunk
// lacks is sha.
func trunkAheadText(sha string) string {
	return sha + " local change on " + trunk + "\n" +
		"Refused: the local trunk " + trunk + " has commits that origin's trunk lacks, listed here\n" +
		"Next: move those commits off the local trunk by hand, then run ils merge again\n"
}

// nothingPastMerge is the refusal by `ils merge` of a branch with no commit past origin's trunk.
func nothingPastMerge(branch string) string {
	return "Refused: the branch " + branch + " has no commit past origin's trunk " + trunk + "\n" +
		"Next: commit the work, then run ils push\n"
}

// squashing is the identity of Acme/Widgets, whose user can push, with settings that allow squash
// merges or not.
func squashing(allowed bool) forge.Repo {
	repo := widgets(false, true)
	repo.SquashAllowed = allowed

	return repo
}

// pullAt is pull request 12, open from the topic branch against the trunk at head, with a
// conventional title.
func pullAt(head string) forge.Change {
	pull := standing()
	pull.HeadSHA = head
	pull.Title = conventional

	return pull
}

// merging is a forge on which every read `ils merge` makes before the wait lets the merge go on:
// a repository and a trunk that allow squash merges, and pull request 12 at the head it is given.
func merging() forgeFor {
	return func(head string) forgetest.Script {
		return forgetest.Script{
			Identity:        []forgetest.Answer[forge.Repo]{{Value: squashing(true)}},
			FindChanges:     listing([]forge.Change{pullAt(head)}),
			TrunkProtection: []forgetest.Answer[forge.Protection]{{Value: forge.Protection{SquashAllowed: true}}},
		}
	}
}

// pulling returns build with pull request 12, the one pull request build lists, changed by edit,
// so that edits made one after the other all hold.
func (build forgeFor) pulling(edit func(*forge.Change)) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		edit(&script.FindChanges[0].Value[0])
	})
}

// titled returns build with pull request 12 titled title.
func (build forgeFor) titled(title string) forgeFor {
	return build.pulling(func(pull *forge.Change) { pull.Title = title })
}

// drafted returns build with pull request 12 a draft.
func (build forgeFor) drafted() forgeFor {
	return build.pulling(func(pull *forge.Change) { pull.Draft = true })
}

// findingOnly returns build with a forge that finds changes for the branch and nothing else.
func (build forgeFor) findingOnly(changes ...forge.Change) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.FindChanges = listing(changes)
	})
}

// forbiddenInSettings returns build with a repository whose settings forbid squash merges.
func (build forgeFor) forbiddenInSettings() forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.Identity = []forgetest.Answer[forge.Repo]{{Value: squashing(false)}}
	})
}

// forbiddenByRuleset returns build with a trunk whose rulesets with the ids ids forbid squash
// merges.
func (build forgeFor) forbiddenByRuleset(ids ...int) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.TrunkProtection = []forgetest.Answer[forge.Protection]{{Value: forge.Protection{SquashForbiddenBy: ids}}}
	})
}

// failingRules returns build with a read of the trunk's rules that fails with err.
func (build forgeFor) failingRules(err error) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.TrunkProtection = []forgetest.Answer[forge.Protection]{{Err: err}}
	})
}

// mergeEnd is how a run of `ils merge` must end, as the arrange of a row works it out.
type mergeEnd struct {
	text string
	// failedRead, when set, is the read the git call failing makes fail; the run must then end
	// broke on it, carrying what the failed call said, in place of printing text.
	failedRead string
	// head is the head of pull request 12, which the payload carries; empty is the tip of the branch.
	head string
	// from is the forge read from which on the git call fails; empty is the whole run.
	from forgetest.Op
	// failing is the arguments of a git call made to fail as gittest.FakeGit makes it; empty fails none.
	failing []string
}

// ending is a mergeEnd that prints text, for pull request 12 at the tip.
func ending(text string) mergeEnd { return mergeEnd{text: text} }

// mergeRow is one run of `ils merge` that stops before the wait.
type mergeRow struct {
	// arrange changes the repository and returns how the run must end.
	arrange func(*gittest.Fixture) mergeEnd
	// runCtx returns the context to run under once the scene is built; nil is the test's own.
	runCtx func(t *testing.T, s *scene) context.Context
	// script returns the forge's answers once the head of pull request 12 is known.
	script forgeFor
	name   string
	ops    []forgetest.Op
	// drawn are parts of the text and how a terminal with color draws them; empty leaves them
	// unchecked.
	drawn   []drawnPart
	outcome result.Outcome
	// answered is how many calls origin answers before it stops; zero is every call.
	answered int
	// fetches is whether git fetches from origin before the run stops.
	fetches bool
	// offline gives the scene an origin that does not answer.
	offline bool
	// known is whether the pull request against the trunk is known when the run stops, so that
	// its result carries the merge payload.
	known bool
}

// mergeAsked returns the calls that ops stand for: each about the repository origin's URLs name,
// the find-changes read about the topic branch, and the read of the trunk's rules about the trunk.
func mergeAsked(ops []forgetest.Op) []forgetest.Call {
	calls := asked(ops)
	for i := range calls {
		if calls[i].Op == forgetest.OpTrunkProtection {
			calls[i].Branch = trunk
		}
	}

	return calls
}

// notMerged is the payload of a run that knew pull request 12 at head and stopped before it read
// the checks or merged anything.
func notMerged(head string) *result.Merge {
	return &result.Merge{
		URL: pullURL, Number: opened, SHA: head,
		Verdict: string(forge.VerdictNone), MergeState: string(forge.MergeUnknown),
	}
}

// runMerge runs tt on a scene of its own: `ils merge` ends as the row's arrange says, holds every
// step line it said, carries the merge payload only once the pull request is known, writes
// nothing, and reads no further than the row says. A row that names how its result is drawn is
// held to it too.
func runMerge(t *testing.T, tt mergeRow) {
	t.Helper()

	end, run := mergeRowOn(t, tt)

	assertStoppedBy(t, run.got, tt.outcome, mergeRequest())

	if end.failedRead != "" {
		assertFailedGit(t, run.got, mergeCommand(), end.failedRead)
	} else {
		text, _ := shown(t, run.got, run.gitDir)
		assert.Check(t, is.Equal(text, end.text))
	}

	var want *result.Merge
	if tt.known {
		want = notMerged(end.head)
	}

	assert.Check(t, is.DeepEqual(mergePayload(t, run.got), want, cmpopts.EquateEmpty()))

	assert.Check(t, is.Equal(run.written(), run.before))
	assert.Check(t, is.DeepEqual(run.forge.Calls(), mergeAsked(tt.ops), whichRepo()))
	assert.Check(t, is.Equal(run.gitRan("fetch"), tt.fetches))

	if len(tt.drawn) > 0 {
		assertDrawn(t, run.got, run.gitDir, tt.drawn)
	}
}

// mergeRowOn runs `ils merge` on a scene of tt's own, as runMergeOn does, and returns how the run
// must end and the run.
func mergeRowOn(t *testing.T, tt mergeRow) (mergeEnd, mergeRun) {
	t.Helper()

	var end mergeEnd

	run := runMergeOn(t, setting{
		arrange: func(f *gittest.Fixture) { end = tt.arrange(f) },
		tip: func(f *gittest.Fixture) string {
			if end.head == "" {
				end.head = f.Git("rev-parse", "HEAD")
			}

			return end.head
		},
		script: tt.script,
		scripted: func(t *testing.T, s *scene, script *forgetest.Script) {
			t.Helper()

			switch {
			case end.from != "":
				script.OnRead = failingFrom(t, s, end.failing, end.from)
			case len(end.failing) > 0:
				failGit(t, s, end.failing)
			}
		},
		rewire: func(t *testing.T, s *scene) {
			t.Helper()

			if tt.answered > 0 {
				s.stopAnswering(t, tt.answered)
			}
		},
		runCtx:    tt.runCtx,
		offline:   tt.offline,
		unbounded: true,
	})

	return end, run
}

// runMerges runs each row as a parallel test of its own.
func runMerges(t *testing.T, rows []mergeRow) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runMerge(t, tt)
		})
	}
}

// rulesRead is the forge operations `ils merge` makes up to the read of the trunk's rules.
func rulesRead() []forgetest.Op {
	return []forgetest.Op{forgetest.OpIdentity, forgetest.OpFindChanges, forgetest.OpTrunkProtection}
}

// standingStops are the refusals of `ils merge` decided from where HEAD stands and from the
// working tree, before anything is fetched.
func standingStops() []mergeRow {
	row := func(name string, arrange func(*gittest.Fixture) string) mergeRow {
		return mergeRow{
			name: name, script: merging(), ops: identityRead(), outcome: result.Refused,
			arrange: func(f *gittest.Fixture) mergeEnd { return ending(arrange(f)) },
		}
	}

	return []mergeRow{
		row("on the trunk with a dirty tree", func(f *gittest.Fixture) string {
			f.Git("switch", "--quiet", trunk)
			f.DirtyFile()

			return onTrunk
		}),
		row("tracked files changed and a local trunk ahead of origin's", func(f *gittest.Fixture) string {
			f.AdvanceTrunk()

			return dirtyText(mergeCommand(), f.DirtyFile(), f.StagedFile())
		}),
	}
}

// trunkStops are the refusals of `ils merge` decided from the local trunk and from the branch's
// commits past origin's trunk, once the trunk is fetched.
func trunkStops() []mergeRow {
	row := func(name string, arrange func(*gittest.Fixture) string) mergeRow {
		return mergeRow{
			name: name, script: merging(), ops: identityRead(), outcome: result.Refused, fetches: true,
			arrange: func(f *gittest.Fixture) mergeEnd { return ending(arrange(f)) },
		}
	}

	return []mergeRow{
		row("local trunk ahead of origin's", func(f *gittest.Fixture) string {
			return trunkAheadText(f.AdvanceTrunk())
		}),
		row("local trunk diverged from origin's", func(f *gittest.Fixture) string {
			ahead := f.AdvanceTrunk()
			f.AdvanceOrigin(trunk)

			return trunkAheadText(ahead)
		}),
		row("local trunk ahead of origin's on a branch with no commit past the trunk", func(f *gittest.Fixture) string {
			fetched := f.Git("rev-parse", trunk)
			ahead := f.AdvanceTrunk()
			f.Git("switch", "--quiet", "--create", spare, fetched)

			return trunkAheadText(ahead)
		}),
		row("no commit past the trunk", func(f *gittest.Fixture) string {
			f.Git("switch", "--quiet", "--create", spare, trunk)

			return nothingPastMerge(spare)
		}),
	}
}

// pullStops are the refusals of `ils merge` decided from the pull requests the forge finds for
// the branch, before one against the trunk is known.
func pullStops() []mergeRow {
	row := func(name string, script forgeFor, arrange func(*gittest.Fixture)) mergeRow {
		return mergeRow{
			name: name, script: script, outcome: result.Refused, fetches: true,
			ops: changesRead(),
			arrange: func(f *gittest.Fixture) mergeEnd {
				if arrange != nil {
					arrange(f)
				}

				return ending(noPullText)
			},
		}
	}
	elsewhere := row("open pull request against another base",
		merging().findingOnly(change(opened, otherBase, forge.ChangeOpen)), nil)
	elsewhere.arrange = func(*gittest.Fixture) mergeEnd { return ending(elsewhereText(mergeCommand())) }
	landed := row("open pull request against the trunk beside a merged one that carries every commit",
		merging().with(func(script *forgetest.Script, tip string) {
			script.FindChanges = listing([]forge.Change{pullAt(tip), change(merged, trunk, forge.ChangeMerged)})
		}), nil)
	landed.arrange = func(f *gittest.Fixture) mergeEnd {
		f.LandPull(merged, topic)

		return ending("Refused: the branch " + topic + " already landed with pull request #" + strconv.Itoa(merged) +
			"\n" + nextBranch + "\n")
	}

	return []mergeRow{
		elsewhere,
		landed,
		row("no pull request at all", merging().findingOnly(), nil),
		row("closed pull request against the trunk", merging().findingOnly(change(opened, trunk, forge.ChangeClosed)), nil),
		row("merged pull request sharing no commit past the trunk",
			merging().findingOnly(change(merged, trunk, forge.ChangeMerged)),
			func(f *gittest.Fixture) {
				f.Commits(spare, "earlier work")
				f.LandPull(merged, spare)
				f.Git("switch", "--quiet", topic)
			}),
	}
}

// headStops are the refusals of `ils merge` of a tip that is not the head of pull request 12.
func headStops() []mergeRow {
	row := func(name, text string, arrange func(*gittest.Fixture) string) mergeRow {
		return mergeRow{
			name: name, script: merging(), ops: changesRead(), outcome: result.Refused, fetches: true, known: true,
			arrange: func(f *gittest.Fixture) mergeEnd { return mergeEnd{text: text, head: arrange(f)} },
		}
	}

	return []mergeRow{
		row("tip behind the head", behindHeadText, headPastTip),
		row("tip past the head", pastHeadText, tipPastHead),
		row("tip diverged from the head", divergedHeadText, func(f *gittest.Fixture) string {
			head := headPastTip(f)
			f.Commits(topic, "add an example")

			return head
		}),
	}
}

// headPastTip gives pull request 12 a head one commit past the tip and returns it.
func headPastTip(f *gittest.Fixture) string {
	head := f.Commits(topic, "fix a typo")[0]
	f.PullRef(opened, head)
	f.Git("reset", "--quiet", "--hard", "HEAD~1")

	return head
}

// tipPastHead gives pull request 12 the tip as its head, then adds a commit to the branch, and
// returns the head.
func tipPastHead(f *gittest.Fixture) string {
	head := f.Git("rev-parse", topic)
	f.PullRef(opened, head)
	f.Commits(topic, "fix a typo")

	return head
}

// pullRequestStops are the refusals of `ils merge` decided from what the forge says of pull
// request 12 and of the squash merges the repository allows.
func pullRequestStops() []mergeRow {
	row := func(name, text string, script forgeFor, ops []forgetest.Op) mergeRow {
		return mergeRow{
			name: name, script: script, ops: ops, outcome: result.Refused, fetches: true, known: true,
			arrange: func(*gittest.Fixture) mergeEnd { return ending(text) },
		}
	}

	draft := row("draft", draftText, merging().drafted(), changesRead())
	draft.drawn = []drawnPart{commandIn(nextReady, readyCommand)}

	return []mergeRow{
		draft,
		row("draft titled after its branch", draftText, merging().drafted().titled("fix-thing"), changesRead()),
		row("titled after its branch with squash merges forbidden in the settings", titleText("fix-thing"),
			merging().titled("fix-thing").forbiddenInSettings(), changesRead()),
		row("squash merges forbidden in the settings", settingsText, merging().forbiddenInSettings(), changesRead()),
		row("squash merges forbidden in the settings and by the ruleset", settingsText,
			merging().forbiddenInSettings().forbiddenByRuleset(42), changesRead()),
		row("squash merges forbidden by the ruleset", rulesetText, merging().forbiddenByRuleset(42), rulesRead()),
		row("squash merges forbidden by two rulesets",
			"Refused: the rulesets 42, 51 on the trunk "+trunk+" forbid squash merges\n"+nextRuleset,
			merging().forbiddenByRuleset(42, 51), rulesRead()),
	}
}

// titleStops are the refusals of pull request 12 by its title.
func titleStops() []mergeRow {
	titles := []string{"fix-thing", "Feat: x", "feat(): x", "fix(a(b)): x", "feat:x", "feat: ", "feat:  x"}

	rows := make([]mergeRow, 0, len(titles))
	for _, title := range titles {
		text := titleText(title)
		rows = append(rows, mergeRow{
			name: "title " + strconv.Quote(title), script: merging().titled(title), ops: changesRead(),
			outcome: result.Refused, fetches: true, known: true,
			arrange: func(*gittest.Fixture) mergeEnd { return ending(text) },
		})
	}

	// Every title ends on the same next step, so the first row alone checks how it is drawn.
	rows[0].drawn = []drawnPart{commandIn(editCommand, editCommand)}

	return rows
}

// precedingStops are the stops of `ils merge` with a draft on top, which the draft would refuse
// if they let the run reach it.
func precedingStops() []mergeRow {
	return []mergeRow{{
		name: "tip past the head of a draft", script: merging().drafted(), ops: changesRead(),
		outcome: result.Refused, fetches: true, known: true, arrange: pastHead(pastHeadText),
	}}
}

func TestMerge_WhenACheckBeforeTheWaitFails_RefusesAtTheFirst(t *testing.T) {
	t.Parallel()

	runMerges(t, slices.Concat(
		standingStops(), trunkStops(), pullStops(), headStops(), precedingStops(), pullRequestStops(), titleStops(),
	))
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
		assert.Check(t, is.Equal(reason,
			"pull request #"+strconv.Itoa(merged)+" from "+topic+" was merged without the commits listed here"))
		assert.Check(t, is.DeepEqual(mergePayload(t, run.got), resumed(head, ""), cmpopts.EquateEmpty()))
		assert.Check(t, is.Equal(run.written(), run.before))
		assert.Check(t, is.DeepEqual(run.forge.Calls(), asked(changesRead()), whichRepo()))
	})
}

// mergeAccepted are the states that pass a check before the wait that another state fails: each
// reaches the read of the trunk's rules, whose ruleset forbids squash merges.
func mergeAccepted() []mergeRow {
	row := func(name string, script forgeFor, arrange func(*gittest.Fixture)) mergeRow {
		return mergeRow{
			name: name, script: script.forbiddenByRuleset(42), ops: rulesRead(), outcome: result.Refused,
			fetches: true, known: true,
			arrange: func(f *gittest.Fixture) mergeEnd {
				if arrange != nil {
					arrange(f)
				}

				return ending(rulesetText)
			},
		}
	}

	return []mergeRow{
		row("local trunk behind origin's", merging(), func(f *gittest.Fixture) { f.AdvanceOrigin(trunk) }),
		row("no local trunk", merging(), func(f *gittest.Fixture) { f.Git("branch", "--quiet", "-D", trunk) }),
		row("title with a type and a summary", merging().titled("feat: x"), nil),
		row("title with a scope", merging().titled("fix(cli): x"), nil),
		row("title with a breaking mark", merging().titled("feat!: x"), nil),
		row("title with a scope and a breaking mark", merging().titled("refactor(git)!: x"), nil),
	}
}

func TestMerge_WhenStatePassesACheckAnotherStateFails_GoesOnToTheNext(t *testing.T) {
	t.Parallel()

	runMerges(t, mergeAccepted())
}

// pastHead is the arrange of a tip past the head of pull request 12 whose run ends as text says.
func pastHead(text string) func(*gittest.Fixture) mergeEnd {
	return func(f *gittest.Fixture) mergeEnd { return mergeEnd{text: text, head: tipPastHead(f)} }
}

// unansweredMerges are the runs of `ils merge` that origin stops answering partway through.
func unansweredMerges() []mergeRow {
	return []mergeRow{
		{
			name: "asked for its trunk", script: merging(), ops: identityRead(), offline: true, outcome: result.Refused,
			arrange: func(*gittest.Fixture) mergeEnd { return ending(unreached(mergeCommand(), "ask for its trunk")) },
		},
		{
			// Origin answers about its trunk and the fetch of it, and nothing after.
			name: "asked to fetch the head of the pull request", script: merging(), ops: changesRead(), answered: 2,
			outcome: result.Refused, fetches: true, known: true,
			arrange: pastHead(unreached(mergeCommand(), "fetch the head of pull request #12")),
		},
	}
}

// overtimeMerges are the runs of `ils merge` that a cut ends during one of their reads, for each cut.
func overtimeMerges() []mergeRow {
	var rows []mergeRow

	for _, c := range cuts() {
		rows = append(rows,
			mergeRow{
				name: c.name + " during the fetch of the head of the pull request", script: merging(), ops: changesRead(),
				runCtx: c.stalling(2), outcome: result.Refused, fetches: true, known: true,
				arrange: pastHead(overtimeText(mergeCommand(), c, readPullHead)),
			},
			mergeRow{
				name: c.name + " during the read of the trunk's rules", script: merging().failingRules(c.of("gh")),
				ops: rulesRead(), outcome: result.Refused, fetches: true, known: true,
				arrange: func(*gittest.Fixture) mergeEnd { return ending(overtimeText(mergeCommand(), c, readRules)) },
			},
		)
	}

	return rows
}

// failedForgeMerges are the runs of `ils merge` whose read of the forge fails for a reason no
// precondition covers.
func failedForgeMerges() []mergeRow {
	failed := errors.New("HTTP 502")

	return []mergeRow{
		{
			name: "the read of the trunk's rules", script: merging().failingRules(failed), ops: rulesRead(),
			outcome: result.Broke, fetches: true, known: true,
			arrange: func(*gittest.Fixture) mergeEnd { return ending(unreadText(mergeCommand(), readRules, "HTTP 502")) },
		},
	}
}

func TestMerge_WhenAReadBeforeTheWaitDoesNotFinish_StopsNamingTheRead(t *testing.T) {
	t.Parallel()

	runMerges(t, slices.Concat(unansweredMerges(), overtimeMerges(), failedForgeMerges()))
}

// failedGitMerge is the mergeEnd of a run whose git call with the arguments words fails as
// failGit makes it, during the read named read, with pull request 12 at head.
func failedGitMerge(read, head string, words []string) mergeEnd {
	return mergeEnd{failedRead: read, head: head, failing: words}
}

// failedGitMerges are the runs of `ils merge` whose git read fails for a reason no refusal covers.
func failedGitMerges() []mergeRow {
	return []mergeRow{
		{
			name: "the commits on origin's trunk", script: merging(), ops: identityRead(),
			outcome: result.Broke, fetches: true,
			arrange: func(*gittest.Fixture) mergeEnd {
				return failedGitMerge(readFetchedTrunk, "", []string{"rev-parse", "FETCH_HEAD"})
			},
		},
		{
			name: "what the fetch of the head of the pull request fetched", script: merging(), ops: changesRead(),
			outcome: result.Broke, fetches: true, known: true,
			arrange: func(f *gittest.Fixture) mergeEnd {
				end := failedGitMerge(readPullHead, tipPastHead(f), []string{"rev-parse", "FETCH_HEAD"})
				end.from = forgetest.OpFindChanges

				return end
			},
		},
		{
			name: "where the trunk is checked out", script: merging(), ops: identityRead(),
			outcome: result.Broke, fetches: true,
			arrange: func(*gittest.Fixture) mergeEnd {
				return failedGitMerge(readWorktrees, "", worktreeList())
			},
		},
		{
			name: "whether the local trunk exists", script: merging(), ops: identityRead(),
			outcome: result.Broke, fetches: true,
			arrange: func(*gittest.Fixture) mergeEnd {
				return failedGitMerge(readLocalTrunk, "", []string{"show-ref", "--verify", "--quiet", "refs/heads/" + trunk})
			},
		},
		{
			name: "whether origin's trunk has the local trunk's commits", script: merging(), ops: identityRead(),
			outcome: result.Broke, fetches: true,
			arrange: func(*gittest.Fixture) mergeEnd { return failedGitMerge(readLocalTrunk, "", localAncestry()) },
		},
		{
			name: "the commits of the local trunk", script: merging(), ops: identityRead(),
			outcome: result.Broke, fetches: true,
			arrange: func(f *gittest.Fixture) mergeEnd {
				f.AdvanceTrunk()

				return failedGitMerge(readLocalTrunk, "", append(history(), "refs/heads/"+trunk, "^refs/remotes/origin/"+trunk))
			},
		},
		{
			name: "the divergence of the tip from the head", script: merging(), ops: changesRead(),
			outcome: result.Broke, fetches: true, known: true,
			arrange: func(f *gittest.Fixture) mergeEnd {
				head := tipPastHead(f)
				tip := f.Git("rev-parse", topic)

				return failedGitMerge(readPullHead, head, []string{"rev-list", "--left-right", "--count", tip + "..." + head})
			},
		},
	}
}

func TestMerge_WhenAGitReadBeforeTheWaitFails_BrokeNamingTheRead(t *testing.T) {
	// Not parallel: each row puts a failing git first on PATH with t.Setenv.
	for _, tt := range failedGitMerges() {
		t.Run(tt.name, func(t *testing.T) { runMerge(t, tt) })
	}
}

// failingFrom returns a forge hook that makes the scene's git calls whose arguments start with words
// fail as failGit makes them once the forge is asked op; every git call before then runs the real
// git. It cannot run in a parallel test.
func failingFrom(t *testing.T, s *scene, words []string, op forgetest.Op) func(forgetest.Call) {
	t.Helper()

	plain := os.Getenv("PATH")
	failGit(t, s, words)
	faked := os.Getenv("PATH")
	t.Setenv("PATH", plain)

	return func(call forgetest.Call) {
		if call.Op == op {
			t.Setenv("PATH", faked)
		}
	}
}

// localAncestry is the arguments of the git call that asks whether origin's trunk has every commit
// of the local trunk.
func localAncestry() []string {
	return []string{"merge-base", "--is-ancestor", "refs/heads/" + trunk, "refs/remotes/origin/" + trunk}
}

// worktreeList is the arguments of the git call that asks where the trunk is checked out.
func worktreeList() []string { return []string{"worktree", "list", "--porcelain"} }

// stallingGit returns a context that c ends once the scene's git call whose arguments start with
// words is under way, a call that waits until the end kills it. It cannot run in a parallel test.
func (c cut) stallingGit(words []string) func(t *testing.T, s *scene) context.Context {
	return func(t *testing.T, s *scene) context.Context {
		t.Helper()

		addr, connected := gittest.SilentListener(t)
		fakeGit(t, s, gittest.Fake{StallOn: words, StallAt: addr})

		return c.once(t.Context(), connected)
	}
}

func TestMerge_WhenAGitReadIsCutShort_RefusesNamingIt(t *testing.T) {
	tests := []struct {
		name  string
		read  string
		words []string
	}{
		{name: "the read of the local trunk", read: readLocalTrunk, words: localAncestry()},
		{name: "the read of where the trunk is checked out", read: readWorktrees, words: worktreeList()},
	}
	// Not parallel: each row puts a stalling git first on PATH with t.Setenv.
	for _, c := range cuts() {
		for _, tt := range tests {
			t.Run(c.name+"/"+tt.name, func(t *testing.T) {
				runMerge(t, mergeRow{
					script: merging(), ops: identityRead(), runCtx: c.stallingGit(tt.words),
					outcome: result.Refused, fetches: true,
					arrange: func(*gittest.Fixture) mergeEnd { return ending(overtimeText(mergeCommand(), c, tt.read)) },
				})
			})
		}
	}
}

// mergePayload returns the merge payload got carries, or nil when it carries none, failing the test
// when the payload's checks are nil.
func mergePayload(t *testing.T, got result.Result) *result.Merge {
	t.Helper()

	if got.Payload == nil || got.Payload.Merge == nil {
		return nil
	}

	assertChecksArray(t, got.Payload.Merge.Checks)

	return got.Payload.Merge
}

// assertChecksArray fails when a payload's checks are nil, which the JSON prints as null where its
// contract is an array; the payload comparisons equate nil and empty, so they cannot tell.
func assertChecksArray(t *testing.T, checks []result.Check) {
	t.Helper()

	assert.Check(t, checks != nil, "payload checks = nil, which prints as null; want an array")
}
