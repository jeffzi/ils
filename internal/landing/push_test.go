package landing_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/git"
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

	exitBroke   = 1
	exitRefused = 2

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

// fetched reports whether a git call fetched from origin.
func (s *scene) fetched() bool {
	return slices.ContainsFunc(s.recorder.Calls(), func(argv []string) bool {
		return slices.Contains(argv, "fetch")
	})
}

// publish puts origin's copy of branch at the local tip, by origin's path: the scene's origin
// URLs name GitHub, and only the scene's own runner reaches the local origin through them.
func publish(f *gittest.Fixture, branch string) {
	f.Git("push", "--quiet", filepath.ToSlash(f.OriginDir()), branch)
}

// allowed is a script whose identity call says the user can push to Acme/Widgets.
func allowed() forgetest.Script {
	return answers(widgets(false, true))
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

// change returns the pull request numbered number from the topic branch against base.
func change(number int, base string, state forge.ChangeState) forge.Change {
	return forge.Change{Number: number, Base: base, State: state, Title: topic}
}

// identityRead and changesRead are the forge operations Push makes up to each of its two reads.
func identityRead() []forgetest.Op { return []forgetest.Op{forgetest.OpIdentity} }

func changesRead() []forgetest.Op {
	return []forgetest.Op{forgetest.OpIdentity, forgetest.OpFindChanges}
}

// finish is how a result must have ended: its outcome, its exit code, and its whole text. A result
// of the push sequence is also held to its reason and to its payload, nil for none.
type finish struct {
	payload *result.Push
	text    string
	reason  string
	outcome result.Outcome
	exit    int
}

// assertEnded fails unless got ended as want with the exit code exit, carries the command and
// words of pushRequest, and renders as text.
func assertEnded(t *testing.T, s *scene, got result.Result, end finish) {
	t.Helper()

	want, exit, text := end.outcome, end.exit, end.text

	if got.Outcome != want || got.Outcome.ExitCode() != exit {
		t.Errorf("Push() outcome = %s (exit %d), want %s (exit %d)", got.Outcome, got.Outcome.ExitCode(), want, exit)
	}

	if got.Command != "push" {
		t.Errorf("Push() result command = %q, want %q", got.Command, "push")
	}

	if diff := cmp.Diff([]string{"push"}, got.Words); diff != "" {
		t.Errorf("Push() result words mismatch (-want +got):\n%s", diff)
	}

	rendered, _ := shown(t, got, s.gitDir)
	if diff := cmp.Diff(text, rendered); diff != "" {
		t.Errorf("RenderText() mismatch (-want +got):\n%s", diff)
	}
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

	before, ops, fetches := read.before, read.ops, read.fetches

	if after := s.written(); after != before {
		t.Errorf("refs after Push() = %q, want them unchanged from %q", after, before)
	}

	assertAsked(t, s, ops)

	if fetched := s.fetched(); fetched != fetches {
		t.Errorf("git fetched from origin = %t, want %t", fetched, fetches)
	}
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

// commitRefusals are the refusals decided from the branch's commits past origin's trunk.
func commitRefusals() []pushRefusal {
	nothingPast := func(branch string) string {
		return "Refused: the branch " + branch + " has no commit past origin's trunk " + trunk + "\n" +
			"Next: commit the work, then run ils push again\n"
	}

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

	return []pushRefusal{
		{
			name: "first commit past the trunk is on another branch", script: allowed(),
			ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				first := f.Sibling(side, topic)
				second := f.Commits(side, "sharpen the wording")[0]

				return cutFrom(topic) + first + " change on " + side + "\n" + second + " sharpen the wording\n" +
					nextBranch + ", then bring this branch's own commits over with `git cherry-pick " +
					first + " " + second + "`\n"
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

				return cutFrom(early) + own + " change on " + side + "\n" +
					nextBranch + ", then bring this branch's own commits over with `git cherry-pick " + own + "`\n"
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
				publish(f, side)
				f.AdvanceOrigin(side)

				return cutFrom(topic) + own + " change on " + side + "\n" +
					nextBranch + ", then bring this branch's own commits over with `git cherry-pick " + own + "`\n"
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
				publish(f, topic)
				f.AdvanceOrigin(topic)

				return behind
			},
		},
		{
			// The forge would answer find-changes, so the record of its calls is what shows it was not asked.
			name:   "behind origin's copy with an open pull request against another base",
			script: finding(change(opened, otherBase, forge.ChangeOpen)), ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				publish(f, topic)
				f.AdvanceOrigin(topic)

				return behind
			},
		},
		{
			name: "diverged from origin's copy", script: allowed(), ops: identityRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				publish(f, topic)
				f.Diverge(topic)

				return diverged
			},
		},
	}
}

