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
	shorthand := len(text) == 2 && isShortBundle(text)

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
	valued string
	short  byte
}

// ghGuardedFlags are the gh subcommands that a flag turns into a refusal, by name.
func ghGuardedFlags() map[string]ghGuardedFlag {
	return map[string]ghGuardedFlag{
		"gh pr merge": {
			long: "--admin", reason: "gh pr merge --admin merges past the trunk's protection rules, which no agent may bypass",
		},
		"gh pr checkout": {
			long: "--force", short: 'f', valued: "bR",
			reason: "gh pr checkout --force resets the local branch to the pull request, discarding its commits",
		},
		"gh repo create": {
			long: "--push", reason: "gh repo create --push pushes a branch: `ils push` is the one way an agent pushes a branch",
		},
		"gh auth status": {
			long: "--show-token", short: 't', valued: "h",
			reason: "gh auth status with -t or --show-token prints the auth token",
		},
	}
}

// judgeGHSubcommand judges the gh subcommand name, such as `gh pr merge`, given its words rest.
func judgeGHSubcommand(inv Invocation, name string, rest []Word) Verdict {
	if flag, ok := ghGuardedFlags()[name]; ok {
		takesNext := func(w Word) bool { return mayTakeValue(w, flag.valued) }
		switch ghFlag(rest, flag, takesNext) {
		case flagUnset:
		case flagValueNotLiteral:
			return refuse(fmt.Sprintf("%s; the guard reads %s with a value that is not a literal as set: write the value as a literal, or leave the flag out",
				flag.reason, flag.long))
		default:
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
// literal, or, with no method given, a field or an input, which make gh send a POST. A POST to the
// graphql endpoint is judged by its document instead (see judgeGraphQL).
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

	if endpoint, ok := apiEndpoint(rest); ok && endpoint.Literal && endpoint.Text == "graphql" {
		return judgeGraphQL(rest)
	}

	for _, flag := range append(apiFields(), apiInput()) {
		if ghFlag(rest, flag, takesValueWord) != flagUnset {
			return refuse("gh api with a field or an input and no explicit GET sends a POST, which writes to GitHub")
		}
	}

	return Verdict{Outcome: Allow}
}

// apiFields returns the field flags of `gh api`: --raw-field, whose value gh sends as a string, then
// --field, which reads a value starting with `@` from a file.
func apiFields() []ghGuardedFlag {
	return []ghGuardedFlag{
		{long: "--raw-field", short: 'f', valued: apiValued()},
		{long: "--field", short: 'F', valued: apiValued()},
	}
}

// apiInput returns the flag that gives `gh api` its request body.
func apiInput() ghGuardedFlag {
	return ghGuardedFlag{long: "--input", valued: apiValued()}
}

// apiEndpoint returns the endpoint of `gh api`: its first word that is neither a flag nor a flag's
// value. ok is false when there is none, or a word that is not a literal stands before it.
func apiEndpoint(rest []Word) (endpoint Word, ok bool) {
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		switch {
		case !w.Literal:
			return Word{}, false
		case isEndOfFlags(w):
			return wordAfter(rest, i), i+1 < len(rest)
		case takesValueWord(w):
			i++
		case !strings.HasPrefix(w.Text, "-"):
			return w, true
		}
	}

	return Word{}, false
}

// queryField is the field of `gh api graphql` that holds the document gh sends.
const queryField = "query"

const unreadableGraphQL = "gh api graphql with a query the guard cannot read may send a mutation, which writes to GitHub: " +
	"pass the query inline, as -f query='{ … }'"

// judgeGraphQL judges a POST to the graphql endpoint by its `query` fields, the documents gh sends:
// it refuses a document holding a mutation, and one the guard cannot read, which a body from
// --input, a field read from a file, or a value that is not a literal gives. A call with no field
// and no input sends no document and is allowed. Fields with no `query` among them, and a word that
// is not a literal and may be a field flag, are refused as unreadable too.
func judgeGraphQL(rest []Word) Verdict {
	if ghFlag(rest, apiInput(), takesValueWord) != flagUnset {
		return refuse(unreadableGraphQL)
	}

	var documents []string
	fields := 0
	for _, flag := range apiFields() {
		values, known := ghFlagValues(rest, flag, takesValueWord)
		if !known {
			return refuse(unreadableGraphQL)
		}

		fields += len(values)
		for _, v := range values {
			name, document, named := strings.Cut(v.Text, "=")
			switch {
			case !v.Literal && !named && strings.HasPrefix(queryField, name):
				return refuse(unreadableGraphQL)
			case name != queryField || !named:
				continue
			case !v.Literal || flag.short == 'F' && strings.HasPrefix(document, "@"):
				return refuse(unreadableGraphQL)
			}

			documents = append(documents, document)
		}
	}

	switch {
	case fields > 0 && len(documents) == 0:
		return refuse(unreadableGraphQL)
	case slices.ContainsFunc(documents, holdsMutation):
		return refuse("gh api graphql with a mutation writes to GitHub, which the user does by hand")
	default:
		return Verdict{Outcome: Allow}
	}
}

// ghFlagValues returns the value of each flag rest gives: attached after `=` or in a bundle of
// shorthands, or else the next word. Unlike ghFlag, a word for which takesNext holds always takes
// the next word as its value, which is then never read as a flag. known is false when a word that
// is not a literal may be the flag.
func ghFlagValues(rest []Word, flag ghGuardedFlag, takesNext func(Word) bool) (values []Word, known bool) {
	known = true
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if isEndOfFlags(w) {
			break
		}

		if value, ok, attached := flagValue(w, flag); ok {
			if !attached {
				value = wordAfter(rest, i)
				i++
			}

			values = append(values, value)

			continue
		}

		if mayBeFlag(w, flag.short, flag.long) {
			known = false
		}

		if takesNext(w) || dashesFollow(rest, i) && mayBeValuedFlag(w) {
			i++
		}
	}

	return values, known
}

