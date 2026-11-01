//go:build unix

package guard

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/jeffzi/ils/internal/github"
)

// judgeForge judges an ils or gh invocation: the commands that land a change or publish a release
// ask when they are the whole command and refuse in any other form, and the gh calls that write
// to GitHub, change what a gh word runs, or print the auth token are refused.
func judgeForge(ctx context.Context, _ Caller, inv Invocation) (Verdict, error) {
	switch inv.Tool {
	case ILS:
		return judgeILS(inv), nil
	case GH:
		return judgeGH(ctx, inv)
	default:
		return Verdict{Outcome: Allow}, nil
	}
}

// judgeILS asks for `ils merge` and `ils release` and allows `ils push` and `ils branch`, each only
// when it is the whole command; every other ils command is free in any form. The subcommand is
// the first command word as Cobra finds it among ilsSwitches.
func judgeILS(inv Invocation) Verdict {
	args := inv.Words[1:]
	at := commandWords(args, ilsSwitches())
	if len(at) == 0 {
		return Verdict{Outcome: Allow}
	}

	sub := args[at[0]]
	if !sub.Literal {
		return refuse("ils command that is not a literal: run ils with its command written out")
	}

	switch sub.Text {
	case "merge":
		return aloneAndBare(inv, "ils merge", Verdict{Outcome: Ask, Reason: "ils merge lands the pull request on the trunk"})
	case "release":
		return aloneAndBare(inv, "ils release", Verdict{Outcome: Ask, Reason: "ils release tags and publishes a release"})
	case "push", "branch":
		return aloneAndBare(inv, "ils "+sub.Text, Verdict{Outcome: Allow})
	default:
		return Verdict{Outcome: Allow}
	}
}

// aloneAndBare returns whole when inv is the whole command, its command word the bare name, and
// otherwise refuses, so that the user approves, and the hook reads, exactly the command that runs.
func aloneAndBare(inv Invocation, name string, whole Verdict) Verdict {
	if inv.Whole {
		return whole
	}

	return refuse(fmt.Sprintf("%s runs only on its own, by its bare name: run `%s` as the whole command, "+
		"with no wrapper, list, pipe, redirect, `&`, shell string, path, quote, or backslash", name, name))
}

// ilsSwitches are the flags the root command of ils knows and that take no value. A test of
// package cli fails when the root gains a switch this list lacks.
func ilsSwitches() map[string]bool {
	return map[string]bool{
		"--debug": true, "-d": true, "--help": true, "-h": true, "--json": true, "--no-color": true, "--version": true,
	}
}

// ghSwitches are the flags gh and its command groups know and that take no value.
func ghSwitches() map[string]bool {
	return map[string]bool{"--help": true}
}

// ghCommands are the top-level commands of the oldest gh that ils accepts. A newer command is
// left out on purpose: on an older gh, an extension of that name would run in its place.
func ghCommands() map[string]bool {
	names := []string{
		"alias", "api", "attestation", "auth", "browse", "cache", "codespace", "completion", "config",
		"extension", "gist", "gpg-key", "help", "issue", "label", "org", "pr", "preview", "project",
		"release", "repo", "ruleset", "run", "search", "secret", "ssh-key", "status", "variable",
		"version", "workflow",
	}
	commands := make(map[string]bool, len(names))
	for _, name := range names {
		commands[name] = true
	}

	return commands
}

// commandWords returns the positions in args of the words Cobra reads as command words while it
// looks for the command to run, stopping at `--`. Cobra takes a flag it does not know for one that
// takes a value, so the word after a `--name` or a `-x` with no `=` is that flag's value unless the
// flag is one of switches. A word that is not a literal counts as a command word, since it may be
// one.
func commandWords(args []Word, switches map[string]bool) []int {
	var at []int
	for i := 0; i < len(args); i++ {
		w := args[i]
		switch {
		case !w.Literal:
			at = append(at, i)
		case w.Text == "--":
			return at
		case takesNextWord(w.Text) && !switches[w.Text]:
			i++
		case !strings.HasPrefix(w.Text, "-"):
			at = append(at, i)
		}
	}

	return at
}

