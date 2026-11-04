//go:build unix

package guard

import "strings"

// allexportAfter returns whether `set` given args, run by the shell named shell, leaves allexport
// on, given whether it was on before. An option the guard cannot read may turn it on.
//
// zsh's `o` takes the rest of its bundle as the option name when the rest is non-empty, and the
// next word otherwise; bash's `o` always takes the next word. An unknown shell, empty, may be zsh
// or a shell that reads `set` as bash does, so allexport is on after it when either reading leaves
// it on.
func allexportAfter(shell string, args []Word, on bool) bool {
	if shell == "" {
		return allexportAfterIn("zsh", args, on) || allexportAfterIn("bash", args, on)
	}

	return allexportAfterIn(shell, args, on)
}

// allexportAfterIn returns whether `set` given args, run by the shell named shell, which is known,
// leaves allexport on, given whether it was on before.
func allexportAfterIn(shell string, args []Word, on bool) bool {
	r := allexportReader{shell: shell, on: on}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !arg.Literal {
			return true
		}

		if len(arg.Text) < 2 || arg.Text == "--" || !startsWithSign(arg.Text) {
			return r.on
		}

		letters, enable, taken := arg.Text[1:], arg.Text[0] == '-', 0
		for j, letter := range letters {
			takes, ends := r.letter(letter, letters[j+1:], enable, args[min(i+taken+1, len(args)):])
			taken += takes
			if ends {
				break
			}
		}

		i += taken
	}

	return r.on
}

// allexportReader follows allexport through the bundles of short options of `set` or of a shell,
// in the shell named shell; on is whether allexport is on so far.
type allexportReader struct {
	shell string
	on    bool
}

// letter reads letter, one letter of a bundle of short options, which rest follows in its bundle,
// and next, the words after the values the bundle has taken. It returns how many of next it takes,
// and whether it takes rest. The bundle starts with `-` when enable is true, `+` otherwise.
//
// `a` sets allexport. Each `o` of bash, sh and dash takes the next word as an option name; the `o`
// of zsh takes rest when there is any, and the next word otherwise. Any other letter takes nothing
// here.
func (r *allexportReader) letter(letter rune, rest string, enable bool, next []Word) (int, bool) {
	switch {
	case letter == 'a':
		r.on = enable
	case letter != 'o':
	case r.shell == "zsh" && rest != "":
		r.on = setsAllexport(r.shell, Word{Text: rest, Literal: true}, enable, r.on)

		return 0, true
	case len(next) > 0:
		r.on = setsAllexport(r.shell, next[0], enable, r.on)

		return 1, false
	default:
		return 1, false
	}

	return 0, false
}

// setsAllexport returns whether `set -o name`, or `set +o name` when enable is false, run by the
// shell named shell, leaves allexport on, given whether it was on before. zsh reads the name as
// setopt does (zshOptionSetting); bash, sh and dash match `allexport` exactly and reject any other
// spelling, leaving allexport as it was.
func setsAllexport(shell string, name Word, enable, on bool) bool {
	switch {
	case !name.Literal:
		return on || enable
	case shell == "zsh":
		return zshOptionSetting(name, enable, on)
	case name.Text == "allexport":
		return enable
	default:
		return on
	}
}

// allexportAfterZshOptions returns whether zsh's `setopt` given args, or `unsetopt` when enable is
// false, leaves allexport on, given whether it was on before. zsh ignores case and underscores in an
// option name, and `noallexport` is the reverse of `allexport`. The pattern form `-m`, and an
// argument the guard cannot read, may turn it on.
//
// Only the leading arguments that start with `-` or `+` are option bundles, and `+` reverses `-`;
// the first argument that does not ends them, as do a lone `-` or `+` and a `-` inside a bundle, and
// every argument after that is an option name.
func allexportAfterZshOptions(enable bool, args []Word, on bool) bool {
	i := 0
	for i < len(args) {
		arg := args[i]
		if !arg.Literal {
			return true
		}

		if !startsWithSign(arg.Text) {
			break
		}

		read := readZshOptionBundle(arg, args[i+1:], enable == (arg.Text[0] == '-'), on)
		if read.pattern {
			return true
		}

		on = read.on
		i += 1 + read.taken
		if read.ended {
			break
		}
	}

	for _, arg := range args[i:] {
		on = zshOptionSetting(arg, enable, on)
	}

	return on
}

// zshOptionBundle is what one `-` or `+` bundle of `setopt` or `unsetopt` did.
type zshOptionBundle struct {
	// on reports whether allexport is on after the bundle.
	on bool
	// pattern reports whether the bundle holds `m`, which makes the option names patterns.
	pattern bool
	// ended reports whether the bundle ends the options, so the arguments after it are names.
	ended bool
	// taken counts the words after the bundle it read as an option name.
	taken int
}

// readZshOptionBundle reads the option bundle arg, followed by the words next, that turns on the
// options it names when set and turns them off otherwise, given whether allexport was on before. `a`
// is allexport, and `o` takes the rest of the bundle as an option name when the rest is non-empty,
// else the next word, and ends the bundle.
func readZshOptionBundle(arg Word, next []Word, set, on bool) zshOptionBundle {
	bundle := arg.Text[1:]
	if bundle == "" {
		return zshOptionBundle{on: on, ended: true}
	}

	for j, letter := range bundle {
		switch letter {
		case '-':
			return zshOptionBundle{on: on, ended: true}
		case 'm':
			return zshOptionBundle{on: true, pattern: true}
		case 'a':
			on = set
		case 'o':
			if rest := bundle[j+1:]; rest != "" {
				name := Word{Text: rest, Literal: arg.Literal}

				return zshOptionBundle{on: zshOptionSetting(name, set, on)}
			}

			if len(next) == 0 {
				return zshOptionBundle{on: on}
			}

			return zshOptionBundle{on: zshOptionSetting(next[0], set, on), taken: 1}
		}
	}

	return zshOptionBundle{on: on}
}

// zshOptionSetting returns whether the zsh option name, set when enable and unset otherwise, leaves
// allexport on, given whether it was on before. A name the guard cannot read may be allexport.
func zshOptionSetting(name Word, enable, on bool) bool {
	if !name.Literal {
		return true
	}

	switch zshOptionName(name.Text) {
	case "allexport":
		return enable
	case "noallexport":
		return !enable
	default:
		return on
	}
}

// zshOptionName returns name, the name of an option of zsh, the way zsh compares it: case and
// underscores ignored. Dashes are kept: the caller first turns the dashes of a `--name` argument
// into underscores, as zsh reads them there.
func zshOptionName(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", ""))
}
