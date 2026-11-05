# ils reference

This page is the full reference for `ils`: exit codes, time limits, every refusal, the JavaScript
Object Notation (JSON) output, the guard, and the terminal output. The [README](../README.md) shows
how to install `ils` and what each command does. CI stands for continuous integration throughout.

- [Exit codes](#exit-codes)
  - [When no check reports](#when-no-check-reports)
- [Time limits](#time-limits)
- [Stopping a run](#stopping-a-run)
- [Refusals](#refusals)
  - [What branch refuses](#what-branch-refuses)
  - [What push refuses](#what-push-refuses)
  - [What merge refuses](#what-merge-refuses)
  - [What status refuses](#what-status-refuses)
  - [What release refuses](#what-release-refuses)
- [JSON output](#json-output)
  - [Push JSON output](#push-json-output)
  - [Merge JSON output](#merge-json-output)
  - [Status JSON output](#status-json-output)
  - [Release JSON output](#release-json-output)
- [The guard](#the-guard)
  - [ils guard](#ils-guard)
  - [ils parse](#ils-parse)
  - [ils hook](#ils-hook)
  - [What the guard refuses](#what-the-guard-refuses)
    - [How the guard reads a command](#how-the-guard-reads-a-command)
    - [Git operations that destroy work or skip checks](#git-operations-that-destroy-work-or-skip-checks)
    - [Pushes and the default branch](#pushes-and-the-default-branch)
    - [ils and gh commands](#ils-and-gh-commands)
  - [Guard JSON output](#guard-json-output)
- [Terminal output](#terminal-output)

## Exit codes

| Exit | Means                | Reaction                                                                                                                                                                                                                                                                                                                                         |
| ---- | -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 0    | done                 | Push: the checks are green, or the repository has no CI and a line says nothing verified the commit; follow `Next:`. Merge: the pull request is merged and the branch deleted. Branch: the branch is started. Status: whatever it finds, once its words parse. Release: the tag is on origin, and the GitHub release exists unless `--tag-only`. |
| 1    | broke                | The output says what was done and what was not. Fix what failed, then follow `Next:`.                                                                                                                                                                                                                                                            |
| 2    | refused              | Nothing was pushed, opened, merged, or tagged, except by a push whose pull request's head moved during the wait. The reason says why; `Next:` says what to do.                                                                                                                                                                                   |
| 3    | ask                  | `ils guard` only: the command is one the user approves before it runs. See [ils guard](#ils-guard).                                                                                                                                                                                                                                              |
| 4    | pending              | GitHub had not finished when the command stopped waiting, for example checks still running or not yet reported. Run the command that the `Next:` line names; [Time limits](#time-limits) says how long each command waits. After an `ils release`, run the same command again.                                                                   |
| 5    | delivered, not green | Push only. The push happened; a check is red or missing, or the branch conflicts with or is behind the default branch. Follow `Next:`.                                                                                                                                                                                                           |

Every outcome but done prints its reason labeled by outcome: `Refused:`, `Broke:`, `Ask:`,
`Pending:`, or `Delivered, not green:`. The reason sits on the line above `Next:`, except for a
verdict of `ils guard`, which prints no `Next:` line. When `ils guard` refuses its own words (no
`--cwd`, or not exactly one word after `--`), it prints `Next: ils guard --help`. A run stopped by
Ctrl-C or SIGTERM (the signal `kill` sends by default) exits with the code of the result it
prints; see [Stopping a run](#stopping-a-run).

### When no check reports

A repository has CI when GitHub has an active Actions workflow whose file is on the default
branch, or when a ruleset on the default branch requires checks. An external CI that no ruleset
requires counts as no CI.

When no check reports for the pushed commit, the exit depends on whether the repository has CI:

- **No CI on the default branch:** it prints a line saying that nothing verified the commit. It
  then ends as a green push does: exit 0, or exit 5 when the branch conflicts with or is behind the
  default branch.
- **CI on the default branch:** it stops at exit 5 when no check reported for the pushed commit,
  since the CI did not run for the pull request.

## Time limits

`ils push`, `ils merge`, and `ils release` wait for the checks until a time limit, counted from
when the command started. Stdout and `--wait` pick the limit:

| Command       | At a terminal, or with `--wait` | Otherwise, or with `--wait=false` |
| ------------- | ------------------------------- | --------------------------------- |
| `ils push`    | 15 minutes                      | 100 seconds                       |
| `ils merge`   | 14 minutes 40 seconds           | 80 seconds                        |
| `ils release` | 14 minutes 40 seconds           | 80 seconds                        |

`ils merge` and `ils release` stop waiting 20 seconds before the full limit of 15 minutes or 100
seconds. Those 20 seconds are kept for the writes that follow. When fewer than 20 seconds are left,
they end pending with nothing written.

`ils push` waits until GitHub shows the pushed commit as the pull request's head. When another
push moves that head past the pushed commit before GitHub showed it, `ils` cannot tell the move
from GitHub being slow: it waits until the time limit and ends pending, and the wait itself
writes nothing. The next `ils push` refuses the branch as behind its copy on origin.

When a pending `ils merge` names `ils push` on its `Next:` line, run that push with the same
`--wait` as the merge, so that it takes the same column of the table above. If your script or
harness (the program that runs the agent's tool calls) gives `ils` a terminal but stops it on a
timeout shorter than 15 minutes, pass `--wait=false`. An `ils merge` or `ils release` stopped
between two of its writes continues on the next run.

## Stopping a run

Ctrl-C or SIGTERM during `ils push`, `ils merge`, or `ils release` stops the step in progress. `ils`
then prints the result of that step, as text or as JSON per `--json`, and exits with that result's
code. The output says that `ils` was interrupted, and `Next:` says what to run. As at a normal end,
the spinner line is erased before the result prints; see [Terminal output](#terminal-output).

If `ils release` is stopped while it reads the repository, origin, or the default branch, it
refuses with nothing written. This happens before it waits for checks, and
[What push refuses](#what-push-refuses) lists these refusals.

If it is stopped while it waits for the checks of the release commit, it ends pending with nothing
written, as `ils merge` does. The exception is a check of that commit that has already failed or
been canceled: `ils release` then refuses on that check, as
[What release refuses](#what-release-refuses) lists.

Only when no check reported for the release commit does `ils release` read the rules of the default
branch and whether the repository has CI. If it is stopped during either read, it refuses with
nothing written.

If it is stopped after its last read and before its first write, it ends pending with nothing
written. If it is stopped while it makes the tag or looks up the GitHub release, it says that it
was interrupted, or which time limit ran out, as it does for the push of the tag.

A second Ctrl-C or SIGTERM ends `ils` at once, as that signal ends any program: no result is
printed, and the exit status is the signal's (130 for Ctrl-C and 143 for SIGTERM in most shells),
not one of the [exit codes](#exit-codes).

## Refusals

A refusal ends with exit 2: the command wrote nothing, and the reason says why. The one exception
is an `ils push` whose pull request's head moved during the wait. That push has already pushed, as
[What push refuses](#what-push-refuses) says.

### What branch refuses

When `ils branch` refuses, it has created no branch and switched nothing; at most it has fetched
from origin. It refuses:

- no name, a second word, or `--`;
- any flag other than `--json`, `-d` or `--debug`, `--no-color`, `--help`, or `-h`; a
  `--json=<value>`, `-d=<value>`, `--debug=<value>`, or `--no-color=<value>` whose value is not
  `true` or `false`; and a `--help=<value>` or `-h=<value>` whose value is not a boolean such as
  `true`, `false`, `1`, or `0`;
- a name git cannot hold as a branch, including one that starts with `-` and `@{-1}`;
- a name a branch already takes, locally or on origin. A branch takes the name when it has the
  same name, or collides with it by path, which git cannot hold beside it: `fix` takes `fix/help`,
  and `fix/help` takes `fix`. A branch whose name only ends with the name (`team/fix` for `fix`)
  does not take it. `ils branch` asks origin at the time of the run, so a branch pushed since the
  last fetch counts and one deleted on origin does not;
- a detached HEAD with commits origin's default branch lacks: it lists them, and `Next:` says to
  keep them on a branch with `git switch -c <name>`, then run `ils push`;
- a switch git refuses because a changed or untracked file would be overwritten: it names the
  files;
- a missing `git`, a `git` older than 2.34.1 or one whose version `ils` cannot read, a missing
  `origin`, and an `origin` that cannot be reached;
- a precondition read that the time limit, Ctrl-C, or SIGTERM cut short: run the same command again.

### What push refuses

When `ils push` refuses, it has pushed nothing, opened nothing on GitHub, and changed no branch
or file; at most it has fetched from origin. The one exception is the moved head at the end of
this list, which `ils push` finds after the push. It refuses:

- the default branch, and a detached HEAD;
- a tracked file with uncommitted changes (untracked files are fine);
- a branch with no commit past origin's default branch;
- a branch that is behind, or has diverged from, its copy on origin;
- a branch whose first commit past the default branch is also on another local branch, unless its
  copy on origin already carries that commit. `ils` cannot tell which branch was cut from which. It
  tells you to land each branch that shares the commit first, or to remove each one that was cut
  from this branch. `ils` reads the copy on origin first, so a branch that is also behind or
  diverged from that copy gets that refusal instead;
- a branch whose pull request already merged into the default branch, or is open against another
  base;
- a repository with no remote named `origin`, or an `origin` that cannot be reached;
- an `origin` whose fetch and push URLs do not name one repository (they differ, or one is not an
  `https://`, `ssh://git@`, or `git@host:` repository URL);
- a missing `git` or `gh`;
- a `git` older than 2.34.1, or one whose version `ils` cannot read;
- a `gh` older than 2.73.0, or one whose version `ils` cannot read;
- a `gh` not logged in to origin's host (this is also how `ils` refuses a host that is not GitHub);
- a fork, or a repository you cannot push to;
- any word after `push` other than `--wait`, `--json`, `-d` or `--debug`, `--no-color`, `--help`,
  or `-h`, and a `--wait=<value>`, `--json=<value>`, `--debug=<value>`, or `--no-color=<value>`
  whose value is not `true` or `false`;
- a precondition read that the time limit, Ctrl-C, or SIGTERM cut short: run the same command
  again, with the `--wait` you gave it;
- a pull request whose head moved to another commit while `ils push` waited for its checks:
  `Next:` says to pull the new commit, then run `ils push` again.

An `origin` can answer and still turn the call away, for a failed sign-in, a missing permission, or
a repository it cannot find. That is not a refusal. A command whose call to origin is turned away
before it lands anything ends broke with git's message and `Next: fix what failed, then run
<command> again`, and `ils status` lists the read as failed. The steps `ils merge` runs after the
squash merge keep the `Next:` each gives for any failure, since the merge itself is done. Only an
`origin` that cannot be reached at all ends as described above.

### What merge refuses

When `ils merge` refuses, it has merged nothing, deleted nothing, and changed no branch or file;
at most it has fetched from origin. It refuses everything `ils push` refuses about the tools,
`origin`, and the repository, and also:

- any word after `merge` other than `--wait`, `--json`, `-d` or `--debug`, `--no-color`, `--help`,
  or `-h`, and a `--wait=<value>`, `--json=<value>`, `--debug=<value>`, or `--no-color=<value>`
  whose value is not `true` or `false`;
- the default branch, a detached HEAD, and a tracked file with uncommitted changes;
- a local default branch with commits that origin's lacks;
- a branch with no commit past origin's default branch, or with no open pull request against it;
- a branch whose tip is behind, past, or diverged from the head of its pull request;
- a pull request that is a draft, or open against another base;
- a branch whose only pull request merged into another base: that never lands it, and `Next:` says
  to run `ils push`, which opens one against the default branch;
- a pull request whose title is not a conventional commit subject (`<type>(<scope>)!: <summary>`,
  with the scope and `!` optional);
- a repository whose settings forbid squash merges, and a default branch whose ruleset forbids
  them;
- a red or canceled check, a required check that never reported, and a head no check reported for
  in a repository with CI;
- a pull request that conflicts with or is behind the default branch, needs a review, lacks a
  required check, or that GitHub blocks for another reason;
- a pull request whose head moved to another commit while `ils merge` waited for its checks:
  `Next:` says to pull the new commit, then run `ils merge` again;
- on a run that finishes a pull request already merged, a branch on origin that holds commits the
  merged head does not carry: it lists them and deletes nothing, and `Next:` says to start a branch
  and cherry-pick them.

### What status refuses

`ils status` refuses only the words it is given, before it reads anything. It refuses:

- any word after `status`, and `--`;
- any flag other than `--json`, `-d` or `--debug`, `--no-color`, `--help`, or `-h`, and a
  `--json=<value>`, `--debug=<value>`, or `--no-color=<value>` whose value is not `true` or `false`.

### What release refuses

When `ils release` refuses, it has tagged nothing, pushed nothing, and created no release; at most
it has fetched from origin. It refuses everything `ils push` refuses about the tools, `origin`, and
the repository, and also:

- no version, a second word, or `--`;
- any flag other than `--force`, `--tag-only`, `--wait`, `--json`, `-d` or `--debug`, `--no-color`,
  `--help`, or `-h`, and a `--force=<value>`, `--tag-only=<value>`, `--wait=<value>`,
  `--json=<value>`, `--debug=<value>`, or `--no-color=<value>` whose value is not `true` or
  `false`;
- a version that is not `X.Y.Z` with an optional pre-release part: it refuses `v1.2.0`, `1.2`,
  `1.2.0+build`, and `latest`;
- a version that is not above every other version tag origin holds, naming the highest. Only
  origin's tags count, not the clone's;
- no release commit for the version on origin's default branch since the previous version tag;
- a tag `v<X.Y.Z>` on origin at another commit;
- a local tag `v<X.Y.Z>` at another commit, unless origin already holds the tag at the release
  commit: `Next:` says to delete it by hand with `git tag --delete v<X.Y.Z>`;
- a release commit with no `CHANGELOG.md`, no section for the version, or an empty section, with
  `--tag-only` too. With no `CHANGELOG.md`, `Next:` says to stop and ask the user to start one,
  since `ils release` then requires a section in it for every later release;
- a failed or canceled check of the release commit, and a release commit no check reported for in
  a repository with CI.

When `ils release` creates the GitHub release but cannot remove the notes file it wrote, it still
ends done. The output gives the release's URL and a line naming the file left behind.

## JSON output

With `--json`, a command prints one JSON object with these fields:

| Field       | Type             | Meaning                                                                                                                                                                                                                   |
| ----------- | ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `command`   | string           | The command that ran, such as `push`.                                                                                                                                                                                     |
| `words`     | array of strings | The command-line words `ils` received, less the output flags: `-d`, `--debug` and `--no-color` in any form, and `--json` bare or as `--json=true` or `--json=false`. A refused `--json=<value>` stays. Absent when empty. |
| `outcome`   | string           | `done`, `broke`, `refused`, `ask`, `pending`, or `delivered-not-green`: the Means column of [Exit codes](#exit-codes), with hyphens.                                                                                      |
| `next`      | string           | What to do next, as on the `Next:` line.                                                                                                                                                                                  |
| `reason`    | string           | Why the command did not end as `done`. Present on every other outcome.                                                                                                                                                    |
| `excerpt`   | string           | Third-party text: the end of a failed check's log, or of what the hooks and `git` printed for a push `git` refused. Absent when empty.                                                                                    |
| `lines`     | array of strings | What happened, in order; never a line that only restates the reason. Absent when empty.                                                                                                                                   |
| `exit_code` | integer          | The process exit code.                                                                                                                                                                                                    |
| `payload`   | object           | The command's structured detail, under the command's name, such as `payload.push`. Absent when empty.                                                                                                                     |

### Push JSON output

`ils push --json` prints the [JSON object](#json-output) every command prints, with the detail
under `payload.push`:

| Field         | Type             | Meaning                                                                                                                                                |
| ------------- | ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `url`         | string           | The pull request's URL.                                                                                                                                |
| `number`      | integer          | The pull request's number.                                                                                                                             |
| `sha`         | string           | The pushed commit.                                                                                                                                     |
| `verdict`     | string           | `green`, `red`, `pending`, or `none`.                                                                                                                  |
| `merge_state` | string           | `clean`, `behind`, `conflicting`, `draft`, `blocked-by-review`, `blocked-by-missing-check`, `blocked-by-running-check`, `blocked-other`, or `unknown`. |
| `checks`      | array of objects | One per check, with `name`, `bucket` (`pass`, `fail`, `pending`, `skipped`, or `canceled`), `workflow`, and `link`.                                    |
| `failed_log`  | string           | The log of a failed check. Absent when empty.                                                                                                          |

### Merge JSON output

`ils merge --json` prints the [JSON object](#json-output) every command prints, with the detail
under `payload.merge`. A refusal made before the pull request is known has no payload.

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

### Status JSON output

`ils status --json` prints the [JSON object](#json-output) every command prints, with the detail
under `payload.status`:

| Field                | Type             | Meaning                                                                                                                                                                              |
| -------------------- | ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `standing`           | string           | `branch`, `trunk`, `detached`, `no-origin`, or `no-repository`.                                                                                                                      |
| `branch`             | string           | The branch HEAD is on; empty on a detached HEAD.                                                                                                                                     |
| `trunk`              | string           | Origin's default branch; empty when it could not be learned.                                                                                                                         |
| `ahead`, `behind`    | integer          | Commits ahead of and behind the branch's copy on origin, or origin's default branch on the default branch.                                                                           |
| `on_origin`          | boolean          | Whether origin has a copy of the branch.                                                                                                                                             |
| `pull_request`       | object or null   | The open pull request against the default branch: `number`, `url`, `title`, `body`, `title_ok`, `verdict`, `merge_state`, `checks`; null when there is none or it was not read.      |
| `past_pull_requests` | array of objects | Each closed or merged pull request of the branch: `number` and `state`.                                                                                                              |
| `last_tag`           | string           | On the default branch, the last version tag; empty when there is none.                                                                                                               |
| `since_tag`          | integer          | On the default branch, the commits on origin's default branch since the last tag.                                                                                                    |
| `release_commit`     | boolean          | On the default branch, whether origin's tip is a release commit (`chore: release v<X.Y.Z>`, with an optional pre-release part such as `-rc.1`, with or without a trailing `(#<N>)`). |
| `protection`         | string           | On the default branch, `protected`, `none`, or `unavailable` (the plan does not allow rules).                                                                                        |
| `unlanded`           | array of strings | On a detached HEAD, the commits origin's default branch lacks.                                                                                                                       |
| `unread`             | array of objects | One entry per read that failed: `read` names it and `error` says why. A null `pull_request` with an entry here means it was not read.                                                |

`verdict`, `merge_state`, and `checks` use the words of `payload.push`; `title_ok` says whether the
title is a conventional commit subject, as `ils merge` requires.

### Release JSON output

`ils release --json` prints the [JSON object](#json-output) every command prints, with the detail
under `payload.release`. Once `ils release` has found the release commit, every result carries it,
refusals and pending results included. A result that ends before then (a version not above
origin's tags, no release commit, origin not answering) has no payload.

| Field        | Type    | Meaning                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| ------------ | ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `version`    | string  | The version, without the `v`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| `tag`        | string  | The version tag, `v<X.Y.Z>`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `sha`        | string  | The release commit.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| `verdict`    | string  | How far the CI gate got. `green`: the checks passed. `red`: a check failed or was canceled. `pending`: the wait ended before the checks concluded, including when none had reported yet. `none`: no check or status reported; on a default branch without CI the release goes on, and with CI it refuses. `forced`: `--force` skipped the gate. `skipped`: origin already held the tag at the release commit. `unchecked`: the release stopped before the gate read the checks, or the read of the checks failed. |
| `url`        | string  | The GitHub release's URL once it was created or found; empty otherwise.                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `prerelease` | boolean | Whether the version has a pre-release part.                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `tag_only`   | boolean | Whether `--tag-only` was given.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |

## The guard

The guard judges an agent's shell command before it runs: `ils hook claude` is Claude Code's
`PreToolUse` hook, `ils guard` gives the same verdict to any caller, and `ils parse` prints how the
guard reads a command.

`ils guard`, `ils parse`, and `ils hook` run on macOS and Linux. On Windows each one refuses with
exit 2 and says the guard runs on macOS and Linux. `ils guard` and `ils parse` take the command
to read in one quoted word after `--`; no word, or more than one, is a refusal.

### ils guard

`ils guard --cwd <dir> -- '<command>'` judges a shell command as if it ran in `<dir>`:

| Exit | Verdict | Output                                                                                                                                        |
| ---- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| 0    | allow   | `The guard allows the command.`                                                                                                               |
| 2    | refuse  | `Refused:` and the reason. A command that does not parse, and any error that keeps the guard from judging, is a refusal that says what broke. |
| 3    | ask     | `Ask:` and the reason: the command is one the user approves.                                                                                  |

### ils parse

`ils parse --cwd <dir> -- '<command>'` prints every invocation the command holds, one line each:
its words, its directory, its marks, and, for `git`, the subcommand, its arguments, and the git
dir it targets. A mark names a way the guard could not read the invocation safely.

A word that is not a literal prints `<not literal>`. A directory or git dir the guard cannot tell
prints `unknown`. A git dir outside any repository prints `none`, as does an empty list of marks or
arguments. A git invocation with no subcommand leaves out the subcommand part.

It gives no verdict. The rules for git operations, pushes, and `gh` leave no mark, so use `ils
guard` for the verdict; `ils parse` exits 0 whatever it marks. It exits 1 when the command does not
parse or a state read fails (a `git config` read of an alias, the git dir), and 2 when `--cwd` or
the command word is missing.

### ils hook

`ils hook claude` is Claude Code's `PreToolUse` hook. It reads the hook's JSON on stdin and judges
`tool_input.command` from `cwd`:

- allow: exit 0, nothing printed;
- refuse: exit 2, the reason on stderr;
- ask: exit 0, with `{"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision":
  "ask", "permissionDecisionReason": "<reason>"}}` on stdout.

A tool other than `Bash` is allowed. These exit 2 with a reason on stderr:

- a payload that is not JSON;
- a payload that holds no `tool_name`, no `tool_input.command`, or no `cwd`;
- a `tool_input` that is not the JSON of a Bash call;
- a `cwd` that is not absolute, is not a directory, or cannot be read;
- every internal error.

`ils hook` never exits 1, since Claude Code runs a tool call whose hook exits with anything but 2.
`ils hook` given a harness other than `claude`, or no harness, exits 2 and names the harnesses it
knows.

`skills/ils/hook.sh` fronts `ils hook claude`. It passes exit 0 and 2 through with the binary's
output. Any other exit becomes exit 2, with a line on stderr naming the code. When `ils` is not
on `PATH` or cannot start, it exits 2 and names `! go install github.com/jeffzi/ils@latest`, and
`! task install` from a clone of `ils`.

### What the guard refuses

#### How the guard reads a command

- a command that does not parse;
- a command word that is not a literal (`$(which git)`, `"$GIT"`, `@(git)`). A glob anywhere else
  in a word is data, though zsh runs a qualifier such as `*(e:'git push':)` as code
  ([accepted limits](guard-scope.md#accepted-limits)). These rules decide when a `$NAME` or
  `${NAME}` counts as a literal:
  - **Literal assignments:** a `$NAME` or `${NAME}` the command assigned a literal earlier reads as
    that literal in the words that run later in the same shell. That includes the words of a
    subshell, a command substitution, or an `eval` string. A plain assignment counts. So do
    `export`, `declare`, `typeset`, and `readonly` with a literal value, and `local` inside its
    function. `S=/tmp/work; $S/run.sh` passes, and the guard refuses `G=git; $G push` as
    `git push`. The value holds only where the assignment always ran before the word
    (`S=/x && $S/run.sh`, `S=/x; for f in a b; do $S/run.sh; done`). It holds only across commands
    that write no variable, listed next.
  - **Commands that write no variable:** a program, a function whose body the guard walks, and
    these builtins:
    - `:`, `true`, `false`, `echo`, `test`, `[`, `cd`, `pwd`, `pushd`, `popd`, `dirs`, `exit`,
      `return`, `break`, `continue`, `shift`, `alias`, `unalias`, `hash`, `type`, `which`,
      `whence`, `where`, `umask`, `ulimit`, `kill`, `jobs`, `eval`, `source`, and `.`;
    - `printf` with no argument or whose first argument is a literal that does not start with
      `-v`;
    - `set`, `shopt`, and `wait` whose arguments are all literals, except zsh's `set -A` or `set
      +A` and `wait -p`, which write a variable: `S=/tmp/work; set -euo pipefail; $S/run.sh`
      runs `/tmp/work/run.sh`;
    - `trap` with literal arguments that lists traps, or sets or resets them only on `EXIT`, `0`,
      `INT`, `TERM`, or `HUP` (with or without `SIG`), whose trap runs only once the shell exits
      or is stopped; a trap on any other signal can run between later commands;
    - zsh's `mkdir`, `rm`, `ln`, and the other builtins of its `zsh/files` module, which write no
      variable either and count as programs.
  - **Commands that make every variable unknown:** every other builtin of bash or zsh (`read`,
    `print -v`, `vared`, `zformat`), and a command word that is not a literal, makes every
    variable unknown after it.
  - **`for` loops:** a `for NAME in` loop whose list words are all literals with no glob gives NAME
    each value in turn, read as a literal assignment would leave it. The guard judges the body once
    for each value. `for v in lua5.1 luajit; do /opt/$v/bin/lua b.lua; done` passes, and the guard
    refuses `for g in ls git; do $g push; done` as `git push`. A command gets at most 256 such
    walks of a loop body. Outside a dry run, each value counts twice: once for the dry run, once
    for the walk. Loop bodies inside function bodies and the scripts git runs count too. A loop
    past that limit leaves NAME unknown in its body. After the loop NAME is unknown.
  - **When the variable stays unknown:** after a `for NAME in` loop, as above. The variable also
    stays unknown:
    - when something other than a literal assignment writes it: `+=`, `printf -v`, a `for NAME in`
      whose list holds a word that is not a literal (`for f in *.sh`, `"$@"`, `$(ls)`),
      `${NAME:=…}`, `unset`, arithmetic, or a value that is not a literal;
    - when the assignment ran in a subshell, a pipeline stage, or a background job, or may not
      have run: `true && S=/x; $S/run.sh`, an `if` or loop body, or a function the command calls;
    - in a string another shell runs (`sh -c '$S/run.sh'`) or that the guard reads for a tool a
      command the guard does not know may run (`su -c '$S/run.sh'`), and in a function body, which
      knows only the variables it assigns itself;
    - for a variable bash or zsh defines as it starts or sets on its own (`PWD`, `PATH`, `REPLY`,
      `MATCH`, `match`, `BASH_REMATCH`), and for a name that a declaration with an option names
      anywhere in the command (`declare -x S=/x`, `declare -u S`).
      `HOME` is one of them, but a `~` or `~/…` that opens a word always reads as the caller's
      `HOME`, even after the command assigns it: `HOME=/srv; cd ~/x` reads as a `cd` into the
      caller's `~/x`. Only a command that hides its directory would assign `HOME` there
      ([rule 3](guard-scope.md#3-close-a-bypass-only-when-agents-write-it));
    - for a name made `readonly`, other than by the one `readonly` that gives it its value, and for
      that one too when another `readonly` names the variable anywhere in the command (`readonly
      S=$(pwd); readonly S=/x`);
    - for every variable, later literal assignments included, after a `source`, after a command
      word that is not a literal (`$X; S=/a; $S/run.sh`), after any other write of `IFS` (`read
      IFS`, `for IFS in`), and after a builtin that makes every variable unknown and has a word
      that is not a literal or holds `IFS` anywhere in its text; and anywhere in a command that
      holds a name reference (`declare -n`, `typeset -n`, `local -n`), a `declare`, `typeset`, or
      `local` option the guard cannot read, or an assignment of `IFS`, or that inherits `IFS` from
      its environment. A name reference or an option the guard cannot read counts the same when its
      declaration runs through `builtin` or `command` (`builtin declare -n R=T`), but the names such
      a declaration declares stay readable: `command declare -u S; S=/x; $S/run.sh` runs
      `/x/run.sh`. Inside an `eval` string, each of these, and each declaration
      of the two items above, takes effect where the `eval` runs and throughout any loop it runs
      in, not anywhere in the command: `S=/a; $S/run.sh; eval 'declare -n R=T'` reads `/a/run.sh`;
    - in an expansion with an operator (`${S%/x}`) or a zsh modifier (`$S:h`), and in an unquoted
      expansion whose value is empty or holds a space, a tab, a newline, a glob character (`*`,
      `?`, `[`, `(`), or a backslash (`S='/tmp/my work'; $S/run.sh`).
  - **Assignment prefixes:** an assignment prefix gives its value to the command it prefixes, not
    to that command's own words. In `S=/a; S=/x $S/run.sh` the word reads `/a/run.sh`, the value
    from before the prefix. It stays unknown when the command assigned none earlier
    (`S=/x $S/run.sh`). After the prefixed command, each variable of its prefix is unknown, since a
    special builtin or a function may leave the value in the shell: in
    `S=/a; S=/x true; $S/run.sh` the last word is not read.
- a `git`, `gh`, `ils`, or shell word (`bash`, `sh`, `zsh`, `dash`, `eval`, `source`) handed to a
  command the guard does not know (`xargs`, `timeout`, `sudo`, `find -exec`). These rules decide
  what counts as handed over:
  - **Wrappers the guard sees through:** `env`, `command`, `builtin`, `exec`, `nice`, `nohup`,
    `time`, `noglob`, and `nocorrect`. The guard reads an `exec` with no command behind them
    (`command exec > f`) as it reads `exec > f`. An `env` argument `NAME=value` is an assignment
    when its name is a literal, bare or in single or double quotes: `env "PATH=$HOME/bin:$PATH"
    make` runs `make`.
  - **Commands whose words are data:** the guard takes the words given to a reader, a filter, a
    file tool, a lookup, or a package manager, or a shell checker or formatter (`echo`, `grep`,
    `rg`, `cat`, `cp`, `which`, `go`, `brew`, `shellcheck`, `shfmt`, and the like) as data. So
    `echo git push` holds no `git` call.
  - **`go`:** the words of `go` are data, the values of `-exec` and `-toolexec` included
    (`go test -exec "$RUNNER" ./...`). The guard reads the words of `go run`, of `go generate`, and
    of a `go` whose subcommand or `-C` word is not a literal (`go "$o" run`) as those of a command
    it does not know, so it refuses `go run ./tool git push`.
  - **Behind a command the guard does not know:** the guard allows `ils status`, `parse`, `guard`,
    `hook`, and `help` (`timeout 60 ils status`). It refuses any other `ils` command, including one
    the command fills in (`xargs ils`).
  - **`sh -c` and script files:** a bare `sh`, `bash`, `zsh`, or `dash` followed by `-c` and a
    literal string hands over only what that string does, wherever the command runs it. A bare
    shell word right after the string is the name the shell gives `$0`, not one handed over. The
    guard still reads the words after that. `xargs -I{} sh -c 'ls {}'`,
    `xargs sh -c 'ls "$1"' sh`, and `docker run img sh -c 'make test'` pass. The guard refuses
    `xargs sh -c 'git status'`, a shell with neither `-c` nor a script file (`xargs sh`,
    `xargs bash -s`, `timeout 60 bash /dev/stdin`, `timeout 60 bash <(cat x)`), and one whose
    string is not a literal
    (`xargs bash -c "$s"`). The guard reads a bare shell given a script file, after any options, as
    that file run directly by the same command: `timeout 60 bash x.sh` and
    `find . -exec bash {} \;` pass as `timeout 60 ./x.sh` does. It reads the script's own arguments
    as those of `./x.sh`, so it refuses `timeout 60 bash x.sh git push`, naming `git`.
  - **Interpreters:** words from the first interpreter word on (`sed`, `awk`, `perl`, `python`,
    `node`, `ruby`, listed below) belong to that interpreter. Its code is data
    (`xargs sed -i 's/git push/ils push/'`, `timeout 30 python3 -c 'import git'`). A bare `git`,
    `gh`, `ils`, or shell word among them still counts as handed over
    (`timeout 30 python3 tool.py git push`).
  - **`ssh` and `su`:** they hand their words to a shell. As the command word, they have every
    argument read as a script, interpreter word or not: the guard refuses
    `ssh host sed 's/x/; git push/' f`.
  - **Inside a string the guard reads as a script:** the guard reads a bare shell's `-c` string or
    script file this way only at the top of the command. In such a string, a shell word still
    counts as handed over, and so does one among an interpreter's arguments. The guard refuses
    `ssh host 'xargs sh -c ls'`.
- a string handed to a command the guard does not know, which the guard reads as a script, when
  one of its commands is such a word or hands one over in turn: `su -c 'cd /repo && git push'`,
  `ssh host 'sudo git push'`, and `su -c "git push $b"`. In the last one the guard reads `$b` as an
  ordinary word, so the string still holds `git push`. A string that does not parse as a script
  is data, whatever command it goes to (`uv run python -c 'import subprocess;
  subprocess.run(["git", "log"])'`). The code given to `sed`, `awk`, `perl`, `python`, `python3`
  (and `python3.<minor>`), `node`, or `ruby` is not read as a script, whether or not it parses
  (`sed -n '/git push/p' README.md`), except behind `ssh` or `su` as the command word; a bare
  `git`, `gh`, `ils`, or shell word among their arguments still counts as handed over (`python3
  tool.py git push`);
- a `bash -c`, `sh -c`, `zsh -c`, `dash -c`, or `eval` string that is not a literal or nests more
  than three deep;
- a `git` or shell word among the arguments of a script file a shell runs: the shell runs the file
  as `./x.sh` does (`bash /tmp/probe.sh`), and the guard reads the arguments after the file as
  those of `./x.sh`, so it refuses `bash x.sh git push`. After a `source` or `.`, of a file or of a
  process substitution, the directory and every variable the guard follows are unknown, so the
  guard refuses `source env.sh && git commit -m x` because it cannot tell where the commit runs;
- a shell command that git runs. The guard reads these the same way, and refuses one that is not
  a literal. They are the value of `git rebase --exec` (`-x`), the command of `git bisect run`,
  `git submodule foreach`, the `git filter-branch` filters and `--setup`, and `git for-each-repo`;
  the value of `-c core.editor` and `-c sequence.editor`; and the value of `GIT_EDITOR`,
  `GIT_SEQUENCE_EDITOR`, and the `EDITOR` and `VISUAL` git falls back to. A
  `--config-env` of either editor key is judged as that `-c` with a value that is not a literal,
  whatever the command assigns its variable, so the guard refuses
  `ED=vim git --config-env=core.editor=ED commit`.
  The other keys, variables, and options git runs as a program (`core.pager`, `GIT_PAGER`,
  `PAGER`, `core.sshCommand`, a credential helper, `difftool -x`) are data
  ([accepted limits](guard-scope.md#accepted-limits));
- an assignment or `export` of a `GIT_*` variable off the allow list (`GIT_CONFIG_COUNT`,
  `GIT_EXEC_PATH`, and the like);
- a command of a function body the command defines. The guard judges each one where the function
  is declared, and again at each call, in the directory, environment, redirects, and HEAD of the
  call. A declaration alone changes nothing for the commands after it:
  `f() { git commit -am wip; }; git switch main && f` is refused as a commit on the trunk, and
  `T() { uv run pytest -q "$@"; }; T -k x > out.txt` passes. A call of one whose body may change
  directory leaves the directory unknown after it;
- a call of a function the command defines past the 256 walks of function bodies the guard makes
  for one command, dry runs included, as a function that calls itself makes;
- a git word that is neither a command nor an alias, a `!` alias, an alias nested more than three
  deep, and an alias the guard cannot read. An alias the guard cannot read has a value that does
  not split into words, or one set by a `-c` or `--config-env` the guard cannot follow. A
  subcommand that is not a literal (`git "$sub"`) counts as neither a command nor an alias. So does
  any word off git's command list where the guard cannot tell the git dir (after `cd "$d"`, or
  under `-C "$d"`);
- an alias, given by `-c alias.<name>` or configured, named for a git command that git 2.34.1 does
  not build in (`replay`, `send-email`, `submodule`, and the like): a git without the command runs
  the alias. Where the guard cannot read that alias (the git dir is unknown, as after `cd "$d"` or
  under `-C "$d"`, or a `-c` it cannot follow), it refuses such a command;
- a git option at the end of the command with no value after it (`git -C`);
- a git help call only for the operation git runs: git runs `git <sub> --help`, with `--help` the
  first word after the subcommand, as `git help <sub>`, before it expands any alias, and the guard
  reads it the same way, so `git stash --help`, `git push --help origin`, and `git st --help` pass,
  and `ils parse` shows the subcommand `help`. A `--help` anywhere else leaves the call judged by
  its subcommand, so the guard refuses `git stash drop --help` and `git push origin --help`;
- a `-c` whose key is not a literal (`git -c "$kv" commit`), or a `--config-env` whose value is
  not a literal, on any git command, since the key may be `core.hooksPath`;
- a non-literal argument to a git command the guard checks for flags (`git clean "$opt"`, which
  could expand to `--force`). A commit message (`-m "$(cat <<'EOF' … EOF)"`), the directory of
  `-C "$dir"`, and a path glob whose literal prefix does not start with `-` or `+` (`git add
  src/*.go`, which expands only to paths under `src/`) are exempt; a glob with no such prefix
  (`git add *.go`) could match a file named like an option and is refused.

#### Git operations that destroy work or skip checks

- every `checkout`, `reset`, and `prune`; every `stash` but `stash list` and `stash show`; `switch
  --force` or `--discard-changes`; `clean --force` or `--interactive` (`-i`), unless a dry run
  (`-n`, `--dry-run`) is set last and `--interactive` is not; `branch -D`, `-d -f`, or `--delete
  --force`;
  `restore` without `--staged` (or `-S`), or with `--worktree` (`-W`) beside it; `rm --force`;
  `reflog expire`, `delete`, and `drop`; `gc --prune=…`, and `gc` with `gc.pruneExpire` set
  through `-c` or `--config-env`; `repack -a -d` without `-A`, `--cruft`, or
  `--keep-unreachable`, and `repack -d` with `-A --unpack-unreachable=…` or `--cruft
  --cruft-expiration=…`, whatever the expiry; `clean` without `--force` where
  `clean.requireForce` is off, through `-c`, `--config-env`, or the repository's config;
  `checkout-index --force`; `read-tree`, unless `-n` (`--dry-run`), `--index-output`, or
  `--prefix` keeps it off the index, and always with `--reset` or `-u -m`; `update-ref -d`,
  `update-ref --stdin`, and `update-ref` whose new value is all zeros or whose ref or new value is
  not a literal; `branch -M`, `-C`, or `-m` (`--move`) or `-c` (`--copy`) with
  `--force`, onto a branch that exists in any letter case; `apply --reverse`; `add --force`;
- `--no-verify` on any command, `commit -n`, `am -n`, `send-email --no-validate`, and
  `core.hooksPath` set through `-c` or `--config-env`;
- `git show` or `git cat-file` with stdout redirected, or piped straight into `tee`, to a file in a
  git working tree, or to a file whose name the guard cannot read. The guard allows output landing
  in `/dev/null`, or in a file whose nearest existing directory is outside every working tree. A
  `tee` that reads a group, a subshell, or a function call holding `git show` is not judged
  ([rule 3](guard-scope.md#3-close-a-bypass-only-when-agents-write-it)), and neither is a file an
  earlier `exec` opened (`exec > f; git show HEAD:f`,
  [accepted limits](guard-scope.md#accepted-limits));
- `rebase` in a working tree with uncommitted changes or untracked files, or where the guard cannot
  tell the repository. In a dirty tree, `--continue`, `--edit-todo`, `--show-current-patch`, and
  `--quit` are allowed, since they leave the tree as it is; `--abort` and `--skip` are refused;
- `worktree remove --force`, and `worktree remove` of a worktree with changes or one the guard
  cannot find;
- `git config` writing `remote.*`, `url.*`, or `core.hooksPath`, at any scope, and renaming or
  removing the `remote`, `url`, or `core` section;
- `git remote remove`, `rm`, `rename`, `set-url`, or `set-head` on `origin`.

<!-- cspell:ignore verif -- a prefix git accepts for --no-verify, which the example needs -->

Long options match as git matches them: git accepts any unambiguous prefix, so `--no-verif` counts
as `--no-verify`.

#### Pushes and the default branch

- every `git push`, `git send-pack`, and `git http-push`, in any repository: `ils push` is the way
  an agent pushes a branch;
- in a repository with an `origin`, on its default branch: `commit`, `merge`, `cherry-pick`,
  `revert`, `am`, `rebase`, `pull` without `--ff-only`, and `pull --ff-only` naming a repository or
  branch other than `origin <default branch>`. The reason names `ils branch <name>`.
  A `git rebase` naming the default branch as the branch to rebase counts as one on it. The guard
  asks origin for the default branch once per command and git dir, and falls back to the cached
  `refs/remotes/origin/HEAD`. With neither, it refuses and names
  `git remote set-head origin --auto`, which caches the head on any supported git. Run it outside
  the agent or as a `!` command, since the guard refuses `set-head` on `origin`;
- one of those where the guard cannot tell the directory, the target, or HEAD: after `cd -`,
  `popd`, or a `cd` to a non-literal, and after a git or `gh` command that moves HEAD earlier in
  the list (`git switch main; git commit`). A HEAD move in a background job or a pipe stage, and a
  write to the `HEAD` file by any command other than git, are not followed
  ([accepted limits](guard-scope.md#accepted-limits)).
  The guard follows a `git switch` run as a git command of its own (no wrapper, alias, shell
  function, `!`, or command substitution) in one of two forms:

  - it names one literal branch and takes no option;
  - it takes `-c`, `--create`, `-C`, or `--force-create` followed by a literal name as a word of
    its own, and at most a literal start point.

  The guard judges the commands later in the same `&&` list that run in the same git dir on that
  branch. This lasts until a command that may move HEAD or a call of a function the command
  declares. The guard allows `git switch -c topic && git commit`, and refuses
  `git switch main && git commit` as a commit on the default branch.

  A switch with any other option (`-q`, `--detach`), with the name joined to its option
  (`--create=topic`), with a start point that is not a literal, to `-`, or to a name holding `@{`
  leaves HEAD unknown. The commands of a function body follow no switch where the function is
  declared; at a call, they follow the switch the call follows.

#### ils and gh commands

- a help call passes: an `ils` subcommand, or a subcommand of `gh auth`, `config`, `pr`,
  `release`, or `repo`, whose first word after it is `--help` (`ils merge --help`, `gh pr merge
  --help`) prints its help and runs nothing, and the guard allows it alone, in a pipe, a list, or a
  subshell, and with a redirect. Handed to a command the guard does not know (`timeout 60 ils merge
  --help`), the guard refuses it as any other `ils` or `gh` word there. A `--help` past that first
  word may be the value of an earlier flag, so `gh pr merge 1 --help` is judged as written;
- `ils merge`, `ils release`, `gh pr merge`, and `gh release create` ask when the command is that
  one call by its bare name. The guard refuses them behind a wrapper, in a list or pipe, with a
  redirect or `&`, in a `-c` string, or spelled as a path or with a backslash;
- the guard allows `ils push` and `ils branch` as the whole command by their bare name, and refuses
  them in any other form, piped or `tee`d included;
- `gh pr merge --admin` in any form;
- `gh api` with a method other than `GET`, or with no method but a field or `--input`. A `gh api
  graphql` call whose `query` field is a literal document holding no mutation is a read, and the
  guard allows it, with no method or with `POST` in any spelling (`-X POST`, `--method post`); one
  whose document holds a mutation is a write, and one whose document the guard cannot read (`-F
  query=@q.graphql`, `-f query="$q"`, `--input`) is refused until the query is passed inline;
- `gh repo sync`, `delete`, `edit`, and `create --push`; `gh release delete`, `edit`, `upload`,
  and `delete-asset`; `gh auth token`, `gh auth git-credential`, `gh auth status --show-token`, and
  `gh config get oauth_token`; `gh pr checkout --force`;
- a flag these rules name (`--admin`, `--input`, `--push`, `--show-token`, `--force`) counts as
  given when its value is one the guard cannot read (`--force="$v"`, `--input="$f"`);
- an `ils` or `gh` command word that is not a literal (`ils "$cmd"`);
- a `gh` word that is neither a command nor an alias, extensions included;
- a `!` alias;
- a `gh` alias given an argument that is not a literal to fill a `$N`;
- a `gh` alias whose expansion the guard cannot read, such as a `$2` left unfilled, or that expands
  to no `gh` command;
- a `gh alias list` that fails.

A read the guard needs that fails (a missing `git`, a `gh alias list` that errors, the read of
`clean.requireForce` or of an alias) is a refusal that says what broke.

### Guard JSON output

`ils parse --json` puts its detail under `payload.parse`, and `ils guard --json` under
`payload.guard`, with the guard's verdict in `outcome` and `reason`. Both hold `invocations`, in
the order the guard reads them, which is not always the order the shell runs them. On a command that
does not parse, `ils guard` refuses with no invocations, and `ils parse` ends broke with no
payload. Each invocation has:

| Field   | Type             | Meaning                                                                                               |
| ------- | ---------------- | ----------------------------------------------------------------------------------------------------- |
| `tool`  | string           | `git`, `gh`, or `ils` when the command word names one. Absent otherwise.                              |
| `dir`   | string           | The absolute directory the invocation runs in. Absent when the guard cannot tell it.                  |
| `words` | array of objects | Each word as `text` and `literal`. A word that is not a literal carries its literal prefix as `text`. |
| `marks` | array of objects | What the guard could not read safely in this invocation, as `kind` and, when it names one, `name`.    |
| `git`   | object           | For a git invocation: the fields listed below. Absent otherwise.                                      |

A mark's `kind` is one of these 12 strings. This one names nothing:

- `command word that is not a literal`

These two carry a `name` for a command that git runs from its arguments, and name nothing
otherwise:

- `shell string that is not a literal`: the form git runs it from: `bisect`, `bisect run`,
  `submodule`, `submodule foreach`, `filter-branch` or its filter or `--setup` flag as written
  (`--tree-filter`), or `for-each-repo`;
- `shell string nested too deep`: the form, as above.

These nine carry a `name`:

- `git, gh, ils, or shell word handed to another command`: the word;
- `git variable off the allow list`: the variable, absent when the guard cannot read its name;
- `call of a function past the calls the guard follows`: the function;
- `git alias that runs a shell command`: the alias;
- `git word that is neither a command nor an alias`: the word, or for a word that is not a literal
  its literal prefix, absent when it has none (`git "$sub"`);
- `git alias nested too deep`: the alias at the level where the guard stopped;
- `git alias the guard cannot read`: the alias;
- `git option missing its value`: the option;
- `git alias of a command an older git lacks`: the alias.

A mark is not a verdict. The rules for git operations, pushes, and `gh` leave no mark, so use `ils
guard` for the verdict.

The `git` object holds:

- `subcommand`: a word, absent when git runs none;
- `args`: an array of words;
- `git_dir`: absent outside any repository, and when `git_dir_unknown` is true;
- `git_dir_unknown`: whether the guard could not tell the git dir.

## Terminal output

At a terminal, every command shows its progress as it runs:

- **Spinner:** when stderr is a terminal and `-d` is off, one line on stderr names the running
  step and how long it has run, and is erased before anything else prints.
- **Step lines:** for `ils push`, `ils merge`, and `ils release`, when stdout is a terminal, each
  step prints on stdout as soon as it succeeds, with a check mark and its duration. The result then
  prints only its other lines, its excerpt, the reason, and `Next:`.
- **Color:** a stream that is a terminal gets color: outcome labels are red or yellow, and branch
  names, pull request numbers, and `ils` commands are bold blue.
- **Pending hint:** when stdout is a terminal, the `Next:` line of a pending result ends with
  `# waits again; nothing to fix`.

A stream that is not a terminal gets none of this. Stdout prints the whole result at the end. Stderr
stays empty unless `-d` is on, or `ils` cannot print its result and reports that failure there.
`--json` output is never colored or streamed, but `ils push --json`, `ils merge --json`, and
`ils release --json` at a terminal still wait for the longer [time limit](#time-limits).
`TERM=dumb` counts as no terminal.

`NO_COLOR` and `--no-color` turn color and bold off on both streams, help included. `CLICOLOR_FORCE`
turns color on for a stream that is not a terminal; `NO_COLOR` and `--no-color` override it. None
of the three changes how long `ils push`, `ils merge`, or `ils release` waits.

`-d` (or `--debug`) prints one line on stderr for each `git` and `gh` call and each wait decision,
timed from when the command started, in place of the spinner. It changes neither stdout nor the
exit code.
