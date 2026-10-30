package landing_test

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

const (
	spare  = "spare-parts"
	side   = "polish-docs"
	early  = "early-draft"
	yearly = "yearly-notes"
	merged = 7
	opened = 12

	readCommits  = "the branch's commits"
	readCopy     = "origin's copy of the branch"
	readBranches = "the local branches"
)

// finding is allowed with a find-changes call that returns found.
func finding(found ...forge.Change) forgetest.Script {
	script := allowed()
	script.FindChanges = forgetest.Values(found)

	return script
}

// change returns the pull request numbered number from the topic branch against base.
func change(number int, base string, state forge.ChangeState) forge.Change {
	return forge.Change{Number: number, Base: base, State: state, Title: topic}
}

// identityRead and changesRead are the forge operations Push makes up to each of its two reads.
func identityRead() []forgetest.Op { return []forgetest.Op{forgetest.OpIdentity} }

func changesRead() []forgetest.Op {
	return []forgetest.Op{forgetest.OpIdentity, forgetest.OpFindChanges}
}

// finish is how a result must have ended: its outcome and the parts of its text, as expect builds
// them. A result of the push sequence is also held to the parts of its reason and to its payload,
// nil for none. req is the request it ran, a bare `ils push` when it names none.
type finish struct {
	payload *result.Push
	text    string
	reason  string
	req     landing.Request
	outcome result.Outcome
}

// request is the request end ran: its req, or a bare `ils push` when it names none.
func (end finish) request() landing.Request {
	if end.req.Command == "" {
		return pushRequest()
	}

	return end.req
}

// assertEnded fails unless got ended as end says, carries the command and words of end's request,
// and renders text that holds the parts of end's text.
func assertEnded(t *testing.T, s *scene, got result.Result, end finish) {
	t.Helper()

	assertEndedBy(t, got, s.gitDir, ended{text: end.text, req: end.request(), outcome: end.outcome})
}

// reads is what a refusing Push may have done: the refs before it, the forge operations it made,
// and whether it fetched from origin.
type reads struct {
	before  string
	ops     []forgetest.Op
	fetches bool
}

// assertOnlyRead fails unless nothing was written since before, the forge was called exactly as
// ops stand for, and git fetched from origin only when fetches is set.
func assertOnlyRead(t *testing.T, s *scene, read reads) {
	t.Helper()

	assert.Check(t, is.Equal(s.written(), read.before))

	assertAsked(t, s, read.ops)

	assert.Check(t, is.Equal(s.gitRan("fetch"), read.fetches))
}

// pushRefusal is one state of the branch that Push refuses, and how far Push reads to refuse it.
type pushRefusal struct {
	// arrange changes the repository and returns the parts of the refusal's text.
	arrange func(*gittest.Fixture) string
	name    string
	script  forgetest.Script
	// ops are the forge operations made before the refusal.
	ops []forgetest.Op
	// listed are the branches the refusal's reason names, in no particular order.
	listed []string
	// req is the request Push runs; its zero value is a bare `ils push`.
	req landing.Request
	// answered is how many calls origin answers before it stops; zero is every call.
	answered int
	// fetches is whether git fetches from origin before the refusal.
	fetches bool
}

const (
	ownListed    = "own commits are listed here"
	noOwn        = "no commit of its own"
	changeOnSide = "change on " + side
	sharpen      = "sharpen the wording"
)

// nextEither is the Next line of the refusal of a branch that shares its first commit past the
// trunk with one other local branch, and nextEitherMany the one for several other local branches.
func nextEither() string {
	return expect("ils push", "the other branch and", "ils push")
}

func nextEitherMany() string {
	return expect("ils push", "the other branches and", "ils push")
}

// shares is the reason line of the refusal of the branch side, which shares its first commit past
// the trunk with others, and ends with rest.
func shares(others, rest string) string {
	return expect(side, "shares its first commit", others, rest)
}

// publishAtTrunk puts origin's copy of branch at the trunk.
func publishAtTrunk(f *gittest.Fixture, branch string) {
	f.Git("push", "--quiet", filepath.ToSlash(f.OriginDir()), trunk+":refs/heads/"+branch)
}

