package cli

// The words of the grammar: the command of this version, the commands of later ones, and the flags.
const (
	wordPush    = "push"
	wordBranch  = "branch"
	wordMerge   = "merge"
	wordRelease = "release"
	wordStatus  = "status"

	flagJSON      = "--json"
	flagHelp      = "--help"
	flagHelpShort = "-h"
)

// summary lists every command, the ones of later versions included, so that typing one gets an
// answer.
const summary = `ils lands a branch on GitHub, one command per step.

Commands:
  ils push            push the branch, open its pull request on the first push, wait for the checks
  ils branch <name>   not in this version
  ils merge           not in this version
  ils release <X.Y.Z> not in this version
  ils status          not in this version

Flags, anywhere among the words:
  --json              print the result as one JSON object
  -h, --help          print this text, or a command's own after its name

Exit codes: 0 done, 1 broke, 2 refused, 4 pending, 5 delivered and not green.
Every command ends within 100 seconds of its launch.
`

// pushUsage names, under "By hand:", every git and gh call `ils push` makes, one line per call.
// A test runs push and holds the lines to the calls it recorded, in both directions: a call
// added to push without its line here fails, and so does a line no call stands behind.
const pushUsage = `ils push

Pushes the current branch to origin under its own name, never forced, opens its pull
request against the trunk on the first push, and waits for the checks of the pushed commit.
It refuses the trunk, a detached HEAD, a changed tracked file, a branch with no commit past
origin's trunk, a branch cut from another branch, a branch behind or diverged from origin's
copy, and a branch whose pull request already merged.

In the lines below <name> is the branch, <trunk> is origin's default branch, and <r> is the
repository as <host>/<owner>/<repo>, read from origin's URL.

By hand:
  git rev-parse --absolute-git-dir
  git remote get-url origin
  git remote get-url --push origin
  git symbolic-ref --quiet HEAD
  gh auth status --hostname <host>
  gh repo view <r> --json isFork,viewerPermission
  git ls-remote --symref origin HEAD
  git status --porcelain=v1 -z --untracked-files=no
  git fetch origin <trunk>
  git rev-parse FETCH_HEAD
  git rev-parse HEAD
  git show-ref --verify --quiet refs/heads/<name>
  git log --reverse --topo-order --format='%H %s' --no-merges <sha> ^origin/<trunk>
  git for-each-ref --format='%(refname)' refs/heads/ --contains <sha>
  git merge-base --is-ancestor <sha> <name>
  git ls-remote --exit-code origin refs/heads/<name>
  git fetch origin <name>
  git rev-list --left-right --count <sha>...<sha>
  gh pr list --repo <r> --head <name> --state open --json number,url,title,baseRefName,headRefOid,isDraft
  gh pr list --repo <r> --head <name> --state merged --json number,url,title,baseRefName,headRefOid,isDraft
  git fetch origin refs/pull/<N>/head
  git log --reverse --topo-order --format='%H %s' --no-merges --cherry-pick --right-only <sha>...<sha> ^origin/<trunk>
  git push -u origin <name>
  gh pr create --repo <r> --title <name> --body '' --base <trunk> --head <name>
  gh api --hostname <host> repos/<owner>/<repo>/rules/branches/<trunk>
  gh pr view --repo <r> <N> --json headRefOid,mergeable,mergeStateStatus,isDraft,reviewDecision
  gh pr checks --repo <r> <N> --json name,bucket,workflow,link,state
  gh run list --repo <r> --commit <sha> --json databaseId,status,event,name,workflowName
  gh run view --repo <r> <id> --log-failed

Then, by hand:
  gh pr edit --repo <r> <N> --title … --body …
  gh pr merge --repo <r> <N> --squash --delete-branch --match-head-commit <sha> --subject "<title> (#<N>)" --body "<body>"
`

// usage returns the text for the words typed beside a help flag: the command's own after its
// name, and otherwise the summary followed by the text of each command of this version.
func usage(words []string) string {
	if len(words) > 0 && words[0] == wordPush {
		return pushUsage
	}

	return summary + "\n" + pushUsage
}
