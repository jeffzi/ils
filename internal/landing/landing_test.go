package landing_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

const (
	trunk    = "develop"
	topic    = "tidy-readme"
	host     = "github.com"
	owner    = "Acme"
	repoName = "Widgets"

	// The two spellings name one repository: host, owner, and name differ in case and shape only.
	fetchURL  = "ssh://git@github.com/Acme/Widgets.git"
	pushURL   = "git@github.com:acme/widgets.git"
	otherURL  = "git@github.com:acme/gadgets.git"
	localPath = "/srv/project"

	nextPush   = "Next: ils push\n"
	nextByHand = "Next: stop and tell the user to push by hand\n"
	nextOneURL = "Next: point both of origin's URLs at one GitHub repository with `git remote set-url`, by hand\n"
	nextFixed  = "Next: fix what failed, then run ils push again\n"

	readGitVersion = "git's version"
	readStanding   = "where HEAD stands"
	readURLs       = "origin's URLs"
	readIdentity   = "the repository's identity on GitHub"
	readTrunk      = "origin's trunk"

	noOriginText = "Refused: this repository has no remote named origin\n" +
		"Next: create the repository on GitHub and push to it with `gh repo create --source . --push`\n"
)

// ---------------------------------------------------------------------------
// Scenes and assertions
// ---------------------------------------------------------------------------

// scene is one repository and one forge, with a record of what each was asked.
type scene struct {
	fixture  *gittest.Fixture
	forge    *forgetest.Fake
	recorder *git.Recorder
	gitDir   string
	deps     landing.Deps
}

// newScene returns a repository on the topic branch whose origin URLs name Acme/Widgets on
// GitHub, once arrange has changed it, and a forge that answers from script. An offline scene
// has an origin that does not answer. Its ssh reads the fixture's ssh config, which names no host.
func newScene(t *testing.T, arrange func(*gittest.Fixture), script forgetest.Script, offline bool) *scene {
	t.Helper()

	f, env := gittest.NewServed(t, trunk, topic, fetchURL)
	f.SetPushURL(pushURL)

	if arrange != nil {
		arrange(f)
	}

	if offline {
		env = f.ServedEnv(f.MissingOriginDir())
	}

	recorder := &git.Recorder{}
	fake := forgetest.New(t, script)

	return &scene{
		fixture:  f,
		forge:    fake,
		recorder: recorder,
		gitDir:   f.Git("rev-parse", "--absolute-git-dir"),
		deps: landing.Deps{
			Repo:  git.Repo{Runner: git.Runner{Dir: f.Dir(), Env: env, Recorder: recorder}},
			Forge: fake,
		},
	}
}

// useForge replaces the scene's forge with one that answers from script, both where the code
// under test calls it and where the scene reads back what it was asked.
func (s *scene) useForge(t *testing.T, script forgetest.Script) {
	t.Helper()

	s.forge = forgetest.New(t, script)
	s.deps.Forge = s.forge
}

// refs returns every ref of the working repository and of origin with the commit it is on.
func (s *scene) refs() string {
	return s.fixture.Git("for-each-ref") + "\norigin:\n" + s.fixture.OriginGit("for-each-ref")
}

// origin is the repository origin's URLs name, as a call to the forge carries it.
func origin() forge.Repo {
	return forge.Repo{Host: host, Owner: owner, Name: repoName}
}

// whichRepo compares the repository of a forge call by host, owner, and name alone: what the
// forge said of the repository is no part of which repository a call is about.
func whichRepo() cmp.Option {
	return cmpopts.IgnoreFields(forge.Repo{}, "Fork", "CanPush", "SquashAllowed")
}

// asked returns the calls that ops stand for: each about the repository origin's URLs name, and
// the find-changes read about the topic branch.
func asked(ops []forgetest.Op) []forgetest.Call {
	var calls []forgetest.Call

	for _, op := range ops {
		call := forgetest.Call{Op: op, Repo: origin()}
		if op == forgetest.OpFindChanges {
			call.Branch = topic
		}

		calls = append(calls, call)
	}

	return calls
}

