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

| Exit | Means                | Reaction                                                                                                                                                                                                                                                                                                                           |
| ---- | -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 0    | done                 | Push: the checks are green, or the repository has no CI and a line says nothing verified the commit; follow `Next:`. Merge: the pull request is merged and the branch deleted. Branch: the branch is started. Status: always, whatever it finds. Release: the tag is on origin, and the GitHub release exists unless `--tag-only`. |
| 1    | broke                | The output says what was done and what was not. Fix what failed, then follow `Next:`.                                                                                                                                                                                                                                              |
| 2    | refused              | Nothing was pushed, opened, merged, or tagged, except by a push whose pull request's head moved during the wait. The reason says why; `Next:` says what to do.                                                                                                                                                                     |
| 3    | ask                  | `ils guard` only: the command is one the user approves before it runs. See [ils guard](#ils-guard).                                                                                                                                                                                                                                |
| 4    | pending              | GitHub had not finished when the command stopped waiting, for example checks still running or not yet reported. Run the command that the `Next:` line names; [Time limits](#time-limits) says how long each command waits. After an `ils release`, run the same command again.                                                     |
| 5    | delivered, not green | Push only. The push happened; a check is red or missing, or the branch conflicts with or is behind the default branch. Follow `Next:`.                                                                                                                                                                                             |

Every outcome but done prints its reason labeled by outcome: `Refused:`, `Broke:`, `Ask:`,
`Pending:`, or `Delivered, not green:`. The reason sits on the line above `Next:`, except for `ils
guard`, which prints no `Next:` line. A run stopped by Ctrl-C or SIGTERM (the
signal `kill` sends by default) exits with the code of the result it prints; see [Stopping a
run](#stopping-a-run).

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

When a pending `ils merge` names `ils push` on its `Next:` line, that push waits as long. A script
or harness may give `ils` a terminal but stop it on a timeout shorter than 15 minutes. It then
passes `--wait=false`. An `ils merge` or `ils release` stopped between two of its writes continues
on the next run.

## Stopping a run

Ctrl-C or SIGTERM during `ils push`, `ils merge`, or `ils release` stops the step in progress. `ils`
then prints the result of that step, as text or as JSON per `--json`, and exits with that result's
code. The output says that `ils` was interrupted, and `Next:` says what to run. As at a normal end,
the spinner line is erased before the result prints; see [Terminal output](#terminal-output).

`ils release` stopped while it reads the repository, origin, or the default branch, before it
waits for checks, is refused with nothing written, as [What push refuses](#what-push-refuses)
lists. Stopped while it waits for the checks of the release commit, it ends pending with nothing
written, as `ils merge` does, unless a check of that commit has already failed or been canceled:
it is then refused on that check, as [What release refuses](#what-release-refuses) lists. Only
when no check reported for the release commit does it then read the rules of the default branch
and whether the repository has CI; stopped during either read, it is refused with nothing written.
Stopped after its last read and before its first write, it ends pending with nothing written.
Stopped while it makes the tag or looks up the GitHub release, it says that it was interrupted, or
which time limit ran out, as it does for the push of the tag.

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

An `origin` that answers and turns the call away, for a failed sign-in, a missing permission, or a
repository it cannot find, is not a refusal. A command whose call to origin is turned away before
it lands anything ends broke with git's message and `Next: fix what failed, then run <command>
again`, and `ils status` lists the read as failed. The steps `ils merge` runs after the squash merge
keep the `Next:` each gives for any failure, since the merge itself is done. Only an `origin` that
cannot be reached at all ends as described above.

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
  `Next:` says to pull the new commit, then run `ils merge` again;
- on a run that finishes a pull request already merged, a branch on origin that holds commits the
  merged head does not carry: it lists them and deletes nothing, and `Next:` says to start a branch
  and cherry-pick them.

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

A GitHub release created while the notes file `ils release` wrote for it cannot be removed ends
done, with the release's URL and a line naming the file left behind.

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
- a `cwd` that does not exist;
- every internal error.

`ils hook` never exits 1, since Claude Code runs a tool call whose hook exits with anything but 2.
A harness (the program that runs the agent's tool calls) other than `claude`, or none, exits 2 and
names the harnesses `ils` knows.

`skills/ils/hook.sh` fronts `ils hook claude`. It passes exit 0 and 2 through with the binary's
output. Any other exit becomes exit 2, with a line on stderr naming the code. When `ils` is not
on `PATH` or cannot start, it exits 2 and names `! go install github.com/jeffzi/ils@latest`, and
`! task install` from a clone of `ils`.

### What the guard refuses

#### How the guard reads a command

- a command that does not parse;
- an extended glob written directly in the words, the non-array values, or the redirects
  (here-strings included) of a simple command or a declaration such as `declare` or `export` (zsh
  runs a glob qualifier such as `*(e:'git push':)` or `*(+f)` as code). The one form allowed there
  is a `*(…)` whose parentheses hold no `e`, `+`, `$`, backtick, or inner parenthesis, such as the
  null glob `*(N)`; every other extended glob, `@(a|b)` and `!(x)` included, is refused. The guard
  looks for none anywhere else: an array value, a `for` or `select` list, the operand of a `${…}`
  expansion such as a default, a `case` word or pattern, a `[[ … ]]` test, or the redirects of
  `let` or a compound command. A qualifier there could only hide a command
  ([rule 3](guard-scope.md#3-close-a-bypass-only-when-agents-write-it));
- a command word that is not a literal (`$(which git)`, `"$GIT"`). A `$NAME` or `${NAME}` the
  command assigned a literal earlier reads as that literal in the words that run later in the same
  shell, the words of a subshell, a command substitution, or an `eval` string included. A plain
  assignment counts, and so do `export`, `declare`, `typeset`, and `readonly` with a literal
  value, and `local` inside its function. So `S=/tmp/work; $S/run.sh` passes, and `G=git; $G
  push` is refused as `git push`. The value holds only where the assignment always ran before the
  word (`S=/x && $S/run.sh`, `S=/x; for f in a b; do $S/run.sh; done`), and only across commands
  that write no variable: a program, a function whose body the guard walks, and the builtins `:`,
  `true`, `false`, `echo`, `test`, `[`, `cd`, `pwd`, `pushd`, `popd`, `dirs`, `exit`, `return`,
  `break`, `continue`, `shift`, `alias`, `unalias`, `hash`, `type`, `which`, `whence`, `where`,
  `umask`, `ulimit`, `kill`, `jobs`, `eval`, `source`, `.`, and `printf` with no argument or whose
  first argument is a literal that does not start with `-v`. zsh's `mkdir`, `rm`, `ln`, and the
  other builtins of its `zsh/files` module write no variable either, and count as programs. Every
  other builtin of bash or zsh (`read`, `print -v`, `vared`, `zformat`, `set`), and a command word
  that is not a literal, makes every variable unknown after it. The variable also stays unknown:
  - when something other than a literal assignment writes it: `+=`, `printf -v`, `for NAME in`,
    `${NAME:=…}`, `unset`, arithmetic, or a value that is not a literal;
  - when the assignment ran in a subshell, a pipeline stage, or a background job, or may not have
    run: `true && S=/x; $S/run.sh`, an `if` or loop body, or a function the command calls;
  - in a string another shell runs (`sh -c '$S/run.sh'`) or that the guard reads for a tool a
    command the guard does not know may run (`su -c '$S/run.sh'`), and in a function body, which
    knows only the variables it assigns itself;
  - for a variable bash or zsh defines as it starts or sets on its own (`PWD`, `PATH`, `REPLY`,
    `MATCH`, `match`, `BASH_REMATCH`), and for a name that a declaration with an option, or zsh's
    `integer` or `float`, names anywhere in the command (`declare -x S=/x`, `declare -u S`);
  - for a name made `readonly`, other than by the one `readonly` that gives it its value, and for
    that one too when another `readonly` names the variable anywhere in the command (`readonly
    S=$(pwd); readonly S=/x`);
  - for every variable, later literal assignments included, after a `source`, after a command word
    that is not a literal (`$X; S=/a; $S/run.sh`), after any other write of `IFS` (`read IFS`,
    `for IFS in`), and after a builtin that makes every variable unknown and has a word that is not
    a literal or holds `IFS` anywhere in its text; and anywhere in a command that holds a name
    reference (`declare -n`, `typeset -n`, `local -n`), a `declare`, `typeset`, or `local` option
    the guard cannot read, or an assignment of `IFS`, or that inherits `IFS` from its environment.
    A declaration run through `builtin` or `command` (`builtin declare -n R=T`) counts as one
    written plainly. Inside an `eval` string, each of these, and each declaration of the two items
    above, takes effect where the `eval` runs and throughout any loop it runs in, not anywhere in
    the command: `S=/a; $S/run.sh; eval 'declare -n R=T'` reads `/a/run.sh`;
  - in an expansion with an operator (`${S%/x}`) or a zsh modifier (`$S:h`), and in an unquoted
    expansion whose value is empty or holds a space, a tab, a newline, a glob character (`*`, `?`,
    `[`, `(`), or a backslash (`S='/tmp/my work'; $S/run.sh`).

  An assignment prefix gives its value to the command it prefixes, not to that command's own
  words: in `S=/a; S=/x $S/run.sh` the word reads `/a/run.sh`, the value from before the prefix,
  and it stays unknown when the command assigned none earlier (`S=/x $S/run.sh`). After the
  prefixed command, each variable of its prefix is unknown, since a special builtin or a function
  may leave the value in the shell: in `S=/a; S=/x true; $S/run.sh` the last word is not read.
- a `git`, `gh`, `ils`, or shell word (`bash`, `sh`, `zsh`, `dash`, `eval`, `source`) handed to a
  command the guard does not know (`xargs`, `timeout`, `sudo`, `find -exec`). The guard sees
  through the wrappers `env`, `command`, `builtin`, `exec`, `nice`, `nohup`, `time`, `noglob`, and
  `nocorrect`, and reads an `exec` with no command behind them (`command exec > f`) as it reads
  `exec > f`. It takes the words given to a reader, a filter, a file tool, a lookup, or a package
  manager (`echo`, `grep`, `rg`, `cat`, `cp`, `which`, `go`, `brew`, and the like) as data, so
  `echo git push` holds no `git` call. `go` is data except for the command its `-toolexec` or
  `-exec` names, which the guard reads as a script and refuses when it is not a literal or does not
  parse, and the words of `go run`, of `go generate`, and of a `go` whose subcommand or `-C` word is
  not a literal (`go "$o" run`), which it reads as those of a command it does not know. Behind such
  a command the guard allows
  `ils status`, `parse`, `guard`, `hook`, and `help` (`timeout 60 ils status`). It refuses any
  other `ils` command, including one the command fills in (`xargs ils`). A bare `sh`, `bash`,
  `zsh`, or `dash` followed by `-c` and a literal string hands over only what that string does,
  wherever the command runs it: `xargs -I{} sh -c 'ls {}'` and `docker run img sh -c 'make test'`
  pass, while `xargs sh -c 'git status'`, a shell with no `-c` (`xargs sh`), and one whose string is
  not a literal (`xargs bash -c "$s"`) are refused. This holds only at the top of the command:
  inside a string the guard reads as a script, a shell word still counts as handed over
  (`ssh host 'xargs sh -c ls'` is refused), and so does one among an interpreter's arguments;
- a string handed to a command the guard does not know, which the guard reads as a script, when
  one of its commands is such a word or hands one over in turn: `su -c 'cd /repo && git push'`,
  `ssh host 'sudo git push'`, and `su -c "git push $b"`. In the last one the guard reads `$b` as an
  ordinary word, so the string still holds `git push`. A string that does not parse as a script
  is data, whatever command it goes to (`uv run python -c 'import subprocess;
  subprocess.run(["git", "log"])'`). The code given to `sed`, `awk`, `perl`, `python`, `python3`
  (and `python3.<minor>`), `node`, or `ruby` is never read as a script, whether or not it parses
  (`sed -n '/git push/p' README.md`); a bare `git`, `gh`, `ils`, or shell word among their
  arguments still counts as handed over (`python3 tool.py git push`);
- a `bash -c`, `sh -c`, `zsh -c`, `dash -c`, or `eval` string that is not a literal or nests more
  than three deep;
- a shell that reads its script from standard input or a process substitution (`cat x | bash`,
  `bash -s`, `bash /dev/stdin`, `bash <(…)`), and a `source` or `.` of either. A shell given a script
  file runs it as `./x.sh` does (`bash /tmp/probe.sh`), and its arguments after the file are read
  as those of `./x.sh` (`bash x.sh git push` is refused). A `source` or `.` of a file is allowed;
  after it, the directory and every variable the guard follows are unknown, so `source env.sh &&
  git commit -m x` is refused because the guard cannot tell where the commit runs;
- a shell command that git runs. The guard reads these the same way, and refuses one that is not
  a literal. They are the value of `git rebase --exec` (`-x`), the command of `git bisect run`,
  `git submodule foreach`, `git difftool -x` (`--extcmd`), the `git filter-branch` filters and
  `--setup`, and `git for-each-repo`; the value of `-c core.editor`, `core.pager`,
  `sequence.editor`, `core.sshCommand`, `pager.<command>`, `difftool.<tool>.cmd`, or
  `mergetool.<tool>.cmd`; and the value of `GIT_EDITOR`, `GIT_SEQUENCE_EDITOR`, `GIT_PAGER`, and
  the `EDITOR`, `VISUAL`, and `PAGER` git falls back to;
- an assignment or `export` of a `GIT_*` variable off the allow list (`GIT_CONFIG_COUNT`,
  `GIT_EXEC_PATH`, and the like), and under `set -a` a `read`, `printf -v`, `mapfile`, `for`,
  `getopts`, or `${V:=…}` that sets one, since `set -a` exports it;
- a function the command defines that shadows `cd`, `pushd`, `popd`, or a wrapper the guard sees
  through. A call of one whose body may change directory leaves the directory unknown after it.
  The guard judges each command of a function body where the function is declared, and again at
  each call, in the directory, environment, redirects, and HEAD of the call:
  `f() { git commit -am wip; }; git switch main && f` is refused as a commit on the trunk, and
  `T() { uv run pytest -q "$@"; }; T -k x > out.txt` passes;
- a call of a function the command defines past the 256 calls the guard follows into a body, as a
  function that calls itself makes;
- an `alias` whose value runs `git`, `gh`, or `ils`, or that the guard cannot read;
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
- a `-c` or `--config-env` value that is not a literal, on any git command, since it may set
  `core.hooksPath`;
- a non-literal argument to a git command the guard checks for flags (`git clean "$opt"`, which
  could expand to `--force`). A commit message (`-m "$(cat <<'EOF' … EOF)"`) and the directory of
  `-C "$dir"` are exempt.

#### Git operations that destroy work or skip checks

- every `checkout`, `reset`, and `prune`; every `stash` but `stash list` and `stash show`; `switch
  --force` or `--discard-changes`; `clean --force`; `branch -D`, `-d -f`, or `--delete --force`;
  `restore` without `--staged` (or `-S`), or with `--worktree` (`-W`) beside it; `rm --force`;
  `reflog expire`, `delete`, and `drop`; `gc --prune=…`, and `gc` with `gc.pruneExpire` set
  through `-c` or `--config-env`; `repack -a -d` without `-A`, `--cruft`, or
  `--keep-unreachable`; `clean` without `--force` where `clean.requireForce` is off,
  through `-c` or the repository's config;
  `checkout-index --force`; `read-tree --reset`, and `read-tree -u -m`; `update-ref -d` and
  `update-ref --stdin`; `branch -M` onto a branch that exists; `apply --reverse`; `add --force`;
- `--no-verify` on any command, `commit -n`, `am -n`, `send-email --no-validate`, and
  `core.hooksPath` set through `-c` or `--config-env`;
- `git show` or `git cat-file` with stdout redirected, or piped straight into `tee`, to a file in a
  git working tree, or to a file whose name the guard cannot read; output landing in `/dev/null`,
  or in a file whose nearest existing directory is outside every working tree, is allowed;
- `rebase` in a working tree with uncommitted changes or untracked files, or where the guard cannot
  tell the repository;
- `worktree remove --force`, and `worktree remove` of a worktree with changes or one the guard
  cannot find;
- `git config` writing `alias.*`, `include.*`, `includeIf.*`, `remote.*`, `url.*`, `pager.*`,
  `difftool.<tool>.cmd`, `mergetool.<tool>.cmd`, `core.hooksPath`, `core.editor`, `core.pager`,
  `core.sshCommand`, or `sequence.editor`, at any
  scope, renaming or removing a section that holds one of them, and `git config --edit`, which can
  write any of them;
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
  `popd`, or a `cd` to a non-literal, after a command that moves HEAD earlier in the list
  (`git switch main; git commit`) or a write to the git dir's `HEAD` file, and anywhere in a
  list where a background job or a pipe stage may move HEAD, since those run beside the rest.
  The guard follows a `git switch` run as a git command of its own (no wrapper, alias, shell
  function, `!`, or command substitution) that names one literal branch and takes no option, or
  that takes `-c`, `--create`, `-C`, or `--force-create` followed by a literal name as a word of
  its own, and at most a literal start point. The commands later in the same `&&` list that run in
  the same git dir are judged on that branch, until a command that may move HEAD or a call of a
  function the command declares: `git switch -c topic && git commit` is allowed, and
  `git switch main && git commit` is refused as a commit on the default branch. A switch with any
  other option (`-q`, `--detach`), with the name joined to its option (`--create=topic`), with a
  start point that is not a literal, to `-`, or to a name holding `@{` leaves HEAD unknown, and
  the commands of a function body are never judged on the branch of a switch outside it.

#### ils and gh commands

- `ils merge`, `ils release`, `gh pr merge`, and `gh release create` ask when the command is that
  one call by its bare name. The guard refuses them behind a wrapper, in a list or pipe, with a
  redirect or `&`, in a `-c` string, or spelled as a path or with a backslash;
- the guard allows `ils push` and `ils branch` as the whole command by their bare name, and refuses
  them in any other form, piped or `tee`d included;
- `gh pr merge --admin` in any form;
- `gh api` with a method other than `GET`, or with no method but a field or `--input`. A `gh api
  graphql` call whose `query` field is a literal document holding no mutation is a read, and the
  guard allows it; one whose document holds a mutation is a write, and one whose document the guard
  cannot read (`-F query=@q.graphql`, `-f query="$q"`, `--input`) is refused until the query is
  passed inline;
- `gh repo sync`, `delete`, `edit`, and `create --push`; `gh release delete`, `edit`, `upload`,
  and `delete-asset`; `gh auth token`, `gh auth git-credential`, `gh auth status --show-token`, and
  `gh config get oauth_token`; `gh pr checkout --force`;
- a flag these rules name (`--admin`, `--input`, `--push`, `--show-token`, `--force`) counts as
  given when its value is one the guard cannot read (`--force="$v"`, `--input="$f"`);
- an `ils` or `gh` command word that is not a literal (`ils "$cmd"`);
- `gh alias set`, `import`, and `delete`;
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

A mark's `kind` is one of these 12 strings. These five name nothing:

- `extended glob`
- `command word that is not a literal`
- `shell string that is not a literal`
- `shell string nested too deep`
- `shell running a script the command does not hold`

These seven carry a `name`:

- `git, gh, ils, or shell word handed to another command`: the word;
- `git variable off the allow list`: the variable;
- `git alias that runs a shell command`: the alias;
- `git word that is neither a command nor an alias`: the word;
- `git alias nested too deep`: the alias at the level where the guard stopped;
- `git alias the guard cannot read`: the alias;
- `git option missing its value`: the option.

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
