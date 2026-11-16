// Gh is the fake gh of package ghtest built as a program of its own, which ghtest.Install points
// GH_PATH at.
package main

import (
	"fmt"
	"os"

	"github.com/jeffzi/ils/internal/github/ghtest"
)

func main() {
	code, served := ghtest.Main()
	if !served {
		_, _ = fmt.Fprintln(os.Stderr, "ghtest: the fake gh ran with no script, want the settings of ghtest.Fake.Settings in its environment")
		code = ghtest.BrokenCode
	}

	os.Exit(code)
}
