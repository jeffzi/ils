package guard

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
)

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
	expansion, found, err := runner.Alias(ctx, name)
	if err != nil {
		return nil, "", fmt.Errorf("read gh alias list: %w", err)
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
