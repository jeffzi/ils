package child

import "context"

// InterruptedExit is interruptedExit, for the tests of package child_test.
func InterruptedExit(code int) bool { return interruptedExit(code) }

// Failure is failure, for the tests of package child_test.
func Failure(ctx context.Context, argv []string, runErr error, stderr string) error {
	return failure(ctx, argv, runErr, stderr)
}

// Ended is ended, for the tests of package child_test.
func Ended(ctx context.Context, argv []string, runErr error, printed Output) (Output, error) {
	return ended(ctx, argv, runErr, printed)
}

// Check is check, for the tests of package child_test.
func (r Requirement) Check(line, version string) error { return r.check(line, version) }
