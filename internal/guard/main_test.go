//go:build unix

package guard_test

import (
	"os"
	"testing"

	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
)

func TestMain(m *testing.M) {
	if code, served := gittest.Main(); served {
		os.Exit(code)
	}

	if code, served := ghtest.Main(); served {
		os.Exit(code)
	}

	os.Exit(m.Run())
}
