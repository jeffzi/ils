//go:build unix

package guard

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"mvdan.cc/sh/v3/syntax"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/landing"
)

// Tool is the program a command word names, when it is one the guard judges.
type Tool string

// The tools a command word can name; NoTool is any other program.
const (
	NoTool Tool = ""
	Git    Tool = "git"
	GH     Tool = "gh"
	ILS    Tool = "ils"
)

// Caller is what Parse needs to know about the shell that would run the command.
type Caller struct {
	// Env is the environment the command would start with; Parse reads CDPATH, GIT_DIR,
	// GIT_WORK_TREE, GH_CONFIG_DIR, XDG_CONFIG_HOME, and HOME from it.
	Env map[string]string
	// Home is the HOME that `~` and `~/…` expand with.
	Home string
	// Dir is the absolute directory the command starts in; empty when unknown.
	Dir string
	// Git runs the git calls that read where each git invocation points git and what its aliases
	// expand to. Parse runs each call from the invocation's directory, with the GIT_DIR and
	// GIT_WORK_TREE it modeled for the invocation in place of those of Git's environment.
	Git git.Runner
	// GH runs the `gh alias list` calls that read what a gh alias expands to, with the
	// GH_CONFIG_DIR, XDG_CONFIG_HOME and HOME modeled for the invocation in place of those of GH's
	// environment.
	GH github.Runner
}

// Invocation is one simple command of a parsed command string.
type Invocation struct {
	// Tool is the program the command word names, read from its basename once a leading `=` is
	// removed; NoTool when the command word is not a literal.
	Tool Tool
	// Words are the command word and its arguments, behind any wrapper; none for a command that
	// only assigns.
	Words     []Word
	Redirects []Redirect
	// Assignments are the variables the command runs with: its assignment prefix, then the `VAR=…`
	// arguments of an `env` wrapper.
	Assignments []Assignment
	// Wrappers are the wrapper words the command runs behind, such as `env` or `nice`, outermost
	// first.
	Wrappers []string
	Marks    []Mark
	// Dir is the directory the invocation runs in; empty when the guard cannot follow it.
	Dir string
	// env is every change to the environment the command runs with, in the order bash makes them.
	env []envChange
	// enclosing are the redirects in effect on the shell's descriptors where the command runs:
	// those of each compound command around it, and those of an earlier `exec` with no command.
	enclosing []Redirect
	// ghConfig is what the guard knows of where a gh invocation's gh reads its config; zero for
	// any other tool.
	ghConfig ghConfig
	// Target is where a git invocation points git; zero for any other tool.
	Target GitTarget
	// Git is how git reads the words of a git invocation; zero for any other tool.
	Git GitCall
	// depth is how many levels of nested strings hold the invocation; 0 for one the command holds
	// itself.
	depth int
	// Bare reports whether the command word is written as a plain unquoted name, with no path, no
	// quoting or escape, and no leading `=`.
	Bare bool
	// Whole reports whether this invocation is the entire command: one simple command with a bare
	// command word, and no pipe, list, redirect, background `&`, assignment prefix, or wrapper.
	Whole bool
	// HeadUnknown reports whether an invocation before it may have moved HEAD.
	HeadUnknown bool
}

// Assignment is one variable a command runs with.
type Assignment struct {
	Name  string
	Value Word
}

// Mark is something Parse found in an invocation that it could not read, or that the guard does
// not allow.
type Mark struct {
	Kind MarkKind
	// Name is what the mark found, such as the variable of a GitVariable mark; empty for a kind
	// that names nothing.
	Name string
}

// MarkKind is the kind of a Mark.
type MarkKind string

