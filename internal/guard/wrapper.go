//go:build unix

package guard

import (
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

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
