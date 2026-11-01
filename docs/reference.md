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

Every outcome but done prints its reason on the line above `Next:`, labeled by outcome: `Refused:`,
`Broke:`, `Ask:`, `Pending:`, or `Delivered, not green:`. A run stopped by Ctrl-C or SIGTERM (the
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

## The guard

`ils guard`, `ils parse`, and `ils hook` run on macOS and Linux. On Windows each one refuses with
exit 2 and says the guard runs on macOS and Linux. `ils guard` and `ils parse` take the command
to judge in one quoted word after `--`; no word, or more than one, is a refusal.

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
dir it targets. A directory the guard cannot tell prints `unknown`; a target outside any
repository prints `none`.

It gives no verdict. A mark names what `ils guard` would refuse, and `ils parse` still exits 0.
It exits 1 when the command does not parse or a state read fails (a `git config` read of an
alias, the git dir), and 2 when `--cwd` or the command word is missing.

### ils hook

`ils hook claude` is Claude Code's `PreToolUse` hook. It reads the hook's JSON on stdin and judges
`tool_input.command` from `cwd`:

- allow: exit 0, nothing printed;
- refuse: exit 2, the reason on stderr;
- ask: exit 0, with `{"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision":
  "ask", "permissionDecisionReason": "<reason>"}}` on stdout.

A tool other than `Bash` is allowed. A payload that is not JSON, or that holds no `tool_name`, no
`tool_input.command`, or no `cwd`, a `cwd` that does not exist, and every internal error exit 2
with a reason on stderr. `ils hook` never exits 1, since Claude Code runs a tool call whose hook
exits with anything but 2. A harness other than `claude`, or none, exits 2 and names the
harnesses it knows.

`skills/ils/hook.sh` fronts `ils hook claude`. It passes exit 0 and 2 through with the binary's
output. Any other exit becomes exit 2, with a line on stderr naming the code. When `ils` is not
on `PATH` or cannot start, it exits 2 and names `! task install`.

### What the guard refuses

How a command is read:

- a command that does not parse, and an extended glob anywhere in it (zsh runs a glob qualifier
  such as `*(e:'git push':)` as code);
- a command word that is not a literal (`$(which git)`, `"$GIT"`);
- a `git`, `gh`, `ils`, or shell word (`bash`, `sh`, `zsh`, `dash`, `eval`, `source`) handed to a
  command the guard does not know (`xargs`, `timeout`, `sudo`, `find -exec`). The guard sees
  through the wrappers `env`, `command`, `builtin`, `exec`, `nice`, `nohup`, `time`, `noglob`, and
  `nocorrect`. It takes the words given to a reader, a filter, a file tool, a lookup, or a package
  manager (`echo`, `grep`, `rg`, `cat`, `cp`, `which`, `go`, `brew`, and the like) as data, so
  `echo git push` holds no `git` call. `ils status`, `parse`, `guard`, `hook`, and `help` are
  allowed behind such a command (`timeout 60 ils status`); any other `ils` command, or one the
  command fills in (`xargs ils`), is refused;
- a string handed to a command the guard does not know, read as a script, when one of its
  commands is such a word or hands one over in turn: `su -c 'cd /repo && git push'`, `ssh host
  'sudo git push'`, and `su -c "git push $b"`, whose expansion stands as a plain word. A string
  that does not parse as a script is data;
- a `bash -c`, `sh -c`, `zsh -c`, `dash -c`, or `eval` string that is not a literal or nests more
  than three deep, and a shell that runs what the command does not hold (`cat x | bash`, `bash
  -s`, `bash ./x.sh`, `bash <(…)`, `source` or `.` of a file). The shell commands git runs are
  read the same way: the value of `git rebase --exec` (`-x`), of `-c core.editor`, `core.pager`,
  `sequence.editor`, `core.sshCommand`, or `pager.<command>`, and of `GIT_EDITOR`,
  `GIT_SEQUENCE_EDITOR`, `GIT_PAGER`, and the `EDITOR`, `VISUAL`, and `PAGER` git falls back to;
- an assignment or `export` of a `GIT_*` variable off the allow list (`GIT_CONFIG_COUNT`,
  `GIT_EXEC_PATH`, and the like);
- a git word that is neither a command nor an alias, a `!` alias, an alias nested more than
  three deep, and an alias the guard cannot read: a value that does not split into words, or one
  set by a `-c` or `--config-env` it cannot follow. A subcommand that is not a literal (`git
  "$sub"`), and any word off git's command list where the guard cannot tell the git dir (after
  `cd "$d"`, or under `-C "$d"`), count as neither;
- a git option at the end of the command with no value after it (`git -C`), and a `-c` or
  `--config-env` value that is not a literal, on any git command, since it may set
  `core.hooksPath`;