// changeRefusals are the refusals decided from the pull requests the forge finds for the branch.
func changeRefusals() []pushRefusal {
	number := strconv.Itoa(merged)
	landed := finding(change(merged, trunk, forge.ChangeMerged))

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
				squash(f)

				return elsewhere
			},
		},
		{
			name: "merged pull request with nothing unlanded", script: landed, ops: changesRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				squash(f)

				return "Refused: the branch " + topic + " already landed with pull request #" + number + "\n" +
					nextBranch + "\n"
			},
		},
		{
			name: "merged pull request with commits made since", script: landed, ops: changesRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				squash(f)
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

// squash lands the topic branch's tip on origin's trunk as the merged pull request's squash, and
// keeps the tip as that pull request's head.
func squash(f *gittest.Fixture) {
	tip := f.Git("rev-parse", topic)

	f.PullRef(merged, tip)
	f.SquashOnOrigin(tip, "tidy the readme (#"+strconv.Itoa(merged)+")")
}

func TestPush_WhenSharedPreconditionFails_ReturnsItsRefusal(t *testing.T) {
	t.Parallel()
	s := newScene(t, (*gittest.Fixture).RemoveOrigin, forgetest.Script{}, false)
	before := s.written()

	got := landing.Push(t.Context(), s.deps, pushRequest())

	want := "Refused: this repository has no remote named origin\n" +
		"Next: create the repository on GitHub and push to it with `gh repo create --source . --push`\n"
	assertEnded(t, s, got, finish{outcome: result.Refused, exit: exitRefused, text: want})
	assertOnlyRead(t, s, reads{before: before, ops: nil, fetches: false})
}

func TestPush_WhenBranchCannotBePushed_RefusesAtTheFirstAndWritesNothing(t *testing.T) {
	t.Parallel()

	refusals := slices.Concat(standingRefusals(), commitRefusals(), cutRefusals(), copyRefusals(), changeRefusals())
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var want string

			s := newScene(t, func(f *gittest.Fixture) { want = tt.arrange(f) }, tt.script, false)
			if tt.answered > 0 {
				s.deps.Repo.Runner.Env = answering(t, s.fixture, tt.answered, "true")
			}

			before := s.written()

			got := landing.Push(t.Context(), s.deps, pushRequest())

			assertEnded(t, s, got, finish{outcome: result.Refused, exit: exitRefused, text: want})
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
		"GIT_SSH_COMMAND="+counted.String()+"{ "+after+"; exit 1; }; "+serving(f.OriginDir()),
	)
}

// silent returns the address of a local server that accepts one client, never answers it, and
// holds the connection until the test ends, and a channel that closes once the client connected.
func silent(t *testing.T) (addr string, connected chan struct{}) {
	t.Helper()

	var config net.ListenConfig

	listener, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on a local port: %v", err)
	}

	connected = make(chan struct{})
	released := make(chan struct{})

	t.Cleanup(func() {
		close(released)

		if closeErr := listener.Close(); closeErr != nil {
			t.Errorf("close the local listener: %v", closeErr)
		}
	})

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}

		close(connected)
		<-released

		_ = conn.Close()
	}()

	return listener.Addr().String(), connected
}

func TestPush_WhenOriginStopsAnsweringBeforeItIsAskedForTheBranch_RefusesAndWritesNothing(t *testing.T) {
	t.Parallel()
	s := newScene(t, func(f *gittest.Fixture) { publish(f, topic) }, allowed(), false)
	// Origin answers the question about its trunk and the fetch of it, and nothing after.
	s.deps.Repo.Runner.Env = answering(t, s.fixture, 2, "true")
	before := s.written()

	got := landing.Push(t.Context(), s.deps, pushRequest())

	want := "Refused: origin could not be reached to ask whether it has the branch " + topic + "\n" + nextPush
	assertEnded(t, s, got, finish{outcome: result.Refused, exit: exitRefused, text: want})
	assertOnlyRead(t, s, reads{before: before, ops: identityRead(), fetches: true})
}

func TestPush_WhenOriginStopsAnsweringBeforeAFetch_RefusesAndWritesNothing(t *testing.T) {
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
			name: "of its trunk", script: allowed(), ops: identityRead(), answered: 1,
			want: "origin could not be reached to fetch its trunk " + trunk,
		},
		{
			name: "of origin's copy of the branch", script: allowed(), ops: identityRead(), answered: 3,
			arrange: func(f *gittest.Fixture) { publish(f, topic) },
			want:    "origin could not be reached to fetch its copy of the branch " + topic,
		},
		{
			name:   "of the merged pull request's head",
			script: finding(change(merged, trunk, forge.ChangeMerged)), ops: changesRead(), answered: 4,
			arrange: func(f *gittest.Fixture) {
				squash(f)
				publish(f, topic)
			},
			want: "origin could not be reached to fetch the head of pull request #" + strconv.Itoa(merged),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, tt.arrange, tt.script, false)
			s.deps.Repo.Runner.Env = answering(t, s.fixture, tt.answered, "true")
			before := s.written()

			got := landing.Push(t.Context(), s.deps, pushRequest())

			want := "Refused: " + tt.want + "\n" + nextPush
			assertEnded(t, s, got, finish{outcome: result.Refused, exit: exitRefused, text: want})
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
	if err != nil {
		t.Fatalf("overwrite %s: %v", path, err)
	}
}

// lose deletes the commit sha from the working repository's object store, where the fixture keeps
// every object in a file of its own.
func lose(t *testing.T, f *gittest.Fixture, sha string) {
	t.Helper()

	err := os.Remove(filepath.Join(f.Dir(), ".git", "objects", sha[:2], sha[2:]))
	if err != nil {
		t.Fatalf("delete the object %s: %v", sha, err)
	}
}

