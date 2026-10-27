package landing_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
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

	readGitVersion = "git's version"
	readStanding   = "where HEAD stands"
	readURLs       = "origin's URLs"
	readIdentity   = "the repository's identity on GitHub"
	readTrunk      = "origin's trunk"
)

// partEnd ends each part of an expected text built by expect, so that expected texts concatenate
// with + and still split into their parts.
const partEnd = "\x00"

// expect is the expected text made of parts: what the code computes and the phrase that tells its
// branch apart, in the order the text holds them. The fixed wording around them stays free to
// change.
func expect(parts ...string) string {
	var text strings.Builder
	for _, part := range parts {
		text.WriteString(part + partEnd)
	}

	return text.String()
}

// nextPush is the `Next:` line that runs `ils push` again as it is, and nextFixed the one that runs
// it again once what failed is fixed.
func nextPush() string {
	return expect("Next: ils push\n")
}

func nextFixed() string {
	return expect("fix what failed", "ils push")
}

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

// written returns everything a push could change: the local branches, where HEAD is, and every
// ref of origin.
func (s *scene) written() string {
	return s.fixture.Git("for-each-ref", "refs/heads") + "\nHEAD: " + s.fixture.Git("rev-parse", "HEAD") +
		"\norigin:\n" + s.fixture.OriginGit("for-each-ref")
}

// stopAnswering makes origin answer the first times calls git makes to it and fail every call after.
func (s *scene) stopAnswering(t *testing.T, times int) {
	t.Helper()

	s.deps.Repo.Runner.Env = answering(t, s.fixture, times, "true")
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
	return forgetest.Script{Identity: forgetest.Values(repo)}
}

// allowed is a script whose identity call says the user can push to Acme/Widgets.
func allowed() forgetest.Script {
	return answers(widgets(false, true))
}

// fails is a script whose identity call returns err.
func fails(err error) forgetest.Script {
	return forgetest.Script{Identity: []forgetest.Answer[forge.Repo]{{Err: err}}}
}

// shown renders r through both renderers and returns the text and the reason the JSON carries.
func shown(t *testing.T, r result.Result, gitDir string) (text, reason string) {
	t.Helper()

	text, err := result.RenderText(r, gitDir)
	assert.NilError(t, err)

	out, err := result.RenderJSON(r, gitDir)
	assert.NilError(t, err)

	return text, resulttest.Decode(t, out).Reason
}

// assertSays fails unless text holds each part of want, as expect builds it, in order, each after the
// one before it.
func assertSays(t *testing.T, text, want string) {
	t.Helper()

	assert.Check(t, holds(text, want), "text %q, want it to hold %q in order", text, partsOf(want))
}

// holds reports whether text holds each part of want in order, each after the one before it.
func holds(text, want string) bool {
	_, found := past(text, want)

	return found
}

// past returns what text holds after the last part of want, once each part of want is found in
// order, and whether every part was.
func past(text, want string) (string, bool) {
	rest := text
	for _, part := range partsOf(want) {
		_, after, found := strings.Cut(rest, part)
		if !found {
			return "", false
		}

		rest = after
	}

	return rest, true
}

