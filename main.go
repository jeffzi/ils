// Ils lands a branch on GitHub in fixed, checked steps: push, pull request, CI verdict.
package main

import (
	"context"
	"os"
	"os/signal"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/jeffzi/ils/internal/cli"
)

func main() {
	app := launched(os.Stdout, os.Stderr, terminal{IsTerminal: term.IsTerminal, Size: term.GetSize, Foreground: foreground})

	os.Exit(app.Run(context.Background(), os.Args[1:]))
}

// terminal holds the terminal queries launched makes on file descriptors, so that tests can stand
// in for the real terminal.
type terminal struct {
	IsTerminal func(fd uintptr) bool
	Size       func(fd uintptr) (width, height int, err error)
	Foreground func(fd uintptr) bool
}

// launched returns the launch of ils that prints on stdout and stderr, launched now under the
// process's environment, with tty reading their terminals: whether each is one, and the width of
// stderr's and whether ils is its foreground job.
func launched(stdout, stderr *os.File, tty terminal) cli.App {
	return cli.App{
		Stdout:         stdout,
		Stderr:         stderr,
		Environ:        os.Environ(),
		StdoutTerminal: tty.IsTerminal(stdout.Fd()),
		StderrTerminal: tty.IsTerminal(stderr.Fd()),
		StderrWidth: func() int {
			// A width that cannot be read counts as none, which the display reads as 80.
			width, _, err := tty.Size(stderr.Fd())
			if err != nil {
				return 0
			}

			return width
		},
		StderrForeground: func() bool { return tty.Foreground(stderr.Fd()) },
		Notify:           signal.Notify,
		Launch:           time.Now(),
	}
}
