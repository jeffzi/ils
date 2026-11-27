package guard_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
)

// usersGH is the GH_PATH this process started with, before the fake gh took its place, and whether
// it was set: the replay judges with the gh the user has.
//
//nolint:gochecknoglobals // set once by TestMain before any test runs, and read only after.
var usersGH struct {
	path string
	set  bool
}

func TestMain(m *testing.M) {
	usersGH.path, usersGH.set = os.LookupEnv(github.PathVariable)
	os.Exit(run(m))
}

// run runs the tests with the fake git first on PATH and the fake gh on GH_PATH, both built
// without the race detector, and returns their exit code: 1 when either fake could not be
// installed or cleaned up.
func run(m *testing.M) (code int) {
	ctx := context.Background()

	cleanGit, err := gittest.InstallFakeGit(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		return 1
	}
	defer func() { code = failOnCleanup(code, cleanGit()) }()

	cleanGh, err := ghtest.Install(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		return 1
	}
	defer func() { code = failOnCleanup(code, cleanGh()) }()

	return m.Run()
}

// failOnCleanup reports err on stderr and returns 1 when the cleanup failed, else code.
func failOnCleanup(code int, err error) int {
	if err == nil {
		return code
	}

	_, _ = fmt.Fprintln(os.Stderr, err)

	return 1
}
