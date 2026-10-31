// cspell:ignore verif forc -- deliberately truncated git option spellings git accepts.

package guard_test

import (
	"testing"
)

func TestJudge_RefusesAPrefixOfAGuardedLongOption(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "prefix of --no-verify", command: "git commit --no-verif -m msg", wantNamed: "--no-verif"},
		{name: "ambiguous prefix of --no-verify", command: "git commit --no-v -m msg", wantNamed: "--no-v"},
		{name: "prefix of --no-verify on push", command: "git push --no-ver", wantNamed: "--no-ver"},
		{name: "prefix of send-email --no-validate", command: "git send-email --no-valid x.patch", wantNamed: "--no-valid"},
		{name: "prefix of clean --force", command: "git clean --forc", wantNamed: "git clean"},
		{name: "prefix of rm --force", command: "git rm --forc file", wantNamed: "git rm"},
		{name: "prefix of switch --discard-changes", command: "git switch --disc branch", wantNamed: "git switch"},
		{name: "prefix of switch --force", command: "git switch --forc branch", wantNamed: "git switch"},
		{name: "prefixes of branch --delete --force", command: "git branch --del --forc feature", wantNamed: "git branch"},
		{name: "prefix of restore --worktree", command: "git restore --work file", wantNamed: "git restore"},
		{name: "prefix of restore --worktree beside --staged", command: "git restore --staged --work file", wantNamed: "git restore"},
		{name: "prefix of apply --reverse", command: "git apply --rev patch.diff", wantNamed: "git apply"},
		{name: "prefix of add --force", command: "git add --forc x", wantNamed: "git add"},
		{name: "prefix of gc --prune", command: "git gc --pru=now", wantNamed: "git gc"},
	})
}

func TestJudge_RefusesAFlagBehindTheValueOfAnOptionThatTakesOne(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "restore --pathspec-from-file before --staged", command: "git restore --pathspec-from-file --staged", wantNamed: "git restore"},
		{name: "restore prefix of --pathspec-from-file", command: "git restore --pathspec-from --staged", wantNamed: "git restore"},
		{name: "restore prefix shared with a flag", command: "git restore --pathspec-f --staged f", wantNamed: "git restore"},
		{name: "rm --pathspec-from-file taking --", command: "git rm --pathspec-from-file -- -f", wantNamed: "git rm"},
		{name: "add prefix of --pathspec-from-file taking --", command: "git add --pathspec-from -- -f", wantNamed: "git add"},
		{name: "clean --exclude taking --", command: "git clean --exclude -- -f", wantNamed: "git clean"},
		{name: "clean prefix of --exclude taking --", command: "git clean --exc -- -f", wantNamed: "git clean"},
		{name: "branch --format taking --", command: "git branch --format -- -D feature", wantNamed: "git branch"},
		{name: "apply --exclude taking --", command: "git apply --exclude -- -R patch.diff", wantNamed: "git apply"},
		{name: "commit --trailer taking --", command: "git commit --trailer -- -n -m msg", wantNamed: "git commit"},
		{name: "commit prefix of --trailer taking --", command: "git commit --trail -- -n -m msg", wantNamed: "git commit"},
		{name: "push --repo taking --", command: "git push --repo -- --no-verify", wantNamed: "--no-verify"},
		{name: "merge --into-name taking --", command: "git merge --into-name -- --no-verify main", wantNamed: "--no-verify"},
		{name: "rebase --exec taking --", command: "git rebase --exec -- --no-verify main", wantNamed: "--no-verify"},
		{name: "pull --upload-pack taking --", command: "git pull --upload-pack -- --no-verify", wantNamed: "--no-verify"},
		{name: "am --directory taking --", command: "git am --directory -- --no-verify x.patch", wantNamed: "--no-verify"},
	})
}

func valueSlots() []keptWork {
	return []keptWork{
		{name: "message from a heredoc", command: "git commit -m \"$(cat <<'EOF'\nsubject\n\nbody\nEOF\n)\""},
		{name: "-m", command: `git commit -m "$msg"`},
		{name: "attached --message", command: `git commit --message="$msg"`},
		{name: "-F", command: `git commit -F "$file"`},
		{name: "--file", command: `git commit --file "$file"`},
		{name: "--author", command: `git commit --author "$who" -m msg`},
		{name: "attached --date", command: `git commit --date="$when" -m msg`},
		{name: "commit -C", command: `git commit -C "$rev"`},
		{name: "commit -c", command: `git commit -c "$rev"`},
		{name: "attached value of another option", command: `git push origin --push-option="$x"`},
		{name: "rm path after --", command: `git rm -- "$f"`},
		{name: "add path after --", command: `git add -- "$f"`},
		{name: "global -C", command: `git -C "$dir" status`},
		{name: "global -C before commit", command: `git -C "$dir" commit -m msg`},
		{name: "global --git-dir", command: `git --git-dir "$d" commit -m msg`},
		{name: "global --work-tree", command: `git --work-tree="$w" commit -m msg`},
		{name: "global --namespace", command: `git --namespace "$n" push origin`},
		{name: "message spelled like -n", command: "git commit -m -n"},
		{name: "message spelled like --no-verify", command: "git commit -m --no-verify"},
		{name: "attached message spelled like -n", command: "git commit -m-n"},
		{name: "--message value spelled like -n", command: "git commit --message -n"},
		{name: "commit --trailer", command: `git commit --trailer "$t" -m msg`},
		{name: "switch --create", command: `git switch --create "$b"`},
		{name: "push --repo", command: `git push --repo "$r"`},
		{name: "merge -m", command: `git merge -m "$msg" main`},
		{name: "rebase prefix of --onto", command: `git rebase --ont "$base" main`},
		{name: "pull --upload-pack", command: `git pull --upload-pack "$p"`},
		{name: "log revision", command: `git log "$x"`},
	}
}

func TestJudge_PassesOverTheValueOfAnOptionThatTakesOne(t *testing.T) {
	t.Parallel()

	checkAllowed(t, repositoryCaller(t), valueSlots())
}
