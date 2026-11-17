package child

import "context"

// PipeGrace is pipeGrace, for the tests of package child_test.
const PipeGrace = pipeGrace

// Failure is failure, for the tests of package child_test.
func Failure(ctx context.Context, argv []string, runErr error, stderr string) error {
	return failure(ctx, argv, runErr, stderr)
}

// StopDescendants is stopDescendants, for the tests of package child_test.
func StopDescendants(
	ctx context.Context, root int, read func(context.Context) (map[int][]int, error), stop func(int) error,
) ([]int, error) {
	return stopDescendants(ctx, root, read, stop)
}
