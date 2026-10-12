package landing_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
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
	spare     = "spare-parts"
	side      = "polish-docs"
	early     = "early-draft"
	yearly    = "yearly-notes"
	otherBase = "release"
	merged    = 7
	opened    = 12

	nextBranch = "Next: start a branch from origin's trunk: `git fetch origin " + trunk +
		" && git switch -c <name> --no-track origin/" + trunk + "`"
	nextCommitFiles = "Next: commit them, then run ils push again\n"

	readTree         = "the working tree's changes"
	readFetchedTrunk = "the commits on origin's trunk"
	readCommits      = "the branch's commits"
	readOnOrigin     = "whether origin has the branch"
	readCopy         = "origin's copy of the branch"
	readChanges      = "the branch's pull requests"
	readMergedHead   = "the merged pull request's head"
	readTip          = "the branch's tip"
	readBranches     = "the local branches"
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
	// answered is how many calls origin answers before it stops; zero is every call.
	answered int
	// fetches is whether git fetches from origin before the refusal.
	fetches bool
}

// dirtyText is the refusal of a tree whose changed tracked files are files.
func dirtyText(files ...string) string {
	slices.Sort(files)

	return "Refused: tracked files have changes that are not committed: " + strings.Join(files, ", ") + "\n" +
		nextCommitFiles
}

// standingRefusals are the refusals decided from where HEAD stands and from the working tree,
// before anything is fetched.
func standingRefusals() []pushRefusal {
	const (
		onTrunk  = "Refused: HEAD is on the trunk " + trunk + ", which ils never pushes\n" + nextBranch + "\n"
		detached = "Refused: HEAD is detached, so there is no branch to push\n" + nextBranch + "\n"
	)

	return []pushRefusal{
		{
			name: "on the trunk", script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) string {
				f.Git("switch", "--quiet", trunk)

				return onTrunk
			},
		},
		{
			name: "on the trunk with a dirty tree", script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) string {
				f.Git("switch", "--quiet", trunk)
				f.DirtyFile()

				return onTrunk
			},
		},
		{
			name: "detached HEAD", script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) string {
				f.Detach()

				return detached
			},
		},
		{
			name: "tracked file changed and not staged", script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) string { return dirtyText(f.DirtyFile()) },
		},
		{
			name: "tracked file changed and staged", script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) string { return dirtyText(f.StagedFile()) },
		},
		{
			name: "two tracked files changed", script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) string { return dirtyText(f.DirtyFile(), f.StagedFile()) },
		},
		{
			name: "dirty tree on a branch with no commit past the trunk", script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) string {
				f.Git("switch", "--quiet", "--create", spare, trunk)

				return dirtyText(f.DirtyFile())
			},
		},
	}
}

// nothingPast is the refusal of a branch with no commit past origin's trunk.
func nothingPast(branch string) string {
	return "Refused: the branch " + branch + " has no commit past origin's trunk " + trunk + "\n" +
		"Next: commit the work, then run ils push again\n"
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

// cutRefusals are the refusals decided from the other local branches that carry the branch's
// first commit past the trunk.
func cutRefusals() []pushRefusal {
	cutFrom := func(parent string) string {
		return "Refused: the branch " + side + " was cut from the branch " + parent +
			" and not from the trunk; its own commits are listed here\n"
	}
	bringOver := func(shas ...string) string {
		return nextBranch + ", then bring this branch's own commits over with `git cherry-pick " +
			strings.Join(shas, " ") + "`\n"
	}

	return []pushRefusal{
		{
			name: "first commit past the trunk is on another branch", script: allowed(),
			ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				first := f.Sibling(side, topic)
				second := f.Commits(side, "sharpen the wording")[0]

				return cutFrom(topic) + first + " change on " + side + "\n" + second + " sharpen the wording\n" +
					bringOver(first, second)
			},
		},
		{
			// In the order they were created the branches are neither sorted nor sorted backwards.
			name: "first commit past the trunk is on three other branches", script: allowed(),
			ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Git("branch", early, topic)
				f.Git("branch", yearly, topic)
				own := f.Sibling(side, topic)

				return cutFrom(early) + own + " change on " + side + "\n" + bringOver(own)
			},
		},
		{
			name: "cut from another branch with no commit of its own", script: allowed(),
			ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Git("switch", "--quiet", "--create", side, topic)

				return "Refused: the branch " + side + " was cut from the branch " + topic +
					" and not from the trunk, and has no commit of its own\n" + nextBranch + "\n"
			},
		},
		{
			// Origin answers about its trunk and the fetch of it, and nothing after: asked whether it
			// has the branch, it would be what refuses.
			name: "cut from another branch and behind origin's copy", script: allowed(),
			ops: identityRead(), fetches: true, answered: 2,
			arrange: func(f *gittest.Fixture) string {
				own := f.Sibling(side, topic)
				f.Publish(side)
				f.AdvanceOrigin(side)

				return cutFrom(topic) + own + " change on " + side + "\n" + bringOver(own)
			},
		},
	}
}

