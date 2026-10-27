package landing_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
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
	conventional = "docs: tidy the readme"

	readRules      = "the trunk's rules"
	readLocalTrunk = "the local trunk"
	readWorktrees  = "where the trunk is checked out"

	readyCommand = "gh pr ready --repo " + repoArg + " 12"
)

// nextMergeAgain is the `Next:` line that runs `ils merge` again as it is, and nextMergeFixed the
// one that runs it again once what failed is fixed.
func nextMergeAgain() string {
	return expect("Next: ils merge\n")
}

func nextMergeFixed() string {
	return expect("fix what failed", "ils merge")
}

func draftText() string {
	return expect("#12", "is a draft") + mergeCommand().after("`"+readyCommand+"`")
}

func settingsText() string {
	return expect(owner+"/"+repoName, "forbid squash merges", "Allow squash merging") +
		mergeCommand().after("repository's settings")
}

func rulesetText() string {
	return expect("ruleset 42", trunk, "forbids squash merges") + mergeCommand().after("ruleset")
}

func noPullText() string {
	return expect(topic, "no open pull request", trunk) + nextPush()
}

func behindHeadText() string {
	return expect(topic, "is behind", "#12") + nextPush()
}

func pastHeadText() string {
	return expect(topic, "is past", "#12", "never pushed") + nextPush()
}

func divergedHeadText() string {
	return expect(topic, "#12", "have diverged") + nextPush()
}

// titleText is the refusal of pull request 12 titled title.
func titleText(title string) string {
	return expect("#12", "\""+title+"\"", "not a conventional commit subject") + editSteps()
}

// trunkAheadText is the refusal of a local trunk whose one commit origin's trunk lacks is sha.
func trunkAheadText(sha string) string {
	return expect(sha, "local change on "+trunk, "local trunk "+trunk, "origin's trunk lacks") +
		mergeCommand().after("move those commits off the local trunk")
}

// nothingPastMerge is the refusal by `ils merge` of a branch with no commit past origin's trunk. Its
// `Next:` line runs a first `ils push`, not `ils merge` again.
func nothingPastMerge(branch string) string {
	return expect(branch, "no commit past", trunk, "commit the work, then run ils push\n")
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
			Identity:        forgetest.Values(squashing(true)),
			FindChanges:     listing([]forge.Change{pullAt(head)}),
			TrunkProtection: forgetest.Values(forge.Protection{SquashAllowed: true}),
		}
	}
}

// titled returns build with pull request 12 titled title.
func (build forgeFor) titled(title string) forgeFor {
	return build.pulling(func(pull *forge.Change) { pull.Title = title })
}

// drafted returns build with pull request 12 a draft.
func (build forgeFor) drafted() forgeFor {
	return build.pulling(func(pull *forge.Change) { pull.Draft = true })
}

// forbiddenInSettings returns build with a repository whose settings forbid squash merges.
func (build forgeFor) forbiddenInSettings() forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.Identity = forgetest.Values(squashing(false))
	})
}

// forbiddenByRuleset returns build with a trunk whose rulesets with the ids ids forbid squash
// merges.
func (build forgeFor) forbiddenByRuleset(ids ...int) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.TrunkProtection = forgetest.Values(forge.Protection{SquashForbiddenBy: ids})
	})
}

// mergeEnd is how a run of `ils merge` must end, as the arrange of a row works it out.
type mergeEnd struct {
	text string
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
	merge := mergeDetail(forge.VerdictNone, forge.MergeUnknown)
	merge.SHA = head

	return merge
}

// runMerge runs tt on a scene of its own: `ils merge` prints the text the row's arrange says, and
// stops before the wait as assertStoppedBeforeWait checks.
func runMerge(t *testing.T, tt mergeRow) {
	t.Helper()

	end, run := mergeRowOn(t, tt)

	text, _ := shown(t, run.got, run.gitDir)
	assertSays(t, text, end.text)
	assertStoppedBeforeWait(t, tt, end, run)
}