// partsOf returns the parts of want, as expect builds it; a text expect did not build is one part.
func partsOf(want string) []string {
	parts := strings.Split(want, partEnd)

	return slices.DeleteFunc(parts, func(part string) bool { return part == "" })
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

// ended is how a run must end: the request it ran, its outcome, and the parts its text holds.
type ended struct {
	text    string
	req     landing.Request
	outcome result.Outcome
}

// assertEndedBy fails unless got ended as want says, carries the command and words of its request,
// and renders text in gitDir that holds the parts of its text.
func assertEndedBy(t *testing.T, got result.Result, gitDir string, want ended) {
	t.Helper()

	assertStoppedBy(t, got, want.outcome, want.req)

	rendered, _ := shown(t, got, gitDir)
	assertSays(t, rendered, want.text)
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
	said := between(t, text, read, nothingWritten)
	assertSays(t, text, expect(read, nothingWritten)+cmd.fixed)

	assert.Check(t, is.Contains(reason, read))

	return said
}

// nothingWritten is the phrase of the line that says a run that stopped early wrote nothing.
const nothingWritten = "Nothing was written"

// between returns what text holds after the first before and ahead of the next after, without the
// separators around it, and fails unless text holds both and something between them.
func between(t *testing.T, text, before, after string) string {
	t.Helper()

	_, rest, starts := strings.Cut(text, before)
	said, _, ends := strings.Cut(rest, after)
	said = strings.Trim(said, ":;,. \n")
	assert.Check(t, starts && ends && said != "",
		"RenderText() = %q, want %q, then what failed said, then %q", text, before, after)

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

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

// mergeRequest is the command and words of a bare `ils merge`.
func mergeRequest() landing.Request {
	return landing.Request{Command: "merge", Words: []string{"merge"}}
}

// landingCommand is a command that runs the shared preconditions, with the parts of the `Next:`
// lines that name it.
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
	return expect(step, "ils "+c.name)
}

// pushCommand is `ils push` with the `Next:` lines that name it.
func pushCommand() landingCommand {
	return landingCommand{name: "push", req: pushRequest(), again: nextPush(), fixed: nextFixed()}
}

// mergeCommand is `ils merge` with the `Next:` lines that name it.
func mergeCommand() landingCommand {
	return landingCommand{name: "merge", req: mergeRequest(), again: nextMergeAgain(), fixed: nextMergeFixed()}
}

// unreached is the parts of cmd's refusal when origin stopped answering while it was asked to do
// what.
func unreached(cmd landingCommand, what string) string {
	return expect("could not be reached", what) + cmd.again
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

// stalling returns an arrange under which origin answers the first answered calls git makes to it
// and the next reaches a server that never answers; c ends the context once that call connected.
func (c cut) stalling(answered int) func(t *testing.T, s *scene) context.Context {
	return func(t *testing.T, s *scene) context.Context {
		t.Helper()

		addr, connected := gittest.SilentListener(t)
		s.deps.Repo.Runner.Env = answering(t, s.fixture, answered, "git ls-remote git://"+addr+"/stalled.git")

		return c.once(t.Context(), connected)
	}
}

// overtimeText is the parts of cmd's refusal when c ended the read named read.
func overtimeText(cmd landingCommand, c cut, read string) string {
	return expect(c.ends, read, "nothing was written") + cmd.again
}

// unreadText is the parts of cmd's result when the read named read failed saying detail.
func unreadText(cmd landingCommand, read, detail string) string {
	return expect(read, detail, nothingWritten) + cmd.fixed
}

func TestPush_WhenGivenNoProgressHookOrLogger_EndsAsWithThem(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// A first push whose check is still running at the first read reaches every place a hook
		// or the logger is told something.
		run := func(withoutHooks bool) result.Result {
			d := newDelivery(t, pushRow{script: opening(running(), passing())})
			if withoutHooks {
				d.deps.Progress, d.deps.Logger = nil, nil
			}

			return landing.Push(d.ctx, d.deps, pushRequest())
		}
		watched := run(false)

		unwatched := run(true)

		want, err := result.RenderJSON(watched, "")
		assert.NilError(t, err)
		got, err := result.RenderJSON(unwatched, "")
		assert.NilError(t, err)
		assert.Check(t, is.Equal(got, want))
	})
}

// ---------------------------------------------------------------------------
// Refusals of the shared preconditions
// ---------------------------------------------------------------------------

// refusal is one state that the preconditions refuse, with what they print and how far they read.
type refusal struct {
	arrange func(*gittest.Fixture)
	name    string
	// want is the parts of the refusal's text.
	want   string
	script forgetest.Script
	// offline gives the scene an origin that does not answer.
	offline bool
	// asksForge is whether the forge's identity is read before the refusal.
	asksForge bool
	// asksOrigin is whether origin is asked for its trunk before the refusal.
	asksOrigin bool
}

// originRefusals are the refusals decided from origin's URLs alone; their forge has no answer,
// so that any call to it fails the test.
func originRefusals() []refusal {
	mismatch := func(fetch, push string) string {
		return expect(fetch, push, "do not name one GitHub repository", "`git remote set-url`")
	}

	return []refusal{
		{
			name:    "no remote named origin",
			arrange: (*gittest.Fixture).RemoveOrigin,
			want:    expect("no remote named origin", "`gh repo create --source . --push`"),
		},
		{
			name:    "fetch URL does not parse",
			arrange: func(f *gittest.Fixture) { f.SetOriginURL(localPath) },
			want:    mismatch(localPath, pushURL),
		},
		{
			name:    "URLs name different repositories",
			arrange: func(f *gittest.Fixture) { f.SetPushURL(otherURL) },
			want:    mismatch(fetchURL, otherURL),
		},
	}
}