// assertAsked fails unless the forge was called exactly as ops stand for: the operations in order,
// the host, owner, and name of the repository each was about, and the branch.
func assertAsked(t *testing.T, s *scene, ops []forgetest.Op) {
	t.Helper()

	assert.Check(t, is.DeepEqual(s.forge.Calls(), asked(ops), whichRepo()))
}

// gitRan reports whether a git call of the scene carried the argument word.
func (s *scene) gitRan(word string) bool {
	return slices.ContainsFunc(s.recorder.Calls(), func(argv []string) bool {
		return slices.Contains(argv, word)
	})
}

// pushRequest is the command and words of a bare `ils push`.
func pushRequest() landing.Request {
	return landing.Request{Command: "push", Words: []string{"push"}}
}

// widgets returns the identity the forge gives for Acme/Widgets.
func widgets(fork, canPush bool) forge.Repo {
	return forge.Repo{Host: host, Owner: owner, Name: repoName, Fork: fork, CanPush: canPush}
}

// answers is a script whose identity call returns repo.
func answers(repo forge.Repo) forgetest.Script {
	return forgetest.Script{Identity: []forgetest.Answer[forge.Repo]{{Value: repo}}}
}

// allowed is a script whose identity call says the user can push to Acme/Widgets.
func allowed() forgetest.Script {
	return answers(widgets(false, true))
}

// fails is a script whose identity call returns err.
func fails(err error) forgetest.Script {
	return forgetest.Script{Identity: []forgetest.Answer[forge.Repo]{{Err: err}}}
}

// shown renders r through both renderers and returns the text and the reason the JSON carries. It
// fails unless the JSON carries every line of the text, so that a caller who reads the JSON is
// told what one who reads the text is.
func shown(t *testing.T, r result.Result, gitDir string) (text, reason string) {
	t.Helper()

	text, err := result.RenderText(r, gitDir)
	assert.NilError(t, err)

	out, err := result.RenderJSON(r, gitDir)
	assert.NilError(t, err)

	decoded := resulttest.Decode(t, out)
	resulttest.AssertCarried(t, text, decoded)

	return text, decoded.Reason
}

// drawnPart is a part of a result's text and how a terminal with color draws it where it first
// appears inside within. tipMark in either stands for the sha of the tip.
type drawnPart struct {
	within string
	part   string
	want   resulttest.Look
}

// commandIn is command, which the wording wraps in backticks, where it first appears inside within,
// as a terminal with color draws it: without the backticks, in the style of a command. Both are
// given as the plain text prints them.
func commandIn(within, command string) drawnPart {
	unquoted := func(text string) string { return strings.ReplaceAll(text, "`", "") }

	return drawnPart{within: unquoted(within), part: unquoted(command), want: resulttest.NameLook()}
}

// atTip returns parts with the sha tip in place of tipMark in each.
func atTip(parts []drawnPart, tip string) []drawnPart {
	placed := slices.Clone(parts)
	for i := range placed {
		placed[i].within = mark(placed[i].within, tip)
		placed[i].part = mark(placed[i].part, tip)
	}

	return placed
}

// assertDrawn fails unless got, rendered with color, draws each of parts as it says.
func assertDrawn(t *testing.T, got result.Result, gitDir string, parts []drawnPart) {
	t.Helper()

	out, err := result.RenderStyled(got, gitDir, colorprofile.ANSI)
	assert.NilError(t, err)

	d := resulttest.Draw(t, out)
	for _, p := range parts {
		look := d.LookOf(t, p.within, p.part)
		assert.Check(t, is.Equal(look, p.want), "%q inside %q", p.part, p.within)
	}
}

// assertStopped fails unless got ended as want and carries the command and words of pushRequest.
func assertStopped(t *testing.T, got result.Result, want result.Outcome) {
	t.Helper()

	assertStoppedBy(t, got, want, pushRequest())
}

