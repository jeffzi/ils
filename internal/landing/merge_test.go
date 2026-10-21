package landing_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

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
	// conventional is a title that parses as a conventional commit subject.
	conventional = "docs: tidy the readme"

	nextMergeAgain = "Next: ils merge\n"
	nextMergeFixed = "Next: fix what failed, then run ils merge again\n"

	readRules      = "the trunk's rules"
	readLocalTrunk = "the local trunk"
	readPullHead   = "the pull request's head"
	readWorktrees  = "where the trunk is checked out"

	nextReady    = "Next: mark it ready with `gh pr ready --repo " + repoArg + " 12`, then run ils merge again\n"
	draftText    = "Refused: pull request #12 is a draft\n" + nextReady
	settingsText = "Refused: the settings of " + owner + "/" + repoName +
		" forbid squash merges: Allow squash merging is off\n" +
		"Next: allow squash merges in the repository's settings, then run ils merge again\n"
	nextRuleset = "Next: allow squash merges in the trunk's ruleset, then run ils merge again\n"
	rulesetText = "Refused: the ruleset 42 on the trunk " + trunk + " forbids squash merges\n" + nextRuleset
	noPullText  = "Refused: the branch " + topic + " has no open pull request against the trunk " + trunk + "\n" +
		nextPush
	elsewhereText = "Refused: pull request #12 from " + topic + " is open against " + otherBase +
		" and not against the trunk " + trunk + "\n" +
		"Next: close it or change its base with `gh pr edit 12 --base " + trunk + "`, then run ils merge again\n"
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
		"Next: write the pull request's title and body as the squash commit's message with " +
		"`gh pr edit --repo " + repoArg + " 12 --title … --body …`, then run ils merge again\n"
}

// trunkAheadText is the whole text of the refusal of a local trunk whose one commit origin's trunk
// lacks is sha.
func trunkAheadText(sha string) string {
	return "Refused: the local trunk " + trunk + " has commits that origin's trunk lacks, listed here\n" +
		sha + " local change on " + trunk + "\n" +
		"Next: move those commits off the local trunk by hand, then run ils merge again\n"
}

// nothingPastMerge is the refusal by `ils merge` of a branch with no commit past origin's trunk.
func nothingPastMerge(branch string) string {
	return "Refused: the branch " + branch + " has no commit past origin's trunk " + trunk + "\n" +
		"Next: commit the work, then run ils push\n"
}

// unreachedMerge is the whole text of the refusal by `ils merge` when origin stopped answering while
// it was asked for what.
func unreachedMerge(what string) string {
	return "Refused: origin could not be reached to " + what + "\n" + nextMergeAgain
}

// overtimeMerge is the whole text of the refusal by `ils merge` when the deadline ended in the
// read named read.
func overtimeMerge(read string) string {
	return "Refused: the time limit ended while reading " + read + "; nothing was written\n" + nextMergeAgain
}

// unreadMerge is the whole text of the result of `ils merge` when the read named read failed
// saying detail.
func unreadMerge(read, detail string) string {
	return "Could not read " + read + ": " + detail + "\nNothing was written.\n" + nextMergeFixed
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

// failingFind returns build with a find-changes read that fails with err.
func (build forgeFor) failingFind(err error) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.FindChanges = []forgetest.Answer[[]forge.Change]{{Err: err}}
	})
}

