package github_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
)

// No test in this file is parallel: the fake `gh` is found through `GH_PATH`,
// which `go-gh` reads from the environment of the whole process.

// pullView is the shape the JSON tests decode `gh pr view` output into.
type pullView struct {
	Title  string `json:"title"`
	Number int    `json:"number"`
}

// missingToolText is what the user reads when `gh` is not installed.
const missingToolText = "gh is not installed: brew install gh"

// missingTool is the error every call returns when `gh` is not installed.
func missingTool() *forge.ToolMissingError {
	return &forge.ToolMissingError{Tool: "gh", InstallHint: "brew install gh"}
}

func TestRunner_Run_WhenGhExitsZero_ReturnsOutput(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"pr", "view", "--json", "number"},
		Answers: []ghtest.Answer{{Stdout: "{\"number\":7}\n", Stderr: "notice\n"}},
	})

	got, err := github.Runner{}.Run(t.Context(), "pr", "view", "--json", "number")
	assert.NilError(t, err)

	want := github.Result{Stdout: "{\"number\":7}\n", Stderr: "notice\n"}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestRunner_Run_WhenArgHoldsShellSyntax_PassesItVerbatim(t *testing.T) {
	value := "first && second; $HOME `third` > fourth"
	fake := ghtest.Setup(t, ghtest.Call{
		Args:    []string{"api", value},
		Answers: []ghtest.Answer{{Stdout: "matched\n"}},
	})

	_, err := github.Runner{}.Run(t.Context(), "api", value)
	assert.NilError(t, err)

	assertCalls(t, fake, [][]string{{"gh", "api", value}})
}

func TestRunner_Run_WhenGhExitsNonZero_ReturnsExitErrorAndStdout(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"pr", "checks", "7"},
		Answers: []ghtest.Answer{{Stdout: "build\tpending\n", Stderr: "still running\n", Code: 8}},
	})

	got, err := github.Runner{}.Run(t.Context(), "pr", "checks", "7")

	exitErr, ok := errors.AsType[*github.ExitError](err)
	assert.Assert(t, ok, "Run(pr checks) error = %v, want *github.ExitError", err)
	wantErr := github.ExitError{
		Argv:   []string{"gh", "pr", "checks", "7"},
		Stderr: "still running\n",
		Code:   8,
	}
	assert.Check(t, is.DeepEqual(*exitErr, wantErr))
	assert.Check(t, is.Equal(got.Stdout, "build\tpending\n"))
}

func TestRunner_Run_WhenArgvNotScripted_FakeExitsOneNamingArgv(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"pr", "view"},
		Answers: []ghtest.Answer{{Stdout: "scripted\n"}},
	})

	_, err := github.Runner{}.Run(t.Context(), "release", "view", "v1.2.3")

	exitErr, ok := errors.AsType[*github.ExitError](err)
	assert.Assert(t, ok, "Run(release view) error = %v, want *github.ExitError", err)
	assert.Check(t, is.Equal(exitErr.Code, 1))
	assert.Check(t, is.Contains(exitErr.Stderr, "release view v1.2.3"))
}

func TestRunner_Run_WhenArgvDiffersInLengthFromTheScriptedOne_FakeExitsOneNamingArgv(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "longer", args: []string{"pr", "view", "7", "--json", "x"}},
		{name: "shorter", args: []string{"pr", "view"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghtest.Setup(t, ghtest.Call{
				Args:    []string{"pr", "view", "7"},
				Answers: []ghtest.Answer{{Stdout: "scripted\n"}},
			})

			_, err := github.Runner{}.Run(t.Context(), tt.args...)

			exitErr, ok := errors.AsType[*github.ExitError](err)
			assert.Assert(t, ok, "Run(%q) error = %v, want *github.ExitError", tt.args, err)
			assert.Check(t, is.Equal(exitErr.Code, 1))
			assert.Check(t, is.Contains(exitErr.Stderr, strings.Join(tt.args, " ")))
		})
	}
}

func TestSetup_PointsGhPathAtTheTestBinary(t *testing.T) {
	want, err := os.Executable()
	assert.NilError(t, err)

	ghtest.Setup(t)

	assert.Check(t, is.Equal(os.Getenv("GH_PATH"), want))
}