// assertStoppedBy fails unless got ended as want and carries the command and words of req.
func assertStoppedBy(t *testing.T, got result.Result, want result.Outcome, req landing.Request) {
	t.Helper()

	assert.Check(t, is.Equal(got.Outcome, want))

	assert.Check(t, is.Equal(got.Command, req.Command))

	assert.Check(t, is.DeepEqual(got.Words, req.Words))
}

// assertBrokeRead fails unless got is broke on the read named read, says nothing was written,
// and carries detail in the line about the failure.
func assertBrokeRead(t *testing.T, got result.Result, read, detail string) {
	t.Helper()

	said := assertBrokeReadBy(t, got, pushCommand(), read)
	assert.Check(t, is.Contains(said, detail))
}

// assertBrokeReadBy fails unless got is broke on the read named read for the command cmd: it
// carries cmd's command and words, says nothing was written, and ends on cmd's `Next:` line for a
// fix. It returns what the result says about the failure, between the read's name and the line
// that nothing was written, which must not be empty.
func assertBrokeReadBy(t *testing.T, got result.Result, cmd landingCommand, read string) string {
	t.Helper()

	assertStoppedBy(t, got, result.Broke, cmd.req)

	text, reason := shown(t, got, "")
	prefix := "Could not read " + read + ": "
	suffix := "\nNothing was written.\nBroke: could not read " + read + "\n" + cmd.fixed

	said, starts := strings.CutPrefix(text, prefix)
	said, ends := strings.CutSuffix(said, suffix)
	assert.Check(t, starts, "RenderText() = %q, want it to start with %q", text, prefix)
	assert.Check(t, ends, "RenderText() = %q, want it to end with %q", text, suffix)
	assert.Check(t, said != "", "RenderText() = %q, want what failed said between %q and %q", text, prefix, suffix)

	assert.Check(t, is.Equal(reason, "could not read "+read))

	return said
}

// stopped returns the result the preconditions stopped with, and fails the test when they did not
// stop; want says what the stop is.
func stopped(t *testing.T, stop *result.Result, want string) result.Result {
	t.Helper()

	assert.Assert(t, stop != nil, "Preconditions() stop = nil, want %s", want)

	return *stop
}

// ready runs the preconditions of a push on the scene and fails unless every one holds.
func ready(t *testing.T, s *scene) landing.Ready {
	t.Helper()

	got, stop := landing.Preconditions(t.Context(), s.deps, pushRequest())
	if stop != nil {
		text, _ := shown(t, *stop, s.gitDir)
		t.Fatalf("Preconditions() stopped with %q, want every precondition to hold", text)
	}

	return got
}