// cutRefusals are the refusals of a branch whose copy on origin, if any, does not carry its first
// commit past the trunk, decided from the one other local branch that carries that commit.
func cutRefusals() []pushRefusal {
	const other = "the branch " + topic

	return []pushRefusal{
		{
			name: "first commit past the trunk is on another branch", script: allowed(),
			ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				first := f.Sibling(side, topic)
				second := f.Commits(side, sharpen)[0]

				return expect(shortSHA(first), changeOnSide, shortSHA(second), sharpen) + shares(other, ownListed) + nextEither()
			},
		},
		{
			name: "cut from another branch with no commit of its own", script: allowed(),
			ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Git("switch", "--quiet", "--create", side, topic)

				return shares(other, noOwn) + nextEither()
			},
		},
		{
			name: "first commit past the trunk is on another branch and origin's copy is at the trunk", script: allowed(),
			ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				own := f.Sibling(side, topic)
				publishAtTrunk(f, side)

				return expect(shortSHA(own), changeOnSide) + shares(other, ownListed) + nextEither()
			},
		},
	}
}

// manyCutRefusals are the refusals of an unpublished branch whose first commit past the trunk
// several other local branches carry.
func manyCutRefusals() []pushRefusal {
	listed := []string{early, topic, yearly}

	return []pushRefusal{
		{
			// The second commit is on the last of the other branches alone, so it is not the branch's own.
			name: "first commit past the trunk is on three other branches", script: allowed(),
			ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Git("branch", early, topic)
				f.Sibling(side, topic)
				f.Git("branch", yearly, side)
				own := f.Commits(side, sharpen)[0]

				return expect(shortSHA(own), sharpen) + shares("branches", ownListed) + nextEitherMany()
			},
			listed: listed,
		},
		{
			name: "cut from three other branches with no commit of its own", script: allowed(),
			ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Git("branch", early, topic)
				f.Git("branch", yearly, topic)
				f.Git("switch", "--quiet", "--create", side, topic)

				return shares("branches", noOwn) + nextEitherMany()
			},
			listed: listed,
		},
	}
}

// pullFrom is the command that fast-forwards branch to origin's copy of it.
func pullFrom(branch string) string {
	return "git pull --ff-only origin " + branch
}

// behind is the refusal of branch, which is behind origin's copy of it.
func behind(branch string) string {
	return expect(branch, "is behind") + pushCommand().after("`"+pullFrom(branch)+"`")
}

// diverged is the refusal of branch, which has diverged from origin's copy of it.
func diverged(branch string) string {
	return expect(branch, "have diverged") + pushCommand().after("`"+originMerge+branch+"`")
}

// copyRefusals are the refusals decided from origin's copy of the branch.
func copyRefusals() []pushRefusal {
	return []pushRefusal{
		{
			name: "behind origin's copy", script: allowed(), ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				f.AdvanceOrigin(topic)

				return behind(topic)
			},
		},
		{
			name: "diverged from origin's copy", script: allowed(), ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				f.Diverge(topic)

				return diverged(topic)
			},
		},
	}
}

func TestPush_WhenOriginsCopyCarriesAFirstCommitAnotherBranchShares_PushesIt(t *testing.T) {
	t.Parallel()

	runPushes(t, []pushRow{{
		// Another branch was cut from the published branch at an earlier commit, and each has a
		// commit since.
		name: "published branch whose first commit past the trunk is on another branch", script: opening(passing()),
		want: greenAfter(pushedLine() + openedLine()),
		arrange: func(f *gittest.Fixture) {
			f.Commits(topic, "second step")
			f.Publish(topic)
			f.Git("branch", spare, topic+"~1")
			f.Commits(spare, "earlier work")
			f.Commits(topic, "fix a typo")
		},
	}})
}

func TestPush_WhenSharedPreconditionFails_ReturnsItsRefusal(t *testing.T) {
	t.Parallel()
	s := newScene(t, (*gittest.Fixture).RemoveOrigin, forgetest.Script{}, false)
	before := s.written()

	got := landing.Push(t.Context(), s.deps, pushRequest())

	assertEnded(t, s, got, finish{outcome: result.Refused, text: expect("no remote named origin")})
	assert.Check(t, is.Equal(s.written(), before))
	assert.Check(t, !s.gitRan("fetch"), "git fetched from origin before the refusal")
}

