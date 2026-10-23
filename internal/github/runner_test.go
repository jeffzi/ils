package github_test

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
)

// No test that scripts `gh` is parallel: the fake `gh` is found through `GH_PATH`,
// which `go-gh` reads from the environment of the whole process.

func TestVersionOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "release with a date", line: "gh version 2.50.0 (2024-05-29)", want: "2.50.0"},
		{name: "release without a date", line: "gh version 2.100.3", want: "2.100.3"},
		{name: "pre-release keeps its suffix", line: "gh version 2.50.0-rc.1", want: "2.50.0-rc.1"},
		{name: "development build", line: "gh version DEV"},
		{name: "no gh version prefix", line: "2.50.0"},
		{name: "empty line"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := github.VersionOf(tt.line)

			assert.Check(t, is.Equal(got, tt.want), "VersionOf(%q)", tt.line)
		})
	}
}

// missingTool is the error every call returns when `gh` is not installed.
func missingTool() *child.ToolMissingError {
	return &child.ToolMissingError{Tool: program, InstallURL: installURL}
}

// assertMissingTool fails the test unless err is the missingTool error, ignoring its cause, and
// returns that error.
func assertMissingTool(t *testing.T, err error, label string) *child.ToolMissingError {
	t.Helper()

	missing, ok := errors.AsType[*child.ToolMissingError](err)
	assert.Assert(t, ok, "%s error = %v, want *child.ToolMissingError", label, err)
	assert.Check(t, is.DeepEqual(missing, missingTool(), cmpopts.IgnoreFields(child.ToolMissingError{}, "Err")))

	return missing
}

// withoutGh sets `GH_PATH` to ghPath and empties `PATH` for the rest of the test, so that no `gh`
// can be found.
func withoutGh(t *testing.T, ghPath string) {
	t.Helper()

	t.Setenv("GH_PATH", ghPath)
	t.Setenv("PATH", t.TempDir())
}

func TestRunner_Run_WhenGhExitsZero_ReturnsOutput(t *testing.T) {
	args := []string{"pr", "view", "--json", "number"}
	ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "{\"number\":7}\n", Stderr: "notice\n"}, args...))

	got, err := github.Runner{}.Run(t.Context(), args...)
	assert.NilError(t, err)

	want := github.Result{Stdout: "{\"number\":7}\n", Stderr: "notice\n"}
	assert.Check(t, is.DeepEqual(got, want))
}

func TestRunner_Run_WhenArgHoldsShellSyntax_PassesItVerbatim(t *testing.T) {
	value := "first && second; $HOME `third` > fourth"
	args := []string{"api", value}
	fake := ghtest.Setup(t, ghtest.Says("matched\n", args...))

	_, err := github.Runner{}.Run(t.Context(), args...)
	assert.NilError(t, err)

	assertCalls(t, fake, [][]string{{"gh", "api", value}})
}

func TestRunner_Run_WhenGhExitsNonZero_ReturnsExitErrorAndStdout(t *testing.T) {
	args := []string{"pr", "checks", "7"}
	ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "build\tpending\n", Stderr: "still running\n", Code: 8}, args...))

	got, err := github.Runner{}.Run(t.Context(), args...)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Run(pr checks) error = %v, want *child.ExitError", err)
	wantErr := child.ExitError{
		Argv:   []string{"gh", "pr", "checks", "7"},
		Stderr: "still running\n",
		Code:   8,
	}
	assert.Check(t, is.DeepEqual(*exitErr, wantErr))
	assert.Check(t, is.Equal(got.Stdout, "build\tpending\n"))
}

func TestRunner_Run_WhenContextIsCanceledWhileGhRuns_ReturnsContextError(t *testing.T) {
	args := []string{"pr", "checks", "7"}
	fake := ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "pass\n", Delay: 30 * time.Second}, args...))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	ran := make(chan error, 1)
	go func() {
		_, err := github.Runner{}.Run(ctx, args...)
		ran <- err
	}()
	select {
	case <-fake.Holding():
	case err := <-ran:
		t.Fatalf("Run(pr checks) returned %v before the fake held its answer", err)
	}

	cancel()
	err := <-ran

	assert.Check(t, is.ErrorIs(err, context.Canceled))
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
			withoutGh(t, tt.ghPath)

			_, err := github.Runner{}.Run(t.Context(), "pr", "view")

			missing := assertMissingTool(t, err, "Run(pr view)")
			assert.Check(t, missing.Err != nil, "Run(pr view) error carries no cause, want what starting gh failed with")
		})
	}
}

