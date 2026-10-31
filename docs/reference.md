# ils reference

This page is the full reference for `ils`: exit codes, time limits, every refusal, the JavaScript
Object Notation (JSON) output, and the terminal output. The [README](../README.md) shows how to
install `ils` and what each command does. CI stands for continuous integration throughout.

- [Exit codes](#exit-codes)
  - [When no check reports](#when-no-check-reports)
- [Time limits](#time-limits)
- [Stopping a run](#stopping-a-run)
- [Refusals](#refusals)
  - [What branch refuses](#what-branch-refuses)
  - [What push refuses](#what-push-refuses)
  - [What merge refuses](#what-merge-refuses)
  - [What release refuses](#what-release-refuses)
- [JSON output](#json-output)
  - [Push JSON output](#push-json-output)
  - [Merge JSON output](#merge-json-output)
  - [Status JSON output](#status-json-output)
  - [Release JSON output](#release-json-output)
- [Terminal output](#terminal-output)

## Exit codes

| Exit | Means                | Reaction                                                                                                                                                                                                                                                                                                                           |
| ---- | -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 0    | done                 | Push: the checks are green, or the repository has no CI and a line says nothing verified the commit; follow `Next:`. Merge: the pull request is merged and the branch deleted. Branch: the branch is started. Status: always, whatever it finds. Release: the tag is on origin, and the GitHub release exists unless `--tag-only`. |
| 1    | broke                | The output says what was done and what was not. Fix what failed, then follow `Next:`.                                                                                                                                                                                                                                              |
| 2    | refused              | Nothing was pushed, opened, merged, or tagged, except by a push whose pull request's head moved during the wait. The reason says why; `Next:` says what to do.                                                                                                                                                                     |
| 3    | ask                  | Reserved; no command uses it yet.                                                                                                                                                                                                                                                                                                  |
| 4    | pending              | GitHub had not finished when the command stopped waiting, for example checks still running or not yet reported. Run the command that the `Next:` line names; [Time limits](#time-limits) says how long each command waits. After an `ils release`, run the same command again.                                                     |
| 5    | delivered, not green | Push only. The push happened; a check is red or missing, or the branch conflicts with or is behind the default branch. Follow `Next:`.                                                                                                                                                                                             |

Every outcome but done prints its reason on the line above `Next:`, labeled by outcome:
`Refused:`, `Broke:`, `Pending:`, or `Delivered, not green:`. A run stopped by Ctrl-C or SIGTERM
(the signal `kill` sends by default) exits with the code of the result it prints; see
[Stopping a run](#stopping-a-run).

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

When a pending `ils merge` names `ils push` on its `Next:` line, that push waits as long. A script
or harness may give `ils` a terminal but stop it on a timeout shorter than 15 minutes. It then
passes `--wait=false`. An `ils merge` or `ils release` stopped between two of its writes continues
on the next run.

## Stopping a run

Ctrl-C or SIGTERM during `ils push`, `ils merge`, or `ils release` stops the step in progress. `ils`
then prints the result of that step, as text or as JSON per `--json`, and exits with that result's
code. The output says that `ils` was interrupted, and `Next:` says what to run. As at a normal end,
the spinner line is erased before the result prints; see [Terminal output](#terminal-output).

A second Ctrl-C or SIGTERM ends `ils` at once, as that signal ends any program: no result is
printed, and the exit status is the signal's (130 for Ctrl-C and 143 for SIGTERM in most shells),
not one of the [exit codes](#exit-codes).

## Refusals

A refusal ends with exit 2: the command wrote nothing, and the reason says why.

### What branch refuses

When `ils branch` refuses, it has created no branch and switched nothing; at most it has fetched
from origin. It refuses:

- no name, a second word, or `--`;
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
or file; at most it has fetched from origin. It refuses:

- the default branch, and a detached HEAD;
- a tracked file with uncommitted changes (untracked files are fine);
- a branch with no commit past origin's default branch;
- a branch that is behind, or has diverged from, its copy on origin;
- a branch whose first commit past the default branch is also on another local branch, unless its
  copy on origin already carries that commit. `ils` cannot tell which branch was cut from which. It
  tells you to land each branch that shares the commit first, or to remove each one that was cut
  from this branch. `ils` reads the copy on origin first, so a branch that is also behind or
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
- any word after `push` other than `--wait`, `--json`, `-d` or `--debug`, `--no-color`, `--help`,
  or `-h`, and a `--wait=<value>`, `--json=<value>`, `--debug=<value>`, or `--no-color=<value>`
  whose value is not `true` or `false`;
- a precondition read that the time limit, Ctrl-C, or SIGTERM cut short: run the same command
  again, with the `--wait` you gave it.

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
  required check, or that GitHub blocks for another reason;
- a pull request whose head moved to another commit while `ils merge` waited for its checks:
  `Next:` says to pull the new commit, then run `ils merge` again.

### What release refuses

When `ils release` refuses, it has tagged nothing, pushed nothing, and created no release; at most
it has fetched from origin. It refuses everything `ils push` refuses about the tools, `origin`, and
the repository, and also:

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

## JSON output

With `--json`, a command prints one JSON object with these fields:

| Field       | Type             | Meaning                                                                                                                                |
| ----------- | ---------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| `command`   | string           | The command that ran, such as `push`.                                                                                                  |
| `words`     | array of strings | The command-line words `ils` received. Absent when empty.                                                                              |
| `outcome`   | string           | `done`, `broke`, `refused`, `ask`, `pending`, or `delivered-not-green`: the Means column of [Exit codes](#exit-codes), with hyphens.   |
| `next`      | string           | What to do next, as on the `Next:` line.                                                                                               |
| `reason`    | string           | Why the command did not end as `done`. Present on every other outcome.                                                                 |
| `excerpt`   | string           | Third-party text: the end of a failed check's log, or of what the hooks and `git` printed for a push `git` refused. Absent when empty. |
| `lines`     | array of strings | What happened, in order; never a line that only restates the reason. Absent when empty.                                                |
| `exit_code` | integer          | The process exit code.                                                                                                                 |
| `payload`   | object           | The command's structured detail, under the command's name, such as `payload.push`. Absent when empty.                                  |

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
