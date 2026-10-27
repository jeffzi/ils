package cli_test

import (
	"context"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// anyOrder lets cmp compare two slices of strings whatever the order of their elements.
func anyOrder() cmp.Option {
	return cmpopts.SortSlices(func(a, b string) bool { return a < b })
}

func TestApp_Run_WhenADebugLineCannotBeWritten_TheLoggerReturnsTheWriteError(t *testing.T) {
	t.Parallel()

	app := outside(t)
	app.Stderr = shut{err: errReaderGone}
	var err error
	app.SetPush(func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result {
		err = deps.Logger.Handler().Handle(ctx, debugRecord(time.Now(), "wait", slog.String("state", "clean")))

		return finished(req)
	})

	launch(t, app, "push", "-d")

	assert.Check(t, is.ErrorIs(err, errReaderGone))
}

var (
	// execLine is a debug line of one git or gh call that exited: its time since launch, the call,
	// its exit code and its duration.
	execLine = regexp.MustCompile(`^\+\d+\.\d{3}s ((?:git|gh) .+) exit \d+ \S+$`)
	// waitLine is a debug line of one decision of a wait.
	waitLine = regexp.MustCompile(`^\+\d+\.\d{3}s wait \S+=\S+( \S+=\S+)*$`)
)

// traced sorts the debug lines of stderr: the calls they trace, how many wait decisions they
// trace, and the lines that are neither.
func traced(stderr string) (calls []string, waits int, unmatched []string) {
	for line := range strings.Lines(stderr) {
		line = strings.TrimSuffix(line, "\n")
		switch m := execLine.FindStringSubmatch(line); {
		case m != nil:
			calls = append(calls, m[1])
		case waitLine.MatchString(line):
			waits++
		default:
			unmatched = append(unmatched, line)
		}
	}

	return calls, waits, unmatched
}

// commandLines returns each argv as the command line a debug line names it by.
func commandLines(recorded [][]string) []string {
	lines := make([]string, 0, len(recorded))
	for _, argv := range recorded {
		lines = append(lines, strings.Join(argv, " "))
	}

	return lines
}

func TestApp_Run_WhenDebugIsOnDuringALanding_TracesTheLandingOnStderrAlone(t *testing.T) {
	want := launch(t, newScene(t, greenFirstPush()).app(), "push", "--json")
	s := newScene(t, greenFirstPush())

	got := launch(t, s.app(), "push", "--json", "-d")

	calls, waits, unmatched := traced(got.stderr)
	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Equal(got.stdout, want.stdout))
	assert.Check(t, is.Len(unmatched, 0), "lines that are neither a call nor a wait decision: %q", unmatched)
	assert.Check(t, is.DeepEqual(calls, commandLines(slices.Concat(s.recorder.Calls(), s.fake.Calls())), anyOrder()))
	assert.Check(t, waits > 0, "stderr traced no wait decision:\n%s", got.stderr)
}

func debugRecord(at time.Time, message string, attrs ...slog.Attr) slog.Record {
	record := slog.NewRecord(at, slog.LevelDebug, message, 0)
	record.AddAttrs(attrs...)

	return record
}

func TestApp_Run_WhenDebugIsOn_DrawsEachRecordTimedFromLaunchWithTimesDim(t *testing.T) {
	t.Parallel()

	app := painted(t, []string{colorTerm}, false, true)
	app.Launch = time.Now()
	app.SetPush(func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result {
		if deps.Logger == nil {
			t.Error("push ran with no logger")

			return finished(req)
		}
		for _, record := range []slog.Record{
			debugRecord(app.Launch.Add(1500*time.Millisecond), "wait",
				slog.String("read", "merge-state"), slog.String("state", "unknown"), slog.Duration("window", 30*time.Second)),
			debugRecord(app.Launch.Add(2250*time.Millisecond), "exec",
				slog.String("tool", "gh"), slog.Any("args", []string{"pr", "view", "12"}), slog.Int64("exit", 0),
				slog.Duration("duration", 1234*time.Millisecond)),
			debugRecord(app.Launch.Add(3*time.Second), "exec",
				slog.String("tool", "gh"), slog.Any("args", []string{"pr", "checks", "12"}), slog.String("error", "signal: killed"),
				slog.Duration("duration", 30*time.Millisecond)),
		} {
			assert.Check(t, deps.Logger.Handler().Handle(ctx, record))
		}

		return finished(req)
	})

	got := launch(t, app, "push", "-d")

	drawn := resulttest.Draw(t, got.stderr)
	lines := strings.Split(strings.TrimSuffix(drawn.String(), "\n"), "\n")
	want := []string{
		"+1.500s wait read=merge-state state=unknown window=30s",
		"+2.250s gh pr view 12 exit 0 1.234s",
		"+3.000s gh pr checks 12 signal: killed 30ms",
	}
	assert.Assert(t, len(lines) >= len(want), "stderr = %q", drawn.String())
	assert.Check(t, is.DeepEqual(lines[len(lines)-len(want):], want))
	dim := resulttest.Look{Dim: true}
	assert.Check(t, is.Equal(drawn.LookOf(t, want[1], "+2.250s"), dim))
	assert.Check(t, is.Equal(drawn.LookOf(t, want[1], "1.234s"), dim))
	assert.Check(t, is.Equal(drawn.LookOf(t, want[1], "gh pr view 12 exit 0"), resulttest.Look{}))
}