// takesNextWord reports whether Cobra may read the word after the flag text as its value: a long
// flag or a lone shorthand, with no value attached.
func takesNextWord(text string) bool {
	if strings.Contains(text, "=") {
		return false
	}

	return strings.HasPrefix(text, "--") && len(text) > 2 || len(text) == 2 && text[0] == '-' && text[1] != '-'
}

// judgeGH judges a gh invocation by its command. A first command word that is not one of gh's
// commands is resolved through `gh alias list`, and the expansion is judged in its place.
func judgeGH(ctx context.Context, inv Invocation) (Verdict, error) {
	args := inv.Words[1:]
	at := commandWords(args, ghSwitches())
	if len(at) == 0 {
		return Verdict{Outcome: Allow}, nil
	}

	first := args[at[0]]
	if !first.Literal {
		return refuse("gh command that is not a literal: run gh with its command written out"), nil
	}

	if !ghCommands()[first.Text] {
		return judgeGHAlias(ctx, inv, args, at[0])
	}

	return judgeGHCommand(inv, args, at), nil
}

// judgeGHAlias judges the gh invocation inv, whose words after gh are args, by the expansion of
// the alias at position alias of args.
func judgeGHAlias(ctx context.Context, inv Invocation, args []Word, alias int) (Verdict, error) {
	name := args[alias].Text
	expanded, refusal, err := expandGHAlias(ctx, name, args[alias+1:])
	if err != nil {
		return Verdict{}, err
	}

	if refusal != "" {
		return refuse(refusal), nil
	}

	args = slices.Concat(args[:alias], expanded)
	at := commandWords(args, ghSwitches())
	if len(at) == 0 || !args[at[0]].Literal || !ghCommands()[args[at[0]].Text] {
		return refuse(fmt.Sprintf("gh alias %s expands to no gh command the guard can judge", name)), nil
	}

	return judgeGHCommand(inv, args, at), nil
}

// expandGHAlias returns the words gh runs for the alias name given rest, the words after it, as gh
// expands them: each word of rest, in order, fills its `$N` while the expansion still holds a `$`,
// and every word after that is appended to the expansion. It returns a refusal instead for a word
// that is no alias, a shell alias, a word filling a `$N` that is not a literal, and an expansion
// the guard cannot read, such as one left holding a `$N`.
func expandGHAlias(ctx context.Context, name string, rest []Word) (words []Word, refusal string, err error) {
	expansion, found, err := ghAlias(ctx, name)
	if err != nil {
		return nil, "", err
	}

	if !found {
		return nil, fmt.Sprintf("gh %s is neither a gh command nor a gh alias: the guard judges only gh's own commands", name), nil
	}

	if strings.HasPrefix(expansion, "!") {
		return nil, fmt.Sprintf("gh alias %s runs a shell command, which the guard cannot judge", name), nil
	}

	var appended []Word
	for i, w := range rest {
		if !strings.Contains(expansion, "$") {
			appended = append(appended, w)

			continue
		}

		if !w.Literal {
			return nil, fmt.Sprintf("gh alias %s is given an argument that is not a literal", name), nil
		}

		expansion = strings.ReplaceAll(expansion, "$"+strconv.Itoa(i+1), w.Text)
	}

	split, ok := splitAlias(strings.TrimSpace(expansion))
	if !ok || placeholder.MatchString(expansion) {
		return nil, fmt.Sprintf("gh alias %s has an expansion the guard cannot read: %s", name, expansion), nil
	}

	for _, text := range split {
		words = append(words, Word{Text: text, Literal: true})
	}

	return append(words, appended...), "", nil
}

// placeholder matches a `$N` of a gh alias left unreplaced, which makes gh refuse the alias.
var placeholder = regexp.MustCompile(`\$\d`)

