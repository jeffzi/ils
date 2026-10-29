package landing_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
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
	"github.com/jeffzi/ils/internal/result/resulttest"
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
	return expect(resulttest.NextLine("ils merge") + "\n")
}

func nextMergeFixed() string {
	return expect("fix what failed", "ils merge")
}

func draftText() string {
	return expect("#12", "is a draft") + mergeCommand().after("`"+readyCommand+"`")
}

func settingsText() string {
	return expect(owner+"/"+repoName, "forbid squash merges", "ils merge")
}

func rulesetText() string {
	return expect("ruleset 42", trunk, "ils merge")
}

func noPullText() string {
	return expect(topic, "no open pull request", trunk) + nextPush()
}

func behindHeadText() string {
	return expect(topic, "is behind", "#12") + nextPush()
}

func pastHeadText() string {
	return expect(topic, "is past", "#12") + nextPush()
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
	return expect(shortSHA(sha), "local change on "+trunk, trunk, "ils merge")
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
	// script returns the forge's answers once the head of pull request 12 is known.
	script  forgeFor
	name    string
	ops     []forgetest.Op
	outcome result.Outcome
	// answered is how many calls origin answers before it stops; zero is every call.
	answered int
	// fetches is whether git fetches from origin before the run stops.
	fetches bool
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
		row("no commit past the trunk", func(f *gittest.Fixture) string {
			f.Git("switch", "--quiet", "--create", spare, trunk)

			return nothingPastMerge(spare)
		}),
	}
}

