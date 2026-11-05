//go:build unix

package guard

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
)

// ---------------------------------------------------------------------------
// Invocation model
// ---------------------------------------------------------------------------

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
	// GIT_WORK_TREE, GH_CONFIG_DIR, XDG_CONFIG_HOME, HOME, and IFS from it.
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
	// head is the branch HEAD is on where the invocation runs, once a switch earlier in its `&&`
	// list moved it there; empty when HEAD is where the repository has it, or HeadUnknown.
	head string
	// follows is the switch of the `&&` list the invocation runs after, which leaves HEAD on its
	// branch unless HEAD moves again before the invocation; nil when it runs after none.
	follows *headClaim
	// sets is the switch the invocation makes, when it is one followSwitch reads; nil otherwise.
	sets *headClaim
	// env is every change to the environment the command runs with, in the order bash makes them.
	env []envChange
	// enclosing are the redirects in effect on the shell's descriptors where the command runs:
	// those of each compound command around it.
	enclosing []Redirect
	// pipedInto are the words, behind any wrapper, of the simple command of the pipe stage that
	// reads the invocation's stdout; none when no such stage follows it.
	pipedInto []Word
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
	// HeadUnknown reports whether an invocation before it may have moved HEAD to a branch the guard
	// cannot tell.
	HeadUnknown bool
	// beside reports whether the invocation runs beside the commands around it, in a background job
	// or a stage of a pipe, so a move of HEAD it makes is not followed.
	beside bool
	// atCall reports whether the invocation is a command of a function body walked where a call of
	// the function stands, which Parse leaves out: the walk of the body where it is declared holds
	// the same command.
	atCall bool
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
	// DynamicCommand is a command word that is not a literal, so the program it runs is unknown.
	DynamicCommand MarkKind = "command word that is not a literal"
	// ToolArgument is a git, gh, ils, or shell word among the arguments of a command that may run
	// it.
	ToolArgument MarkKind = "git, gh, ils, or shell word handed to another command"
	// DynamicScript is a string a shell, `eval`, or git runs that is not a literal.
	DynamicScript MarkKind = "shell string that is not a literal"
	// DeepScript is a shell string nested past the depth Parse follows.
	DeepScript MarkKind = "shell string nested too deep"
	// GitVariable is an assignment or export of a GIT_* variable off the guard's allow list.
	GitVariable MarkKind = "git variable off the allow list"
	// DeepCall is a call of a function the command defines whose body walks would take Parse past
	// the maxCalls it makes, so the guard cannot tell where the commands of that body run.
	DeepCall MarkKind = "call of a function past the calls the guard follows"
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
//
// The body of a function the command defines is walked again where each call of it stands, from
// the shell as it is there; Parse leaves out the invocations of those walks, which Assess judges.
// A call past the calls the walks follow that stands outside them is returned as an invocation
// holding only its DeepCall mark, as the marks of a call are.
func Parse(ctx context.Context, command string, caller Caller) ([]Invocation, error) {
	invocations, err := parse(ctx, command, caller)

	return slices.DeleteFunc(invocations, func(inv Invocation) bool { return inv.atCall }), err
}

