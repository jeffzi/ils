# ils

`ils` (Instrument Landing System) lands a change on GitHub in fixed, checked steps. A large
language model (LLM) agent that runs it cannot skip a step or push to the wrong place. Each
command runs only standard `git` and `gh` commands, prints what it did and what to do next, and
keeps no state of its own.

This version has two commands. `ils push` pushes a branch, opens its pull request, and waits for
the checks. `ils merge` squash-merges that pull request once its checks are green. `branch`,
`status`, and `release` are coming.

- [Example](#example)
- [Install](#install)
  - [Requirements](#requirements)
  - [Build the binary](#build-the-binary)
- [Start a repository](#start-a-repository)
- [Usage](#usage)
  - [Stopping a run](#stopping-a-run)
  - [What it refuses](#what-it-refuses)
  - [Exit codes](#exit-codes)
    - [When no check reports](#when-no-check-reports)
  - [JSON output](#json-output)
  - [Terminal output](#terminal-output)
  - [After a green push](#after-a-green-push)
- [Merge](#merge)
  - [What merge refuses](#what-merge-refuses)
  - [How merge ends](#how-merge-ends)
  - [A merge made elsewhere](#a-merge-made-elsewhere)
  - [Merge JSON output](#merge-json-output)
- [Use with Claude Code](#use-with-claude-code)
- [Contributing](#contributing)
- [License](#license)

## Example

```console
$ ils push
Pushed tidy-readme at 4f2a9c1d0e5b7a8c6f3e2d1b0a9c8e7f6d5b4a39
Opened pull request #12 against main, titled tidy-readme with no body: https://github.com/you/repo/pull/12
Checks for 4f2a9c1d0e5b7a8c6f3e2d1b0a9c8e7f6d5b4a39: 3 passed, 0 skipped
GitHub: CLEAN (clean)
Next: write the pull request's title and body as the squash commit's message with …
```

The `Next:` line is cut short here; it goes on to name the `gh pr edit` command and `ils merge`,
as [After a green push](#after-a-green-push) shows. Every result that leaves something to do ends
with one `Next:` line (a `next` field with `--json`). A merged result has nothing left to do: its
text has no `Next:` line, and its JSON carries `"next": "none"`. The exit code says which reaction
fits, so a caller that reads no prose still reacts correctly.

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

From a clone, `task install` builds the binary and copies it to the directory `go install` writes
to:

```bash
task install
```

## Start a repository

`ils` needs an `origin` before it can do anything. Set one up once, by hand:

1. Create the repository on GitHub and push to it:

   ```bash
   gh repo create --source . --push
   ```

2. Protect the default branch with a ruleset. In the repository on GitHub, open Settings, then
   Rules, then Rulesets. Create a branch ruleset that targets the default branch with these rules:

   - require a pull request before merging, with squash as the only merge method;
   - require status checks to pass;
   - block force pushes.

   If your CI runs outside GitHub Actions, make the ruleset require its checks. Without that,
   `ils` treats the repository as having no CI.

   GitHub's guide to
   [creating rulesets for a repository](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository)
   has the details. Rulesets need a public repository or a paid GitHub plan; on a free private
   repository skip this step. `ils` works either way. `ils merge` refuses a red merge on its own;
   the ruleset is what blocks one made by hand.

## Usage

```bash
ils push          # push the branch, open its pull request on the first push, wait for the checks
ils push --json   # the same result as one JSON object
ils push --help   # usage of push
ils push -d       # the same push, tracing each git and gh call and wait decision on stderr
ils push --no-color  # the same push, with no color
ils merge         # squash-merge the pull request once its checks are green
ils merge --json  # the same result as one JSON object
ils help          # usage of ils and its commands
ils help push     # the same as ils push --help
ils --version     # print the version of ils
```

Run `ils push` on a work branch with everything committed. It:

1. checks every precondition before it pushes or opens anything, and refuses with a reason when
   one does not hold;
2. pushes the branch by name, never with a force flag;
3. opens a pull request against origin's default branch when the branch has none, titled with the
   branch name and with an empty body;
4. waits for the checks of the pushed commit until you stop it or 100 seconds have passed since the
   command started, then prints the verdict.

Run it again at any time: on a commit that is already pushed it pushes nothing and reports the
verdict again.

When another commit reaches the pull request while `ils push` waits, the checks GitHub reports are
no longer those of the pushed commit. `ils push` then prints no verdict and ends refused, naming
the pull request's new head, and `Next:` says to pull the new commit, then run `ils push` again.
The push itself already happened.

### Stopping a run

Ctrl-C or SIGTERM during `ils push` or `ils merge` stops the step in progress. `ils` then prints
the result of that step, as text or as JSON per `--json`, and exits with that result's code. The
output says that `ils` was interrupted, and `Next:` says what to run. As at a normal end, the
spinner line is erased before the result prints; see [Terminal output](#terminal-output).

A second Ctrl-C or SIGTERM ends `ils` at once, as that signal ends any program: no result is
printed, and the exit status is the signal's (130 for Ctrl-C and 143 for SIGTERM in most shells),
not one of the [exit codes](#exit-codes).

### What it refuses

When `ils push` refuses, it has pushed nothing, opened nothing on GitHub, and changed no branch
or file; at most it has fetched from origin. It refuses:

- the default branch, and a detached HEAD;
- a tracked file with uncommitted changes (untracked files are fine);
- a branch with no commit past origin's default branch;
- a branch that is behind, or has diverged from, its copy on GitHub;
- a branch whose first commit past the default branch is also on another local branch, unless its
  copy on GitHub already carries that commit. `ils` cannot tell which branch was cut from which. It
  tells you to land each branch that shares the commit first, or to remove each one that was cut
  from this branch. `ils` reads the copy on GitHub first, so a branch that is also behind or
  diverged from that copy gets that refusal instead;
- a branch whose pull request already merged, or is open against another base;
- a repository with no remote named `origin`, or an `origin` that cannot be reached;
- an `origin` whose fetch and push URLs do not name one repository (they differ, or one is not an
  `https://`, `ssh://git@`, or `git@host:` repository URL);
- a missing `git` or `gh`;
- a `git` older than 2.34.1, or one whose version `ils` cannot read;
- a `gh` older than 2.73.0, or one whose version `ils` cannot read;
- a `gh` not logged in to origin's host (this is also how `ils` refuses a host that is not GitHub);
- a fork, or a repository you cannot push to;
- any word after `push` other than `--json`, `-d` or `--debug`, `--no-color`, `--help`, or `-h`,
  and a `--json=<value>`, `--debug=<value>`, or `--no-color=<value>` whose value is not `true` or
  `false`;
- a precondition read that the time limit, Ctrl-C, or SIGTERM cut short: run `ils push` again.

### Exit codes

| Exit | Means                | Reaction                                                                                                                                                                                                                         |
| ---- | -------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 0    | done                 | Push: the checks are green, or the repository has no continuous integration (CI) and a line says nothing verified the commit; write the pull request's title and body. Merge: the pull request is merged and the branch deleted. |
| 1    | broke                | The output says what was done and what was not. Fix what failed, then follow `Next:`.                                                                                                                                            |
| 2    | refused              | Nothing was pushed, opened, or merged, except by a push whose pull request's head moved during the wait. The reason says why; `Next:` says what to do.                                                                           |
| 3    | ask                  | Reserved; not used by this version.                                                                                                                                                                                              |
| 4    | pending              | GitHub had not finished when the command stopped waiting, for example checks still running or not yet reported. Run `ils push` again; a merge that was already done elsewhere names `ils merge` instead.                         |
| 5    | delivered, not green | Push only. The push happened; a check is red or missing, or the branch conflicts with or is behind the default branch. Follow `Next:`.                                                                                           |

Every outcome but done prints its reason on the line above `Next:`, labeled by outcome:
`Refused:`, `Broke:`, `Pending:`, or `Delivered, not green:`. A run stopped by Ctrl-C or SIGTERM
exits with the code of the result it prints; see [Stopping a run](#stopping-a-run).

#### When no check reports

A repository has CI when GitHub has an active Actions workflow whose file is on the default
branch, or when a ruleset on the default branch requires checks. An external CI that no ruleset
requires counts as no CI.

When no check reports for the pushed commit, the exit depends on whether the repository has CI:

- **No CI on the default branch:** it prints a line saying that nothing verified the commit. It
  then ends as a green push does: exit 0, or exit 5 when the branch conflicts with or is behind the
  default branch.
- **CI on the default branch:** it stops at exit 5 when no check reported for the pushed commit,
  since the CI did not run for the pull request.

### JSON output

`ils push --json` prints one JSON object with these fields:

| Field       | Type             | Meaning                                                                                                                              |
| ----------- | ---------------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| `command`   | string           | The command that ran, such as `push`.                                                                                                |
| `words`     | array of strings | The command-line words `ils` received. Absent when empty.                                                                            |
| `outcome`   | string           | `done`, `broke`, `refused`, `ask`, `pending`, or `delivered-not-green`: the Means column of [Exit codes](#exit-codes), with hyphens. |
| `next`      | string           | What to do next, as on the `Next:` line.                                                                                             |
| `reason`    | string           | Why the command did not end as `done`. Present on every other outcome.                                                               |
| `excerpt`   | string           | Third-party text, such as part of a failed check's log. Absent when empty.                                                           |
| `lines`     | array of strings | What happened, in order; never a line that only restates the reason. Absent when empty.                                              |
| `exit_code` | integer          | The process exit code.                                                                                                               |
| `payload`   | object           | The command's structured detail; a push puts it under `payload.push`. Absent when empty.                                             |

`payload.push` has these fields:

| Field         | Type             | Meaning                                                                                                                    |
| ------------- | ---------------- | -------------------------------------------------------------------------------------------------------------------------- |
| `url`         | string           | The pull request's URL.                                                                                                    |
| `number`      | integer          | The pull request's number.                                                                                                 |
| `sha`         | string           | The pushed commit.                                                                                                         |
| `verdict`     | string           | `green`, `red`, `pending`, or `none`.                                                                                      |
| `merge_state` | string           | `clean`, `behind`, `conflicting`, `draft`, `blocked-by-review`, `blocked-by-missing-check`, `blocked-other`, or `unknown`. |
| `checks`      | array of objects | One per check, with `name`, `bucket` (`pass`, `fail`, `pending`, `skipped`, or `canceled`), `workflow`, and `link`.        |
| `failed_log`  | string           | The log of a failed check. Absent when empty.                                                                              |

### Terminal output

At a terminal, `ils push` and `ils merge` show their progress as they run:

- **Spinner:** when stderr is a terminal and `-d` is off, one line on stderr names the running
  step and how long it has run, and is erased before anything else prints.
- **Step lines:** when stdout is a terminal, each step prints on stdout as soon as it succeeds,
  with a check mark and its duration. The result then prints only its other lines, its excerpt,
  the reason, and `Next:`.
- **Color:** a stream that is a terminal gets color: outcome labels are red or yellow, and branch
  names, pull request numbers, and `ils` commands are bold blue.
- **Pending hint:** when stdout is a terminal, the `Next:` line of a pending result ends with
  `# waits again; nothing to fix`.

A stream that is not a terminal gets none of this. Stdout prints the whole result at the end. Stderr
stays empty unless `-d` is on, or `ils` cannot print its result and reports that failure there.
`--json` output is never colored or streamed. `TERM=dumb` counts as no terminal.

`NO_COLOR` and `--no-color` turn color and bold off on both streams, help included. `CLICOLOR_FORCE`
turns color on for a stream that is not a terminal; `NO_COLOR` and `--no-color` override it.

`-d` (or `--debug`) prints one line on stderr for each `git` and `gh` call and each wait decision,
timed from when the command started, in place of the spinner. It changes neither stdout nor the
exit code.

### After a green push

The pull request's title and body become the squash commit's message, so write them first. The
title must be a conventional commit subject, such as `fix(cli): reject an empty name`:

```bash
gh pr edit --repo <host>/<owner>/<name> <N> --title "<title>" --body "<body>"
```

Then run `ils merge`. `ils push` prints the `gh pr edit` command on its `Next:` line with the
repository and number filled in; the title and body stay placeholders for you to write. When the
pull request needs a review, `Next:` says to request one first.

To merge a pull request whose checks are red anyway, merge it by hand. `ils merge` never does:

```bash
gh pr merge --repo <host>/<owner>/<name> <N> --squash --delete-branch \
  --match-head-commit <sha> --subject "<title> (#<N>)" --body "<body>"
```

## Merge

Run `ils merge` on the branch whose pull request you want to land. It:

1. checks every precondition before it writes anything, and refuses with a reason when one does
   not hold;
2. waits for the checks of the pull request's head, as `ils push` does, until you stop it or 80
   seconds have passed since the command started;
3. squash-merges the pull request, with its title plus `(#<N>)` as the subject and its body as
   the commit body, followed by the trailer block GitHub adds (`Signed-off-by` and
   `Co-authored-by` lines);
4. deletes the branch on origin and in the clone, and leaves the default branch checked out at
   origin's tip.

`ils merge` never pushes and has no `--force`. A commit that is not on the pull request yet is
refused: push it with `ils push` first. When the checks are still running, or fewer than 20
seconds are left for the merge, `ils merge` ends pending with nothing written, and `Next:` says to
run `ils push`, which waits for the checks and then names `ils merge` again.

### What merge refuses

When `ils merge` refuses, it has merged nothing, deleted nothing, and changed no branch or file;
at most it has fetched from origin. It refuses everything `ils push` refuses about the tools,
`origin`, and the repository, and also:

- the default branch, a detached HEAD, and a tracked file with uncommitted changes;
- a local default branch with commits that origin's lacks;
- a branch with no commit past origin's default branch, or with no open pull request against it;
- a branch whose tip is behind, past, or diverged from the head of its pull request;
- a pull request that is a draft, or open against another base;
- a pull request whose title is not a conventional commit subject (`<type>(<scope>)!: <summary>`,
  with the scope and `!` optional);
- a repository whose settings forbid squash merges, and a default branch whose ruleset forbids
  them;
- a red or canceled check, a required check that never reported, and a head no check reported for
  in a repository with CI;
- a pull request that conflicts with or is behind the default branch, needs a review, lacks a
  required check, or that GitHub blocks for another reason.
- a pull request whose head moved to another commit while `ils merge` waited for its checks:
  `Next:` says to pull the new commit, then run `ils merge` again.

### How merge ends

After the merge, `ils merge` deletes the branch on origin. It then switches to the default branch,
deletes the local branch, and fast-forwards the default branch to origin's tip: a fetch, then
`git merge --ff-only`.

When the default branch is checked out in another worktree, `ils merge` leaves that worktree
alone. It detaches the current worktree at origin's default branch instead, deletes the local
branch, and says that the other worktree updates on its next `git pull --ff-only`.

When a step of that local ending fails, the output lists the steps that ran, the one that failed
with git's message, and the steps not run, and `Next:` gives what is left to run. When the delete
on origin fails and origin still lists the branch, the output says the merge went through and the
branch is still on origin, and `Next:` is `ils merge`.

When Ctrl-C or SIGTERM stops `ils merge`, the output says that `ils` was interrupted, and `Next:`
says what to run.

### A merge made elsewhere

When the branch's pull request is already merged, on the web or by a run of `ils merge` that was
cut short, `ils merge` merges nothing and reads no check. It deletes the branch on origin if it is
still there, then ends as above. This also covers a pull request that got an "Update branch" or a
committed suggestion on the web before its merge. When the branch has commits the merge did not
carry, `ils merge` refuses and lists them, and `Next:` says to start a new branch and cherry-pick
them.

### Merge JSON output

`ils merge --json` prints the same object as `ils push --json`, with the detail under
`payload.merge`. A refusal made before the pull request is known has no payload.

| Field         | Type             | Meaning                                                                                                                                                                     |
| ------------- | ---------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `url`         | string           | The pull request's URL.                                                                                                                                                     |
| `number`      | integer          | The pull request's number.                                                                                                                                                  |
| `sha`         | string           | The head that was merged, or found merged.                                                                                                                                  |
| `verdict`     | string           | As in `payload.push`; `none` when no deciding check reported (the required checks when the default branch requires any, every check otherwise) or the checks were not read. |
| `merge_state` | string           | As in `payload.push`; `unknown` when it was not read.                                                                                                                       |
| `checks`      | array of objects | As in `payload.push`, the deciding checks only; empty when none reported or the checks were not read.                                                                       |
| `subject`     | string           | The squash commit's subject, once composed after the wait; empty before.                                                                                                    |
| `body`        | string           | The squash commit's body, once composed after the wait; empty before.                                                                                                       |
| `merged`      | boolean          | Whether this run made the merge.                                                                                                                                            |
| `ending`      | string           | `trunk` or `detached` once every step of the local ending ran; empty otherwise.                                                                                             |

## Use with Claude Code

The repository ships one agent skill, `ils`. Install it with
[`skills`](https://github.com/vercel-labs/skills):

```bash
npx skills add jeffzi/ils
```

With the skill installed, `/ils push` and `/ils merge` run the binary and relay its output, and
the skill tells an agent how to react to each exit code. The merge stays the user's decision: the
skill runs `ils merge` only when you ask for it.

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

## Contributing

The tasks need [Task](https://taskfile.dev), [prek](https://github.com/j178/prek), and
[dprint](https://dprint.dev/install/). `task --list` shows them all. `task check` runs every
pre-commit hook (format, lint, spelling) and `task test` runs the tests; run both before pushing.

## License

[MIT](LICENSE)
