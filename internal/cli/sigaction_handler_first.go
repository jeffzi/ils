//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package cli

// signalSetSize is the size in bytes of the kernel's signal set, which rt_sigaction checks.
const signalSetSize = 8

// disposition is the kernel's struct sigaction where its handler comes first, on every
// architecture but MIPS; rest holds the fields after it, with room to spare.
type disposition struct {
	handler uintptr
	rest    [32]byte
}
