package landing_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
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

	nextPush   = "Next: ils push\n"
	nextByHand = "Next: stop and tell the user to push by hand\n"
	nextOneURL = "Next: point both of origin's URLs at one GitHub repository with `git remote set-url`, by hand\n"
	nextFixed  = "Next: fix what failed, then run ils push again\n"

	readGitVersion = "git's version"
	readStanding   = "where HEAD stands"
	readURLs       = "origin's URLs"
	readIdentity   = "the repository's identity on GitHub"
	readTrunk      = "origin's trunk"

	unreachedText = "Refused: origin could not be reached to ask for its trunk\n" + nextPush

	noOriginText = "Refused: this repository has no remote named origin\n" +
		"Next: create the repository on GitHub and push to it with `gh repo create --source . --push`\n"
)

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
	return cmpopts.IgnoreFields(forge.Repo{}, "Fork", "CanPush")
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

// shown renders r through both renderers and returns the text and the reason the JSON carries.
func shown(t *testing.T, r result.Result, gitDir string) (text, reason string) {
	t.Helper()

	text, err := result.RenderText(r, gitDir)
	assert.NilError(t, err)

	out, err := result.RenderJSON(r, gitDir)
	assert.NilError(t, err)

	return text, resulttest.Decode(t, out).Reason
}

// assertStopped fails unless got ended as want and carries the command and words of pushRequest.
func assertStopped(t *testing.T, got result.Result, want result.Outcome) {
	t.Helper()

	assert.Check(t, is.Equal(got.Outcome, want))

	assert.Check(t, is.Equal(got.Command, "push"))

	assert.Check(t, is.DeepEqual(got.Words, []string{"push"}))
}

// assertBrokeRead fails unless got is broke on the read named read, says nothing was written,
// and carries detail in the line about the failure.
func assertBrokeRead(t *testing.T, got result.Result, read, detail string) {
	t.Helper()

	assertStopped(t, got, result.Broke)

	text, reason := shown(t, got, "")
	prefix := "Could not read " + read + ": "
	suffix := "\nNothing was written.\n" + nextFixed

	assert.Check(t, strings.HasPrefix(text, prefix), "RenderText() = %q, want it to start with %q", text, prefix)
	assert.Check(t, strings.HasSuffix(text, suffix), "RenderText() = %q, want it to end with %q", text, suffix)

	assert.Check(t, is.Contains(text, detail))

	assert.Check(t, is.Equal(reason, "could not read "+read))
}

// ready runs the preconditions of a push on the scene and fails unless every one holds.
func ready(t *testing.T, s *scene) landing.Ready {
	t.Helper()

	got, stop, ok := landing.Preconditions(t.Context(), s.deps, pushRequest())
	if !ok {
		text, _ := shown(t, stop, s.gitDir)
		t.Fatalf("Preconditions() ok = false with %q, want every precondition to hold", text)
	}

	return got
}

// assertRefsUnchanged fails unless every ref of the scene is as it was in before.
func assertRefsUnchanged(t *testing.T, s *scene, before string) {
	t.Helper()

	assert.Check(t, is.Equal(s.refs(), before))
}

// refusal is one state that the preconditions refuse, with what they print and how far they read.
type refusal struct {
	arrange func(*gittest.Fixture)
	name    string
	// want is the whole text of the refusal.
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
		return "Refused: origin's fetch URL " + fetch + " and push URL " + push +
			" do not name one GitHub repository\n" + nextOneURL
	}

	return []refusal{
		{
			name:    "no remote named origin",
			arrange: (*gittest.Fixture).RemoveOrigin,
			want:    noOriginText,
		},
		{
			name:    "fetch URL does not parse",
			arrange: func(f *gittest.Fixture) { f.SetOriginURL(localPath) },
			want:    mismatch(localPath, pushURL),
		},
		{
			name:    "push URL does not parse",
			arrange: func(f *gittest.Fixture) { f.SetPushURL(localPath) },
			want:    mismatch(fetchURL, localPath),
		},
		{
			name:    "URLs name different repositories",
			arrange: func(f *gittest.Fixture) { f.SetPushURL(otherURL) },
			want:    mismatch(fetchURL, otherURL),
		},
		{
			name:    "URLs name different repositories and origin does not answer",
			arrange: func(f *gittest.Fixture) { f.SetPushURL(otherURL) },
			offline: true,
			want:    mismatch(fetchURL, otherURL),
		},
	}
}