- a non-literal argument a flag rule turns on (`git clean "$opt"`). A commit message (`-m
  "$(cat <<'EOF' … EOF)"`) and the directory of `-C "$dir"` are passed over.

git operations that destroy work or skip checks:

- every `checkout`, `reset`, `stash`, and `prune`; `switch --force` or `--discard-changes`; `clean
  --force`; `branch -D`, `-d -f`, or `--delete --force`; `restore` without `--staged` (or `-S`), or
  with `--worktree` (`-W`) beside it; `rm
  --force`; `reflog expire`, `delete`, and `drop`; `gc --prune=…`; `apply --reverse`; `add --force`;
- `--no-verify` on any command, `commit -n`, `am -n`, `send-email --no-validate`, and
  `core.hooksPath` set through `-c` or `--config-env`;
- `git show` or `git cat-file` with stdout redirected to a file;
- `rebase` over uncommitted changes, or where the guard cannot tell the target;
- `worktree remove --force`, and `worktree remove` of a worktree with changes or one the guard
  cannot find;
- `git config` writing `alias.*`, `include.*`, `includeIf.*`, `remote.*`, `pager.*`,
  `core.hooksPath`, `core.editor`, `core.pager`, `core.sshCommand`, or `sequence.editor`, at any
  scope, renaming or removing a section that holds one of them, and `git config --edit`, which
  can write any of them; `git remote remove`, `rm`,
  `rename`, `set-url`, or `set-head` on `origin`.

<!-- cspell:ignore verif -- a prefix git accepts for --no-verify, which the example needs -->

Long options match as git matches them: git accepts any unambiguous prefix, so `--no-verif` counts
as `--no-verify`.

Pushes and the trunk:

- every `git push`, `git send-pack`, and `git http-push`, in any repository: `ils push` is the way
  an agent pushes a branch;
- in a repository with an `origin`, on its default branch: `commit`, `merge`, `cherry-pick`,
  `revert`, `am`, `rebase`, and `pull` without `--ff-only`. The reason names `ils branch <name>`.
  A `git rebase` naming the default branch as the branch to rebase counts as one on it. The guard
  asks origin for the default branch once per command and git dir, and falls back to the cached
  `refs/remotes/origin/HEAD`; with neither, it refuses and names `git fetch origin`;
- one of those where the guard cannot tell the directory, the target, or HEAD: after `cd -`,
  `popd`, or a `cd` to a non-literal, and after a command that moves HEAD earlier in the list
  (`git switch main && git commit`).

`ils` and `gh`:

- `ils merge`, `ils release`, `gh pr merge`, and `gh release create` ask when the command is that
  one call by its bare name. Behind a wrapper, in a list or pipe, with a redirect or `&`, in a
  `-c` string, or spelled as a path or with a backslash, they are refused;
- `ils push` and `ils branch` are allowed as the whole command by their bare name, and refused in
  any other form, piped or `tee`d included;
- `gh pr merge --admin` in any form;
- `gh api` with a method other than `GET`, or with no method but a field or `--input`;
  `gh repo sync`, `delete`, `edit`, and `create --push`; `gh release delete`, `edit`, `upload`,
  and `delete-asset`; `gh auth token`, `gh auth status --show-token`, and `gh config get
  oauth_token`; `gh pr checkout --force`;
- an `ils` or `gh` command word that is not a literal (`ils "$cmd"`);
- `gh alias set`, `import`, and `delete`; a `gh` word that is neither a command nor an alias,
  extensions included; a `!` alias; a `gh` alias given an argument that is not a literal to fill
  a `$N`, or whose expansion the guard cannot read, such as a `$2` left unfilled, or that expands
  to no `gh` command; and a `gh alias list` that fails.

A read the guard needs that fails (a missing `git`, a `gh alias list` that errors) is a refusal
that says what broke.

### Guard JSON output

`ils parse --json` puts its detail under `payload.parse`, and `ils guard --json` under
`payload.guard`, beside the verdict in `outcome` and `reason`. Both hold `invocations`, in the order
the guard reads them, which is not always the order the shell runs them; a command that does not
parse holds none. Each invocation has:

| Field   | Type             | Meaning                                                                                                          |
| ------- | ---------------- | ---------------------------------------------------------------------------------------------------------------- |
| `tool`  | string           | `git`, `gh`, or `ils` when the command word names one. Absent otherwise.                                         |
| `dir`   | string           | The absolute directory the invocation runs in. Absent when the guard cannot tell it.                             |
| `words` | array of objects | Each word as `text` and `literal`. A word that is not a literal carries its literal prefix as `text`.            |
| `marks` | array of objects | What the guard refuses here, as `kind` and, when it names one, `name` (an alias, a variable).                    |
| `git`   | object           | For a git invocation: `subcommand` (a word), `args` (words), `git_dir`, and `git_dir_unknown`. Absent otherwise. |

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
