//go:build unix

package guard_test

import (
	"testing"
)

func TestJudge_RefusesAPrefixOfAGuardedLongOption(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "ambiguous prefix of --no-verify", command: "git commit --no-v -m msg", wantNamed: "--no-v"},
	})
}

func TestJudge_RefusesAFlagBehindTheValueOfAnOptionThatTakesOne(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "restore --pathspec-from-file before --staged", command: "git restore --pathspec-from-file --staged", wantNamed: "git restore"},
		{name: "restore prefix shared with a flag", command: "git restore --pathspec-f --staged f"},
	})
}

func TestJudge_PassesOverTheValueOfAnOptionThatTakesOne(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "-m", command: `git commit -m "$msg"`},
		{name: "attached --message", command: `git commit --message="$msg"`},
		{name: "attached message spelled like -n", command: "git commit -m-n"},
	})
}

func TestJudge_PassesOverANonLiteralWordThatNoGuardedOptionCanBe(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "rm path after --", command: `git rm -- "$f"`},
		{name: "log revision", command: `git log "$x"`},
		{name: "add of a glob with a literal prefix", command: "git add src/*.go"},
		{name: "commit of a glob path", command: "git commit -m msg src/*.go"},
		{name: "rm of a glob path", command: "git rm docs/*.md"},
		{name: "restore --staged of a glob path", command: "git restore --staged src/*.go"},
	})
}

func TestJudge_WhenAGitPathArgumentCanExpandToAnOption_RefusesNamingDoubleDash(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "variable after a literal prefix", command: "git add src/$x", wantNamed: "after `--`"},
		{name: "glob with no literal prefix", command: "git add *.go", wantNamed: "after `--`"},
		{name: "glob whose prefix starts with a dash", command: "git add -*", wantNamed: "after `--`"},
		{name: "glob whose prefix starts with a plus", command: "git add +*", wantNamed: "after `--`"},
		{name: "zsh glob qualifier that can replace the match", command: "git commit -m msg src/*(e:REPLY=--no-verify:)"},
	})
}

func TestJudge_RefusesAGlobWhereTheGuardNeedsALiteral(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "worktree remove path", command: "git worktree remove wt*"},
		{name: "update-ref ref name", command: "git update-ref refs/heads/new* HEAD"},
	})
}