func TestRunner_Run_WhenArgvScriptedWithSequence_AnswersInOrderThenRepeatsLast(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"pr", "checks", "7"},
		Answers: []ghtest.Answer{{Stdout: "pending\n"}, {Stdout: "pass\n"}},
	})
	runner := github.Runner{}

	var got []string
	for range 3 {
		result, err := runner.Run(t.Context(), "pr", "checks", "7")
		assert.NilError(t, err)
		got = append(got, result.Stdout)
	}

	want := []string{"pending\n", "pass\n", "pass\n"}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestRunner_Run_WhenContextCanceled_ReturnsContextError(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"pr", "view"},
		Answers: []ghtest.Answer{{Stdout: "scripted\n"}},
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := github.Runner{}.Run(ctx, "pr", "view")

	assert.Check(t, is.ErrorIs(err, context.Canceled))
}

func TestRunner_Run_WhenAnswerIsDelayed_ReturnsItAfterTheDelay(t *testing.T) {
	const delay = 300 * time.Millisecond
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"pr", "checks", "7"},
		Answers: []ghtest.Answer{{Stdout: "pass\n", Delay: delay}},
	})
	start := time.Now()

	got, err := github.Runner{}.Run(t.Context(), "pr", "checks", "7")
	assert.NilError(t, err)

	elapsed := time.Since(start)
	assert.Check(t, elapsed >= delay, "Run(pr checks) answered after %s, want no sooner than %s", elapsed, delay)
	assert.Check(t, is.Equal(got.Stdout, "pass\n"))
}

func TestRunner_Run_WhenContextEndsDuringTheDelay_ReturnsContextErrorPromptly(t *testing.T) {
	const prompt = 5 * time.Second
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"pr", "checks", "7"},
		Answers: []ghtest.Answer{{Stdout: "pass\n", Delay: 30 * time.Second}},
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)
	start := time.Now()

	_, err := github.Runner{}.Run(ctx, "pr", "checks", "7")

	assert.Check(t, is.ErrorIs(err, context.DeadlineExceeded))
	elapsed := time.Since(start)
	assert.Check(t, elapsed <= prompt, "Run(pr checks) returned after %s, want it back within %s", elapsed, prompt)
}

func TestRunner_Run_WhenRecorderAttached_RecordsEachArgvTheFakeReceived(t *testing.T) {
	fake := ghtest.Setup(t,
		ghtest.Call{Args: []string{"pr", "view", "7"}, Answers: []ghtest.Answer{{Stdout: "open\n"}}},
		ghtest.Call{Args: []string{"pr", "merge", "7", "--squash"}, Answers: []ghtest.Answer{{}}},
	)
	recorder := &github.Recorder{}
	runner := github.Runner{Recorder: recorder}
	_, err := runner.Run(t.Context(), "pr", "view", "7")
	assert.NilError(t, err)

	_, err = runner.Run(t.Context(), "pr", "merge", "7", "--squash")
	assert.NilError(t, err)

	want := [][]string{{"gh", "pr", "view", "7"}, {"gh", "pr", "merge", "7", "--squash"}}
	assert.Check(t, is.DeepEqual(recorder.Calls(), want))
	assertCalls(t, fake, want)
}

func TestRunner_Run_WhenGhMissing_ReturnsToolMissingError(t *testing.T) {
	tests := []struct {
		name   string
		ghPath string
	}{
		{name: "GH_PATH names no file", ghPath: filepath.Join(t.TempDir(), "gh")},
		{name: "no GH_PATH and no gh on PATH", ghPath: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Sets `GH_PATH` and `PATH` itself, so that no `gh` can be found.
			t.Setenv("GH_PATH", tt.ghPath)
			t.Setenv("PATH", t.TempDir())

			_, err := github.Runner{}.Run(t.Context(), "pr", "view")

			missing, ok := errors.AsType[*forge.ToolMissingError](err)
			assert.Assert(t, ok, "Run(pr view) error = %v, want *forge.ToolMissingError", err)
			assert.Check(t, is.DeepEqual(missing, missingTool()))
			assert.Check(t, is.Equal(missing.Error(), missingToolText))
		})
	}
}

