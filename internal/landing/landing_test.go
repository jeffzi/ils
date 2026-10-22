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

	text := assertBrokeReadBy(t, got, pushCommand(), read)
	assert.Check(t, is.Contains(text, detail))
}

// assertBrokeReadBy fails unless got is broke on the read named read for the command cmd: it
// carries cmd's command and words, says nothing was written, and ends on cmd's `Next:` line for a
// fix. It returns the text got renders.
func assertBrokeReadBy(t *testing.T, got result.Result, cmd landingCommand, read string) string {
	t.Helper()

	assertStoppedBy(t, got, result.Broke, cmd.req)

	text, reason := shown(t, got, "")
	prefix := "Could not read " + read + ": "
	suffix := "\nNothing was written.\n" + cmd.fixed

	assert.Check(t, strings.HasPrefix(text, prefix), "RenderText() = %q, want it to start with %q", text, prefix)
	assert.Check(t, strings.HasSuffix(text, suffix), "RenderText() = %q, want it to end with %q", text, suffix)

	assert.Check(t, is.Equal(reason, "could not read "+read))

	return text
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

// httpsURL names Acme/Widgets over HTTPS, which git reads without asking ssh.
const httpsURL = "https://github.com/acme/widgets.git"

// missingProgram is an ssh command that no PATH holds.
const missingProgram = "no-such-ssh"

// sshCommandVar is the environment variable the fixture sets to an ssh command of its own.
const sshCommandVar = "GIT_SSH_COMMAND"

// without returns a copy of env with every entry of key removed.
func without(env []string, key string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")

		return strings.EqualFold(name, key)
	})
}

// commandMissing is the whole text of the refusal of an ssh command, configured by setting, that
// runs missingProgram.
func commandMissing(setting string) string {
	return "Refused: " + missingProgram + ", the ssh command " + setting + " names, is not found\n" +
		"Next: fix " + setting + ", then run ils push again\n"
}

// sshMissing is a refusal of a push whose ssh command, or ssh itself, is not installed.
type sshMissing struct {
	arrange func(*gittest.Fixture)
	// env receives the scene's runner environment and returns the one the runner is given. A row
	// removes or replaces GIT_SSH_COMMAND or PATH in it to set up the case.
	env  func(t *testing.T, env []string) []string
	name string
	want string
}

// sshMissingRefusals are the refusals of a push whose ssh command, or ssh itself, is not installed.
func sshMissingRefusals() []sshMissing {
	return []sshMissing{
		{
			name:    "GIT_SSH_COMMAND runs a missing program for the push URL",
			arrange: func(f *gittest.Fixture) { f.SetOriginURL(httpsURL) },
			env: func(_ *testing.T, env []string) []string {
				return append(without(env, sshCommandVar), sshCommandVar+"="+missingProgram)
			},
			want: commandMissing(sshCommandVar),
		},
		{
			name: "core.sshCommand runs a missing program for the fetch URL",
			arrange: func(f *gittest.Fixture) {
				f.SetPushURL(httpsURL)
				f.Git("config", "--local", "core.sshCommand", missingProgram)
			},
			env: func(_ *testing.T, env []string) []string {
				return without(env, sshCommandVar)
			},
			want: commandMissing("core.sshCommand"),
		},
		{
			name: "GIT_SSH runs a missing program for both URLs",
			env: func(_ *testing.T, env []string) []string {
				return append(without(env, sshCommandVar), "GIT_SSH="+missingProgram)
			},
			want: commandMissing("GIT_SSH"),
		},
		{
			name: "plain ssh is not installed",
			env: func(t *testing.T, env []string) []string {
				t.Helper()

				return append(without(without(env, sshCommandVar), "PATH"), "PATH="+t.TempDir())
			},
			want: "Refused: ssh is not installed; install it from https://www.openssh.com\n" +
				"Next: install ssh, then run ils push again\n",
		},
	}
}

func TestPreconditions_WhenSSHIsMissing_RefusesNamingWhatToFix(t *testing.T) {
	t.Parallel()
	tests := sshMissingRefusals()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, tt.arrange, forgetest.Script{}, false)
			s.deps.Repo.Runner.Env = tt.env(t, s.deps.Repo.Runner.Env)
			before := s.refs()

			_, got, ok := landing.Preconditions(t.Context(), s.deps, pushRequest())

			assert.Assert(t, !ok, "Preconditions() ok = true, want a refusal")
			assertStopped(t, got, result.Refused)

			text, _ := shown(t, got, s.gitDir)
			assert.Check(t, is.DeepEqual(text, tt.want))

			assertRefsUnchanged(t, s, before)
			assertAsked(t, s, nil)
		})
	}
}

