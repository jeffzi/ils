package guard_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

// run runs the tests with the fake git first on PATH and the fake gh on GH_PATH, both built
// without the race detector, and returns their exit code.
func run(m *testing.M) int {
	cleanGit, err := gittest.InstallFakeGit()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		return 1
	}
	defer cleanGit()

	cleanGh, err := ghtest.Install()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		return 1
	}
	defer cleanGh()

	return m.Run()
}