// flagValue returns the value w gives flag: after its long name and `=`, or after its shorthand in
// a bundle. ok is false when w does not give flag; attached is false when the value is the next
// word. A value in a word that is not a literal is not a literal either.
func flagValue(w Word, flag ghGuardedFlag) (value Word, ok, attached bool) {
	if text, cut := strings.CutPrefix(w.Text, flag.long+"="); cut {
		return Word{Text: text, Literal: w.Literal}, true, true
	}

	if w.Literal && w.Text == flag.long {
		return Word{}, true, false
	}

	if flag.short == 0 {
		return Word{}, false, false
	}

	return shortValue(w, flag.short, flag.valued)
}

// holdsMutation reports whether the GraphQL document holds a mutation: the keyword `mutation` that
// starts an operation, outside any selection set, argument list, string, or comment.
func holdsMutation(document string) bool {
	depth := 0
	for i := 0; i < len(document); i++ {
		c := document[i]
		switch {
		case c == '#':
			i = skipPast(document, i+1, "\n")
		case strings.HasPrefix(document[i:], `"""`):
			i = skipPast(document, i+3, `"""`)
		case c == '"':
			i = skipString(document, i)
		case c == '{' || c == '(':
			depth++
		case c == '}' || c == ')':
			depth--
		case isNameStart(c):
			end := i + 1
			for end < len(document) && (isNameStart(document[end]) || '0' <= document[end] && document[end] <= '9') {
				end++
			}

			if depth == 0 && document[i:end] == "mutation" {
				return true
			}

			i = end - 1
		}
	}

	return false
}

// skipPast returns the position in document of the last byte of the first end at or after from,
// or of the document's last byte when none follows.
func skipPast(document string, from int, end string) int {
	at := strings.Index(document[min(from, len(document)):], end)
	if at < 0 {
		return len(document) - 1
	}

	return from + at + len(end) - 1
}

// skipString returns the position in document of the quote that closes the string opened at i,
// past any escaped quote, or of the document's last byte when none closes it.
func skipString(document string, i int) int {
	for j := i + 1; j < len(document); j++ {
		switch document[j] {
		case '\\':
			j++
		case '"':
			return j
		}
	}

	return len(document) - 1
}

func isNameStart(c byte) bool {
	return c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// apiValued returns the shorthands of `gh api` that take a value.
func apiValued() string {
	// cspell:disable-next-line -- a set of option letters, not a word.
	return "XfFHpqt"
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
	default:
		return bundleEndsWithValued(t, apiValued())
	}
}

// bundleEndsWithValued reports whether the text t is a bundle of shorthands whose first letter that
// is in valued is its last.
func bundleEndsWithValued(t, valued string) bool {
	if !isShortBundle(t) {
		return false
	}

	for i := 1; i < len(t); i++ {
		if strings.IndexByte(valued, t[i]) >= 0 {
			return i == len(t)-1
		}
	}

	return false
}

