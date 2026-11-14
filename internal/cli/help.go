package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeffzi/ils/internal/commandline"
	"github.com/jeffzi/ils/internal/landing"
)

// parseHeader returns the `ils parse` command with its help, which parseCommand completes with its
// Args and RunE.
func parseHeader() *cobra.Command {
	return &cobra.Command{
		Use:     parseUse,
		Short:   parseShort,
		Long:    parseLong,
		Example: parseExamples,
	}
}

// guardHeader returns the `ils guard` command with its help, which guardCommand completes with its
// Args and RunE.
func guardHeader() *cobra.Command {
	return &cobra.Command{
		Use:     guardUse,
		Short:   guardShort,
		Long:    guardLong,
		Example: guardExamples,
	}
}

// hookHeader returns the `ils hook` command with its help and flag parsing off, which hookCommand
// completes with its RunE.
func hookHeader() *cobra.Command {
	return &cobra.Command{
		Use:                hookUse,
		Short:              hookShort,
		Long:               hookLong,
		Example:            hookExamples,
		DisableFlagParsing: true,
	}
}

// withTimeLimits returns text with each %[1]s set to the time limit and each %[2]s set to the time
// limit of the long wait, as results name them, so that the help says the limits the command
// enforces. A text that needs only the time limit leaves %[2]s out.
func withTimeLimits(text string) string {
	return fmt.Sprintf(text, landing.Span(landing.TimeLimit), landing.Span(landing.WaitTimeLimit))
}

// withCheckWaits returns text as withTimeLimits does, with each limit cut to how long `ils merge`
// and `ils release` wait for the checks under it, so that their help does not promise the time
// they keep for their writes.
func withCheckWaits(text string) string {
	return fmt.Sprintf(text, checkWait(landing.TimeLimit), checkWait(landing.WaitTimeLimit))
}

// checkWait returns the wait for the checks under limit, in the unit Span names limit in: in
// seconds under a limit of seconds, "80 seconds", and in minutes, then the seconds left over, under
// a limit of minutes, "14 minutes 40 seconds".
func checkWait(limit time.Duration) string {
	wait := landing.CheckWait(limit)
	if limit%time.Minute != 0 || wait < time.Minute || wait%time.Minute == 0 {
		return landing.Span(wait)
	}

	return landing.Span(wait.Truncate(time.Minute)) + " " + landing.Span(wait%time.Minute)
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
green: within %[2]s of the launch when stdout is a
terminal or with --wait, and within %[1]s otherwise or
with --wait=false.

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
notes. It waits up to %[2]s when stdout is a terminal
or with --wait, and up to %[1]s otherwise or with
--wait=false.

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

	parseWord  = commandline.ParseCommand
	parseUse   = parseWord + " --cwd <dir> -- '<command>'"
	parseShort = "Show how the guard reads a shell command"
	parseLong  = `Read a shell command as the guard does, as run from <dir>, and show every
command it starts: its words, what the guard would refuse in it, the
directory it runs in, and for git the subcommand, its arguments and the
git dir it targets. The command goes after -- as one quoted word.

It runs nothing of the command and refuses nothing it holds: it exits 0
once the command is read, and 1 when the command does not parse or a read
of git it needs fails. The guard runs on macOS and Linux.`

	parseExamples = `# Show the commands a shell command starts
ils parse --cwd . -- 'git switch main && git pull'

# The same as one JSON object, for a filter such as jq
ils parse --cwd . --json -- 'git push'`

	guardWord  = commandline.GuardCommand
	guardUse   = guardWord + " --cwd <dir> -- '<command>'"
	guardShort = "Judge a shell command as the guard does"
	guardLong  = `Read a shell command as the guard does, as run from <dir>, and judge
every git, gh and ils command it starts. The command goes after -- as one
quoted word.

It runs nothing of the command. It exits 0 when the guard allows the
command, 2 when it refuses it, and 3 when it asks you first; a command it
cannot read, or a read of git it cannot make, is a refusal. The guard
runs on macOS and Linux.`

	guardExamples = `# Judge a shell command
ils guard --cwd . -- 'git push --force'

# The verdict, its reason and every command, as one JSON object
ils guard --cwd . --json -- 'ils merge'`

	hookWord  = commandline.HookCommand
	hookUse   = hookWord + " <harness>"
	hookShort = "Judge a coding agent's shell command as its pre-tool hook"
	hookLong  = `Read the payload a coding agent's harness hands its pre-tool hook on
stdin, judge the shell command in it as ils guard does, and answer in
the harness's own terms. The one harness is claude, Claude Code's
PreToolUse hook.

It exits 0 with nothing printed when the guard allows the command, or
when the tool is not Bash; 2 with the reason on stderr when it refuses
it; and 0 with the ask as JSON on stdout when the guard asks you first.
A payload it cannot read, and any error, is a refusal: it never exits 1.
The guard runs on macOS and Linux.`

	hookExamples = `# Judge a payload as Claude Code's PreToolUse hook
ils hook claude < payload.json`

	initWord  = "init"
	initUse   = initWord + " <harness>"
	initShort = "Set up a coding agent's harness: the skills, the guard's hook, the rules"
	initLong  = `Set up a coding agent's harness for ils. The one harness is claude:
the skills ils ships land in ~/.claude/skills/<name>, and the guard's
hook and the permission rules of the flow go into
~/.claude/settings.json. When CLAUDE_CONFIG_DIR is set and not empty,
both go into that directory instead. With --project, both go into .claude/ at the
top level of the repository the working directory is in.

A skill file that already holds what ils ships is left as it is, a file
ils does not ship stays, and a skill directory that is a link is not
written to. The settings keep every key and entry they hold: ils adds
the hook and each rule no list holds yet, and sets the timeout and the
failure of a guard hook already there. It prints one line per change,
and exits 0 once everything is in place.`

	initExamples = `# Set up Claude Code under your home directory
ils init claude

# Set it up in the repository you are in
ils init claude --project`
)
