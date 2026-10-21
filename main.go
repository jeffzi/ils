// Ils lands a branch on GitHub in fixed, checked steps: push, pull request, CI verdict.
package main

import (
	"context"
	"os"
	"time"

	"github.com/jeffzi/ils/internal/cli"
)

func main() {
	app := cli.App{Stdout: os.Stdout, Stderr: os.Stderr, Launch: time.Now()}

	os.Exit(app.Run(context.Background(), os.Args[1:]))
}