// forgeRefusals are the refusals decided from the forge's answer about the repository.
func forgeRefusals() []refusal {
	push := pushCommand()
	noTool := expect("gh", "not installed", "https://cli.github.com") + push.after("install gh")
	noLogin := expect("not logged in", host, "`gh auth login --hostname github.com`") + push.after("log in to "+host)
	tooOld := expect("gh 2.49.0", "older than 2.50.0", "https://cli.github.com") + push.after("upgrade gh")
	noVersion := expect("`gh version DEV`", "names no version", "2.50.0", "https://cli.github.com") + push.after("upgrade gh")
	isFork := expect(owner+"/"+repoName, "is a fork", "push by hand")
	noPush := expect("no push permission", owner+"/"+repoName, "push by hand")

	missing := fails(&child.ToolMissingError{Tool: "gh", InstallURL: "https://cli.github.com"})
	old := child.ToolTooOldError{Tool: "gh", Minimum: "2.50.0", InstallURL: "https://cli.github.com"}
	outdated, unread := old, old
	outdated.Version, outdated.Line = "2.49.0", "gh version 2.49.0 (2024-05-13)"
	unread.Line = "gh version DEV"

	return []refusal{
		{name: "gh is not installed", script: missing, asksForge: true, want: noTool},
		{
			name: "gh is older than the minimum", script: fails(&outdated), asksForge: true, want: tooOld,
		},
		{name: "gh prints no version", script: fails(&unread), asksForge: true, want: noVersion},
		{name: "gh is not logged in", script: fails(&forge.NotLoggedInError{Host: host}), asksForge: true, want: noLogin},
		{name: "fork", script: answers(widgets(true, true)), asksForge: true, want: isFork},
		{name: "fork without push permission", script: answers(widgets(true, false)), asksForge: true, want: isFork},
		{name: "no push permission", script: answers(widgets(false, false)), asksForge: true, want: noPush},
	}
}

// trunkRefusals are the refusals decided from origin's answer about its trunk.
func trunkRefusals() []refusal {
	return []refusal{
		{
			name:   "origin does not answer",
			script: allowed(), offline: true, asksForge: true, asksOrigin: true, want: unreached(pushCommand(), "ask for its trunk"),
		},
		{
			name:    "origin's answer names no branch",
			arrange: (*gittest.Fixture).DetachOriginHead,
			script:  allowed(), asksForge: true, asksOrigin: true, want: unreached(pushCommand(), "ask for its trunk"),
		},
	}
}

func TestPreconditions_WhenOneFails_RefusesAtTheFirst(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(originRefusals(), forgeRefusals(), trunkRefusals()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, tt.arrange, tt.script, tt.offline)

			_, stop := landing.Preconditions(t.Context(), s.deps, pushRequest())

			got := stopped(t, stop, "a refusal")
			assertStopped(t, got, result.Refused)

			text, _ := shown(t, got, s.gitDir)
			assertSays(t, text, tt.want)

			assertReadsStoppedAt(t, s, tt)
		})
	}
}

func assertReadsStoppedAt(t *testing.T, s *scene, tt refusal) {
	t.Helper()

	var ops []forgetest.Op
	if tt.asksForge {
		ops = identityRead()
	}

	assertAsked(t, s, ops)

	assert.Check(t, is.Equal(s.gitRan("ls-remote"), tt.asksOrigin))
}

func TestPreconditions_WhenAllHold_ReturnsReady(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, allowed(), false)
	standing := git.Standing{Kind: git.OnBranch, Branch: topic}
	want := landing.Ready{Standing: standing, Repo: widgets(false, true), Trunk: trunk}

	got := ready(t, s)

	assert.Check(t, is.DeepEqual(got, want))
	assertAsked(t, s, identityRead())
}

func TestPreconditions_WhenSSHFails_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, forgetest.Script{}, false)
	s.fixture.RemoveSSHConfig()

	_, stop := landing.Preconditions(t.Context(), s.deps, pushRequest())

	got := stopped(t, stop, "a broke result")
	assertBrokeRead(t, got, readURLs, "ssh ")
}

// ---------------------------------------------------------------------------
// Missing ssh
// ---------------------------------------------------------------------------

// missingProgram is an ssh command that no PATH holds.
const missingProgram = "no-such-ssh"

// sshCommandVar is the environment variable the fixture sets to an ssh command of its own.
const sshCommandVar = "GIT_SSH_COMMAND"

// commandMissing is the parts of cmd's refusal of an ssh command, configured by setting, that runs
// missingProgram.
func commandMissing(cmd landingCommand, setting string) string {
	return expect(missingProgram, setting, "not found") + cmd.after("fix "+setting)
}