const (
	// ExtendedGlob is an extended glob such as `@(a|b)`, or zsh's `*(e:…:)` qualifier, which runs
	// code.
	ExtendedGlob MarkKind = "extended glob"
	// DynamicCommand is a command word that is not a literal, so the program it runs is unknown.
	DynamicCommand MarkKind = "command word that is not a literal"
	// ToolArgument is a git, gh, ils, or shell word among the arguments of a command that may run
	// it.
	ToolArgument MarkKind = "git, gh, ils, or shell word handed to another command"
	// DynamicScript is a string a shell, `eval`, or git runs that is not a literal.
	DynamicScript MarkKind = "shell string that is not a literal"
	// DeepScript is a shell string nested past the depth Parse follows.
	DeepScript MarkKind = "shell string nested too deep"
	// UnreadCommand is a string a command runs as a shell command, such as the value of go's
	// `-exec`, that does not parse.
	UnreadCommand MarkKind = "command string that does not parse"
	// HiddenScript is a shell that runs a script the command does not hold: standard input, a
	// file, or a process substitution.
	HiddenScript MarkKind = "shell running a script the command does not hold"
	// GitVariable is an assignment or export of a GIT_* variable off the guard's allow list.
	GitVariable MarkKind = "git variable off the allow list"
	// RedirectedFunction is a call of a function the command defines that runs with a redirect its
	// declaration does not: the commands of its body are read with the redirects of the
	// declaration.
	RedirectedFunction MarkKind = "call of a function the command defines, with a redirect"
	// ShadowingFunction is a function the command defines under the name of a command the guard
	// reads, such as `cd` or a wrapper, which every later call of that name runs instead.
	ShadowingFunction MarkKind = "function defined over a command the guard reads"
	// ToolAlias is an alias whose value may run git, gh, or ils, or that the guard cannot read.
	ToolAlias MarkKind = "alias that may run git, gh, or ils"
)

// maxDepth is how many levels of strings run by `bash -c`, `eval`, or a heredoc fed to a shell
// Parse follows.
const maxDepth = 3

// ParseError is a command string the bash parser cannot read.
type ParseError struct {
	Message string
	Line    uint
	Column  uint
}

// Error returns the position of the failure and its message.
func (e *ParseError) Error() string {
	return fmt.Sprintf("%d:%d: %s", e.Line, e.Column, e.Message)
}

// Parse reads command with bash's grammar and returns one invocation for every simple command in
// its parse tree, in source order, whether or not it would run: a function body, an `if` branch,
// and a command substitution inside an argument each give their own. A string run by `bash -c`
// or `eval`, and a literal heredoc fed to a shell, are parsed in turn to a depth of maxDepth, their
// invocations following the one that runs them. What Parse cannot read is marked on the invocation
// it occurs in. It returns a *ParseError when the parser cannot read command or a string it runs.
//
// For each git invocation, Parse asks git through caller.Git which git dir it targets and what the
// aliases it names expand to; a command with no git invocation starts no git. A failed call is
// returned as an error naming it, never read as an answer.
func Parse(ctx context.Context, command string, caller Caller) ([]Invocation, error) {
	file, err := read(command)
	if err != nil {
		return nil, err
	}

	w := walker{caller: caller, timed: map[*syntax.Stmt]bool{}}
	sc := startScope(caller, file)
	w.list(file.Stmts, &sc)
	if w.err != nil {
		return nil, w.err
	}

	if err := w.resolveGit(ctx); err != nil {
		return nil, err
	}

	invocations := w.returned()
	if isWhole(file, invocations) {
		invocations[0].Whole = true
	}

	return invocations, nil
}

// isWhole reports whether the one invocation of file is the entire command: a single simple
// statement with a bare command word and no wrapper.
func isWhole(file *syntax.File, invocations []Invocation) bool {
	return len(invocations) == 1 && len(file.Stmts) == 1 && alone(file.Stmts[0]) &&
		invocations[0].Bare && len(invocations[0].Wrappers) == 0
}

