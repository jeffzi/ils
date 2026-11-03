//go:build darwin || dragonfly || freebsd

package cli

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// stopIgnored reports whether the process ignores SIGTSTP, as the kernel holds it: the Go runtime
// keeps no record of a stop signal ignored from the start, so signal.Ignored reports false for it.
// A disposition that cannot be read counts as not ignored.
func stopIgnored() bool {
	var old disposition
	// x/sys/unix has no sigaction wrapper, from libSystem or otherwise, so the call is a raw one
	// through a pointer the kernel writes the old action to.
	//nolint:gosec,staticcheck // no wrapper exists to use instead of the raw call
	_, _, errno := unix.RawSyscall(unix.SYS_SIGACTION, uintptr(syscall.SIGTSTP), 0, uintptr(unsafe.Pointer(&old)))

	return errno == 0 && old.handler == sigIgnore
}

// sigIgnore is the handler of a signal the process ignores, SIG_IGN.
const sigIgnore = 1

// disposition is the kernel's struct sigaction, whose handler comes first; rest holds the fields
// after it, with room to spare.
type disposition struct {
	handler uintptr
	rest    [32]byte
}
