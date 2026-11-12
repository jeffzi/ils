//go:build unix

// cspell:ignore Clob -- the shell parser's name for its clobbering redirect operators.

package guard

import (
	"cmp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Redirect is one redirection of an invocation.
type Redirect struct {
	// Dir is the directory the shell opens Target from: the one the command carrying the redirect
	// starts in, before anything that command runs moves it. Empty when the guard cannot tell.
	Dir string
	// Fd is the descriptor written before the operator, empty when none is.
	Fd     string
	Target Word
	Op     syntax.RedirOperator
}

const (
	stdinFd  = "0"
	stdoutFd = "1"
	stderrFd = "2"
)

// openedIn returns a copy of redirects, each opened from dir.
func openedIn(redirects []Redirect, dir string) []Redirect {
	opened := slices.Clone(redirects)
	for i := range opened {
		opened[i].Dir = dir
	}

	return opened
}

// stdoutFile returns the redirect that opened the file the invocation's stdout is on, with the
// directory the shell opened it from, and false when its redirects leave stdout on a descriptor
// rather than a file. The redirects in effect where it runs come before its own: those of each
// group or subshell around it. Each redirect applies in order, as in the shell, so `2> f >&2` sends
// stdout to f, while `>&2 2> f` leaves it where stderr was.
func (inv Invocation) stdoutFile() (Redirect, bool) {
	opened, toFile := filesOf(slices.Concat(inv.enclosing, inv.Redirects))[stdoutFd]

	return opened, toFile
}

// filesOf returns the redirect that opened the file each descriptor is on once redirects apply in
// order.
func filesOf(redirects []Redirect) map[string]Redirect {
	files := map[string]Redirect{}
	for _, r := range redirects {
		applyRedirect(files, r)
	}

	return files
}

// applyRedirect updates files, the redirect that opened the file each descriptor is on, with the
// redirect r. A descriptor absent from files is on no file.
func applyRedirect(files map[string]Redirect, r Redirect) {
	switch r.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.AppClob:
		files[cmp.Or(r.Fd, stdoutFd)] = r
	case syntax.RdrAll, syntax.AppAll, syntax.RdrAllClob, syntax.AppAllClob:
		files[stdoutFd], files[stderrFd] = r, r
	case syntax.RdrInOut:
		files[cmp.Or(r.Fd, stdinFd)] = r
	case syntax.RdrIn, syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		// A descriptor opened for reading only, or on a heredoc, cannot write over a file.
		delete(files, cmp.Or(r.Fd, stdinFd))
	case syntax.DplOut, syntax.DplIn:
		into := cmp.Or(r.Fd, stdoutFd)
		if r.Op == syntax.DplIn {
			into = cmp.Or(r.Fd, stdinFd)
		}

		if !isDescriptor(r.Target) {
			// `>&word` with a word that is no descriptor sends stdout and stderr to that file.
			files[into] = r
			if r.Fd == "" {
				files[stderrFd] = r
			}

			return
		}

		if source, isFile := files[r.Target.Text]; isFile {
			files[into] = source
		} else {
			delete(files, into)
		}
	}
}

func isDescriptor(w Word) bool {
	if !w.Literal || w.Text == "" {
		return false
	}

	return w.Text == "-" || strings.Trim(w.Text, "0123456789") == ""
}

func redirectsOf(stmt *syntax.Stmt, ex expander) []Redirect {
	redirects := make([]Redirect, 0, len(stmt.Redirs))
	for _, r := range stmt.Redirs {
		redirect := Redirect{Op: r.Op, Target: readWord(r.Word, ex)}
		if r.N != nil {
			redirect.Fd = r.N.Value
		}

		redirects = append(redirects, redirect)
	}

	return redirects
}
