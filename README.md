# ils

`ils` (Instrument Landing System) lands a change on GitHub in fixed, checked steps. A large
language model (LLM) agent that runs it cannot skip a step or push to the wrong place. Each
command runs only standard `git` and `gh` commands, prints what it did and what to do next, and
keeps no state of its own.

This version has five commands. `ils branch <name>` starts a work branch from the default branch
as origin has it. `ils push` pushes a branch, opens its pull request, and waits for the checks.
`ils merge` squash-merges that pull request once its checks are green. `ils status` says where the
checkout stands and what to run next. `ils release <X.Y.Z>` tags the release commit once its checks
are green, and publishes the GitHub release.

- [Example](#example)
- [Install](#install)
  - [Requirements](#requirements)
  - [Build the binary](#build-the-binary)
- [Start a repository](#start-a-repository)
- [Usage](#usage)
- [Push](#push)
  - [What push refuses](#what-push-refuses)
  - [After a green push](#after-a-green-push)
  - [Push JSON output](#push-json-output)
- [Merge](#merge)
  - [What merge refuses](#what-merge-refuses)
  - [How merge ends](#how-merge-ends)
  - [A merge made elsewhere](#a-merge-made-elsewhere)
  - [Merge JSON output](#merge-json-output)
- [Start a branch](#start-a-branch)
  - [What branch refuses](#what-branch-refuses)
- [Status](#status)
  - [Status JSON output](#status-json-output)
- [Release](#release)
  - [What release refuses](#what-release-refuses)
  - [Run it again](#run-it-again)
  - [Release by hand](#release-by-hand)
  - [Release JSON output](#release-json-output)
- [Stopping a run](#stopping-a-run)
- [Exit codes](#exit-codes)
  - [When no check reports](#when-no-check-reports)
- [JSON output](#json-output)
- [Terminal output](#terminal-output)
- [Use with Claude Code](#use-with-claude-code)
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
   gh repo create --source . --push --private
   ```

   Use `--public` in place of `--private` to get rulesets on a free plan; see step 2.

2. Protect the default branch with a ruleset. In the repository on GitHub, open Settings, then
   Rules, then Rulesets. Create a branch ruleset that targets the default branch with these rules:

   - require a pull request before merging, with squash as the only merge method;
   - require status checks to pass;
   - block force pushes.

   If your continuous integration (CI) runs outside GitHub Actions, make the ruleset require its
   checks. Without that, `ils` treats the repository as having no CI.

   GitHub's guide to
   [creating rulesets for a repository](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository)
   has the details. Rulesets need a public repository or a paid GitHub plan; on a free private
   repository skip this step. `ils` works either way. `ils merge` refuses a red merge on its own;
   the ruleset is what blocks one made by hand. A repository you share with a team needs the paid
   plan or must be public. Without the ruleset, nothing stops a teammate from pushing to the
   default branch or merging red. The gates of `ils` hold only for your own agent.

## Usage

```bash
ils push          # push the branch, open its pull request on the first push, wait for the checks
ils push --wait   # the same, waiting up to 15 minutes for the checks even off a terminal
ils push --json   # the same result as one JSON object
ils push --help   # usage of push
ils push -d       # the same push, tracing each git and gh call and wait decision on stderr
ils push --no-color  # the same push, with no color
ils merge         # squash-merge the pull request once its checks are green
ils merge --json  # the same result as one JSON object
ils branch fix-help  # start the branch fix-help from the default branch as origin has it
ils status        # where the checkout stands, and what to run next
ils release 1.2.0 # tag the release commit of v1.2.0 once its checks are green, then publish it
ils release 1.2.0 --tag-only  # the same, stopping once the tag is on origin
ils help          # usage of ils and its commands
ils help push     # the same as ils push --help
ils --version     # print the version of ils
```

`push`, `merge`, `branch`, `status`, and `release` each take `--json`, `-d` or `--debug`,
`--no-color`, and `--help` or `-h`. `push` alone also takes `--wait`, and `release` alone
`--force` and `--tag-only`.

## Push

Run `ils push` on a work branch with everything committed. It:

1. checks every precondition before it pushes or opens anything, and refuses with a reason when
   one does not hold;
2. pushes the branch by name, never with a force flag;
3. opens a pull request against origin's default branch when the branch has none, titled with the
   branch name and with an empty body;
4. waits for the checks of the pushed commit until you stop it or the time limit has passed since
   the command started, then prints the verdict. The time limit is 15 minutes when stdout is a
   terminal and 100 seconds otherwise. `--wait` picks 15 minutes and `--wait=false` 100 seconds,
   wherever it runs.

Run it again at any time: on a commit that is already pushed it pushes nothing and reports the
verdict again.

When another commit reaches the pull request while `ils push` waits, the checks GitHub reports are
no longer those of the pushed commit. `ils push` then prints no verdict and ends refused, naming
the pull request's new head, and `Next:` says to pull the new commit, then run `ils push` again
(`ils push --wait=false` if you gave `--wait=false`). The push itself already happened.

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

### After a green push

The pull request's title and body become the squash commit's message, so write them first. The
title must be a conventional commit subject, such as `fix(cli): reject an empty name`:

```bash
gh pr edit --repo <host>/<owner>/<name> <N> --title "<title>" --body "<body>"
```

Then run `ils merge`. `ils push` prints the `gh pr edit` command on its `Next:` line with the
repository and number filled in; the title and body stay placeholders for you to write.

When the pull request's merge state stands in the way, `Next:` names that step instead:

- **Needs a review:** request a review first, then write the title and body and run `ils merge`.
- **Draft:** mark it ready with `gh pr ready --repo <host>/<owner>/<name> <N>`, then write the
  title and body and run `ils merge`.
- **A required check never reported on the head:** report that, and stop.
- **Blocked by GitHub for another reason:** report that GitHub blocks the merge, and stop.

To merge a pull request whose checks are red anyway, merge it by hand. `ils merge` never does:

```bash
gh pr merge --repo <host>/<owner>/<name> <N> --squash --delete-branch \
  --match-head-commit <sha> --subject "<title> (#<N>)" --body "<body>"
```

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

## Merge

Run `ils merge` on the branch whose pull request you want to land. It:

1. checks every precondition before it writes anything, and refuses with a reason when one does
   not hold;
2. waits for the checks of the pull request's head, as `ils push` does, until you stop it or 80
   seconds have passed since the command started;
3. squash-merges the pull request, with its title plus `(#<N>)` as the subject and its body as
   the commit body. The trailer block GitHub adds (`Signed-off-by` and `Co-authored-by` lines)
   follows the body;
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
  required check, or that GitHub blocks for another reason;
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
with git's message, and the steps not run. `Next:` gives what is left to run. When the delete on
origin fails and origin still lists the branch, the output says the merge went through and the
branch is still on origin, and `Next:` is `ils merge`.

When Ctrl-C or SIGTERM stops `ils merge`, the output says that `ils` was interrupted, and `Next:`
says what to run.

### A merge made elsewhere

When the branch's pull request is already merged, on the web or by a run of `ils merge` that was
cut short, `ils merge` merges nothing and reads no check. It deletes the branch on origin if it is
still there, then ends as [How merge ends](#how-merge-ends) describes. This also covers a pull
request that got an "Update branch" or a committed suggestion on the web before its merge. When
the branch has commits the merge did not carry, `ils merge` refuses and lists them, and `Next:`
says to start a new branch and cherry-pick them.

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

## Start a branch

Run `ils branch <name>` before you start new work. It:

1. fetches the default branch from origin;
2. creates `<name>`, exactly as typed, at origin's copy of the default branch, with no upstream;
3. switches to it, carrying your uncommitted changes and untracked files along.

The new branch starts at origin's default branch whatever you stand on. Another work branch's
commits never ride along into the new pull request, and a stale local default branch never holds
it back. When the local default branch has commits origin lacks, a line names them: they stay on
the local default branch. `ils branch` needs `git` alone: it runs without `gh`, and with an
`origin` that is not on GitHub.

`ils branch --json` prints the [JSON object](#json-output) every command prints, with no payload.

By hand, the same steps are:

```bash
git fetch origin main
git ls-remote --heads origin   # no branch named fix-help, a path prefix of it, or under it
git switch -c fix-help --no-track origin/main
```

### What branch refuses

When `ils branch` refuses, it has created no branch and switched nothing; at most it has fetched
from origin. It refuses:

- no name, a second word, or `--`;
- a name git cannot hold as a branch, including one that starts with `-` and `@{-1}`;
- a name a branch already takes, locally or on origin: one of the same name, or one that collides
  with it by path (`fix` takes `fix/help`, and `fix/help` takes `fix`), which git cannot hold
  beside it. A branch whose name only ends with the name (`team/fix` for `fix`) does not take it.
  Origin is asked at the time of the run, so a branch pushed since the last fetch counts and one
  deleted on origin does not;
- a detached HEAD with commits origin's default branch lacks: it lists them, and `Next:` says to
  keep them on a branch with `git switch -c <name>`, then run `ils push`;
- a switch git refuses because a changed or untracked file would be overwritten: it names the
  files;
- an `origin` that cannot be reached, a missing `origin`, and a `git` older than 2.34.1.

## Status

Run `ils status` to learn where the checkout stands before you act. It always exits 0 once its
words parse: what it could not read is a line of its output, not a refusal. It fetches what it
reads from origin (the default branch, the branch's copy, a merged pull request's head, a version
tag) and writes nothing of yours. When origin cannot be reached, it counts against the last fetch
and says so.

What it reports depends on where HEAD stands:

- **On a work branch:** how the branch stands against its copy on origin (up to date, ahead,
  behind, diverged, or not on origin). Its open pull request, with its title, body, check verdict,
  and merge state. Every closed or merged pull request the branch had.
- **On the default branch:** how it stands against origin's. The last version tag (the highest
  `v*` version tag origin holds on its default branch) and the commits since it. Whether the tip
  is a release commit, and whether rules protect the branch.
- **On a detached HEAD:** the commits origin lacks.

`Next:` names the first of these the state calls for, in the order `ils push` would refuse or end:

1. start a branch with `ils branch <name>` once the work landed;
2. bring in the branch's copy on origin when the branch is behind or diverged;
3. `ils push` when there is work to push or checks to wait for;
4. the fix for a red check or a conflict;
5. a review to request;
6. the pull request's title and body, then `ils merge`, when it is green and mergeable.

With `gh` missing or logged out, it says so, names the fix, and still reports what `git` alone can
tell.

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

## Release

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
4. waits for the checks of the release commit, as `ils merge` does, until you stop it or 80
   seconds have passed since the command started;
5. tags the release commit `v<X.Y.Z>` with an annotated tag and pushes the tag to origin;
6. creates the GitHub release of the tag, titled with the tag and with the notes as its body, and
   marked as a pre-release when the version has a pre-release part.

`--force` skips the wait for the checks: the output says the CI gate was skipped and nothing
verified the commit. Every other refusal still holds. `--tag-only` ends once the tag is on origin,
and creates no GitHub release.

When the checks are still running, or fewer than 20 seconds are left for the writes, `ils release`
ends pending with nothing written, and `Next:` is the same command, flags included. A repository
with no CI on its default branch goes on, and a line says nothing verified the commit.

### What release refuses

When `ils release` refuses, it has tagged nothing, pushed nothing, and created no release; at most
it has fetched from origin. It refuses everything `ils push` refuses about the tools, `origin`, and
the repository, and also:

- a version that is not `X.Y.Z` with an optional pre-release part: `v1.2.0`, `1.2`, `1.2.0+build`,
  and `latest` are refused;
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

### Run it again

`ils release` writes in a fixed order: the local tag, the tag on origin, then the GitHub release.
Each step first looks at what is already there, so a run that broke resumes where it stopped:

- a local tag `v<X.Y.Z>` already at the release commit is kept, not made again;
- a tag `v<X.Y.Z>` already on origin at the release commit means the release was tagged before: the
  run reads no check and pushes nothing, and goes on to the GitHub release;
- a GitHub release that already exists for the tag is left as it is, and its URL is reported.

When origin refuses the tag push, `ils release` ends broke: the tag was made locally, and the
output gives what `git` and the hooks printed. When the GitHub release cannot be created, it ends
broke with the tag already on origin. Either way, `Next:` says to run the same command again, after
the fix when there is one.

### Release by hand

The same steps, by hand, for `1.2.0` on `main`:

```bash
git fetch origin main
git log --first-parent --format='%H %s' origin/main   # the newest chore: release v1.2.0
gh api repos/<owner>/<name>/commits/<sha>/check-runs   # its checks, green before you go on
git tag --annotate --message v1.2.0 v1.2.0 <sha>
git push origin refs/tags/v1.2.0
gh release create v1.2.0 --repo <host>/<owner>/<name> --verify-tag --title v1.2.0 \
  --notes-file <notes>   # add --prerelease for a version such as 1.2.0-rc.1
```

`<notes>` is a file holding the `## [1.2.0]` section of `CHANGELOG.md` at the release commit.

### Release JSON output

`ils release --json` prints the [JSON object](#json-output) every command prints, with the detail
under `payload.release`. Only a result from the writes on carries it: a refusal or a pending result
before the tag is written has no payload.

| Field        | Type    | Meaning                                                                                                                                                                        |
| ------------ | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `version`    | string  | The version, without the `v`.                                                                                                                                                  |
| `tag`        | string  | The version tag, `v<X.Y.Z>`.                                                                                                                                                   |
| `sha`        | string  | The release commit.                                                                                                                                                            |
| `verdict`    | string  | `green` when its checks passed, `none` when none reported on a default branch without CI, `forced` when `--force` skipped them, or `skipped` when origin already held the tag. |
| `url`        | string  | The GitHub release's URL once it was created or found; empty otherwise.                                                                                                        |
| `prerelease` | boolean | Whether the version has a pre-release part.                                                                                                                                    |
| `tag_only`   | boolean | Whether `--tag-only` was given.                                                                                                                                                |

## Stopping a run

Ctrl-C or SIGTERM during `ils push`, `ils merge`, or `ils release` stops the step in progress. `ils`
then prints the result of that step, as text or as JSON per `--json`, and exits with that result's
code. The output says that `ils` was interrupted, and `Next:` says what to run. As at a normal end,
the spinner line is erased before the result prints; see [Terminal output](#terminal-output).

A second Ctrl-C or SIGTERM ends `ils` at once, as that signal ends any program: no result is
printed, and the exit status is the signal's (130 for Ctrl-C and 143 for SIGTERM in most shells),
not one of the [exit codes](#exit-codes).

## Exit codes

| Exit | Means                | Reaction                                                                                                                                                                                                                                                                                                                                               |
| ---- | -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 0    | done                 | Push: the checks are green, or the repository has no CI and a line says nothing verified the commit; follow `Next:`. Merge: the pull request is merged and the branch deleted. Branch: the branch is started. Status: always, whatever it finds. Release: the tag is on origin, and the GitHub release exists unless `--tag-only`.                     |
| 1    | broke                | The output says what was done and what was not. Fix what failed, then follow `Next:`.                                                                                                                                                                                                                                                                  |
| 2    | refused              | Nothing was pushed, opened, merged, or tagged, except by a push whose pull request's head moved during the wait. The reason says why; `Next:` says what to do.                                                                                                                                                                                         |
| 3    | ask                  | Reserved; not used by this version.                                                                                                                                                                                                                                                                                                                    |
| 4    | pending              | GitHub had not finished when the command stopped waiting, for example checks still running or not yet reported. Run `ils push` again, which waits up to 15 minutes at a terminal, or `ils push --wait` to wait that long anywhere; a merge that was already done elsewhere names `ils merge` instead. After `ils release`, run the same command again. |
| 5    | delivered, not green | Push only. The push happened; a check is red or missing, or the branch conflicts with or is behind the default branch. Follow `Next:`.                                                                                                                                                                                                                 |

Every outcome but done prints its reason on the line above `Next:`, labeled by outcome:
`Refused:`, `Broke:`, `Pending:`, or `Delivered, not green:`. A run stopped by Ctrl-C or SIGTERM
exits with the code of the result it prints; see [Stopping a run](#stopping-a-run).

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

Each command's payload is described with the command: [Push JSON output](#push-json-output),
[Merge JSON output](#merge-json-output), [Status JSON output](#status-json-output), and
[Release JSON output](#release-json-output).

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
- **Longer wait:** when stdout is a terminal, `ils push` waits up to 15 minutes for the checks
  instead of 100 seconds, unless you give `--wait=false`. A pending `ils merge` sends you to
  `ils push`, which waits that long there too.

A stream that is not a terminal gets none of this. Stdout prints the whole result at the end. Stderr
stays empty unless `-d` is on, or `ils` cannot print its result and reports that failure there.
`--json` output is never colored or streamed, but `ils push --json` at a terminal still waits up to
15 minutes. `TERM=dumb` counts as no terminal.

`NO_COLOR` and `--no-color` turn color and bold off on both streams, help included. `CLICOLOR_FORCE`
turns color on for a stream that is not a terminal; `NO_COLOR` and `--no-color` override it. None
of the three changes how long `ils push` waits.

`-d` (or `--debug`) prints one line on stderr for each `git` and `gh` call and each wait decision,
timed from when the command started, in place of the spinner. It changes neither stdout nor the
exit code.

## Use with Claude Code

The repository ships two agent skills: `ils`, which runs the binary, and `write-release`, which
prepares the release commit. Install them with [`skills`](https://github.com/vercel-labs/skills):

```bash
npx skills add jeffzi/ils
```

With the skills installed, `/ils status`, `/ils branch <name>`, `/ils push`, `/ils merge`, and
`/ils release <X.Y.Z>` run the binary and relay its output, and the `ils` skill tells an agent how to
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

## Contributing

The tasks need [Task](https://taskfile.dev), [prek](https://github.com/j178/prek), and
[dprint](https://dprint.dev/install/). `task --list` shows them all. `task check` runs every
pre-commit hook (format, lint, spelling) and `task test` runs the tests; run both before pushing.

## License

[MIT](LICENSE)
