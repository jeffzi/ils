# ils

`ils` (Instrument Landing System) lands a change on GitHub in fixed, checked steps: start a branch,
push it and open its pull request, wait for the checks, squash-merge it, and tag a release. It is
built for the large language model (LLM) agents that do this work for you.

An agent lands code the way its instructions describe, and instructions written as prose get
skipped or paraphrased. The result is a merge while a check is red, a push to the default branch, a
force push over someone else's commits, or a stale run reported as green. `ils` moves the steps that
must not fail out of the prose and into a binary. Each command checks its preconditions first
and refuses, with a reason, what must not happen. Each one ends with a `Next:` line and an exit code
that an agent can act on without reading the rest. A guard refuses the destructive `git` commands
before they run.

Everything `ils` does, you could do by hand with `git` and `gh`: it adds the order and the
refusals. It keeps no state of its own and reads only what git and GitHub already hold: the branch,
its pull request, its checks, and the tags. A command exists when it makes a mistake impossible.

- [Example](#example)
- [Commands](#commands)
- [The flow](#the-flow)
- [Typical workflows](#typical-workflows)
- [The guard](#the-guard)
- [Install](#install)
- [Set up](#set-up)
- [Reference](#reference)
- [Contributing](#contributing)
- [License](#license)

## Example

<!-- The output below is verbatim and a terminal soft-wraps it, so its lines stay whole. -->
<!-- markdownlint-disable MD013 -->

```console
$ ils push
Pushed tidy-readme at 4f2a9c1
Opened pull request #12 against main, titled tidy-readme with no body: https://github.com/you/repo/pull/12
Checks for 4f2a9c1: 3 passed
Next: write the pull request's title and body as the squash commit's message with `gh pr edit --repo github.com/you/repo 12 --title … --body …`, then run ils merge
```

<!-- markdownlint-enable MD013 -->

The `Next:` line names the step that follows: here, writing the squash commit's message, then
`ils merge`.

## Commands

```bash
ils branch <name>     # start a branch from the default branch as origin has it
ils push              # push the branch, open its pull request, wait for the checks
ils merge             # squash-merge the pull request once its checks are green
ils status            # where the checkout stands, and what to run next
ils release <X.Y.Z>   # tag the release commit once its checks are green, then publish it
ils init claude       # install the skills and wire the guard into Claude Code
```

[`ils branch <name>`](docs/reference.md#ils-branch) creates the branch at origin's copy of the
default branch and switches to it, carrying your uncommitted changes along. New work never starts
from a stale local default branch, or from another work branch whose commits would ride along into
the new pull request. While the local default branch holds commits origin lacks, it refuses, lists
them, and names the one command that moves them to a branch of their own; `ils push` and
`ils merge` refuse the same way. `ils` never moves those commits itself.

[`ils push`](docs/reference.md#ils-push) pushes the branch by name, never with a force flag, opens
its pull request on the first push, and waits for the checks of the pushed commit. It refuses the
default branch, changes to tracked files, a branch behind or diverged from its copy on origin, and a
branch that already merged. A red check, a conflict, or a required check that never ran ends at
exit 5, never as green.

[`ils merge`](docs/reference.md#ils-merge) squash-merges the pull request with its title and body
as the commit message, deletes the branch, and leaves the checkout on origin's default branch. It
never pushes and has no `--force`. It refuses a red or missing check, a draft, a review
still required, a commit not yet on the pull request, and a title that is not a conventional commit
subject. After a merge done on the web, it finishes the local steps.

[`ils status`](docs/reference.md#ils-status) says where the checkout stands and what to run next.
It fetches from origin and writes nothing of yours. It exits 0 whenever its arguments are valid,
except when it cannot read the local default branch, which ends it broke as it ends every landing
command.

[`ils release <X.Y.Z>`](docs/reference.md#ils-release) finds the commit `chore: release v<X.Y.Z>`
on origin's default branch, waits for its checks, tags it, pushes the tag, and creates the GitHub
release from that version's section of `CHANGELOG.md`. It refuses a version that is not above every
version tag on origin, a missing changelog section, and a failed check.

[`ils init claude`](docs/reference.md#ils-init) installs the agent skills that ship inside the
binary and adds the guard's hook and the flow's permission rules to Claude Code's settings, under
your home directory or, with `--project`, in the repository.
[Use with Claude Code](#use-with-claude-code) walks through it.

Each of these commands takes `--json`, `-d` for a trace of each `git` and `gh` call, and `--help`.
[Flags](docs/reference.md#flags) lists them all, and [Exit codes](docs/reference.md#exit-codes)
lists what each exit means.

## The flow

| Step | What happens                                                               | Command               | Who runs it                                     |
| ---- | -------------------------------------------------------------------------- | --------------------- | ----------------------------------------------- |
| 1    | Start a branch from origin's default branch                                | `ils branch <name>`   | the agent                                       |
| 2    | Commit                                                                     | `git commit`          | the agent                                       |
| 3    | Push, open the pull request, wait for the checks; again until green        | `ils push`            | the agent, on your word on a private repository |
| 4    | Write the pull request's title and body, which become the squash's message | `gh pr edit`          | agent or you                                    |
| 5    | Squash-merge the pull request and delete the branch                        | `ils merge`           | you                                             |
| 6    | Tag and publish a release, when you want one                               | `ils release <X.Y.Z>` | you                                             |

The agent runs steps 1 to 4 on its own: the worst a push leaves behind is a pull request with red
checks. The push has one condition. On a public repository the agent pushes at the end of each
request. On a private one it commits each request and pushes when you say so, since each push
starts CI runs that spend your GitHub Actions minutes: the guard asks you before every `ils push`
and `ils push --wait` there. The merge and the release change what other people see, so they wait
for your word.
[Use with Claude Code](#use-with-claude-code) shows how to make Claude Code ask you before each.

## Typical workflows

**One change, start to finish.** `ils branch fix-typo`, edit, `git commit`, then `ils push` until
it is green. Write the title and body with the `gh pr edit` command `Next:` prints, then
`ils merge`.

**Continuous integration (CI) is red.** `ils push` exits 5 and prints the failing check, its URL,
and the end of its log. Fix the cause on the same branch, commit, and run `ils push` again. For a
conflict, or a branch behind the default branch, merge the default branch into the branch,
resolve, commit, and run `ils push` again.

**A follow-up change.** When the first pull request has merged, start the next with
`ils branch <name>`: the default branch already holds the first change. While it is still open,
the agent asks you where a request outside that pull request lands, and leaves the choice to you:

- **A new branch from the default branch:** one more pull request, and one more CI run.
- **The open branch:** `ils push` updates the same pull request, and the squash merge folds both
  changes into one commit, whose title and body are written once, at merge time, for the whole
  branch.

Merges are squash-only: every pull request lands on the default branch as one commit.

**A run was killed, or something was done on the web.** Run the same command again. `ils push`
pushes nothing new and waits for the same commit's checks. `ils merge` finishes the local steps of
a merge made on the web. `ils release` finishes a release whose tag push or GitHub release failed.

**Cutting a release.** Releases are optional: `ils branch`, `ils push`, and `ils merge` never read
a version, a tag, or a changelog. To release 1.2.0, land a commit titled `chore: release v1.2.0`
that bumps the version and adds the `## [1.2.0]` section to `CHANGELOG.md`, through `ils branch`,
`ils push`, and `ils merge`. Then run `ils release 1.2.0`. It needs no checkout, and finds the
release commit on origin's default branch even after later merges. The `write-release` skill (see
[Use with Claude Code](#use-with-claude-code)) prepares that commit.

## The guard

The guard judges a shell command before an agent runs it, and answers allow, refuse, or ask. It
refuses `git` operations that destroy work or skip the hooks, and every `git push`. It also refuses
commits on the default branch of every GitHub repository, protected or not, and `gh` calls that
land or publish a change without the checks of `ils`. It asks you before `ils merge`,
`ils release`, `gh pr merge`, and `gh release create`, and before `ils push` on a private
repository.

The guard runs on macOS and Linux.
[Use with Claude Code](#use-with-claude-code) sets it up.
[The guard](docs/reference.md#the-guard) in the reference lists every rule, and
[What the guard does not see](docs/reference.md#what-the-guard-does-not-see) lists what gets past
it.

## Install

### Requirements

To build:

- Go 1.27 or later.
- [Task](https://taskfile.dev), to install from a clone.

To run:

- [`git`](https://git-scm.com/downloads) 2.34.1 or later, on `PATH`.
- [`gh`](https://cli.github.com) 2.81.0 or later, installed and logged in to the host of `origin`
  (`gh auth login --hostname <host>`). `ils` owns no token and no login flow.
- A remote named `origin` on GitHub that you can push to and that is not a fork.

`ils` runs on macOS, Linux, and Windows.

### Build the binary

With Go:

```bash
go install github.com/jeffzi/ils@latest
```

`@latest` installs the newest release. This README follows the main branch: for the changes under
Unreleased in [CHANGELOG.md](CHANGELOG.md), install with `go install github.com/jeffzi/ils@main`.

From a clone, `task install` builds the binary and copies it to the directory `go install` writes
to:

```bash
task install
```

## Set up

### Start a repository

`ils branch`, `ils push`, `ils merge`, and `ils release` need a remote named `origin`. Set one up
once, by hand:

1. Create the repository on GitHub and push to it:

   ```bash
   gh repo create --source . --push --private
   ```

   Use `--public` in place of `--private` to get rulesets on a free plan; see step 2.

2. Protect the default branch with a ruleset. Rulesets need a public repository or a paid GitHub
   plan; on a free private repository, skip this step. `ils` works either way.

   In the repository on GitHub, open Settings, then Rules, then Rulesets. Create a branch ruleset
   that targets the default branch with these rules:

   - require a pull request before merging, with squash as the only merge method;
   - require status checks to pass;
   - block force pushes.

   If your CI runs outside GitHub Actions, make the ruleset require its checks. Without that,
   `ils` treats the repository as having no CI. GitHub's guide to
   [creating rulesets for a repository](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository)
   has the details.

   `ils merge` refuses a red merge on its own; the ruleset is what blocks one made by hand. A
   repository you share with a team needs the paid plan or must be public. Without the ruleset,
   nothing stops a teammate from pushing to the default branch or merging red. The gates of `ils`
   hold only for your own agent.

### Use with Claude Code

`ils init claude` is not in a release yet: [CHANGELOG.md](CHANGELOG.md) lists it under Unreleased.
Install `ils` from the main branch first, with `go install github.com/jeffzi/ils@main` or
`task install` from a clone. The guard's hook needs Claude Code 2.1.295 or later to block a command
when the hook cannot run.

With the binary installed, set up Claude Code with one command:

```bash
ils init claude
```

It installs the two agent skills that ship inside the binary into `~/.claude/skills/`: `ils`, which
runs the binary, and `write-release`, which prepares the release commit. It also adds the guard's
hook and the flow's permission rules to `~/.claude/settings.json`. The hook is a `PreToolUse` hook
on `Bash` that runs the `ils` skill's `hook.sh`. When you set `CLAUDE_CONFIG_DIR` to move Claude
Code's directory, both land in that directory instead.

The rules allow `ils branch` and `ils status`. They make Claude Code ask you before every
`ils merge`, `ils release`, `gh pr merge`, and `gh release create`. They deny `git push`. No rule
covers `ils push`: the guard answers `ils push` and `ils push --wait` itself, allowing them on a
public repository and asking you on a private one.

Your own settings stay. `ils init` replaces any earlier guard hook with its own, and running it
again changes nothing already in place. After you upgrade `ils`, run it again to update the skills
to the new binary's.

The setup under your home directory puts every repository you open in Claude Code under this
workflow: the guard refuses a commit on any GitHub default branch. To leave a repository out, such
as one where you commit to the default branch directly, skip the home-directory setup and run
`ils init claude --project` in each repository you want under the workflow instead. It puts the
skills and the settings in `.claude/` at the top level of the repository you are in, for a setup
you commit with the project.
[ils init](docs/reference.md#ils-init) lists every step, the exact rules, and the same setup by
hand.

With the skills installed, `/ils status`, `/ils branch <name>`, `/ils push`, `/ils merge`, and
`/ils release <X.Y.Z>` run the binary and relay its output. The `ils` skill tells an agent how to
react to each exit code. The merge and the release stay the user's decision: the skill runs
`ils merge` and `ils release` only when you ask for them. Asked for a release, an agent uses
`write-release` to land the release commit through `ils branch`, `ils push`, and `ils merge`, then
tells you to run `ils release <X.Y.Z>`.

### Work with the guard in Claude Code

A missing or broken binary blocks every command. Reinstall it from Claude Code's prompt with a `!`
command, which skips the hook: `! go install github.com/jeffzi/ils@main`, or `! task install`
from a clone of `ils`.

The permission rules are the first layer and the guard the second. Add `deny` rules for the other
commands you never want run, such as `Bash(gh auth token *)`.

In a repository made with `gh repo create --source . --push`, run
`git remote set-head origin --auto` once, as a `!` command or outside the agent.
[Pushes and the default branch](docs/reference.md#pushes-and-the-default-branch) says why the
guard needs it.

## Reference

[docs/reference.md](docs/reference.md) holds the details a caller looks up:

- [Commands](docs/reference.md#commands), each command's steps and flags, and the by-hand steps
  for `ils branch`, a red merge, `ils release`, and `ils init`;
- [Exit codes](docs/reference.md#exit-codes), and what to do after each;
- [Time limits](docs/reference.md#time-limits), how long each command waits for the checks;
- [Stopping a run](docs/reference.md#stopping-a-run) with Ctrl-C or SIGTERM;
- [Refusals](docs/reference.md#refusals), every precondition each command checks;
- [JSON output](docs/reference.md#json-output), the object `--json` prints and each command's
  payload;
- [The guard](docs/reference.md#the-guard): `ils guard`, `ils parse`, and `ils hook`, and every
  rule;
- [Terminal output](docs/reference.md#terminal-output): the spinner, step lines, color, and `-d`.

## Contributing

[VISION.md](VISION.md) states what `ils` is for and ranks the principles that decide what it takes
on. Read it before you propose a change.

The tasks need [Task](https://taskfile.dev), [prek](https://github.com/j178/prek), and
[dprint](https://dprint.dev/install/). `task --list` shows them all. `task check` runs every
pre-commit hook (format, lint, spelling) and `task test` runs the tests; run both before pushing.

## License

[MIT](LICENSE)
