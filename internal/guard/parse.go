// Package guard reads a shell command into the invocations it holds.
package guard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
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
	// Env is the environment the command would start with; Parse reads CDPATH, GIT_DIR, and
	// GIT_WORK_TREE from it.
	Env map[string]string
	// Home is the HOME that `~` and `~/…` expand with.
	Home string
	// Dir is the absolute directory the command starts in; empty when unknown.
	Dir string
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
	// EnvUnset are the names an `env` wrapper removed from the environment with `-u`, in order.
	EnvUnset []string
	Marks    []Mark
	// Dir is the directory the invocation runs in; empty when the guard cannot follow it.
	Dir string
	// env is every change to the environment the command runs with, in the order bash makes them.
	env []envChange
	// Target is where a git invocation points git; zero for any other tool.
	Target GitTarget
	// Bare reports whether the command word is written as a plain unquoted name, with no path, no
	// quoting or escape, and no leading `=`.
	Bare bool
	// Whole reports whether this invocation is the entire command: one simple command with a bare
	// command word, and no pipe, list, redirect, background `&`, assignment prefix, or wrapper.
	Whole bool
	// HeadUnknown reports whether an invocation before it may have moved HEAD.
	HeadUnknown bool
	// EnvCleared reports whether an `env` wrapper was given `-i`, which starts the command with an
	// empty environment.
	EnvCleared bool
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

// The kinds of mark Parse puts on an invocation.
const (
	// ExtendedGlob is an extended glob such as `@(a|b)`, or zsh's `*(e:…:)` qualifier, which runs
	// code.
	ExtendedGlob MarkKind = "extended glob"
	// DynamicCommand is a command word that is not a literal, so the program it runs is unknown.
	DynamicCommand MarkKind = "command word that is not a literal"
	// ToolArgument is a git, gh, or ils word among the arguments of a command that may run it.
	ToolArgument MarkKind = "git, gh, or ils word handed to another command"
	// DynamicScript is a string a shell or `eval` runs that is not a literal.
	DynamicScript MarkKind = "shell string that is not a literal"
	// DeepScript is a shell string nested past the depth Parse follows.
	DeepScript MarkKind = "shell string nested too deep"
	// HiddenScript is a shell that runs a script the command does not hold: standard input, a
	// file, or a process substitution.
	HiddenScript MarkKind = "shell running a script the command does not hold"
	// GitVariable is an assignment or export of a GIT_* variable off the guard's allow list.
	GitVariable MarkKind = "git variable off the allow list"
)

// maxDepth is how many levels of strings run by `bash -c`, `eval`, or a heredoc fed to a shell
// Parse follows.
const maxDepth = 3

// Redirect is one redirection of an invocation.
type Redirect struct {
	// Fd is the descriptor written before the operator, empty when none is.
	Fd     string
	Op     string
	Target Word
}

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

// Stdout returns the file the invocation's stdout is redirected to, and false when its redirects
// leave stdout on a descriptor rather than a file. The last redirect of stdout decides, as in the
// shell.
func (inv Invocation) Stdout() (Word, bool) {
	var target Word
	toFile := false
	for _, r := range inv.Redirects {
		ontoStdout := r.Fd == "" || r.Fd == "1"
		switch r.Op {
		case ">", ">>", ">|", ">>|":
			if ontoStdout {
				target, toFile = r.Target, true
			}
		case "&>", "&>>", "&>|", "&>>|":
			target, toFile = r.Target, true
		case "<>":
			// `<>` opens its file for reading and writing on fd 0 unless a descriptor is written.
			if r.Fd == "1" {
				target, toFile = r.Target, true
			}
		case ">&":
			// `>&word` duplicates a descriptor when word is a number or `-`, and otherwise sends
			// stdout and stderr to the file word names.
			if ontoStdout {
				target, toFile = r.Target, !isDescriptor(r.Target)
			}
		}
	}

	if !toFile {
		return Word{}, false
	}

	return target, true
}

func isDescriptor(w Word) bool {
	if !w.Literal || w.Text == "" {
		return false
	}

	return w.Text == "-" || strings.Trim(w.Text, "0123456789") == ""
}

