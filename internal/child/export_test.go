package child

import "context"

// Failure is failure, for the tests of package child_test.
func Failure(ctx context.Context, argv []string, runErr error, stderr string) error {
	return failure(ctx, argv, runErr, stderr)
}
