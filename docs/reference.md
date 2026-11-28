# ils reference

This page is the full reference for `ils`. It covers what each command does step by step, exit
codes, time limits, stopping a run, every refusal, the JavaScript Object Notation (JSON) output, the
guard, and the terminal output. The [README](../README.md) says why `ils` exists, how to install and
set it up, and what each command is for. CI stands for continuous integration throughout.

[Exit codes](#exit-codes) defines the outcome words (done, broke, refused, ask, pending, and
delivered, not green) and the `Next:` line, which the sections before it already use.

- [Commands](#commands)
  - [Flags](#flags)
  - [ils branch](#ils-branch)
    - [Branch by hand](#branch-by-hand)
    - [A local default branch ahead of origin's](#a-local-default-branch-ahead-of-origins)
  - [ils push](#ils-push)
    - [After a green push](#after-a-green-push)
    - [Push by hand](#push-by-hand)
  - [ils merge](#ils-merge)
    - [How merge ends](#how-merge-ends)
    - [A merge made elsewhere](#a-merge-made-elsewhere)
    - [Merge by hand](#merge-by-hand)
  - [ils status](#ils-status)
  - [ils release](#ils-release)
    - [Resume a broken release](#resume-a-broken-release)
    - [Release by hand](#release-by-hand)
  - [ils init](#ils-init)
    - [Init by hand](#init-by-hand)
- [Exit codes](#exit-codes)
  - [Done and broke by command](#done-and-broke-by-command)
  - [When no check reports](#when-no-check-reports)
- [Time limits](#time-limits)
- [Stopping a run](#stopping-a-run)
- [Refusals](#refusals)
  - [What branch refuses](#what-branch-refuses)
  - [What push refuses](#what-push-refuses)
  - [What merge refuses](#what-merge-refuses)
  - [What status refuses](#what-status-refuses)
  - [What release refuses](#what-release-refuses)
  - [What init refuses](#what-init-refuses)
- [JSON output](#json-output)
  - [Push JSON output](#push-json-output)
  - [Merge JSON output](#merge-json-output)
  - [Status JSON output](#status-json-output)
  - [Release JSON output](#release-json-output)
  - [Init JSON output](#init-json-output)
- [The guard](#the-guard)
  - [What the guard does not see](#what-the-guard-does-not-see)
  - [ils guard](#ils-guard)
  - [ils parse](#ils-parse)
  - [ils hook](#ils-hook)
  - [What the guard refuses](#what-the-guard-refuses)
    - [How the guard reads a command](#how-the-guard-reads-a-command)
    - [Git operations that destroy work or skip hooks](#git-operations-that-destroy-work-or-skip-hooks)
    - [Pushes and the default branch](#pushes-and-the-default-branch)
    - [The default branch on GitHub](#the-default-branch-on-github)
    - [gh and ils calls the guard refuses](#gh-and-ils-calls-the-guard-refuses)
  - [ils and gh commands the guard allows or asks about](#ils-and-gh-commands-the-guard-allows-or-asks-about)
  - [Guard JSON output](#guard-json-output)
- [Terminal output](#terminal-output)

## Commands

### Flags

| Flag            | Commands                       | Effect                                                                                                                                                           |
| --------------- | ------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--json`        | every command but `ils hook`   | Prints the result as one JSON object; see [JSON output](#json-output).                                                                                           |
| `-d`, `--debug` | every command but `ils hook`   | Traces each `git` and `gh` call and each wait decision on stderr; see [Terminal output](#terminal-output).                                                       |
| `--no-color`    | every command but `ils hook`   | Turns color and bold off on both streams.                                                                                                                        |
| `--help`, `-h`  | every command                  | Prints the command's usage. `ils help <command>` does the same.                                                                                                  |
| `--wait`        | `push`, `merge`, and `release` | Waits for the checks up to the long [time limit](#time-limits) even off a terminal. On by default at a terminal; `--wait=false` keeps the short limit there too. |
| `--force`       | `release`                      | Skips the wait for the checks and says nothing verified the commit. Every other refusal still holds.                                                             |
| `--tag-only`    | `release`                      | Stops once the tag is on origin, and creates no GitHub release.                                                                                                  |
| `--project`     | `init`                         | Sets up `.claude/` at the top level of the repository the working directory is in, instead of `~/.claude/`.                                                      |

Each flag in the table takes `=true` or `=false`. A command refuses a flag the table does not list
for it, and a flag given any other value.

`ils guard` and `ils parse` also take `--cwd <dir>`; see [ils guard](#ils-guard). `ils help`
prints the usage of `ils` and its commands, and `ils --version` prints the version of `ils`.

### ils branch

Run `ils branch <name>` before you start new work. It:

1. fetches the default branch from origin;
2. creates `<name>`, exactly as typed, at origin's copy of the default branch, with no upstream;
3. switches to it, carrying your uncommitted changes and untracked files along.

The new branch starts at origin's default branch whatever you stand on. Another work branch's
commits never ride along into the new pull request, and a stale local default branch never holds
it back. When the local default branch has commits origin's default branch lacks, `ils branch`
refuses, as [A local default branch ahead of origin's](#a-local-default-branch-ahead-of-origins)
describes. `ils branch` needs `git` alone: it runs without `gh`, and with an `origin` that is not
on GitHub.

`ils branch --json` prints the [JSON object](#json-output) every command prints, with no payload.

[What branch refuses](#what-branch-refuses) lists every refusal.

#### Branch by hand

The same steps, by hand, for a branch `fix-help` on `main`:

```bash
git fetch origin main
git ls-remote --heads origin   # no branch named fix-help, a path prefix of it, or under it
git switch -c fix-help --no-track origin/main
```

#### A local default branch ahead of origin's

`ils branch`, `ils push`, and `ils merge` refuse while the local default branch holds commits that
origin's lacks, whether or not it is also behind origin's. `ils status` ends on the same step. A
branch cut beside those commits would carry work no pull request lands, so `ils` stops until they
are on a branch of their own. It never moves them itself: `Next:` names one command for you to
run, which keeps them on a new branch and puts the local default branch back at origin's. A
leading `!` there means you run the command yourself from Claude Code's prompt, which skips the
guard's hook.

A read of the local default branch that fails other than by the time limit or an interrupt ends
each of these commands broke.

### ils push

Run `ils push` on a work branch with everything committed. It:

1. checks every precondition before it pushes or opens anything, and refuses with a reason when
   one does not hold;
2. pushes the branch by name, never with a force flag;
3. opens a pull request against origin's default branch when the branch has none, titled with the
   branch name and with an empty body;
4. waits for the checks of the pushed commit until you stop it or its [time limit](#time-limits)
   has passed since the command started, then prints the verdict.

Run it again at any time: on a commit that is already pushed it pushes nothing and reports the
verdict again.

Over Secure Shell (SSH), `ils push` and the tag push of `ils release` add a keep-alive to the
push, so that a slow pre-push hook does not lose the connection.
[A slow pre-push hook](../README.md#a-slow-pre-push-hook) says when they add it, and when you set
it yourself. Both run git with `LC_ALL=C`, so your pre-push hook runs in the C locale too.

When another commit reaches the pull request while `ils push` waits, the checks GitHub reports are
no longer those of the pushed commit. `ils push` then prints no verdict and ends
[refused](#exit-codes), naming the pull request's new head. The push itself already happened: pull
the new commit, then run `ils push` again.

[What push refuses](#what-push-refuses) lists every refusal, and
[Push JSON output](#push-json-output) describes `ils push --json`.

#### After a green push

The pull request's title and body become the squash commit's message, so write them first. The
title must be a conventional commit subject, such as `fix(cli): reject an empty name`:

```bash
gh pr edit --repo <host>/<owner>/<name> <N> --title "<title>" --body "<body>"
```

Then run `ils merge`. When the pull request is a draft, needs a review, or is blocked,
[ils merge refuses it](#what-merge-refuses), and the `Next:` line of `ils push` names the step to
take first.

To merge a pull request whose checks are red anyway, see [Merge by hand](#merge-by-hand).

#### Push by hand

The same steps, by hand, for a branch `fix-help` on `main`:

```bash
git fetch origin main
git push --set-upstream origin fix-help:refs/heads/fix-help
gh pr create --repo <host>/<owner>/<name> --base main --head fix-help --title fix-help --body ""
gh pr checks --repo <host>/<owner>/<name> <N> --watch
```

### ils merge

Run `ils merge` on the branch whose pull request you want to land. It:

1. checks every precondition before it writes anything, and refuses with a reason when one does
   not hold;
2. waits for the checks of the pull request's head, as `ils push` does, until you stop it or it
   reaches its [time limit](#time-limits);
3. squash-merges the pull request, with its title plus `(#<N>)` as the subject and its body as
   the commit body. The trailer block GitHub adds (`Signed-off-by` and `Co-authored-by` lines)
   follows the body;
4. deletes the branch on origin and in the clone, and leaves the default branch checked out at
   origin's tip.

`ils merge` never pushes and has no `--force`. It refuses a commit that is not on the pull request
yet: push it with `ils push` first. When the checks are still running, or fewer than 20
seconds are left for the merge, `ils merge` ends pending with nothing written.

[What merge refuses](#what-merge-refuses) lists every refusal, and
[Merge JSON output](#merge-json-output) describes `ils merge --json`.

#### How merge ends

After the merge, `ils merge` deletes the branch on origin and in the clone, and leaves the default
branch checked out at origin's tip. Three cases end differently.

When the default branch is checked out in another worktree, `ils merge` leaves that worktree
alone. It detaches the current worktree at origin's default branch instead, and deletes the local
branch.

`ils merge` deletes the local branch only while it still points at the commit it merged. When a
commit landed on the branch during the wait, it keeps the branch and ends broke.

When a step after the merge fails, the delete on origin included, `ils merge` ends broke with the
pull request merged, and `Next:` gives what is left to run.

#### A merge made elsewhere

When the branch's pull request is already merged into the default branch, on the web or by a run
of `ils merge` that was cut short, `ils merge` merges nothing and reads no check. It deletes the
branch on origin if it is still there, then ends as [How merge ends](#how-merge-ends) describes.
This also covers a pull request that got an "Update branch" or a committed suggestion on the web
before its merge, and one merged on the web with a merge commit, which leaves the branch with no
commit past the default branch: `ils merge` resumes it when the merged pull request's head is the
branch's tip. When the branch has commits the merge did not carry, `ils merge` refuses and
deletes nothing.

#### Merge by hand

To merge a pull request whose checks are red anyway, merge it by hand. `ils merge` never does:

```bash
gh pr merge --repo <host>/<owner>/<name> <N> --squash --delete-branch \
  --match-head-commit <sha> --subject "<title> (#<N>)" --body "<body>"
```

### ils status

Run `ils status` to learn where the checkout stands before you act. It exits 0 once its words
parse: what it could not read is a line of its output, not a refusal. The one exception is a failed
read of the local default branch, which ends it broke as
[A local default branch ahead of origin's](#a-local-default-branch-ahead-of-origins) says. It
fetches what it reads from origin (the default branch, the branch's copy, a merged pull request's
head, a version tag) and writes nothing of yours. When origin cannot be reached, it counts against
the last fetch and says so.

What it reports depends on where HEAD stands:

- **On a work branch:** how the branch stands against its copy on origin (up to date, ahead,
  behind, diverged, or not on origin). Its open pull request, with its title, body, check verdict,
  and merge state. Every closed or merged pull request the branch had.
- **On the default branch:** how it stands against origin's. The last version tag (the highest
  `v*` version tag origin holds on its default branch) and the commits since it. Whether the tip
  is a release commit, and whether rules protect the branch.
- **On a detached HEAD:** the commits origin lacks.

`Next:` is the step the command you would run next would end on, taken from that command's own
checks run over what the status read: `ils push` on a work branch, and `ils branch <name>` on the
default branch and on a detached HEAD.

With `gh` missing or logged out, it says so and still reports what `git` alone can tell.

[Status JSON output](#status-json-output) describes `ils status --json`.

### ils release

Run `ils release <X.Y.Z>` once the release commit is on the default branch. The version takes no
leading `v`, and may carry a pre-release part, such as `1.2.0-rc.1`. It:

1. checks every precondition before it writes anything, and refuses with a reason when one does
   not hold. It reads the release from origin, whatever the checkout: origin's tags, the
   first-parent history of origin's default branch as fetched now, and `CHANGELOG.md` at the
   release commit. From the clone it reads only the local tag `v<X.Y.Z>`;
2. finds the release commit: the newest commit on that history, since the previous version tag,
   whose subject is `chore: release v<X.Y.Z>`, with or without a trailing `(#<N>)`. It need not be
   the tip: commits merged after it stay out of the tag;
3. reads the release notes from the `## [X.Y.Z]` section of `CHANGELOG.md` at the release commit;
4. waits for the checks of the release commit, as `ils merge` does, until you stop it or it
   reaches its [time limit](#time-limits). It counts the runs a push started on the release
   commit. It leaves out the runs that judge something else: a run another workflow started
   (`workflow_run`), a scheduled or manual run, and a Dependabot run. GitHub files a `workflow_run`
   run under the default branch's tip whatever started it, pull request CI included;
5. tags the release commit `v<X.Y.Z>` with an annotated tag and pushes the tag to origin;
6. creates the GitHub release of the tag, titled with the tag and with the notes as its body, and
   marked as a pre-release when the version has a pre-release part.

[Flags](#flags) describes `--force` and `--tag-only`.

When the checks are still running, or fewer than 20 seconds are left for the writes, `ils release`
ends pending with nothing written. A repository with no CI on its default branch goes on, and a
line says nothing verified the commit.

[What release refuses](#what-release-refuses) lists every refusal, and
[Release JSON output](#release-json-output) describes `ils release --json`.

#### Resume a broken release

`ils release` writes in a fixed order: the local tag, the tag on origin, then the GitHub release.
Each step first looks at what is already there, so a run that broke resumes where it stopped:

- a local tag `v<X.Y.Z>` already at the release commit is kept, not made again;
- a tag `v<X.Y.Z>` already on origin at the release commit means the release was tagged before: the
  run reads no check and pushes nothing, and goes on to the GitHub release;
- a GitHub release that already exists for the tag is left as it is, and its URL is reported.

When origin refuses the tag push, `ils release` ends broke with the tag made locally. When the
GitHub release cannot be created, it ends broke with the tag already on origin. Either way, the
same command resumes, after the fix when there is one.

#### Release by hand

The same steps, by hand, for `1.2.0` on `main`:

```bash
git fetch origin main
git log --first-parent --format='%H %s' origin/main   # the newest chore: release v1.2.0
gh run list --repo <host>/<owner>/<name> --commit <sha> --event push   # all green before you go on
gh api --hostname <host> --paginate repos/<owner>/<name>/commits/<sha>/status \
  --jq '.statuses[] | "\(.state) \(.context)"'   # its commit statuses, all success
# its checks from GitHub Apps other than Actions, all success
gh api --hostname <host> --paginate repos/<owner>/<name>/commits/<sha>/check-runs \
  --jq '.check_runs[] | select(.app.slug != "github-actions") | "\(.conclusion) \(.name)"'
git tag --annotate --message v1.2.0 v1.2.0 <sha>
git push origin refs/tags/v1.2.0
gh release create v1.2.0 --repo <host>/<owner>/<name> --verify-tag --title v1.2.0 \
  --notes-file <notes>   # add --prerelease for a version such as 1.2.0-rc.1
```

`<notes>` is a file holding the `## [1.2.0]` section of `CHANGELOG.md` at the release commit.

### ils init

Run `ils init claude` once to set up Claude Code for `ils`, and again after you upgrade `ils`.
`claude` is the one harness (the program that runs the agent's tool calls) it knows. It writes
under the Claude Code directory: `~/.claude/`, or the directory `CLAUDE_CONFIG_DIR` names when it
is set and not empty, as Claude Code itself reads it. With `--project`, it writes `.claude/` at the
top level of the repository the working directory is in, whatever `CLAUDE_CONFIG_DIR` holds. It:

1. reads `settings.json` and refuses a shape it does not recognize, before it writes anything: a
   file that is not JSON, or not a JSON object, a member name held twice in one object, a string
   escape that names no character, or a wrong type for `hooks`, `hooks.PreToolUse`, `permissions`,
   `permissions.allow`, `permissions.ask`, or `permissions.deny`;
2. installs the `ils` and `write-release` skills the binary ships, in `skills/<name>/`. It
   replaces a shipped file that differs, and leaves a file it does not ship;
3. adds the guard's hook and the permission rules to `settings.json`, and creates the file when
   it is missing, so that it holds what [Init by hand](#init-by-hand) shows.

`ils init` never writes through a skill directory that is a symbolic link, such as a clone of
`ils` linked in. When the files behind the link are the shipped ones, the step has nothing to do.
Otherwise the run still does step 3, then ends broke, naming the skill. A `settings.json` that is
a symbolic link is written through to its target.

The hook's command runs the installed `hook.sh`: `bash ~/.claude/skills/ils/hook.sh`, the
absolute path instead when `CLAUDE_CONFIG_DIR` sets the directory, and
`bash "$CLAUDE_PROJECT_DIR"/.claude/skills/ils/hook.sh` with `--project`. Without
`onFailure: block`, Claude Code runs a command whose hook could not run. `onFailure` needs the
Claude Code version [Use with Claude Code](../README.md#use-with-claude-code) names.

A hook whose command runs `skills/ils/hook.sh`, under any directory, is a guard entry. In
`settings.json`, `ils init` changes only these:

- **Guard entries:** each one in a `PreToolUse` group whose matcher covers `Bash`, as Claude Code
  reads it, becomes the hook `ils init` writes, with no other key. A key such as `async` or `if`
  would keep the hook from blocking, or narrow the commands it judges. One in a group whose
  matcher does not cover `Bash` is removed, and so is a group that leaves with no hooks. When no
  group that covers `Bash` held one, a group with the matcher `Bash` is added at the end of
  `PreToolUse`.
- **Permission rules:** each rule lands in the list [Init by hand](#init-by-hand) puts it in,
  unless one of the three lists already holds it. No rule covers `ils push`: the guard
  [answers it itself](#ils-and-gh-commands-the-guard-allows-or-asks-about). `ils init` removes
  `Bash(ils push)` and `Bash(ils push --wait)` from `allow`, where an earlier `ils init` added
  them: such a rule could let a push to a private repository run without a prompt.

Every other key, value, and entry of `settings.json` stays, in its order, and a run that changes
nothing leaves the file as it was.

A run with nothing to change ends done with no `Next:` line. A run that cannot read or write a
settings file or a skill file ends broke, naming the file. Skills written before a failed settings
write stay, and running the command again finishes the job.

#### Init by hand

Copy `skills/ils/` and `skills/write-release/` from a clone of `ils` at the version you run into
`~/.claude/skills/`, then merge this into `~/.claude/settings.json`:

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
  },
  "permissions": {
    "allow": ["Bash(ils branch *)", "Bash(ils status)"],
    "ask": [
      "Bash(ils merge)",
      "Bash(ils merge *)",
      "Bash(ils release)",
      "Bash(ils release *)",
      "Bash(gh pr merge)",
      "Bash(gh pr merge *)",
      "Bash(gh release create)",
      "Bash(gh release create *)"
    ],
    "deny": ["Bash(git push)", "Bash(git push *)"]
  }
}
```

Remove `Bash(ils push)` and `Bash(ils push --wait)` from `allow` if an earlier setup put them
there.

## Exit codes

| Exit | Means                | Reaction                                                                                                                                                                                                                                                                       |
| ---- | -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 0    | done                 | The command finished its job; [Done and broke by command](#done-and-broke-by-command) says what that means for each. Follow `Next:` when there is one.                                                                                                                         |
| 1    | broke                | The output says what was done and what was not. Fix what failed, then follow `Next:`.                                                                                                                                                                                          |
| 2    | refused              | Nothing was pushed, opened, merged, tagged, or set up, except by a push whose pull request's head moved during the wait. The reason says why; `Next:` says what to do.                                                                                                         |
| 3    | ask                  | `ils guard` only: the command is one the user approves before it runs. See [ils guard](#ils-guard).                                                                                                                                                                            |
| 4    | pending              | GitHub had not finished when the command stopped waiting, for example checks still running or not yet reported. Run the command that the `Next:` line names; [Time limits](#time-limits) says how long each command waits. After an `ils release`, run the same command again. |
| 5    | delivered, not green | Push only. The push happened; a check is red or missing, or the branch conflicts with or is behind the default branch. Follow `Next:`.                                                                                                                                         |

Every outcome but done prints its reason labeled by outcome: `Refused:`, `Broke:`, `Ask:`,
`Pending:`, or `Delivered, not green:`. The reason sits on the line above `Next:`, except in a
verdict of `ils guard`, which prints no `Next:` line (JSON `next: "none"`).
A run stopped by Ctrl-C or SIGTERM (the signal `kill` sends by default) exits with the code of
the result it prints; see
[Stopping a run](#stopping-a-run).

### Done and broke by command

Exit 0 means, for each command:

- **Push:** the checks are green, or the repository has no CI and a line says nothing verified the
  commit; follow `Next:`.
- **Merge:** the pull request is merged and the branch deleted.
- **Branch:** the branch is started.
- **Status:** whatever it finds, once its words parse. A failed read of the local default branch
  ends it broke.
- **Release:** the tag is on origin, and the GitHub release exists unless `--tag-only`.
- **Init:** the skills, the rules, and the hook are in place.
- **Guard, Parse, and Hook:** [ils guard](#ils-guard), [ils parse](#ils-parse), and
  [ils hook](#ils-hook) give their exits.

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

`ils branch`, `ils status`, `ils guard`, `ils parse`, `ils hook`, and `ils init` run under a limit
of 100 seconds, counted the same way. They take no `--wait`.

`ils guard` and `ils hook` also give every state read of one command, calls to origin included, 5
seconds in all. A read cut short is a refusal that says what broke. Claude Code stops the hook
after the 15-second `timeout` that `ils init` writes.

`ils merge` and `ils release` stop waiting 20 seconds before the full limit of 15 minutes or 100
seconds. Those 20 seconds are kept for the writes that follow. When fewer than 20 seconds are left,
they end pending with nothing written.

`ils push` waits until GitHub shows the pushed commit as the pull request's head. When another
push moves that head past the pushed commit before GitHub showed it, `ils push` waits until the
time limit and ends pending. The next `ils push` refuses the branch as behind its copy on origin.

A failed check keeps the wait going until its log is available. A canceled check gets no log, so
it ends the wait red at once.

When a pending `ils merge` names `ils push` on its `Next:` line, run that push with the same
`--wait` as the merge, so that it takes the same column of the table above. If your script or
harness gives `ils` a terminal but stops it on a timeout shorter than 15 minutes, pass
`--wait=false`. An `ils merge` or `ils release` stopped
between two of its writes continues on the next run.

## Stopping a run

Ctrl-C or SIGTERM during `ils push`, `ils merge`, or `ils release` stops the step in progress.
`ils` then prints the result of that step, as text or as JSON per `--json`, and exits with that
result's code.

An `ils release` stopped before its first write has written nothing. Stopped while it makes the tag
or looks up the GitHub release, the same command resumes as
[Resume a broken release](#resume-a-broken-release) describes.

When a time limit or a stop ends a run of git, `ils` also ends the processes git started, such as
your pre-push hook and what the hook started.

A second Ctrl-C or SIGTERM, or a Ctrl-\ (SIGQUIT), ends `ils` at once, with no result and none of
the [exit codes](#exit-codes).

## Refusals

A refusal ends with exit 2: the command wrote nothing, and the reason says why. The one exception
is the moved head at the end of [What push refuses](#what-push-refuses).

An `origin` can answer and still turn the call away, for a failed sign-in, a missing permission, or
a repository it cannot find. That is not a refusal. A command whose call to origin is turned away
before it lands anything ends broke with git's message, and `ils status` lists the read as failed.
Only an `origin` that cannot be reached at all is a refusal (exit 2).

`ils branch`, `ils push`, `ils merge`, and `ils release` also refuse a flag [Flags](#flags) does not
list for them, and a precondition read that the time limit, Ctrl-C, or SIGTERM cut short: run the
same command again, with any `--wait` you gave it.

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
- a local default branch with commits origin's lacks (see
  [A local default branch ahead of origin's](#a-local-default-branch-ahead-of-origins));
- a detached HEAD with commits origin's default branch lacks;
- a switch git refuses because a changed or untracked file would be overwritten;
- a missing `git`, a `git` older than 2.34.1 or one whose version `ils` cannot read, a missing
  `origin`, and an `origin` that cannot be reached.

### What push refuses

When `ils push` refuses, it has pushed nothing, opened nothing on GitHub, and changed no branch
or file; at most it has fetched from origin. The one exception is the moved head at the end of
this list, which `ils push` finds after the push. It refuses:

- the default branch, and a detached HEAD;
- a tracked file with uncommitted changes (untracked files are fine);
- a local default branch with commits origin's lacks, wherever HEAD stands (see
  [A local default branch ahead of origin's](#a-local-default-branch-ahead-of-origins));
- a branch with no commit past origin's default branch;
- a branch that is behind, or has diverged from, its copy on origin;
- a branch whose first commit past the default branch is also on another local branch, unless its
  copy on origin already carries that commit: `ils` cannot tell which branch was cut from which;
- a branch whose pull request already merged into the default branch, or is open against another
  base;
- a repository with no remote named `origin`, or an `origin` that cannot be reached;
- an `origin` whose fetch URL and every push URL do not all name one repository (one differs, or
  one is not an `https://`, `ssh://git@`, or `git@host:` repository URL);
- a missing `git` or `gh`;
- a `git` older than 2.34.1, or one whose version `ils` cannot read;
- a `gh` older than 2.81.0, or one whose version `ils` cannot read;
- a `gh` not logged in to origin's host (this is also how `ils` refuses a host that is not GitHub);
- a fork, or a repository you cannot push to;
- any word after `push` that is not one of its flags;
- a pull request whose head moved to another commit while `ils push` waited for its checks, as
  [ils push](#ils-push) describes.

### What merge refuses

When `ils merge` refuses, it has merged nothing, deleted nothing, and changed no branch or file;
at most it has fetched from origin. It refuses everything `ils push` refuses about the tools,
`origin`, and the repository, and also:

- any word after `merge` that is not one of its flags;
- the default branch, a detached HEAD, and a tracked file with uncommitted changes;
- a local default branch with commits that origin's lacks (see
  [A local default branch ahead of origin's](#a-local-default-branch-ahead-of-origins));
- a branch with no commit past origin's default branch, unless a pull request of it merged into the
  default branch with the branch's tip as its head (see
  [A merge made elsewhere](#a-merge-made-elsewhere)), or with no open pull request against it;
- a branch whose tip is behind, past, or diverged from the head of its pull request;
- a pull request that is a draft, or open against another base;
- a branch whose only pull request merged into another base, which never lands it;
- a pull request whose title is not a conventional commit subject (`<type>(<scope>)!: <summary>`,
  with the scope and `!` optional);
- a repository whose settings forbid squash merges, and a default branch whose ruleset forbids
  them;
- a default branch whose ruleset requires a merge queue;
- a red or canceled check, a required check that never reported, and a head no check reported for
  in a repository with CI;
- a pull request that conflicts with or is behind the default branch, needs a review, lacks a
  required check, or that GitHub blocks for another reason;
- a pull request whose head moved to another commit while `ils merge` waited for its checks;
- on a run that finishes a pull request already merged, a branch on origin that holds commits the
  merged head does not carry.

### What status refuses

`ils status` refuses only the words it is given, before it reads anything. It refuses:

- any word after `status`, and `--`;
- a flag [Flags](#flags) does not list for it.

### What release refuses

When `ils release` refuses, it has tagged nothing, pushed nothing, and created no release; at most
it has fetched from origin. It refuses everything `ils push` refuses about the tools, `origin`, and
the repository, and also:

- no version, a second word, or `--`;
- a version that is not `X.Y.Z` with an optional pre-release part: it refuses `v1.2.0`, `1.2`,
  `1.2.0+build`, and `latest`;
- a version that is not above every other version tag origin holds. Only origin's tags count, not
  the clone's;
- no release commit for the version on origin's default branch since the previous version tag;
- a tag `v<X.Y.Z>` on origin at another commit;
- a local tag `v<X.Y.Z>` at another commit, unless origin already holds the tag at the release
  commit;
- a release commit with no `CHANGELOG.md`, no section for the version, or an empty section, with
  `--tag-only` too;
- a failed or canceled check of the release commit, and a release commit no check reported for in
  a repository with CI.

### What init refuses

When `ils init` refuses, it has written nothing. It refuses:

- no harness, a harness other than `claude`, a second word, or `--`;
- a flag [Flags](#flags) does not list for it;
- without `--project`, a `CLAUDE_CONFIG_DIR` that is set and either not an absolute path or one no
  bash command can name;
- without `--project`, when `CLAUDE_CONFIG_DIR` is unset or empty, a `HOME` that is unset or empty;
- with `--project`, a working directory outside the work tree of a repository;
- with `--project`, a missing `git`, or a read of the repository's top level that the time limit,
  Ctrl-C, or SIGTERM cut short;
- a `settings.json` whose shape step 1 of [ils init](#ils-init) refuses.

## JSON output

With `--json`, a command prints one JSON object with these fields:

| Field       | Type             | Meaning                                                                                                                                                                                                                   |
| ----------- | ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `command`   | string           | The command that ran, such as `push`.                                                                                                                                                                                     |
| `words`     | array of strings | The command-line words `ils` received, less the output flags: `-d`, `--debug` and `--no-color` in any form, and `--json` bare or as `--json=true` or `--json=false`. A refused `--json=<value>` stays. Absent when empty. |
| `outcome`   | string           | `done`, `broke`, `refused`, `ask`, `pending`, or `delivered-not-green`: the Means column of [Exit codes](#exit-codes), with hyphens.                                                                                      |
| `next`      | string           | What to do next, as on the `Next:` line; `none` when nothing is left to do, where the text prints no `Next:` line.                                                                                                        |
| `reason`    | string           | Why the command did not end as `done`. Present on every other outcome.                                                                                                                                                    |
| `excerpt`   | string           | Third-party text: the end of a failed check's log, or of what the hooks and `git` printed for a push `git` refused. Absent when empty.                                                                                    |
| `lines`     | array of strings | What happened, in order; never a line that only restates the reason. Absent when empty.                                                                                                                                   |
| `exit_code` | integer          | The process exit code.                                                                                                                                                                                                    |
| `payload`   | object           | The command's structured detail, under the command's name, such as `payload.push`. Absent when empty.                                                                                                                     |

[Guard JSON output](#guard-json-output) describes the payloads of `ils parse` and `ils guard`.

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
| `standing`           | string           | `branch`, `trunk`, `detached`, `no-origin`, or `no-repository`; empty when where HEAD stands was not read, and `unread` then names the read that failed.                             |
| `branch`             | string           | The branch HEAD is on when `standing` is `branch` or `trunk`; empty otherwise.                                                                                                       |
| `trunk`              | string           | Origin's default branch; empty when it could not be learned.                                                                                                                         |
| `ahead`, `behind`    | integer          | Commits ahead of and behind the branch's copy on origin, or origin's default branch on the default branch.                                                                           |
| `on_origin`          | boolean          | Whether origin has a copy of the branch.                                                                                                                                             |
| `pull_request`       | object or null   | The open pull request against the default branch: `number`, `url`, `title`, `body`, `title_ok`, `verdict`, `merge_state`, `checks`; null when there is none or it was not read.      |
| `past_pull_requests` | array of objects | Each closed or merged pull request of the branch: `number`, and `state` (`closed` or `merged`).                                                                                      |
| `last_tag`           | string           | On the default branch, the last version tag; empty when there is none.                                                                                                               |
| `since_tag`          | integer          | On the default branch, the commits on origin's default branch since the last tag.                                                                                                    |
| `release_commit`     | boolean          | On the default branch, whether origin's tip is a release commit (`chore: release v<X.Y.Z>`, with an optional pre-release part such as `-rc.1`, with or without a trailing `(#<N>)`). |
| `protection`         | string           | On the default branch, `protected`, `none`, or `unavailable` (GitHub refused the rules read of a repository that is not public).                                                     |
| `unlanded`           | array of strings | On a detached HEAD, the commits origin's default branch lacks.                                                                                                                       |
| `unread`             | array of objects | One entry per read that failed: `read` names it and `error` says why. A null `pull_request` with an entry here means it was not read.                                                |

`verdict`, `merge_state`, and `checks` use the words of `payload.push`; `title_ok` says whether the
title is a conventional commit subject, as `ils merge` requires.

### Release JSON output

`ils release --json` prints the [JSON object](#json-output) every command prints, with the detail
under `payload.release`. Once `ils release` has found the release commit, every result carries it,
refusals and pending results included. A result that ends before then (a version not above
origin's tags, no release commit, origin not answering) has no payload.

| Field        | Type    | Meaning                                                                 |
| ------------ | ------- | ----------------------------------------------------------------------- |
| `version`    | string  | The version, without the `v`.                                           |
| `tag`        | string  | The version tag, `v<X.Y.Z>`.                                            |
| `sha`        | string  | The release commit.                                                     |
| `verdict`    | string  | How far the CI gate got, as listed after this table.                    |
| `url`        | string  | The GitHub release's URL once it was created or found; empty otherwise. |
| `prerelease` | boolean | Whether the version has a pre-release part.                             |
| `tag_only`   | boolean | Whether `--tag-only` was given.                                         |

`verdict` is one of:

- `green`: the checks passed.
- `red`: a check failed or was canceled.
- `pending`: the wait ended before the checks concluded, including when none had reported yet.
- `none`: no check or status reported. On a default branch without CI the release goes on, and
  with CI it refuses.
- `forced`: `--force` skipped the gate.
- `skipped`: origin already held the tag at the release commit.
- `unchecked`: the release stopped before the gate read the checks, or the read of the checks
  failed.

### Init JSON output

`ils init --json` prints the [JSON object](#json-output) every command prints, with no payload.
Its `lines` hold the lines the text output prints.

## The guard

The guard judges an agent's shell command before it runs: `ils hook claude` is Claude Code's
`PreToolUse` hook, `ils guard` gives the same verdict to any caller, and `ils parse` prints how the
guard reads a command. It parses the command with a bash parser, finds the `git`, `gh`, and `ils`
calls in it, nested ones included, and answers allow, refuse, or ask.

`ils guard` and `ils parse` take `--cwd <dir>` and the command to read in one quoted word after
`--`; a missing `--cwd`, no word, or more than one word is a refusal.

### What the guard does not see

The guard lowers the odds of an accident; it is not a sandbox. It does not see:

- an interpreter that is not a shell, such as `python -c 'subprocess.run(["git", "push"])'`;
- a command held in a variable and handed to a command the guard does not know, such as `su -c
  "$cmd"`;
- `go run . merge` in a clone of `ils`, or `go run github.com/jeffzi/ils@<tag> merge` anywhere,
  which hold no `ils` word;
- a task runner (`task`, `make`, `npm run`) running lines from a file the agent wrote;
- a shell reading a script the command does not hold, as the command itself: `curl -fsSL
  https://example.com/install.sh | bash`, `bash < x.sh`, `bash <(cat x.sh)`, `bash -s`, a
  here-string such as `bash <<< 'git push'`, or `source <(…)`. Behind a command the guard does
  not know (`timeout 60 bash <(…)`), the guard still refuses such a shell as a shell word handed
  over;
- `gh workflow run` of a workflow whose token can push, tag, or release;
- an alias or function from your shell config, such as `gp='git push'`, which the agent's shell
  runs with no `git` word in the command;
- an alias or a shadowing function the command defines, such as `alias g=git` or a `cd()` that
  wraps `builtin cd`. A non-interactive bash expands no alias, and the guard still judges each
  command of a function body;
- a HEAD move the guard does not follow: a write to `.git/HEAD` by a command other than git, or a
  `git switch` in a background job or a pipe stage, which runs beside the rest of the command;
- an option, variable, or config key that runs a program: `core.pager`, `GIT_PAGER`, `PAGER`,
  `core.sshCommand`, `credential.helper`, the command keys of diff, merge, and tool drivers,
  `difftool -x`, and `go test -exec`. The editors are the exception: the guard reads
  `core.editor` and `sequence.editor` given by `-c` or `--config-env`, `GIT_EDITOR`,
  `GIT_SEQUENCE_EDITOR`, and the `EDITOR` and `VISUAL` git falls back to, where the command sets
  them;
- config written by `git config` that the guard does not read: an editor or any other key that
  runs a program (`git config core.editor vim`), and an `include.path` or `includeIf.*.path` whose
  file can set any key, `core.hooksPath` included. A later command runs what that config names
  from a file the guard never reads. The guard still refuses `git config` of `core.hooksPath`,
  `remote.*`, and `url.*` themselves;
- an export through `set -a`: `set -a; read -r GIT_DIR` exports a variable the guard never sees
  assigned. The guard reads `export GIT_DIR=…`;
- a glob that runs code: a zsh qualifier such as `*(e:'git push':)` or `*(+f)`, or a command
  substitution inside the parentheses of an extended glob. The guard still refuses a command word
  written as a glob (`@(git) push`);
- a redirect an earlier `exec` opens: `exec > README.md; git show HEAD:README.md` writes history
  over a tracked file without the guard's refusal of `git show` into a working tree. The guard
  still refuses a redirect on `git show` itself;
- a construct whose only use is to hide a command, such as a name reference (`declare -n`) that
  writes `GIT_DIR` or a tool name built by a glob.

[Guard scope](guard-scope.md#accepted-limits) says why the guard lets these through. It gives no
reason for three of them: a command handed to `su -c`, `go run` of `ils`, and `gh workflow run`.

### ils guard

`ils guard --cwd <dir> -- '<command>'` judges a shell command as if it ran in `<dir>`:

| Exit | Verdict | Output                                                                                                                                                    |
| ---- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 0    | allow   | One line saying so.                                                                                                                                       |
| 2    | refuse  | `Refused:` and the reason. The guard refuses a command that does not parse, and any error that keeps it from judging, with a reason that says what broke. |
| 3    | ask     | `Ask:` and the reason: the command is one the user approves.                                                                                              |

### ils parse

`ils parse --cwd <dir> -- '<command>'` prints every invocation the command holds, one line each:
its words, its directory, its marks, and, for `git`, the subcommand, its arguments, the git dir it
targets, and the directory git runs in once its `-C` options apply. A mark names a way the guard
could not read the invocation safely.

It gives no verdict. The rules for git operations, pushes, and `gh` leave no mark, so use `ils
guard` for the verdict; `ils parse` exits 0 whatever it marks. It exits 1 when the command does not
parse or a state read fails (a `git config` read of an alias, the git dir), and 2 when `--cwd` is
missing or `--` is not followed by exactly one word.

### ils hook

`ils hook claude` is Claude Code's `PreToolUse` hook, which [ils init](#ils-init) wires in. It
judges each Bash call Claude Code is about to run, and lets every other tool through:

- **Allow:** Claude Code's permission rules decide, as if no hook ran.
- **Allow on the guard's own word:** the command runs without a prompt. Only an `ils push` or
  `ils push --wait` gets this.
- **Ask:** Claude Code prompts, showing the guard's reason.
- **Refuse:** Claude Code blocks the call and shows the agent the reason. A payload the hook cannot
  read, and any internal error, blocks the call the same way.

`ils hook` exits 0 when it allows or asks, and 2 when it refuses or fails, never 1. It takes no
flag. Only `ils hook --help` or `ils hook -h`, alone, prints its usage.

`skills/ils/hook.sh` fronts `ils hook claude`. It passes exit 0 and 2 through with the binary's
output. Any other exit becomes exit 2, and so does an `ils` that is not on `PATH` or cannot start.

### What the guard refuses

#### How the guard reads a command

The guard refuses a command it cannot read, and a `git`, `gh`, or `ils` call it reaches in a way it
cannot follow. Each refusal names a spelling the guard allows. `ils parse` shows how the guard
read a command: each mark it prints names a way the guard could not read an invocation.

- **Parsing and command words:** a command that does not parse, and a command word that is not a
  literal (`$(which git)`, `"$GIT"`, `@(git)`).
- **Variables:** a `$NAME` the command assigned a literal earlier, where the assignment always ran
  before the word, reads as that literal, and so does the variable of a `for` loop over literal
  words. The guard allows `S=/tmp/work; $S/run.sh`, and refuses `G=git; $G push` as `git push`.
  Any other write (`read`, `+=`, `source`, an `IFS` change, a name reference), an assignment that
  may not have run, and a value holding a space or a glob character leave the variable unknown,
  and a word holding an unknown variable is not a literal. The guard also refuses an assignment or
  `export` of a `GIT_*` variable off the allow list (`GIT_CONFIG_COUNT`, `GIT_EXEC_PATH`, and the
  like).
- **Commands handed to another command:** a `git`, `gh`, `ils`, or shell word (`bash`, `sh`, `zsh`,
  `dash`, `eval`, `source`) handed to a command the guard does not know, such as
  `timeout 60 git status` or `xargs sh -c 'git status'`. The guard sees through `env`, `command`,
  `builtin`, `exec`, `nice`, `nohup`, `time`, `noglob`, and `nocorrect`. It takes the words of
  readers, filters, file tools, and package managers as data (`echo git push` holds no `git` call).
  The code given to `sed`, `awk`, `perl`, `python`, `node`, or `ruby` is data too. A shell given a
  literal `-c` string or a script file hands over only what that string or file does:
  the guard allows `xargs sh -c 'ls "$1"' sh`. Behind a command it does not know, the guard allows
  `ils status`, `parse`, `guard`, `hook`, and `help`. It reads the words given to `ssh` or `su` as
  a script, so it refuses `ssh host 'sudo git status'`.
- **Strings read as scripts:** a `bash -c`, `sh -c`, `zsh -c`, `dash -c`, or `eval` string, or a
  heredoc fed to a shell, that is not a literal or nests more than three deep. The same holds for
  a shell command git runs: `git rebase --exec`, `git bisect run`, `git submodule foreach`, the
  `git filter-branch` filters, `git for-each-repo`, and the editor that `core.editor`,
  `sequence.editor`, `GIT_EDITOR`, `GIT_SEQUENCE_EDITOR`, `EDITOR`, or `VISUAL` names. After a
  `source` or `.`, the directory and every variable are unknown, so the guard refuses
  `source env.sh && git commit -m x`: it cannot tell where the commit runs.
- **Functions:** the guard judges each command of a function body the command defines at every
  call, in the directory, environment, and branch of the call, so it refuses
  `f() { git commit -am wip; }; git switch main && f` as a commit on the default branch.
- **Aliases:** a git word that is neither a command nor an alias, a `!` alias, an alias nested more
  than three deep, and an alias the guard cannot read. It also refuses an alias named for a git
  command that git 2.34.1 does not build in: one newer than it (`replay`), or one packaging ships
  on its own (`send-email`). Git runs such an alias when the command's program is missing.
- **Git options and arguments:** a git option with no value at the end of the command (`git -C`); a
  `-c` key or `--config-env` value that is not a literal, since the key may be `core.hooksPath`;
  and an argument that is not a literal to a git command the guard checks for flags
  (`git clean "$opt"`). A commit message, the directory of `-C`, and a path glob whose literal
  prefix does not start with `-` or `+` are exempt: the guard allows `git add src/*.go`, and refuses
  `git add *.go`. `git <sub> --help`, with `--help` the first word after the subcommand, reads as
  `git help <sub>`.

#### Git operations that destroy work or skip hooks

The guard refuses:

- every `checkout`, `reset`, and `prune`;
- every `stash` but `stash list` and `stash show`;
- `switch --force` or `--discard-changes`;
- `clean --force` or `--interactive` (`-i`), unless a dry run (`-n`, `--dry-run`) is set last and
  `--interactive` is not;
- `clean` without `--force` where `clean.requireForce` is off, through `-c`, `--config-env`, or
  the repository's config;
- `branch -D`, `-d -f`, or `--delete --force`;
- `branch -M`, `-C`, or `-m` (`--move`) or `-c` (`--copy`) with `--force`, onto a branch that
  exists in any letter case;
- `restore` without `--staged` (or `-S`), or with `--worktree` (`-W`) beside it;
- `rm --force`;
- `mv --force` (`-f`);
- `submodule update --force` (`-f`) and `submodule deinit --force` (`-f`);
- `reflog expire`, `delete`, and `drop`;
- `gc --prune=…`, and `gc` with `gc.pruneExpire` set through `-c` or `--config-env`;
- `repack -a -d` without `-A`, `--cruft`, or `--keep-unreachable`, and `repack -d` with
  `-A --unpack-unreachable=…` or `--cruft --cruft-expiration=…`, whatever the expiry;
- `checkout-index --force`;
- `read-tree`, unless `-n` (`--dry-run`), `--index-output`, or `--prefix` keeps it off the index,
  and always with `--reset` or `-u -m`;
- `update-ref -d`, `update-ref --stdin`, and `update-ref` whose new value is all zeros or whose
  ref or new value is not a literal;
- `apply --reverse`;
- `add --force`;
- `--no-verify` on any command, `commit -n`, `am -n`, `send-email --no-validate`, and
  `core.hooksPath` set through `-c` or `--config-env`;
- `git show` or `git cat-file` with stdout redirected, or piped straight into `tee`, to a file in a
  git working tree. The same holds for a file whose name the guard cannot read. The guard allows
  output landing in `/dev/null`, or in a file whose nearest existing directory is outside every
  working tree. A `tee` that reads a group, a subshell, or a function call holding `git show` is not
  judged ([rule 3](guard-scope.md#3-close-a-bypass-only-when-agents-write-it));
- `rebase` in a working tree with uncommitted changes or untracked files, or where the guard cannot
  tell the repository. In a dirty tree, `--continue`, `--edit-todo`, `--show-current-patch`, and
  `--quit` are allowed, since they leave the tree as it is; `--abort` and `--skip` are refused;
- `merge --abort`, `cherry-pick --abort`, and `revert --abort` in a working tree with uncommitted
  changes or untracked files, or where the guard cannot tell the repository: they reset the index
  and the tree, staged changes included. `--continue` and `--quit` stay allowed;
- `worktree remove --force`, and `worktree remove` of a worktree with changes or one the guard
  cannot find;
- `git config` writing `remote.*`, `url.*`, or `core.hooksPath`, at any scope, and renaming or
  removing the `remote`, `url`, or `core` section;
- `git remote remove`, `rm`, `rename`, `set-url`, or `set-head` on `origin`.

<!-- cspell:ignore verif -- a prefix git accepts for --no-verify, which the example needs -->

Long options match as git matches them: git accepts any unambiguous prefix, so `--no-verif` counts
as `--no-verify`.

#### Pushes and the default branch

The guard refuses:

- every `git push`, `git send-pack`, and `git http-push`, in any repository: `ils push` is the way
  an agent pushes a branch;
- in a repository whose `origin` is on GitHub, on its default branch, protected or not:
  `commit`, `merge`, `cherry-pick`, `revert`, `am`, and `rebase`. On that branch the guard also
  refuses `pull` without `--ff-only`, and `pull --ff-only` naming a repository or branch other than
  `origin <default branch>`.
  [The default branch on GitHub](#the-default-branch-on-github) says when `origin` counts as on
  GitHub. A `git rebase` naming the default branch as the branch to rebase counts as one on it.
  When the guard can learn the default branch neither from origin nor from the cached
  `refs/remotes/origin/HEAD`, it refuses;
- on that default branch, the same subcommands when the guard cannot tell whether `origin` is on
  GitHub, as listed under [The default branch on GitHub](#the-default-branch-on-github);
- one of those where the guard cannot tell the directory, the target, or HEAD. That happens after
  `cd -`, `popd`, or a `cd` to a non-literal. It also happens after a `git` or `gh` command earlier
  in the list moves HEAD (`git switch main; git commit`).
  The guard follows a `git switch` written as a git command of its own that names one literal
  branch with no option, or creates one with `-c` or `-C` and a literal name. It judges the
  commands after it in the same `&&` list on that branch: it allows
  `git switch -c topic && git commit`, and refuses `git switch main && git commit` as a commit on
  the default branch. A switch in any other form (`-q`, `--detach`, `--create=topic`, `-`) leaves
  HEAD unknown.

#### The default branch on GitHub

The guard judges the repository that `origin`'s first push URL names, since the push is what lands
a commit; the fetch URL plays no part. It reads no branch protection: a commit on the default
branch of a GitHub repository is refused whether or not a rule protects it. Hosts compare without
regard to case, so `GitHub.com` is `github.com`.

| Case                                                                                                           | Verdict                                                                                 |
| -------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| The push URL names a repository on `github.com`                                                                | Refused, whatever `gh` holds or can reach.                                              |
| A host other than `github.com`, and `gh` holds a login there that passes its check                             | Refused: `gh` takes the host for GitHub, such as a GitHub Enterprise Server.            |
| A host other than `github.com`, and `gh` is missing or has no login there                                      | Allowed: the guard cannot tell it is GitHub, and `ils push` refuses that origin anyway. |
| A host other than `github.com`, and `gh`'s login check fails for another reason                                | Refused: the guard cannot tell whether the host is GitHub.                              |
| The guard cannot tell the `GH_CONFIG_DIR`, `XDG_CONFIG_HOME`, or `HOME` the agent's `gh` uses                  | Refused, on a host other than `github.com`.                                             |
| No `origin`, or a push URL that names no repository: a local path, `file://`, or an ssh host starting with `-` | Allowed: no GitHub repository takes its changes.                                        |
| `git` fails to read the push URL, or the host of an ssh URL                                                    | Refused, as a failed read.                                                              |

For a host other than `github.com`, the guard reads the login the agent's own `gh` would use: the
one under the `GH_CONFIG_DIR`, `XDG_CONFIG_HOME`, and `HOME` the command sets.

#### gh and ils calls the guard refuses

The guard refuses:

- `gh pr merge --admin` in any form;
- `gh api` with a method other than `GET`, or with no method but a field or `--input`. A `gh api
  graphql` call whose `query` field is a literal document holding no mutation is a read. The guard
  allows it, with no method or with `POST` in any spelling (`-X POST`, `--method post`). A call
  whose document holds a mutation is a write. The guard also refuses a call whose document it
  cannot read (`-F query=@q.graphql`, `-f query="$q"`, `--input`) until the query is inline;
- `gh repo sync`, `delete`, `edit`, and `create --push`;
- `gh release delete`, `edit`, `upload`, and `delete-asset`;
- `gh auth token`, `gh auth git-credential`, and `gh auth status --show-token`;
- `gh config get oauth_token`;
- `gh pr checkout --force`;
- a flag these rules name (`--admin`, `--input`, `--push`, `--show-token`, `--force`) counts as
  given when its value is one the guard cannot read (`--force="$v"`, `--input="$f"`);
- an `ils` or `gh` command word that is not a literal (`ils "$cmd"`);
- a `gh` word that is neither a command nor an alias, extensions included;
- a `!` alias;
- a `gh` alias given an argument that is not a literal to fill a `$N`;
- a `gh` alias whose expansion the guard cannot read, such as a `$2` left unfilled, or that expands
  to no `gh` command;
- a `gh alias list` that fails.

The guard judges an alias `gh` itself gives a subcommand as the command it names: `gh pr co` as
`gh pr checkout`, `gh release new` as `gh release create`, and `gh repo new` as `gh repo create`.

When a read the guard needs fails (a missing `git`, a `gh alias list` that errors, the read of
`clean.requireForce` or of an alias), the guard refuses and says what broke.

### ils and gh commands the guard allows or asks about

The guard allows or asks about these `ils` and `gh` calls only in the forms each item names:

- **A help call:** an `ils` subcommand, or a subcommand of `gh auth`, `config`, `pr`, `release`,
  or `repo`, whose first word after it is `--help` (`ils merge --help`, `gh pr merge --help`).
  It prints its help and runs nothing. The guard allows it alone, in a pipe, a list, or a
  subshell, and with a redirect. Handed to a command the guard does not know
  (`timeout 60 ils merge --help`), the guard refuses it as any other `ils` or `gh` word there. A
  `--help` past that first word may be the value of an earlier flag, so the guard judges
  `gh pr merge 1 --help` as written.
- **`ils merge`, `ils release`, `gh pr merge`, and `gh release create`:** the guard asks when the
  command is that one call by its bare name, so the prompt shows exactly what runs. The guard
  refuses them behind a wrapper, in a list or pipe, with a redirect or `&`, in a `-c` string, or
  spelled as a path or with a backslash.
- **`ils push` and `ils branch`:** the guard refuses them in any form but the whole command by
  their bare name, piped or `tee`d included. As the whole command, `ils branch` is allowed, and so
  is an `ils push` with words other than `--wait`, such as `ils push --json`: Claude Code's
  permission rules decide those.
- **`ils push` and `ils push --wait`, as the whole command:** the verdict turns on the visibility
  of the repository `origin`'s first push URL names. The guard reads it where the command runs,
  with the `GIT_DIR`, `GIT_WORK_TREE`, `GH_CONFIG_DIR`, `XDG_CONFIG_HOME`, and `HOME` the command
  runs with. Only a `gh` login for the push URL's host counts.

  | Case                                                                                           | Verdict                                                                                              |
  | ---------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
  | A public repository                                                                            | Allowed on the guard's own word: [ils hook](#ils-hook) answers Claude Code with an explicit `allow`. |
  | A private or internal repository                                                               | Asks: the push starts CI runs that spend GitHub Actions minutes.                                     |
  | `gh` missing or with no login for the host, a failed read, or a reply that names no visibility | Asks.                                                                                                |
  | A directory or git dir the guard cannot tell                                                   | Asks.                                                                                                |
  | No repository, no `origin`, or a push URL that names no repository                             | Allowed on the guard's own word: `ils push` refuses that origin itself.                              |

  Two limits come from Claude Code. Its documentation does not say whether a hook's `ask` still
  prompts in `bypassPermissions` mode. In a `claude -p` run, an `ask` that nobody answers ends as a
  denial carrying the guard's reason.

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

The `git` object holds:

- `subcommand`: a word, absent when git runs none;
- `args`: an array of words;
- `git_dir`: absent outside any repository, and when `git_dir_unknown` is true;
- `git_dir_unknown`: whether the guard could not tell the git dir;
- `dir`: the absolute directory git runs in, which pathspecs and relative paths such as
  `commit -F msg.txt` resolve against: the invocation's own `dir` moved by each `-C` in turn.
  Absent when the guard cannot tell it.

## Terminal output

At a terminal, every command shows its progress as it runs:

- **Spinner:** when stderr is a terminal and `-d` is off, one line on stderr names the running
  step and how long it has run, and is erased before anything else prints.
- **Step lines:** for `ils push`, `ils merge`, `ils release`, and `ils init`, when stdout is a
  terminal, each step prints on stdout as soon as it ends, with its duration and a mark: a green
  `✓` when it did its work, a yellow `–` when it had nothing to do, and a red `✗` when it failed.
- **Color:** a stream that is a terminal gets color and bold.

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
