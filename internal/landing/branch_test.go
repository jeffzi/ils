package landing_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

// ---------------------------------------------------------------------------
// Checks of the branch
// ---------------------------------------------------------------------------

const (
	startCommand = "ils branch <name>"
	baseCommand  = "gh pr edit 12 --base " + trunk
	otherBase    = "release"

	readTree         = "the working tree's changes"
	readFetchedTrunk = "the commits on origin's trunk"
	readMergedHead   = "the merged pull request's head"
	readTip          = "the branch's tip"
	readChanges      = "the branch's pull requests"
)

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

// nextBranch is the `Next:` line that starts a branch from origin's trunk.
func nextBranch() string { return expect("`" + startCommand + "`") }

// keepDetached is the `Next:` line that keeps a detached HEAD's commits on a new branch.
func keepDetached() string { return expect("`git switch -c <name>`", "ils push") }

func onTrunk() string {
	return expect("on the trunk "+trunk) + nextBranch()
}

func detached() string {
	return expect("detached") + nextBranch()
}

// dirtyText is cmd's refusal of a tree with uncommitted tracked files, which it names in sorted
// order.
func dirtyText(cmd landingCommand, files ...string) string {
	slices.Sort(files)

	return expect("not committed", strings.Join(files, ", ")) + cmd.after("commit them")
}

// elsewhereText is cmd's refusal of a branch whose open pull request 12 is against otherBase.
func elsewhereText(cmd landingCommand) string {
	return expect("#12", topic, otherBase, trunk) + cmd.after("`"+baseCommand+"`")
}

// standingRefusals are the refusals decided from where HEAD stands and from the working tree,
// before anything is fetched.
func standingRefusals() []pushRefusal {
	return []pushRefusal{
		{
			name: "on the trunk", script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) string {
				f.Git("switch", "--quiet", trunk)

				return onTrunk()
			},
		},
		{
			name: "detached HEAD at origin's trunk", script: allowed(), ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Git("switch", "--quiet", "--detach", trunk)

				return detached()
			},
		},
		{
			name: "detached HEAD with commits origin's trunk lacks", script: allowed(), ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Detach()

				return expect(shortSHA(f.Git("rev-parse", "HEAD")), "detached") + keepDetached()
			},
		},
		{
			name: "two tracked files changed", script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) string { return dirtyText(pushCommand(), f.DirtyFile(), f.StagedFile()) },
		},
		{
			name: "two tracked files changed, under the long wait", script: allowed(), ops: identityRead(),
			req:     waitRequest(),
			arrange: func(f *gittest.Fixture) string { return dirtyText(waitCommand(), f.DirtyFile(), f.StagedFile()) },
		},
	}
}

// nothingPast is the refusal of a branch with no commit past origin's trunk, whose `Next:` line
// runs `ils push` again.
func nothingPast(branch string) string {
	return expect(branch, "no commit past", trunk, "commit the work", "ils push again\n")
}

// commitRefusals are the refusals decided from the branch's commits past origin's trunk.
func commitRefusals() []pushRefusal {
	return []pushRefusal{
		{
			name: "no commit past the trunk", script: allowed(), ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Git("switch", "--quiet", "--create", spare, trunk)

				return nothingPast(spare)
			},
		},
		{
			name: "every commit landed on origin's trunk since the last fetch", script: allowed(),
			ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Git("push", "--quiet", filepath.ToSlash(f.OriginDir()), topic+":"+trunk)

				return nothingPast(topic)
			},
		},
	}
}