// apiMethod returns the last method rest gives `gh api` with -X or --method, and false when it
// gives none. A word that is not a literal and may be the flag gives a method that is not a
// literal. The word after a flag that takes a value is that value, never a method flag; a `--` after
// a word that is not a literal and may be such a flag is skipped as its value too.
func apiMethod(rest []Word) (method Word, given bool) {
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if isEndOfFlags(w) {
			break
		}

		// pflag reads a literal `--` after a flag that takes the next word as its value as that
		// value, so a `--` after a word that may be such a flag is skipped with it.
		dashes := dashesFollow(rest, i) && mayBeValuedFlag(rest[i])
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

// The long name and the shorthand letter of the method flag of `gh api`.
const (
	apiMethodLong  = "--method"
	apiMethodShort = 'X'
)

// methodFlag returns the method w gives `gh api` when it is -X or --method, reading next as the
// value when the flag takes the next word. A word that is not a literal and may be the flag gives
// a method that is not a literal.
func methodFlag(w, next Word) (method Word, ok, takesNext bool) {
	if value, attached := strings.CutPrefix(w.Text, apiMethodLong+"="); attached {
		return Word{Text: value, Literal: w.Literal}, true, false
	}

	if w.Literal && w.Text == apiMethodLong {
		return next, true, true
	}

	if value, found, attached := shortValue(w, apiMethodShort, apiValued()); found {
		if attached {
			return value, true, false
		}

		return next, true, true
	}

	if mayBeFlag(w, apiMethodShort, apiMethodLong) {
		return Word{}, true, false
	}

	return Word{}, false, false
}

// ghFlagReading is what ghFlag reads of a flag among the words of a gh command.
type ghFlagReading int

const (
	// flagUnset means no word sets the flag or may be it.
	flagUnset ghFlagReading = iota
	// flagSet means a word sets the flag: a literal, or a bundle of shorthands holding it.
	flagSet
	// flagValueNotLiteral means a word gives the long flag a value after `=` that is not a literal,
	// which gh reads as setting the flag.
	flagValueNotLiteral
	// flagMaybeSet means a word that is not a literal may be the flag.
	flagMaybeSet
)

// ghFlag reads whether rest sets flag, by its shorthand letter (0 when it has none) or its long
// name, read as pflag reads it, up to `--`. A `--` that follows a word for which takesNext holds is
// that flag's value, as pflag reads it, and does not end the words; so does a `--` that follows a
// word that is not a literal and may be such a flag (see mayBeValuedFlag).
func ghFlag(rest []Word, flag ghGuardedFlag, takesNext func(Word) bool) ghFlagReading {
	reading := flagUnset
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if isEndOfFlags(w) {
			break
		}

		_, gives, _ := flagValue(w, flag)
		switch {
		case gives && !w.Literal && strings.HasPrefix(w.Text, flag.long+"="):
			return flagValueNotLiteral
		case gives:
			return flagSet
		case mayBeFlag(w, flag.short, flag.long):
			reading = flagMaybeSet
		}

		if dashesFollow(rest, i) && (takesNext(w) || mayBeValuedFlag(w)) {
			i++
		}
	}

	return reading
}

// wordAfter returns the word after rest[i], and the zero Word when rest[i] is the last.
func wordAfter(rest []Word, i int) Word {
	if i+1 < len(rest) {
		return rest[i+1]
	}

	return Word{}
}

func dashesFollow(rest []Word, i int) bool {
	return i+1 < len(rest) && isEndOfFlags(rest[i+1])
}

func isEndOfFlags(w Word) bool {
	return w.Literal && w.Text == "--"
}

// mayBeValuedFlag reports whether w is a word that is not a literal whose literal prefix is empty
// or starts with `-`, so that it may expand to a flag that takes the next word as its value. The
// guard reads on past a `--` after such a word, which can only make a refusal more likely.
func mayBeValuedFlag(w Word) bool {
	return !w.Literal && (w.Text == "" || strings.HasPrefix(w.Text, "-"))
}

// mayTakeValue reports whether the literal word w may take the next word as its value in a gh
// command whose shorthands that take a value are valued: a long flag or a lone shorthand with no
// `=`, as takesNextWord reads it, or a bundle of shorthands whose first valued letter ends it. A
// flag that takes none counts too, since the guard does not know every flag of the command.
func mayTakeValue(w Word, valued string) bool {
	t := w.Text
	switch {
	case !w.Literal:
		return false
	case takesNextWord(t):
		return true
	default:
		return bundleEndsWithValued(t, valued)
	}
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