// brokenRead is one local read of Push that the repository's own state breaks.
type brokenRead struct {
	// arrange breaks the repository and returns the git command that failed and what it said.
	arrange func(t *testing.T, f *gittest.Fixture) string
	name    string
	// read is the read the result names.
	read string
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

				return "git status --porcelain=v1 -z --untracked-files=no: exit status 128: " +
					"error: bad signature 0x20746f6e fatal: index file corrupt"
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
					": exit status 128: error: Could not read " + first + " fatal: revision walk setup failed"
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

				return "git merge-base --is-ancestor " + f.Git("rev-parse", topic) + " " + side +
					": exit status 128: error: Could not read " + middle + " error: could not parse commit " + middle
			},
		},
	}
}

func TestPush_WhenLocalReadFails_BrokeNamingTheReadAndWritesNothing(t *testing.T) {
	t.Parallel()

	for _, tt := range brokenReads() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var detail string

			s := newScene(t, func(f *gittest.Fixture) { detail = tt.arrange(t, f) }, allowed(), false)
			before := s.written()

			got := landing.Push(t.Context(), s.deps, pushRequest())

			want := "Could not read " + tt.read + ": " + detail + "\nNothing was written.\n" + nextFixed
			assertEnded(t, s, got, finish{outcome: result.Broke, exit: exitBroke, text: want})
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
	assertEnded(t, s, got, finish{outcome: result.Broke, exit: exitBroke, text: want})
	assertOnlyRead(t, s, reads{before: before, ops: changesRead(), fetches: true})
}

// stalling returns an arrange under which origin answers the first answered calls git makes to it
// and the next reaches a server that never answers; the deadline ends once that call connected.
func stalling(answered int) func(t *testing.T, s *scene) context.Context {
	return func(t *testing.T, s *scene) context.Context {
		t.Helper()

		addr, connected := silent(t)
		s.deps.Repo.Runner.Env = answering(t, s.fixture, answered, "git ls-remote git://"+addr+"/stalled.git")

		return &ending{Context: t.Context(), done: connected}
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
		// read is the read the refusal names.
		read   string
		script forgetest.Script
		ops    []forgetest.Op
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
			prepare: func(f *gittest.Fixture) { publish(f, topic) },
		},
		{
			name: "during the fetch of the merged pull request's head", read: readMergedHead,
			script: finding(change(merged, trunk, forge.ChangeMerged)), ops: changesRead(), arrange: stalling(4),
			prepare: func(f *gittest.Fixture) {
				squash(f)
				publish(f, topic)
			},
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
			assertEnded(t, s, got, finish{outcome: result.Refused, exit: exitRefused, text: want})
			assertOnlyRead(t, s, reads{before: before, ops: tt.ops, fetches: true})
		})
	}
}

func TestPush_WhenBranchHasNoCommitAtAll_RefusesAsNoCommitPastTheTrunkAndWritesNothing(t *testing.T) {
	t.Parallel()
	s := newScene(t, func(f *gittest.Fixture) { f.Git("switch", "--quiet", "--orphan", spare) }, allowed(), false)
	// A branch with no commit has no HEAD commit to read, so the snapshot names the branch instead.
	standing := func() string { return s.refs() + "\nHEAD: " + s.fixture.Git("symbolic-ref", "HEAD") }
	before := standing()

	got := landing.Push(t.Context(), s.deps, pushRequest())

	want := "Refused: the branch " + spare + " has no commit past origin's trunk " + trunk + "\n" +
		"Next: commit the work, then run ils push again\n"
	assertEnded(t, s, got, finish{outcome: result.Refused, exit: exitRefused, text: want})

	if after := standing(); after != before {
		t.Errorf("refs after Push() = %q, want them unchanged from %q", after, before)
	}

	assertAsked(t, s, identityRead())
}

func TestPush_WhenBranchRefCannotBeRead_BrokeNamingTheTipReadAndWritesNothing(t *testing.T) {
	// Not parallel: failGit puts a failing git first on PATH with t.Setenv.
	s := newScene(t, func(f *gittest.Fixture) { f.Git("switch", "--quiet", "--orphan", spare) }, allowed(), false)
	words := []string{"show-ref", "--verify", "--quiet", "refs/heads/" + spare}
	failGit(t, s, words)

	before := s.refs()

	got := landing.Push(t.Context(), s.deps, pushRequest())

	want := "Could not read " + readTip + ": git " + strings.Join(words, " ") + ": exit status " +
		strconv.Itoa(failingGitExit) + ": " + failingGitSaid + "\nNothing was written.\n" + nextFixed
	assertEnded(t, s, got, finish{outcome: result.Broke, exit: exitBroke, text: want})

	if after := s.refs(); after != before {
		t.Errorf("refs after Push() = %q, want them unchanged from %q", after, before)
	}

	assertAsked(t, s, identityRead())
}