// mergeEnd is how a run of `ils merge` must end, as the arrange of a row works it out.
type mergeEnd struct {
	// text is the whole text of the result.
	text string
	// head is the head of pull request 12, which the payload carries; empty is the tip of the branch.
	head string
	// listed is the head the forge lists for pull request 12; empty is head.
	listed string
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
	// deadline returns the context to run under once the scene is built; nil is the test's own.
	deadline func(t *testing.T, s *scene) context.Context
	// script returns the forge's answers once the head of pull request 12 is known.
	script  forgeFor
	name    string
	ops     []forgetest.Op
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

// runMerge runs tt on a scene of its own: `ils merge` ends as the row's arrange says, carries the
// merge payload only once the pull request is known, writes nothing, and reads no further than the
// row says.
func runMerge(t *testing.T, tt mergeRow) {
	t.Helper()

	var end mergeEnd

	s := newScene(t, func(f *gittest.Fixture) { end = tt.arrange(f) }, forgetest.Script{}, tt.offline)
	if end.head == "" {
		end.head = s.fixture.Git("rev-parse", "HEAD")
	}

	script := tt.script(cmp.Or(end.listed, end.head))

	switch {
	case end.from != "":
		script.OnRead = failingFrom(t, s, end.failing, end.from)
	case len(end.failing) > 0:
		failGit(t, s, end.failing)
	}

	s.useForge(t, script)

	if tt.answered > 0 {
		s.stopAnswering(t, tt.answered)
	}

	ctx := t.Context()
	if tt.deadline != nil {
		ctx = tt.deadline(t, s)
	}

	before := s.written()

	got := landing.Merge(ctx, s.deps, mergeRequest())

	assertStoppedBy(t, got, tt.outcome, mergeRequest())

	text, _ := shown(t, got, s.gitDir)
	assert.Check(t, is.Equal(text, end.text))

	var want *result.Merge
	if tt.known {
		want = notMerged(end.head)
	}

	assert.Check(t, is.DeepEqual(mergePayload(got), want, cmpopts.EquateEmpty()))

	assert.Check(t, is.Equal(s.written(), before))
	assert.Check(t, is.DeepEqual(s.forge.Calls(), mergeAsked(tt.ops), whichRepo()))
	assert.Check(t, is.Equal(s.gitRan("fetch"), tt.fetches))
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
	const (
		onTrunk  = "Refused: HEAD is on the trunk " + trunk + ", which ils never pushes\n" + nextBranch + "\n"
		detached = "Refused: HEAD is detached, so there is no branch to push\n" + nextBranch + "\n"
	)

	dirty := func(files ...string) string {
		slices.Sort(files)

		return "Refused: tracked files have changes that are not committed: " + strings.Join(files, ", ") + "\n" +
			"Next: commit them, then run ils merge again\n"
	}
	row := func(name string, arrange func(*gittest.Fixture) string) mergeRow {
		return mergeRow{
			name: name, script: merging(), ops: identityRead(), outcome: result.Refused,
			arrange: func(f *gittest.Fixture) mergeEnd { return ending(arrange(f)) },
		}
	}

	return []mergeRow{
		row("on the trunk", func(f *gittest.Fixture) string {
			f.Git("switch", "--quiet", trunk)

			return onTrunk
		}),
		row("on the trunk with a dirty tree", func(f *gittest.Fixture) string {
			f.Git("switch", "--quiet", trunk)
			f.DirtyFile()

			return onTrunk
		}),
		row("detached HEAD", func(f *gittest.Fixture) string {
			f.Detach()

			return detached
		}),
		row("tracked file changed and not staged", func(f *gittest.Fixture) string { return dirty(f.DirtyFile()) }),
		row("tracked file changed and staged", func(f *gittest.Fixture) string { return dirty(f.StagedFile()) }),
		row("tracked files changed and a local trunk ahead of origin's", func(f *gittest.Fixture) string {
			f.AdvanceTrunk()

			return dirty(f.DirtyFile(), f.StagedFile())
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
	elsewhere.arrange = func(*gittest.Fixture) mergeEnd { return ending(elsewhereText) }

	return []mergeRow{
		elsewhere,
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

	return []mergeRow{
		row("draft", draftText, merging().drafted(), changesRead()),
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

	// The forge still lists the trunk's commit as the head, while origin's ref of the pull request
	// is already the tip, which the run then takes as the head.
	pulledTip := row("tip at origin's head of a pull request the forge lists at an earlier head", merging(), nil)
	pulledTip.arrange = func(f *gittest.Fixture) mergeEnd {
		f.PullRef(opened, f.Git("rev-parse", topic))

		return mergeEnd{text: rulesetText, listed: f.Git("rev-parse", trunk)}
	}

	return []mergeRow{
		row("only a file git does not track", merging(), func(f *gittest.Fixture) { f.UntrackedFile() }),
		row("local trunk behind origin's", merging(), func(f *gittest.Fixture) { f.AdvanceOrigin(trunk) }),
		row("no local trunk", merging(), func(f *gittest.Fixture) { f.Git("branch", "--quiet", "-D", trunk) }),
		row("title with a type and a summary", merging().titled("feat: x"), nil),
		row("title with a scope", merging().titled("fix(cli): x"), nil),
		row("title with a breaking mark", merging().titled("feat!: x"), nil),
		row("title with a scope and a breaking mark", merging().titled("refactor(git)!: x"), nil),
		pulledTip,
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
			arrange: func(*gittest.Fixture) mergeEnd { return ending(unreachedMerge("ask for its trunk")) },
		},
		{
			// Origin answers the question about its trunk, and nothing after.
			name: "asked to fetch its trunk", script: merging(), ops: identityRead(), answered: 1,
			outcome: result.Refused, fetches: true,
			arrange: func(*gittest.Fixture) mergeEnd { return ending(unreachedMerge("fetch its trunk " + trunk)) },
		},
		{
			// Origin answers about its trunk and the fetch of it, and nothing after.
			name: "asked to fetch the head of the pull request", script: merging(), ops: changesRead(), answered: 2,
			outcome: result.Refused, fetches: true, known: true,
			arrange: pastHead(unreachedMerge("fetch the head of pull request #12")),
		},
	}
}

// overtimeMerges are the runs of `ils merge` whose deadline ends during one of their reads.
func overtimeMerges() []mergeRow {
	deadline := fmt.Errorf("gh: %w", context.DeadlineExceeded)

	return []mergeRow{
		{
			name: "during the fetch of origin's trunk", script: merging(), ops: identityRead(), deadline: stalling(1),
			outcome: result.Refused, fetches: true,
			arrange: func(*gittest.Fixture) mergeEnd { return ending(overtimeMerge(readFetchedTrunk)) },
		},
		{
			name: "during the fetch of the head of the pull request", script: merging(), ops: changesRead(),
			deadline: stalling(2), outcome: result.Refused, fetches: true, known: true,
			arrange: pastHead(overtimeMerge(readPullHead)),
		},
		{
			name: "during the find-changes read", script: merging().failingFind(deadline), ops: changesRead(),
			outcome: result.Refused, fetches: true,
			arrange: func(*gittest.Fixture) mergeEnd { return ending(overtimeMerge(readChanges)) },
		},
		{
			name: "during the read of the trunk's rules", script: merging().failingRules(deadline), ops: rulesRead(),
			outcome: result.Refused, fetches: true, known: true,
			arrange: func(*gittest.Fixture) mergeEnd { return ending(overtimeMerge(readRules)) },
		},
	}
}

// failedForgeMerges are the runs of `ils merge` whose read of the forge fails for a reason no
// precondition covers.
func failedForgeMerges() []mergeRow {
	failed := errors.New("HTTP 502")

	return []mergeRow{
		{
			name: "the find-changes read", script: merging().failingFind(failed), ops: changesRead(),
			outcome: result.Broke, fetches: true,
			arrange: func(*gittest.Fixture) mergeEnd { return ending(unreadMerge(readChanges, "HTTP 502")) },
		},
		{
			name: "the read of the trunk's rules", script: merging().failingRules(failed), ops: rulesRead(),
			outcome: result.Broke, fetches: true, known: true,
			arrange: func(*gittest.Fixture) mergeEnd { return ending(unreadMerge(readRules, "HTTP 502")) },
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
	text := unreadMerge(read, "git "+strings.Join(words, " ")+": exit status "+
		strconv.Itoa(gittest.FailedCode)+": "+gittest.FailedSaid)

	return mergeEnd{text: text, head: head, failing: words}
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
				return failedGitMerge(readWorktrees, "", []string{"worktree", "list", "--porcelain"})
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

				return failedGitMerge(readLocalTrunk, "", append(history(), "refs/heads/"+trunk, "^origin/"+trunk))
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

// stallingGit returns a deadline that ends once the scene's git call whose arguments start with
// words is under way, a call that waits until the deadline kills it. It cannot run in a parallel test.
func stallingGit(words []string) func(t *testing.T, s *scene) context.Context {
	return func(t *testing.T, s *scene) context.Context {
		t.Helper()

		addr, connected := gittest.SilentListener(t)
		fakeGit(t, s, gittest.Fake{StallOn: words, StallAt: addr})

		return gittest.Ending(t.Context(), connected, context.DeadlineExceeded)
	}
}

func TestMerge_WhenTheDeadlineEndsInAGitRead_RefusesNamingIt(t *testing.T) {
	tests := []struct {
		name  string
		read  string
		words []string
	}{
		{name: "the read of the local trunk", read: readLocalTrunk, words: localAncestry()},
		{name: "the read of where the trunk is checked out", read: readWorktrees, words: []string{"worktree", "list", "--porcelain"}},
	}
	// Not parallel: each row puts a stalling git first on PATH with t.Setenv.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runMerge(t, mergeRow{
				script: merging(), ops: identityRead(), deadline: stallingGit(tt.words),
				outcome: result.Refused, fetches: true,
				arrange: func(*gittest.Fixture) mergeEnd { return ending(overtimeMerge(tt.read)) },
			})
		})
	}
}

// mergePayload returns the merge payload got carries, or nil when it carries none.
func mergePayload(got result.Result) *result.Merge {
	if got.Payload == nil {
		return nil
	}

	return got.Payload.Merge
}
