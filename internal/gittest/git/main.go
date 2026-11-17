// Git is the fake git of package gittest built as a program of its own, which
// gittest.InstallFakeGit puts first on PATH under git's name.
package main

import (
	"fmt"
	"os"

	"github.com/jeffzi/ils/internal/gittest"
)

func main() {
	code, served := gittest.Main()
	if !served {
		_, _ = fmt.Fprintf(os.Stderr, "fake git: run as %s, want it run under git's name\n", os.Args[0])
		code = gittest.BrokenCode
	}

	os.Exit(code)
}