// ghAlias returns what `gh alias list` says the alias name expands to, and false when gh holds no
// such alias.
func ghAlias(ctx context.Context, name string) (expansion string, found bool, err error) {
	out, err := github.Runner{}.Run(ctx, "alias", "list")
	if err != nil {
		return "", false, fmt.Errorf("read gh alias list: %w", err)
	}

	var aliases map[string]string
	if err := yaml.Unmarshal([]byte(out.Stdout), &aliases); err != nil {
		return "", false, fmt.Errorf("read gh alias list: %w", err)
	}

	expansion, found = aliases[name]

	return expansion, found, nil
}

// judgeGHCommand judges the gh command at the positions at of args, its command words.
func judgeGHCommand(inv Invocation, args []Word, at []int) Verdict {
	group := args[at[0]].Text
	rest := args[at[0]+1:]
	if group == "api" {
		return judgeAPI(rest)
	}

	if !slices.Contains([]string{"alias", "auth", "config", "pr", "release", "repo"}, group) || len(at) < 2 {
		return Verdict{Outcome: Allow}
	}

	sub := args[at[1]]
	if !sub.Literal {
		return refuse(fmt.Sprintf("gh %s command that is not a literal: run gh with its command written out", group))
	}

	return judgeGHSubcommand(inv, "gh "+group+" "+sub.Text, rest)
}

// ghGuardedFlag is a flag that turns a gh subcommand into a refusal, with the shorthands of that
// subcommand that take a value.
type ghGuardedFlag struct {
	long   string
	reason string
	valued []byte
	short  byte
}

// ghGuardedFlags are the gh subcommands that a flag turns into a refusal, by name.
func ghGuardedFlags() map[string]ghGuardedFlag {
	return map[string]ghGuardedFlag{
		"gh pr merge": {
			long: "--admin", reason: "gh pr merge --admin merges past the trunk's protection rules, which no agent may bypass",
		},
		"gh pr checkout": {
			long: "--force", short: 'f', valued: []byte{'b', 'R'},
			reason: "gh pr checkout --force resets the local branch to the pull request, discarding its commits",
		},
		"gh repo create": {
			long: "--push", reason: "gh repo create --push pushes a branch: `ils push` is the one way an agent pushes a branch",
		},
		"gh auth status": {
			long: "--show-token", short: 't', valued: []byte{'h'},
			reason: "gh auth status with -t or --show-token prints the auth token",
		},
	}
}

// judgeGHSubcommand judges the gh subcommand name, such as `gh pr merge`, given its words rest.
func judgeGHSubcommand(inv Invocation, name string, rest []Word) Verdict {
	if flag, ok := ghGuardedFlags()[name]; ok {
		if set, known := ghFlag(rest, flag.short, flag.long, flag.valued); set || !known {
			return refuse(flag.reason)
		}
	}

	switch name {
	case "gh pr merge":
		return aloneAndBare(inv, name, Verdict{Outcome: Ask, Reason: "gh pr merge lands the pull request on the trunk; `ils merge` does the same once its checks pass"})
	case "gh release create":
		return aloneAndBare(inv, name, Verdict{Outcome: Ask, Reason: "gh release create publishes a release; `ils release` tags and publishes one once the release commit's checks pass"})
	case "gh config get":
		if slices.ContainsFunc(rest, func(w Word) bool { return !w.Literal || w.Text == "oauth_token" }) {
			return refuse("gh config get oauth_token prints the auth token")
		}
	case "gh auth token":
		return refuse("gh auth token prints the auth token")
	case "gh alias set", "gh alias import", "gh alias delete":
		return refuse(name + " changes what a gh word runs")
	case "gh repo sync", "gh repo delete", "gh repo edit", "gh release delete", "gh release edit", "gh release upload",
		"gh release delete-asset":
		return refuse(name + " writes to GitHub, which the user does by hand")
	}

	return Verdict{Outcome: Allow}
}