func TestPreconditions_WhenGitSSHRunsAMissingProgram_RefusesNamingIt(t *testing.T) {
	t.Parallel()
	cmd := pushCommand()
	s := newScene(t, nil, forgetest.Script{}, false)
	s.deps.Repo.Runner.Env = append(gittest.Without(s.deps.Repo.Runner.Env, sshCommandVar), "GIT_SSH="+missingProgram)

	_, stop := landing.Preconditions(t.Context(), s.deps, cmd.req)

	got := stopped(t, stop, "a refusal")
	assertStoppedBy(t, got, result.Refused, cmd.req)

	text, _ := shown(t, got, s.gitDir)
	assertSays(t, text, commandMissing(cmd, "GIT_SSH"))
}

// ---------------------------------------------------------------------------
// The git on PATH
// ---------------------------------------------------------------------------

func TestPreconditions_WhenGitIsTooOld_RefusesBeforeAnyOtherRead(t *testing.T) {
	// Not parallel: fakeGit puts a git that prints its own version first on PATH with t.Setenv.
	cmd := pushCommand()
	s := newScene(t, nil, forgetest.Script{}, false)
	fakeGit(t, s, gittest.Fake{Version: "git version 1.0.0"})

	_, stop := landing.Preconditions(t.Context(), s.deps, cmd.req)

	got := stopped(t, stop, "a refusal")
	assertStoppedBy(t, got, result.Refused, cmd.req)

	text, _ := shown(t, got, s.gitDir)
	assertSays(t, text, expect("git 1.0.0", "older than "+git.MinimumVersion, "https://git-scm.com/downloads")+
		cmd.after("upgrade git"))

	assert.Check(t, is.Len(s.recorder.Calls(), 1))
}

func TestPreconditions_WhenGitVersionFails_BrokeNamingTheRead(t *testing.T) {
	// Not parallel: failGit puts a git that fails first on PATH with t.Setenv.
	s := newScene(t, nil, forgetest.Script{}, false)
	failGit(t, s, []string{"--version"})

	_, stop := landing.Preconditions(t.Context(), s.deps, pushRequest())

	got := stopped(t, stop, "a broke result")
	assertBrokeRead(t, got, readGitVersion, gittest.FailedSaid)
}

// ---------------------------------------------------------------------------
// Unread preconditions
// ---------------------------------------------------------------------------

func TestPreconditions_WhenForgeIdentityFailsUntyped_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, fails(errors.New("HTTP 502")), false)

	_, stop := landing.Preconditions(t.Context(), s.deps, pushRequest())

	got := stopped(t, stop, "a broke result")
	assertStoppedBy(t, got, result.Broke, pushRequest())

	text, reason := shown(t, got, s.gitDir)
	assertSays(t, text, unreadText(pushCommand(), readIdentity, "HTTP 502"))
	assert.Check(t, is.Contains(reason, readIdentity))

	assertAsked(t, s, identityRead())
}

func TestPreconditions_WhenDirectoryIsNotARepository_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()
	deps := landing.Deps{
		Repo:  git.Repo{Runner: git.Runner{Dir: t.TempDir(), Env: gittest.Env(t)}},
		Forge: forgetest.New(t, forgetest.Script{}),
	}

	_, stop := landing.Preconditions(t.Context(), deps, pushRequest())

	got := stopped(t, stop, "a broke result")
	assertBrokeReadBy(t, got, pushCommand(), readStanding)
}

// unfinished is a cut that ends one read of the preconditions.
type unfinished struct {
	// arrange returns the context to run under and the forge's script, for the cut c.
	arrange func(t *testing.T, c cut) (context.Context, forgetest.Script)
	name    string
	read    string
	// ops are the forge operations made before the refusal.
	ops []forgetest.Op
}

func TestPreconditions_WhenAReadIsCutShort_RefusesNamingTheRead(t *testing.T) {
	t.Parallel()
	c := byTimeLimit()
	tests := []unfinished{
		{
			name: "before the first read", read: readGitVersion,
			arrange: func(t *testing.T, c cut) (context.Context, forgetest.Script) {
				t.Helper()

				return c.spent(t), forgetest.Script{}
			},
		},
		{
			name: "between the identity read and the trunk read", read: readTrunk, ops: identityRead(),
			arrange: func(t *testing.T, c cut) (context.Context, forgetest.Script) {
				t.Helper()

				ended := make(chan struct{})
				script := allowed()
				script.OnRead = func(forgetest.Call) { close(ended) }

				return c.once(t.Context(), ended), script
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, script := tt.arrange(t, c)
			s := newScene(t, nil, script, false)

			_, stop := landing.Preconditions(ctx, s.deps, pushRequest())

			got := stopped(t, stop, "a refusal")
			assertStopped(t, got, result.Refused)

			text, _ := shown(t, got, s.gitDir)
			assertSays(t, text, overtimeText(pushCommand(), c, tt.read))

			assertAsked(t, s, tt.ops)
		})
	}
}
