package cli_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/jeffzi/ils/internal/github/ghtest"
)

func TestMain(m *testing.M) {
	if code, served := ghtest.Main(); served {
		os.Exit(code)
	}

	// Fang reads the width of the help once for the process, from the terminal unless this is set.
	if err := os.Setenv("__FANG_TEST_WIDTH", "120"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}
