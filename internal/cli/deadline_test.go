package cli_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

const (
	// grace is how long a run may take to come back after the moment it is due: past the bound for
	// timed, after the held call starts for cutWhileHeld.
	grace = 2 * time.Second
	// held is how long a held call stays open, far past the moment the run is cut.
	held = 30 * time.Second
)

// timed runs `ils push` in a scene of sc, and fails the test unless it exits as sc wants and
// comes back no later than grace past the bound.
func timed(t *testing.T, sc scenario) (*scene, ran) {
	t.Helper()

	s := newScene(t, sc)
	app := s.app()

	got := launch(t, app, "push")
	late := time.Since(app.Launch.Add(bound))

	assert.Equal(t, got.code, sc.want, "stdout:\n%s\nstderr:\n%s", got.stdout, got.stderr)
	assert.Check(t, late <= grace, "ils push returned %s past the bound, want it back no later than %s past it", late, grace)

	return s, got
}

// cutWhileHeld runs `ils push` in a scene of sc under a context that ends with
// [context.DeadlineExceeded] the moment the fake gh starts holding an answer, and fails the test
// unless it exits as sc wants and comes back no later than grace after that moment.
func cutWhileHeld(t *testing.T, sc scenario) (*scene, ran) {
	t.Helper()

	s := newScene(t, sc)
	got := s.endWhileHeld(t, s.app(), context.DeadlineExceeded, "push")

	assert.Equal(t, got.code, sc.want, "stdout:\n%s\nstderr:\n%s", got.stdout, got.stderr)

	return s, got
}

// endWhileHeld runs app with args in s under a context that ends with cause the moment the fake gh
// of s starts holding an answer, and fails the test unless it comes back no later than grace after
// that moment.
func (s *scene) endWhileHeld(t *testing.T, app cli.App, cause error, args ...string) ran {
	t.Helper()

	cut := make(chan struct{})
	cutAt := make(chan time.Time, 1)

	ctx, cancel := context.WithCancel(t.Context())

	var wg sync.WaitGroup

	wg.Go(func() {
		select {
		case <-s.fake.Holding():
			cutAt <- time.Now()
			close(cut)
		case <-ctx.Done():
		}
	})

	got := launchUnder(gittest.Ending(t.Context(), cut, cause), t, app, args...)

	cancel()
	wg.Wait()

	select {
	case at := <-cutAt:
		late := time.Since(at)
		assert.Check(t, late <= grace, "ils %v returned %s after the held call started, want it back no later than %s after", args, late, grace)
	default:
		t.Errorf("ils %v returned without the fake gh holding an answer", args)
	}

	return got
}

func heldChecks() scenario {
	slow := func(sha string) verdict {
		v := passing(sha)
		v.checks.Delay = held

		return v
	}

	return scenario{name: "the read of the checks", script: firstPush(slow), want: exitPending}
}

func heldIdentity() scenario {
	calls := allowed()
	calls[1].Answers[0].Delay = held

	return scenario{name: "the read of the identity", script: only(calls...), want: exitRefused}
}

func heldVersion() scenario {
	slow := ghtest.Exits(ghtest.Answer{Stdout: "gh version 99.0.0\n", Delay: held}, ghtest.VersionArgs()...)

	return scenario{name: "the read of gh's version", script: only(slow), want: exitRefused}
}

func TestApp_Run_WhenMergeRuns_EndsItAtTheBoundFromLaunch(t *testing.T) {
	t.Parallel()

	var (
		deadline time.Time
		bounded  bool
	)

	app := outside(t)
	app.Launch = time.Now()
	app.SetMerge(func(ctx context.Context, _ landing.Deps, req landing.Request) result.Result {
		deadline, bounded = ctx.Deadline()

		return finished(req)
	})

	got := launch(t, app, "merge")

	assert.Check(t, is.Equal(got.code, exitDone))
	assert.Check(t, bounded, "ils merge ran with no deadline")
	assert.Check(t, deadline.Equal(app.Launch.Add(bound)), "deadline = %s, want %s", deadline, app.Launch.Add(bound))
}