// failGit makes the scene's next git call whose arguments start with words fail as failingGit does,
// by putting a copy of the test binary named git first on PATH. It cannot run in a parallel test.
func failGit(t *testing.T, s *scene, words []string) {
	t.Helper()

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("find the test binary: %v", err)
	}

	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}

	dir := t.TempDir()
	if err = os.Link(self, filepath.Join(dir, name)); err != nil {
		t.Fatalf("link the test binary as git: %v", err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s.deps.Repo.Runner.Env = append(s.deps.Repo.Runner.Env,
		failingWordsEnv+"="+strings.Join(words, failingWordsSep),
		realGitEnv+"="+realGit,
		// The race detector's exit pause would otherwise cost a second per git call.
		"GORACE=atexit_sleep_ms=0",
	)
}

// failedRead is one git read of Push that fails for a reason no precondition covers.
type failedRead struct {
	// arrange changes the repository and returns the arguments of the git call that is made to fail.
	arrange func(*gittest.Fixture) []string
	name    string
	// read is the read the result names.
	read   string
	script forgetest.Script
	// ops are the forge operations made before the read.
	ops []forgetest.Op
}

// history is the arguments of the git call that lists commits, before the revisions it walks.
func history() []string {
	return []string{"log", "--reverse", "--topo-order", "--format=%H %s", "--no-merges"}
}

func failedReads() []failedRead {
	landed := finding(change(merged, trunk, forge.ChangeMerged))
	// since lands the branch as the merged pull request, adds a commit, and returns the pull
	// request's head and the branch's tip.
	since := func(f *gittest.Fixture) (head, tip string) {
		head = f.Git("rev-parse", topic)
		squash(f)

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
				publish(f, topic)
				copied := f.Git("rev-parse", topic)
				tip := f.Commits(topic, "fix a typo")[0]

				return []string{"rev-list", "--left-right", "--count", tip + "..." + copied}
			},
		},
		{
			name: "the merged head's commits past the trunk", read: readMergedHead, script: landed, ops: changesRead(),
			arrange: func(f *gittest.Fixture) []string {
				head, _ := since(f)

				return append(history(), head, "^origin/"+trunk)
			},
		},
		{
			name: "the unlanded commits", read: readCommits, script: landed, ops: changesRead(),
			arrange: func(f *gittest.Fixture) []string {
				head, tip := since(f)

				return append(history(), "--cherry-pick", "--right-only", head+"..."+tip, "^origin/"+trunk)
			},
		},
	}
}

func TestPush_WhenGitReadFailsForNoPrecondition_BrokeNamingTheReadAndWritesNothing(t *testing.T) {
	// Not parallel: each row puts a failing git first on PATH with t.Setenv.
	for _, tt := range failedReads() {
		t.Run(tt.name, func(t *testing.T) {
			var words []string

			s := newScene(t, func(f *gittest.Fixture) { words = tt.arrange(f) }, tt.script, false)
			failGit(t, s, words)

			before := s.written()

			got := landing.Push(t.Context(), s.deps, pushRequest())

			want := "Could not read " + tt.read + ": git " + strings.Join(words, " ") + ": exit status " +
				strconv.Itoa(failingGitExit) + ": " + failingGitSaid + "\nNothing was written.\n" + nextFixed
			assertEnded(t, s, got, finish{outcome: result.Broke, exit: exitBroke, text: want})
			assertOnlyRead(t, s, reads{before: before, ops: tt.ops, fetches: true})
		})
	}
}

const (
	pullURL = "https://github.com/Acme/Widgets/pull/12"
	lintURL = "https://github.com/Acme/Widgets/actions/runs/7/job/9"
	// repoArg is the repository as the --repo flag of gh takes it.
	repoArg = host + "/" + owner + "/" + repoName

	exitDone      = 0
	exitPending   = 4
	exitDelivered = 5

	// tipMark stands, in what a push must print, for the sha of the branch's tip.
	tipMark = "<tip>"

	pushedLine  = "Pushed " + topic + " at " + tipMark + "\n"
	alreadyLine = topic + " is already on origin at " + tipMark + "; nothing pushed\n"
	openedLine  = "Opened pull request #12 against " + trunk + ", titled " + topic + " with no body: " + pullURL + "\n"
	foundLine   = "Pull request #12: " + pullURL + "\n"
	countLine   = "Checks for " + tipMark + ": 1 passed, 0 skipped\n"
	cleanLine   = "GitHub: CLEAN (clean)\n"

	mergeSteps = "write the pull request's title and body as the squash commit's message with " +
		"`gh pr edit --repo " + repoArg + " 12 --title … --body …`, then merge by hand with " +
		"`gh pr merge --repo " + repoArg + " 12 --squash --delete-branch --match-head-commit " + tipMark +
		` --subject "<title> (#12)" --body "<body>"` + "`\n"
	nextMerge  = "Next: " + mergeSteps
	nextReview = "Next: request a review of the pull request first, then " + mergeSteps
	nextRed    = "Next: fix the failing check, commit, and run ils push again\n"
	nextTrunk  = "Next: merge the trunk into the branch with `git merge origin/" + trunk +
		"`, resolve the conflicts, commit, and run ils push again\n"
	nextNever   = "Next: report that a required check never reported, and stop\n"
	nextNothing = "Next: report that no check or status reported for the pushed commit, and stop\n"

	readProtection = "the trunk's required checks"
	readHead       = "the pull request's head"
	readMergeState = "whether the pull request can merge"
	readChecks     = "the checks of the pushed commit"
)

// lineBreaks matches each run of line breaks, which a result prints as one space.
var lineBreaks = regexp.MustCompile(`[\r\n]+`)

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
		script.Head = nil

		for _, head := range heads {
			if head.SHA == "" {
				head.SHA = tip
			}

			script.Head = append(script.Head, forgetest.Answer[forge.Head]{Value: head})
		}
	})
}