// read parses command with bash's grammar. It returns a *ParseError when the parser cannot read it.
func read(command string) (*syntax.File, error) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err == nil {
		return file, nil
	}

	if failure, ok := errors.AsType[syntax.ParseError](err); ok {
		return nil, &ParseError{Line: failure.Pos.Line(), Column: failure.Pos.Col(), Message: failure.Text}
	}

	if failure, ok := errors.AsType[syntax.LangError](err); ok {
		// The parser is given no file name, so its message opens with the position alone.
		message := strings.TrimPrefix(failure.Error(), failure.Pos.String()+": ")

		return nil, &ParseError{Line: failure.Pos.Line(), Column: failure.Pos.Col(), Message: message}
	}

	return nil, fmt.Errorf("parse %q: %w", command, err)
}

// walker collects the invocations of one parse tree and of the strings it runs, depth levels of
// nested strings down.
type walker struct {
	caller Caller
	// timed holds the statements a `time` keyword runs.
	timed     map[*syntax.Stmt]bool
	functions map[string]*function
	err       error
	// walked holds every invocation the walk placed, in the order it walked them, those of dry runs
	// included.
	walked []Invocation
	// kept holds the index in walked of each invocation Parse returns, in the order it returns them.
	kept  []int
	depth int
	// probing reports whether the walk is a dry run, whose invocations are dropped once it ends: of
	// a body that may repeat, of a background job or pipe stage, or of a function body or an alias
	// string.
	probing bool
}

// simple walks stmt when it is a simple command, recording its invocation and then those of the
// substitutions in it, and reports false when it is any other command.
func (w *walker) simple(stmt *syntax.Stmt, sc *scope) bool {
	switch stmt.Cmd.(type) {
	case *syntax.CallExpr, *syntax.DeclClause, *syntax.LetClause:
	default:
		return false
	}

	// Each substitution runs before the command, in a subshell of the shell as it is then, so a
	// HEAD move in one comes before the command; their invocations still follow its own. Their
	// indices stay in w.kept while the command is walked, since a race the command reaches shifts
	// every index there; they move behind the command's own once it is walked.
	at := len(w.kept)
	w.substitutions(stmt, sc)
	held := len(w.kept) - at
	switch cmd := stmt.Cmd.(type) {
	case *syntax.CallExpr:
		w.add(stmt, cmd, sc)
	case *syntax.DeclClause:
		inv := w.placed(declarationOf(stmt, cmd, w.caller.Home), sc)
		scripts := w.variableScripts(&inv, declared(cmd, w.caller.Home))
		w.keep(inv)
		sc.declare(cmd, w.caller.Home)
		w.variableCommands(scripts, laterScope())
	case *syntax.LetClause:
		// Parse does not read an arithmetic expression back into text, so each one is a word that is
		// not literal.
		words := append([]Word{{Text: "let", Literal: true}}, make([]Word, len(cmd.Exprs))...)
		inv := Invocation{Words: words, Redirects: redirectsOf(stmt, w.caller.Home), Bare: true}
		w.keep(w.placed(inv, sc))
	}

	w.kept = slices.Concat(w.kept[:at], w.kept[at+held:], w.kept[at:at+held])

	return true
}

// declarationOf returns the invocation of a declaration builtin such as `export` or `declare`: its
// name, then each of its arguments as a word.
func declarationOf(stmt *syntax.Stmt, decl *syntax.DeclClause, home string) Invocation {
	words := []Word{{Text: decl.Variant.Value, Literal: true}}
	values := make([]*syntax.Word, 0, len(decl.Args))
	var names []string
	for _, a := range decl.Args {
		words = append(words, declarationWord(a, home))
		values = append(values, a.Value)
		if a.Name != nil {
			names = append(names, a.Name.Value)
		}
	}

	inv := Invocation{Words: words, Redirects: redirectsOf(stmt, home), Bare: true}
	if hasExtGlob(values, stmt) {
		inv.Marks = []Mark{{Kind: ExtendedGlob}}
	}

	inv.Marks = append(inv.Marks, gitVariableMarks(names)...)

	return inv
}

