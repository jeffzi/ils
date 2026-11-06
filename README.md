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

```console
$ ils push
Pushed tidy-readme at 4f2a9c1
Opened pull request #12 against main, titled tidy-readme with no body: https://github.com/you/repo/pull/12
Checks for 4f2a9c1: 3 passed
Next: write the pull request's title and body as the squash commit's message with …
```

The `Next:` line is cut short here; it goes on to name the `gh pr edit` command and `ils merge`,
as [After a green push](docs/reference.md#after-a-green-push) shows. Every result that leaves
something to do ends with one `Next:` line (a `next` field in the JavaScript Object Notation (JSON)
that `--json` prints). A merged result has nothing left to do: its text has no `Next:` line, and its
JSON carries `"next": "none"`. The exit code says which reaction fits, so a caller that reads no
prose still reacts correctly.

Each run ends with one of six outcomes, and each outcome has its own exit code: `done` (0), `broke`
(1), `refused` (2), `ask` (3, from the guard only), `pending` (4), or `delivered, not green` (5).
[Exit codes](docs/reference.md#exit-codes) says what each means.

## Commands

```bash
ils branch <name>     # start a branch from the default branch as origin has it
ils push              # push the branch, open its pull request, wait for the checks
ils merge             # squash-merge the pull request once its checks are green
ils status            # where the checkout stands, and what to run next
ils release <X.Y.Z>   # tag the release commit once its checks are green, then publish it
```

[`ils branch <name>`](docs/reference.md#ils-branch) creates the branch at origin's copy of the
default branch and switches to it, carrying your uncommitted changes along. New work never starts
from a stale local default branch, or from another work branch whose commits would ride along into
the new pull request.

[`ils push`](docs/reference.md#ils-push) pushes the branch by name, never with a force flag, opens
its pull request on the first push, and waits for the checks of the pushed commit. It refuses the
default branch, uncommitted changes, a branch behind or diverged from its copy on origin, and a
branch that already merged. A red check, a conflict, or a required check that never ran ends at
exit 5, never as green.

[`ils merge`](docs/reference.md#ils-merge) squash-merges the pull request with its title and body
as the commit message, deletes the branch, and leaves the default branch checked out and up to
date. It never pushes and has no `--force`. It refuses a red or missing check, a draft, a review
still required, a commit not yet on the pull request, and a title that is not a conventional commit
subject. After a merge done on the web, it finishes the local steps.

[`ils status`](docs/reference.md#ils-status) says where the checkout stands and what to run next.
It changes nothing and exits 0 whenever its arguments are valid.

[`ils release <X.Y.Z>`](docs/reference.md#ils-release) finds the commit `chore: release v<X.Y.Z>`
on origin's default branch, waits for its checks, tags it, pushes the tag, and creates the GitHub
release from that version's section of `CHANGELOG.md`. It refuses a version that is not above every
tag on origin, a missing changelog section, and a failed check.

Every command takes `--json`, `-d` for a trace of each `git` and `gh` call, and `--help`.
[Flags](docs/reference.md#flags) lists them all.

## The flow

| Step | What happens                                                               | Command               | Who runs it  |
| ---- | -------------------------------------------------------------------------- | --------------------- | ------------ |
| 1    | Start a branch from origin's default branch                                | `ils branch <name>`   | the agent    |
| 2    | Commit                                                                     | `git commit`          | the agent    |
| 3    | Push, open the pull request, wait for the checks; again until green        | `ils push`            | the agent    |
| 4    | Write the pull request's title and body, which become the squash's message | `gh pr edit`          | agent or you |
| 5    | Squash-merge the pull request and delete the branch                        | `ils merge`           | you          |
| 6    | Tag and publish a release, when you want one                               | `ils release <X.Y.Z>` | you          |

The agent runs steps 1 to 4 on its own: the worst a push leaves behind is a pull request with red
checks. The merge and the release change what other people see, so they wait for your word.
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
`ils branch <name>`: the default branch already holds the first change. When it has not merged,
keep committing on the same branch. `ils push` updates the same pull request, and its title and
body are written once, at merge time, for the whole branch.

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

The guard judges a shell command before an agent runs it. It parses the command with a bash parser,
finds the `git`, `gh`, and `ils` calls in it, nested ones included, and answers allow, refuse, or
ask. It refuses `git` operations that destroy work or skip the hooks, every `git push`, commits on
the default branch, and `gh` calls that land or publish a change without the checks of `ils`.

It asks before `ils merge`, `ils release`, `gh pr merge`, and `gh release create`, and only when
the command is that one call by its bare name, so the prompt shows exactly what runs. It refuses
them in any other form, such as piped or behind a wrapper. It allows `ils push` and `ils branch`
only as the whole command, so that a pipe never hides their exit code.

The guard lowers the odds of an accident; it is not a sandbox. It runs on macOS and Linux; on
Windows its commands refuse. [Wire the guard into Claude Code](#wire-the-guard-into-claude-code)
sets it up. [The guard](docs/reference.md#the-guard) in the reference lists every rule, and
[What the guard does not see](docs/reference.md#what-the-guard-does-not-see) lists its limits.

## Install

### Requirements

To build:

- Go 1.27 or later.
- [Task](https://taskfile.dev), to install from a clone.

To run:

- [`git`](https://git-scm.com/downloads) 2.34.1 or later, on `PATH`.
- [`gh`](https://cli.github.com) 2.73.0 or later, installed and logged in to the host of `origin`
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

The repository ships two agent skills: `ils`, which runs the binary, and `write-release`, which
prepares the release commit. Install them with [`skills`](https://github.com/vercel-labs/skills):

```bash
npx skills add jeffzi/ils
```

With the skills installed, `/ils status`, `/ils branch <name>`, `/ils push`, `/ils merge`, and
`/ils release <X.Y.Z>` run the binary and relay its output. The `ils` skill tells an agent how to
react to each exit code. The merge and the release stay the user's decision: the skill runs
`ils merge` and `ils release` only when you ask for them. Asked for a release, an agent uses
`write-release` to land the release commit through `ils branch`, `ils push`, and `ils merge`, then
tells you to run `ils release <X.Y.Z>`.

To make Claude Code ask you before every merge or release, add `ask` rules to your settings
(`~/.claude/settings.json`, or the project's `.claude/settings.json`):

```json
{
  "permissions": {
    "ask": [
      "Bash(ils merge:*)",
      "Bash(ils release:*)",
      "Bash(gh pr merge:*)",
      "Bash(gh release create:*)"
    ]
  }
}
```

### Wire the guard into Claude Code

Add a `PreToolUse` hook on `Bash` that runs `hook.sh` from the installed `ils` skill, here at its
global install path. `onFailure` needs Claude Code 2.1.295 or later:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "bash ~/.claude/skills/ils/hook.sh",
            "timeout": 15,
            "onFailure": "block"
          }
        ]
      }
    ]
  }
}
```

`hook.sh` runs `ils hook claude` and blocks the command whenever `ils` cannot answer: when it is
not installed or cannot start, and when it exits with a code the hook contract does not know.

A missing or broken binary blocks every command. Reinstall it from Claude Code's prompt with a `!`
command, which skips the hook: `! go install github.com/jeffzi/ils@latest`, or `! task install`
from a clone of `ils`.

Keep the four `ask` rules from [Use with Claude Code](#use-with-claude-code) beside it, and add
`deny` rules for the commands you never want run, such as `Bash(git push:*)`
and `Bash(gh auth token:*)`: the permission rules are the first layer and the guard the second.

The guard reads the default branch from origin and falls back to the copy `git fetch` keeps. In a
repository made with `gh repo create --source . --push`, run `git remote set-head origin --auto`
once, so that the guard has that copy when origin cannot be reached. With git 2.48 or later, `git
fetch origin` also works. Run it as a `!` command or outside the agent, since the guard refuses
`set-head` on origin.

## Reference

[docs/reference.md](docs/reference.md) holds the details a caller looks up:

- [Commands](docs/reference.md#commands), each command's steps and flags, and the by-hand steps
  for `ils branch`, a red merge, and `ils release`;
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

The tasks need [Task](https://taskfile.dev), [prek](https://github.com/j178/prek), and
[dprint](https://dprint.dev/install/). `task --list` shows them all. `task check` runs every
pre-commit hook (format, lint, spelling) and `task test` runs the tests; run both before pushing.

## License

[MIT](LICENSE)