// changeRefusals are the refusals decided from the pull requests the forge finds for the branch.
func changeRefusals() []pushRefusal {
	number := strconv.Itoa(merged)

	elsewhere := elsewhereText(pushCommand())

	return []pushRefusal{
		{
			name:   "open pull request against another base",
			script: finding(change(opened, otherBase, forge.ChangeOpen)), ops: changesRead(), fetches: true,
			arrange: func(*gittest.Fixture) string { return elsewhere },
		},
		{
			// Origin answers about its trunk, the fetch of it, and whether it has the branch, and
			// nothing after: a fetch of the merged head would be what refuses.
			name:   "open pull request against another base beside a merged one that shares a commit",
			script: finding(change(merged, trunk, forge.ChangeMerged), change(opened, otherBase, forge.ChangeOpen)),
			ops:    changesRead(), fetches: true, answered: 3,
			arrange: func(f *gittest.Fixture) string {
				f.LandPull(merged, topic)

				return elsewhere
			},
		},
		{
			name: "merged pull request with nothing unlanded", script: landedScript(), ops: changesRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.LandPull(merged, topic)

				return expect(topic, "already landed", "#"+number) + nextBranch()
			},
		},
		{
			name: "merged pull request with commits made since", script: landedScript(), ops: changesRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.LandPull(merged, topic)
				later := f.Commits(topic, "fix a typo", "add an example")

				return expect(shortSHA(later[0]), "fix a typo", shortSHA(later[1]), "add an example", "#"+number, topic, "merged without the commits") +
					nextBranch() + expect("`git cherry-pick "+later[0]+" "+later[1]+"`")
			},
		},
	}
}

func TestPush_WhenASharedCheckOfTheBranchFails_RefusesAtTheFirst(t *testing.T) {
	t.Parallel()

	runPushRefusals(t, slices.Concat(standingRefusals(), commitRefusals(), changeRefusals()))
}

// sharedAccepted are the states of the branch that pass a shared check another state fails, each
// pushed and opened as a first push that ends green.
func sharedAccepted() []pushRow {
	opens := greenAfter(pushedLine() + openedLine())
	sinceMerged := opening(passing()).findingOnly(change(merged, trunk, forge.ChangeMerged))

	return []pushRow{
		{
			name: "merged pull request sharing no commit past the trunk", script: sinceMerged, want: opens,
			arrange: func(f *gittest.Fixture) {
				f.Commits(spare, "earlier work")
				f.LandPull(merged, spare)
				f.Git("switch", "--quiet", topic)
			},
		},
	}
}

func TestPush_WhenBranchPassesASharedCheckAnotherStateFails_PushesIt(t *testing.T) {
	t.Parallel()

	runPushes(t, sharedAccepted())
}

func TestPush_WhenOriginStopsAnsweringASharedRead_RefusesNamingWhatItWasAskedFor(t *testing.T) {
	t.Parallel()

	runUnanswered(t, []unanswered{
		{
			// Origin answers the question about its trunk, and nothing after.
			name: "asked to fetch its trunk", script: allowed(), ops: identityRead(), answered: 1,
			asked: "fetch its trunk " + trunk,
		},
	})
}

func TestPush_WhenASharedReadIsCutShort_RefusesNamingTheRead(t *testing.T) {
	t.Parallel()

	runOvertime(t, func(c cut) []overtime {
		return []overtime{
			{
				name: "during the fetch of the merged pull request's head", read: readMergedHead,
				script: landedScript(), ops: changesRead(), arrange: c.stalling(4),
				prepare: landAndPublish,
			},
		}
	})
}

// ---------------------------------------------------------------------------
// Failed local reads
// ---------------------------------------------------------------------------

// lose deletes the commit sha from the working repository's object store, where the fixture keeps
// every object in a file of its own.
func lose(t *testing.T, f *gittest.Fixture, sha string) {
	t.Helper()

	err := os.Remove(filepath.Join(f.Dir(), ".git", "objects", sha[:2], sha[2:]))
	assert.NilError(t, err)
}

// brokenRead is one local read of Push that the repository's own state breaks.
type brokenRead struct {
	// arrange breaks the repository.
	arrange func(t *testing.T, f *gittest.Fixture)
	name    string
	read    string
	// fetches is whether git fetches from origin before the read.
	fetches bool
}

func brokenReads() []brokenRead {
	return []brokenRead{
		{
			name: "the index is corrupt", read: readTree,
			arrange: func(t *testing.T, f *gittest.Fixture) {
				t.Helper()
				f.CorruptIndex()
			},
		},
		{
			name: "a commit of the branch is missing", read: readCommits, fetches: true,
			arrange: func(t *testing.T, f *gittest.Fixture) {
				t.Helper()

				first := f.Git("rev-parse", topic)
				f.Commits(topic, "fix a typo")
				lose(t, f, first)
			},
		},
	}
}

