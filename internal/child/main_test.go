package child_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jeffzi/ils/internal/gittest"
)

// hookHoldsAtEnv holds the address the lingering copy of a hook stand-in holds; its presence makes
// the test binary act as a hook that starts that copy and waits for it.
const hookHoldsAtEnv = "ILS_CHILD_TEST_HOOK_HOLDS_AT"

// holdsAtEnv holds an address; its presence makes the test binary hold that address itself until
// it is killed, starting no process of its own.
const holdsAtEnv = "ILS_CHILD_TEST_HOLDS_AT"

func TestMain(m *testing.M) {
	// First: the hook's lingering copy inherits hookHoldsAtEnv, and would start another copy of
	// itself as the hook, without end.
	if code, served := gittest.Linger(); served {
		os.Exit(code)
	}

	if addr := os.Getenv(holdsAtEnv); addr != "" {
		os.Exit(gittest.Hold(context.Background(), addr))
	}

	if addr := os.Getenv(hookHoldsAtEnv); addr != "" {
		os.Exit(hookHolding(addr))
	}

	if code, served := gittest.Main(); served {
		os.Exit(code)
	}

	os.Exit(m.Run())
}

// hookHolding starts a lingering copy of the test binary that inherits this process's output and
// holds addr, and waits for it, so that this process is still running while the copy holds addr.
func hookHolding(addr string) int {
	lingerer, err := gittest.StartLingerer(context.Background(), addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hook stand-in: %v\n", err)

		return 1
	}

	if err = lingerer.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "hook stand-in: wait for the lingering copy: %v\n", err)

		return 1
	}

	return 0
}
