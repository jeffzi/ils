package github_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/logtest"
)

// No test that scripts `gh` is parallel: the fake `gh` is found through `GH_PATH`,
// which the runner reads from the environment of the whole process.

func TestVersionOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "release with a date", line: "gh version 2.50.0 (2024-05-29)", want: "2.50.0"},
		{name: "development build", line: "gh version DEV"},
		{name: "no gh version prefix", line: "2.50.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := github.VersionOf(tt.line)

			assert.Check(t, is.Equal(got, tt.want), "VersionOf(%q)", tt.line)
		})
	}
}

// withoutGh empties `GH_PATH` and `PATH` for the rest of the test, so that no `gh` can be found.
func withoutGh(t *testing.T) {
	t.Helper()

	t.Setenv("GH_PATH", "")
	t.Setenv("PATH", t.TempDir())
}

// assertToolMissing fails unless err is a *child.ToolMissingError.
func assertToolMissing(t *testing.T, err error) {
	t.Helper()

	_, ok := errors.AsType[*child.ToolMissingError](err)
	assert.Assert(t, ok, "error = %v, want *child.ToolMissingError", err)
}

func TestRunner_Run_WhenGhPathUnset_RunsTheGhOnPath(t *testing.T) {
	ghtest.Setup(t, ghtest.Says("found\n", "pr", "view"))
	dir := t.TempDir()
	gittest.Link(t, dir, github.Program)
	t.Setenv("GH_PATH", "")
	t.Setenv("PATH", dir)
	t.Chdir(dir)

	got, err := github.Runner{}.Run(t.Context(), "pr", "view")
	assert.NilError(t, err, "Run(pr view)")

	assert.Check(t, is.Equal(got.Stdout, "found\n"))
}

func TestRunner_CheckLogin_WhenActiveAccountPassesCheck_ReturnsNil(t *testing.T) {
	failed := ghtest.Account{State: "error", Error: "token expired"}
	tests := []struct {
		name   string
		answer ghtest.Answer
	}{
		{name: "the one account", answer: ghtest.LoggedIn(engineHost)},
		{
			name:   "an inactive account failed its check",
			answer: ghtest.LoginStatus(engineHost, failed, ghtest.Account{State: "success", Active: true}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Exits(tt.answer, ghtest.LoginArgs(engineHost)...))

			err := github.Runner{}.CheckLogin(t.Context(), engineHost)

			assert.Check(t, err, "CheckLogin(%q)", engineHost)
		})
	}
}

func TestRunner_CheckLogin_WhenNoAccountForHost_ReturnsNotLoggedInError(t *testing.T) {
	ghtest.Setup(t, ghtest.Exits(ghtest.LoggedOut(), ghtest.LoginArgs(engineHost)...))

	err := github.Runner{}.CheckLogin(t.Context(), engineHost)

	notLoggedIn, ok := errors.AsType[*forge.NotLoggedInError](err)
	assert.Assert(t, ok, "CheckLogin error = %v, want *forge.NotLoggedInError", err)
	assert.Check(t, is.Equal(notLoggedIn.Host, engineHost))
}

// assertCheckFailed fails unless err is an error other than a *forge.NotLoggedInError.
func assertCheckFailed(t *testing.T, err error) {
	t.Helper()

	assert.Assert(t, err != nil, "CheckLogin error = nil, want the check to fail")
	_, loggedOut := errors.AsType[*forge.NotLoggedInError](err)
	assert.Check(t, !loggedOut, "CheckLogin error = %v, want an error other than *forge.NotLoggedInError", err)
}

func TestRunner_CheckLogin_WhenActiveAccountFailsCheck_ReturnsErrorHoldingGhText(t *testing.T) {
	const ghText = `Get "https://git.example.com/api/v3/": dial tcp: lookup git.example.com: no such host`
	failed := ghtest.Account{State: "error", Error: ghText, Active: true}
	ghtest.Setup(t, ghtest.Exits(ghtest.LoginStatus(engineHost, failed), ghtest.LoginArgs(engineHost)...))

	err := github.Runner{}.CheckLogin(t.Context(), engineHost)

	assertCheckFailed(t, err)
	assert.Check(t, is.Contains(err.Error(), ghText))
}

