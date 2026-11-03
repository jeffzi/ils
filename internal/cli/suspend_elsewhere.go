//go:build unix && !darwin && !dragonfly && !freebsd && !(linux && !mips && !mipsle && !mips64 && !mips64le)

package cli

// stopIgnored reports false: on this system ils does not read the disposition of SIGTSTP, so a
// process started with it ignored stops on it all the same.
func stopIgnored() bool { return false }