// runPushRefusals runs each row as a test of its own: Push refuses with the text the row's arrange
// returns, having written nothing and read no further than the row says.
func runPushRefusals(t *testing.T, rows []pushRefusal) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var want string

			s := newScene(t, func(f *gittest.Fixture) { want = tt.arrange(f) }, tt.script, false)
			if tt.answered > 0 {
				s.stopAnswering(t, tt.answered)
			}

			before := s.written()

			req := tt.req
			if req.Command == "" {
				req = pushRequest()
			}

			got := landing.Push(t.Context(), s.deps, req)

			assertEnded(t, s, got, finish{outcome: result.Refused, text: want, req: req})
			_, reason := shown(t, got, s.gitDir)
			for _, branch := range tt.listed {
				assert.Check(t, is.Contains(reason, branch))
			}
			assertOnlyRead(t, s, reads{before: before, ops: tt.ops, fetches: tt.fetches})
		})
	}
}

func TestPush_WhenBranchCannotBePushed_RefusesAtTheFirst(t *testing.T) {
	t.Parallel()

	runPushRefusals(t, slices.Concat(cutRefusals(), manyCutRefusals(), copyRefusals()))
}

// answering returns the fixture's environment with ssh replaced by a command that serves origin
// for the first times calls git makes to it, and then runs after instead. A read of ssh's config
// never reaches that command, so it takes none of those turns.
func answering(t *testing.T, f *gittest.Fixture, times int, after string) []string {
	t.Helper()

	tally := filepath.ToSlash(t.TempDir())

	var counted strings.Builder
	for i := range times {
		// mkdir succeeds once per name, so each call to origin takes the next free one.
		fmt.Fprintf(&counted, "mkdir '%s/%d' 2>/dev/null || ", tally, i)
	}

	serving := counted.String() + "{ " + after + "; exit 1; }; " + gittest.Serving(f.OriginDir())

	return f.ServedBy(serving)
}

// unanswered is a push that origin stops answering partway through.
type unanswered struct {
	arrange func(*gittest.Fixture)
	name    string
	// asked is what origin was asked to do when it stopped answering.
	asked  string
	script forgetest.Script
	ops    []forgetest.Op
	// answered is how many calls origin answers before it stops.
	answered int
}

// runUnanswered runs each row as a test of its own: Push refuses naming what origin was asked for
// when it stopped answering, having written nothing.
func runUnanswered(t *testing.T, rows []unanswered) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, tt.arrange, tt.script, false)
			s.stopAnswering(t, tt.answered)
			before := s.written()

			got := landing.Push(t.Context(), s.deps, pushRequest())

			assertEnded(t, s, got, finish{outcome: result.Refused, text: unreached(pushCommand(), tt.asked)})
			assertOnlyRead(t, s, reads{before: before, ops: tt.ops, fetches: true})
		})
	}
}

func TestPush_WhenOriginStopsAnswering_RefusesNamingWhatItWasAskedFor(t *testing.T) {
	t.Parallel()

	runUnanswered(t, []unanswered{
		{
			// Origin answers the question about its trunk and the fetch of it, and nothing after.
			name: "asked whether it has the branch", script: allowed(), ops: identityRead(), answered: 2,
			arrange: func(f *gittest.Fixture) { f.Publish(topic) },
			asked:   "ask whether it has the branch " + topic,
		},
		{
			name: "asked to fetch its copy of the branch", script: allowed(), ops: identityRead(), answered: 3,
			arrange: func(f *gittest.Fixture) { f.Publish(topic) },
			asked:   "fetch its copy of the branch " + topic,
		},
	})
}

// overtime is a push whose context ends during one of its reads.
type overtime struct {
	// arrange returns the context to run under, once it has changed how the scene reads.
	arrange func(t *testing.T, s *scene) context.Context
	// prepare changes the repository before the scene reads it.
	prepare func(*gittest.Fixture)
	name    string
	read    string
	script  forgetest.Script
	ops     []forgetest.Op
}

// runOvertime runs each row rows returns for the time limit as a test of its own: Push refuses
// naming the read the time limit ended, having written nothing. Push hands each cut read on to the
// shared wording, whose tests tell the time limit from an interrupt, so the time limit alone is
// enough here.
func runOvertime(t *testing.T, rows func(c cut) []overtime) {
	t.Helper()

	c := byTimeLimit()
	for _, tt := range rows(c) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, tt.prepare, tt.script, false)
			ctx := tt.arrange(t, s)
			before := s.written()

			got := landing.Push(ctx, s.deps, pushRequest())

			assertEnded(t, s, got, finish{outcome: result.Refused, text: overtimeText(pushCommand(), c, tt.read)})
			assertOnlyRead(t, s, reads{before: before, ops: tt.ops, fetches: true})
		})
	}
}