func TestRunner_CheckLogin_WhenNoAccountIsActive_ReturnsError(t *testing.T) {
	inactive := ghtest.Account{State: "success"}
	ghtest.Setup(t, ghtest.Exits(ghtest.LoginStatus(engineHost, inactive), ghtest.LoginArgs(engineHost)...))

	err := github.Runner{}.CheckLogin(t.Context(), engineHost)

	assertCheckFailed(t, err)
}

func TestRunner_CheckLogin_WhenGhExitsNonZero_ReturnsExitError(t *testing.T) {
	tooOld := ghtest.Answer{Stderr: "unknown flag: --json\n", Code: 1}
	ghtest.Setup(t, ghtest.Exits(tooOld, ghtest.LoginArgs(engineHost)...))

	err := github.Runner{}.CheckLogin(t.Context(), engineHost)

	assertCheckFailed(t, err)
	_, ok := errors.AsType[*child.ExitError](err)
	assert.Check(t, ok, "CheckLogin error = %v, want it to wrap the *child.ExitError", err)
}

func aliasListArgs() []string { return []string{"alias", "list"} }

func TestRunner_Alias(t *testing.T) {
	const aliases = "co: pr checkout\nmg: pr merge $1\n"
	tests := []struct {
		name          string
		alias         string
		wantExpansion string
		wantOK        bool
	}{
		{name: "alias the list holds", alias: "mg", wantExpansion: "pr merge $1", wantOK: true},
		{name: "name the list does not hold", alias: "vw"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Says(aliases, aliasListArgs()...))

			expansion, ok, err := github.Runner{}.Alias(t.Context(), tt.alias)
			assert.NilError(t, err, "Alias(%q)", tt.alias)

			assert.Check(t, is.Equal(expansion, tt.wantExpansion), "Alias(%q) expansion", tt.alias)
			assert.Check(t, is.Equal(ok, tt.wantOK), "Alias(%q) ok", tt.alias)
		})
	}
}

func TestRunner_Alias_WhenListIsNotANameToExpansionMap_ReturnsDecodeErrorNamingTheList(t *testing.T) {
	ghtest.Setup(t, ghtest.Says("- pr checkout\n", aliasListArgs()...))

	_, _, err := github.Runner{}.Alias(t.Context(), "co")

	decodeErr, ok := errors.AsType[*github.DecodeError](err)
	assert.Assert(t, ok, "Alias error = %v, want *github.DecodeError", err)
	assert.Check(t, is.DeepEqual(decodeErr.Argv, ghArgv(aliasListArgs())))
}

func TestRunner_Alias_WhenListFails_ReturnsTheRunnersError(t *testing.T) {
	ghtest.Setup(t, ghtest.Exits(ghtest.ServerError(), aliasListArgs()...))

	_, _, err := github.Runner{}.Alias(t.Context(), "co")

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Alias error = %v, want *child.ExitError", err)
	assert.Check(t, is.DeepEqual(exitErr.Argv, ghArgv(aliasListArgs())))
}

func TestDecodeError_Unwrap_ReachesTheDecodersError(t *testing.T) {
	t.Parallel()
	cause := errors.New("unexpected end of JSON input")
	err := error(&github.DecodeError{Err: cause, Argv: []string{"gh", "auth", "status"}})

	assert.Check(t, is.ErrorIs(err, cause))
}

func viewArgs() []string { return []string{"pr", "view", "7"} }

// apiRead is the argv a test hands Read for a `gh api` read; apiReadRun is the argv gh then runs.
func apiRead() []string { return []string{"api", "repos/ada/engine"} }

func apiReadRun() []string { return []string{"api", "--include", "repos/ada/engine"} }

// pagedRead is the argv a test hands Read for a paginated `gh api` read; pagedReadRun is the argv
// gh then runs.
func pagedRead() []string { return []string{"api", "--paginate", "repos/ada/engine/pulls"} }

func pagedReadRun() []string {
	return []string{"api", "--include", "--paginate", "repos/ada/engine/pulls"}
}

// readTimed runs runner.Read of args in a synctest bubble, so that its pauses take no real time,
// and returns the reply, how long the read took on the bubble's clock, and the read's error.
func readTimed(t *testing.T, runner github.Runner, args ...string) (github.Reply, time.Duration, error) {
	t.Helper()

	var (
		reply github.Reply
		took  time.Duration
		err   error
	)

	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		reply, err = runner.Read(t.Context(), args...)
		took = time.Since(start)
	})

	return reply, took, err
}

