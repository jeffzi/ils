//go:build unix

package guard

import (
	"maps"
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

// settled returns the redirects that, applied after those that left each descriptor on the file
// files names, put it on the file wanted names, in descriptor order: one wanted on no file is
// closed, and one wanted on another file is opened on it.
func settled(files, wanted map[string]Word) []Redirect {
	fds := slices.Concat(slices.Collect(maps.Keys(files)), slices.Collect(maps.Keys(wanted)))
	slices.Sort(fds)

	var fixes []Redirect
	for _, fd := range slices.Compact(fds) {
		current, onFile := files[fd]
		target, wantFile := wanted[fd]
		switch {
		case wantFile && current != target:
			fixes = append(fixes, Redirect{Fd: fd, Op: ">", Target: target})
		case !wantFile && onFile:
			fixes = append(fixes, Redirect{Fd: fd, Op: ">&", Target: Word{Text: "-", Literal: true}})
		}
	}

	return fixes
}

// restored returns the redirects in effect once a command whose own redirects are own ends, given
// those in effect before it and those at its end. bash and zsh put back only each descriptor the
// command redirected, so what an `exec` inside it did to any other descriptor holds after it.
func restored(before, own, after []Redirect) []Redirect {
	if len(own) == 0 {
		return after
	}

	files := filesOf(after)
	beforeFiles := filesOf(before)
	wanted := maps.Clone(files)
	for _, r := range own {
		for _, fd := range touched(r) {
			delete(wanted, fd)
			if target, onFile := beforeFiles[fd]; onFile {
				wanted[fd] = target
			}
		}
	}

	return slices.Concat(after, settled(files, wanted))
}

// touched returns the descriptors the redirect r changes: `&>`, and `>&` onto a word that is no
// descriptor, change stdout and stderr; an unnumbered redirect changes stdin when it reads, and
// stdout otherwise.
func touched(r Redirect) []string {
	if strings.HasPrefix(r.Op, "&>") || (r.Op == ">&" && r.Fd == "" && !isDescriptor(r.Target)) {
		return []string{stdoutFd, stderrFd}
	}

	if r.Fd != "" {
		return []string{r.Fd}
	}

	if strings.HasPrefix(r.Op, "<") {
		return []string{stdinFd}
	}

	return []string{stdoutFd}
}

func ontoStdout(r Redirect) bool {
	return slices.Contains(touched(r), stdoutFd)
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
		delete(files, fd(stdinFd))
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

func redirectsOf(stmt *syntax.Stmt, ex expander) []Redirect {
	redirects := make([]Redirect, 0, len(stmt.Redirs))
	for _, r := range stmt.Redirs {
		redirect := Redirect{Op: r.Op.String(), Target: readWord(r.Word, ex)}
		if r.N != nil {
			redirect.Fd = r.N.Value
		}

		redirects = append(redirects, redirect)
	}

	return redirects
}
