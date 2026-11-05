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

// Stdout returns the file the invocation's stdout is redirected to, and false when its redirects
// leave stdout on a descriptor rather than a file. The redirects in effect where it runs come
// before its own: those of each group or subshell around it. Each redirect applies in order, as in
// the shell, so `2> f >&2` sends stdout to f, while `>&2 2> f` leaves it where stderr was.
func (inv Invocation) Stdout() (Word, bool) {
	target, toFile := filesOf(slices.Concat(inv.enclosing, inv.Redirects))[stdoutFd]

	return target, toFile
}

// filesOf returns the file each descriptor is open on once redirects apply in order.
func filesOf(redirects []Redirect) map[string]Word {
	files := map[string]Word{}
	for _, r := range redirects {
		applyRedirect(files, r)
	}

	return files
}

// applyRedirect updates files, the file each descriptor is open on, with the redirect r. A
// descriptor absent from files is on no file.
func applyRedirect(files map[string]Word, r Redirect) {
	switch r.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.AppClob:
		files[cmp.Or(r.Fd, stdoutFd)] = r.Target
	case syntax.RdrAll, syntax.AppAll, syntax.RdrAllClob, syntax.AppAllClob:
		files[stdoutFd], files[stderrFd] = r.Target, r.Target
	case syntax.RdrInOut:
		files[cmp.Or(r.Fd, stdinFd)] = r.Target
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
			files[into] = r.Target
			if r.Fd == "" {
				files[stderrFd] = r.Target
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
