package cli_test

import (
	"os"
	"testing"

	"github.com/jeffzi/ils/internal/github/ghtest"
)

func TestMain(m *testing.M) {
	if code, served := ghtest.Main(); served {
		os.Exit(code)
	}

	os.Exit(m.Run())
}
