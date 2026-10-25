package child

import (
	"context"
	"time"
)

// InterruptedExit is interruptedExit, for the tests of package child_test.
func InterruptedExit(code int) bool { return interruptedExit(code) }

// FailureWithin returns Failure with wait in place of terminateGrace, for the tests of package
// child_test.
func FailureWithin(wait time.Duration) func(ctx context.Context, argv []string, runErr error, stderr string) error {
	return grace(wait).failure
}
