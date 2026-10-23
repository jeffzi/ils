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
	app := cli.App{
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
		Environ:        os.Environ(),
		StdoutTerminal: term.IsTerminal(os.Stdout.Fd()),
		StderrTerminal: term.IsTerminal(os.Stderr.Fd()),
		StderrWidth: func() int {
			// A width that cannot be read counts as none, which the display reads as 80.
			width, _, err := term.GetSize(os.Stderr.Fd())
			if err != nil {
				return 0
			}

			return width
		},
		Launch: time.Now(),
	}

	os.Exit(app.Run(context.Background(), os.Args[1:]))
}