// forgeRefusals are the refusals decided from the forge's answer about the repository.
func forgeRefusals() []refusal {
	const (
		noTool = "Refused: gh is not installed; install it from https://cli.github.com\n" +
			"Next: install gh, then run ils push again\n"
		noLogin = "Refused: gh is not logged in to github.com; log in with " +
			"`gh auth login --hostname github.com`, or push by hand if github.com is not GitHub\n" +
			"Next: log in to github.com, then run ils push again\n"
		upgrade = "Next: upgrade gh, then run ils push again\n"
		tooOld  = "Refused: gh 2.49.0 is older than 2.50.0, the oldest version ils works with; " +
			"upgrade it from https://cli.github.com\n" + upgrade
		noVersion = "Refused: gh printed `gh version DEV`, which names no version ils can read; " +
			"ils needs gh 2.50.0 or newer, from https://cli.github.com\n" + upgrade
		isFork = "Refused: Acme/Widgets is a fork, which ils does not push to; the user pushes by hand\n" +
			nextByHand
		noPush = "Refused: the logged-in user has no push permission on Acme/Widgets; " +
			"the user pushes by hand\n" + nextByHand
	)

	missing := fails(&child.ToolMissingError{Tool: "gh", InstallURL: "https://cli.github.com"})
	old := child.ToolTooOldError{Tool: "gh", Minimum: "2.50.0", InstallURL: "https://cli.github.com"}
	outdated, unread := old, old
	outdated.Version, outdated.Line = "2.49.0", "gh version 2.49.0 (2024-05-13)"
	unread.Line = "gh version DEV"

	return []refusal{
		{name: "gh is not installed", script: missing, asksForge: true, want: noTool},
		{name: "gh is not installed and origin does not answer", script: missing, offline: true, asksForge: true, want: noTool},
		{name: "gh is older than the minimum", script: fails(&outdated), asksForge: true, want: tooOld},
		{name: "gh prints no version", script: fails(&unread), asksForge: true, want: noVersion},
		{name: "gh is not logged in", script: fails(&forge.NotLoggedInError{Host: host}), asksForge: true, want: noLogin},
		{name: "fork", script: answers(widgets(true, true)), asksForge: true, want: isFork},
		{name: "fork without push permission", script: answers(widgets(true, false)), asksForge: true, want: isFork},
		{name: "no push permission", script: answers(widgets(false, false)), asksForge: true, want: noPush},
		{
			name:   "no push permission and origin does not answer",
			script: answers(widgets(false, false)), offline: true, asksForge: true, want: noPush,
		},
	}
}

// trunkRefusals are the refusals decided from origin's answer about its trunk.
func trunkRefusals() []refusal {
	return []refusal{
		{
			name:   "origin does not answer",
			script: allowed(), offline: true, asksForge: true, asksOrigin: true, want: unreachedText,
		},
		{
			name:    "origin's answer names no branch",
			arrange: (*gittest.Fixture).DetachOriginHead,
			script:  allowed(), asksForge: true, asksOrigin: true, want: unreachedText,
		},
	}
}

func TestPreconditions_WhenOneFails_RefusesAtTheFirst(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(originRefusals(), forgeRefusals(), trunkRefusals()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, tt.arrange, tt.script, tt.offline)
			before := s.refs()

			_, got, ok := landing.Preconditions(t.Context(), s.deps, pushRequest())

			assert.Assert(t, !ok, "Preconditions() ok = true, want a refusal")
			assertStopped(t, got, result.Refused)

			text, _ := shown(t, got, s.gitDir)
			assert.Check(t, is.DeepEqual(text, tt.want))

			assertRefsUnchanged(t, s, before)
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
	tests := []struct {
		arrange func(*gittest.Fixture)
		name    string
		want    git.Standing
	}{
		{name: "on a branch", want: git.Standing{Kind: git.OnBranch, Branch: topic}},
		{name: "on a detached HEAD", arrange: (*gittest.Fixture).Detach, want: git.Standing{Kind: git.Detached}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, tt.arrange, allowed(), false)

			got := ready(t, s)

			want := landing.Ready{Standing: tt.want, Repo: widgets(false, true), Trunk: trunk}
			assert.Check(t, is.DeepEqual(got, want))

			assertAsked(t, s, identityRead())
		})
	}
}