// pullStops are the refusals of `ils merge` decided from the pull requests the forge finds for
// the branch, before one against the trunk is known.
func pullStops() []mergeRow {
	row := func(name string, script forgeFor) mergeRow {
		return mergeRow{
			name: name, script: script, outcome: result.Refused, fetches: true,
			ops:     changesRead(),
			arrange: func(*gittest.Fixture) mergeEnd { return ending(noPullText()) },
		}
	}
	landed := row("open pull request against the trunk beside a merged one that carries every commit",
		merging().with(func(script *forgetest.Script, tip string) {
			script.FindChanges = listing([]forge.Change{pullAt(tip), change(merged, trunk, forge.ChangeMerged)})
		}))
	landed.arrange = func(f *gittest.Fixture) mergeEnd {
		f.LandPull(merged, topic)

		return ending(expect(topic, "already landed", "#"+strconv.Itoa(merged)) + nextBranch())
	}

	return []mergeRow{
		landed,
		row("closed pull request against the trunk", merging().findingOnly(change(opened, trunk, forge.ChangeClosed))),
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
		row("squash merges forbidden in the settings", settingsText(), merging().forbiddenInSettings(), changesRead()),
		row("squash merges forbidden by the ruleset", rulesetText(), merging().forbiddenByRuleset(42), rulesRead()),
		row("squash merges forbidden by two rulesets",
			expect("rulesets 42, 51", trunk, "ils merge"),
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

func TestMerge_WhenACheckBeforeTheWaitFails_RefusesAtTheFirst(t *testing.T) {
	t.Parallel()

	runMerges(t, slices.Concat(
		trunkStops(), pullStops(), headStops(), pullRequestStops(), titleStops(),
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
	row := func(name string, script forgeFor) mergeRow {
		return mergeRow{
			name: name, script: script.forbiddenByRuleset(42), ops: rulesRead(), outcome: result.Refused,
			fetches: true, known: true,
			arrange: func(*gittest.Fixture) mergeEnd { return ending(rulesetText()) },
		}
	}

	return []mergeRow{
		row("title with a scope", merging().titled("fix(cli): x")),
		row("title with a breaking mark", merging().titled("feat!: x")),
		row("title with a scope and a breaking mark", merging().titled("refactor(git)!: x")),
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
	failed := errors.New(forgeFailure)

	return []mergeRow{
		{
			name: "the read of the trunk's rules", script: merging().failingRules(failed), ops: rulesRead(),
			outcome: result.Broke, fetches: true, known: true,
			arrange: func(*gittest.Fixture) mergeEnd { return ending(unreadText(mergeCommand(), readRules, forgeFailure)) },
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
}

// runMergeOn runs `ils merge` inside the test's bubble on the scene set describes, under a deadline
// unless set is unbounded, and fails unless every step line the run said is held by its result. A
// landing the forge lets through squashes the tip onto origin's trunk, as GitHub would. The run has
// a progress hook and a logger.
func runMergeOn(t *testing.T, set setting) mergeRun {
	t.Helper()

	s := newScene(t, set.arrange, forgetest.Script{}, false)

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

const (
	readMessage = "the pull request's message"

	// worded is the title pull request 12 holds when its message is read after the wait.
	worded = "docs(readme): tidy the wording"

	// squashSubject is the subject the squash commit of pull request 12 lands with.
	squashSubject = worded + " (#12)"
)

// waiting is merging with a forge that shows pull request 12 clean at the tip and answers the
// checks with each of verdicts in turn. It has no answer for the message read.
func waiting(verdicts ...forge.Verdict) forgeFor {
	return merging().showing(cleanHead()).with(func(script *forgetest.Script, _ string) {
		script.Verdict = says(verdicts...)
	})
}

// messageTaking returns build with a message read that lets d of fake time pass.
func (build forgeFor) messageTaking(d time.Duration) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.OnRead = func(call forgetest.Call) {
			if call.Op == forgetest.OpMessage {
				time.Sleep(d)
			}
		}
	})
}

// titledAfter is a message of pull request 12 titled worded, with body and trailers.
func titledAfter(body string, trailers ...string) forgetest.Answer[forge.Message] {
	return forgetest.Answer[forge.Message]{Value: forge.Message{Title: worded, Body: body, Trailers: trailers}}
}

// mergeDetail returns the payload of `ils merge` on pull request 12 once its checks were read, but
// for the sha: the verdict's name, the merge state in the words of ils, and the deciding checks.
func mergeDetail(verdict forge.VerdictState, state forge.MergeState, checks ...forge.Check) *result.Merge {
	push := detail(verdict, state, checks...)

	return &result.Merge{
		URL: push.URL, Number: push.Number, Verdict: push.Verdict, MergeState: push.MergeState, Checks: push.Checks,
	}
}

// composed returns the payload of a green run on a clean pull request 12 with subject and body.
func composed(subject, body string) *result.Merge {
	payload := mergeDetail(forge.VerdictGreen, forge.MergeClean, passing().Checks...)
	payload.Subject = subject
	payload.Body = body

	return payload
}

// made returns payload as that of a run that made the merge and ended on the trunk.
func made(payload *result.Merge) *result.Merge {
	payload.Merged = true
	payload.Ending = result.EndingTrunk

	return payload
}

// waitRow is one run of `ils merge` that passes every check before the wait, and how it ends.
type waitRow struct {
	script  forgeFor
	payload *result.Merge
	cut     func(context.Context) context.Context
	name    string
	text    string
	outcome result.Outcome
	left    time.Duration
}

// runWaits runs each row as a test in a bubble of its own: the run carries the row's payload,
// writes nothing, and ends as the row says.
func runWaits(t *testing.T, rows []waitRow) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				run := runMergeOn(t, setting{script: tt.script, left: tt.left, cut: tt.cut})

				want := *tt.payload
				want.SHA = run.tip
				assert.Check(t, is.DeepEqual(mergePayload(t, run.got), &want, cmpopts.EquateEmpty()))
				assert.Check(t, is.Equal(run.written(), run.before))
				assertEndedBy(t, run.got, run.gitDir, ended{text: mark(tt.text, run.tip), req: mergeRequest(), outcome: tt.outcome})
			})
		})
	}
}

// waitVerdicts are the runs of `ils merge` whose wait ends in a verdict that stops the merge.
func waitVerdicts() []waitRow {
	red := lintRed()
	red.FailedLog.Text = steps(1, 3)
	nothing := expect("no check or status reported", tipMark, "#12")

	return []waitRow{
		{
			name: "a deciding check failed", script: waiting(red).showing(inState(forge.MergeBlockedOther)),
			outcome: result.Refused, payload: mergeDetail(forge.VerdictRed, forge.MergeBlockedOther, red.Checks...),
			text: expect("lint", "failed", lintURL, partsOf(stateLine(forge.MergeBlockedOther))[0]+steps(1, 3),
				"lint", "failed") + nextRed(),
		},
		{
			name:    "nothing reported in a repository with CI on its trunk",
			script:  waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{Value: true}),
			outcome: result.Refused, text: nothing, payload: mergeDetail(forge.VerdictNone, forge.MergeClean),
		},
		{
			// A trunk that requires checks has CI, so the forge has no answer for the CI read.
			name:    "nothing reported though the trunk requires checks",
			script:  waiting(forge.Verdict{MissingRequired: []string{"lint", "test"}}).requiring("lint", "test"),
			outcome: result.Refused, text: nothing, payload: mergeDetail(forge.VerdictNone, forge.MergeClean),
		},
	}
}