// judgeAPI refuses a `gh api` call that may write: a method other than GET, a method that is not a
// literal, or, with no method given, a field or an input, which make gh send a POST.
func judgeAPI(rest []Word) Verdict {
	method, given := apiMethod(rest)
	switch {
	case given && !method.Literal:
		return refuse("gh api with a method that is not a literal may write to GitHub")
	case given && !strings.EqualFold(method.Text, "GET"):
		return refuse(fmt.Sprintf("gh api with method %s writes to GitHub, which the user does by hand", method.Text))
	case given:
		return Verdict{Outcome: Allow}
	}

	for _, flag := range []struct {
		long  string
		short byte
	}{{"--raw-field", 'f'}, {"--field", 'F'}, {"--input", 0}} {
		if set, known := ghFlag(rest, flag.short, flag.long, apiValued()); set || !known {
			return refuse("gh api with a field or an input and no explicit GET sends a POST, which writes to GitHub")
		}
	}

	return Verdict{Outcome: Allow}
}

// apiValued returns the shorthands of `gh api` that take a value.
func apiValued() []byte {
	return []byte{'X', 'f', 'F', 'H', 'p', 'q', 't'}
}

// apiMethod returns the last method rest gives `gh api` with -X or --method, and false when it
// gives none. A word that is not a literal and may be the flag gives a method that is not a
// literal.
func apiMethod(rest []Word) (method Word, given bool) {
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if w.Literal && w.Text == "--" {
			break
		}

		next := Word{}
		if i+1 < len(rest) {
			next = rest[i+1]
		}

		if value, ok, takesNext := methodFlag(w, next); ok {
			method, given = value, true
			if takesNext {
				i++
			}
		}
	}

	return method, given
}

// methodFlag returns the method w gives `gh api` when it is -X or --method, reading next as the
// value when the flag takes the next word. A word that is not a literal and may be the flag gives
// a method that is not a literal.
func methodFlag(w, next Word) (method Word, ok, takesNext bool) {
	if value, attached := strings.CutPrefix(w.Text, "--method="); attached {
		return Word{Text: value, Literal: w.Literal}, true, false
	}

	if w.Literal && w.Text == "--method" {
		return next, true, true
	}

	if value, found, attached := shortValue(w, 'X', apiValued()); found {
		if attached {
			return value, true, false
		}

		return next, true, true
	}

	if mayBeFlag(w, 'X', "--method") {
		return Word{}, true, false
	}

	return Word{}, false, false
}

// shortValue returns the value of the shorthand letter in the bundle w, reading the bundle's
// letters up to the first of valued, the shorthands that take a value. attached is false when the
// value is the next word.
func shortValue(w Word, letter byte, valued []byte) (value Word, ok, attached bool) {
	t := w.Text
	if len(t) < 2 || t[0] != '-' || t[1] == '-' {
		return Word{}, false, false
	}

	for i := 1; i < len(t); i++ {
		if t[i] == letter {
			value = Word{Text: t[i+1:], Literal: w.Literal}
			return value, true, i+1 < len(t) || !w.Literal
		}

		if slices.Contains(valued, t[i]) {
			break
		}
	}

	return Word{}, false, false
}

// ghFlag reports whether rest sets the flag with the shorthand letter short (0 when it has none)
// or the long name, read as pflag reads it, up to `--`. known is false when a word that is not a
// literal may be the flag.
func ghFlag(rest []Word, short byte, long string, valued []byte) (set, known bool) {
	known = true
	for _, w := range rest {
		if w.Literal && w.Text == "--" {
			break
		}

		switch {
		case w.Literal && (w.Text == long || strings.HasPrefix(w.Text, long+"=")):
			return true, true
		case short != 0 && w.Literal:
			if _, ok, _ := shortValue(w, short, valued); ok {
				return true, true
			}
		}

		if mayBeFlag(w, short, long) {
			known = false
		}
	}

	return false, known
}

// mayBeFlag reports whether w is a word that is not a literal whose literal prefix may still grow
// into the flag with the shorthand letter short (0 when it has none) or the long name.
func mayBeFlag(w Word, short byte, long string) bool {
	if w.Literal {
		return false
	}

	if strings.HasPrefix(long, w.Text) {
		return true
	}

	return short != 0 && strings.HasPrefix(w.Text, "-") && !strings.HasPrefix(w.Text, "--")
}
