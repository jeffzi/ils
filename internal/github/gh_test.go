package github_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

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
	if err != nil {
		t.Fatalf("Run(pr view) error = %v, want nil", err)
	}

	want := github.Result{Stdout: "{\"number\":7}\n", Stderr: "notice\n"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Run(pr view) mismatch (-want +got):\n%s", diff)
	}
}

func TestRunner_Run_WhenArgHoldsShellSyntax_PassesItVerbatim(t *testing.T) {
	value := "first && second; $HOME `third` > fourth"
	fake := ghtest.Setup(t, ghtest.Call{
		Args:    []string{"api", value},
		Answers: []ghtest.Answer{{Stdout: "matched\n"}},
	})

	_, err := github.Runner{}.Run(t.Context(), "api", value)
	if err != nil {
		t.Fatalf("Run(api) error = %v, want nil", err)
	}

	assertCalls(t, fake, [][]string{{"gh", "api", value}})
}

func TestRunner_Run_WhenGhExitsNonZero_ReturnsExitErrorAndStdout(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"pr", "checks", "7"},
		Answers: []ghtest.Answer{{Stdout: "build\tpending\n", Stderr: "still running\n", Code: 8}},
	})

	got, err := github.Runner{}.Run(t.Context(), "pr", "checks", "7")

	exitErr, ok := errors.AsType[*github.ExitError](err)
	if !ok {
		t.Fatalf("Run(pr checks) error = %v, want *github.ExitError", err)
	}
	wantErr := github.ExitError{
		Argv:   []string{"gh", "pr", "checks", "7"},
		Stderr: "still running\n",
		Code:   8,
	}
	if diff := cmp.Diff(wantErr, *exitErr); diff != "" {
		t.Errorf("Run(pr checks) exit error mismatch (-want +got):\n%s", diff)
	}
	if got.Stdout != "build\tpending\n" {
		t.Errorf("Run(pr checks).Stdout = %q, want %q", got.Stdout, "build\tpending\n")
	}
}

func TestRunner_Run_WhenArgvNotScripted_FakeExitsOneNamingArgv(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"pr", "view"},
		Answers: []ghtest.Answer{{Stdout: "scripted\n"}},
	})

	_, err := github.Runner{}.Run(t.Context(), "release", "view", "v1.2.3")

	exitErr, ok := errors.AsType[*github.ExitError](err)
	if !ok {
		t.Fatalf("Run(release view) error = %v, want *github.ExitError", err)
	}
	if exitErr.Code != 1 {
		t.Errorf("Run(release view) exit code = %d, want 1", exitErr.Code)
	}
	if !strings.Contains(exitErr.Stderr, "release view v1.2.3") {
		t.Errorf("Run(release view) stderr = %q, want it to name the argv", exitErr.Stderr)
	}
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
			if !ok {
				t.Fatalf("Run(%q) error = %v, want *github.ExitError", tt.args, err)
			}
			if exitErr.Code != 1 {
				t.Errorf("Run(%q) exit code = %d, want 1", tt.args, exitErr.Code)
			}
			if !strings.Contains(exitErr.Stderr, strings.Join(tt.args, " ")) {
				t.Errorf("Run(%q) stderr = %q, want it to name the argv", tt.args, exitErr.Stderr)
			}
		})
	}
}

func TestSetup_PointsGhPathAtTheTestBinary(t *testing.T) {
	want, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v, want nil", err)
	}

	ghtest.Setup(t)

	if got := os.Getenv("GH_PATH"); got != want {
		t.Errorf("GH_PATH after Setup = %q, want the test binary %q", got, want)
	}
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
		if err != nil {
			t.Fatalf("Run(pr checks) error = %v, want nil", err)
		}
		got = append(got, result.Stdout)
	}

	want := []string{"pending\n", "pass\n", "pass\n"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Run(pr checks) answers mismatch (-want +got):\n%s", diff)
	}
}

func TestRunner_Run_WhenContextCanceled_ReturnsContextError(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"pr", "view"},
		Answers: []ghtest.Answer{{Stdout: "scripted\n"}},
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := github.Runner{}.Run(ctx, "pr", "view")

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run(pr view) error = %v, want context.Canceled", err)
	}
}

func TestRunner_Run_WhenRecorderAttached_RecordsEachArgvTheFakeReceived(t *testing.T) {
	fake := ghtest.Setup(t,
		ghtest.Call{Args: []string{"pr", "view", "7"}, Answers: []ghtest.Answer{{Stdout: "open\n"}}},
		ghtest.Call{Args: []string{"pr", "merge", "7", "--squash"}, Answers: []ghtest.Answer{{}}},
	)
	recorder := &github.Recorder{}
	runner := github.Runner{Recorder: recorder}
	if _, err := runner.Run(t.Context(), "pr", "view", "7"); err != nil {
		t.Fatalf("Run(pr view) error = %v, want nil", err)
	}

	_, err := runner.Run(t.Context(), "pr", "merge", "7", "--squash")
	if err != nil {
		t.Fatalf("Run(pr merge) error = %v, want nil", err)
	}

	want := [][]string{{"gh", "pr", "view", "7"}, {"gh", "pr", "merge", "7", "--squash"}}
	if diff := cmp.Diff(want, recorder.Calls()); diff != "" {
		t.Errorf("Recorder.Calls() mismatch (-want +got):\n%s", diff)
	}
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
			if !ok {
				t.Fatalf("Run(pr view) error = %v, want *forge.ToolMissingError", err)
			}
			if diff := cmp.Diff(missingTool(), missing); diff != "" {
				t.Errorf("Run(pr view) error mismatch (-want +got):\n%s", diff)
			}
			if missing.Error() != missingToolText {
				t.Errorf("Run(pr view) error text = %q, want %q", missing.Error(), missingToolText)
			}
		})
	}
}