// loggedCall is one record a logger received: its message and its attributes by key, a
// duration standing as measured{}.
type loggedCall struct {
	Attrs   map[string]any
	Message string
}

// measured stands in a loggedCall for a duration, which no test can predict.
//
// Zero counts: a call that fails before gh starts can end within one tick of a coarse clock,
// such as the one on Windows, and log 0s.
type measured struct{}

// logSink keeps every record logged through the loggers built on it.
type logSink struct {
	calls []loggedCall
	mu    sync.Mutex
}

// Calls is every record the sink received, in order.
func (s *logSink) Calls() []loggedCall {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.calls)
}

// newLogger is a logger that sends every record, at any level, to the sink it returns.
func newLogger() (*slog.Logger, *logSink) {
	sink := &logSink{}

	return slog.New(sinkHandler{sink: sink}), sink
}

// sinkHandler is the slog.Handler that feeds a logSink, with the attributes and group added
// through With and WithGroup.
type sinkHandler struct {
	sink   *logSink
	prefix string
	attrs  []slog.Attr
}

func (sinkHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h sinkHandler) Handle(_ context.Context, record slog.Record) error {
	attrs := map[string]any{}
	for _, attr := range h.attrs {
		attrs[attr.Key] = valueOf(attr.Value)
	}
	record.Attrs(func(attr slog.Attr) bool {
		attrs[h.prefix+attr.Key] = valueOf(attr.Value)
		return true
	})

	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	h.sink.calls = append(h.sink.calls, loggedCall{Message: record.Message, Attrs: attrs})

	return nil
}

func (h sinkHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = slices.Clone(h.attrs)
	for _, attr := range attrs {
		attr.Key = h.prefix + attr.Key
		h.attrs = append(h.attrs, attr)
	}

	return h
}

func (h sinkHandler) WithGroup(name string) slog.Handler {
	h.prefix += name + "."

	return h
}

// valueOf is what a loggedCall holds for value: measured{} for a duration that is not negative,
// the value itself otherwise.
func valueOf(value slog.Value) any {
	value = value.Resolve()
	if value.Kind() == slog.KindDuration && value.Duration() >= 0 {
		return measured{}
	}

	return value.Any()
}

// exitedCall is the record Runner logs for a gh call with args that exited with code: message "exec",
// tool "gh", args and exit, and a measured duration.
func exitedCall(code int64, args ...string) loggedCall {
	return loggedCall{
		Message: "exec",
		Attrs:   map[string]any{"tool": "gh", "args": args, "exit": code, "duration": measured{}},
	}
}

// failedCall is the record of the gh call with args that ended with err instead of an exit code.
func failedCall(err error, args ...string) loggedCall {
	return loggedCall{
		Message: "exec",
		Attrs:   map[string]any{"tool": "gh", "args": args, "error": err.Error(), "duration": measured{}},
	}
}

func TestRunner_Run_WhenLoggerSet_LogsToolArgvExitCodeAndDuration(t *testing.T) {
	args := []string{"pr", "checks", "7"}
	ghtest.Setup(t, ghtest.Exits(ghtest.Answer{Stdout: "build\tpending\n", Stderr: "still running\n", Code: 8}, args...))
	logger, sink := newLogger()

	_, _ = github.Runner{Logger: logger}.Run(t.Context(), args...)

	assert.Check(t, is.DeepEqual(sink.Calls(), []loggedCall{exitedCall(8, args...)}))
}

func TestRunner_Run_WhenLoggerSetAndGhMissing_LogsTheFailure(t *testing.T) {
	withoutGh(t, filepath.Join(t.TempDir(), "gh"))
	logger, sink := newLogger()

	_, err := github.Runner{Logger: logger}.Run(t.Context(), "pr", "view")
	assert.Assert(t, err != nil, "Run(pr view) error = nil, want what starting gh failed with")

	assert.Check(t, is.DeepEqual(sink.Calls(), []loggedCall{failedCall(err, "pr", "view")}))
}
