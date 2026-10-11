package landing_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
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

	readStanding = "where HEAD stands"
	readIdentity = "the repository's identity on GitHub"
	readTrunk    = "origin's trunk"

	unreachedText = "Refused: origin could not be reached to ask for its trunk\n" + nextPush
)

// scene is one repository and one forge, with a record of what each was asked.
type scene struct {
	fixture  *gittest.Fixture
	forge    *forgetest.Fake
	recorder *git.Recorder
	gitDir   string
	deps     landing.Deps
}

// served returns the fixture's environment with ssh replaced by a command that serves the
// repository at dir, so that origin's GitHub URLs reach a local repository and never the network.
func served(f *gittest.Fixture, dir string) []string {
	return append(f.Env(),
		"GIT_SSH_VARIANT=simple",
		// The trailing # turns the host and the command git appends into a shell comment.
		"GIT_SSH_COMMAND=git upload-pack '"+filepath.ToSlash(dir)+"' #",
	)
}

// newScene returns a repository on the topic branch whose origin URLs name Acme/Widgets on
// GitHub, once arrange has changed it, and a forge that answers from script. An offline scene
// has an origin that does not answer.
func newScene(t *testing.T, arrange func(*gittest.Fixture), script forgetest.Script, offline bool) *scene {
	t.Helper()

	f := gittest.New(t, trunk)
	f.Commits(topic, "tidy the readme")
	f.SetOriginURL(fetchURL)
	f.SetPushURL(pushURL)

	if arrange != nil {
		arrange(f)
	}

	dir := f.OriginDir()
	if offline {
		dir += "-missing"
	}

	recorder := &git.Recorder{}
	fake := forgetest.New(t, script)

	return &scene{
		fixture:  f,
		forge:    fake,
		recorder: recorder,
		gitDir:   f.Git("rev-parse", "--absolute-git-dir"),
		deps: landing.Deps{
			Repo:  git.Repo{Runner: git.Runner{Dir: f.Dir(), Env: served(f, dir), Recorder: recorder}},
			Forge: fake,
			Clock: landing.SystemClock{},
		},
	}
}

// refs returns every ref of the working repository and of origin with the commit it is on.
func (s *scene) refs() string {
	return s.fixture.Git("for-each-ref") + "\norigin:\n" + s.fixture.OriginGit("for-each-ref")
}