// requiring returns build with a trunk whose protection requires the checks names.
func (build forgeFor) requiring(names ...string) forgeFor {
	return build.with(func(script *forgetest.Script, _ string) {
		script.TrunkProtection = []forgetest.Answer[forge.Protection]{{Value: forge.Protection{RequiredChecks: names}}}
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
func listing(lists ...[]forge.Change) []forgetest.Answer[[]forge.Change] {
	answers := make([]forgetest.Answer[[]forge.Change], 0, len(lists))
	for _, list := range lists {
		answers = append(answers, forgetest.Answer[[]forge.Change]{Value: list})
	}

	return answers
}

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
		outcome: result.Done, exit: exitDone,
		text:    before + countLine + cleanLine + nextMerge,
		payload: detail(forge.VerdictGreen, forge.MergeClean, passing().Checks...),
	}
}

// assertDelivered is assertEnded for a result of the push sequence: it also fails unless got
// carries the reason of end and its payload, or no payload when end names none.
func assertDelivered(t *testing.T, s *scene, got result.Result, end finish) {
	t.Helper()

	assertEnded(t, s, got, end)

	if _, reason := shown(t, got, s.gitDir); reason != end.reason {
		t.Errorf("RenderJSON() reason = %q, want %q", reason, end.reason)
	}

	var payload *result.Push
	if got.Payload != nil {
		payload = got.Payload.Push
	}

	if diff := cmp.Diff(end.payload, payload, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Push() payload mismatch (-want +got):\n%s", diff)
	}
}

// pushes reports whether a git call pushed to origin.
func (s *scene) pushes() bool {
	return slices.ContainsFunc(s.recorder.Calls(), func(argv []string) bool {
		return slices.Contains(argv, "push")
	})
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

// delivery is a scene whose branch Push can push: the tip, the clock the wait runs on, and the
// deadline it runs under.
type delivery struct {
	*scene

	ctx   context.Context
	clock *fakeClock
	tip   string
}

// newDelivery returns the scene of tt on the topic branch, with a clock at epoch that moves only
// when the wait polls, and a deadline the time tt has left away.
func newDelivery(t *testing.T, tt pushRow) *delivery {
	t.Helper()

	s := newScene(t, tt.arrange, forgetest.Script{}, false)
	sha := s.fixture.Git("rev-parse", topic)
	clock := &fakeClock{now: epoch}

	s.forge = forgetest.New(t, tt.script(sha))
	s.deps.Forge = s.forge
	s.deps.Clock = clock

	return &delivery{
		scene: s, clock: clock, tip: sha,
		ctx: bounded{Context: t.Context(), deadline: epoch.Add(orBound(tt.left))},
	}
}

// runPushes runs each row as a test of its own: the push ends as the row wants, origin's copy of
// the branch is at the tip, and git pushed unless the row is quiet.
func runPushes(t *testing.T, rows []pushRow) {
	t.Helper()

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := newDelivery(t, tt)

			got := landing.Push(d.ctx, d.deps, pushRequest())

			assertDelivered(t, d.scene, got, tt.want.at(d.tip))

			if at := d.onOrigin(); at != d.tip {
				t.Errorf("origin's copy of %s after Push() = %q, want the tip %q", topic, at, d.tip)
			}

			if pushed := d.pushes(); pushed == tt.quiet {
				t.Errorf("git pushed = %t, want %t", pushed, !tt.quiet)
			}
		})
	}
}

// pushWriteRows are the pushes of a tip origin does not have yet, each ending green.
func pushWriteRows() []pushRow {
	opens := greenAfter(pushedLine + openedLine)
	sinceMerged := opening(passing()).with(func(script *forgetest.Script, _ string) {
		script.FindChanges = listing([]forge.Change{change(merged, trunk, forge.ChangeMerged)})
	})
	skipping := forge.Verdict{
		Checks: []forge.Check{
			checked("lint", forge.BucketPass), checked("test", forge.BucketPass), checked("docs", forge.BucketSkipped),
		},
		Runs: ran(forge.RunCompleted),
	}
	skips := finish{
		outcome: result.Done, exit: exitDone,
		text:    pushedLine + openedLine + "Checks for " + tipMark + ": 2 passed, 1 skipped\n" + cleanLine + nextMerge,
		payload: detail(forge.VerdictGreen, forge.MergeClean, skipping.Checks...),
	}

	return []pushRow{
		{name: "two checks passed and one was skipped", script: opening(skipping), want: skips},
		{name: "first push of the branch", script: opening(passing()), want: opens},
		{
			name: "only a file git does not track", script: opening(passing()), want: opens,
			arrange: func(f *gittest.Fixture) { f.UntrackedFile() },
		},
		{
			name: "tip ahead of origin's copy", script: opening(passing()), want: opens,
			arrange: func(f *gittest.Fixture) {
				publish(f, topic)
				f.Commits(topic, "fix a typo")
			},
		},
		{
			name: "merged pull request sharing no commit past the trunk", script: sinceMerged, want: opens,
			arrange: func(f *gittest.Fixture) {
				earlier := f.Commits(spare, "earlier work")[0]
				f.PullRef(merged, earlier)
				f.SquashOnOrigin(earlier, "earlier work (#7)")
				f.Git("switch", "--quiet", topic)
			},
		},
		{
			name:   "open pull request against the trunk",
			script: reusing(passing()), want: greenAfter(pushedLine + foundLine),
		},
	}
}

