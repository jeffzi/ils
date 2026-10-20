// Package main is the entry point for ils.
package main

import (
	"context"
	"os"
	"runtime/debug"
	"time"

	"github.com/jeffzi/ils/internal/cli"
)

func main() {
	app := cli.App{Stdout: os.Stdout, Stderr: os.Stderr, Launch: time.Now(), Version: cli.Version(debug.ReadBuildInfo())}

	os.Exit(app.Run(context.Background(), os.Args[1:]))
}