// asked returns the calls that ops stand for: each about the repository origin's URLs name, and
// the find-changes read about the topic branch.
func asked(ops []forgetest.Op) []forgetest.Call {
	var calls []forgetest.Call

	for _, op := range ops {
		call := forgetest.Call{Op: op, Repo: forge.Repo{Host: host, Owner: owner, Name: repoName}}
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

	// What the forge said of the repository is no part of which repository a call is about.
	which := cmpopts.IgnoreFields(forge.Repo{}, "Fork", "CanPush")

	if diff := cmp.Diff(asked(ops), s.forge.Calls(), which); diff != "" {
		t.Errorf("forge calls mismatch (-want +got):\n%s", diff)
	}
}

// askedOrigin reports whether a git call went to origin.
func (s *scene) askedOrigin() bool {
	return slices.ContainsFunc(s.recorder.Calls(), func(argv []string) bool {
		return slices.Contains(argv, "ls-remote")
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

// fails is a script whose identity call returns err.
func fails(err error) forgetest.Script {
	return forgetest.Script{Identity: []forgetest.Answer[forge.Repo]{{Err: err}}}
}

// shown renders r through both renderers and returns the text and the reason the JSON carries.
func shown(t *testing.T, r result.Result, gitDir string) (text, reason string) {
	t.Helper()

	text, err := result.RenderText(r, gitDir)
	if err != nil {
		t.Fatalf("RenderText() error = %v, want nil", err)
	}

	out, err := result.RenderJSON(r, gitDir)
	if err != nil {
		t.Fatalf("RenderJSON() error = %v, want nil", err)
	}

	var decoded struct {
		Reason string `json:"reason"`
	}

	err = json.Unmarshal([]byte(out), &decoded)
	if err != nil {
		t.Fatalf("RenderJSON() output is not one JSON object: %v\n%s", err, out)
	}

	return text, decoded.Reason
}

// assertStopped fails unless got ended as want with the exit code exit and carries the command
// and words of pushRequest.
func assertStopped(t *testing.T, got result.Result, want result.Outcome, exit int) {
	t.Helper()

	if got.Outcome != want || got.Outcome.ExitCode() != exit {
		t.Errorf("Preconditions() outcome = %s (exit %d), want %s (exit %d)",
			got.Outcome, got.Outcome.ExitCode(), want, exit)
	}

	if got.Command != "push" {
		t.Errorf("Preconditions() result command = %q, want %q", got.Command, "push")
	}

	if diff := cmp.Diff([]string{"push"}, got.Words); diff != "" {
		t.Errorf("Preconditions() result words mismatch (-want +got):\n%s", diff)
	}
}

// assertBrokeRead fails unless got is broke on the read named read, says nothing was written,
// and carries detail in the line about the failure.
func assertBrokeRead(t *testing.T, got result.Result, read, detail string) {
	t.Helper()

	assertStopped(t, got, result.Broke, exitBroke)

	text, reason := shown(t, got, "")
	prefix := "Could not read " + read + ": "
	suffix := "\nNothing was written.\n" + nextFixed

	if !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, suffix) {
		t.Errorf("RenderText() = %q, want it to start with %q and end with %q", text, prefix, suffix)
	}

	if !strings.Contains(text, detail) {
		t.Errorf("RenderText() = %q, want it to contain %q", text, detail)
	}

	if want := "could not read " + read; reason != want {
		t.Errorf("RenderJSON() reason = %q, want %q", reason, want)
	}
}

// assertRefsUnchanged fails unless every ref of the scene is as it was in before.
func assertRefsUnchanged(t *testing.T, s *scene, before string) {
	t.Helper()

	if after := s.refs(); after != before {
		t.Errorf("refs after Preconditions() = %q, want them unchanged from %q", after, before)
	}
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
			want: "Refused: this repository has no remote named origin\n" +
				"Next: create the repository on GitHub and push to it with `gh repo create --source . --push`\n",
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
		noTool = "Refused: gh is not installed; install it with `brew install gh`\n" +
			"Next: install gh, then run ils push again\n"
		noLogin = "Refused: gh is not logged in to github.com; log in with " +
			"`gh auth login --hostname github.com`, or push by hand if github.com is not GitHub\n" +
			"Next: log in to github.com, then run ils push again\n"
		isFork = "Refused: Acme/Widgets is a fork, which ils does not push to; the user pushes by hand\n" +
			nextByHand
		noPush = "Refused: the logged-in user has no push permission on Acme/Widgets; " +
			"the user pushes by hand\n" + nextByHand
	)

	missing := fails(&forge.ToolMissingError{Tool: "gh", InstallHint: "brew install gh"})

	return []refusal{
		{name: "gh is not installed", script: missing, asksForge: true, want: noTool},
		{name: "gh is not installed and origin does not answer", script: missing, offline: true, asksForge: true, want: noTool},
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
	allowed := answers(widgets(false, true))

	return []refusal{
		{
			name:   "origin does not answer",
			script: allowed, offline: true, asksForge: true, asksOrigin: true, want: unreachedText,
		},
		{
			name: "origin's answer names no branch",
			arrange: func(f *gittest.Fixture) {
				f.OriginGit("update-ref", "--no-deref", "HEAD", f.OriginGit("rev-parse", "HEAD"))
			},
			script: allowed, asksForge: true, asksOrigin: true, want: unreachedText,
		},
	}
}

func TestPreconditions_WhenOneFails_RefusesAtTheFirstAndWritesNothing(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(originRefusals(), forgeRefusals(), trunkRefusals()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, tt.arrange, tt.script, tt.offline)
			before := s.refs()

			_, got, ok := landing.Preconditions(t.Context(), s.deps, pushRequest())

			if ok {
				t.Fatal("Preconditions() ok = true, want a refusal")
			}
			assertStopped(t, got, result.Refused, exitRefused)

			text, _ := shown(t, got, s.gitDir)
			if diff := cmp.Diff(tt.want, text); diff != "" {
				t.Errorf("RenderText() mismatch (-want +got):\n%s", diff)
			}

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

	if asked := s.askedOrigin(); asked != tt.asksOrigin {
		t.Errorf("origin asked for its trunk = %t, want %t", asked, tt.asksOrigin)
	}
}

func TestPreconditions_WhenAllHold_ReturnsStandingRepositoryAndTrunk(t *testing.T) {
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
			s := newScene(t, tt.arrange, answers(widgets(false, true)), false)

			got, stop, ok := landing.Preconditions(t.Context(), s.deps, pushRequest())
			if !ok {
				text, _ := shown(t, stop, s.gitDir)
				t.Fatalf("Preconditions() ok = false with %q, want every precondition to hold", text)
			}

			want := landing.Ready{Standing: tt.want, Repo: widgets(false, true), Trunk: trunk}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("Preconditions() mismatch (-want +got):\n%s", diff)
			}

			wantCalls := []forgetest.Call{
				{Op: forgetest.OpIdentity, Repo: forge.Repo{Host: host, Owner: owner, Name: repoName}},
			}
			if diff := cmp.Diff(wantCalls, s.forge.Calls()); diff != "" {
				t.Errorf("forge calls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPreconditions_WhenForgeIdentityFailsUntyped_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()
	s := newScene(t, nil, fails(errors.New("HTTP 502")), false)
	before := s.refs()

	_, got, ok := landing.Preconditions(t.Context(), s.deps, pushRequest())

	if ok {
		t.Fatal("Preconditions() ok = true, want a broke result")
	}
	assertBrokeRead(t, got, readIdentity, "HTTP 502")

	text, _ := shown(t, got, s.gitDir)
	want := "Could not read the repository's identity on GitHub: HTTP 502\nNothing was written.\n" + nextFixed
	if diff := cmp.Diff(want, text); diff != "" {
		t.Errorf("RenderText() mismatch (-want +got):\n%s", diff)
	}

	assertRefsUnchanged(t, s, before)
	assertAsked(t, s, identityRead())

	if s.askedOrigin() {
		t.Error("origin was asked for its trunk after the identity read failed, want no later read")
	}
}

func TestPreconditions_WhenDirectoryIsNotARepository_BrokeNamingTheRead(t *testing.T) {
	t.Parallel()
	deps := landing.Deps{
		Repo:  git.Repo{Runner: git.Runner{Dir: t.TempDir(), Env: gittest.New(t, trunk).Env()}},
		Forge: forgetest.New(t, forgetest.Script{}),
		Clock: landing.SystemClock{},
	}

	_, got, ok := landing.Preconditions(t.Context(), deps, pushRequest())

	if ok {
		t.Fatal("Preconditions() ok = true, want a broke result")
	}
	assertBrokeRead(t, got, readStanding, "not a git repository")
}

func TestPreconditions_WhenGitMissingFromPath_BrokeNamingTheRead(t *testing.T) {
	// Not parallel: this empties PATH for the whole process while it runs.
	t.Setenv("PATH", t.TempDir())

	deps := landing.Deps{
		Repo:  git.Repo{Runner: git.Runner{Dir: t.TempDir()}},
		Forge: forgetest.New(t, forgetest.Script{}),
		Clock: landing.SystemClock{},
	}

	_, got, ok := landing.Preconditions(t.Context(), deps, pushRequest())

	if ok {
		t.Fatal("Preconditions() ok = true, want a broke result")
	}
	assertBrokeRead(t, got, readStanding, "git not found on PATH")
}

// ending is a context whose deadline ends when the test says so.
type ending struct {
	context.Context

	done chan struct{}
}

// Done returns the channel that end closes.
func (e *ending) Done() <-chan struct{} { return e.done }

// Err returns context.DeadlineExceeded once end was called.
func (e *ending) Err() error {
	select {
	case <-e.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// end ends the deadline.
func (e *ending) end() { close(e.done) }

// unfinished is a deadline that ends during one read of the preconditions.
type unfinished struct {
	// arrange returns the context to run under and the forge's script.
	arrange func(t *testing.T) (context.Context, forgetest.Script)
	name    string
	// read is the read the refusal names.
	read string
	// ops are the forge operations made before the refusal.
	ops []forgetest.Op
}

func unfinishedReads() []unfinished {
	return []unfinished{
		{
			name: "before the first read",
			read: readStanding,
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

				ctx := &ending{Context: t.Context(), done: make(chan struct{})}
				script := answers(widgets(false, true))
				script.OnRead = func(forgetest.Call) { ctx.end() }

				return ctx, script
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

			if ok {
				t.Fatal("Preconditions() ok = true, want a refusal")
			}
			assertStopped(t, got, result.Refused, exitRefused)

			text, _ := shown(t, got, s.gitDir)
			want := "Refused: the time limit ended while reading " + tt.read + "; nothing was written\n" + nextPush
			if diff := cmp.Diff(want, text); diff != "" {
				t.Errorf("RenderText() mismatch (-want +got):\n%s", diff)
			}

			assertRefsUnchanged(t, s, before)
			assertAsked(t, s, tt.ops)
		})
	}
}

func TestSystemClock_Now_ReturnsCurrentTime(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		want := time.Now()

		got := landing.SystemClock{}.Now()

		if !got.Equal(want) {
			t.Errorf("Now() = %v, want %v", got, want)
		}
	})
}

func TestSystemClock_Wait_WhenDurationPasses_ReturnsNil(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()

		err := landing.SystemClock{}.Wait(t.Context(), time.Minute)
		if err != nil {
			t.Fatalf("Wait(1m) error = %v, want nil", err)
		}

		if waited := time.Since(start); waited != time.Minute {
			t.Errorf("Wait(1m) returned after %v, want %v", waited, time.Minute)
		}
	})
}

func TestSystemClock_Wait_WhenContextEndsFirst_ReturnsItsErrorEarly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arrange func(context.Context) (context.Context, context.CancelFunc)
		want    error
		name    string
		after   time.Duration
	}{
		{
			name: "deadline",
			arrange: func(ctx context.Context) (context.Context, context.CancelFunc) {
				return context.WithTimeout(ctx, 10*time.Second)
			},
			want:  context.DeadlineExceeded,
			after: 10 * time.Second,
		},
		{
			name: "already canceled",
			arrange: func(ctx context.Context) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(ctx)
				cancel()

				return ctx, cancel
			},
			want: context.Canceled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := tt.arrange(t.Context())
				defer cancel()

				start := time.Now()

				err := landing.SystemClock{}.Wait(ctx, time.Minute)

				if !errors.Is(err, tt.want) {
					t.Errorf("Wait(1m) error = %v, want %v", err, tt.want)
				}
				if waited := time.Since(start); waited != tt.after {
					t.Errorf("Wait(1m) returned after %v, want %v", waited, tt.after)
				}
			})
		})
	}
}
