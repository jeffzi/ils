//go:build unix

// cspell:ignore verif -- deliberately truncated git option spelling git accepts.

package guard_test

import (
	"testing"
)

func TestJudge_RefusesAPrefixOfAGuardedLongOption(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "prefix of --no-verify", command: "git commit --no-verif -m msg", wantNamed: "--no-verif"},
		{name: "ambiguous prefix of --no-verify", command: "git commit --no-v -m msg", wantNamed: "--no-v"},
		{name: "prefix of gc --prune", command: "git gc --pru=now", wantNamed: "git gc"},
	})
}

func TestJudge_RefusesAFlagBehindTheValueOfAnOptionThatTakesOne(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "restore --pathspec-from-file before --staged", command: "git restore --pathspec-from-file --staged", wantNamed: "git restore"},
		{name: "restore prefix of --pathspec-from-file", command: "git restore --pathspec-from --staged", wantNamed: "git restore"},
		{name: "restore prefix shared with a flag", command: "git restore --pathspec-f --staged f", wantNamed: "git restore"},
	})
}

func valueSlots() []namedCommand {
	return []namedCommand{
		{name: "-m", command: `git commit -m "$msg"`},
		{name: "attached --message", command: `git commit --message="$msg"`},
		{name: "--file", command: `git commit --file "$file"`},
		{name: "rm path after --", command: `git rm -- "$f"`},
		{name: "attached message spelled like -n", command: "git commit -m-n"},
		{name: "log revision", command: `git log "$x"`},
	}
}

func TestJudge_PassesOverTheValueOfAnOptionThatTakesOne(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, valueSlots())
}