// assertRefsUnchanged fails unless every ref of the scene is as it was in before.
func assertRefsUnchanged(t *testing.T, s *scene, before string) {
	t.Helper()

	assert.Check(t, is.Equal(s.refs(), before))
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

// mergeRequest is the command and words of a bare `ils merge`.
func mergeRequest() landing.Request {
	return landing.Request{Command: "merge", Words: []string{"merge"}}
}

// landingCommand is a command that runs the shared preconditions, with the `Next:` lines that
// name it.
type landingCommand struct {
	name string
	// again is the `Next:` line that runs the command again.
	again string
	// fixed is the `Next:` line that runs the command again once what failed is fixed.
	fixed string
	req   landing.Request
}

// after is the `Next:` line that does step, then runs the command again.
func (c landingCommand) after(step string) string {
	return "Next: " + step + ", then run ils " + c.name + " again\n"
}

// pushCommand is `ils push` with the `Next:` lines that name it.
func pushCommand() landingCommand {
	return landingCommand{name: "push", req: pushRequest(), again: nextPush, fixed: nextFixed}
}

// mergeCommand is `ils merge` with the `Next:` lines that name it.
func mergeCommand() landingCommand {
	return landingCommand{name: "merge", req: mergeRequest(), again: nextMergeAgain, fixed: nextMergeFixed}
}

// landingCommands are push and merge, the two commands that run the shared preconditions.
func landingCommands() []landingCommand {
	return []landingCommand{pushCommand(), mergeCommand()}
}

// unreached is the whole text of cmd's refusal when origin stopped answering while it was asked to
// do what.
func unreached(cmd landingCommand, what string) string {
	return "Refused: origin could not be reached to " + what + "\n" + cmd.again
}

// cut is one way the context of a run ends before the run does: the time limit, its deadline, or
// an interrupt, which cancels it. A result words the two apart and ends alike for both.
type cut struct {
	// err is the context's error once it ended, which a call it cut short wraps.
	err error
	// spend returns a context over parent that has already ended as the context package ends one
	// for this cut.
	spend func(parent context.Context) (context.Context, context.CancelFunc)
	name  string
	// ends says the context ended, and before says what had not happened by then.
	ends   string
	before string
}

// byTimeLimit is the cut whose context is already past its deadline, so its error is
// context.DeadlineExceeded.
func byTimeLimit() cut {
	return cut{
		name: "the time limit", err: context.DeadlineExceeded,
		spend: func(parent context.Context) (context.Context, context.CancelFunc) {
			return context.WithDeadline(parent, time.Unix(0, 0))
		},
		ends: "the time limit ended", before: "before the time limit",
	}
}

// byInterrupt is an interrupt of a run, which cancels its context before the deadline.
func byInterrupt() cut {
	return cut{
		name: "an interrupt", err: context.Canceled,
		spend: func(parent context.Context) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(parent)
			cancel()

			return ctx, cancel
		},
		ends: "ils was interrupted", before: "before ils was interrupted",
	}
}

// cuts lists every cut. A test that covers one outcome loops over it, so that the time limit and
// an interrupt are held to the same result.
func cuts() []cut { return []cut{byTimeLimit(), byInterrupt()} }

// of returns the error the call named call fails with when c cuts it short.
func (c cut) of(call string) error { return fmt.Errorf("%s: %w", call, c.err) }

// once returns a context over parent that ends as c says once done closes, and not before.
func (c cut) once(parent context.Context, done <-chan struct{}) context.Context {
	return gittest.Ending(parent, done, c.err)
}

// after returns a context over parent that ends as c says once d of fake time has passed, without
// its end being known ahead.
func (c cut) after(parent context.Context, d time.Duration) context.Context {
	done := make(chan struct{})
	time.AfterFunc(d, func() { close(done) })

	return c.once(parent, done)
}

// inPoll returns a context over ctx that ends as c says midPoll into the first poll of a wait.
func (c cut) inPoll(ctx context.Context) context.Context { return c.after(ctx, midPoll) }

// spent returns a context that c ended before it was handed out.
func (c cut) spent(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := c.spend(t.Context())
	t.Cleanup(cancel)

	return ctx
}

// overtimeText is the whole text of cmd's refusal when c ended the read named read.
func overtimeText(cmd landingCommand, c cut, read string) string {
	return "Refused: " + c.ends + " while reading " + read + "; nothing was written\n" + cmd.again
}

// unreadText is the whole text of cmd's result when the read named read failed saying detail.
func unreadText(cmd landingCommand, read, detail string) string {
	return "Could not read " + read + ": " + detail + "\nNothing was written.\nBroke: could not read " + read + "\n" + cmd.fixed
}

// ---------------------------------------------------------------------------
// Checks of the branch
// ---------------------------------------------------------------------------

const (
	startCommand = "git fetch origin " + trunk + " && git switch -c <name> --no-track refs/remotes/origin/" + trunk
	nextBranch   = "Next: start a branch from origin's trunk: `" + startCommand + "`"
	baseCommand  = "gh pr edit 12 --base " + trunk

	readTree         = "the working tree's changes"
	readFetchedTrunk = "the commits on origin's trunk"
	readMergedHead   = "the merged pull request's head"
	readTip          = "the branch's tip"
)