// declarationWord returns the argument a of a declaration builtin as the word the shell reads:
// an option or a name alone, or `NAME=value`.
func declarationWord(a *syntax.Assign, home string) Word {
	if a.Naked {
		if a.Name != nil {
			return Word{Text: a.Name.Value, Literal: true}
		}

		return readWord(a.Value, home)
	}

	prefix := a.Name.Value
	switch {
	case a.Index != nil:
		return Word{Text: prefix + "["}
	case a.Append:
		prefix += "+="
	default:
		prefix += "="
	}

	if a.Array != nil {
		return Word{Text: prefix}
	}

	value := Word{Literal: true}
	if a.Value != nil {
		value = readWord(a.Value, home)
	}

	return Word{Text: prefix + value.Text, Literal: value.Literal}
}

// add appends the invocation of call, then the invocations of the string it runs, and applies to
// sc what it changes in the shell.
func (w *walker) add(stmt *syntax.Stmt, call *syntax.CallExpr, sc *scope) {
	inv := invocationOf(stmt, call, w.caller.Home)
	if w.timed[stmt] {
		inv.Wrappers = slices.Insert(inv.Wrappers, 0, "time")
	}

	inv = w.placed(inv, sc)
	inv.depth = w.depth
	script, runs, mark := scriptOf(stmt, inv.Words)
	runs, mark = capDepth(w.depth, runs, mark)

	inv.Marks = append(w.aliasMarks(inv), marksOf(call, stmt, inv, w.caller.Home)...)
	inv.Marks = append(inv.Marks, w.callMarks(inv)...)
	if mark != "" {
		inv.Marks = append(inv.Marks, Mark{Kind: mark})
	}

	commands := goCommands(&inv, w.depth)

	names := make([]string, 0, len(inv.Assignments))
	for _, a := range inv.Assignments {
		names = append(names, a.Name)
	}

	if sc.allexport {
		names = append(names, assignedNames(stmt, inv.Words)...)
	}

	inv.Marks = append(inv.Marks, gitVariableMarks(names)...)
	scripts := w.variableScripts(&inv, inv.Assignments)
	w.keep(inv)
	if len(inv.Words) == 0 {
		// A bare assignment reaches the git of a later command once the variable is exported, as it
		// may already be.
		w.variableCommands(scripts, laterScope())
	} else {
		w.variableCommands(scripts, sc.runScope(inv))
	}

	if runs && w.err == nil {
		w.script(script, inv, sc)
	}

	// go starts each command in a process of its own, so nothing it changes holds after it.
	for _, file := range commands {
		inner := sc.runScope(inv)
		w.nested(file, &inner)
	}

	sc.apply(inv)
	w.call(inv, sc)
}

// aliasMarks marks each alias the `alias` invocation inv defines whose value may run git, gh, or
// ils, or is a string the guard cannot read. A later command that names the alias runs its value
// from wherever it stands.
func (w *walker) aliasMarks(inv Invocation) []Mark {
	if len(inv.Words) == 0 || !inv.Words[0].Literal || inv.Words[0].Text != "alias" {
		return nil
	}

	var marks []Mark
	for _, word := range inv.Words[1:] {
		name, value, defines := strings.Cut(word.Text, "=")
		if word.Literal && (!defines || !w.runsTool(value)) {
			continue
		}

		marks = append(marks, Mark{Kind: ToolAlias, Name: name})
	}

	return marks
}

// runsTool reports whether the shell string script may run git, gh, or ils: when it holds an
// invocation of one, or one the guard marks, or when the guard cannot read it.
func (w *walker) runsTool(script string) bool {
	file, err := read(script)
	if err != nil || w.depth == maxDepth {
		return true
	}

	sub := w.probe()
	sc := laterScope()
	sub.nested(file, &sc)

	return sub.err != nil || slices.ContainsFunc(sub.walked, func(inv Invocation) bool {
		return inv.Tool != NoTool || len(inv.Marks) > 0
	})
}