// Parse reads command with bash's grammar and returns one invocation for every simple command in
// its parse tree, in source order, whether or not it would run: a function body, an `if` branch,
// and a command substitution inside an argument each give their own. A string run by `bash -c`
// or `eval`, and a literal heredoc fed to a shell, are parsed in turn to a depth of maxDepth, their
// invocations following the one that runs them. What Parse cannot read is marked on the invocation
// it occurs in. It returns a *ParseError when the parser cannot read command or a string it runs.
func Parse(command string, caller Caller) ([]Invocation, error) {
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

	invocations := w.invocations
	if len(invocations) == 1 && len(file.Stmts) == 1 && alone(file.Stmts[0]) &&
		invocations[0].Bare && len(invocations[0].Wrappers) == 0 {
		invocations[0].Whole = true
	}

	return invocations, nil
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
	timed       map[*syntax.Stmt]bool
	err         error
	invocations []Invocation
	depth       int
	// headMoved reports whether an invocation walked so far may move HEAD.
	headMoved bool
	// probing reports whether the walk is a dry run of a body that may repeat, whose invocations
	// are dropped once it ends.
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
	// HEAD move in one comes before the command; their invocations still follow its own.
	at := len(w.invocations)
	w.substitutions(stmt, sc)
	held := slices.Clone(w.invocations[at:])
	w.invocations = w.invocations[:at]
	switch cmd := stmt.Cmd.(type) {
	case *syntax.CallExpr:
		w.add(stmt, cmd, sc)
	case *syntax.DeclClause:
		w.invocations = append(w.invocations, w.placed(declarationOf(stmt, cmd, w.caller.Home), sc))
		sc.declare(cmd, w.caller.Home)
	case *syntax.LetClause:
		// Parse does not read an arithmetic expression back into text, so each one is a word that is
		// not literal.
		words := append([]Word{{Text: "let", Literal: true}}, make([]Word, len(cmd.Exprs))...)
		inv := Invocation{Words: words, Redirects: redirectsOf(stmt, w.caller.Home), Bare: true}
		w.invocations = append(w.invocations, w.placed(inv, sc))
	}

	w.invocations = append(w.invocations, held...)

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
	script, runs, mark := scriptOf(stmt, inv.Words)
	if runs && w.depth == maxDepth {
		runs, mark = false, DeepScript
	}

	inv.Marks = marksOf(call, stmt, inv)
	if mark != "" {
		inv.Marks = append(inv.Marks, Mark{Kind: mark})
	}

	names := make([]string, 0, len(inv.Assignments))
	for _, a := range inv.Assignments {
		names = append(names, a.Name)
	}

	inv.Marks = append(inv.Marks, gitVariableMarks(names)...)
	w.invocations = append(w.invocations, inv)
	w.headMoved = w.headMoved || movesHead(inv)
	if runs {
		w.script(script, inv, sc)
	}

	sc.apply(inv)
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

// seeThrough returns the index in words of the command that the wrappers opening words run, and
// what those wrappers set on its invocation: their names, the `VAR=…` arguments of an `env` among
// them, and the environment its options clear or remove. A wrapper with nothing after it is the
// command itself.
func seeThrough(args []*syntax.Word, words []Word, home string) (int, Invocation) {
	start := 0
	var inv Invocation
	for start < len(words) && words[start].Literal && !isLookup(words[start:]) {
		name := words[start].Text
		options, ok := wrapperOptionsOf(name)
		if !ok {
			break
		}

		inv.Wrappers = append(inv.Wrappers, name)
		n, given := skipOptions(words[start+1:], options)
		start += 1 + n
		if name == "env" {
			start += envArguments(&inv, given, args[start:], home)
		}
	}

	if start == len(words) {
		return 0, Invocation{}
	}

	return start, inv
}

// envArguments records on inv what an `env` wrapper does to the environment: the options given to
// it, then its `VAR=…` arguments at the start of args. It returns how many of args those are.
func envArguments(inv *Invocation, given []option, args []*syntax.Word, home string) int {
	for _, option := range given {
		switch option.letter {
		case 'i':
			inv.EnvCleared = true
			inv.env = append(inv.env, envChange{clear: true})
		case 'u':
			inv.EnvUnset = append(inv.EnvUnset, option.value)
			inv.env = append(inv.env, envChange{unset: option.value})
		}
	}

	for i, arg := range args {
		a, ok := envAssignment(arg, home)
		if !ok {
			return i
		}

		inv.Assignments = append(inv.Assignments, a)
		inv.env = append(inv.env, envChange{assignment: a})
	}

	return len(args)
}

// wrapperOptions are the options a wrapper takes before the command it runs.
type wrapperOptions struct {
	// flags are the option letters that take no value.
	flags string
	// valued are the option letters that take a value, attached or as the next word.
	valued string
	// numeric reports whether `-N` is an option, as nice's adjustment is.
	numeric bool
	// none reports whether the wrapper takes no option at all, not even `--`.
	none bool
}

// option is one option letter given to a wrapper, with its value when it takes one.
type option struct {
	value  string
	letter byte
}

// wrappers is the table of wrapper words: command words that run the command after them, each
// with the options it takes before that command. An option a wrapper does not take is left out, so
// that it ends the options and stands as the command word.
func wrappers() map[string]wrapperOptions {
	return map[string]wrapperOptions{
		"env":       {flags: "i", valued: "u"},
		"command":   {flags: "p"},
		"exec":      {flags: "cl", valued: "a"},
		"nice":      {valued: "n", numeric: true},
		"time":      {flags: "p"},
		"builtin":   {},
		"nohup":     {},
		"noglob":    {none: true},
		"nocorrect": {none: true},
	}
}

// wrapperOptionsOf returns the options of the wrapper name, and false when name is no wrapper.
func wrapperOptionsOf(name string) (wrapperOptions, bool) {
	options, ok := wrappers()[name]

	return options, ok
}

// skipOptions returns how many of words, the words after a wrapper, are options it takes, and the
// letters given among them. It stops at the first word that is not literal, is no option, or holds
// a letter the wrapper does not take, or whose value is missing or not literal.
func skipOptions(words []Word, options wrapperOptions) (int, []option) {
	var given []option
	for i := 0; i < len(words); i++ {
		text := words[i].Text
		switch {
		case !words[i].Literal || options.none || len(text) < 2 || text[0] != '-':
			return i, given
		case text == "--":
			return i + 1, given
		case options.numeric && strings.Trim(text[1:], "0123456789") == "":
			continue
		}

		letters, n, ok := optionLetters(text[1:], words[i+1:], options)
		if !ok {
			return i, given
		}

		given = append(given, letters...)
		i += n
	}

	return len(words), given
}

// optionLetters reads the option letters of one word, after its `-`, and returns them with how
// many of the next words their value took. It reports false when a letter is one the wrapper does
// not take, or when its value is missing or not literal.
func optionLetters(letters string, next []Word, options wrapperOptions) ([]option, int, bool) {
	var given []option
	for j := range len(letters) {
		letter := letters[j]
		switch {
		case strings.IndexByte(options.flags, letter) >= 0:
			given = append(given, option{letter: letter})
		case strings.IndexByte(options.valued, letter) < 0:
			return nil, 0, false
		case j+1 < len(letters):
			return append(given, option{letter: letter, value: letters[j+1:]}), 0, true
		case len(next) > 0 && next[0].Literal:
			return append(given, option{letter: letter, value: next[0].Text}), 1, true
		default:
			return nil, 0, false
		}
	}

	return given, 0, true
}

// envAssignment reads raw as an `env` argument of the form `NAME=value`.
func envAssignment(raw *syntax.Word, home string) (Assignment, bool) {
	lit, ok := raw.Parts[0].(*syntax.Lit)
	if !ok {
		return Assignment{}, false
	}

	name, rest, found := strings.Cut(lit.Value, "=")
	if !found || !isName(name) {
		return Assignment{}, false
	}

	parts := slices.Clone(raw.Parts[1:])
	if rest != "" {
		parts = slices.Insert(parts, 0, syntax.WordPart(&syntax.Lit{Value: rest}))
	}

	if len(parts) == 0 {
		return Assignment{Name: name, Value: Word{Literal: true}}, true
	}

	return Assignment{Name: name, Value: readWord(&syntax.Word{Parts: parts}, home)}, true
}

func isName(s string) bool {
	for i, c := range s {
		letter := c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}

	return s != ""
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
	names := []string{
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
	}
	table := make(map[string]bool, len(names))
	for _, name := range names {
		table[name] = true
	}

	return table
}

// isDataConsumer reports whether the command words open with takes its arguments as data.
func isDataConsumer(words []Word) bool {
	return isLookup(words) || dataConsumers()[words[0].Text]
}

// marksOf returns what Parse cannot read in the invocation inv of call, apart from the string it
// runs.
func marksOf(call *syntax.CallExpr, stmt *syntax.Stmt, inv Invocation) []Mark {
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
	case slices.ContainsFunc(inv.Words[1:], func(w Word) bool { return isToolWord(w, inv.Dir) }):
		marks = append(marks, Mark{Kind: ToolArgument})
	}

	return marks
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

// isToolWord reports whether w names git, gh, or ils: the bare name, or a path whose basename is
// the name and that is an executable regular file, a relative path read against dir. A path whose
// stat fails for any reason other than its absence counts as one, and so does a relative path when
// dir is empty, since the guard cannot rule it out.
func isToolWord(w Word, dir string) bool {
	if !w.Literal || toolOf(w) == NoTool {
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