// copyRefusals are the refusals decided from origin's copy of the branch.
func copyRefusals() []pushRefusal {
	const (
		behind = "Refused: the branch " + topic + " is behind origin's copy of it\n" +
			"Next: run `git pull --ff-only origin " + topic + "`, then run ils push again\n"
		diverged = "Refused: the branch " + topic + " and origin's copy of it have diverged\n" +
			"Next: run `git merge origin/" + topic + "`, then run ils push again\n"
	)

	return []pushRefusal{
		{
			name: "behind origin's copy", script: allowed(), ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				f.AdvanceOrigin(topic)

				return behind
			},
		},
		{
			// The forge would answer find-changes, so the record of its calls is what shows it was not asked.
			name:   "behind origin's copy with an open pull request against another base",
			script: finding(change(opened, otherBase, forge.ChangeOpen)), ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				f.AdvanceOrigin(topic)

				return behind
			},
		},
		{
			name: "diverged from origin's copy", script: allowed(), ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.Publish(topic)
				f.Diverge(topic)

				return diverged
			},
		},
	}
}

// changeRefusals are the refusals decided from the pull requests the forge finds for the branch.
func changeRefusals() []pushRefusal {
	number := strconv.Itoa(merged)

	const elsewhere = "Refused: pull request #12 from " + topic + " is open against " + otherBase +
		" and not against the trunk " + trunk + "\n" +
		"Next: close it or change its base with `gh pr edit 12 --base " + trunk +
		"`, then run ils push again\n"

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

				return "Refused: the branch " + topic + " already landed with pull request #" + number + "\n" +
					nextBranch + "\n"
			},
		},
		{
			name: "merged pull request with commits made since", script: landedScript(), ops: changesRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.LandPull(merged, topic)
				later := f.Commits(topic, "fix a typo", "add an example")

				return "Refused: pull request #" + number + " from " + topic +
					" was merged without the commits listed here\n" +
					later[0] + " fix a typo\n" + later[1] + " add an example\n" +
					nextBranch + ", then bring those commits over with `git cherry-pick " + later[0] + " " + later[1] +
					"`; expect a conflict where the squash touched the same files\n"
			},
		},
	}
}

func TestPush_WhenSharedPreconditionFails_ReturnsItsRefusal(t *testing.T) {
	t.Parallel()
	s := newScene(t, (*gittest.Fixture).RemoveOrigin, forgetest.Script{}, false)
	before := s.written()

	got := landing.Push(t.Context(), s.deps, pushRequest())

	assertEnded(t, s, got, finish{outcome: result.Refused, text: noOriginText})
	assertOnlyRead(t, s, reads{before: before, ops: nil, fetches: false})
}

func TestPush_WhenBranchCannotBePushed_RefusesAtTheFirst(t *testing.T) {
	t.Parallel()

	refusals := slices.Concat(standingRefusals(), commitRefusals(), cutRefusals(), copyRefusals(), changeRefusals())
	for _, tt := range refusals {
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
		})
	}
}

// answering returns the fixture's environment with ssh replaced by a command that serves origin
// for the first times calls git makes to it, and then runs after instead.
func answering(t *testing.T, f *gittest.Fixture, times int, after string) []string {
	t.Helper()

	tally := filepath.ToSlash(t.TempDir())

	var counted strings.Builder
	for i := range times {
		// mkdir succeeds once per name, so each call to origin takes the next free one.
		fmt.Fprintf(&counted, "mkdir '%s/%d' 2>/dev/null || ", tally, i)
	}

	return append(f.Env(),
		"GIT_SSH_VARIANT=simple",
		"GIT_SSH_COMMAND="+counted.String()+"{ "+after+"; exit 1; }; "+gittest.Serving(f.OriginDir()),
	)
}

