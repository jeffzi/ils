// Ils lands a branch on GitHub in fixed, checked steps: push, pull request, CI verdict.
package main

import (
	"context"
	"os"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/jeffzi/ils/internal/cli"
)

func main() {
	app := launched(os.Stdout, os.Stderr, term.IsTerminal, term.GetSize)

	os.Exit(app.Run(context.Background(), os.Args[1:]))
}

// launched returns the launch of ils that prints on stdout and stderr, launched now under the
// process's environment: isTerminal tells whether the stream of a file descriptor is a terminal,
// and size reads the width of the terminal stderr is.
func launched(
	stdout, stderr *os.File, isTerminal func(fd uintptr) bool, size func(fd uintptr) (width, height int, err error),
) cli.App {
	return cli.App{
		Stdout:         stdout,
		Stderr:         stderr,
		Environ:        os.Environ(),
		StdoutTerminal: isTerminal(stdout.Fd()),
		StderrTerminal: isTerminal(stderr.Fd()),
		StderrWidth: func() int {
			// A width that cannot be read counts as none, which the display reads as 80.
			width, _, err := size(stderr.Fd())
			if err != nil {
				return 0
			}

			return width
		},
		Launch: time.Now(),
	}
}
