package cli

import (
	"fmt"

	"github.com/jeffzi/ils/internal/landing"
)

// withTimeLimits returns text with each %[1]s set to the time limit and each %[2]s set to the time
// limit of the long wait, as results name them, so that the help says the limits the command
// enforces. A text that needs only the time limit leaves %[2]s out.
func withTimeLimits(text string) string {
	return fmt.Sprintf(text, landing.Span(landing.TimeLimit), landing.Span(landing.WaitTimeLimit))
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
wait for the checks of the pushed commit: up to %[2]s when
stdout is a terminal or with --wait, and up to %[1]s
otherwise or with --wait=false. It never forces a push and it does
not merge.

It refuses before pushing anything when the branch is not ready, for
example on the trunk (origin's default branch), with uncommitted
changes, or behind origin's copy of it. The output gives the reason and
the next step.`

	pushExamples = `# Push and wait for the checks
ils push

# Wait the longer time even when stdout is no terminal
ils push --wait

# Wait only the shorter time even at a terminal
ils push --wait=false

# The same result as one JSON object
ils push --json`

	waitUsage = "Wait up to %[2]s for the checks, as at a terminal; =false waits up to %[1]s"

	mergeShort = "Squash and merge the pull request once its checks are green"
	mergeLong  = `Squash-merge the pull request of the current branch once its checks are
green: within %[2]s of the launch when stdout is a terminal or
with --wait, and within %[1]s otherwise or with --wait=false.

It squash-merges the pull request once its checks are green, deletes the
branch on origin and in the clone, and leaves the trunk checked out at
origin's tip. It never pushes: push the branch with ils push first.

It refuses before merging anything when the pull request is not ready,
for example when a check is red, when it is a draft, or when its title is
not a conventional commit title. The output gives the reason and the next
step.`

	mergeExamples = `# Merge once the checks are green
ils merge

# Wait the longer time even when stdout is no terminal
ils merge --wait

# Wait only the shorter time even at a terminal
ils merge --wait=false

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

	releaseShort = "Tag a release commit once its CI is green, and publish the release"
	releaseLong  = `Find the release commit of <version>, chore: release v<version>, on
origin's trunk since the previous version tag, and read the notes of
<version> from the changelog at that commit. Wait for the commit's CI,
then tag it, push the tag and create the GitHub release from those
notes. It waits up to %[2]s when stdout is a terminal or with
--wait, and up to %[1]s otherwise or with --wait=false.

--force tags the commit without waiting for its CI. --tag-only stops
once the tag is pushed. Running it again resumes where the last run
stopped.

It refuses before writing anything when the release is not ready, for
example when no release commit names the version, when the changelog
has no notes for it, or when its CI is red. The output gives the reason
and the next step.`

	releaseExamples = `# Tag and publish 1.2.0 once its CI is green
ils release 1.2.0

# Push the tag only, leaving the GitHub release to another workflow
ils release 1.2.0 --tag-only`

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