func TestPush_WhenEveryPreconditionHolds_PushesAndReportsTheVerdictOfThePullRequest(t *testing.T) {
	t.Parallel()

	runPushes(t, pushWriteRows())
}

func TestPush_AsksTheForgeToOpenThePullRequestAndForTheChecksTheTrunkRequires(t *testing.T) {
	t.Parallel()

	required := []string{"lint", "test"}
	d := newDelivery(t, pushRow{script: opening(requiredPassing()).requiring(required...)})

	landing.Push(d.ctx, d.deps, pushRequest())

	repo := forge.Repo{Host: host, Owner: owner, Name: repoName}
	head := forgetest.Call{Op: forgetest.OpHead, Repo: repo, Head: forge.HeadQuery{Number: opened, Required: required}}
	want := []forgetest.Call{
		{Op: forgetest.OpIdentity, Repo: repo},
		{Op: forgetest.OpFindChanges, Repo: repo, Branch: topic},
		{Op: forgetest.OpOpenChange, Repo: repo, Change: forge.NewChange{Title: topic, Base: trunk, Head: topic}},
		{Op: forgetest.OpTrunkProtection, Repo: repo, Branch: trunk},
		head,
		{Op: forgetest.OpVerdict, Repo: repo, Query: forge.VerdictQuery{SHA: d.tip, Number: opened, Required: required}},
		head,
	}
	// What the forge said of the repository is no part of which repository a call is about.
	which := cmpopts.IgnoreFields(forge.Repo{}, "Fork", "CanPush")

	if diff := cmp.Diff(want, d.forge.Calls(), which); diff != "" {
		t.Errorf("forge calls mismatch (-want +got):\n%s", diff)
	}
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
				outcome: result.Broke, exit: exitBroke, reason: "no pull request was opened or found",
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

	// Pushing the topic branch once more shows what git says of the refusal, as a result prints it on one line.
	err := d.deps.Repo.Push(t.Context(), topic)

	refused, ok := errors.AsType[*git.PushError](err)
	if !ok {
		t.Fatalf("Repo.Push() error = %v, want a *git.PushError", err)
	}

	said := lineBreaks.ReplaceAllString(strings.TrimSpace(refused.Stderr), " ")

	assertDelivered(t, d.scene, got, finish{
		outcome: result.Broke, exit: exitBroke, reason: "git refused the push",
		text: "Git refused the push of " + topic + ", so nothing was pushed: " + said + "\n" + nextFixed,
	})

	if at := d.onOrigin(); at != "" {
		t.Errorf("origin's copy of %s after Push() = %q, want no such branch", topic, at)
	}

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
				outcome: result.Broke, exit: exitBroke, reason: "could not open the pull request",
				text: pushedLine + "No pull request was opened: HTTP 502\n" + nextFixed,
			},
		},
		{
			name:   "because the deadline ended in it",
			script: failing(fmt.Errorf("gh pr create: %w", context.DeadlineExceeded)),
			want: finish{
				outcome: result.Broke, exit: exitBroke, reason: "the time limit ended while opening the pull request",
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
		outcome: result.Broke, exit: exitBroke, reason: "the time limit ended during the push",
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
		outcome: result.Broke, exit: exitBroke, reason: "could not push the branch",
		text: "Could not push " + topic + ", so nothing was pushed: git push -u origin " + topic +
			": context canceled\n" + nextFixed,
	})
	assertAsked(t, s, changesRead())
}

