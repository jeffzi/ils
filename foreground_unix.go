//go:build unix

package main

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// foreground reports whether ils is the foreground job of the terminal fd is: whether the
// terminal's foreground process group is the process's own. A descriptor that is no terminal, or a
// group that cannot be read, counts as the foreground job.
func foreground(fd uintptr) bool {
	read, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP)
	if err != nil {
		return true
	}

	// The kernel writes the group as a 32-bit pid_t into the first four bytes of read, which on a
	// big-endian 64-bit system are its high half: read those four bytes back as the int32 they are.
	terminalGroup := *(*int32)(unsafe.Pointer(&read)) //nolint:gosec // G103: the four bytes the kernel wrote

	ownGroup, err := unix.Getpgid(0)
	if err != nil {
		return true
	}

	return int(terminalGroup) == ownGroup
}