func TestPreconditions_WhenFetchURLUsesAnSSHAliasOfThePushURLHost_AsksTheForgeAboutThatHost(t *testing.T) {
	t.Parallel()
	const alias = "Host github-personal\n  HostName github.com\n"
	arrange := func(f *gittest.Fixture) { f.SetOriginURL("git@github-personal:Acme/Widgets.git") }
	s := newScene(t, arrange, allowed(), false)
	s.fixture.SetSSHConfig(alias)

	ready(t, s)

	assertAsked(t, s, identityRead())
}

func TestPreconditions_WhenSSHFails_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, forgetest.Script{}, false)
	s.fixture.RemoveSSHConfig()

	_, got, ok := landing.Preconditions(t.Context(), s.deps, pushRequest())

	assert.Assert(t, !ok, "Preconditions() ok = true, want a broke result")
	assertBrokeRead(t, got, readURLs, readURLs+": ssh ")
	assertAsked(t, s, nil)
}

func TestPreconditions_WhenForgeIdentityFailsUntyped_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, fails(errors.New("HTTP 502")), false)
	before := s.refs()

	_, got, ok := landing.Preconditions(t.Context(), s.deps, pushRequest())

	assert.Assert(t, !ok, "Preconditions() ok = true, want a broke result")
	assertBrokeRead(t, got, readIdentity, "HTTP 502")

	text, _ := shown(t, got, s.gitDir)
	want := "Could not read the repository's identity on GitHub: HTTP 502\nNothing was written.\n" + nextFixed
	assert.Check(t, is.DeepEqual(text, want))

	assertRefsUnchanged(t, s, before)
	assertAsked(t, s, identityRead())

	assert.Check(t, !s.gitRan("ls-remote"), "origin was asked for its trunk after the identity read failed, want no later read")
}

func TestPreconditions_WhenDirectoryIsNotARepository_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()
	deps := landing.Deps{
		Repo:  git.Repo{Runner: git.Runner{Dir: t.TempDir(), Env: gittest.Env(t)}},
		Forge: forgetest.New(t, forgetest.Script{}),
	}

	_, got, ok := landing.Preconditions(t.Context(), deps, pushRequest())

	assert.Assert(t, !ok, "Preconditions() ok = true, want a broke result")
	assertBrokeRead(t, got, readStanding, "not a git repository")
}

// noGit is the whole text of the refusal of a git that is missing from PATH.
const noGit = "Refused: git is not installed; install it from https://git-scm.com/downloads\n" +
	"Next: install git, then run ils push again\n"

// hideGit empties PATH for the whole process until the test ends, so that the next git call finds
// no git. It cannot run in a parallel test.
func hideGit(t *testing.T) {
	t.Helper()

	t.Setenv("PATH", t.TempDir())
}

// hidingGitAt returns script with a hook that hides git once the forge is asked for op, so that
// the git read after that call is the first to find no git.
func hidingGitAt(t *testing.T, script forgetest.Script, op forgetest.Op) forgetest.Script {
	t.Helper()

	script.OnRead = func(call forgetest.Call) {
		if call.Op == op {
			hideGit(t)
		}
	}

	return script
}

func TestPreconditions_WhenGitMissingFromPath_RefusesWithAnInstallHint(t *testing.T) {
	// Not parallel: each row empties PATH for the whole process while it runs.
	tests := []struct {
		// arrange returns the forge's script, having hidden git or set the script up to.
		arrange func(t *testing.T) forgetest.Script
		name    string
		ops     []forgetest.Op
	}{
		{
			name: "at the first read",
			arrange: func(t *testing.T) forgetest.Script {
				t.Helper()
				hideGit(t)

				return forgetest.Script{}
			},
		},
		{
			name: "at the read of origin's trunk", ops: identityRead(),
			arrange: func(t *testing.T) forgetest.Script {
				t.Helper()

				return hidingGitAt(t, allowed(), forgetest.OpIdentity)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScene(t, nil, forgetest.Script{}, false)
			s.useForge(t, tt.arrange(t))

			_, got, ok := landing.Preconditions(t.Context(), s.deps, pushRequest())

			assert.Assert(t, !ok, "Preconditions() ok = true, want a refusal")
			assertStopped(t, got, result.Refused)

			text, _ := shown(t, got, s.gitDir)
			assert.Check(t, is.DeepEqual(text, noGit))

			assertAsked(t, s, tt.ops)
		})
	}
}