func TestRunner_Read_WhenExit1WithoutA4xx_ReadsAgainAfterThePause(t *testing.T) {
	tests := []struct {
		name    string
		want    string
		read    []string
		run     []string
		failure ghtest.Answer
		answer  ghtest.Answer
	}{
		{
			name: "pr view", read: viewArgs(), run: viewArgs(),
			failure: ghtest.ServerError(), answer: ghtest.Answer{Stdout: "found\n"}, want: "found\n",
		},
		{
			name: "api read GitHub answered 2xx carrying GraphQL errors", read: apiRead(), run: apiReadRun(),
			failure: ghtest.Answer{
				Stdout: `{"data":null,"errors":[{"message":"Something went wrong while executing your query."}]}`,
				Stderr: "gh: Something went wrong while executing your query.\n", Status: 200, Code: 1,
			},
			answer: ghtest.Answer{Stdout: `{"data":{}}`}, want: `{"data":{}}`,
		},
		{
			name: "api read GitHub answered 5xx", read: apiRead(), run: apiReadRun(),
			failure: ghtest.Answer{Stdout: `{"message":"Server Error"}`, Stderr: "gh: Server Error (HTTP 500)\n", Status: 500, Code: 1},
			answer:  ghtest.Answer{Stdout: `{"id":1}`}, want: `{"id":1}`,
		},
		{
			name: "api read GitHub never answered", read: apiRead(), run: apiReadRun(),
			failure: ghtest.Answer{Stderr: "error connecting to api.github.com\n", Code: 1},
			answer:  ghtest.Answer{Stdout: `{"id":1}`}, want: `{"id":1}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.InTurn(tt.run, tt.failure, tt.answer))

			got, took, err := readTimed(t, github.Runner{}, tt.read...)
			assert.NilError(t, err, "Read(%q)", tt.read)

			assert.Check(t, is.Equal(got.Stdout, tt.want))
			assert.Check(t, is.Equal(took, 2*time.Second))
		})
	}
}

func TestRunner_Read_WhenStillFailingWhenTheWindowEnds_ReturnsTheLastFailure(t *testing.T) {
	badGateway := ghtest.Answer{Stderr: "gh: Bad Gateway (HTTP 502)\n", Code: 1}
	fake := ghtest.Setup(t, ghtest.InTurn(viewArgs(), ghtest.ServerError(), badGateway))
	logger, sink := logtest.New()

	_, took, err := readTimed(t, github.Runner{Logger: logger}, viewArgs()...)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Read error = %v, want *child.ExitError", err)
	assert.Check(t, is.Equal(exitErr.Stderr, badGateway.Stderr))
	assert.Check(t, is.Len(fake.Calls(), 6))
	assert.Check(t, is.Equal(took, 10*time.Second))
	rereads := slices.DeleteFunc(sink.Records(), func(r logtest.Record) bool { return r.Message == child.ExecMessage })
	assert.Check(t, is.Len(rereads, 5), "one record per failure read again, none for the last")
}

func TestRunner_Read_WhenContextEndsDuringThePause_ReturnsTheContextError(t *testing.T) {
	ghtest.Setup(t, ghtest.Exits(ghtest.ServerError(), viewArgs()...))

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		start := time.Now()

		_, err := github.Runner{}.Read(ctx, viewArgs()...)

		assert.Check(t, is.ErrorIs(err, context.DeadlineExceeded))
		assert.Check(t, is.Equal(time.Since(start), 3*time.Second))
	})
}

func TestRunner_Read_WhenAPIReadAnswers4xx_ReturnsStatusAndHeadersWithoutReadingAgain(t *testing.T) {
	tests := []struct {
		name          string
		wantRemaining string
		read          []string
		run           []string
		refusal       ghtest.Answer
		wantStatus    int
	}{
		{
			name: "on the only page", read: apiRead(), run: apiReadRun(),
			refusal: ghtest.Answer{
				Stdout: `{"message":"Not Found"}`, Stderr: "gh: Not Found (HTTP 404)\n",
				Status: 404, Headers: []string{"X-Ratelimit-Remaining: 4999"}, Code: 1,
			},
			wantStatus: 404, wantRemaining: "4999",
		},
		{
			name: "on a later page", read: pagedRead(), run: pagedReadRun(),
			refusal: ghtest.Answer{
				Stdout: `[{"number":7}]` + "\n" + ghtest.StatusBlock(403, "X-Ratelimit-Remaining: 0") +
					`{"message":"API rate limit exceeded"}`,
				Stderr: "gh: API rate limit exceeded (HTTP 403)\n",
				Status: 200, Headers: []string{"X-Ratelimit-Remaining: 1"}, Code: 1,
			},
			wantStatus: 403, wantRemaining: "0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.InTurn(tt.run, tt.refusal, ghtest.Answer{Stdout: "[]"}))

			got, _, err := readTimed(t, github.Runner{}, tt.read...)

			exitErr, ok := errors.AsType[*child.ExitError](err)
			assert.Assert(t, ok, "Read error = %v, want *child.ExitError", err)
			assert.Check(t, is.Equal(exitErr.Stderr, tt.refusal.Stderr))
			assert.Check(t, is.Equal(got.Status, tt.wantStatus))
			assert.Check(t, is.Equal(got.Header.Get("x-ratelimit-remaining"), tt.wantRemaining))
		})
	}
}

func TestRunner_Read_WhenAPIReadAnswersPages_ReturnsTheirBodiesAndTheLastPagesHeaders(t *testing.T) {
	pages := ghtest.Answer{
		Stdout: `[{"number":7}]` + "\n" + ghtest.StatusBlock(200, "X-Ratelimit-Remaining: 4998") + `[{"number":8}]`,
		Status: 200, Headers: []string{"X-Ratelimit-Remaining: 4999"},
	}
	ghtest.Setup(t, ghtest.Exits(pages, pagedReadRun()...))

	got, _, err := readTimed(t, github.Runner{}, pagedRead()...)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got.Stdout, `[{"number":7}]`+"\n"+`[{"number":8}]`))
	assert.Check(t, is.Equal(got.Status, 200))
	assert.Check(t, is.Equal(got.Header.Get("X-Ratelimit-Remaining"), "4998"))
}

func TestRunner_Read_WhenExitIsNot1_ReturnsItWithoutReadingAgain(t *testing.T) {
	refusal := ghtest.Answer{Code: 2}
	ghtest.Setup(t, ghtest.InTurn(viewArgs(), refusal, ghtest.Answer{Stdout: "found\n"}))

	_, _, err := readTimed(t, github.Runner{}, viewArgs()...)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Read error = %v, want *child.ExitError", err)
	assert.Check(t, is.Equal(exitErr.Code, refusal.Code))
}

func TestRunner_Read_WhenALaterStatusBlockCannotBeRead_ReturnsDecodeError(t *testing.T) {
	tests := []struct {
		name  string
		block string
	}{
		{name: "status line without a code", block: "HTTP/2.0\r\n\r\n[2]"},
		{name: "status code that is not a number", block: "HTTP/2.0 OK\r\n\r\n[2]"},
		{name: "header line without a colon", block: "HTTP/2.0 200 OK\nbroken\r\n\r\n[2]"},
		{name: "headers that never end", block: "HTTP/2.0 200 OK\nX-Ratelimit-Remaining: 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Says("[1]\n"+tt.block, pagedReadRun()...))

			_, err := github.Runner{}.Read(t.Context(), pagedRead()...)

			decodeErr, ok := errors.AsType[*github.DecodeError](err)
			assert.Assert(t, ok, "Read error = %v, want *github.DecodeError", err)
			assert.Check(t, is.DeepEqual(decodeErr.Argv, github.Argv(pagedReadRun())))
		})
	}
}

func TestRunner_Read_WhenGhFailedAndItsStatusBlockCannotBeRead_ReturnsGhsFailure(t *testing.T) {
	refusal := ghtest.Answer{Stdout: "HTTP/2.0 OK\r\n\r\n{}", Stderr: "gh: Bad Gateway\n", Code: 1}
	ghtest.Setup(t, ghtest.Exits(refusal, apiReadRun()...))

	_, _, err := readTimed(t, github.Runner{}, apiRead()...)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Read error = %v, want *child.ExitError", err)
	assert.Check(t, is.Equal(exitErr.Stderr, refusal.Stderr))
}

func TestRunner_Read_WhenGhMissing_ReturnsToolMissingErrorWithoutWaiting(t *testing.T) {
	withoutGh(t)

	_, took, err := readTimed(t, github.Runner{}, viewArgs()...)

	assertToolMissing(t, err)
	assert.Check(t, is.Equal(took, time.Duration(0)))
}