// pushAgainRows are the pushes of a tip origin already has.
func pushAgainRows() []pushRow {
	again := func(f *gittest.Fixture) { publish(f, topic) }

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

func TestPush_WhenTipIsAlreadyOnOrigin_PushesNothingAndReportsTheVerdict(t *testing.T) {
	t.Parallel()

	runPushes(t, pushAgainRows())
}

func TestPush_WhenTipIsOnOriginAndChecksStillRun_WaitsAgainForTheVerdict(t *testing.T) {
	t.Parallel()
	d := newDelivery(t, pushRow{
		arrange: func(f *gittest.Fixture) { publish(f, topic) },
		script:  reusing(running(), running(), passing()),
	})

	got := landing.Push(d.ctx, d.deps, pushRequest())

	assertDelivered(t, d.scene, got, greenAfter(alreadyLine+foundLine).at(d.tip))

	if diff := cmp.Diff(polls(2, verdictPoll), d.clock.waits); diff != "" {
		t.Errorf("clock waits mismatch (-want +got):\n%s", diff)
	}
}

// steps returns the lines "step <first>" to "step <last>", each ended by a line break.
func steps(first, last int) string {
	var lines strings.Builder
	for i := first; i <= last; i++ {
		fmt.Fprintf(&lines, "step %d\n", i)
	}

	return lines.String()
}

// redRow is a push whose lint check ended in bucket beside a passing test, with log as its failed
// log: the lines say the check failed, or was canceled, and the excerpt block is excerpt.
func redRow(name string, bucket forge.Bucket, log forge.FailedLog, excerpt string) pushRow {
	said := "failed"
	if bucket == forge.BucketCanceled {
		said = "was canceled"
	}

	log.Check = "lint"
	verdict := forge.Verdict{
		Checks: []forge.Check{
			{Name: "lint", Bucket: bucket, Workflow: "ci", Link: lintURL},
			checked("test", forge.BucketPass),
		},
		Runs:      ran(forge.RunCompleted),
		FailedLog: log,
	}
	payload := detail(forge.VerdictRed, forge.MergeBlockedOther, verdict.Checks...)
	payload.FailedLog = log.Text

	return pushRow{
		name:   name,
		script: opening(verdict).showing(inState(forge.MergeBlockedOther, "UNSTABLE")),
		want: finish{
			outcome: result.DeliveredNotGreen, exit: exitDelivered, reason: "the check lint " + said, payload: payload,
			text: pushedLine + openedLine + "Check lint " + said + ": " + lintURL + "\n" +
				"GitHub: UNSTABLE (blocked-other)\n" + excerpt + nextRed,
		},
	}
}

// pushRedRows are the pushes whose deciding check failed or was canceled.
func pushRedRows() []pushRow {
	// The log is one line longer than the excerpt may be.
	read := forge.FailedLog{Text: steps(1, 61), State: forge.FailedLogRead}
	short := forge.FailedLog{Text: steps(1, 3), State: forge.FailedLogRead}

	return []pushRow{
		redRow("a check failed and its log is longer than the excerpt", forge.BucketFail, read, steps(2, 61)),
		redRow("a check failed and its log is shorter than the excerpt", forge.BucketFail, short, steps(1, 3)),
		redRow("a check failed that no workflow run stands behind", forge.BucketFail,
			forge.FailedLog{State: forge.FailedLogNoRun}, "no log: lint is not a workflow run\n"),
		redRow("a check was canceled and its log cannot be read", forge.BucketCanceled,
			forge.FailedLog{State: forge.FailedLogUnreadable}, "no log: the log of lint could not be read\n"),
	}
}

// stoppedRow is a push the pull request's merge state stops before any check is read.
func stoppedRow(state forge.MergeState, raw, reason string) pushRow {
	return pushRow{
		name:   string(state),
		script: reusing().showing(inState(state, raw)),
		want: finish{
			outcome: result.DeliveredNotGreen, exit: exitDelivered, reason: reason,
			text:    pushedLine + foundLine + "GitHub: " + raw + " (" + string(state) + ")\n" + nextTrunk,
			payload: detail(forge.VerdictNone, state),
		},
	}
}

// pushUnreportedRows are the pushes whose checks did not all report.
func pushUnreportedRows() []pushRow {
	lint := needed("lint", forge.BucketPass)
	never := forge.Verdict{Checks: []forge.Check{lint}, MissingRequired: []string{"test"}, Runs: ran(forge.RunCompleted)}
	missing := inState(forge.MergeBlockedByMissingCheck, "BLOCKED")

	return []pushRow{
		{
			name:   "a required check never reported",
			script: opening(never).requiring("lint", "test").showing(missing),
			want: finish{
				outcome: result.DeliveredNotGreen, exit: exitDelivered, reason: "a required check never reported",
				text: pushedLine + openedLine + "Required and never reported: test\n" +
					"GitHub: BLOCKED (blocked-by-missing-check)\n" + nextNever,
				payload: detail(forge.VerdictGreen, forge.MergeBlockedByMissingCheck, lint),
			},
		},
		{
			name: "nothing reported", script: opening(forge.Verdict{}),
			want: finish{
				outcome: result.DeliveredNotGreen, exit: exitDelivered,
				reason: "no check or status reported for " + tipMark + " on pull request #12",
				text: pushedLine + openedLine + "No check or status reported for " + tipMark +
					" on pull request #12\n" + cleanLine + nextNothing,
				payload: detail(forge.VerdictNone, forge.MergeClean),
			},
		},
	}
}

// pushPendingRows are the pushes whose wait the deadline ended, one per stage of the wait.
func pushPendingRows() []pushRow {
	pending := func(reason, lines string, payload *result.Push) finish {
		return finish{
			outcome: result.Pending, exit: exitPending, reason: reason, payload: payload,
			text: pushedLine + foundLine + lines + nextPush,
		}
	}

	started := forge.Verdict{Runs: ran(forge.RunInProgress)}

	return []pushRow{
		{
			// Twelve seconds end inside the 20 seconds a silent commit is given.
			name: "nothing has reported yet", left: 12 * time.Second, script: reusing(forge.Verdict{}),
			want: pending("no check or status had reported at the time limit",
				"No check or status has reported for "+tipMark+" on pull request #12 yet\n"+cleanLine,
				detail(forge.VerdictNone, forge.MergeClean)),
		},
		{
			name: "a workflow run is going and no check has reported yet", left: 12 * time.Second,
			script: reusing(started),
			want: pending("the checks had not reported at the time limit",
				"No check has reported for "+tipMark+" on pull request #12 yet; a workflow run is still going\n"+cleanLine,
				detail(forge.VerdictNone, forge.MergeClean)),
		},
		{
			// A third wait would end a second past the deadline.
			name: "the pull request still shows an earlier commit", left: 5 * time.Second,
			script: reusing().showing(stale()),
			want: pending("GitHub did not show the pushed commit on the pull request before the time limit",
				"GitHub does not show "+tipMark+" on pull request #12 yet\n", detail(forge.VerdictNone, forge.MergeClean)),
		},
		{
			name:   "the merge state is still unknown",
			script: reusing().showing(inState(forge.MergeUnknown, "UNKNOWN")),
			want: pending("GitHub did not say whether the pull request can merge before the time limit",
				"GitHub has not said whether pull request #12 can merge yet\n", detail(forge.VerdictNone, forge.MergeUnknown)),
		},
		{
			// Two polls of five seconds pass; a third would end three seconds past the deadline.
			name: "a check still runs", left: 12 * time.Second, script: reusing(running()),
			want: pending("the checks were still running at the time limit",
				"Still running after 10 seconds: test\n"+cleanLine,
				detail(forge.VerdictPending, forge.MergeClean, running().Checks...)),
		},
		{
			name: "the deadline ends in the read of the merge state after the checks",
			script: reusing(passing()).with(func(script *forgetest.Script, _ string) {
				script.Head = append(script.Head, forgetest.Answer[forge.Head]{
					Err: fmt.Errorf("gh pr view: %w", context.DeadlineExceeded),
				})
			}),
			want: pending("GitHub did not say whether the pull request can merge before the time limit",
				"GitHub has not said whether pull request #12 can merge yet\n",
				detail(forge.VerdictGreen, forge.MergeClean, passing().Checks...)),
		},
	}
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
		stoppedRow(forge.MergeConflicting, "DIRTY", "the pull request conflicts with the trunk"),
		stoppedRow(forge.MergeBehind, "BEHIND", "the pull request is behind the trunk"),
	}

	return slices.Concat(rows, pushRedRows(), pushUnreportedRows(), pushPendingRows())
}

