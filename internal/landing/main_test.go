package landing_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jeffzi/ils/internal/gittest"
)

func TestMain(m *testing.M) {
	cleanup, err := gittest.InstallFakeGit(context.Background())
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	code := m.Run()
	if err := cleanup(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		code = 1
	}

	os.Exit(code)
}
