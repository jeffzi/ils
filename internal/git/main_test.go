package git_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

const (
	// helperModeEnv makes the test binary act as git's external diff program instead of running tests.
	helperModeEnv = "ILS_GIT_TEST_DIFF_HELPER"
	// helperAddrEnv holds the local address a lingering helper connects to once it has started.
	helperAddrEnv = "ILS_GIT_TEST_DIFF_HELPER_ADDR"

	// helperLinger makes the helper outlive git, holding the output pipes it inherited.
	helperLinger = "linger"
	// helperSpawn makes the helper start a lingering copy of itself and exit at once, so that git
	// exits zero while a process it started still holds its output pipes.
	helperSpawn = "spawn"

	// helperLife bounds how long a lingering helper stays when nothing releases it.
	helperLife = 30 * time.Second
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperModeEnv); mode != "" {
		os.Exit(diffHelper(mode))
	}

	os.Exit(m.Run())
}

// diffHelper plays git's external diff program and returns its exit code. In the lingering mode it
// connects to the address in the environment, then stays until that connection closes or its life is up.
func diffHelper(mode string) int {
	if mode == helperSpawn {
		return spawnLingerer()
	}

	ctx, cancel := context.WithTimeout(context.Background(), helperLife)
	defer cancel()

	var dialer net.Dialer

	conn, err := dialer.DialContext(ctx, "tcp", os.Getenv(helperAddrEnv))
	if err != nil {
		return 1
	}

	defer func() { _ = conn.Close() }()

	deadline, _ := ctx.Deadline()
	if err = conn.SetReadDeadline(deadline); err != nil {
		return 1
	}

	// The test never writes: the read returns when it closes the connection or the deadline passes.
	_, _ = conn.Read(make([]byte, 1))

	return 0
}

// spawnLingerer starts a lingering helper that inherits this process's output and does not wait for it.
func spawnLingerer() int {
	binary, err := os.Executable()
	if err != nil {
		return 1
	}

	//nolint:gosec // G204: the test binary starts itself; nothing here comes from outside the test.
	lingerer := exec.CommandContext(context.Background(), binary)
	lingerer.Env = append(os.Environ(), helperModeEnv+"="+helperLinger)
	lingerer.Stdout = os.Stdout
	lingerer.Stderr = os.Stderr

	if err = lingerer.Start(); err != nil {
		return 1
	}

	return 0
}
