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
	// those of each compound command around it, and those of an earlier `exec` with no command.
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
	// racing reports whether the invocation runs beside the commands around it, in a background job
	// or a stage of a pipe.
	racing bool
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
	// ExtendedGlob is an extended glob such as `@(a|b)`, or zsh's `*(e:…:)` qualifier, which runs
	// code. A zsh qualifier that runs nothing, such as `*(N)`, is not one.
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
	// HiddenScript is a shell, or a `source` or `.`, that runs a script the command does not hold:
	// standard input, a startup file, or a process substitution.
	HiddenScript MarkKind = "shell running a script the command does not hold"
	// GitVariable is an assignment or export of a GIT_* variable off the guard's allow list.
	GitVariable MarkKind = "git variable off the allow list"
	// DeepCall is a call of a function the command defines past the maxCalls calls Parse follows
	// into the body they run, so the guard cannot tell where the commands of that body run.
	DeepCall MarkKind = "call of a function past the calls the guard follows"
	// ShadowingFunction is a function the command defines under the name of a command the guard
	// reads, such as `cd`, a wrapper, or a declaration builtin like `export`, which every later call
	// of that name runs instead.
	ShadowingFunction MarkKind = "function defined over a command the guard reads"
	// ToolAlias is an alias whose value may run git, gh, or ils, or that the guard cannot read.
	ToolAlias MarkKind = "alias that may run git, gh, or ils"
	// ReadCommandAlias is an alias whose value runs a command the guard reads, such as `cd` or a
	// wrapper: a later command that names the alias runs it without the guard following it.
	ReadCommandAlias MarkKind = "alias that runs a command the guard reads"
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
	// dryRuns is shared by w and every walker it probes with; newWalker creates it.
	dryRuns *dryRuns
	// loopBodies counts the walks of `for` loop bodies the walk made once per value of the loop's
	// list, dry runs included, which it stops doing past maxCalls, the bound it holds calls to as
	// well. It is shared by w and every walker it probes or walks git's scripts with, so the bound
	// holds for the whole command; newWalker creates it.
	loopBodies *int
	// walked holds every invocation the walk placed, in the order it walked them, those of dry runs
	// included.
	walked []Invocation
	// kept holds the index in walked of each invocation Parse returns, in the order it returns them.
	kept  []int
	depth int
	// calls counts the calls whose bodies the walk followed where the call stands, and calling
	// those under way, inside which every invocation the walk keeps is atCall.
	calls, calling int
	// probing reports whether the walk is a dry run, whose invocations are dropped once it ends: of
	// a body that may repeat, of a background job or pipe stage, or of a function body or an alias
	// string.
	probing bool
}

