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

	"github.com/jeffzi/ils/internal/commandline"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/landing"
)

// ---------------------------------------------------------------------------
// Judging ils commands
// ---------------------------------------------------------------------------

// judgeForge judges an ils or gh invocation: the commands that land a change or publish a release
// ask when they are the whole command and refuse in any other form, and the gh calls that write
// to GitHub, change what a gh word runs, or print the auth token are refused.
func judgeForge(ctx context.Context, caller Caller, inv Invocation) (Verdict, error) {
	switch inv.Tool {
	case ILS:
		return judgeILS(inv), nil
	case GH:
		return judgeGH(ctx, caller, inv)
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
		return notLiteralCommand("ils", "ils")
	}

	name := "ils " + sub.Text
	switch sub.Text {
	case landing.MergeCommand:
		return aloneAndBare(inv, name, Verdict{Outcome: Ask, Reason: "ils merge lands the pull request on the trunk"})
	case landing.ReleaseCommand:
		return aloneAndBare(inv, name, Verdict{Outcome: Ask, Reason: "ils release tags and publishes a release"})
	case landing.PushCommand, landing.BranchCommand:
		return aloneAndBare(inv, name, Verdict{Outcome: Allow})
	default:
		return Verdict{Outcome: Allow}
	}
}

// notLiteralCommand refuses the invocation of tool whose command word, which command names as the
// refusal shows it, is not a literal.
func notLiteralCommand(command, tool string) Verdict {
	return refuse(fmt.Sprintf("%s command that is not a literal: run %s with its command written out", command, tool))
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
// package cli fails when the root gains a switch commandline.RootSwitches lacks.
func ilsSwitches() map[string]bool {
	return setOf(commandline.RootSwitches()...)
}

// ---------------------------------------------------------------------------
// Finding the gh command
// ---------------------------------------------------------------------------

// ghSwitches are the flags gh and its command groups know and that take no value.
func ghSwitches() map[string]bool {
	return map[string]bool{"--help": true}
}

// ghCommands are the top-level commands of the oldest gh that ils accepts. A newer command is
// left out on purpose: on an older gh, an extension of that name would run in its place.
func ghCommands() map[string]bool {
	return setOf(
		"alias", "api", "attestation", "auth", "browse", "cache", "codespace", "completion", "config",
		"extension", "gist", "gpg-key", "help", "issue", "label", "org", "pr", "preview", "project",
		"release", "repo", "ruleset", "run", "search", "secret", "ssh-key", "status", "variable",
		"version", "workflow",
	)
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

	long := strings.HasPrefix(text, "--") && len(text) > 2
	shorthand := len(text) == 2 && text[0] == '-' && text[1] != '-'

	return long || shorthand
}

// judgeGH judges a gh invocation by its command. A first command word that is not one of gh's
// commands is resolved through `gh alias list`, run by caller's gh runner, and the expansion is
// judged in its place.
func judgeGH(ctx context.Context, caller Caller, inv Invocation) (Verdict, error) {
	args := inv.Words[1:]
	at := commandWords(args, ghSwitches())
	if len(at) == 0 {
		return Verdict{Outcome: Allow}, nil
	}

	first := args[at[0]]
	if !first.Literal {
		return notLiteralCommand("gh", "gh"), nil
	}

	if !ghCommands()[first.Text] {
		return judgeGHAlias(ctx, caller, inv, at[0])
	}

	return judgeGHCommand(inv, args, at), nil
}

// ---------------------------------------------------------------------------
// gh aliases
// ---------------------------------------------------------------------------

// judgeGHAlias judges the gh invocation inv by the expansion of the alias at position alias of its
// words after gh, read by caller's gh runner from the config directory inv's gh reads. An
// invocation whose config directory the guard cannot tell is refused.
func judgeGHAlias(ctx context.Context, caller Caller, inv Invocation, alias int) (Verdict, error) {
	args := inv.Words[1:]
	name := args[alias].Text
	runner, unknown := ghConfigRunner(caller.GH, inv.Dir, inv.ghConfig)
	if unknown != "" {
		return refuse(fmt.Sprintf(
			"gh %s may be a gh alias, which the guard cannot read: it cannot tell the %s gh runs with", name, unknown)), nil
	}

	expanded, refusal, err := expandGHAlias(ctx, runner, name, args[alias+1:])
	if err != nil {
		return Verdict{}, err
	}

	if refusal != "" {
		return refuse(refusal), nil
	}

	args = slices.Concat(args[:alias], expanded)
	at := commandWords(args, ghSwitches())
	if len(at) == 0 || !inTable(ghCommands(), args[at[0]]) {
		return refuse(fmt.Sprintf("gh alias %s expands to no gh command the guard can judge", name)), nil
	}

	return judgeGHCommand(inv, args, at), nil
}

// expandGHAlias returns the words gh runs for the alias name given rest, the words after it, as gh
// expands them: each word of rest, in order, fills its `$N` while the expansion still holds a `$`,
// and every word after that is appended to the expansion. It returns a refusal instead for a word
// that is no alias, a shell alias, a word filling a `$N` that is not a literal, and an expansion
// the guard cannot read, such as one left holding a `$N`.
func expandGHAlias(ctx context.Context, runner github.Runner, name string, rest []Word) (words []Word, refusal string, err error) {
	expansion, found, err := ghAlias(ctx, runner, name)
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

	split, ok := git.SplitCommandLine(strings.TrimSpace(expansion))
	if !ok || placeholder.MatchString(expansion) {
		return nil, fmt.Sprintf("gh alias %s has an expansion the guard cannot read: %s", name, expansion), nil
	}

	return append(literalWords(split), appended...), "", nil
}

// placeholder matches a `$N` of a gh alias left unreplaced, which makes gh refuse the alias.
var placeholder = regexp.MustCompile(`\$\d`)

// ghAlias returns what `gh alias list`, run by runner, says the alias name expands to, and false
// when gh holds no such alias.
func ghAlias(ctx context.Context, runner github.Runner, name string) (expansion string, found bool, err error) {
	out, err := runner.Run(ctx, "alias", "list")
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

// The variables that choose the config directory gh reads: GH_CONFIG_DIR, else
// XDG_CONFIG_HOME/gh, else HOME/.config/gh.
const (
	ghConfigDirVar  = "GH_CONFIG_DIR"
	ghConfigHomeVar = "XDG_CONFIG_HOME"
	ghHomeVar       = "HOME"
)

// ghConfig is what the guard knows of the variables that choose the config directory gh reads,
// and with it the aliases gh expands. A variable neither set nor unknown keeps the value of the
// gh runner's environment.
type ghConfig struct {
	configDir  variable
	configHome variable
	home       variable
}

// or returns the config that either c or other may be: what they agree on, and unknown elsewhere.
func (c ghConfig) or(other ghConfig) ghConfig {
	return ghConfig{
		configDir:  c.configDir.or(other.configDir),
		configHome: c.configHome.or(other.configHome),
		home:       c.home.or(other.home),
	}
}

// ghConfigRunner returns runner with each variable config sets in place of its own, a relative
// path made absolute against dir, where the invocation runs. It returns the name of the first
// variable whose value the guard cannot tell instead: one that is unknown, or a relative path in a
// directory the guard cannot follow.
func ghConfigRunner(runner github.Runner, dir string, config ghConfig) (github.Runner, string) {
	env := runner.Environ()
	for _, named := range []struct {
		name     string
		variable variable
	}{
		{name: ghConfigDirVar, variable: config.configDir},
		{name: ghConfigHomeVar, variable: config.configHome},
		{name: ghHomeVar, variable: config.home},
	} {
		if named.variable.unknown {
			return runner, named.name
		}

		if !named.variable.set {
			continue
		}

		// gh reads an empty value as an unset one, so it is handed over as it is.
		value := named.variable.value
		if value != "" {
			value = absolute(dir, value)
			if value == "" {
				return runner, named.name
			}
		}

		env = append(git.WithoutKeys(env, named.name), named.name+"="+value)
	}

	runner.Env = env

	return runner, ""
}

// ---------------------------------------------------------------------------
// gh commands that write or print secrets
// ---------------------------------------------------------------------------

// judgeGHCommand judges the gh command at the positions at of args, its command words.
func judgeGHCommand(inv Invocation, args []Word, at []int) Verdict {
	group := args[at[0]].Text
	rest := args[at[0]+1:]
	if group == "api" {
		return judgeAPI(rest)
	}

	if !ghGuardedGroups()[group] || len(at) < 2 {
		return Verdict{Outcome: Allow}
	}

	sub := args[at[1]]
	if !sub.Literal {
		return notLiteralCommand("gh "+group, "gh")
	}

	return judgeGHSubcommand(inv, "gh "+group+" "+sub.Text, rest)
}

// ghGuardedGroups are the gh command groups whose subcommands judgeGHSubcommand judges and
// ghGuardedFlags names; a subcommand of any other group is allowed.
func ghGuardedGroups() map[string]bool {
	return setOf("alias", "auth", "config", "pr", "release", "repo")
}

// ghGuardedFlag is a gh flag the guard looks for, with the shorthands of its subcommand that take a
// value. The reason is the refusal a set flag gives; it is empty for a flag whose caller words its
// own refusal.
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
		takesNext := func(w Word) bool { return mayTakeValue(w, flag.valued) }
		if set, known := ghFlag(rest, flag, takesNext); set || !known {
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
	case "gh auth token", "gh auth git-credential":
		return refuse(name + " prints the auth token")
	case "gh alias set", "gh alias import", "gh alias delete":
		return refuse(name + " changes what a gh word runs")
	case "gh repo sync", "gh repo delete", "gh repo edit", "gh release delete", "gh release edit", "gh release upload",
		"gh release delete-asset":
		return refuse(name + " writes to GitHub, which the user does by hand")
	}

	return Verdict{Outcome: Allow}
}

// ---------------------------------------------------------------------------
// gh api and flag reading
// ---------------------------------------------------------------------------

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

	for _, flag := range []ghGuardedFlag{
		{long: "--raw-field", short: 'f', valued: apiValued()},
		{long: "--field", short: 'F', valued: apiValued()},
		{long: "--input", valued: apiValued()},
	} {
		if set, known := ghFlag(rest, flag, takesValueWord); set || !known {
			return refuse("gh api with a field or an input and no explicit GET sends a POST, which writes to GitHub")
		}
	}

	return Verdict{Outcome: Allow}
}

