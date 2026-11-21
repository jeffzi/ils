// Gh is the fake gh of package ghtest built as a program of its own, which ghtest.Install points
// GH_PATH at.
package main

import (
	"os"

	"github.com/jeffzi/ils/internal/github/ghtest"
)

func main() {
	os.Exit(ghtest.ProgramMain())
}