// pendingWaits are the runs of `ils merge` whose wait, on a deadline 20 seconds before the
// command's, ends before the forge had an answer.
func pendingWaits() []waitRow {
	var rows []waitRow
	for _, c := range cuts() {
		pendingText := func(read string) string {
			return expect(c.ends, read, "nothing more will change") + nextPush()
		}
		rows = append(rows, waitRow{
			name:    c.name + " ends the CI read",
			script:  waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{Err: c.of("gh")}),
			outcome: result.Pending, text: pendingText(readCI), payload: mergeDetail(forge.VerdictNone, forge.MergeClean),
		})
	}

	return rows
}

// waitBreaks are the runs of `ils merge` whose read during the wait fails.
func waitBreaks() []waitRow {
	failed := errors.New(forgeFailure)
	row := func(read string, script forgeFor) waitRow {
		return waitRow{
			name: read, script: script, outcome: result.Broke, text: unreadText(mergeCommand(), read, forgeFailure),
			payload: mergeDetail(forge.VerdictNone, forge.MergeUnknown),
		}
	}
	unread := row(readCI, waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{Err: failed}))
	unread.payload = mergeDetail(forge.VerdictNone, forge.MergeClean)

	return []waitRow{
		row(readChecks, waiting().failingChecks(failed)),
		unread,
	}
}

func TestMerge_WhenTheWaitStopsTheMerge_EndsWithNothingWritten(t *testing.T) {
	t.Parallel()

	runWaits(t, slices.Concat(waitVerdicts(), pendingWaits(), waitBreaks()))
}

// afterWaitStops are the runs of `ils merge` whose checks are green and which stop on what they
// read after the wait.
func afterWaitStops() []waitRow {
	greenThen := func(state forge.MergeState, lead, rest string) waitRow {
		return waitRow{
			name:    string(state),
			script:  waiting(passing()).showing(cleanHead(), inState(state)),
			outcome: result.Refused, text: lead + stateLine(state) + rest,
			payload: mergeDetail(forge.VerdictGreen, state, passing().Checks...),
		}
	}
	reviewed := inState(forge.MergeBlockedByReview)
	// The forge reports no required check missing, so the wait ends green and GitHub's state decides.
	// The head has checks named lint and test; named is the line that names the required checks.
	missing := func(name, named string, required ...string) waitRow {
		row := greenThen(forge.MergeBlockedByMissingCheck,
			expect(tipMark, ": 2 passed\n")+named,
			expect("requires has no check", tipMark))
		row.name = name
		row.script = waiting(requiredPassing()).requiring(required...).
			showing(cleanHead(), inState(forge.MergeBlockedByMissingCheck))
		row.payload = mergeDetail(forge.VerdictGreen, forge.MergeBlockedByMissingCheck, requiredPassing().Checks...)

		return row
	}

	conflicting := greenThen(forge.MergeConflicting,
		countLine(), expect("conflicts with the trunk")+nextTrunk())
	conflicting.name = "conflicting after the checks"

	return []waitRow{
		greenThen(forge.MergeDraft, countLine(), draftText()),
		conflicting,
		greenThen(forge.MergeBlockedByReview,
			countLine(), expect("#12", "requires a review")),
		missing(string(forge.MergeBlockedByMissingCheck), expect("no check on", tipMark, ": build\n"),
			"lint", "test", "build"),
		// GitHub expects a check ils cannot tell apart, so every required check is named.
		missing("blocked-by-missing-check with a check for each required one", expect("Required by the trunk: lint, test\n"),
			"lint", "test"),
		greenThen(forge.MergeBlockedOther,
			countLine(), expect("blocks", "#12", "reason ils does not read", pullURL)),
		{
			name:    string(forge.MergeBlockedByRunningCheck),
			script:  waiting(passing()).showing(cleanHead(), inState(forge.MergeBlockedByRunningCheck)),
			outcome: result.Pending, payload: mergeDetail(forge.VerdictGreen, forge.MergeBlockedByRunningCheck, passing().Checks...),
			text: countLine() + stateLine(forge.MergeBlockedByRunningCheck) +
				expect("requires still runs on", tipMark) + nextMergeAgain(),
		},
		{
			name: "blocked by review with nothing reported and no CI on the trunk",
			script: waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{}).
				showing(cleanHead(), reviewed),
			outcome: result.Refused, payload: mergeDetail(forge.VerdictNone, forge.MergeBlockedByReview),
			text: noCILine() + blockedLine() + expect("#12", "requires a review"),
		},
	}
}