// assertStoppedBeforeWait fails unless run, of tt, ended as the row says, holds every step line it
// said, carries the merge payload only once the pull request is known, and read no further than
// the row says.
func assertStoppedBeforeWait(t *testing.T, tt mergeRow, end mergeEnd, run mergeRun) {
	t.Helper()

	assertStoppedBy(t, run.got, tt.outcome, mergeRequest())

	var want *result.Merge
	if tt.known {
		want = notMerged(end.head)
	}

	assert.Check(t, is.DeepEqual(mergePayload(t, run.got), want, cmpopts.EquateEmpty()))

	assert.Check(t, is.DeepEqual(run.forge.Calls(), mergeAsked(tt.ops), whichRepo()))
	assert.Check(t, is.Equal(run.gitRan("fetch"), tt.fetches))
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

				return ending(noPullText())
			},
		}
	}
	landed := row("open pull request against the trunk beside a merged one that carries every commit",
		merging().with(func(script *forgetest.Script, tip string) {
			script.FindChanges = listing([]forge.Change{pullAt(tip), change(merged, trunk, forge.ChangeMerged)})
		}), nil)
	landed.arrange = func(f *gittest.Fixture) mergeEnd {
		f.LandPull(merged, topic)

		return ending(expect(topic, "already landed", "#"+strconv.Itoa(merged)) + nextBranch())
	}

	return []mergeRow{
		landed,
		row("no pull request at all", merging().findingOnly(), nil),
		row("closed pull request against the trunk", merging().findingOnly(change(opened, trunk, forge.ChangeClosed)), nil),
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
		row("tip behind the head", behindHeadText(), headPastTip),
		row("tip past the head", pastHeadText(), tipPastHead),
		row("tip diverged from the head", divergedHeadText(), func(f *gittest.Fixture) string {
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
		row("draft", draftText(), merging().drafted(), changesRead()),
		row("draft titled after its branch", draftText(), merging().drafted().titled("fix-thing"), changesRead()),
		row("titled after its branch with squash merges forbidden in the settings", titleText("fix-thing"),
			merging().titled("fix-thing").forbiddenInSettings(), changesRead()),
		row("squash merges forbidden in the settings", settingsText(), merging().forbiddenInSettings(), changesRead()),
		row("squash merges forbidden by the ruleset", rulesetText(), merging().forbiddenByRuleset(42), rulesRead()),
		row("squash merges forbidden by two rulesets",
			expect("rulesets 42, 51", trunk, "forbid squash merges")+mergeCommand().after("ruleset"),
			merging().forbiddenByRuleset(42, 51), rulesRead()),
	}
}

// titleStops are the refusals of pull request 12 by its title.
func titleStops() []mergeRow {
	titles := []string{"fix-thing", "Feat: x", "feat(): x", "fix(a(b)): x", "feat:x", "feat:  x"}

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
		outcome: result.Refused, fetches: true, known: true, arrange: pastHead(pastHeadText()),
	}}
}

func TestMerge_WhenACheckBeforeTheWaitFails_RefusesAtTheFirst(t *testing.T) {
	t.Parallel()

	runMerges(t, slices.Concat(
		trunkStops(), pullStops(), headStops(), precedingStops(), pullRequestStops(), titleStops(),
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
		assertSays(t, reason, expect("#"+strconv.Itoa(merged), topic, "merged without the commits"))
		assert.Check(t, is.DeepEqual(mergePayload(t, run.got), resumed(head, ""), cmpopts.EquateEmpty()))
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

				return ending(rulesetText())
			},
		}
	}

	return []mergeRow{
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
			// Origin answers about its trunk and the fetch of it, and nothing after.
			name: "asked to fetch the head of the pull request", script: merging(), ops: changesRead(), answered: 2,
			outcome: result.Refused, fetches: true, known: true,
			arrange: pastHead(unreached(mergeCommand(), "fetch the head of pull request #12")),
		},
	}
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

	runMerges(t, slices.Concat(unansweredMerges(), failedForgeMerges()))
}

// failedGitMerge is the mergeEnd of a run whose git call with the arguments words fails as
// failGit makes it, with pull request 12 at head.
func failedGitMerge(head string, words []string) mergeEnd {
	return mergeEnd{head: head, failing: words}
}

// failedGitRow is one run of `ils merge` whose git read fails for a reason no refusal covers.
type failedGitRow struct {
	// read is the read the failing git call makes fail.
	read string
	mergeRow
}

// failedGitMerges are the runs of `ils merge` whose git read fails for a reason no refusal covers.
func failedGitMerges() []failedGitRow {
	return []failedGitRow{
		{
			read: readHead,
			name: "what the fetch of the head of the pull request fetched", script: merging(), ops: changesRead(),
			outcome: result.Broke, fetches: true, known: true,
			arrange: func(f *gittest.Fixture) mergeEnd {
				end := failedGitMerge(tipPastHead(f), []string{"rev-parse", "FETCH_HEAD"})
				end.from = forgetest.OpFindChanges

				return end
			},
		},
		{
			read: readWorktrees,
			name: "where the trunk is checked out", script: merging(), ops: identityRead(),
			outcome: result.Broke, fetches: true,
			arrange: func(*gittest.Fixture) mergeEnd {
				return failedGitMerge("", worktreeList())
			},
		},
		{
			read: readLocalTrunk,
			name: "whether the local trunk exists", script: merging(), ops: identityRead(),
			outcome: result.Broke, fetches: true,
			arrange: func(*gittest.Fixture) mergeEnd {
				return failedGitMerge("", []string{"show-ref", "--verify", "--quiet", "refs/heads/" + trunk})
			},
		},
		{
			read: readHead,
			name: "the divergence of the tip from the head", script: merging(), ops: changesRead(),
			outcome: result.Broke, fetches: true, known: true,
			arrange: func(f *gittest.Fixture) mergeEnd {
				head := tipPastHead(f)
				tip := f.Git("rev-parse", topic)

				return failedGitMerge(head, []string{"rev-list", "--left-right", "--count", tip + "..." + head})
			},
		},
	}
}

