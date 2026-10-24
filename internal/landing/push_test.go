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
	"github.com/jeffzi/ils/internal/result/resulttest"
)

const (
	spare     = "spare-parts"
	side      = "polish-docs"
	early     = "early-draft"
	yearly    = "yearly-notes"
	otherBase = "release"
	merged    = 7
	opened    = 12

	readCommits  = "the branch's commits"
	readOnOrigin = "whether origin has the branch"
	readCopy     = "origin's copy of the branch"
	readChanges  = "the branch's pull requests"
	readBranches = "the local branches"
)

// written returns everything a push could change: the local branches, where HEAD is, and every
// ref of origin.
func (s *scene) written() string {
	return s.fixture.Git("for-each-ref", "refs/heads") + "\nHEAD: " + s.fixture.Git("rev-parse", "HEAD") +
		"\norigin:\n" + s.fixture.OriginGit("for-each-ref")
}

// finding is allowed with a find-changes call that returns found.
func finding(found ...forge.Change) forgetest.Script {
	script := allowed()
	script.FindChanges = []forgetest.Answer[[]forge.Change]{{Value: found}}

	return script
}

// failingToFind is allowed with a find-changes call that returns err.
func failingToFind(err error) forgetest.Script {
	script := allowed()
	script.FindChanges = []forgetest.Answer[[]forge.Change]{{Err: err}}

	return script
}

// landedScript is finding with the merged pull request 7 against the trunk.
func landedScript() forgetest.Script {
	return finding(change(merged, trunk, forge.ChangeMerged))
}

// landAndPublish lands the topic branch as pull request 7 and publishes it.
func landAndPublish(f *gittest.Fixture) {
	f.LandPull(merged, topic)
	f.Publish(topic)
}

// stopAnswering makes origin answer the first times calls git makes to it and fail every call after.
func (s *scene) stopAnswering(t *testing.T, times int) {
	t.Helper()

	s.deps.Repo.Runner.Env = answering(t, s.fixture, times, "true")
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

// finish is how a result must have ended: its outcome and its whole text. A result of the push
// sequence is also held to its reason and to its payload, nil for none.
type finish struct {
	payload *result.Push
	text    string
	reason  string
	outcome result.Outcome
}

// assertEnded fails unless got ended as end says, carries the command and words of pushRequest,
// and renders as text.
func assertEnded(t *testing.T, s *scene, got result.Result, end finish) {
	t.Helper()

	assertStopped(t, got, end.outcome)

	rendered, _ := shown(t, got, s.gitDir)
	assert.Check(t, is.DeepEqual(rendered, end.text))
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
	// arrange changes the repository and returns the whole text of the refusal.
	arrange func(*gittest.Fixture) string
	name    string
	script  forgetest.Script
	// ops are the forge operations made before the refusal.
	ops []forgetest.Op
	// drawn are parts of the refusal and how a terminal with color draws them, with tipMark where
	// the sha of the topic branch's tip is printed.
	drawn []drawnPart
	// answered is how many calls origin answers before it stops; zero is every call.
	answered int
	// fetches is whether git fetches from origin before the refusal.
	fetches bool
}

const (
	// nextEither is the Next line of the refusal of a branch that shares its first commit past the
	// trunk with one other local branch.
	nextEither = "Next: run ils push on the other branch and get it merged first when this branch builds on it, " +
		"or remove the other branch when it was cut from this one, then run ils push again\n"
	// nextEitherMany is the Next line of the refusal of a branch that shares its first commit past
	// the trunk with several other local branches.
	nextEitherMany = "Next: run ils push on the other branches and get them merged first when this branch builds " +
		"on them, or remove the other branches when they were cut from this one, then run ils push again\n"
	ownListed    = "its own commits are listed here"
	noOwn        = "it has no commit of its own"
	changeOnSide = " change on " + side + "\n"
	sharpen      = "sharpen the wording"
)

// shares is the reason line of the refusal of the branch side, which shares its first commit past
// the trunk with others, and ends with rest.
func shares(others, rest string) string {
	return "Refused: the branch " + side + " shares its first commit past the trunk with " + others +
		"; ils cannot tell which was cut from which, and " + rest + "\n"
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

				return first + changeOnSide + second + " " + sharpen + "\n" +
					shares(other, ownListed) + nextEither
			},
		},
		{
			name: "cut from another branch with no commit of its own", script: allowed(),
			ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Git("switch", "--quiet", "--create", side, topic)

				return shares(other, noOwn) + nextEither
			},
		},
		{
			name: "first commit past the trunk is on another branch and origin's copy is at the trunk", script: allowed(),
			ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				own := f.Sibling(side, topic)
				publishAtTrunk(f, side)

				return own + changeOnSide + shares(other, ownListed) + nextEither
			},
		},
	}
}