func TestPush_WhenOriginStopsAnswering_RefusesNamingWhatItWasAskedFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arrange func(*gittest.Fixture)
		name    string
		// want is the reason of the refusal.
		want   string
		script forgetest.Script
		ops    []forgetest.Op
		// answered is how many calls origin answers before it stops.
		answered int
	}{
		{
			// Origin answers the question about its trunk, and nothing after.
			name: "asked to fetch its trunk", script: allowed(), ops: identityRead(), answered: 1,
			want: "origin could not be reached to fetch its trunk " + trunk,
		},
		{
			// Origin answers the question about its trunk and the fetch of it, and nothing after.
			name: "asked whether it has the branch", script: allowed(), ops: identityRead(), answered: 2,
			arrange: func(f *gittest.Fixture) { f.Publish(topic) },
			want:    "origin could not be reached to ask whether it has the branch " + topic,
		},
		{
			name: "asked to fetch its copy of the branch", script: allowed(), ops: identityRead(), answered: 3,
			arrange: func(f *gittest.Fixture) { f.Publish(topic) },
			want:    "origin could not be reached to fetch its copy of the branch " + topic,
		},
		{
			name:   "asked to fetch the merged pull request's head",
			script: landedScript(), ops: changesRead(), answered: 4,
			arrange: landAndPublish,
			want:    "origin could not be reached to fetch the head of pull request #" + strconv.Itoa(merged),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, tt.arrange, tt.script, false)
			s.stopAnswering(t, tt.answered)
			before := s.written()

			got := landing.Push(t.Context(), s.deps, pushRequest())

			want := "Refused: " + tt.want + "\n" + nextPush
			assertEnded(t, s, got, finish{outcome: result.Refused, text: want})
			assertOnlyRead(t, s, reads{before: before, ops: tt.ops, fetches: true})
		})
	}
}

// spoil overwrites the file at the slash-separated path under the fixture's git directory with
// bytes that git cannot parse.
func spoil(t *testing.T, f *gittest.Fixture, path string) {
	t.Helper()

	garbage := []byte(strings.Repeat("not what git wrote here\n", 4))

	err := os.WriteFile(filepath.Join(f.Dir(), ".git", filepath.FromSlash(path)), garbage, 0o600)
	assert.NilError(t, err)
}

// lose deletes the commit sha from the working repository's object store, where the fixture keeps
// every object in a file of its own.
func lose(t *testing.T, f *gittest.Fixture, sha string) {
	t.Helper()

	err := os.Remove(filepath.Join(f.Dir(), ".git", "objects", sha[:2], sha[2:]))
	assert.NilError(t, err)
}

// brokenRead is one local read of Push that the repository's own state breaks.
type brokenRead struct {
	// arrange breaks the repository and returns the git command that failed with its exit status,
	// as the result prints them ahead of what git said.
	arrange func(t *testing.T, f *gittest.Fixture) string
	name    string
	read    string
	// fetches is whether git fetches from origin before the read.
	fetches bool
}

func brokenReads() []brokenRead {
	return []brokenRead{
		{
			name: "the index is corrupt", read: readTree,
			arrange: func(t *testing.T, f *gittest.Fixture) string {
				t.Helper()
				spoil(t, f, "index")

				return "git status --porcelain=v1 -z --untracked-files=no: exit status 128: "
			},
		},
		{
			name: "a commit of the branch is missing", read: readCommits, fetches: true,
			arrange: func(t *testing.T, f *gittest.Fixture) string {
				t.Helper()

				first := f.Git("rev-parse", topic)
				tip := f.Commits(topic, "fix a typo")[0]
				lose(t, f, first)

				return "git log --reverse --topo-order --format=%H %s --no-merges " + tip + " ^origin/" + trunk +
					": exit status 128: "
			},
		},
		{
			name: "a commit of another branch is missing", read: readCommits, fetches: true,
			arrange: func(t *testing.T, f *gittest.Fixture) string {
				t.Helper()

				middle := f.Sibling(side, topic)
				f.Commits(side, "sharpen the wording")
				f.Git("switch", "--quiet", topic)
				lose(t, f, middle)

				return "git merge-base --is-ancestor " + f.Git("rev-parse", topic) + " " + side + ": exit status 128: "
			},
		},
	}
}

