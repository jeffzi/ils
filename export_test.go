package main

import (
	"os"

	"github.com/jeffzi/ils/internal/cli"
)

// Terminal is terminal, for the tests of package main_test.
type Terminal = terminal

// Launched is launched, for the tests of package main_test.
func Launched(stdout, stderr *os.File, tty Terminal) cli.App {
	return launched(stdout, stderr, tty)
}

// Foreground is foreground, for the tests of package main_test.
func Foreground(fd uintptr) bool {
	return foreground(fd)
}
