//go:build linux && (mips || mipsle || mips64 || mips64le)

package cli

// signalSetSize is the size in bytes of the kernel's signal set on MIPS, which holds 128 signals
// and which rt_sigaction checks.
const signalSetSize = 16

// disposition is the kernel's struct sigaction on MIPS, whose flags come before its handler; rest
// holds the signal set after it, with room to spare.
type disposition struct {
	flags   uint32
	handler uintptr
	rest    [32]byte
}