// settledStops are the runs of `ils merge` whose checks are green and whose merge state, read again
// after the wait, stops the merge once a poll finds it or fails.
func settledStops() []waitRow {
	unknown := inState(forge.MergeUnknown)
	rows := []waitRow{
		{
			name:   "behind after an unknown poll",
			script: waiting(passing()).showing(cleanHead(), unknown, inState(forge.MergeBehind)), outcome: result.Refused,
			text:    countLine() + stateLine(forge.MergeBehind) + expect("behind the trunk") + nextTrunk(),
			payload: mergeDetail(forge.VerdictGreen, forge.MergeBehind, passing().Checks...),
		},
		{
			name: "conflicting with nothing reported and no CI on the trunk",
			script: waiting(forge.Verdict{}).answeringCI(forgetest.Answer[bool]{}).
				showing(cleanHead(), inState(forge.MergeConflicting)),
			outcome: result.Refused, payload: mergeDetail(forge.VerdictNone, forge.MergeConflicting),
			text: noCILine() + stateLine(forge.MergeConflicting) + expect("conflicts with the trunk") + nextTrunk(),
		},
		{
			name:    "a poll of the merge state fails",
			script:  waiting(passing()).showing(cleanHead(), unknown).thenFailingHead(errors.New(forgeFailure)),
			outcome: result.Broke, text: unreadText(mergeCommand(), readMergeState, forgeFailure),
			payload: mergeDetail(forge.VerdictGreen, forge.MergeUnknown, passing().Checks...),
		},
	}

	c := byTimeLimit()

	return append(rows, waitRow{
		name: c.name + " ends the poll of the merge state", script: waiting(passing()).showing(cleanHead(), unknown),
		cut:     c.inPoll,
		outcome: result.Pending, payload: mergeDetail(forge.VerdictGreen, forge.MergeUnknown, passing().Checks...),
		text: countLine() + expect("#12", "can merge", c.before) + nextPush(),
	})
}

// messageStops are the runs of `ils merge` whose checks are green on a clean pull request and which
// stop on its message, read after the wait, or on the time that read left.
func messageStops() []waitRow {
	green := mergeDetail(forge.VerdictGreen, forge.MergeClean, passing().Checks...)
	row := func(name string, answer forgetest.Answer[forge.Message], outcome result.Outcome, text string) waitRow {
		return waitRow{
			name: name, script: waiting(passing()).saying(answer), outcome: outcome, text: text, payload: green,
		}
	}
	late := row("the message read leaves fewer than 20 seconds", titledAfter(""), result.Pending,
		countLine()+expect("for the merge", "for the merge")+nextPush())
	// The wait's deadline is 80 seconds away: the read ends 19 seconds before the command's.
	late.script = late.script.messageTaking(81 * time.Second)
	late.payload = composed(squashSubject, "")
	unread := row("the message read fails", forgetest.Answer[forge.Message]{Err: errors.New(forgeFailure)},
		result.Broke, unreadText(mergeCommand(), readMessage, forgeFailure))

	rows := []waitRow{
		row("the title no longer parses", forgetest.Answer[forge.Message]{Value: forge.Message{Title: "fix-thing"}},
			result.Refused, countLine()+titleText("fix-thing")),
		unread,
	}

	return append(rows, late)
}

func TestMerge_WhenWhatIsReadAfterTheWaitStopsTheMerge_EndsWithNothingWritten(t *testing.T) {
	t.Parallel()

	runWaits(t, slices.Concat(afterWaitStops(), settledStops(), messageStops()))
}

// readyRow is one run of `ils merge` that reaches the merge, and the payload it carries.
type readyRow struct {
	script forgeFor
	// payload is the merge payload the run carries, but for the sha.
	payload *result.Merge
	name    string
}