// manyCutRefusals are the refusals of an unpublished branch whose first commit past the trunk
// several other local branches carry.
func manyCutRefusals() []pushRefusal {
	// In the order they were created the branches are neither sorted nor sorted backwards.
	const others = "the branches " + early + ", " + topic + " and " + yearly

	drawn := []drawnPart{
		{within: others, part: early, want: resulttest.NameLook()},
		{within: others, part: ", "},
		{within: others, part: topic, want: resulttest.NameLook()},
		{within: others, part: " and "},
		{within: others, part: yearly, want: resulttest.NameLook()},
	}

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

				return own + " " + sharpen + "\n" + shares(others, ownListed) + nextEitherMany
			},
			drawn: drawn,
		},
		{
			name: "cut from three other branches with no commit of its own", script: allowed(),
			ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Git("branch", early, topic)
				f.Git("branch", yearly, topic)
				f.Git("switch", "--quiet", "--create", side, topic)

				return shares(others, noOwn) + nextEitherMany
			},
			drawn: drawn,
		},
	}
}

func pullFrom(branch string) string {
	return "git pull --ff-only origin " + branch
}

// behind is the whole text of the refusal of branch, which is behind origin's copy of it.
func behind(branch string) string {
	return "Refused: the branch " + branch + " is behind origin's copy of it\n" +
		"Next: run `" + pullFrom(branch) + "`, then run ils push again\n"
}

// diverged is the whole text of the refusal of branch, which has diverged from origin's copy of it.
func diverged(branch string) string {
	return "Refused: the branch " + branch + " and origin's copy of it have diverged\n" +
		"Next: run `" + originMerge + branch + "`, then run ils push again\n"
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
			drawn: []drawnPart{commandIn(behind(topic), pullFrom(topic))},
		},
		{
			// The forge would answer find-changes, so the record of its calls is what shows it was not asked.
			name:   "behind origin's copy with an open pull request against another base",
			script: finding(change(opened, otherBase, forge.ChangeOpen)), ops: identityRead(), fetches: true,
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
			drawn: []drawnPart{commandIn(diverged(topic), originMerge+topic)},
		},
	}
}

// copyBeforeCutRefusals are the refusals decided from origin's copy of a branch whose first
// commit past the trunk another local branch also carries: origin's copy is read first.
func copyBeforeCutRefusals() []pushRefusal {
	return []pushRefusal{
		{
			// Origin's copy lacks the first commit past the trunk, so the other branch that carries it
			// would refuse the branch too.
			name:   "diverged from origin's copy at the trunk with its first commit past the trunk on another branch",
			script: allowed(), ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				publishAtTrunk(f, side)
				f.AdvanceOrigin(side)
				f.Sibling(side, topic)

				return diverged(side)
			},
		},
	}
}

// pushAccepted are the states of the branch that pass a check of Push another state fails, each
// pushed and opened as a first push that ends green.
func pushAccepted() []pushRow {
	opens := greenAfter(pushedLine + openedLine)

	return []pushRow{
		{
			name: "tip ahead of origin's copy", script: opening(passing()), want: opens,
			arrange: func(f *gittest.Fixture) {
				f.Publish(topic)
				f.Commits(topic, "fix a typo")
			},
		},
		{
			// Another branch was cut from the published branch at an earlier commit, and each has a
			// commit since.
			name: "published branch whose first commit past the trunk is on another branch", script: opening(passing()),
			want: opens,
			arrange: func(f *gittest.Fixture) {
				f.Commits(topic, "second step")
				f.Publish(topic)
				f.Git("branch", spare, topic+"~1")
				f.Commits(spare, "earlier work")
				f.Commits(topic, "fix a typo")
			},
		},
	}
}