func TestPreconditions_WhenForgeIdentityFailsUntyped_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, fails(errors.New("HTTP 502")), false)
	before := s.refs()

	_, got, ok := landing.Preconditions(t.Context(), s.deps, pushRequest())

	assert.Assert(t, !ok, "Preconditions() ok = true, want a broke result")
	assertBrokeRead(t, got, readIdentity, "HTTP 502")

	text, _ := shown(t, got, s.gitDir)
	assert.Check(t, is.DeepEqual(text, unreadText(pushCommand(), readIdentity, "HTTP 502")))

	assertRefsUnchanged(t, s, before)
	assertAsked(t, s, identityRead())

	assert.Check(t, !s.gitRan("ls-remote"), "origin was asked for its trunk after the identity read failed, want no later read")
}

func TestPreconditions_WhenDirectoryIsNotARepository_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()

	for _, cmd := range landingCommands() {
		t.Run(cmd.name, func(t *testing.T) {
			t.Parallel()
			deps := landing.Deps{
				Repo:  git.Repo{Runner: git.Runner{Dir: t.TempDir(), Env: gittest.Env(t)}},
				Forge: forgetest.New(t, forgetest.Script{}),
			}

			_, got, ok := landing.Preconditions(t.Context(), deps, cmd.req)

			assert.Assert(t, !ok, "Preconditions() ok = true, want a broke result")
			assertBrokeReadBy(t, got, cmd, readStanding)
		})
	}
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

	for _, cmd := range landingCommands() {
		for _, tt := range unfinishedReads() {
			t.Run(cmd.name+" "+tt.name, func(t *testing.T) {
				t.Parallel()
				ctx, script := tt.arrange(t)
				s := newScene(t, nil, script, false)
				before := s.refs()

				_, got, ok := landing.Preconditions(ctx, s.deps, cmd.req)

				assert.Assert(t, !ok, "Preconditions() ok = true, want a refusal")
				assertStoppedBy(t, got, result.Refused, cmd.req)

				text, _ := shown(t, got, s.gitDir)
				assert.Check(t, is.DeepEqual(text, overtimeText(cmd, tt.read)))

				assertRefsUnchanged(t, s, before)
				assertAsked(t, s, tt.ops)
			})
		}
	}
}

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

// overtimeText is the whole text of cmd's refusal when the deadline ended in the read named read.
func overtimeText(cmd landingCommand, read string) string {
	return "Refused: the time limit ended while reading " + read + "; nothing was written\n" + cmd.again
}

// unreadText is the whole text of cmd's result when the read named read failed saying detail.
func unreadText(cmd landingCommand, read, detail string) string {
	return "Could not read " + read + ": " + detail + "\nNothing was written.\n" + cmd.fixed
}

// mergeStop is one stop of the preconditions of `ils merge`, with how it ends and the whole text
// it prints.
type mergeStop struct {
	// arrange returns the context to run under, once it has changed how the scene reads.
	arrange func(t *testing.T, s *scene) context.Context
	name    string
	want    string
	script  forgetest.Script
	outcome result.Outcome
	// offline gives the scene an origin that does not answer.
	offline bool
}

// withoutSSH is an arrange that leaves the scene's git no ssh to run.
func withoutSSH(t *testing.T, s *scene) context.Context {
	t.Helper()

	s.deps.Repo.Runner.Env = append(without(without(s.deps.Repo.Runner.Env, sshCommandVar), "PATH"), "PATH="+t.TempDir())

	return t.Context()
}

// sshMissingStops are the stops of `ils merge` whose ssh command, or ssh itself, is not installed.
func sshMissingStops() []mergeStop {
	return []mergeStop{
		{
			name: "plain ssh is not installed", outcome: result.Refused, arrange: withoutSSH,
			want: "Refused: ssh is not installed; install it from https://www.openssh.com\n" +
				"Next: install ssh, then run ils merge again\n",
		},
		{
			name: "GIT_SSH runs a missing program", outcome: result.Refused,
			arrange: func(t *testing.T, s *scene) context.Context {
				t.Helper()

				s.deps.Repo.Runner.Env = append(without(s.deps.Repo.Runner.Env, sshCommandVar), "GIT_SSH="+missingProgram)

				return t.Context()
			},
			want: "Refused: " + missingProgram + ", the ssh command GIT_SSH names, is not found\n" +
				"Next: fix GIT_SSH, then run ils merge again\n",
		},
	}
}

