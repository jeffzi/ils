//go:build unix

package guard

import (
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Redirect is one redirection of an invocation.
type Redirect struct {
	// Fd is the descriptor written before the operator, empty when none is.
	Fd     string
	Op     string
	Target Word
}

const (
	stdinFd  = "0"
	stdoutFd = "1"
	stderrFd = "2"
)

// Stdout returns the file the invocation's stdout is redirected to, and false when its redirects
// leave stdout on a descriptor rather than a file. The redirects in effect where it runs come
// before its own: those of each group or subshell around it, and those of an earlier `exec` with
// no command. Each redirect applies in order, as in the shell, so `2> f >&2` sends stdout to f,
// while `>&2 2> f` leaves it where stderr was.
func (inv Invocation) Stdout() (Word, bool) {
	return stdoutFile(slices.Concat(inv.enclosing, inv.Redirects))
}

// stdoutFile returns the file stdout is on once redirects apply in order, and false when it is on no
// file.
func stdoutFile(redirects []Redirect) (Word, bool) {
	files := map[string]Word{}
	for _, r := range redirects {
		applyRedirect(files, r)
	}

	target, toFile := files[stdoutFd]

	return target, toFile
}

// applyRedirect updates files, the file each descriptor is open on, with the redirect r. A
// descriptor absent from files is on no file.
func applyRedirect(files map[string]Word, r Redirect) {
	fd := func(fallback string) string {
		if r.Fd == "" {
			return fallback
		}

		return r.Fd
	}
	open := func(descriptor string, target Word, isFile bool) {
		if isFile {
			files[descriptor] = target
		} else {
			delete(files, descriptor)
		}
	}

	switch r.Op {
	case ">", ">>", ">|", ">>|":
		open(fd(stdoutFd), r.Target, true)
	case "&>", "&>>", "&>|", "&>>|":
		open(stdoutFd, r.Target, true)
		open(stderrFd, r.Target, true)
	case "<>":
		open(fd(stdinFd), r.Target, true)
	case "<":
		// A descriptor opened for reading only cannot write over the file.
		open(fd(stdinFd), r.Target, false)
	case ">&", "<&":
		into := fd(stdoutFd)
		if r.Op == "<&" {
			into = fd(stdinFd)
		}

		if !isDescriptor(r.Target) {
			// `>&word` with a word that is no descriptor sends stdout and stderr to that file.
			open(into, r.Target, true)
			if r.Fd == "" {
				open(stderrFd, r.Target, true)
			}

			return
		}

		source, isFile := files[r.Target.Text]
		open(into, source, isFile)
	}
}

func isDescriptor(w Word) bool {
	if !w.Literal || w.Text == "" {
		return false
	}

	return w.Text == "-" || strings.Trim(w.Text, "0123456789") == ""
}

func redirectsOf(stmt *syntax.Stmt, home string) []Redirect {
	redirects := make([]Redirect, 0, len(stmt.Redirs))
	for _, r := range stmt.Redirs {
		redirect := Redirect{Op: r.Op.String(), Target: readWord(r.Word, home)}
		if r.N != nil {
			redirect.Fd = r.N.Value
		}

		redirects = append(redirects, redirect)
	}

	return redirects
}
