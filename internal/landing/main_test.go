package landing_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/jeffzi/ils/internal/gittest"
)

func TestMain(m *testing.M) {
	cleanup, err := gittest.InstallFakeGit()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	code := m.Run()
	cleanup()
	os.Exit(code)
}