// newWalker returns a walker for caller that reads at depth levels of nested strings.
func newWalker(caller Caller, depth int) walker {
	return walker{
		caller:     caller,
		timed:      map[*syntax.Stmt]bool{},
		pipes:      map[*syntax.Stmt]*syntax.Stmt{},
		depth:      depth,
		dryRuns:    &dryRuns{},
		loopBodies: new(int),
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
	values := make([]*syntax.Word, 0, len(decl.Args))
	var names []string
	nameless := false
	for _, a := range decl.Args {
		words = append(words, declarationWord(a, ex))
		values = append(values, a.Value)
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

	inv := Invocation{Words: words, Redirects: redirectsOf(stmt, ex), Bare: true}
	if hasExtGlob(values, stmt) {
		inv.Marks = []Mark{{Kind: ExtendedGlob}}
	}

	inv.Marks = append(inv.Marks, gitVariableMarks(names)...)
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
	script, runs, mark := scriptOf(stmt, call, inv.Words)
	runs, mark = capDepth(w.depth, runs, mark)

	inv.Marks = append(w.aliasMarks(inv), marksOf(call, stmt, inv, w.reading(sc))...)
	if mark != "" {
		inv.Marks = append(inv.Marks, Mark{Kind: mark})
	}

	commands := goCommands(&inv, w.depth)

	names := make([]string, 0, len(inv.Assignments))
	for _, a := range inv.Assignments {
		names = append(names, a.Name)
	}

	assigned := assignedNames(stmt, inv.Words)
	sc.assignUnknown(assigned)
	if sc.allexport {
		names = append(names, assigned...)
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

	sc.assignVariables(call, inv)
	if len(inv.Words) > 0 {
		sc.vars = sc.vars.ran(inv, sc.functions)
	}

	sc.apply(inv)
	w.call(inv, sc)
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
// Alias and assignment marks
// ---------------------------------------------------------------------------

// aliasMarks marks each alias the `alias` invocation inv defines whose value takes a mark (see
// aliasKind), or is a string the guard cannot read. A later command that names the alias runs its
// value from wherever it stands.
func (w *walker) aliasMarks(inv Invocation) []Mark {
	if len(inv.Words) == 0 || !inv.Words[0].Literal || inv.Words[0].Text != "alias" {
		return nil
	}

	var marks []Mark
	for _, word := range inv.Words[1:] {
		name, value, defines := strings.Cut(word.Text, "=")
		kind := ToolAlias
		if word.Literal {
			if !defines {
				continue
			}

			var marked bool
			if kind, marked = w.aliasKind(value); !marked {
				continue
			}
		}

		marks = append(marks, Mark{Kind: kind, Name: name})
	}

	return marks
}

// aliasKind returns the mark an alias whose value is the shell string value takes, and false when
// it takes none. The mark is ToolAlias when value holds an invocation of git, gh, or ils, or one
// the guard marks, or when the guard cannot read it; it is ReadCommandAlias when value runs a
// command the guard reads (see readsThrough), whose effect the guard never applies to the command
// that names the alias.
func (w *walker) aliasKind(value string) (MarkKind, bool) {
	file, err := read(value)
	if err != nil || w.depth == maxDepth {
		return ToolAlias, true
	}

	sub := w.probe()
	sc := laterScope()
	sub.nested(file, &sc)

	holdsTool := slices.ContainsFunc(sub.walked, func(inv Invocation) bool {
		return inv.Tool != NoTool || len(inv.Marks) > 0
	})
	switch {
	case sub.err != nil || holdsTool:
		return ToolAlias, true
	case slices.ContainsFunc(sub.walked, readsThrough):
		return ReadCommandAlias, true
	default:
		return "", false
	}
}

// readsThrough reports whether inv runs a command the guard reads: `cd`, `pushd`, `popd`, or a
// wrapper it sees through, whether as the command word or in front of it.
func readsThrough(inv Invocation) bool {
	if len(inv.Wrappers) > 0 {
		return true
	}

	return len(inv.Words) > 0 && inv.Words[0].Literal && readsCommand(inv.Words[0].Text)
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

// alone reports whether stmt is a simple command with no assignment prefix, redirect, or
// background operator.
func alone(stmt *syntax.Stmt) bool {
	call, ok := stmt.Cmd.(*syntax.CallExpr)

	return ok && len(call.Args) > 0 && len(call.Assigns) == 0 && len(stmt.Redirs) == 0 &&
		!stmt.Background && !stmt.Coprocess && !stmt.Disown
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

// hasExtGlob reports whether any part of words, or of the redirect targets of stmt, is an extended
// glob the guard cannot read. A nil word is skipped. A zsh glob qualifier that runs nothing, such
// as the null glob `*(N)`, is read: see runsNoCode.
func hasExtGlob(words []*syntax.Word, stmt *syntax.Stmt) bool {
	words = slices.Clone(words)
	for _, r := range stmt.Redirs {
		words = append(words, r.Word)
	}

	return slices.ContainsFunc(words, func(w *syntax.Word) bool {
		return w != nil && slices.ContainsFunc(w.Parts, func(part syntax.WordPart) bool {
			glob, ok := part.(*syntax.ExtGlob)

			return ok && !runsNoCode(glob)
		})
	})
}

// runsNoCode reports whether glob is a `*(…)` whose parentheses hold no `e`, `+`, `$`, backtick
// or inner parenthesis: zsh's glob qualifier in a form that runs nothing (`*(N)`, `*(.)`,
// `*(om[1])`). zsh runs the string of an `e` qualifier (`*(e:'git push':)`) and the function a `+`
// names (`*(+f)`) as code, and the shell expands a substitution inside the parentheses
// (`*($(git push)N)`, `*(<(git push)N)`) before it globs; the parser keeps the parentheses as raw
// text, so the guard never walks one. Every other operator opens a bash or ksh extended glob, which
// stays unread.
func runsNoCode(glob *syntax.ExtGlob) bool {
	return glob.Op == syntax.GlobZeroOrMore && !strings.ContainsAny(glob.Pattern.Value, "e+$`(")
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
