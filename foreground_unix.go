//go:build unix

package main

import "golang.org/x/sys/unix"

// foreground reports whether ils is the foreground job of the terminal fd is: whether the
// terminal's foreground process group is the process's own. A descriptor that is no terminal, or a
// group that cannot be read, counts as the foreground job.
func foreground(fd uintptr) bool {
	terminalGroup, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP)
	if err != nil {
		return true
	}

	ownGroup, err := unix.Getpgid(0)
	if err != nil {
		return true
	}

	return terminalGroup == ownGroup
}
