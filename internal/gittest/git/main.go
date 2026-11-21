// Git is the fake git of package gittest built as a program of its own, which
// gittest.InstallFakeGit puts first on PATH under git's name.
package main

import (
	"os"

	"github.com/jeffzi/ils/internal/gittest"
)

func main() {
	os.Exit(gittest.ProgramMain())
}