func TestRunJSON_WhenStdoutIsJSON_DecodesIntoValue(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"pr", "view", "--json", "number,title"},
		Answers: []ghtest.Answer{{Stdout: "{\"number\":7,\"title\":\"Add the runner\"}\n"}},
	})

	got, err := github.RunJSON[pullView](t.Context(), github.Runner{}, "pr", "view", "--json", "number,title")
	if err != nil {
		t.Fatalf("RunJSON(pr view) error = %v, want nil", err)
	}

	want := pullView{Number: 7, Title: "Add the runner"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("RunJSON(pr view) mismatch (-want +got):\n%s", diff)
	}
}

func TestRunJSON_WhenStdoutMalformed_ReturnsDecodeErrorNamingCommand(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"pr", "view", "--json", "number"},
		Answers: []ghtest.Answer{{Stdout: "{\"number\":"}},
	})

	_, err := github.RunJSON[pullView](t.Context(), github.Runner{}, "pr", "view", "--json", "number")

	decodeErr, ok := errors.AsType[*github.DecodeError](err)
	if !ok {
		t.Fatalf("RunJSON(pr view) error = %v, want *github.DecodeError", err)
	}
	wantArgv := []string{"gh", "pr", "view", "--json", "number"}
	if diff := cmp.Diff(wantArgv, decodeErr.Argv); diff != "" {
		t.Errorf("RunJSON(pr view) decode error argv mismatch (-want +got):\n%s", diff)
	}
	if !strings.Contains(err.Error(), "gh pr view --json number") {
		t.Errorf("RunJSON(pr view) error = %q, want it to name the command", err.Error())
	}
}

func TestRunJSON_WhenGhExitsNonZero_ReturnsExitError(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"api", "repos/ada/engine"},
		Answers: []ghtest.Answer{{Stderr: "gh: Forbidden (HTTP 403)\n", Code: 1}},
	})

	_, err := github.RunJSON[pullView](t.Context(), github.Runner{}, "api", "repos/ada/engine")

	exitErr, ok := errors.AsType[*github.ExitError](err)
	if !ok {
		t.Fatalf("RunJSON(api) error = %v, want *github.ExitError", err)
	}
	if exitErr.Stderr != "gh: Forbidden (HTTP 403)\n" {
		t.Errorf("RunJSON(api) stderr = %q, want gh's HTTP 403 line", exitErr.Stderr)
	}
}

func TestRunner_CheckLogin_WhenAuthStatusExitsZero_ReturnsNil(t *testing.T) {
	fake := ghtest.Setup(t, ghtest.Call{
		Args:    []string{"auth", "status", "--hostname", "github.com"},
		Answers: []ghtest.Answer{{Stdout: "Logged in to github.com\n"}},
	})

	err := github.Runner{}.CheckLogin(t.Context(), "github.com")
	if err != nil {
		t.Errorf("CheckLogin(github.com) error = %v, want nil", err)
	}

	assertCalls(t, fake, [][]string{{"gh", "auth", "status", "--hostname", "github.com"}})
}

func TestRunner_CheckLogin_WhenAuthStatusExitsNonZero_ReturnsNotLoggedInError(t *testing.T) {
	ghtest.Setup(t, ghtest.Call{
		Args:    []string{"auth", "status", "--hostname", "git.example.com"},
		Answers: []ghtest.Answer{{Stderr: "You are not logged into any GitHub hosts.\n", Code: 1}},
	})

	err := github.Runner{}.CheckLogin(t.Context(), "git.example.com")

	notLoggedIn, ok := errors.AsType[*forge.NotLoggedInError](err)
	if !ok {
		t.Fatalf("CheckLogin(git.example.com) error = %v, want *forge.NotLoggedInError", err)
	}
	if notLoggedIn.Host != "git.example.com" {
		t.Errorf("CheckLogin(git.example.com) host = %q, want %q", notLoggedIn.Host, "git.example.com")
	}
	wantText := "not logged in to git.example.com"
	if notLoggedIn.Error() != wantText {
		t.Errorf("CheckLogin(git.example.com) error text = %q, want %q", notLoggedIn.Error(), wantText)
	}
}

func TestRunner_CheckLogin_WhenGhMissing_ReturnsToolMissingError(t *testing.T) {
	// Sets `GH_PATH` and `PATH` itself, so that no `gh` can be found.
	t.Setenv("GH_PATH", "")
	t.Setenv("PATH", t.TempDir())

	err := github.Runner{}.CheckLogin(t.Context(), "github.com")

	missing, ok := errors.AsType[*forge.ToolMissingError](err)
	if !ok {
		t.Fatalf("CheckLogin(github.com) error = %v, want *forge.ToolMissingError", err)
	}
	if diff := cmp.Diff(missingTool(), missing); diff != "" {
		t.Errorf("CheckLogin(github.com) error mismatch (-want +got):\n%s", diff)
	}
}