func TestRunJSON_WhenStdoutIsJSON_DecodesIntoValue(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"pr", "view", "--json", "number,title"},
		Answers: []ghtest.Answer{{Stdout: "{\"number\":7,\"title\":\"Add the runner\"}\n"}},
	})

	got, err := github.RunJSON[pullView](t.Context(), github.Runner{}, "pr", "view", "--json", "number,title")
	assert.NilError(t, err)

	want := pullView{Number: 7, Title: "Add the runner"}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestRunJSON_WhenStdoutMalformed_ReturnsDecodeErrorNamingCommand(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"pr", "view", "--json", "number"},
		Answers: []ghtest.Answer{{Stdout: "{\"number\":"}},
	})

	_, err := github.RunJSON[pullView](t.Context(), github.Runner{}, "pr", "view", "--json", "number")

	decodeErr, ok := errors.AsType[*github.DecodeError](err)
	assert.Assert(t, ok, "RunJSON(pr view) error = %v, want *github.DecodeError", err)
	wantArgv := []string{"gh", "pr", "view", "--json", "number"}
	assert.Check(t, is.DeepEqual(decodeErr.Argv, wantArgv))
	assert.Check(t, is.Contains(err.Error(), "gh pr view --json number"))
}

func TestRunJSON_WhenGhExitsNonZero_ReturnsExitError(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"api", "repos/ada/engine"},
		Answers: []ghtest.Answer{{Stderr: "gh: Forbidden (HTTP 403)\n", Code: 1}},
	})

	_, err := github.RunJSON[pullView](t.Context(), github.Runner{}, "api", "repos/ada/engine")

	exitErr, ok := errors.AsType[*github.ExitError](err)
	assert.Assert(t, ok, "RunJSON(api) error = %v, want *github.ExitError", err)
	assert.Check(t, is.Equal(exitErr.Stderr, "gh: Forbidden (HTTP 403)\n"))
}

func TestRunner_CheckLogin_WhenAuthStatusExitsZero_ReturnsNil(t *testing.T) {
	fake := ghtest.Setup(t, ghtest.Call{
		Args:    []string{"auth", "status", "--hostname", "github.com"},
		Answers: []ghtest.Answer{{Stdout: "Logged in to github.com\n"}},
	})

	err := github.Runner{}.CheckLogin(t.Context(), "github.com")
	assert.Check(t, err)

	assertCalls(t, fake, [][]string{{"gh", "auth", "status", "--hostname", "github.com"}})
}

func TestRunner_CheckLogin_WhenAuthStatusExitsNonZero_ReturnsNotLoggedInError(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"auth", "status", "--hostname", "git.example.com"},
		Answers: []ghtest.Answer{{Stderr: "You are not logged into any GitHub hosts.\n", Code: 1}},
	})

	err := github.Runner{}.CheckLogin(t.Context(), "git.example.com")

	notLoggedIn, ok := errors.AsType[*forge.NotLoggedInError](err)
	assert.Assert(t, ok, "CheckLogin(git.example.com) error = %v, want *forge.NotLoggedInError", err)
	assert.Check(t, is.Equal(notLoggedIn.Host, "git.example.com"))
	wantText := "not logged in to git.example.com"
	assert.Check(t, is.Equal(notLoggedIn.Error(), wantText))
}

func TestRunner_CheckLogin_WhenGhMissing_ReturnsToolMissingError(t *testing.T) {
	// Sets `GH_PATH` and `PATH` itself, so that no `gh` can be found.
	t.Setenv("GH_PATH", "")
	t.Setenv("PATH", t.TempDir())

	err := github.Runner{}.CheckLogin(t.Context(), "github.com")

	missing, ok := errors.AsType[*forge.ToolMissingError](err)
	assert.Assert(t, ok, "CheckLogin(github.com) error = %v, want *forge.ToolMissingError", err)
	assert.Check(t, is.DeepEqual(missing, missingTool()))
}