func TestPush_WhenTheWaitEnds_ReportsItsOutcomeAndTheMergeState(t *testing.T) {
	t.Parallel()

	runPushes(t, pushWaitRows())
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

// readsAfterWrites returns the reads of a push that follow its writes, in the order the push makes
// them, each failing with err.
func readsAfterWrites(err error) []failingRead {
	breaking := func(edit func(*forgetest.Script)) forgeFor {
		return opening(passing()).with(func(script *forgetest.Script, _ string) { edit(script) })
	}
	// undecided shows the tip in a merge state the forge has not worked out, and then fails.
	undecided := opening(passing()).showing(inState(forge.MergeUnknown, "UNKNOWN")).
		with(func(script *forgetest.Script, _ string) {
			script.Head = append(script.Head, forgetest.Answer[forge.Head]{Err: err})
		})
	opens := pushedLine + openedLine

	return []failingRead{
		{
			name: readChanges, before: pushedLine,
			script: breaking(func(script *forgetest.Script) {
				script.OpenChange = []forgetest.Answer[forge.ChangeRef]{{Err: forge.ErrChangeExists}}
				script.FindChanges = append(listing(nil), forgetest.Answer[[]forge.Change]{Err: err})
			}),
		},
		{
			name: readProtection, before: opens, known: true,
			script: breaking(func(script *forgetest.Script) {
				script.TrunkProtection = []forgetest.Answer[forge.Protection]{{Err: err}}
			}),
		},
		{
			name: readHead, before: opens, known: true,
			script: breaking(func(script *forgetest.Script) {
				script.Head = []forgetest.Answer[forge.Head]{{Err: err}}
			}),
		},
		{name: readMergeState, before: opens, known: true, script: undecided},
		{
			name: readChecks, before: opens, known: true,
			script: breaking(func(script *forgetest.Script) {
				script.Verdict = []forgetest.Answer[forge.Verdict]{{Err: err}}
			}),
		},
	}
}

// pushBrokenRows are the pushes one of whose reads fails after the writes.
func pushBrokenRows() []pushRow {
	reads := readsAfterWrites(errors.New("HTTP 502"))
	rows := make([]pushRow, 0, len(reads))

	for _, read := range reads {
		rows = append(rows, read.row(finish{
			outcome: result.Broke, exit: exitBroke, reason: "could not read " + read.name,
			text: read.before + "Could not read " + read.name + ": HTTP 502\n" + nextFixed,
		}))
	}

	// The checks pass, and the read of the merge state made after them fails.
	late := opening(passing()).with(func(script *forgetest.Script, _ string) {
		script.Head = append(script.Head, forgetest.Answer[forge.Head]{Err: errors.New("HTTP 502")})
	})

	return append(rows, pushRow{
		name: readMergeState + " after the checks", script: late,
		want: finish{
			outcome: result.Broke, exit: exitBroke, reason: "could not read " + readMergeState,
			text:    pushedLine + openedLine + "Could not read " + readMergeState + ": HTTP 502\n" + nextFixed,
			payload: detail(forge.VerdictNone, forge.MergeUnknown),
		},
	})
}

func TestPush_WhenDeadlineEndsInAReadAfterTheWrites_PendingNamingTheRead(t *testing.T) {
	t.Parallel()

	// The deadline ends in the first two reads: the pull requests of the branch and the trunk's protection.
	reads := readsAfterWrites(fmt.Errorf("gh: %w", context.DeadlineExceeded))[:2]
	rows := make([]pushRow, 0, len(reads))

	for _, read := range reads {
		rows = append(rows, read.row(finish{
			outcome: result.Pending, exit: exitPending, reason: "the time limit ended while reading " + read.name,
			text: read.before + "The time limit ended while reading " + read.name + "; nothing more will change\n" + nextPush,
		}))
	}

	runPushes(t, rows)
}

func TestPush_WhenAReadFailsAfterTheWrites_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()

	runPushes(t, pushBrokenRows())
}
