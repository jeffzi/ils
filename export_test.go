package main

import (
	"os"

	"github.com/jeffzi/ils/internal/cli"
)

// Launched is launched, for the tests of package main_test.
func Launched(
	stdout, stderr *os.File, isTerminal func(fd uintptr) bool, size func(fd uintptr) (width, height int, err error),
) cli.App {
	return launched(stdout, stderr, isTerminal, size)
}