func TestPreconditions_WhenGitIsTooOld_RefusesBeforeAnyOtherRead(t *testing.T) {
	// Not parallel: fakeGit puts a git that prints its own version first on PATH with t.Setenv.
	const want = "Refused: git 2.34.0 is older than 2.34.1, the oldest version ils works with; " +
		"upgrade it from https://git-scm.com/downloads\n" +
		"Next: upgrade git, then run ils push again\n"

	s := newScene(t, nil, forgetest.Script{}, false)
	fakeGit(t, s, gittest.Fake{Version: "git version 2.34.0"})
	before := s.refs()

	_, got, ok := landing.Preconditions(t.Context(), s.deps, pushRequest())

	assert.Assert(t, !ok, "Preconditions() ok = true, want a refusal")
	assertEnded(t, s, got, finish{outcome: result.Refused, text: want})

	assertRefsUnchanged(t, s, before)
	assertAsked(t, s, nil)

	assert.Check(t, is.DeepEqual(s.recorder.Calls(), [][]string{{"git", "--version"}}))
}

func TestPreconditions_WhenGitVersionFails_BrokeNamingTheRead(t *testing.T) {
	// Not parallel: failGit puts a git that fails first on PATH with t.Setenv.
	s := newScene(t, nil, forgetest.Script{}, false)
	failGit(t, s, []string{"--version"})

	_, got, ok := landing.Preconditions(t.Context(), s.deps, pushRequest())

	assert.Assert(t, !ok, "Preconditions() ok = true, want a broke result")
	assertBrokeRead(t, got, readGitVersion, gittest.FailedSaid)
	assertAsked(t, s, nil)
}

// unfinished is a deadline that ends during one read of the preconditions.
type unfinished struct {
	// arrange returns the context to run under and the forge's script.
	arrange func(t *testing.T) (context.Context, forgetest.Script)
	name    string
	read    string
	// ops are the forge operations made before the refusal.
	ops []forgetest.Op
}

func unfinishedReads() []unfinished {
	return []unfinished{
		{
			name: "before the first read",
			read: readGitVersion,
			arrange: func(t *testing.T) (context.Context, forgetest.Script) {
				t.Helper()

				ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
				t.Cleanup(cancel)

				return ctx, forgetest.Script{}
			},
		},
		{
			name: "during the forge's identity read",
			read: readIdentity,
			ops:  identityRead(),
			arrange: func(t *testing.T) (context.Context, forgetest.Script) {
				t.Helper()

				return t.Context(), fails(fmt.Errorf("gh repo view: %w", context.DeadlineExceeded))
			},
		},
		{
			name: "between the identity read and the trunk read",
			read: readTrunk,
			ops:  identityRead(),
			arrange: func(t *testing.T) (context.Context, forgetest.Script) {
				t.Helper()

				ended := make(chan struct{})
				script := allowed()
				script.OnRead = func(forgetest.Call) { close(ended) }

				return gittest.Ending(t.Context(), ended, context.DeadlineExceeded), script
			},
		},
	}
}

func TestPreconditions_WhenDeadlineEndsDuringRead_RefusesNamingTheRead(t *testing.T) {
	t.Parallel()

	for _, tt := range unfinishedReads() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, script := tt.arrange(t)
			s := newScene(t, nil, script, false)
			before := s.refs()

			_, got, ok := landing.Preconditions(ctx, s.deps, pushRequest())

			assert.Assert(t, !ok, "Preconditions() ok = true, want a refusal")
			assertStopped(t, got, result.Refused)

			text, _ := shown(t, got, s.gitDir)
			want := "Refused: the time limit ended while reading " + tt.read + "; nothing was written\n" + nextPush
			assert.Check(t, is.DeepEqual(text, want))

			assertRefsUnchanged(t, s, before)
			assertAsked(t, s, tt.ops)
		})
	}
}