func TestPush_WhenLocalReadFails_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()

	for _, tt := range brokenReads() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newScene(t, func(f *gittest.Fixture) { tt.arrange(t, f) }, allowed(), false)
			before := s.written()

			got := landing.Push(t.Context(), s.deps, pushRequest())

			assertBrokeReadBy(t, got, pushCommand(), tt.read)
			assertOnlyRead(t, s, reads{before: before, ops: identityRead(), fetches: tt.fetches})
		})
	}
}

func TestPush_WhenFindChangesFailsUntyped_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, failingToFind(errors.New(forgeFailure)), false)
	before := s.written()

	got := landing.Push(t.Context(), s.deps, pushRequest())

	want := unreadText(pushCommand(), readChanges, forgeFailure)
	assertEnded(t, s, got, finish{outcome: result.Broke, text: want})
	assertOnlyRead(t, s, reads{before: before, ops: changesRead(), fetches: true})
}

func TestPush_WhenBranchHasNoCommitAtAll_RefusesAsNoCommitPastTheTrunk(t *testing.T) {
	t.Parallel()
	s := newScene(t, func(f *gittest.Fixture) { f.Git("switch", "--quiet", "--orphan", spare) }, allowed(), false)

	got := landing.Push(t.Context(), s.deps, pushRequest())

	assertEnded(t, s, got, finish{outcome: result.Refused, text: nothingPast(spare)})

	assertAsked(t, s, identityRead())
}

func TestPush_WhenBranchRefCannotBeRead_BrokeNamingTheTipRead(t *testing.T) {
	// Not parallel: failGit puts a failing git first on PATH with t.Setenv.
	words := []string{"show-ref", "--verify", "--quiet", "refs/heads/" + spare}
	s := newScene(t, func(f *gittest.Fixture) { f.Git("switch", "--quiet", "--orphan", spare) }, allowed(), false)
	failGit(t, s, words)

	got := landing.Push(t.Context(), s.deps, pushRequest())

	assertFailedGit(t, got, pushCommand(), readTip)

	assertAsked(t, s, identityRead())
}

// sharedFailedReads are the failed reads of the commits on origin's trunk, of the branch's tip, and
// of the commits a merged pull request from the branch already carries.
func sharedFailedReads() []failedRead {
	// since lands the branch as the merged pull request, adds a commit, and returns the pull
	// request's head and the branch's tip.
	since := func(f *gittest.Fixture) (head, tip string) {
		head = f.Git("rev-parse", topic)
		f.LandPull(merged, topic)

		return head, f.Commits(topic, "fix a typo")[0]
	}

	return []failedRead{
		{
			name: "the commits on origin's trunk", read: readFetchedTrunk, script: allowed(), ops: identityRead(),
			arrange: func(*gittest.Fixture) []string {
				return []string{"rev-parse", "--verify", "refs/ils/fetched/heads/" + trunk}
			},
		},
		{
			name: "the branch's tip", read: readTip, script: allowed(), ops: identityRead(),
			arrange: func(*gittest.Fixture) []string { return []string{"rev-parse", "HEAD"} },
		},
		{
			name: "the merged head's commits past the trunk", read: readMergedHead, script: landedScript(), ops: changesRead(),
			arrange: func(f *gittest.Fixture) []string {
				head, _ := since(f)

				return append(history(), head, "^refs/remotes/origin/"+trunk)
			},
		},
		{
			name: "the unlanded commits", read: readCommits, script: landedScript(), ops: changesRead(),
			arrange: func(f *gittest.Fixture) []string {
				head, tip := since(f)

				return append(history(), "--cherry-pick", "--right-only", head+"..."+tip, "^refs/remotes/origin/"+trunk)
			},
		},
	}
}

func TestPush_WhenASharedGitReadFails_BrokeNamingTheRead(t *testing.T) {
	// Not parallel: each row puts a failing git first on PATH with t.Setenv.
	runFailedReads(t, sharedFailedReads())
}
