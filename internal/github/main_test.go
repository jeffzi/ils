package github_test

import (
	"os"
	"testing"

	"github.com/jeffzi/ils/internal/github/ghtest"
)

func TestMain(m *testing.M) {
	ghtest.Main()

	os.Exit(m.Run())
}
