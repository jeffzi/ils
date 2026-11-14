package guard

import (
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/jeffzi/ils/internal/git"
)

// seeThrough returns the index in words of the command that the wrappers opening words run, and
// what those wrappers set on its invocation: their names, the `VAR=…` arguments of an `env` among
// them, and the environment its options clear or remove. A wrapper with nothing after it is the
// command itself.
func seeThrough(args []*syntax.Word, words []Word, ex expander) (int, Invocation) {
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
			start += envArguments(&inv, given, args[start:], ex)
		}
	}

	if start == len(words) {
		return 0, Invocation{}
	}

	return start, inv
}

// envArguments records on inv what an `env` wrapper does to the environment: the options given to
// it, then its `VAR=…` arguments at the start of args. It returns how many of args those are.
func envArguments(inv *Invocation, given []option, args []*syntax.Word, ex expander) int {
	for _, opt := range given {
		switch opt.letter {
		case 'i':
			inv.env = append(inv.env, envChange{clear: true})
		case 'u':
			inv.env = append(inv.env, envChange{unset: opt.value})
		}
	}

	for i, arg := range args {
		a, ok := envAssignment(arg, ex)
		if !ok {
			return i
		}

		inv.Assignments = append(inv.Assignments, a)
		inv.env = append(inv.env, envChange{assignment: a})
	}

	return len(args)
}

// wrapperOptions are the options a wrapper takes before the command it runs, and whether it runs
// that command in the current shell.
type wrapperOptions struct {
	// flags are the option letters that take no value.
	flags string
	// valued are the option letters that take a value, attached or as the next word.
	valued string
	// numeric reports whether `-N` is an option, as nice's adjustment is.
	numeric bool
	// none reports whether the wrapper takes no option at all, not even `--`.
	none bool
	// inShell reports whether the wrapper runs the command after it in the current shell, so that a
	// `cd` or `eval` behind it changes the shell. Every other wrapper starts a program of its own.
	inShell bool
}

// option is one option letter given to a wrapper, with its value when it takes one.
type option struct {
	value  string
	letter byte
}

// wrapperOptionsOf returns the options of the wrapper name, and false when name is no wrapper. The
// wrapper words are command words that run the command after them, each with the options it takes
// before that command. An option a wrapper does not take is left out, so that it ends the options
// and stands as the command word.
func wrapperOptionsOf(name string) (wrapperOptions, bool) {
	options, ok := map[string]wrapperOptions{
		"env":       {flags: "i", valued: "u"},
		"command":   {flags: "p", inShell: true},
		"exec":      {flags: "cl", valued: "a"},
		"nice":      {valued: "n", numeric: true},
		"time":      {flags: "p", inShell: true},
		"builtin":   {inShell: true},
		"nohup":     {},
		"noglob":    {none: true, inShell: true},
		"nocorrect": {none: true, inShell: true},
	}[name]

	return options, ok
}

// runsInShell reports whether every one of wrappers runs the command after it in the current shell,
// so that a `cd` or `eval` behind them changes the shell. Every other wrapper starts a program of
// its own.
func runsInShell(wrappers []string) bool {
	for _, wrapper := range wrappers {
		if options, _ := wrapperOptionsOf(wrapper); !options.inShell {
			return false
		}
	}

	return true
}

// skipOptions returns how many of words, the words after a wrapper, are options it takes, and the
// letters given among them. It stops at the first word that is not literal, is no option, or holds
// a letter the wrapper does not take, or whose value is missing or not literal.
func skipOptions(words []Word, options wrapperOptions) (int, []option) {
	if options.none {
		return 0, nil
	}

	var given []option
	for i := 0; i < len(words); i++ {
		text := words[i].Text
		switch {
		case !words[i].Literal || len(text) < 2 || text[0] != '-':
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

// envAssignment reads raw as an `env` argument of the form `NAME=value`, quoted in whole or in
// part: env sees the word after quote removal, so `"NAME=value"` assigns as `NAME=value` does.
func envAssignment(raw *syntax.Word, ex expander) (Assignment, bool) {
	c, ok := cutParts(raw.Parts)
	if !ok || !c.found || !git.IsName(c.head) {
		return Assignment{}, false
	}

	return Assignment{Name: c.head, Value: readWord(&syntax.Word{Parts: c.tail}, ex)}, true
}

// equalsCut is word parts cut at their first `=`.
type equalsCut struct {
	// head is the text before the `=`, or all the text when found is false.
	head string
	// tail is the parts after the `=`, with the text that follows it kept in the quotes it stood in.
	tail  []syntax.WordPart
	found bool
}

// cutParts cuts parts at their first `=`. It reports false when a part before the `=` is anything
// but plain or quoted text: the text an expansion gives is known only at run time.
func cutParts(parts []syntax.WordPart) (equalsCut, bool) {
	var head strings.Builder
	for i, part := range parts {
		c, ok := cutPart(part)
		if !ok {
			return equalsCut{}, false
		}

		head.WriteString(c.head)
		if c.found {
			return equalsCut{head: head.String(), tail: slices.Concat(c.tail, parts[i+1:]), found: true}, true
		}
	}

	return equalsCut{head: head.String()}, true
}

// cutPart is cutParts for one part. A `$'…'` or `$"…"` string is no plain text: its escapes or its
// translation decide what it holds.
func cutPart(part syntax.WordPart) (equalsCut, bool) {
	switch p := part.(type) {
	case *syntax.Lit:
		return cutText(p.Value, func(tail string) syntax.WordPart { return &syntax.Lit{Value: tail} }), true
	case *syntax.SglQuoted:
		if p.Dollar {
			return equalsCut{}, false
		}

		return cutText(p.Value, func(tail string) syntax.WordPart { return &syntax.SglQuoted{Value: tail} }), true
	case *syntax.DblQuoted:
		if p.Dollar {
			return equalsCut{}, false
		}

		c, ok := cutParts(p.Parts)
		if len(c.tail) > 0 {
			c.tail = []syntax.WordPart{&syntax.DblQuoted{Parts: c.tail}}
		}

		return c, ok
	default:
		return equalsCut{}, false
	}
}

// cutText cuts text at its first `=`, giving what follows it to quote to make the tail part. An
// empty remainder makes no part.
func cutText(text string, quote func(string) syntax.WordPart) equalsCut {
	head, tail, found := strings.Cut(text, "=")
	cut := equalsCut{head: head, found: found}
	if tail != "" {
		cut.tail = []syntax.WordPart{quote(tail)}
	}

	return cut
}
