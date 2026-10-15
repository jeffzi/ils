package cli_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/jeffzi/ils/internal/github/ghtest"
)

const (
	fangWidth = "__FANG_TEST_WIDTH"

	// helpWidth keeps every entry of the help on one line, which entries in cli_test.go relies on.
	helpWidth = "120"
)

func TestMain(m *testing.M) {
	if code, served := ghtest.Main(); served {
		os.Exit(code)
	}

	// Fang reads the width of the help once for the process, from the terminal unless this is set.
	if err := os.Setenv(fangWidth, helpWidth); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}
