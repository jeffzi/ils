package cli_test

import (
	"context"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

func TestApp_Run_WhenADebugLineCannotBeWritten_PrintsWhatTheSameRunPrintsWithoutDebug(t *testing.T) {
	t.Parallel()

	tests := [][]string{{"push"}, {"merge", "--json"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			app := lost(t)
			want := launch(t, app, args...)
			app.Stderr = shut{err: errReaderGone}

			got := launch(t, app, append(args, "-d")...)

			assert.Check(t, is.Equal(got.stdout, want.stdout))
			assert.Check(t, is.Equal(got.code, want.code))
		})
	}
}

func TestApp_Run_WhenDebugIsOnDuringALanding_PrintsWhatTheSameRunPrintsWithoutDebug(t *testing.T) {
	tests := []struct {
		sc   func() scenario
		args []string
	}{
		{sc: greenFirstPush, args: []string{"push"}},
		{sc: greenFirstPush, args: []string{"push", "--json"}},
		{sc: noPullRequest, args: []string{"merge"}},
		{sc: noPullRequest, args: []string{"merge", "--json"}},
		{sc: landedMerge, args: []string{"merge"}},
		{sc: landedMerge, args: []string{"merge", "--json"}},
	}
	for _, tt := range tests {
		t.Run(tt.sc().name+"/"+strings.Join(tt.args, " "), func(t *testing.T) {
			want := launch(t, newScene(t, tt.sc()).app(), tt.args...)

			got := launch(t, newScene(t, tt.sc()).app(), append(tt.args, "-d")...)

			assert.Check(t, is.Equal(got.code, tt.sc().want))
			assert.Check(t, is.Equal(got.stdout, want.stdout))
			assert.Check(t, is.Equal(got.code, want.code))
			assert.Check(t, got.stderr != "", "ils %s -d wrote no debug line", strings.Join(tt.args, " "))
		})
	}
}

func TestApp_Run_WhenDebugIsSetToFalse_PrintsWhatTheSameRunPrintsWithoutIt(t *testing.T) {
	t.Parallel()

	app := lost(t)
	want := launch(t, app, "push", "--json")

	got := launch(t, app, "push", "--json", "--debug=false")

	assert.Check(t, is.DeepEqual(got, want, sameRun()))
	assert.Check(t, is.Equal(got.stderr, ""))
}

func TestApp_Run_WhenEveryDebugLineOfALandingCannotBeWritten_PrintsWhatTheSameRunPrintsWithoutDebug(t *testing.T) {
	want := launch(t, newScene(t, greenFirstPush()).app(), "push")
	app := newScene(t, greenFirstPush()).app()
	app.Stderr = shut{err: errReaderGone}

	got := launch(t, app, "push", "-d")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Equal(got.stdout, want.stdout))
}

var (
	// execLine is a debug line of one git or gh call that exited: its time since launch, the call,
	// its exit code and its duration.
	execLine = regexp.MustCompile(`^\+\d+\.\d{3}s ((?:git|gh) .+) exit \d+ (\S+)$`)
	// waitLine is a debug line of one decision of a wait.
	waitLine = regexp.MustCompile(`^\+\d+\.\d{3}s wait \S+=\S+( \S+=\S+)*$`)
)

func TestApp_Run_WhenDebugIsOn_WritesALineForEachGitAndGhCallAndWaitDecision(t *testing.T) {
	s := newScene(t, greenFirstPush())

	got := launch(t, s.app(), "push", "-d")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	var called, waited []string
	for line := range strings.Lines(got.stderr) {
		line = strings.TrimSuffix(line, "\n")
		if m := execLine.FindStringSubmatch(line); m != nil {
			_, err := time.ParseDuration(m[2])
			assert.Check(t, err, "duration of %q", line)
			called = append(called, m[1])

			continue
		}
		assert.Check(t, waitLine.MatchString(line), "line %q is neither a call nor a wait decision", line)
		waited = append(waited, line[strings.Index(line, " wait ")+1:])
	}
	var want []string
	for _, argv := range slices.Concat(s.recorder.Calls(), s.fake.Calls()) {
		want = append(want, strings.Join(argv, " "))
	}
	assert.Check(t, is.DeepEqual(called, want, anyOrder()))
	assert.Check(t, is.Contains(waited, "wait ended=green"))
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
	assert.Assert(t, len(lines) > len(want), "stderr = %q", drawn.String())
	assert.Check(t, is.DeepEqual(lines[len(lines)-len(want):], want))
	dim := resulttest.Look{Dim: true}
	assert.Check(t, is.Equal(drawn.LookOf(t, want[1], "+2.250s"), dim))
	assert.Check(t, is.Equal(drawn.LookOf(t, want[1], "1.234s"), dim))
	assert.Check(t, is.Equal(drawn.LookOf(t, want[1], "gh pr view 12 exit 0"), resulttest.Look{}))
}

// mergeTitle is the conventional commit title of the pull request a merge lands.
const mergeTitle = "docs: tidy the readme"

// landedMerge is pull request 12, open at the published tip with its one check passed, which
// GitHub squash-merges and whose branch it deletes.
func landedMerge() scenario {
	script := func(f *gittest.Fixture) []ghtest.Call {
		sha := tipOf(f)
		open := changeJSON(opened, mergeTitle, trunk, sha)
		return slices.Concat(found(open, "[]"), passing(sha).calls(sha), []ghtest.Call{
			ghtest.Says(ghtest.MessageJSON(mergeTitle, "", ""), ghtest.MessageArgs(host, owner, widgets, opened)...),
			ghtest.Says("", ghtest.LandArgs(slug, forge.Landing{Number: opened, HeadSHA: sha, Subject: mergeTitle + " (#12)"})...),
			ghtest.Says("", ghtest.DeleteBranchArgs(host, owner+"/"+widgets, topic)...),
		})
	}

	return scenario{
		name: "a merge that lands", script: script, want: exitDone,
		arrange: func(f *gittest.Fixture) { f.Publish(topic) },
	}
}