func TestPush_WhenBranchPassesACheckAnotherStateFails_PushesIt(t *testing.T) {
	t.Parallel()

	runPushes(t, pushAccepted())
}

func TestPush_WhenSharedPreconditionFails_ReturnsItsRefusal(t *testing.T) {
	t.Parallel()
	s := newScene(t, (*gittest.Fixture).RemoveOrigin, forgetest.Script{}, false)
	before := s.written()

	got := landing.Push(t.Context(), s.deps, pushRequest())

	assertEnded(t, s, got, finish{outcome: result.Refused, text: noOriginText})
	assertOnlyRead(t, s, reads{before: before, ops: nil, fetches: false})
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

			got := landing.Push(t.Context(), s.deps, pushRequest())

			assertEnded(t, s, got, finish{outcome: result.Refused, text: want})
			assertOnlyRead(t, s, reads{before: before, ops: tt.ops, fetches: tt.fetches})
			assertDrawn(t, got, s.gitDir, atTip(tt.drawn, s.fixture.Git("rev-parse", topic)))
		})
	}
}

func TestPush_WhenBranchCannotBePushed_RefusesAtTheFirst(t *testing.T) {
	t.Parallel()

	runPushRefusals(t, slices.Concat(cutRefusals(), manyCutRefusals(), copyRefusals(), copyBeforeCutRefusals()))
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

// stalling returns an arrange under which origin answers the first answered calls git makes to it
// and the next reaches a server that never answers; the deadline ends once that call connected.
func stalling(answered int) func(t *testing.T, s *scene) context.Context {
	return func(t *testing.T, s *scene) context.Context {
		t.Helper()

		addr, connected := gittest.SilentListener(t)
		s.deps.Repo.Runner.Env = answering(t, s.fixture, answered, "git ls-remote git://"+addr+"/stalled.git")

		return gittest.Ending(t.Context(), connected, context.DeadlineExceeded)
	}
}

// overtime is a push whose deadline ends during one of its reads.
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

// runOvertime runs each row as a test of its own: Push refuses naming the read the deadline ended
// in, having written nothing.
func runOvertime(t *testing.T, rows []overtime) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, tt.prepare, tt.script, false)
			ctx := tt.arrange(t, s)
			before := s.written()

			got := landing.Push(ctx, s.deps, pushRequest())

			assertEnded(t, s, got, finish{outcome: result.Refused, text: overtimeText(pushCommand(), tt.read)})
			assertOnlyRead(t, s, reads{before: before, ops: tt.ops, fetches: true})
		})
	}
}

func TestPush_WhenDeadlineEndsDuringRead_RefusesNamingTheRead(t *testing.T) {
	t.Parallel()

	runOvertime(t, []overtime{
		{
			// Origin answers about its trunk and the fetch of it, and nothing after.
			name: "during the question whether origin has the branch", read: readOnOrigin,
			script: allowed(), ops: identityRead(), arrange: stalling(2),
		},
		{
			name: "during the fetch of origin's copy of the branch", read: readCopy,
			script: allowed(), ops: identityRead(), arrange: stalling(3),
			prepare: func(f *gittest.Fixture) { f.Publish(topic) },
		},
	})
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

			assertFailedGit(t, got, pushCommand(), tt.read)
			assertOnlyRead(t, s, reads{before: before, ops: tt.ops, fetches: true})
		})
	}
}

func TestPush_WhenGitReadFailsForNoPrecondition_BrokeNamingTheRead(t *testing.T) {
	// Not parallel: each row puts a failing git first on PATH with t.Setenv.
	runFailedReads(t, slices.Concat(branchFailedReads(), copyFailedReads()))
}
