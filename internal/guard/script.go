package guard

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// isShell reports whether the command word names a shell whose `-c` string Parse reads.
func isShell(command Word) bool {
	switch programName(command) {
	case "bash", "sh", "zsh", "dash":
		return true
	default:
		return false
	}
}

// scriptOf returns the string that the invocation of words runs as commands of its own, when it
// is `eval` or a shell given `-c` or a heredoc, and reports whether there is one. The mark names
// a string it runs that Parse cannot read.
func scriptOf(stmt *syntax.Stmt, words []Word) (string, bool, Mark) {
	switch {
	case len(words) == 0 || !words[0].Literal:
		return "", false, ""
	case words[0].Text == "eval":
		return evalScript(words[1:])
	case isShell(words[0]):
		return shellScript(stmt, words[1:])
	default:
		return "", false, ""
	}
}

func evalScript(args []Word) (string, bool, Mark) {
	if len(args) == 0 {
		return "", false, ""
	}

	texts := make([]string, 0, len(args))
	for _, arg := range args {
		if !arg.Literal {
			return "", false, DynamicScript
		}

		texts = append(texts, arg.Text)
	}

	return strings.Join(texts, " "), true, ""
}

// shellScript returns the script a shell given args runs: its `-c` string, or the heredoc on its
// standard input. Any other script it would read is marked hidden.
func shellScript(stmt *syntax.Stmt, args []Word) (string, bool, Mark) {
	operands, command, stdin := shellOptions(args)
	switch {
	case command && len(operands) == 0:
		return "", false, ""
	case command && !operands[0].Literal:
		return "", false, DynamicScript
	case command:
		return operands[0].Text, true, ""
	case len(operands) > 0 && !stdin:
		return "", false, HiddenScript
	default:
		return heredocScript(stmt)
	}
}

// shellOptions splits the arguments of a shell into the operands after its options, and reports
// whether the options hold `-c` and `-s`.
func shellOptions(args []Word) ([]Word, bool, bool) {
	command, stdin := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i].Text
		switch {
		case !args[i].Literal || len(arg) < 2 || (arg[0] != '-' && arg[0] != '+'):
			return args[i:], command, stdin
		case arg == "--":
			return args[i+1:], command, stdin
		case arg == "-o" || arg == "+o" || arg == "-O" || arg == "+O":
			i++
		case arg[0] == '-' && arg[1] != '-':
			command = command || strings.ContainsRune(arg, 'c')
			stdin = stdin || strings.ContainsRune(arg, 's')
		}
	}

	return nil, command, stdin
}

// heredocScript returns the script on the standard input of stmt when it is a literal heredoc,
// and marks any other standard input as hidden.
func heredocScript(stmt *syntax.Stmt) (string, bool, Mark) {
	var input *syntax.Redirect
	for _, r := range stmt.Redirs {
		if (r.N == nil || r.N.Value == "0") && isInput(r.Op) {
			input = r
		}
	}

	if input == nil || (input.Op != syntax.Hdoc && input.Op != syntax.DashHdoc) {
		return "", false, HiddenScript
	}

	if input.Hdoc == nil {
		return "", true, ""
	}

	var body strings.Builder
	for _, part := range input.Hdoc.Parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			return "", false, DynamicScript
		}

		body.WriteString(lit.Value)
	}

	if quoted(input.Word) {
		return body.String(), true, ""
	}

	return unescapeHeredoc(body.String()), true, ""
}

func isInput(op syntax.RedirOperator) bool {
	switch op {
	case syntax.RdrIn, syntax.RdrInOut, syntax.DplIn, syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		return true
	default:
		return false
	}
}

// quoted reports whether the heredoc delimiter w has any quoting, which leaves its body as written.
func quoted(w *syntax.Word) bool {
	if len(w.Parts) != 1 {
		return true
	}

	lit, ok := w.Parts[0].(*syntax.Lit)

	return !ok || strings.Contains(lit.Value, `\`)
}

// unescapeHeredoc removes the backslashes the shell removes from the body of an unquoted heredoc:
// before `$`, a backtick, a backslash, and a newline.
func unescapeHeredoc(body string) string {
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' && i+1 < len(body) && strings.IndexByte("$`\\\n", body[i+1]) >= 0 {
			i++
			if body[i] == '\n' {
				continue
			}
		}

		b.WriteByte(body[i])
	}

	return b.String()
}
