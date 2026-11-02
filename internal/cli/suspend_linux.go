//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package cli

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// signalSetSize is the size in bytes of the kernel's signal set, which rt_sigaction checks.
const signalSetSize = 8

// stopIgnored reports whether the process ignores SIGTSTP, as the kernel holds it: the Go runtime
// keeps no record of a stop signal ignored from the start, so signal.Ignored reports false for it.
// A disposition that cannot be read counts as not ignored.
func stopIgnored() bool {
	var old disposition
	//nolint:gosec // rt_sigaction has no wrapper in x/sys/unix; the kernel writes the old action through the pointer
	_, _, errno := unix.RawSyscall6(
		unix.SYS_RT_SIGACTION,
		uintptr(syscall.SIGTSTP),
		0,
		uintptr(unsafe.Pointer(&old)),
		signalSetSize,
		0,
		0,
	)

	return errno == 0 && old.handler == sigIgnore
}

// sigIgnore is the handler of a signal the process ignores, SIG_IGN.
const sigIgnore = 1

// disposition is the kernel's struct sigaction, whose handler comes first on every architecture
// but MIPS; rest holds the fields after it, with room to spare.
type disposition struct {
	handler uintptr
	rest    [32]byte
}
