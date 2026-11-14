// Ils lands a branch on GitHub in fixed, checked steps: push, pull request, CI verdict.
package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"time"
	"unsafe"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"

	"github.com/jeffzi/ils/internal/cli"
)

// shipped holds the skills `ils init` installs, under skills/.
//
//go:embed skills
var shipped embed.FS

func main() {
	app := launched(os.Stdout, os.Stderr, terminal{IsTerminal: term.IsTerminal, Size: term.GetSize, Foreground: foreground})

	os.Exit(app.Run(context.Background(), os.Args[1:]))
}

// terminal holds the terminal queries launched makes on file descriptors, so that tests can stand
// in for the real terminal.
type terminal struct {
	IsTerminal func(fd uintptr) bool
	Size       func(fd uintptr) (width, height int, err error)
	Foreground func(fd uintptr) bool
}

// launched returns the launch of ils that reads the process's stdin and prints on stdout and
// stderr, launched now under the process's environment, with tty reading their terminals: whether
// each is one, and the width of stderr's and whether ils is its foreground job.
func launched(stdout, stderr *os.File, tty terminal) cli.App {
	return cli.App{
		Stdin:          os.Stdin,
		Stdout:         stdout,
		Stderr:         stderr,
		Environ:        os.Environ(),
		StdoutTerminal: tty.IsTerminal(stdout.Fd()),
		StderrTerminal: tty.IsTerminal(stderr.Fd()),
		StderrWidth: func() int {
			// A width that cannot be read counts as none, which the display reads as 80.
			width, _, err := tty.Size(stderr.Fd())
			if err != nil {
				return 0
			}

			return width
		},
		StderrForeground: func() bool { return tty.Foreground(stderr.Fd()) },
		Notify:           signal.Notify,
		Launch:           time.Now(),
		Skills:           skills(),
	}
}

// skills returns the embedded skills with one directory per skill at its root.
func skills() fs.FS {
	tree, err := fs.Sub(shipped, "skills")
	if err != nil {
		// Sub fails only on a malformed directory name, and "skills" is a constant.
		panic(fmt.Sprintf("ils: the embedded skills: %v", err))
	}

	return tree
}

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