// readyRows are the runs of `ils merge` that reach the merge, each with the subject and body it
// composed from the message read after the wait.
func readyRows() []readyRow {
	const body = "Tidy the readme.\n\n# Notes\nKeep the heading."

	trailers := []string{"Co-authored-by: Sam Smith <sam@example.com>", "Signed-off-by: Kim Lee <kim@example.com>"}
	block := strings.Join(trailers, "\n")
	row := func(name string, answer forgetest.Answer[forge.Message], want string) readyRow {
		return readyRow{
			name: name, script: waiting(passing()).saying(answer).landing(nil, nil),
			payload: made(composed(squashSubject, want)),
		}
	}

	return []readyRow{
		row("a body with a line starting with # and trailers", titledAfter(body, trailers...), body+"\n\n"+block),
		row("an empty body and trailers", titledAfter("", trailers...), block),
		row("a body and no trailers", titledAfter(body), body),
	}
}

func TestMerge_WhenTheMessageReadLeavesExactlyTheReserve_Merges(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// The wait's deadline is 80 seconds away: the read ends 20 seconds before the command's.
		script := waiting(passing()).saying(titledAfter("")).landing(nil, nil).messageTaking(80 * time.Second)

		run := runMergeIn(t, script, 0)

		assertStoppedBy(t, run.got, result.Done, mergeRequest())

		want := made(composed(squashSubject, ""))
		want.SHA = run.tip
		assert.Check(t, is.DeepEqual(mergePayload(t, run.got), want, cmpopts.EquateEmpty()))
	})
}

// runMergeIn runs `ils merge` inside the test's bubble with the forge script builds and a deadline
// left of fake time away.
func runMergeIn(t *testing.T, script forgeFor, left time.Duration) mergeRun {
	t.Helper()

	return runMergeOn(t, setting{script: script, left: left})
}

func TestMerge_WhenTheChecksNeverConclude_EndsTheWaitTheReserveBeforeTheDeadline(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()

		run := runMergeIn(t, waiting(running()), 0)

		waited := time.Since(started)
		latest := bound - landing.MergeReserve
		// The last poll is the first that leaves less than a poll and the head read's reserve before
		// the wait's deadline, so the wait ends within that much of it.
		earliest := latest - verdictPoll - headReadReserve

		assertStoppedBy(t, run.got, result.Pending, mergeRequest())
		assert.Check(t, waited <= latest, "the wait ended %s in, want at most %s", waited, latest)
		assert.Check(t, waited > earliest, "the wait ended %s in, want more than %s", waited, earliest)
	})
}

func TestMerge_WhenEveryCheckBeforeTheMergeHolds_ComposesTheMessageFromThePullRequest(t *testing.T) {
	t.Parallel()

	for _, tt := range readyRows() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				run := runMergeIn(t, tt.script, 0)

				assertStoppedBy(t, run.got, result.Done, mergeRequest())

				want := *tt.payload
				want.SHA = run.tip
				assert.Check(t, is.DeepEqual(mergePayload(t, run.got), &want, cmpopts.EquateEmpty()))
			})
		})
	}
}

// movedTo is the reason of a refusal of pull request 12, whose head moved to movedSHA.
func movedTo() string { return expect("#12", "moved to "+movedSHA) }

func TestMerge_WhenTheHeadMovesPastThePushedCommit_RefusesWithoutMerging(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// The head moves while the merge state is read again, to a commit in a merge state of its own.
		unknownHead := inState(forge.MergeUnknown)
		heads := []forge.Head{cleanHead(), unknownHead, unknownHead, moved()}
		// The forge would compose and land the merge, were it asked to.
		script := waiting(passing()).showing(heads...).saying(titledAfter("Tidy the wording.")).landing(nil, nil)

		run := runMergeIn(t, script, 0)

		assertEndedBy(t, run.got, run.gitDir, ended{text: movedTo() + mergeCommand().after("pull the new commit"), req: mergeRequest(), outcome: result.Refused})
		want := mergeDetail(forge.VerdictNone, forge.MergeUnknown)
		want.SHA = run.tip
		assert.Check(t, is.DeepEqual(mergePayload(t, run.got), want, cmpopts.EquateEmpty()))
		assert.Check(t, is.Equal(run.written(), run.before))
	})
}

func TestMerge_WhenTrunkRequiresChecks_WaitsForThoseOfTheHead(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		required := []string{"lint", "test"}

		run := runMergeIn(t, waiting(requiredPassing()).requiring(required...).saying(titledAfter("")).landing(nil, nil), 0)

		assertReadsRequire(t, run.forge.Calls(), run.tip, required)
	})
}
