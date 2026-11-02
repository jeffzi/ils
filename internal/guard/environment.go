//go:build unix

package guard

import (
	"mvdan.cc/sh/v3/syntax"
)

// environment is what the guard knows of the variables it follows: those that point git at a
// repository, CDPATH, which `cd` reads a relative path against, and those that choose the config
// directory gh reads its aliases from.
type environment struct {
	gitDir   variable
	workTree variable
	cdPath   variable
	ghConfig ghConfig
}

// unknownEnvironment returns the environment in which every variable the guard follows is unknown.
func unknownEnvironment() environment {
	unknown := variable{unknown: true}

	return environment{
		gitDir: unknown, workTree: unknown, cdPath: unknown,
		ghConfig: ghConfig{configDir: unknown, configHome: unknown, home: unknown},
	}
}

// envChange is one step in building the environment a command runs with: an assignment, or what
// an `env -i` or `env -u NAME` removes.
type envChange struct {
	// unset is the name `env -u` removes; empty for any other change.
	unset      string
	assignment Assignment
	// clear reports whether the change is `env -i`, which removes every variable before it.
	clear bool
}

// variable is what the guard knows of one variable.
type variable struct {
	value   string
	set     bool
	unknown bool
}

// forgetCDPath makes CDPATH unknown in e when file assigns it anywhere. A function body is walked
// where it is declared rather than where it is called, so an assignment the walk has not reached
// in order may still be in effect when a `cd` runs.
func (e *environment) forgetCDPath(file *syntax.File) {
	if assigns(file, "CDPATH") {
		e.cdPath = variable{unknown: true}
	}
}

func assigns(node syntax.Node, name string) bool {
	found := false
	syntax.Walk(node, func(n syntax.Node) bool {
		if a, ok := n.(*syntax.Assign); ok && a.Name != nil && a.Name.Value == name {
			found = true
		}

		return !found
	})

	return found
}

func (v variable) or(other variable) variable {
	if v != other {
		return variable{unknown: true}
	}

	return v
}

// set records v as the value of name, when name is a variable the guard follows.
func (e *environment) set(name string, v variable) {
	switch name {
	case "GIT_DIR":
		e.gitDir = v
	case "GIT_WORK_TREE":
		e.workTree = v
	case "CDPATH":
		e.cdPath = v
	case ghConfigDirVar:
		e.ghConfig.configDir = v
	case ghConfigHomeVar:
		e.ghConfig.configHome = v
	case ghHomeVar:
		e.ghConfig.home = v
	}
}

func valueOf(w Word) variable {
	if !w.Literal {
		return variable{unknown: true}
	}

	return variable{value: w.Text, set: true}
}

// resolve returns the path v holds, made absolute against dir, and false when the guard cannot
// tell it.
func (v variable) resolve(dir string) (string, bool) {
	switch {
	case v.unknown:
		return "", false
	case !v.set:
		return "", true
	}

	abs := absolute(dir, v.value)

	return abs, abs != ""
}

// commandEnv returns the environment the invocation inv runs with, run from sc: the shell's, then
// inv's changes in the order bash makes them, its assignment prefix first and then each `env`
// wrapper's. What `env -i` or `env -u` removes is unknown rather than unset.
func (sc *scope) commandEnv(inv Invocation) environment {
	env := sc.env
	for _, c := range inv.env {
		switch {
		case c.clear:
			env = unknownEnvironment()
		case c.unset != "":
			env.set(c.unset, variable{unknown: true})
		default:
			env.set(c.assignment.Name, valueOf(c.assignment.Value))
		}
	}

	return env
}

// declare changes sc the way the declaration builtin decl does. Only `export NAME=value` with no
// option sets a git variable the guard follows; any other declaration of one makes it unknown.
func (sc *scope) declare(decl *syntax.DeclClause, home string) {
	plain := decl.Variant.Value == "export"
	for _, a := range decl.Args {
		if a.Name == nil {
			plain = false
		}
	}

	for _, a := range decl.Args {
		if a.Name == nil {
			continue
		}

		v := variable{unknown: true}
		if plain && !a.Naked && !a.Append {
			v = valueOf(assignmentOf(a, home).Value)
		}

		sc.env.set(a.Name.Value, v)
	}
}

// declared returns the assignments the declaration decl makes: each `NAME=value` among its
// arguments.
func declared(decl *syntax.DeclClause, home string) []Assignment {
	var assignments []Assignment
	for _, a := range decl.Args {
		if a.Name != nil && !a.Naked {
			assignments = append(assignments, assignmentOf(a, home))
		}
	}

	return assignments
}
