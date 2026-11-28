//go:build !darwin && !linux

package cli

import "syscall"

// stopIgnored reports false: on this system ils does not read the disposition of SIGTSTP, so a
// process started with it ignored stops on it all the same.
func stopIgnored() bool { return false }

// setDefault does nothing: on this system ils sets no disposition, so the signal sent next ends
// the process as the Go runtime ends it, which for a quit dumps every goroutine and exits 2.
func setDefault(syscall.Signal) error { return nil }
