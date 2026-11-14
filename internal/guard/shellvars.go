package guard

import (
	"maps"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// shellVars is what the guard knows of the shell's own variables, whose value an expansion in a
// word stands for. A variable it does not know is one the command never gave a literal, or one the
// guard cannot be sure still holds it: another kind of write may have changed it (`+=`, `read`,
// `printf -v`, `for NAME in`, `${NAME:=…}`, `unset`), or the assignment may not have run, or ran in
// another shell.
type shellVars struct {
	// Copies of a scope share known, so a change replaces it rather than changing it in place.
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
// sets on its own (see selfSetVariables), in the forms argumentsWriteNoVariable lets through.
// `eval` and `source` are among them because the walk applies what their strings and files do
// where it reads them.
func variableFreeBuiltins() map[string]bool {
	return setOf(
		":", "true", "false", "echo", "printf", "test", "[", "cd", "pwd", "pushd", "popd", "dirs", "exit",
		"return", "break", "continue", "shift", "eval", "source", ".", "alias", "unalias", "hash", "type",
		"umask", "ulimit", "kill", "jobs", "which", "whence", "where", "set", "shopt", "wait", "trap",
	)
}

// argumentsWriteNoVariable reports whether args, the arguments of name, a builtin among
// variableFreeBuiltins, keep it from writing a variable or running code before later commands:
//
//   - `printf` writes one when its first argument is `-v` or is not a literal;
//   - zsh's `set -A` and `set +A` write an array;
//   - `wait -p` writes the id of the job it waited for;
//   - a `trap` runs its string around later commands unless every signal it names is one of
//     trapSignalsAfterAll (see trapsOnlyAtEnd).
//
// An argument of `set`, `shopt`, `wait` or `trap` that is not a literal may be any of these forms.
func argumentsWriteNoVariable(name string, args []Word) bool {
	var writes func(arg string) bool
	switch name {
	case "printf":
		return len(args) == 0 || args[0].Literal && !strings.HasPrefix(args[0].Text, "-v")
	case "set":
		writes = func(arg string) bool {
			return startsWithSign(arg) && strings.Contains(arg, "A")
		}
	case "wait":
		writes = func(arg string) bool { return strings.HasPrefix(arg, "-") && strings.Contains(arg, "p") }
	case "trap":
		return !slices.ContainsFunc(args, func(w Word) bool { return !w.Literal }) && trapsOnlyAtEnd(wordTexts(args))
	case "shopt":
		writes = func(string) bool { return false }
	default:
		return true
	}

	return !slices.ContainsFunc(args, func(w Word) bool { return !w.Literal || writes(w.Text) })
}

// trapSignalsAfterAll are the signals whose trap runs only when the shell exits or is stopped,
// after every later command of the command line: an exit, and the signals that stop it from the
// outside. A trap on any other signal, such as DEBUG, ERR or CHLD, or on a signal given by number,
// can run between later commands.
func trapSignalsAfterAll() []string {
	return []string{"EXIT", "0", "INT", "TERM", "HUP"}
}

// trapsOnlyAtEnd reports whether `trap` with the literal arguments args sets or resets traps only
// on trapSignalsAfterAll, in any letter case and with or without a `SIG` prefix. A `-p`, `-l` or
// `-P` option only lists traps. With one argument, that argument is the signal to reset; with
// more, the first is the action and the rest are signals.
func trapsOnlyAtEnd(args []string) bool {
	if len(args) == 0 || slices.Contains([]string{"-p", "-l", "-P"}, args[0]) {
		return true
	}

	signals := args
	if len(args) > 1 {
		signals = args[1:]
	}

	return !slices.ContainsFunc(signals, func(signal string) bool {
		name := signal
		if len(name) > len("SIG") && strings.EqualFold(name[:len("SIG")], "SIG") {
			name = name[len("SIG"):]
		}

		return !containsFold(trapSignalsAfterAll(), name)
	})
}

// cspell:enable

// ran returns v once inv, a simple command with a command word, has run in the shell. A program
// changes no variable of the shell, and a function call is applied by the walk of its body; a
// builtin that is not among variableFreeBuiltins or is in a form argumentsWriteNoVariable refuses,
// or a command word that is not a literal, may write any variable, so every one is unknown after
// it. When one of its words is not a literal or names IFS, it may also have written IFS, which
// changes where every later expansion splits, so no variable is read after it.
func (v shellVars) ran(inv Invocation, functions map[string][]*definition) shellVars {
	word := inv.Words[0]
	if word.Literal && (functions[word.Text] != nil || !shellBuiltins()[word.Text]) {
		return v
	}

	if word.Literal && variableFreeBuiltins()[word.Text] && argumentsWriteNoVariable(word.Text, inv.Words[1:]) {
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
// option names is held. A declaration run through `builtin` or `command` counts only for the name
// reference it may declare (see callDeclaration).
func (v shellVars) watched(file *syntax.File) shellVars {
	if assigns(file, "IFS") {
		v.unread = true
	}

	held := cloneOrEmpty(v.held)
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
			held[name]++
		}

		return true
	})
	v.held = held

	return v
}

// declarationHolds returns the variables decl makes readonly or gives an option, and reports
// whether it may declare a name reference: a `declare`, `typeset`, or `local` given `-n`, or an
// option the guard cannot read.
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
			refers = refers || !startsWithSign(option) || strings.Contains(option, "n")
		}
	}

	if variant != "readonly" && !options {
		names = nil
	}

	return names, refers
}

// callDeclaration returns the options of the declaration call, a simple command, runs, and reports
// whether it runs one: a declaration word the parser reads as one (`declare`, `local`, `export`,
// `readonly`, `typeset`) run through `builtin` or `command`, with any options of `command`. A word
// that is not a literal is an option, which may be `-n`.
//
// The names the declaration gives an attribute are left out, so none is held: an agent does not
// run a declaration through `builtin` or `command`, or zsh's `integer` and `float`, to change what
// a later assignment leaves, and the guard reads that assignment as written.
func callDeclaration(call *syntax.CallExpr) (*syntax.DeclClause, bool) {
	words, wrapped := call.Args, false
	for len(words) > 0 && (words[0].Lit() == "builtin" || words[0].Lit() == "command") {
		words, wrapped = words[1:], true
		for len(words) > 0 && strings.HasPrefix(words[0].Lit(), "-") {
			words = words[1:]
		}
	}

	if !wrapped || len(words) == 0 {
		return nil, false
	}

	variant := words[0].Lit()
	if !setOf("declare", "local", "export", "readonly", "typeset")[variant] {
		return nil, false
	}

	decl := &syntax.DeclClause{Variant: &syntax.Lit{Value: variant}}
	for _, w := range words[1:] {
		if text := w.Lit(); text == "" || startsWithSign(text) {
			decl.Args = append(decl.Args, &syntax.Assign{Naked: true, Value: w})
		}
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

	known := cloneOrEmpty(v.known)
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

// forgetAll returns v with every variable unknown. It leaves held, unread and function as they
// were.
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
	if maps.Equal(v.known, other.known) && maps.Equal(v.held, other.held) &&
		v.unread == other.unread && v.function == other.function {
		return v
	}

	known := map[string]string{}
	for name, value := range v.known {
		if theirs, ok := other.known[name]; ok && theirs == value {
			known[name] = value
		}
	}

	held := cloneOrEmpty(v.held)

	for name, count := range other.held {
		held[name] = max(held[name], count)
	}

	return shellVars{known: known, held: held, unread: v.unread || other.unread, function: v.function}
}