func TestPush_WhenLocalReadFails_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()

	for _, tt := range brokenReads() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var failed string

			s := newScene(t, func(f *gittest.Fixture) { failed = tt.arrange(t, f) }, allowed(), false)
			before := s.written()

			got := landing.Push(t.Context(), s.deps, pushRequest())

			assertBrokeRead(t, got, tt.read, failed)
			// What git said must also render under the repository's own git dir: no line names a path in it.
			shown(t, got, s.gitDir)
			assertOnlyRead(t, s, reads{before: before, ops: identityRead(), fetches: tt.fetches})
		})
	}
}

func TestPush_WhenFindChangesFailsUntyped_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, failingToFind(errors.New("HTTP 502")), false)
	before := s.written()

	got := landing.Push(t.Context(), s.deps, pushRequest())

	want := "Could not read " + readChanges + ": HTTP 502\nNothing was written.\n" + nextFixed
	assertEnded(t, s, got, finish{outcome: result.Broke, text: want})
	assertOnlyRead(t, s, reads{before: before, ops: changesRead(), fetches: true})
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

func TestPush_WhenDeadlineEndsDuringRead_RefusesNamingTheRead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		// arrange returns the context to run under, once it has changed how the scene reads.
		arrange func(t *testing.T, s *scene) context.Context
		// prepare changes the repository before the scene reads it.
		prepare func(*gittest.Fixture)
		name    string
		read    string
		script  forgetest.Script
		ops     []forgetest.Op
	}{
		{
			// Origin answers the question about its trunk, and nothing after.
			name: "during the fetch of origin's trunk", read: readFetchedTrunk, script: allowed(), ops: identityRead(),
			arrange: stalling(1),
		},
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
		{
			name: "during the fetch of the merged pull request's head", read: readMergedHead,
			script: landedScript(), ops: changesRead(), arrange: stalling(4),
			prepare: landAndPublish,
		},
		{
			name: "during the find-changes read", read: readChanges, ops: changesRead(),
			script: failingToFind(fmt.Errorf("gh pr list: %w", context.DeadlineExceeded)),
			arrange: func(t *testing.T, _ *scene) context.Context {
				t.Helper()

				return t.Context()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, tt.prepare, tt.script, false)
			ctx := tt.arrange(t, s)
			before := s.written()

			got := landing.Push(ctx, s.deps, pushRequest())

			want := "Refused: the time limit ended while reading " + tt.read + "; nothing was written\n" + nextPush
			assertEnded(t, s, got, finish{outcome: result.Refused, text: want})
			assertOnlyRead(t, s, reads{before: before, ops: tt.ops, fetches: true})
		})
	}
}

func TestPush_WhenGitGoesMissingAfterThePreconditions_RefusesWithAnInstallHint(t *testing.T) {
	// Not parallel: the forge's hook empties PATH for the whole process.
	s := newScene(t, landAndPublish, forgetest.Script{}, false)
	before, path := s.written(), os.Getenv("PATH")
	// Git is hidden once the pull requests are asked for, so the fetch of the merged head finds none.
	script := hidingGitAt(t, landedScript(), forgetest.OpFindChanges)
	s.useForge(t, script)

	got := landing.Push(t.Context(), s.deps, pushRequest())

	assertEnded(t, s, got, finish{outcome: result.Refused, text: noGit})
	assertAsked(t, s, changesRead())

	// The refs are read with git back on PATH.
	t.Setenv("PATH", path)
	assert.Check(t, is.Equal(s.written(), before))
}

func TestPush_WhenBranchHasNoCommitAtAll_RefusesAsNoCommitPastTheTrunk(t *testing.T) {
	t.Parallel()
	s := newScene(t, func(f *gittest.Fixture) { f.Git("switch", "--quiet", "--orphan", spare) }, allowed(), false)
	// A branch with no commit has no HEAD commit to read, so the snapshot names the branch instead.
	standing := func() string { return s.refs() + "\nHEAD: " + s.fixture.Git("symbolic-ref", "HEAD") }
	before := standing()

	got := landing.Push(t.Context(), s.deps, pushRequest())

	assertEnded(t, s, got, finish{outcome: result.Refused, text: nothingPast(spare)})

	assert.Check(t, is.Equal(standing(), before))

	assertAsked(t, s, identityRead())
}

