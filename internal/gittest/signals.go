package gittest

import (
	"runtime"
	"testing"
)

// NeedSignals skips the test on Windows, where a shell cannot end a git process by a signal.
func NeedSignals(tb testing.TB) {
	tb.Helper()

	if runtime.GOOS == "windows" {
		tb.Skip("Windows has no signals for a shell to end a git process with")
	}
}