func TestApp_Run_WhenTheBoundEndedBeforeAnyReadOfPush_RefusesBeforePushing(t *testing.T) {
	s := newScene(t, greenFirstPush())
	app := s.app()
	app.Launch = time.Now().Add(-2 * bound)

	got := launch(t, app, "push")

	assert.Check(t, is.Equal(got.code, exitRefused))
	startsWith(t, got.stdout, "Refused: the time limit ended ")

	nothingPushed(t, s)
}

func TestApp_Run_WhenChecksStillRunAtTheBound_EndsPendingWithoutOutlastingIt(t *testing.T) {
	_, got := timed(t, pendingAtTheBound())

	want := "\nNext: ils push\n"
	endsWith(t, got.stdout, want)
}

func TestApp_Run_WhenTheTimeLimitEndsDuringAHeldReadBeforeThePush_RefusesWithinGraceWithNothingPushed(t *testing.T) {
	const want = "Refused: the time limit ended while reading the repository's identity on GitHub; nothing was written\n" +
		"Next: ils push\n"

	for _, sc := range []scenario{heldIdentity(), heldVersion()} {
		t.Run(sc.name, func(t *testing.T) {
			s, got := cutWhileHeld(t, sc)

			assert.Check(t, is.Equal(got.stdout, want))
			assert.Check(t, is.Equal(got.stderr, ""))
			nothingPushed(t, s)
			assert.Check(t, !opensPullRequest(s), "gh was asked to create a pull request")
		})
	}
}

func TestApp_Run_WhenTheTimeLimitEndsDuringTheHeldReadOfTheChecks_EndsPendingWithinGraceWithThePullRequestOpen(t *testing.T) {
	s, got := cutWhileHeld(t, heldChecks())

	startsWith(t, got.stdout, "Pushed "+topic+" at ")
	assert.Check(t, is.Contains(got.stdout, pullURL))
	endsWith(t, got.stdout, "\nNext: ils push\n")
	assert.Check(t, is.Equal(got.stderr, ""))
	onOrigin := s.fixture.OriginGit("for-each-ref", "--points-at", tipOf(s.fixture), "refs/heads/"+topic)
	assert.Check(t, onOrigin != "", "%s is not on origin at its tip", topic)
	assert.Check(t, opensPullRequest(s), "gh was not asked to create a pull request")
}

func TestApp_Run_WhenAnInterruptEndsAHeldRead_PrintsTheInterruptedResultAndEndsTheDisplay(t *testing.T) {
	const reason = "ils was interrupted while reading the repository's identity on GitHub; nothing was written"

	tests := []struct {
		printed func(t *testing.T, stdout string)
		name    string
		args    []string
	}{
		{
			name: "as text", args: []string{"push"},
			printed: func(t *testing.T, stdout string) {
				t.Helper()

				assert.Check(t, is.Equal(stdout, "Refused: "+reason+"\nNext: ils push\n"))
			},
		},
		{
			name: "as JSON", args: []string{"push", "--json"},
			printed: func(t *testing.T, stdout string) {
				t.Helper()

				shown := resulttest.Decode(t, stdout)
				assert.Check(t, is.Equal(shown.Outcome, "refused"))
				assert.Check(t, is.Equal(shown.Reason, reason))
				assert.Check(t, is.Equal(shown.Next, "ils push"))
				assert.Check(t, is.Equal(shown.ExitCode, exitRefused))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScene(t, heldIdentity())
			stderr := newScreen(t, wide)
			app := s.app()
			app.Environ = []string{colorTerm}
			app.StderrTerminal = true
			app.StderrWidth = func() int { return wide }
			app.Stderr = stderr

			got := s.endWhileHeld(t, app, context.Canceled, tt.args...)

			assert.Check(t, is.Equal(got.code, exitRefused))
			tt.printed(t, got.stdout)
			assert.Check(t, stderr.raw() != "", "the run drew no progress display on stderr")
			assert.Check(t, is.Len(stderr.lines(), 0), "stderr still shows %q", stderr.lines())
			assert.Check(t, !stderr.cursorHidden(), "the cursor is still hidden:\n%q", stderr.raw())
			nothingPushed(t, s)
		})
	}
}