// forgeStops are the stops of `ils merge` decided from the forge's answer about the repository.
func forgeStops() []mergeStop {
	old := child.ToolTooOldError{
		Tool: "gh", Minimum: "2.50.0", InstallURL: "https://cli.github.com",
		Version: "2.49.0", Line: "gh version 2.49.0 (2024-05-13)",
	}

	return []mergeStop{
		{
			name: "gh is not installed", outcome: result.Refused,
			script: fails(&child.ToolMissingError{Tool: "gh", InstallURL: "https://cli.github.com"}),
			want: "Refused: gh is not installed; install it from https://cli.github.com\n" +
				"Next: install gh, then run ils merge again\n",
		},
		{
			name: "gh is older than the minimum", outcome: result.Refused, script: fails(&old),
			want: "Refused: gh 2.49.0 is older than 2.50.0, the oldest version ils works with; " +
				"upgrade it from https://cli.github.com\nNext: upgrade gh, then run ils merge again\n",
		},
		{
			name: "gh is not logged in", outcome: result.Refused, script: fails(&forge.NotLoggedInError{Host: host}),
			want: "Refused: gh is not logged in to github.com; log in with " +
				"`gh auth login --hostname github.com`, or push by hand if github.com is not GitHub\n" +
				"Next: log in to github.com, then run ils merge again\n",
		},
		{
			name: "the forge's identity read fails", outcome: result.Broke, script: fails(errors.New("HTTP 502")),
			want: unreadText(mergeCommand(), readIdentity, "HTTP 502"),
		},
	}
}

// unreadTrunkStops are the stops of `ils merge` whose trunk origin did not name.
func unreadTrunkStops() []mergeStop {
	want := unreached(mergeCommand(), "ask for its trunk")

	return []mergeStop{
		{name: "origin does not answer", outcome: result.Refused, script: allowed(), offline: true, want: want},
		{
			name: "origin's answer names no branch", outcome: result.Refused, script: allowed(), want: want,
			arrange: func(t *testing.T, s *scene) context.Context {
				t.Helper()
				s.fixture.DetachOriginHead()

				return t.Context()
			},
		},
	}
}

func TestPreconditions_WhenTheCommandIsMerge_NamesItInTheNextStep(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(sshMissingStops(), forgeStops(), unreadTrunkStops()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, nil, tt.script, tt.offline)

			ctx := t.Context()
			if tt.arrange != nil {
				ctx = tt.arrange(t, s)
			}

			_, got, ok := landing.Preconditions(ctx, s.deps, mergeRequest())

			assert.Assert(t, !ok, "Preconditions() ok = true, want a stop")
			assert.Check(t, is.Equal(got.Outcome, tt.outcome))
			assert.Check(t, is.Equal(got.Command, "merge"))

			text, _ := shown(t, got, s.gitDir)
			assert.Check(t, is.DeepEqual(text, tt.want))
		})
	}
}

const (
	nextBranch = "Next: start a branch from origin's trunk: `git fetch origin " + trunk +
		" && git switch -c <name> --no-track origin/" + trunk + "`"

	readTree         = "the working tree's changes"
	readFetchedTrunk = "the commits on origin's trunk"
	readMergedHead   = "the merged pull request's head"
	readTip          = "the branch's tip"
)

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
		cmd.after("close it or change its base with `gh pr edit 12 --base "+trunk+"`")
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

func TestPush_WhenDeadlineEndsDuringASharedRead_RefusesNamingTheRead(t *testing.T) {
	t.Parallel()

	runOvertime(t, []overtime{
		{
			// Origin answers the question about its trunk, and nothing after.
			name: "during the fetch of origin's trunk", read: readFetchedTrunk, script: allowed(), ops: identityRead(),
			arrange: stalling(1),
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
	})
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
				f.CorruptIndex()

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
	s := newScene(t, func(f *gittest.Fixture) { f.Git("switch", "--quiet", "--orphan", spare) }, allowed(), false)
	words := []string{"show-ref", "--verify", "--quiet", "refs/heads/" + spare}
	failGit(t, s, words)

	before := s.refs()

	got := landing.Push(t.Context(), s.deps, pushRequest())

	assertEnded(t, s, got, finish{outcome: result.Broke, text: failedGitText(pushCommand(), readTip, words)})

	assert.Check(t, is.Equal(s.refs(), before))

	assertAsked(t, s, identityRead())
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

func TestPush_WhenASharedGitReadFails_BrokeNamingTheRead(t *testing.T) {
	// Not parallel: each row puts a failing git first on PATH with t.Setenv.
	runFailedReads(t, sharedFailedReads())
}
