package cli_test

import (
	"context"
	"errors"
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
	// stuckPiped runs its step with stderr no terminal.
	stuckPiped = "piped"
	// stuckIdle starts no step.
	stuckIdle = "idle"
	// stuckUnstoppable shows the spinner line of its step, and every SIGSTOP it sends itself fails.
	stuckUnstoppable = "unstoppable"
	// stuckReturned lets its landing command return at once, then stays until it is killed.
	stuckReturned = "returned"
	// stuckCancelOnInterrupt shows the spinner line of its step, and its run ends at the first
	// interrupt or termination of the process, as the run of ils does.
	stuckCancelOnInterrupt = "cancel-on-interrupt"
	// stuckHolding is stuckCancelOnInterrupt, except that once the context of its landing command
	// ended, the command prints canceled on stdout and stays for stayAfterReturn before it
	// returns, so that a later signal reaches a run still under way.
	stuckHolding = "holding"
	// stuckHoldingEarly is stuckHolding, except that the first interrupt or termination lands
	// before the landing command runs: as the landing starts, ils waits for it, after printing
	// listening on stdout, and its landing command prints no ready.
	stuckHoldingEarly = "holding early"

	// stayAfterReturn is how long a stuckReturned run stays once its landing command returned, and
	// how long the landing command of a holding run stays once its context ended: longer than any
	// test waits for either.
	stayAfterReturn = 2 * time.Minute

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

// errUnsent is why a SIGSTOP an unstoppable run sends itself fails.
var errUnsent = errors.New("the stop signal was not sent")

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
// a color terminal wide columns wide unless way is stuckPiped, and returns its exit code. Once the
// landing command runs, it prints ready on stdout; a stuckReturned run prints it once the command
// returned, and stays for stayAfterReturn.
func stuck(way string) int {
	if way == stuckCancelOnInterrupt || way == stuckHolding || way == stuckHoldingEarly {
		return cancelOnInterrupt(way)
	}

	app := cli.App{
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
		Environ:        []string{colorTerm},
		StderrTerminal: way != stuckPiped,
		StderrWidth:    func() int { return wide },
		Launch:         time.Now(),
		Notify:         signal.Notify,
	}
	if way == stuckUnstoppable {
		app.SetSuspend(func() error { return errUnsent })
	}
	if way == stuckReturned {
		app.SetMerge(func(_ context.Context, _ landing.Deps, req landing.Request) result.Result {
			return finished(req)
		})
		code := app.Run(context.Background(), []string{"merge"})
		tell(ready)
		time.Sleep(stayAfterReturn)

		return code
	}
	app.SetMerge(func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result {
		if way != stuckIdle {
			startedMerge(progress(deps))
		}
		tell(ready)
		<-ctx.Done()

		return finished(req)
	})

	return app.Run(context.Background(), []string{"merge"})
}

// cancelOnInterrupt runs `ils merge` as stuck does, in a step whose spinner line shows on stderr, a
// color terminal wide columns wide, and its run ends at the first interrupt or termination of the
// process; it returns the exit code. Once the step started, its landing command prints ready on
// stdout unless way is stuckHoldingEarly, then waits for its context to end; unless way is
// stuckCancelOnInterrupt, it then prints canceled and stays for stayAfterReturn before it returns.
func cancelOnInterrupt(way string) int {
	app := cli.App{
		Stdout:            os.Stdout,
		Stderr:            os.Stderr,
		Environ:           []string{colorTerm},
		StderrTerminal:    true,
		StderrWidth:       func() int { return wide },
		Launch:            time.Now(),
		Notify:            signal.Notify,
		CancelOnInterrupt: true,
	}
	if way == stuckHoldingEarly {
		app.Notify = heldForTheFirstInterrupt()
	}
	app.SetMerge(func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result {
		startedMerge(progress(deps))
		if way != stuckHoldingEarly {
			tell(ready)
		}
		<-ctx.Done()
		if way != stuckCancelOnInterrupt {
			tell(canceled)
			time.Sleep(stayAfterReturn)
		}

		return interruptedResult(req)
	})

	return app.Run(context.Background(), []string{"merge"})
}

// interruptedResult is what the landing command of a run that cancels on interrupt ends with once
// its context ended: a merge that says it stopped.
func interruptedResult(req landing.Request) result.Result {
	stopped := finished(req)
	stopped.Lines = []result.Phrase{result.Say("Stopped.")}

	return stopped
}

// heldForTheFirstInterrupt returns a notify that relays to c the signals sig, as signal.Notify
// does. The first time sig leaves out the interrupt, as ils begins listening for the stop signal
// of a terminal at the start of a landing command, it then prints listening on stdout and returns
// only once an interrupt or termination has reached the process.
func heldForTheFirstInterrupt() func(c chan<- os.Signal, sig ...os.Signal) {
	var once sync.Once

	return func(c chan<- os.Signal, sig ...os.Signal) {
		signal.Notify(c, sig...)
		if slices.Contains(sig, os.Interrupt) {
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