// assignedNames returns the variables the simple command stmt, whose words are words, assigns
// other than by an assignment: those `read`, `mapfile`, `readarray`, and `getopts` are given, the
// one `printf -v` is given, and each one a `${NAME:=word}` or `${NAME=word}` in it assigns.
func assignedNames(stmt *syntax.Stmt, words []Word) []string {
	var names []string
	if len(words) > 0 && words[0].Literal {
		switch words[0].Text {
		case "read", "mapfile", "readarray", "getopts":
			for _, word := range words[1:] {
				names = append(names, word.Text)
			}
		case "printf":
			names = printfTarget(words[1:])
		}
	}

	syntax.Walk(stmt, func(n syntax.Node) bool {
		p, ok := n.(*syntax.ParamExp)
		if ok && p.Param != nil && p.Exp != nil && (p.Exp.Op == syntax.AssignUnset || p.Exp.Op == syntax.AssignUnsetOrNull) {
			names = append(names, p.Param.Value)
		}

		return true
	})

	return names
}

// printfTarget returns the variable each `-v` among args, the arguments of `printf`, assigns.
func printfTarget(args []Word) []string {
	var names []string
	for i, arg := range args {
		value, ok := strings.CutPrefix(arg.Text, "-v")
		if !ok {
			continue
		}

		if value == "" && i+1 < len(args) {
			value = args[i+1].Text
		}

		names = append(names, value)
	}

	return names
}

// alone reports whether stmt is a simple command with no assignment prefix, redirect, or
// background operator.
func alone(stmt *syntax.Stmt) bool {
	call, ok := stmt.Cmd.(*syntax.CallExpr)

	return ok && len(call.Args) > 0 && len(call.Assigns) == 0 && len(stmt.Redirs) == 0 &&
		!stmt.Background && !stmt.Coprocess && !stmt.Disown
}

// invocationOf returns the invocation of call. A call that only assigns has no words.
func invocationOf(stmt *syntax.Stmt, call *syntax.CallExpr, home string) Invocation {
	words := make([]Word, 0, len(call.Args))
	for _, arg := range call.Args {
		words = append(words, readWord(arg, home))
	}

	assignments := make([]Assignment, 0, len(call.Assigns))
	for _, a := range call.Assigns {
		assignments = append(assignments, assignmentOf(a, home))
	}

	if len(words) == 0 {
		return Invocation{Redirects: redirectsOf(stmt, home), Assignments: assignments}
	}

	prefix := make([]envChange, 0, len(assignments))
	for _, a := range assignments {
		prefix = append(prefix, envChange{assignment: a})
	}

	start, inv := seeThrough(call.Args, words, home)
	inv.Words = words[start:]
	inv.Redirects = redirectsOf(stmt, home)
	inv.Assignments = append(assignments, inv.Assignments...)
	inv.env = append(prefix, inv.env...)
	inv.Tool = toolOf(words[start])
	inv.Bare = isBare(call.Args[start], words[start])

	return inv
}

func assignmentOf(a *syntax.Assign, home string) Assignment {
	name := ""
	if a.Name != nil {
		name = a.Name.Value
	}

	switch {
	case a.Array != nil:
		return Assignment{Name: name}
	case a.Value == nil:
		return Assignment{Name: name, Value: Word{Literal: true}}
	default:
		return Assignment{Name: name, Value: readWord(a.Value, home)}
	}
}

// isLookup reports whether words open with `command -v` or `command -V`, which look a name up
// instead of running it.
func isLookup(words []Word) bool {
	return len(words) > 1 && words[0].Text == "command" && words[1].Literal &&
		(words[1].Text == "-v" || words[1].Text == "-V")
}

