package cli_test

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

const (
	fangWidth = "__FANG_TEST_WIDTH"

	// helpWidth fixes the width Fang lays styled help out in, whatever terminal the tests run from.
	helpWidth = "120"

	// stuckRun, set in its environment to one of the ways below, makes the test binary run `ils
	// merge` as stuck does instead of the tests.
	stuckRun = "ILS_TEST_STUCK_RUN"

	// stuckSpinning shows the spinner line of its step on stderr.
	stuckSpinning = "spinning"
	// stuckIdle starts no step.
	stuckIdle = "idle"
	// stuckReturned lets its landing command return at once, then stays until it is killed.
	stuckReturned = "returned"
	// stuckStopping shows the spinner line of its step, and its landing command returns the
	// result Stopped. once the first interrupt or termination of the process ended its context.
	stuckStopping = "stopping"
	// stuckHolding is stuckStopping, except that once the context of its landing command
	// ended, the command prints canceled on stdout and stays until it is killed, so that a later
	// signal reaches a run still under way.
	stuckHolding = "holding"
	// stuckHoldingEarly is stuckHolding, except that the first interrupt or termination lands
	// before the landing command runs: the Notify of the run waits for it, after printing
	// listening on stdout, and its landing command prints no ready.
	stuckHoldingEarly = "holding early"

	// ready is the line a stuck run prints on stdout once its landing command runs, or, for
	// stuckReturned, once it returned.
	ready = "ready"
	// canceled is the line a holding run prints on stdout once the context of its landing command
	// ended.
	canceled = "canceled"
	// listening is the line a stuckHoldingEarly run prints on stdout once it waits, as its landing
	// starts, for the first interrupt or termination.
	listening = "listening"
)

func TestMain(m *testing.M) {
	if code, served := ghtest.Main(); served {
		os.Exit(code)
	}
	if way := os.Getenv(stuckRun); way != "" {
		os.Exit(stuck(way))
	}

	// Fang reads the width of the help once for the process, from the terminal unless this is set.
	if err := os.Setenv(fangWidth, helpWidth); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// stuck runs `ils merge` in a step that lasts until the run's deadline, as way says, with stderr
// a color terminal wide columns wide, and returns its exit code. Once the
// landing command runs, it prints ready on stdout; a stuckReturned run prints it once the command
// returned, and stays until it is killed. A stuckStopping, stuckHolding or stuckHoldingEarly run
// is the one stopping describes instead.
func stuck(way string) int {
	app := cli.App{
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
		Environ:        []string{colorTerm},
		StderrTerminal: true,
		StderrWidth:    func() int { return wide },
		Launch:         time.Now(),
		Notify:         signal.Notify,
	}
	if way == stuckStopping || way == stuckHolding || way == stuckHoldingEarly {
		return stopping(app, way)
	}
	if way == stuckReturned {
		app.SetMerge(finishing)
		code := app.Run(context.Background(), []string{"merge"})
		tell(ready)
		<-make(chan struct{})

		return code
	}
	app.SetMerge(func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result {
		if way != stuckIdle {
			startedMerge(deps.Progress)
		}
		tell(ready)
		<-ctx.Done()

		return finished(req)
	})

	return app.Run(context.Background(), []string{"merge"})
}

// stopping runs `ils merge` with app, which stuck set up, in a step whose spinner line shows on
// stderr, and returns the exit code. Once the step started, its landing command prints ready on
// stdout unless way is stuckHoldingEarly, then waits for the first interrupt or termination of the
// process to end its context; a stuckStopping run then returns the result Stopped, and any other
// prints canceled and stays until it is killed.
func stopping(app cli.App, way string) int {
	if way == stuckHoldingEarly {
		app.Notify = heldForTheFirstInterrupt()
	}
	app.SetMerge(func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result {
		startedMerge(deps.Progress)
		if way != stuckHoldingEarly {
			tell(ready)
		}
		<-ctx.Done()
		if way != stuckStopping {
			tell(canceled)
			<-make(chan struct{})
		}

		stopped := finished(req)
		stopped.Lines = []result.Phrase{result.Say("Stopped.")}

		return stopped
	})

	return app.Run(context.Background(), []string{"merge"})
}

// heldForTheFirstInterrupt returns a notify that relays to c the signals sig, as signal.Notify
// does. The first time sig holds the interrupt, as ils begins listening for the end of the
// process, it then prints listening on stdout and returns only once an interrupt or termination
// has reached the process.
func heldForTheFirstInterrupt() func(c chan<- os.Signal, sig ...os.Signal) {
	var once sync.Once

	return func(c chan<- os.Signal, sig ...os.Signal) {
		signal.Notify(c, sig...)
		if !slices.Contains(sig, os.Interrupt) {
			return
		}
		once.Do(func() {
			first := make(chan os.Signal, 1)
			signal.Notify(first, os.Interrupt, syscall.SIGTERM)
			tell(listening)
			<-first
			signal.Stop(first)
		})
	}
}

// tell prints line on stdout for the test that started the stuck run, and panics when it cannot.
func tell(line string) {
	if _, err := fmt.Fprintln(os.Stdout, line); err != nil {
		panic(fmt.Sprintf("tell the test %q: %v", line, err))
	}
}
