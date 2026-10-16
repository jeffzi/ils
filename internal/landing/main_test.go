package landing_test

import (
	"os"
	"testing"

	"github.com/jeffzi/ils/internal/gittest"
)

func TestMain(m *testing.M) {
	if code, served := gittest.Main(); served {
		os.Exit(code)
	}

	os.Exit(m.Run())
}