// dataConsumers is the table of command words that take their arguments as data, never as a
// command to run. `command -v` and `command -V` are the lookups isLookup adds.
func dataConsumers() map[string]bool {
	return setOf(
		// Shell built-ins and file tools.
		"cd", "pushd", "popd", "test", "[", "mkdir", "cp", "mv", "rm", "ln", "touch", "chmod", "tee",
		"file", "stat", "du", "realpath", "readlink", "dirname", "basename",
		// Readers and filters.
		"echo", "printf", "cat", "head", "tail", "less", "grep", "egrep", "fgrep", "rg", "ls", "wc",
		"sort", "uniq", "cut", "tr", "jq", "diff", "man",
		// Lookups.
		"which", "type", "whence", "where",
		// Toolchains and package managers.
		"go", "brew", "apt", "apt-get", "dnf", "yum", "pacman", "apk", "port",
	)
}

// setOf returns the set that holds names.
func setOf(names ...string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}

	return set
}

// isDataConsumer reports whether words are the invocation of a data consumer. `go run` and `go
// generate` are not one, past any global option of go (`go -C dir run`): they start a program the
// command does not hold, the one go run builds or those go generate finds in source files, with
// their arguments. Nor is a go command whose subcommand word is not a literal, which covers a
// non-literal word where go reads `-C`: either may be `run`, `generate`, or `-C=dir`. The commands
// that the flags in goCommandFlags hand to go are read by goCommands.
func isDataConsumer(words []Word) bool {
	if isLookup(words) {
		return true
	}

	if words[0].Text == "go" {
		if sub, ok := goSubcommand(words[1:]); ok && (!sub.Literal || sub.Text == "run" || sub.Text == "generate") {
			return false
		}
	}

	return dataConsumers()[words[0].Text]
}

// goSubcommand returns the subcommand word among args, the words after a go command word, past
// `-C dir`, the one global option go takes, which it reads only as its first argument. A first
// argument that is not a literal is returned as the subcommand word. It reports whether there is a
// subcommand word.
func goSubcommand(args []Word) (Word, bool) {
	if len(args) > 0 && args[0].Literal {
		flag, _, joined := strings.Cut(args[0].Text, "=")
		switch {
		case (flag == "-C" || flag == "--C") && joined:
			args = args[1:]
		case flag == "-C" || flag == "--C":
			args = args[min(2, len(args)):]
		}
	}

	if len(args) == 0 {
		return Word{}, false
	}

	return args[0], true
}

// marksOf returns what Parse cannot read in the invocation inv of call, apart from the string it
// runs. A `~` in a word expands to home.
func marksOf(call *syntax.CallExpr, stmt *syntax.Stmt, inv Invocation, home string) []Mark {
	words := slices.Clone(call.Args)
	for _, a := range call.Assigns {
		words = append(words, a.Value)
	}

	var marks []Mark
	if hasExtGlob(words, stmt) {
		marks = append(marks, Mark{Kind: ExtendedGlob})
	}

	if len(inv.Words) == 0 {
		return marks
	}

	command := inv.Words[0]
	switch {
	case !command.Literal:
		marks = append(marks, Mark{Kind: DynamicCommand})
	case inv.Tool != NoTool, isDataConsumer(inv.Words), isShell(command), command.Text == "eval":
	case command.Text == "source" || command.Text == ".":
		if len(inv.Words) > 1 {
			marks = append(marks, Mark{Kind: HiddenScript})
		}
	default:
		if name, ok := handedByCall(call, inv.Words, inv.Dir, home); ok {
			marks = append(marks, Mark{Kind: ToolArgument, Name: name})
		}
	}

	return marks
}

// handedByCall returns the tool that the command call, whose words after any wrapper are words, may
// run from its arguments, as handedTool reads them, and reports whether there is one.
func handedByCall(call *syntax.CallExpr, words []Word, dir, home string) (string, bool) {
	args := call.Args[len(call.Args)-len(words):]

	return handedTool(args[1:], words[1:], dir, home)
}