func TestMerge_WhenAGitReadBeforeTheWaitFails_BrokeNamingTheRead(t *testing.T) {
	// Not parallel: each row puts a failing git first on PATH with t.Setenv.
	for _, tt := range failedGitMerges() {
		t.Run(tt.name, func(t *testing.T) {
			end, run := mergeRowOn(t, tt.mergeRow)

			assertFailedGit(t, run.got, mergeCommand(), tt.read)
			assertStoppedBeforeWait(t, tt.mergeRow, end, run)
		})
	}
}

// failingFrom returns a forge hook that makes the scene's git calls whose arguments start with words
// fail as failGit makes them once the forge is asked op; every git call before then runs the real
// git. It cannot run in a parallel test.
func failingFrom(t *testing.T, s *scene, words []string, op forgetest.Op) func(forgetest.Call) {
	t.Helper()

	arm := armedLater(t, func() { failGit(t, s, words) })

	return func(call forgetest.Call) {
		if call.Op == op {
			arm()
		}
	}
}

// worktreeList is the arguments of the git call that asks where the trunk is checked out.
func worktreeList() []string { return []string{"worktree", "list", "--porcelain"} }

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

// mergeRun is one run of `ils merge` on a scene `setting` describes.
type mergeRun struct {
	*scene
	// watch is what the run told the progress hook and the logger its deps carry.
	watch *watch
	tip   string
	// before is everything the run could change, as it was before the run.
	before string
	got    result.Result
}

// setting is the scene of a run of `ils merge`; by default its branch passes every check before the
// wait.
type setting struct {
	arrange   func(*gittest.Fixture)
	tip       func(f *gittest.Fixture) string
	scripted  func(t *testing.T, s *scene, script *forgetest.Script)
	rewire    func(t *testing.T, s *scene)
	runCtx    func(t *testing.T, s *scene) context.Context
	landed    func(t *testing.T, f *gittest.Fixture)
	script    forgeFor
	cut       func(context.Context) context.Context
	left      time.Duration
	unbounded bool
	offline   bool
}

// runMergeOn runs `ils merge` inside the test's bubble on the scene set describes, under a deadline
// unless set is unbounded, and fails unless every step line the run said is held by its result. A
// landing the forge lets through squashes the tip onto origin's trunk, as GitHub would. The run has
// a progress hook and a logger.
func runMergeOn(t *testing.T, set setting) mergeRun {
	t.Helper()

	s := newScene(t, set.arrange, forgetest.Script{}, set.offline)

	tip := s.fixture.Git("rev-parse", topic)
	if set.tip != nil {
		tip = set.tip(s.fixture)
	}

	script := set.script(tip)
	if len(script.Land) > 0 && script.Land[0] == nil {
		script.OnLand = squashOnLand(t, s.fixture, tip, set.landed)
	}

	if set.scripted != nil {
		set.scripted(t, s, &script)
	}

	s.useForge(t, script)

	w := watchOn(&s.deps)

	if set.rewire != nil {
		set.rewire(t, s)
	}

	ctx := t.Context()

	switch {
	case set.runCtx != nil:
		ctx = set.runCtx(t, s)
	case !set.unbounded:
		ctx = within(t, set.left)
	}

	if set.cut != nil {
		ctx = set.cut(ctx)
	}

	before := s.written()

	got := landing.Merge(ctx, s.deps, mergeRequest())

	assertHeld(t, w, got)

	return mergeRun{scene: s, watch: w, got: got, tip: tip, before: before}
}

// squashOnLand returns a landing hook that squashes tip onto origin's trunk with the landing's subject
// and body, then runs landed when it is set.
func squashOnLand(t *testing.T, f *gittest.Fixture, tip string, landed func(*testing.T, *gittest.Fixture)) func(forgetest.Call) {
	t.Helper()

	return func(call forgetest.Call) {
		f.SquashOnOrigin(tip, call.Landing.Subject, call.Landing.Body)

		if landed != nil {
			landed(t, f)
		}
	}
}

// armedLater runs install, which puts a fake git first on PATH, then puts the plain PATH back and
// returns arm, which puts the fake git first again. It cannot run in a parallel test.
func armedLater(t *testing.T, install func()) (arm func()) {
	t.Helper()

	plain := os.Getenv("PATH")
	install()
	faked := os.Getenv("PATH")
	t.Setenv("PATH", plain)

	return func() { t.Setenv("PATH", faked) }
}
