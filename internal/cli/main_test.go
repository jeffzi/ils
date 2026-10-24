package cli_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
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

	// stayAfterReturn is how long a stuckReturned run stays once its landing command returned:
	// longer than any test waits for it.
	stayAfterReturn = 2 * time.Minute

	// ready is the line a stuck run prints on stdout once its landing command runs, or, for
	// stuckReturned, once it returned.
	ready = "ready"
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

// tell prints line on stdout for the test that started the stuck run, and panics when it cannot.
func tell(line string) {
	if _, err := fmt.Fprintln(os.Stdout, line); err != nil {
		panic(fmt.Sprintf("tell the test %q: %v", line, err))
	}
}