func TestPush_WhenBranchRefCannotBeRead_BrokeNamingTheTipRead(t *testing.T) {
	// Not parallel: failGit puts a failing git first on PATH with t.Setenv.
	s := newScene(t, func(f *gittest.Fixture) { f.Git("switch", "--quiet", "--orphan", spare) }, allowed(), false)
	words := []string{"show-ref", "--verify", "--quiet", "refs/heads/" + spare}
	failGit(t, s, words)

	before := s.refs()

	got := landing.Push(t.Context(), s.deps, pushRequest())

	assertEnded(t, s, got, finish{outcome: result.Broke, text: failedGitText(readTip, words)})

	assert.Check(t, is.Equal(s.refs(), before))

	assertAsked(t, s, identityRead())
}

// failGit makes the scene's next git call whose arguments start with words fail as failingGit does,
// by putting a copy of the test binary named git first on PATH. It cannot run in a parallel test.
func failGit(t *testing.T, s *scene, words []string) {
	t.Helper()

	realGit, err := exec.LookPath("git")
	assert.NilError(t, err)

	self, err := os.Executable()
	assert.NilError(t, err)

	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}

	dir := t.TempDir()
	err = os.Link(self, filepath.Join(dir, name))
	assert.NilError(t, err)

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s.deps.Repo.Runner.Env = append(s.deps.Repo.Runner.Env,
		failingWordsEnv+"="+strings.Join(words, failingWordsSep),
		realGitEnv+"="+realGit,
		// The race detector's exit pause would otherwise cost a second per git call.
		"GORACE=atexit_sleep_ms=0",
	)
}

// failedGitText is the whole text of the result for the read named read when the git call with
// the arguments words fails as failGit makes it.
func failedGitText(read string, words []string) string {
	return "Could not read " + read + ": git " + strings.Join(words, " ") + ": exit status " +
		strconv.Itoa(failingGitExit) + ": " + failingGitSaid + "\nNothing was written.\n" + nextFixed
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

func failedReads() []failedRead {
	// since lands the branch as the merged pull request, adds a commit, and returns the pull
	// request's head and the branch's tip.
	since := func(f *gittest.Fixture) (head, tip string) {
		head = f.Git("rev-parse", topic)
		f.LandPull(merged, topic)

		return head, f.Commits(topic, "fix a typo")[0]
	}

	return []failedRead{
		{
			name: "the branch's tip", read: readTip, script: allowed(), ops: identityRead(),
			arrange: func(*gittest.Fixture) []string { return []string{"rev-parse", "HEAD"} },
		},
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
			name: "the divergence from origin's copy", read: readCopy, script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) []string {
				f.Publish(topic)
				copied := f.Git("rev-parse", topic)
				tip := f.Commits(topic, "fix a typo")[0]

				return []string{"rev-list", "--left-right", "--count", tip + "..." + copied}
			},
		},
		{
			name: "the merged head's commits past the trunk", read: readMergedHead, script: landedScript(), ops: changesRead(),
			arrange: func(f *gittest.Fixture) []string {
				head, _ := since(f)

				return append(history(), head, "^origin/"+trunk)
			},
		},
		{
			name: "the unlanded commits", read: readCommits, script: landedScript(), ops: changesRead(),
			arrange: func(f *gittest.Fixture) []string {
				head, tip := since(f)

				return append(history(), "--cherry-pick", "--right-only", head+"..."+tip, "^origin/"+trunk)
			},
		},
	}
}

func TestPush_WhenGitReadFailsForNoPrecondition_BrokeNamingTheRead(t *testing.T) {
	// Not parallel: each row puts a failing git first on PATH with t.Setenv.
	for _, tt := range failedReads() {
		t.Run(tt.name, func(t *testing.T) {
			var words []string

			s := newScene(t, func(f *gittest.Fixture) { words = tt.arrange(f) }, tt.script, false)
			failGit(t, s, words)

			before := s.written()

			got := landing.Push(t.Context(), s.deps, pushRequest())

			assertEnded(t, s, got, finish{outcome: result.Broke, text: failedGitText(tt.read, words)})
			assertOnlyRead(t, s, reads{before: before, ops: tt.ops, fetches: true})
		})
	}
}
