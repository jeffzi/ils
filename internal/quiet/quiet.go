// Package quiet keeps a test binary built with -race from pausing before it exits.
package quiet

import (
	"os"
	"strings"
)

// Setting returns the value of GORACE that stops a binary built with -race from sleeping a second
// before every exit, which each call of a fake that the test binary stands in for would pay. It
// keeps the options the environment already holds.
func Setting() string {
	return strings.TrimSpace(os.Getenv("GORACE") + " atexit_sleep_ms=0")
}