// handedTool returns the tool that a command the guard does not know may run from its arguments,
// raw as the parser read them and words after quote removal, and reports whether there is one.
// An argument hands over a tool when it is a tool word itself, or when it reads as a shell script
// one of whose commands is a tool word or hands one over in turn: `su -c 'cd /repo && git push'`,
// `ssh host 'sudo git push'`. An argument that does not parse hands over the git, gh, or ils word
// it holds, even when a data consumer word stands ahead of it: the guard does not know how the
// command runs its words, and `ssh host echo 'fi; git push'` runs them as one shell line. An ils
// word followed by an ils command that changes nothing hands over nothing. dir is where a relative
// path is read against, and a `~` expands to home.
func handedTool(args []*syntax.Word, words []Word, dir, home string) (string, bool) {
	for i, w := range words {
		if isToolWord(w, dir) {
			if toolOf(w) == ILS && changesNothing(words[i+1:]) {
				continue
			}

			return w.Text, true
		}

		if name, ok := scriptTool(scriptText(args[i], home), home); ok {
			return name, true
		}
	}

	return "", false
}

// toolWordIn returns the first git, gh, or ils word in text, split at each character a path does
// not hold, and reports whether there is one.
func toolWordIn(text string) (string, bool) {
	for field := range strings.FieldsFuncSeq(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("_-./", r)
	}) {
		if toolOf(Word{Text: field, Literal: true}) != NoTool {
			return field, true
		}
	}

	return "", false
}

// expansionWord stands for an expansion in the text of an argument read as a script: one plain word
// that names no tool.
const expansionWord = "_"

// scriptText returns the text a command receives as the argument raw, with each expansion the
// shell makes in it standing as expansionWord. A command that runs a string (`su -c "git push $b"`)
// runs it with the expansion already made, so what it holds stays a word of the script.
func scriptText(raw *syntax.Word, home string) string {
	parts := make([]syntax.WordPart, 0, len(raw.Parts))
	for _, part := range raw.Parts {
		switch p := part.(type) {
		case *syntax.Lit, *syntax.SglQuoted:
			parts = append(parts, p)
		case *syntax.DblQuoted:
			inner := make([]syntax.WordPart, 0, len(p.Parts))
			for _, q := range p.Parts {
				if _, ok := q.(*syntax.Lit); !ok {
					q = &syntax.Lit{Value: expansionWord}
				}

				inner = append(inner, q)
			}

			parts = append(parts, &syntax.DblQuoted{Parts: inner, Dollar: p.Dollar})
		default:
			parts = append(parts, &syntax.Lit{Value: expansionWord})
		}
	}

	return readWord(&syntax.Word{Parts: parts}, home).Text
}

// shellWordBreaks are the characters that split text into several shell words or make it more than
// words: a blank, a list or pipe operator, a redirect, a substitution, or a subshell.
const shellWordBreaks = " \t\n;&|()<>`$"

// stringRunners are the command words that run a string or a file as shell commands, besides the
// shells.
func stringRunners() []string {
	return []string{"eval", "source", "."}
}

// scriptTool reads text as a shell script and returns the tool one of its commands is, or hands
// over as handedTool reads it, and reports whether there is one. Text with no blank and no shell
// operator is one word, which handedTool has already judged. Text that does not parse is no
// script, but the command it goes to may run it in a language of its own (`python3 -c`), so it
// hands over the git, gh, or ils word it holds. A command word that is not a literal is passed
// over: the guard cannot tell what a variable handed to another command holds. A relative path in
// the script is read as unknown, since the command that runs it may run it anywhere.
func scriptTool(text, home string) (string, bool) {
	if !strings.ContainsAny(text, shellWordBreaks) {
		return "", false
	}

	file, err := read(text)
	if err != nil {
		return toolWordIn(text)
	}

	var name string
	syntax.Walk(file, func(node syntax.Node) bool {
		if name != "" {
			return false
		}

		stmt, ok := node.(*syntax.Stmt)
		if !ok {
			return true
		}

		call, ok := stmt.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}

		inv := invocationOf(stmt, call, home)
		command := inv.Words[0]
		switch {
		case !command.Literal, isDataConsumer(inv.Words):
		case inv.Tool == ILS && changesNothing(inv.Words[1:]):
		case inv.Tool != NoTool, isShell(command), slices.Contains(stringRunners(), command.Text):
			name = command.Text
		default:
			name, _ = handedByCall(call, inv.Words, "", home)
		}

		return name == ""
	})

	return name, name != ""
}

