//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package cli

import (
	"fmt"
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

	return sigaction(syscall.SIGTSTP, nil, &old) == 0 && old.handler == sigIgnore
}

// setDefault gives sig its default action in the kernel, past the Go runtime, whose own handler
// for a quit dumps every goroutine and exits instead of letting the signal end the process.
func setDefault(sig syscall.Signal) error {
	if errno := sigaction(sig, &disposition{handler: sigDefault}, nil); errno != 0 {
		return fmt.Errorf("set the default action of %v: %w", sig, errno)
	}

	return nil
}

// sigaction sets the action of sig to act unless act is nil, and writes the action it replaces
// to old unless old is nil.
func sigaction(sig syscall.Signal, act, old *disposition) syscall.Errno {
	//nolint:gosec // rt_sigaction has no wrapper in x/sys/unix; the kernel reads and writes the actions through the pointers
	_, _, errno := unix.RawSyscall6(
		unix.SYS_RT_SIGACTION,
		uintptr(sig),
		uintptr(unsafe.Pointer(act)),
		uintptr(unsafe.Pointer(old)),
		signalSetSize,
		0,
		0,
	)

	return errno
}

// sigDefault and sigIgnore are the handlers of a signal under its default action, SIG_DFL, and of
// a signal the process ignores, SIG_IGN.
const (
	sigDefault = 0
	sigIgnore  = 1
)

// disposition is the kernel's struct sigaction, whose handler comes first on every architecture
// but MIPS; rest holds the fields after it, with room to spare.
type disposition struct {
	handler uintptr
	rest    [32]byte
}
