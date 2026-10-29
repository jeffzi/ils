package cli

import (
	"fmt"

	"github.com/jeffzi/ils/internal/landing"
)

// withBound returns long with its one %d verb set to the time limit in seconds, so that the help
// says the limit the command enforces.
func withBound(long string) string {
	return fmt.Sprintf(long, int(landing.TimeLimit.Seconds()))
}

// The help says how to call a command and nothing else: what to do with an exit code and with
// the next step is said by the result of each run.
const (
	rootLong = `ils lands a branch on GitHub in fixed, checked steps: push, pull request,
CI verdict. It exists so that a coding agent cannot push to the wrong
place, skip the checks, or call a red build done.

Docs: https://github.com/jeffzi/ils`

	pushShort = "Push the branch, open its pull request, wait for the checks"
	pushLong  = `Push the current branch, open its pull request on the first push, and
wait up to %d seconds for the checks of the pushed commit. It never
forces a push and it does not merge.

It refuses before pushing anything when the branch is not ready, for
example on the trunk (origin's default branch), with uncommitted
changes, or behind origin's copy of it. The output gives the reason and
the next step.`

	pushExamples = `# Push and wait for the checks
ils push

# The same result as one JSON object
ils push --json`

	mergeShort = "Squash and merge the pull request once its checks are green"
	mergeLong  = `Squash-merge the pull request of the current branch once its checks are
green, within %d seconds of the launch.

It squash-merges the pull request once its checks are green, deletes the
branch on origin and in the clone, and leaves the trunk checked out at
origin's tip. It never pushes: push the branch with ils push first.

It refuses before merging anything when the pull request is not ready,
for example when a check is red, when it is a draft, or when its title is
not a conventional commit title. The output gives the reason and the next
step.`

	mergeExamples = `# Merge once the checks are green
ils merge

# The same result as one JSON object
ils merge --json`

	branchShort = "Start a branch from the trunk as origin has it"
	branchLong  = `Fetch the trunk (origin's default branch) and start <name> from origin's
copy of it, then switch to it. The name is used exactly as typed.
Uncommitted changes come along to the new branch.

It refuses before creating anything when a branch locally or on origin
takes the name, by the same name or a path that collides with it, or when
a detached HEAD has commits the trunk lacks. The output
gives the reason and the next step.`

	branchExamples = `# Start a branch for new work
ils branch fix-help`

	statusShort = "Say where the branch stands, and what to run next"
	statusLong  = `Say where the current branch stands against origin, its pull requests
and the verdict of the open one's checks, or on the trunk, its last tag
and whether a ruleset protects it; then the next command to run.

It fetches what it reads from origin (the trunk, the branch's copy, a
merged pull request's head, a version tag) and writes nothing of yours.
It always exits 0 once its words parse: what it could not read is a line
of the output, not a refusal.`

	statusExamples = `# Where the branch stands
ils status

# The same result as one JSON object
ils status --json`
)