// changesNothing reports whether args, the words after an ils word, name an ils command that
// changes nothing, as Cobra finds the command. A command word that is not a literal, or that is
// no command of ils, may become any command once another command fills it in (`find -exec ils
// {}`), so it changes something.
func changesNothing(args []Word) bool {
	at := commandWords(args, ilsSwitches())
	if len(at) == 0 {
		return false
	}

	sub := args[at[0]]

	return sub.Literal && (sub.Text == landing.StatusCommand || slices.Contains(readOnlyILSCommands(), sub.Text))
}

// readOnlyILSCommands are the commands of ils that change nothing besides `ils status`. `help` is
// Cobra's own command, which ils never declares.
func readOnlyILSCommands() []string {
	return []string{landing.ParseCommand, landing.GuardCommand, landing.HookCommand, "help"}
}

// hasExtGlob reports whether an extended glob opens any part of words or of the redirect targets
// of stmt. A nil word is skipped.
func hasExtGlob(words []*syntax.Word, stmt *syntax.Stmt) bool {
	words = slices.Clone(words)
	for _, r := range stmt.Redirs {
		words = append(words, r.Word)
	}

	return slices.ContainsFunc(words, func(w *syntax.Word) bool {
		return w != nil && slices.ContainsFunc(w.Parts, func(part syntax.WordPart) bool {
			_, ok := part.(*syntax.ExtGlob)

			return ok
		})
	})
}

// isToolWord reports whether w names git, gh, ils, a shell, `eval`, or `source`: the bare name, or
// a path whose basename is the name and that is an executable regular file, a relative path read
// against dir. A path whose stat fails for any reason other than its absence counts as one, and so
// does a relative path when dir is empty, since the guard cannot rule it out.
//
// `.` is not counted: as an argument it names the current directory far more often (`find .`,
// `docker build .`) than it runs a script.
func isToolWord(w Word, dir string) bool {
	program := programName(w)
	runner := toolOf(w) != NoTool || isShell(w) || program == "eval" || program == "source"
	if !w.Literal || !runner {
		return false
	}

	if !strings.Contains(w.Text, "/") {
		return true
	}

	name := w.Text
	if !filepath.IsAbs(name) {
		if dir == "" {
			return true
		}

		name = filepath.Join(dir, name)
	}

	info, err := os.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}

	if err != nil {
		return true
	}

	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// programName returns the basename of the command word, with a leading zsh `=` lookup removed.
func programName(command Word) string {
	return path.Base(strings.TrimPrefix(command.Text, "="))
}

func toolOf(command Word) Tool {
	if !command.Literal {
		return NoTool
	}

	switch name := Tool(programName(command)); name {
	case Git, GH, ILS:
		return name
	default:
		return NoTool
	}
}

// isBare reports whether the command word raw, read as command, is written as a plain name: one
// unquoted part with no escape, no path, and no leading `=`.
func isBare(raw *syntax.Word, command Word) bool {
	if len(raw.Parts) != 1 {
		return false
	}

	lit, ok := raw.Parts[0].(*syntax.Lit)

	return ok && command.Literal && lit.Value == command.Text &&
		!strings.Contains(command.Text, "/") && !strings.HasPrefix(command.Text, "=")
}