// failGit makes the scene's git calls whose arguments start with words fail as gittest.FakeGit
// makes them. It cannot run in a parallel test.
func failGit(t *testing.T, s *scene, words []string) {
	t.Helper()

	fakeGit(t, s, gittest.Fake{FailOn: words})
}

// fakeGit puts a git first on PATH that answers the scene's git calls as fake says; every call it
// does not fake runs the real git. It cannot run in a parallel test.
func fakeGit(t *testing.T, s *scene, fake gittest.Fake) {
	t.Helper()

	s.deps.Repo.Runner.Env = append(s.deps.Repo.Runner.Env, gittest.FakeGit(t, fake)...)
}

// assertFailedGit fails unless got is cmd's broke result on the read named read, carrying what the
// git call failGit made fail said.
func assertFailedGit(t *testing.T, got result.Result, cmd landingCommand, read string) {
	t.Helper()

	said := assertBrokeReadBy(t, got, cmd, read)
	assert.Check(t, is.Contains(said, gittest.FailedSaid))
}

// failedRead is one git read of Push that fails for a reason no precondition covers.
type failedRead struct {
	// arrange changes the repository and returns the arguments of the git call that is made to fail.
	arrange func(*gittest.Fixture) []string
	name    string
	read    string
	script  forgetest.Script
	// ops are the forge operations made before the read.
	ops []forgetest.Op
}

// history is the arguments of the git call that lists commits, before the revisions it walks.
func history() []string {
	return []string{"log", "--reverse", "--topo-order", "--format=%H %s", "--no-merges"}
}

// branchFailedReads are the failed reads of the other local branches.
func branchFailedReads() []failedRead {
	return []failedRead{
		{
			name: "the local branches that contain the first commit", read: readBranches,
			script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) []string {
				return []string{
					"for-each-ref", "--format=%(refname)", "refs/heads/", "--contains", f.Git("rev-parse", topic),
				}
			},
		},
		{
			name: "whether another branch carries a commit", read: readCommits, script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) []string {
				tip := f.Git("rev-parse", topic)
				f.Sibling(side, topic)
				f.Git("switch", "--quiet", topic)

				return append(history(), tip, "^refs/remotes/origin/"+trunk, "^refs/heads/"+side)
			},
		},
	}
}

// copyFailedReads are the failed reads of origin's copy of the branch, fetched before they run.
func copyFailedReads() []failedRead {
	return []failedRead{
		{
			name: "whether origin's copy carries the first commit", read: readCopy, script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) []string {
				f.Publish(topic)
				first := f.Git("rev-parse", topic)

				return []string{"merge-base", "--is-ancestor", first, first}
			},
		},
		{
			name: "the divergence from origin's copy", read: readCopy, script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) []string {
				f.Publish(topic)
				copied := f.Git("rev-parse", topic)
				tip := f.Commits(topic, "fix a typo")[0]

				return []string{"rev-list", "--left-right", "--count", tip + "..." + copied}
			},
		},
	}
}

// runFailedReads runs each row as a test of its own: Push ends broke on the row's read once the
// git call the row names fails, having written nothing. It cannot run in a parallel test.
func runFailedReads(t *testing.T, rows []failedRead) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			var words []string

			s := newScene(t, func(f *gittest.Fixture) { words = tt.arrange(f) }, tt.script, false)
			failGit(t, s, words)
			before := s.written()

			got := landing.Push(t.Context(), s.deps, pushRequest())

			assertBrokeReadBy(t, got, pushCommand(), tt.read)
			assertOnlyRead(t, s, reads{before: before, ops: tt.ops, fetches: true})
		})
	}
}

func TestPush_WhenGitReadFailsForNoPrecondition_BrokeNamingTheRead(t *testing.T) {
	// Not parallel: each row puts a failing git first on PATH with t.Setenv.
	runFailedReads(t, slices.Concat(branchFailedReads(), copyFailedReads()))
}