// parse reads command as Parse does, and also returns the invocations of the function bodies walked
// where a call stands, each one after the call.
func parse(ctx context.Context, command string, caller Caller) ([]Invocation, error) {
	file, err := read(command)
	if err != nil {
		return nil, err
	}

	w := newWalker(caller, 0)
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
// statement with no assignment prefix, redirect, or background operator, a bare command word and no
// wrapper.
func isWhole(file *syntax.File, invocations []Invocation) bool {
	if len(invocations) != 1 || len(file.Stmts) != 1 {
		return false
	}

	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)

	return ok && len(call.Args) > 0 && len(call.Assigns) == 0 && len(stmt.Redirs) == 0 &&
		!stmt.Background && !stmt.Coprocess && !stmt.Disown &&
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

// ---------------------------------------------------------------------------
// Walking a parse tree
// ---------------------------------------------------------------------------

// walker collects the invocations of one parse tree and of the strings it runs, depth levels of
// nested strings down.
type walker struct {
	caller Caller
	// timed holds the statements a `time` keyword runs.
	timed map[*syntax.Stmt]bool
	// pipes holds, for each statement that is a stage of a pipe, the stage after it.
	pipes map[*syntax.Stmt]*syntax.Stmt
	err   error
	// loopBodies counts the walks of `for` loop bodies the walk made once per value of the loop's
	// list, dry runs included, which it stops doing past maxCalls, the bound bodies holds
	// function-body walks to as well. It is shared by w and every walker it probes or walks git's
	// scripts with, so the bound holds for the whole command; newWalker creates it.
	loopBodies *int
	// bodies counts the walks of function bodies the walk made, dry runs included, which it stops
	// making past maxCalls. It is shared like loopBodies; newWalker creates it.
	bodies *int
	// walked holds every invocation the walk placed, in the order it walked them, those of dry runs
	// included.
	walked []Invocation
	// kept holds the index in walked of each invocation Parse returns, in the order it returns them.
	kept  []int
	depth int
	// calling counts the calls whose bodies the walk is following where the call stands, inside
	// which every invocation the walk keeps is atCall.
	calling int
	// beside counts the background jobs and pipe stages the walk is inside, in which every
	// invocation the walk keeps is beside.
	beside int
	// probing reports whether the walk is a dry run, whose invocations are dropped once it ends: of
	// a body that may repeat, or of a function body.
	probing bool
}

// newWalker returns a walker for caller that reads at depth levels of nested strings.
func newWalker(caller Caller, depth int) walker {
	return walker{
		caller:     caller,
		timed:      map[*syntax.Stmt]bool{},
		pipes:      map[*syntax.Stmt]*syntax.Stmt{},
		depth:      depth,
		loopBodies: new(int),
		bodies:     new(int),
	}
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
	// HEAD move in one comes before the command; their invocations still follow its own, so they
	// move behind the command's own once it is walked.
	at := len(w.kept)
	w.substitutions(stmt, sc)
	held := len(w.kept) - at
	switch cmd := stmt.Cmd.(type) {
	case *syntax.CallExpr:
		w.add(stmt, cmd, sc)
	case *syntax.DeclClause:
		ex := w.reading(sc)
		ex.vars = ex.vars.forgetAssigned(cmd.Args)
		inv := w.placed(declarationOf(stmt, cmd, ex), sc)
		scripts := w.variableScripts(&inv, declared(cmd, ex))
		w.keep(inv)
		sc.declare(cmd, ex)
		w.variableCommands(scripts, laterScope())
	case *syntax.LetClause:
		// Parse does not read an arithmetic expression back into text, so each one is a word that is
		// not literal.
		words := append([]Word{{Text: "let", Literal: true}}, make([]Word, len(cmd.Exprs))...)
		inv := Invocation{Words: words, Redirects: redirectsOf(stmt, w.reading(sc)), Bare: true}
		w.keep(w.placed(inv, sc))
	}

	w.kept = moveBehind(w.kept, at, at+held)

	return true
}

// reading returns the expander that reads the words of a command run from sc.
func (w *walker) reading(sc *scope) expander {
	return expander{vars: sc.vars, home: w.caller.Home}
}

func moveBehind[T any](s []T, at, mid int) []T {
	return slices.Concat(s[:at], s[mid:], s[at:mid])
}

// declarationOf returns the invocation of a declaration builtin such as `export` or `declare`: its
// name, then each of its arguments as a word. An argument the parser did not split into a name and a
// value (`export "$v=x"`) may assign any variable, so it takes a GitVariable mark that names none
// when the guard cannot read the name.
func declarationOf(stmt *syntax.Stmt, decl *syntax.DeclClause, ex expander) Invocation {
	words := []Word{{Text: decl.Variant.Value, Literal: true}}
	var names []string
	nameless := false
	for _, a := range decl.Args {
		words = append(words, declarationWord(a, ex))
		if a.Name != nil {
			names = append(names, a.Name.Value)

			continue
		}

		name, assigns := unsplitName(a, ex)
		switch {
		case !assigns:
		case name == "":
			nameless = true
		default:
			names = append(names, name)
		}
	}

	inv := Invocation{Words: words, Redirects: redirectsOf(stmt, ex), Bare: true, Marks: gitVariableMarks(names)}
	if nameless {
		inv.Marks = append(inv.Marks, Mark{Kind: GitVariable})
	}

	return inv
}

// declarationWord returns the argument a of a declaration builtin as the word the shell reads:
// an option or a name alone, or `NAME=value`.
func declarationWord(a *syntax.Assign, ex expander) Word {
	if a.Naked {
		if a.Name != nil {
			return Word{Text: a.Name.Value, Literal: true}
		}

		return readWord(a.Value, ex)
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
		value = readWord(a.Value, ex)
	}

	return Word{Text: prefix + value.Text, Literal: value.Literal}
}

// add appends the invocation of call, then the invocations of the string it runs, and applies to
// sc what it changes in the shell.
func (w *walker) add(stmt *syntax.Stmt, call *syntax.CallExpr, sc *scope) {
	inv := invocationOf(stmt, call, w.reading(sc))
	if w.timed[stmt] {
		inv.Wrappers = slices.Insert(inv.Wrappers, 0, "time")
	}

	inv = w.placed(inv, sc)
	inv.depth = w.depth
	inv.pipedInto = w.pipedInto(stmt, sc)
	script, runs, mark := scriptOf(stmt, inv.Words)
	runs, mark = capDepth(w.depth, runs, mark)

	inv.Marks = marksOf(call, inv, w.reading(sc))
	if mark != "" {
		inv.Marks = append(inv.Marks, Mark{Kind: mark})
	}

	names := make([]string, 0, len(inv.Assignments))
	for _, a := range inv.Assignments {
		names = append(names, a.Name)
	}

	sc.assignUnknown(assignedNames(stmt, inv.Words))

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

	sc.assignVariables(call, inv)
	if len(inv.Words) > 0 {
		sc.vars = sc.vars.ran(inv, sc.functions)
	}

	sc.apply(inv)
	w.call(inv, sc)
}

// marksOf returns what Parse cannot read in the invocation inv of call, apart from the string it
// runs. ex reads its words.
func marksOf(call *syntax.CallExpr, inv Invocation, ex expander) []Mark {
	var marks []Mark
	if len(inv.Words) == 0 {
		return marks
	}

	command := inv.Words[0]
	switch {
	case !command.Literal:
		marks = append(marks, Mark{Kind: DynamicCommand})
	case isShell(command):
		// A script file the shell runs gets its arguments as `./x.sh` does.
		if file, ok := scriptFileWords(inv.Words); ok {
			if name, handed := handedByCall(call, file, inv.Dir, ex); handed {
				marks = append(marks, Mark{Kind: ToolArgument, Name: name})
			}
		}
	case inv.Tool != NoTool, isDataConsumer(inv.Words), command.Text == "eval":
	default:
		if name, ok := handedByCall(call, inv.Words, inv.Dir, ex); ok {
			marks = append(marks, Mark{Kind: ToolArgument, Name: name})
		}
	}

	return marks
}

// laterScope returns the scope of a string a variable set for later commands holds: any later
// command may run it, from a directory and with git variables the guard cannot tell.
func laterScope() scope {
	return scope{env: unknownEnvironment()}
}

// pipedInto returns the words, behind any wrapper, of the simple command of the pipe stage that
// reads the stdout of stmt, read with the variables of sc; none when no such stage follows it.
func (w *walker) pipedInto(stmt *syntax.Stmt, sc *scope) []Word {
	next, piped := w.pipes[stmt]
	if !piped {
		return nil
	}

	stage, simple := next.Cmd.(*syntax.CallExpr)
	if !simple || len(stage.Args) == 0 {
		return nil
	}

	return invocationOf(next, stage, w.reading(sc)).Words
}

// ---------------------------------------------------------------------------
// Assignment marks
// ---------------------------------------------------------------------------

// assignedNames returns the variables the simple command stmt, whose words are words, assigns
// other than by an assignment: those `read`, `mapfile`, `readarray`, and `getopts` are given, the
// one `printf -v` is given, and each one a `${NAME:=word}` or `${NAME=word}` in it assigns.
func assignedNames(stmt *syntax.Stmt, words []Word) []string {
	var names []string
	if len(words) > 0 && words[0].Literal {
		switch words[0].Text {
		case "read", "mapfile", "readarray", "getopts":
			names = append(names, wordTexts(words[1:])...)
		case "printf":
			names = printfTarget(words[1:])
		}
	}

	syntax.Walk(stmt, func(n syntax.Node) bool {
		if p, ok := n.(*syntax.ParamExp); ok && assignsDefault(p) {
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

// invocationOf returns the invocation of call, whose words ex reads. The values of its assignment
// prefix are read with the variables it assigns unknown, since the shell makes them in turn. A call
// that only assigns has no words.
func invocationOf(stmt *syntax.Stmt, call *syntax.CallExpr, ex expander) Invocation {
	words := make([]Word, 0, len(call.Args))
	for _, arg := range call.Args {
		words = append(words, readWord(arg, ex))
	}

	values := ex
	values.vars = ex.vars.forgetAssigned(call.Assigns)
	assignments := make([]Assignment, 0, len(call.Assigns))
	for _, a := range call.Assigns {
		assignments = append(assignments, assignmentOf(a, values))
	}

	if len(words) == 0 {
		return Invocation{Redirects: redirectsOf(stmt, ex), Assignments: assignments}
	}

	prefix := make([]envChange, 0, len(assignments))
	for _, a := range assignments {
		prefix = append(prefix, envChange{assignment: a})
	}

	start, inv := seeThrough(call.Args, words, ex)
	inv.Words = words[start:]
	inv.Redirects = redirectsOf(stmt, ex)
	inv.Assignments = append(assignments, inv.Assignments...)
	inv.env = append(prefix, inv.env...)
	inv.Tool = toolOf(words[start])
	inv.Bare = isBare(call.Args[start], words[start])

	return inv
}

func assignmentOf(a *syntax.Assign, ex expander) Assignment {
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
		return Assignment{Name: name, Value: readWord(a.Value, ex)}
	}
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
