//go:build unix

// cspell:ignore Arithm Arit Assgn OLDPWD OPTARG OPTIND SRANDOM EPOCHSECONDS EPOCHREALTIME BASHPID EUID -- the shell parser's names for its arithmetic nodes, and variables the shell sets itself.

package guard

import (
	"maps"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// The variables the guard follows to tell where git runs.
const (
	// cdPathVar is the variable `cd` reads a relative path against.
	cdPathVar      = "CDPATH"
	gitDirVar      = "GIT_DIR"
	gitWorkTreeVar = "GIT_WORK_TREE"
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

// variable is what the guard knows of one variable: its value when set, and that it cannot tell the
// value when unknown. One that is neither has not been assigned, and its reader decides what that
// leaves: a gh run keeps the value of the runner's environment, while a git run or a `cd` sees none.
type variable struct {
	value   string
	set     bool
	unknown bool
}

// forgetCDPath makes CDPATH unknown in e when file assigns it anywhere. A function body is walked
// where it is declared rather than where it is called, so an assignment the walk has not reached
// in order may still be in effect when a `cd` runs.
func (e *environment) forgetCDPath(file *syntax.File) {
	if assigns(file, cdPathVar) {
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
	case gitDirVar:
		e.gitDir = v
	case gitWorkTreeVar:
		e.workTree = v
	case cdPathVar:
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

// declare changes sc the way the declaration builtin decl, whose values ex reads, does. Only
// `export NAME=value` with no option sets a git variable the guard follows; any other declaration
// of one makes it unknown. The shell variables change as shellVars.declared says. An argument the
// parser did not split into a name and a value (`export "$v=x"`) makes the variable it names
// unknown, and every variable when the guard cannot read the name.
func (sc *scope) declare(decl *syntax.DeclClause, ex expander) {
	plain := decl.Variant.Value == "export"
	for _, a := range decl.Args {
		if a.Name == nil {
			plain = false
		}
	}

	for _, a := range decl.Args {
		if a.Name == nil {
			name, assigns := unsplitName(a, ex)
			switch {
			case !assigns:
			case name == "":
				sc.env = unknownEnvironment()
				sc.vars = sc.vars.forgetAll()
			default:
				sc.env.set(name, variable{unknown: true})
				sc.vars = sc.vars.forget(name)
			}

			continue
		}

		v := variable{unknown: true}
		if plain && !a.Naked && !a.Append {
			v = valueOf(assignmentOf(a, ex).Value)
		}

		sc.env.set(a.Name.Value, v)
	}

	sc.vars = sc.vars.declared(decl, ex)
}

// unsplitName returns the variable the argument a of a declaration builtin names when the parser
// did not split it into a name and a value, as in `export "NAME=value"` or `export "$v=value"`, and
// reports whether a may assign one. The name is empty when the guard cannot read it.
func unsplitName(a *syntax.Assign, ex expander) (name string, assigns bool) {
	if a.Name != nil || a.Value == nil {
		return "", false
	}

	word := readWord(a.Value, ex)
	prefix, _, found := strings.Cut(word.Text, "=")
	switch {
	case found:
		return strings.TrimSuffix(prefix, "+"), true
	case word.Literal:
		return "", false
	default:
		return "", true
	}
}

// declared returns the assignments the declaration decl, whose values ex reads, makes: each
// `NAME=value` among its arguments.
func declared(decl *syntax.DeclClause, ex expander) []Assignment {
	var assignments []Assignment
	for _, a := range decl.Args {
		if a.Name != nil && !a.Naked {
			assignments = append(assignments, assignmentOf(a, ex))
		}
	}

	return assignments
}

// ---------------------------------------------------------------------------
// Shell variables
// ---------------------------------------------------------------------------

// shellVars is what the guard knows of the shell's own variables, whose value an expansion in a
// word stands for. A variable it does not know is one the command never gave a literal, or one the
// guard cannot be sure still holds it: another kind of write may have changed it (`+=`, `read`,
// `printf -v`, `for NAME in`, `${NAME:=…}`, `unset`), or the assignment may not have run, or ran in
// another shell.
type shellVars struct {
	// known holds the value of each variable the guard knows. Copies of a scope share it, so a
	// change replaces it rather than changing it in place.
	known map[string]string
	// held counts, for each variable the command makes readonly or gives an option anywhere, such
	// as `declare -u`, which changes the value an assignment leaves, the declarations that name it.
	// No assignment of a held variable is read, other than a `readonly` that gives it its value and
	// is the only declaration naming it: with another one, the variable may be readonly already
	// where the `readonly` runs, and the shell then refuses the value.
	held map[string]int
	// unread reports whether no variable is known: the command may hold a name reference, which
	// reads one variable through another, or assign IFS, which changes where an expansion splits,
	// or a sourced file may have done either.
	unread bool
	// function reports whether the walk is inside a function body, where `local` assigns.
	function bool
}

// cspell:disable -- the names of bash's and zsh's own variables and builtins, as the shells list them.

// selfSetVariables are the variables bash or zsh gives a meaning of its own: those it defines as it
// starts, as `env -i bash --norc -c 'compgen -v'` and `env -i zsh -f -c 'print ${(k)parameters}'`
// list them, and those a command or a test sets without naming them, such as `[[ … =~ … ]]` or
// `read`. The shell may change one as it runs, refuse an assignment of it, or turn the value
// assigned into another (an integer), so a literal given to one may no longer be its value where a
// word reads it. Keep the list whole for both shells: a name left out reads as the literal the
// command gave it, whatever the shell has made of it since.
func selfSetVariables() map[string]bool {
	return setOf(
		// bash, as it starts.
		"_", "BASH", "BASH_ALIASES", "BASH_ARGC", "BASH_ARGV", "BASH_ARGV0", "BASH_CMDS", "BASH_COMMAND",
		"BASH_EXECUTION_STRING", "BASH_LINENO", "BASH_LOADABLES_PATH", "BASH_MONOSECONDS", "BASH_SOURCE",
		"BASH_SUBSHELL", "BASH_VERSINFO", "BASH_VERSION", "BASHOPTS", "BASHPID", "COMP_WORDBREAKS",
		"DIRSTACK", "EPOCHREALTIME", "EPOCHSECONDS", "EUID", "GROUPS", "HISTCMD", "HOSTNAME", "HOSTTYPE",
		"IFS", "LINENO", "MACHTYPE", "OPTERR", "OPTIND", "OSTYPE", "PATH", "PPID", "PS4", "PWD", "RANDOM",
		"SECONDS", "SHELL", "SHELLOPTS", "SHLVL", "SRANDOM", "TERM", "UID",
		// bash, set by a command or a test.
		"BASH_REMATCH", "COPROC", "COMP_CWORD", "COMP_KEY", "COMP_LINE", "COMP_POINT", "COMP_TYPE",
		"COMP_WORDS", "COMPREPLY", "FUNCNAME", "MAPFILE", "OLDPWD", "OPTARG", "PIPESTATUS", "READLINE_ARGUMENT",
		"READLINE_LINE", "READLINE_MARK", "READLINE_POINT", "REPLY", "COLUMNS", "LINES",
		// zsh, as it starts.
		"-", "!", "?", "@", "*", "#", "$", "0", "aliases", "ARGC", "argv", "builtins", "cdpath", "CDPATH",
		"commands", "CPUTYPE", "dirstack", "dis_aliases", "dis_builtins", "dis_functions",
		"dis_functions_source", "dis_galiases", "dis_patchars", "dis_reswords", "dis_saliases", "EGID",
		"fignore", "FIGNORE", "fpath", "FPATH", "funcfiletrace", "FUNCNEST", "funcsourcetrace", "funcstack",
		"functions", "functions_source", "functrace", "galiases", "GID", "histchars", "HISTCHARS",
		"history", "historywords", "HISTSIZE", "HOME", "HOST", "jobdirs", "jobstates", "jobtexts",
		"KEYBOARD_HACK", "keymaps", "KEYTIMEOUT", "LISTMAX", "LOGNAME", "MAILCHECK", "mailpath", "MAILPATH",
		"manpath", "MANPATH", "module_path", "MODULE_PATH", "modules", "nameddirs", "NULLCMD", "options",
		"parameters", "patchars", "path", "pipestatus", "prompt", "PROMPT", "PROMPT2", "PROMPT3", "PROMPT4",
		"PS1", "PS2", "PS3", "psvar", "PSVAR", "READNULLCMD", "reswords", "saliases", "SAVEHIST", "signals",
		"SPROMPT", "status", "termcap", "terminfo", "TIMEFMT", "TMPPREFIX", "TRY_BLOCK_ERROR",
		"TRY_BLOCK_INTERRUPT", "TTY", "TTYIDLE", "userdirs", "usergroups", "USERNAME", "VENDOR", "watch",
		"WATCH", "widgets", "WORDCHARS", "ZSH_ARGZERO", "zsh_eval_context", "ZSH_EVAL_CONTEXT",
		"ZSH_EXECUTION_STRING", "ZSH_NAME", "ZSH_PATCHLEVEL", "zsh_scheduled_events", "ZSH_SUBSHELL",
		"ZSH_VERSION",
		// zsh, set by a command or a test.
		"MATCH", "MBEGIN", "MEND", "match", "mbegin", "mend", "reply", "ERRNO", "EPOCHSECONDS",
		"EPOCHREALTIME", "epochtime", "TCP_LINE", "TCP_SESS", "ZFTP_CODE", "ZFTP_REPLY",
	)
}

// shellBuiltins are the builtins of bash and zsh, those of zsh's modules included, as `enable -a`
// and `print ${(k)builtins}` list them, other than those of zsh/files (`mkdir`, `rm`, `ln` and the
// like), which write no variable. A command word that is none of them, and no function, runs a
// program, which cannot change a variable of the shell.
func shellBuiltins() map[string]bool {
	return setOf(
		"-", ":", ".", "[", "alias", "autoload", "bg", "bind", "bindkey", "break", "builtin", "bye", "caller",
		"cap", "cd", "chdir", "clone", "command", "compadd", "comparguments", "compcall", "compctl",
		"compdescribe", "compfiles", "compgen", "complete", "compopt", "compgroups", "compquote", "compset",
		"comptags", "comptry", "compvalues", "continue", "declare", "dirs", "disable", "disown", "echo",
		"echotc", "echoti", "emulate", "enable", "eval", "exec", "exit", "export", "false", "fc", "fg",
		"float", "functions", "getcap", "getln", "getopts", "hash", "help", "history", "integer", "jobs",
		"kill", "let", "limit", "local", "log", "logout", "mapfile", "noglob", "pcre_compile", "pcre_match",
		"pcre_study", "popd", "print", "printf", "private", "pushd", "pushln", "pwd", "r", "read",
		"readarray", "readonly", "rehash", "return", "sched", "set", "setcap", "setopt", "shift", "shopt",
		"source", "stat", "strftime", "suspend", "syserror", "sysopen", "sysread", "sysseek", "syswrite",
		"test", "times", "trap", "true", "ttyctl", "type", "typeset", "ulimit", "umask", "unalias",
		"unfunction", "unhash", "unlimit", "unset", "unsetopt", "vared", "wait", "whence", "where", "which",
		"zcompile", "zcurses", "zdelattr", "zformat", "zftp", "zgetattr", "zle", "zlistattr", "zmodload",
		"zparseopts", "zprof", "zpty", "zregexparse", "zselect", "zsetattr", "zsocket", "zstat", "zstyle",
		"zsystem", "ztcp",
	)
}

// variableFreeBuiltins are the builtins that write no variable of the shell, other than those it
// sets on its own (see selfSetVariables). `eval` and `source` are among them because the walk
// applies what their strings and files do where it reads them; `printf` only while its first
// argument is a literal other than `-v`.
func variableFreeBuiltins() map[string]bool {
	return setOf(
		":", "true", "false", "echo", "printf", "test", "[", "cd", "pwd", "pushd", "popd", "dirs", "exit",
		"return", "break", "continue", "shift", "eval", "source", ".", "alias", "unalias", "hash", "type",
		"umask", "ulimit", "kill", "jobs", "which", "whence", "where",
	)
}

// cspell:enable

// ran returns v once inv, a simple command with a command word, has run in the shell. A program
// changes no variable of the shell, and a function call is applied by the walk of its body; a
// builtin that is not among variableFreeBuiltins, or a command word that is not a literal, may
// write any variable, so every one is unknown after it. When one of its words is not a literal or
// names IFS, it may also have written IFS, which changes where every later expansion splits, so no
// variable is read after it.
func (v shellVars) ran(inv Invocation, functions map[string]*function) shellVars {
	word := inv.Words[0]
	if word.Literal && (functions[word.Text] != nil || !shellBuiltins()[word.Text]) {
		return v
	}

	free := word.Literal && variableFreeBuiltins()[word.Text]
	if free && word.Text == "printf" {
		free = len(inv.Words) < 2 || inv.Words[1].Literal && !strings.HasPrefix(inv.Words[1].Text, "-v")
	}

	if free {
		return v
	}

	v = v.forgetAll()
	if slices.ContainsFunc(inv.Words, func(w Word) bool { return !w.Literal || strings.Contains(w.Text, "IFS") }) {
		v.unread = true
	}

	return v
}

// startVars returns the shell variables of a command, file, that starts with the environment env:
// none known, as the guard looks over file for what reads a variable otherwise than as its value
// (see watched). An IFS in env may change where an expansion splits.
func startVars(env map[string]string, file *syntax.File) shellVars {
	_, ifs := env["IFS"]

	return shellVars{unread: ifs}.watched(file)
}

// watched returns v once the guard has looked over file, a command or a string run in the shell,
// anywhere in it: a name reference, or a declaration option the guard cannot read, which may be
// one, or an assignment of IFS leaves v unread; a variable that `readonly` or a declaration with an
// option names is held. A declaration run through `builtin` or `command`, and zsh's `integer` and
// `float`, count as declarations.
func (v shellVars) watched(file *syntax.File) shellVars {
	if assigns(file, "IFS") {
		v.unread = true
	}

	held := maps.Clone(v.held)
	syntax.Walk(file, func(n syntax.Node) bool {
		decl, ok := n.(*syntax.DeclClause)
		if call, isCall := n.(*syntax.CallExpr); isCall {
			decl, ok = callDeclaration(call)
		}

		if !ok {
			return true
		}

		names, refers := declarationHolds(decl)
		v.unread = v.unread || refers
		for _, name := range names {
			if held == nil {
				held = map[string]int{}
			}

			held[name]++
		}

		return true
	})
	v.held = held

	return v
}

// declarationHolds returns the variables decl makes readonly, gives an option, or gives an
// attribute (see attributes), and reports whether it may declare a name reference: a `declare`,
// `typeset`, or `local` given `-n`, or an option the guard cannot read.
func declarationHolds(decl *syntax.DeclClause) ([]string, bool) {
	variant := decl.Variant.Value
	refers, options := false, false
	var names []string
	for _, a := range decl.Args {
		if a.Name != nil {
			names = append(names, a.Name.Value)

			continue
		}

		options = true
		option := ""
		if a.Value != nil {
			option = a.Value.Lit()
		}

		if variant == "declare" || variant == "typeset" || variant == "local" {
			refers = refers || option == "" || !strings.ContainsAny(option[:1], "-+") || strings.Contains(option, "n")
		}
	}

	if !attributes()[variant] && !options {
		names = nil
	}

	return names, refers
}

// attributes are the declaration words that give every name they name an attribute with no
// option: `readonly`, and zsh's `integer` and `float`, which turn the value an assignment leaves
// into a number.
func attributes() map[string]bool {
	return setOf("readonly", "integer", "float")
}

// callDeclaration returns call, a simple command, as the declaration it runs, and reports whether
// it runs one: a declaration word the parser reads as one (`declare`, `local`, `export`,
// `readonly`, `typeset`) run through `builtin` or `command`, with any options of `command`, or
// zsh's `integer` or `float`, which the parser reads as a simple command. A word that is not a
// literal is an option, which may be `-n`, and a literal word that is no option names the variable
// before its `=` or `[`.
func callDeclaration(call *syntax.CallExpr) (*syntax.DeclClause, bool) {
	words, wrapped := call.Args, false
	for len(words) > 0 && (words[0].Lit() == "builtin" || words[0].Lit() == "command") {
		words, wrapped = words[1:], true
		for len(words) > 0 && strings.HasPrefix(words[0].Lit(), "-") {
			words = words[1:]
		}
	}

	if len(words) == 0 {
		return nil, false
	}

	variant := words[0].Lit()
	parsed := setOf("declare", "local", "export", "readonly", "typeset")
	if (!wrapped || !parsed[variant]) && variant != "integer" && variant != "float" {
		return nil, false
	}

	decl := &syntax.DeclClause{Variant: &syntax.Lit{Value: variant}}
	for _, w := range words[1:] {
		text := w.Lit()
		if text == "" || strings.ContainsAny(text[:1], "-+") {
			decl.Args = append(decl.Args, &syntax.Assign{Naked: true, Value: w})

			continue
		}

		name, _, _ := strings.Cut(text, "=")
		name, _, _ = strings.Cut(name, "[")
		decl.Args = append(decl.Args, &syntax.Assign{Naked: !strings.Contains(text, "="), Name: &syntax.Lit{Value: name}})
	}

	return decl, true
}

// expansion returns the value of the variable p expands, and reports whether v knows it: p must
// be a plain `$NAME` or `${NAME}`, with no operator, index, or flag.
func (v shellVars) expansion(p *syntax.ParamExp) (string, bool) {
	plain := p.Param != nil && p.Flags == nil && !p.Excl && !p.Length && !p.Width && !p.IsSet &&
		p.Split == syntax.OptUnset && p.GlobSubst == syntax.OptUnset && p.RcExpand == syntax.OptUnset &&
		p.NestedParam == nil && p.Index == nil && len(p.Modifiers) == 0 && p.Slice == nil &&
		p.Repl == nil && p.Names == 0 && p.Exp == nil
	if !plain || v.unread {
		return "", false
	}

	value, known := v.known[p.Param.Value]

	return value, known
}

// assigned returns v once the assignment a, whose value reads as value, has run: the variable
// holds value when it is a literal assigned whole, and is unknown after an append, an element or
// array assignment, a value that is not a literal, a held variable, or a variable that holding
// refuses.
func (v shellVars) assigned(a *syntax.Assign, value Word) shellVars {
	if a.Name == nil {
		return v.forgetAll()
	}

	name := a.Name.Value
	if a.Append || a.Index != nil || a.Array != nil || !value.Literal || v.held[name] > 0 {
		return v.forget(name)
	}

	return v.holding(name, value.Text)
}

// declared returns v once the declaration decl, whose values ex reads, has run. A variable it
// gives a literal value with no option holds that value under `export`, `declare`, or `typeset`;
// under `local` only inside a function body; and under `readonly` only when no other declaration
// holds it (see shellVars.held). Every other variable it names is unknown.
func (v shellVars) declared(decl *syntax.DeclClause, ex expander) shellVars {
	variant := decl.Variant.Value
	options := slices.ContainsFunc(decl.Args, func(a *syntax.Assign) bool { return a.Name == nil })
	for _, a := range decl.Args {
		if a.Name == nil {
			continue
		}

		name := a.Name.Value
		value := assignmentOf(a, ex).Value
		switch {
		case options, a.Naked, variant == "local" && !v.function, variant == "readonly" && v.held[name] > 1:
			v = v.forget(name)
		case variant == "readonly" && !a.Append && a.Index == nil && a.Array == nil && value.Literal:
			v = v.holding(name, value.Text)
		default:
			v = v.assigned(a, value)
		}
	}

	return v
}

// holding returns v with name holding value, or unknown when v is unread or the shell sets name on
// its own.
func (v shellVars) holding(name, value string) shellVars {
	if v.unread || selfSetVariables()[name] {
		return v.forget(name)
	}

	known := maps.Clone(v.known)
	if known == nil {
		known = map[string]string{}
	}

	known[name] = value
	v.known = known

	return v
}

// forget returns v with each of names unknown. Once IFS is among them, which changes where every
// later expansion splits, no variable is read.
func (v shellVars) forget(names ...string) shellVars {
	if slices.Contains(names, "IFS") {
		v.unread = true
	}

	if !slices.ContainsFunc(names, func(name string) bool { _, known := v.known[name]; return known }) {
		return v
	}

	known := maps.Clone(v.known)
	for _, name := range names {
		delete(known, name)
	}

	v.known = known

	return v
}

// forgetAll returns v with every variable unknown.
func (v shellVars) forgetAll() shellVars {
	v.known = nil

	return v
}

// forgetAssigned returns v with each variable among assigns unknown: the values of one command's
// assignments are read so, since the shell makes them in turn, each after the one before.
func (v shellVars) forgetAssigned(assigns []*syntax.Assign) shellVars {
	for _, a := range assigns {
		if a.Name != nil {
			v = v.forget(a.Name.Value)
		}
	}

	return v
}

// merge returns the variables of v and other, two scopes either of which the shell may be in. It
// keeps what both know, holds what either holds, as many times as the one holding it more, is
// unread if either is, and keeps v's function.
func (v shellVars) merge(other shellVars) shellVars {
	if v.equal(other) {
		return v
	}

	known := map[string]string{}
	for name, value := range v.known {
		if theirs, ok := other.known[name]; ok && theirs == value {
			known[name] = value
		}
	}

	held := maps.Clone(v.held)
	if len(other.held) > 0 && held == nil {
		held = map[string]int{}
	}

	for name, count := range other.held {
		held[name] = max(held[name], count)
	}

	return shellVars{known: known, held: held, unread: v.unread || other.unread, function: v.function}
}

func (v shellVars) equal(other shellVars) bool {
	return maps.Equal(v.known, other.known) && maps.Equal(v.held, other.held) &&
		v.unread == other.unread && v.function == other.function
}

// assignVariables changes the shell variables of sc the way the simple command call, read as inv,
// does with its assignment prefix. A command that only assigns leaves each variable as
// shellVars.assigned says. One that runs a command gives them to that command alone, though a
// special builtin or a function may leave them in the shell, so each is unknown after it.
func (sc *scope) assignVariables(call *syntax.CallExpr, inv Invocation) {
	if len(inv.Words) > 0 {
		sc.vars = sc.vars.forgetAssigned(call.Assigns)

		return
	}

	for i, a := range call.Assigns {
		sc.vars = sc.vars.assigned(a, inv.Assignments[i].Value)
	}
}

// arithmeticNames returns the variables an arithmetic expression in node assigns, as in `((n =
// 1))`, `let n++`, or `$((n += 2))`. A command or process substitution, and a statement other than
// node, run in a shell of their own or are walked apart, so what they assign is left out.
func arithmeticNames(node syntax.Node) []string {
	var names []string
	syntax.Walk(node, func(n syntax.Node) bool {
		var target syntax.ArithmExpr
		switch n := n.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			return false
		case *syntax.Stmt:
			return syntax.Node(n) == node
		case *syntax.BinaryArithm:
			if assignsArithmetic(n.Op) {
				target = n.X
			}
		case *syntax.UnaryArithm:
			if n.Op == syntax.Inc || n.Op == syntax.Dec {
				target = n.X
			}
		}

		if w, ok := target.(*syntax.Word); ok {
			name, _, _ := strings.Cut(w.Lit(), "[")
			names = append(names, name)
		}

		return true
	})

	return names
}

func assignsArithmetic(op syntax.BinAritOperator) bool {
	switch op {
	case syntax.Assgn, syntax.AddAssgn, syntax.SubAssgn, syntax.MulAssgn, syntax.QuoAssgn,
		syntax.RemAssgn, syntax.AndAssgn, syntax.OrAssgn, syntax.XorAssgn, syntax.ShlAssgn,
		syntax.ShrAssgn, syntax.AndBoolAssgn, syntax.OrBoolAssgn, syntax.XorBoolAssgn, syntax.PowAssgn:
		return true
	default:
		return false
	}
}