// apiValued returns the shorthands of `gh api` that take a value.
func apiValued() []byte {
	return []byte{'X', 'f', 'F', 'H', 'p', 'q', 't'}
}

// apiValuedLong returns the long flags of `gh api` that take a value, as the next word when none
// is attached with `=`.
func apiValuedLong() []string {
	return []string{"--raw-field", "--field", "--header", "--preview", "--jq", "--template", "--input", "--hostname", "--cache"}
}

// takesValueWord reports whether the literal word w is a flag of `gh api` other than the method's
// that takes the next word as its value: a long flag in apiValuedLong with no `=`, or a bundle of
// shorthands whose first valued letter ends it.
func takesValueWord(w Word) bool {
	t := w.Text
	switch {
	case !w.Literal:
		return false
	case slices.Contains(apiValuedLong(), t):
		return true
	case !isShortBundle(t):
		return false
	}

	for i := 1; i < len(t); i++ {
		if slices.Contains(apiValued(), t[i]) {
			return i == len(t)-1
		}
	}

	return false
}

// isShortBundle reports whether the text t is a bundle of shorthands: a single dash and at least
// one letter.
func isShortBundle(t string) bool {
	return len(t) >= 2 && t[0] == '-' && t[1] != '-'
}

// apiMethod returns the last method rest gives `gh api` with -X or --method, and false when it
// gives none. A word that is not a literal and may be the flag gives a method that is not a
// literal. The word after a flag that takes a value is that value, never a method flag; a `--` after
// a word that is not a literal and may be such a flag is skipped as its value too.
func apiMethod(rest []Word) (method Word, given bool) {
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if w.Literal && w.Text == "--" {
			break
		}

		dashes := flagBeforeDashes(rest, i)
		if value, ok, takesNext := methodFlag(w, wordAfter(rest, i)); ok {
			method, given = value, true
			if takesNext || dashes {
				i++
			}

			continue
		}

		if takesValueWord(w) || dashes {
			i++
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
	if !isShortBundle(t) {
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

// ghFlag reports whether rest sets flag, by its shorthand letter (0 when it has none) or its long
// name, read as pflag reads it, up to `--`. A `--` that follows a word for which takesNext holds is
// that flag's value, as pflag reads it, and does not end the words; so does a `--` that follows a
// word that is not a literal and may be such a flag (see mayBeValuedFlag). known is false when a
// word that is not a literal may be the flag.
func ghFlag(rest []Word, flag ghGuardedFlag, takesNext func(Word) bool) (set, known bool) {
	known = true
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if w.Literal && w.Text == "--" {
			break
		}

		if setsFlag(w, flag.short, flag.long, flag.valued) {
			return true, true
		}

		if mayBeFlag(w, flag.short, flag.long) {
			known = false
		}

		if dashesFollow(rest, i) && (takesNext(w) || mayBeValuedFlag(w)) {
			i++
		}
	}

	return false, known
}

// wordAfter returns the word after rest[i], and the zero Word when rest[i] is the last.
func wordAfter(rest []Word, i int) Word {
	if i+1 < len(rest) {
		return rest[i+1]
	}

	return Word{}
}

// flagBeforeDashes reports whether rest[i] may be a flag that takes the next word as its value
// and a literal `--` follows it, which pflag then reads as that value.
func flagBeforeDashes(rest []Word, i int) bool {
	return dashesFollow(rest, i) && mayBeValuedFlag(rest[i])
}

// dashesFollow reports whether the word after rest[i] is a literal `--`.
func dashesFollow(rest []Word, i int) bool {
	return i+1 < len(rest) && rest[i+1].Literal && rest[i+1].Text == "--"
}

// mayBeValuedFlag reports whether w is a word that is not a literal whose literal prefix is empty
// or starts with `-`, so that it may expand to a flag that takes the next word as its value. The
// guard reads on past a `--` after such a word, which can only make a refusal more likely.
func mayBeValuedFlag(w Word) bool {
	return !w.Literal && (w.Text == "" || strings.HasPrefix(w.Text, "-"))
}

// setsFlag reports whether the literal word w is the long name, the long name with `=`, or a
// bundle that sets the shorthand letter short (0 when it has none).
func setsFlag(w Word, short byte, long string, valued []byte) bool {
	if !w.Literal {
		return false
	}

	if w.Text == long || strings.HasPrefix(w.Text, long+"=") {
		return true
	}

	if short == 0 {
		return false
	}

	_, ok, _ := shortValue(w, short, valued)

	return ok
}

// mayTakeValue reports whether the literal word w may take the next word as its value in a gh
// command whose shorthands that take a value are valued: a long flag or a lone shorthand with no
// `=`, as takesNextWord reads it, or a bundle of shorthands whose first valued letter ends it. A
// flag that takes none counts too, since the guard does not know every flag of the command.
func mayTakeValue(w Word, valued []byte) bool {
	t := w.Text
	switch {
	case !w.Literal:
		return false
	case takesNextWord(t):
		return true
	case !isShortBundle(t):
		return false
	}

	for i := 1; i < len(t); i++ {
		if slices.Contains(valued, t[i]) {
			return i == len(t)-1
		}
	}

	return false
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
