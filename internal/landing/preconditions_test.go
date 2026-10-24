package landing_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

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

// ---------------------------------------------------------------------------
// Refusals of the shared preconditions
// ---------------------------------------------------------------------------

// refusal is one state that the preconditions refuse, with what they print and how far they read.
type refusal struct {
	arrange func(*gittest.Fixture)
	name    string
	// want is the whole text of the refusal.
	want string
	// drawn are parts of the refusal and how a terminal with color draws them; empty leaves them
	// unchecked.
	drawn  []drawnPart
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
		{
			name: "gh is older than the minimum", script: fails(&outdated), asksForge: true, want: tooOld,
			drawn: []drawnPart{
				{within: "from https://cli.github.com", part: "https://cli.github.com", want: resulttest.URLLook()},
				{within: "run ils push again", part: "ils push", want: resulttest.NameLook()},
			},
		},
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

			_, stop := landing.Preconditions(t.Context(), s.deps, pushRequest())

			got := stopped(t, stop, "a refusal")
			assertStopped(t, got, result.Refused)

			text, _ := shown(t, got, s.gitDir)
			assert.Check(t, is.DeepEqual(text, tt.want))

			assertRefsUnchanged(t, s, before)
			assertReadsStoppedAt(t, s, tt)

			if len(tt.drawn) > 0 {
				assertDrawn(t, got, s.gitDir, tt.drawn)
			}
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

	_, stop := landing.Preconditions(t.Context(), s.deps, pushRequest())

	got := stopped(t, stop, "a broke result")
	assertBrokeRead(t, got, readURLs, "ssh ")
	assertAsked(t, s, nil)
}

// ---------------------------------------------------------------------------
// Merge stops
// ---------------------------------------------------------------------------

// mergeStop is one stop of the preconditions of `ils merge`, with how it ends and the whole text
// it prints.
type mergeStop struct {
	name    string
	want    string
	script  forgetest.Script
	outcome result.Outcome
}

// forgeStops are the stops of `ils merge` decided from the forge's answer about the repository
// whose `Next:` line no other test of `ils merge` reaches.
func forgeStops() []mergeStop {
	return []mergeStop{
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

func TestPreconditions_WhenTheCommandIsMerge_NamesItInTheNextStep(t *testing.T) {
	t.Parallel()

	for _, tt := range forgeStops() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, nil, tt.script, false)

			_, stop := landing.Preconditions(t.Context(), s.deps, mergeRequest())

			got := stopped(t, stop, "a stop")
			assert.Check(t, is.Equal(got.Outcome, tt.outcome))
			assert.Check(t, is.Equal(got.Command, "merge"))

			text, _ := shown(t, got, s.gitDir)
			assert.Check(t, is.DeepEqual(text, tt.want))
		})
	}
}

// ---------------------------------------------------------------------------
// Missing ssh
// ---------------------------------------------------------------------------

// httpsURL names Acme/Widgets over HTTPS, which git reads without asking ssh.
const httpsURL = "https://github.com/acme/widgets.git"

// missingProgram is an ssh command that no PATH holds.
const missingProgram = "no-such-ssh"

// sshCommandVar is the environment variable the fixture sets to an ssh command of its own.
const sshCommandVar = "GIT_SSH_COMMAND"

// commandMissing is the whole text of cmd's refusal of an ssh command, configured by setting, that
// runs missingProgram.
func commandMissing(cmd landingCommand, setting string) string {
	return "Refused: " + missingProgram + ", the ssh command " + setting + " names, is not found\n" + cmd.after("fix "+setting)
}

// sshMissing is a refusal of a command whose ssh command, or ssh itself, is not installed.
type sshMissing struct {
	arrange func(*gittest.Fixture)
	// env receives the scene's runner environment and returns the one the runner is given. A row
	// removes or replaces GIT_SSH_COMMAND or PATH in it to set up the case.
	env  func(t *testing.T, env []string) []string
	name string
	want string
	cmd  landingCommand
}

// missingGitSSH is an env that names missingProgram in GIT_SSH in place of the fixture's ssh command.
func missingGitSSH(_ *testing.T, env []string) []string {
	return append(gittest.Without(env, sshCommandVar), "GIT_SSH="+missingProgram)
}

// sshMissingRefusals are the refusals of a command whose ssh command, or ssh itself, is not
// installed: each way for a push, and one for a merge, whose `Next:` line names it.
func sshMissingRefusals() []sshMissing {
	return []sshMissing{
		{
			name:    "GIT_SSH_COMMAND runs a missing program for the push URL",
			arrange: func(f *gittest.Fixture) { f.SetOriginURL(httpsURL) },
			env: func(_ *testing.T, env []string) []string {
				return append(gittest.Without(env, sshCommandVar), sshCommandVar+"="+missingProgram)
			},
			cmd: pushCommand(), want: commandMissing(pushCommand(), sshCommandVar),
		},
		{
			name: "core.sshCommand runs a missing program for the fetch URL",
			arrange: func(f *gittest.Fixture) {
				f.SetPushURL(httpsURL)
				f.Git("config", "--local", "core.sshCommand", missingProgram)
			},
			env: func(_ *testing.T, env []string) []string {
				return gittest.Without(env, sshCommandVar)
			},
			cmd: pushCommand(), want: commandMissing(pushCommand(), "core.sshCommand"),
		},
		{
			name: "GIT_SSH runs a missing program for both URLs", env: missingGitSSH,
			cmd: pushCommand(), want: commandMissing(pushCommand(), "GIT_SSH"),
		},
		{
			name: "GIT_SSH runs a missing program for a merge", env: missingGitSSH,
			cmd: mergeCommand(), want: commandMissing(mergeCommand(), "GIT_SSH"),
		},
		{
			name: "plain ssh is not installed",
			env: func(t *testing.T, env []string) []string {
				t.Helper()

				return gittest.WithoutSSH(t, env)
			},
			cmd: pushCommand(),
			want: "Refused: ssh is not installed; install it from https://www.openssh.com\n" +
				pushCommand().after("install ssh"),
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

			_, stop := landing.Preconditions(t.Context(), s.deps, tt.cmd.req)

			got := stopped(t, stop, "a refusal")
			assertStoppedBy(t, got, result.Refused, tt.cmd.req)

			text, _ := shown(t, got, s.gitDir)
			assert.Check(t, is.DeepEqual(text, tt.want))

			assertRefsUnchanged(t, s, before)
			assertAsked(t, s, nil)
		})
	}
}