// startDrawn are the parts of nextBranch a terminal with color draws in the style of their kind.
func startDrawn() []drawnPart {
	return []drawnPart{commandIn(nextBranch, startCommand)}
}

const (
	onTrunk  = "Refused: HEAD is on the trunk " + trunk + ", which ils never pushes\n" + nextBranch + "\n"
	detached = "Refused: HEAD is detached, so there is no branch to push\n" + nextBranch + "\n"
)

// dirtyText is cmd's refusal of a tree whose changed tracked files are files.
func dirtyText(cmd landingCommand, files ...string) string {
	slices.Sort(files)

	return "Refused: tracked files have changes that are not committed: " + strings.Join(files, ", ") + "\n" +
		cmd.after("commit them")
}

// elsewhereText is cmd's refusal of a branch whose open pull request 12 is against otherBase.
func elsewhereText(cmd landingCommand) string {
	return "Refused: pull request #12 from " + topic + " is open against " + otherBase +
		" and not against the trunk " + trunk + "\n" +
		cmd.after("close it or change its base with `"+baseCommand+"`")
}

// standingRefusals are the refusals decided from where HEAD stands and from the working tree,
// before anything is fetched.
func standingRefusals() []pushRefusal {
	return []pushRefusal{
		{
			name: "on the trunk", script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) string {
				f.Git("switch", "--quiet", trunk)

				return onTrunk
			},
			drawn: append(startDrawn(), drawnPart{within: "the trunk " + trunk + ",", part: trunk, want: resulttest.NameLook()}),
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
			arrange: func(f *gittest.Fixture) string { return dirtyText(pushCommand(), f.DirtyFile()) },
		},
		{
			name: "tracked file changed and staged", script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) string { return dirtyText(pushCommand(), f.StagedFile()) },
		},
		{
			name: "two tracked files changed", script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) string { return dirtyText(pushCommand(), f.DirtyFile(), f.StagedFile()) },
		},
		{
			name: "dirty tree on a branch with no commit past the trunk", script: allowed(), ops: identityRead(),
			arrange: func(f *gittest.Fixture) string {
				f.Git("switch", "--quiet", "--create", spare, trunk)

				return dirtyText(pushCommand(), f.DirtyFile())
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
			drawn: []drawnPart{
				{within: "the branch " + spare, part: spare, want: resulttest.NameLook()},
				{within: "trunk " + trunk, part: trunk, want: resulttest.NameLook()},
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
			drawn: []drawnPart{
				{within: "pull request #12 from", part: "#12", want: resulttest.NameLook()},
				{within: "from " + topic, part: topic, want: resulttest.NameLook()},
				{within: "against " + otherBase, part: otherBase, want: resulttest.NameLook()},
				{within: "the trunk " + trunk, part: trunk, want: resulttest.NameLook()},
				commandIn(elsewhere, baseCommand),
			},
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
			drawn: startDrawn(),
		},
		{
			name: "merged pull request with commits made since", script: landedScript(), ops: changesRead(), fetches: true,
			arrange: func(f *gittest.Fixture) string {
				f.LandPull(merged, topic)
				later := f.Commits(topic, "fix a typo", "add an example")

				return later[0] + " fix a typo\n" + later[1] + " add an example\n" +
					"Refused: pull request #" + number + " from " + topic +
					" was merged without the commits listed here\n" +
					nextBranch + ", then bring those commits over with `git cherry-pick " + later[0] + " " + later[1] +
					"`; expect a conflict where the squash touched the same files\n"
			},
			drawn: append(startDrawn(),
				commandIn(" "+tipMark+"`; expect", " "+tipMark),
			),
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
	opens := greenAfter(pushedLine + openedLine)
	sinceMerged := opening(passing()).with(func(script *forgetest.Script, _ string) {
		script.FindChanges = listing([]forge.Change{change(merged, trunk, forge.ChangeMerged)})
	})

	return []pushRow{
		{
			name: "only a file git does not track", script: opening(passing()), want: opens,
			arrange: func(f *gittest.Fixture) { f.UntrackedFile() },
		},
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
		{
			name:   "asked to fetch the merged pull request's head",
			script: landedScript(), ops: changesRead(), answered: 4,
			arrange: landAndPublish,
			asked:   "fetch the head of pull request #" + strconv.Itoa(merged),
		},
	})
}

func TestPush_WhenASharedReadIsCutShort_RefusesNamingTheRead(t *testing.T) {
	t.Parallel()

	runOvertime(t, func(c cut) []overtime {
		return []overtime{
			{
				// Origin answers the question about its trunk, and nothing after.
				name: "during the fetch of origin's trunk", read: readFetchedTrunk, script: allowed(), ops: identityRead(),
				arrange: c.stalling(1),
			},
			{
				name: "during the fetch of the merged pull request's head", read: readMergedHead,
				script: landedScript(), ops: changesRead(), arrange: c.stalling(4),
				prepare: landAndPublish,
			},
			{
				name: "during the find-changes read", read: readChanges, ops: changesRead(),
				script: failingToFind(c.of("gh pr list")),
				arrange: func(t *testing.T, _ *scene) context.Context {
					t.Helper()

					return t.Context()
				},
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

	want := unreadText(pushCommand(), readChanges, "HTTP 502")
	assertEnded(t, s, got, finish{outcome: result.Broke, text: want})
	assertOnlyRead(t, s, reads{before: before, ops: changesRead(), fetches: true})
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
	words := []string{"show-ref", "--verify", "--quiet", "refs/heads/" + spare}
	s := newScene(t, func(f *gittest.Fixture) { f.Git("switch", "--quiet", "--orphan", spare) }, allowed(), false)
	failGit(t, s, words)
	before := s.refs()

	got := landing.Push(t.Context(), s.deps, pushRequest())

	assertFailedGit(t, got, pushCommand(), readTip)

	assert.Check(t, is.Equal(s.refs(), before))

	assertAsked(t, s, identityRead())
}

func TestPushAndMerge_WhenGivenNoProgressHookOrLogger_EndsAsWithThem(t *testing.T) {
	t.Parallel()

	// bare is a rewire of the scene's deps that leaves them no progress hook and no logger.
	bare := func(deps *landing.Deps) { deps.Progress, deps.Logger = nil, nil }
	push := func(script forgeFor) func(t *testing.T, withoutHooks bool) result.Result {
		return func(t *testing.T, withoutHooks bool) result.Result {
			t.Helper()

			d := newDelivery(t, pushRow{script: script})
			if withoutHooks {
				bare(&d.deps)
			}

			return landing.Push(d.ctx, d.deps, pushRequest())
		}
	}
	merge := func(t *testing.T, withoutHooks bool) result.Result {
		t.Helper()

		set := setting{script: toLand(nil, nil)}
		if withoutHooks {
			set.rewire = func(_ *testing.T, s *scene) { bare(&s.deps) }
		}

		return runMergeOn(t, set).got
	}
	tests := []struct {
		run  func(t *testing.T, withoutHooks bool) result.Result
		name string
	}{
		{
			name: "a first push whose check is still running at the first read",
			run:  push(opening(running(), passing())),
		},
		{
			name: "a push that waits through a merge state unknown at the first read",
			run: push(reusing(passing()).showing(
				inState(forge.MergeUnknown, "UNKNOWN"), inState(forge.MergeClean, "CLEAN"))),
		},
		{name: "a merge that lands", run: merge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				watched := tt.run(t, false)

				unwatched := tt.run(t, true)

				want, err := result.RenderJSON(watched, "")
				assert.NilError(t, err)

				got, err := result.RenderJSON(unwatched, "")
				assert.NilError(t, err)

				assert.Check(t, is.Equal(got, want))
			})
		})
	}
}

// sharedFailedReads are the failed reads of the branch's tip and of the commits a merged pull
// request from the branch already carries.
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
