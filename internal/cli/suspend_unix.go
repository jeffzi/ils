//go:build unix

package cli

import (
	"os"
	"syscall"
)

// onStop listens through Notify, for nothing when Notify is nil, for the stop signal of a terminal,
// and on each one takes the spinner line of shown off stderr and stops the process, which goes on
// where it was once continued. A stop that cannot be sent leaves the process running, with nothing
// reported: as with the display's own writes, a failed stop never changes the result.
//
// A process started with the stop signal ignored listens for nothing, so that it keeps ignoring it:
// Notify would replace the ignore with a handler, and the listener would stop a process its parent
// meant to keep running.
//
// The listener stays for the rest of the process, after the landing command returns too: once
// Notify was called for SIGTSTP, detaching it does not give the signal back its default of
// stopping the process, so the listener is what stops it. For the same reason it stops the process
// with SIGSTOP, never by sending SIGTSTP again.
func (a App) onStop(shown *display) {
	if a.Notify == nil || stopIgnored() {
		return
	}

	stop := func() error { return syscall.Kill(os.Getpid(), syscall.SIGSTOP) }

	stops := make(chan os.Signal, 1)
	a.Notify(stops, syscall.SIGTSTP)

	go func() {
		for range stops {
			shown.suspend(stop)
		}
	}()
}