// ---------------------------------------------------------------------------
// The git on PATH
// ---------------------------------------------------------------------------

// noGit returns the whole text of the refusal of a git that is missing from PATH, for the command
// named command.
func noGit(command string) string {
	return "Refused: git is not installed; install it from https://git-scm.com/downloads\n" +
		"Next: install git, then run ils " + command + " again\n"
}

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
	type gitHidden struct {
		// arrange returns the forge's script, having hidden git or set the script up to.
		arrange func(t *testing.T) forgetest.Script
		name    string
		ops     []forgetest.Op
		cmd     landingCommand
	}

	tests := []gitHidden{{
		name: "push at the read of origin's trunk", cmd: pushCommand(), ops: identityRead(),
		arrange: func(t *testing.T) forgetest.Script {
			t.Helper()

			return hidingGitAt(t, allowed(), forgetest.OpIdentity)
		},
	}}
	for _, cmd := range landingCommands() {
		tests = append(tests, gitHidden{
			name: cmd.name + " at the first read", cmd: cmd,
			arrange: func(t *testing.T) forgetest.Script {
				t.Helper()
				hideGit(t)

				return forgetest.Script{}
			},
		})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScene(t, nil, forgetest.Script{}, false)
			s.useForge(t, tt.arrange(t))

			_, stop := landing.Preconditions(t.Context(), s.deps, tt.cmd.req)

			got := stopped(t, stop, "a refusal")
			assertStoppedBy(t, got, result.Refused, tt.cmd.req)

			text, _ := shown(t, got, s.gitDir)
			assert.Check(t, is.DeepEqual(text, noGit(tt.cmd.name)))

			assertAsked(t, s, tt.ops)
		})
	}
}

func TestPreconditions_WhenGitIsTooOld_RefusesBeforeAnyOtherRead(t *testing.T) {
	// Not parallel: fakeGit puts a git that prints its own version first on PATH with t.Setenv.
	const tooOld = "Refused: git 1.0.0 is older than " + git.MinimumVersion + ", the oldest version ils works with; " +
		"upgrade it from https://git-scm.com/downloads\n"

	for _, cmd := range landingCommands() {
		t.Run(cmd.name, func(t *testing.T) {
			s := newScene(t, nil, forgetest.Script{}, false)
			fakeGit(t, s, gittest.Fake{Version: "git version 1.0.0"})
			before := s.refs()

			_, stop := landing.Preconditions(t.Context(), s.deps, cmd.req)

			got := stopped(t, stop, "a refusal")
			assertStoppedBy(t, got, result.Refused, cmd.req)

			text, _ := shown(t, got, s.gitDir)
			assert.Check(t, is.DeepEqual(text, tooOld+"Next: upgrade git, then run ils "+cmd.name+" again\n"))

			assertRefsUnchanged(t, s, before)
			assertAsked(t, s, nil)

			assert.Check(t, is.Len(s.recorder.Calls(), 1))
		})
	}
}

func TestPreconditions_WhenGitVersionFails_BrokeNamingTheRead(t *testing.T) {
	// Not parallel: failGit puts a git that fails first on PATH with t.Setenv.
	s := newScene(t, nil, forgetest.Script{}, false)
	failGit(t, s, []string{"--version"})

	_, stop := landing.Preconditions(t.Context(), s.deps, pushRequest())

	got := stopped(t, stop, "a broke result")
	assertBrokeRead(t, got, readGitVersion, gittest.FailedSaid)
	assertAsked(t, s, nil)
}

// ---------------------------------------------------------------------------
// Unread preconditions
// ---------------------------------------------------------------------------

func TestPreconditions_WhenForgeIdentityFailsUntyped_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, fails(errors.New("HTTP 502")), false)
	before := s.refs()

	_, stop := landing.Preconditions(t.Context(), s.deps, pushRequest())

	got := stopped(t, stop, "a broke result")
	assertStoppedBy(t, got, result.Broke, pushRequest())

	text, reason := shown(t, got, s.gitDir)
	assert.Check(t, is.DeepEqual(text, unreadText(pushCommand(), readIdentity, "HTTP 502")))
	assert.Check(t, is.Equal(reason, "could not read "+readIdentity))

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

	_, stop := landing.Preconditions(t.Context(), deps, pushRequest())

	got := stopped(t, stop, "a broke result")
	assertBrokeReadBy(t, got, pushCommand(), readStanding)
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

// unfinishedReads are the deadlines that end during one read of the preconditions, one per read.
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

			_, stop := landing.Preconditions(ctx, s.deps, pushRequest())

			got := stopped(t, stop, "a refusal")
			assertStopped(t, got, result.Refused)

			text, _ := shown(t, got, s.gitDir)
			assert.Check(t, is.DeepEqual(text, overtimeText(pushCommand(), tt.read)))

			assertRefsUnchanged(t, s, before)
			assertAsked(t, s, tt.ops)
		})
	}
}
