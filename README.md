# ils

`ils` (Instrument Landing System) lands a change on GitHub in fixed, checked steps. A large
language model (LLM) agent that runs it cannot skip a step or push to the wrong place. Each
command runs only standard `git` and `gh` commands, prints what it did and what to do next, and
keeps no state of its own.

This version has one command, `ils push`. `merge`, `branch`, `status`, and `release` are coming,
and `merge` will refuse a pull request whose checks are not green. Until then, merge by hand with
the line shown under [After a green push](#after-a-green-push).

- [Example](#example)
- [Install](#install)
  - [Requirements](#requirements)
  - [Build the binary](#build-the-binary)
- [Start a repository](#start-a-repository)
- [Usage](#usage)
  - [What it refuses](#what-it-refuses)
  - [Exit codes](#exit-codes)
    - [When no check reports](#when-no-check-reports)
  - [JSON output](#json-output)
  - [After a green push](#after-a-green-push)
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

The `Next:` line is cut short here; it goes on to print the two commands under [After a green
push](#after-a-green-push). Every result ends with one `Next:` line (a `next` field with
`--json`), and the exit code says which reaction fits, so a caller that reads no prose still
reacts correctly.

## Install

### Requirements

To build:

- Go 1.27 or later.
- [Task](https://taskfile.dev), to install from a clone.

To run:

- [`git`](https://git-scm.com/downloads) 2.34.1 or later, on `PATH`.
- [`gh`](https://cli.github.com) 2.50.0 or later, installed and logged in to the host of `origin`
  (`gh auth login --hostname <host>`). `ils` owns no token and no login flow.
- A remote named `origin` on GitHub that you can push to and that is not a fork.

`ils` runs on macOS, Linux, and Windows.

### Build the binary

With Go:

```bash
GOPRIVATE=github.com/jeffzi/ils go install github.com/jeffzi/ils@latest
```

The repository is private, so the Go module proxy and checksum database cannot serve it.
`GOPRIVATE` makes `go` fetch it with `git` directly, which needs read access to the repository.

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
   repository skip this step. `ils` works either way. Since you merge by hand, the ruleset is what
   blocks a merge on red.

## Usage

```bash
ils push          # push the branch, open its pull request on the first push, wait for the checks
ils push --json   # the same result as one JSON object
ils push --help   # usage of push
ils help          # usage of ils and its commands
ils help push     # the same as ils push --help
```

Run `ils push` on a work branch with everything committed. It:

1. checks every precondition before it pushes or opens anything, and refuses with a reason when
   one does not hold;
2. pushes the branch by name, never with a force flag;
3. opens a pull request against origin's default branch when the branch has none, titled with the
   branch name and with an empty body;
4. waits for the checks of the pushed commit, until 100 seconds have passed since the command
   started, and prints the verdict.

Run it again at any time: on a commit that is already pushed it pushes nothing and reports the
verdict again.

### What it refuses

When `ils push` refuses, it has pushed nothing, opened nothing on GitHub, and changed no branch
or file; at most it has fetched from origin. It refuses:

- the default branch, and a detached HEAD;
- a tracked file with uncommitted changes (untracked files are fine);
- a branch with no commit past origin's default branch;
- a branch cut from another local branch instead of the default branch;
- a branch that is behind, or has diverged from, its copy on GitHub;
- a branch whose pull request already merged, or is open against another base;
- a repository with no remote named `origin`, or an `origin` that cannot be reached;
- an `origin` whose fetch and push URLs do not name one repository (they differ, or one is not an
  `https://`, `ssh://git@`, or `git@host:` repository URL);
- a missing `git` or `gh`;
- a `git` older than 2.34.1, or one whose version `ils` cannot read;
- a `gh` older than 2.50.0, or one whose version `ils` cannot read;
- a `gh` not logged in to origin's host (this is also how `ils` refuses a host that is not GitHub);
- a fork, or a repository you cannot push to;
- any word after `push` other than `--json`, `--help`, or `-h`, and a `--json=<value>` whose value
  is not `true` or `false`;
- a precondition read that the time limit cut short: run `ils push` again.

### Exit codes

| Exit | Means                | Reaction                                                                                                                                                         |
| ---- | -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 0    | done                 | The checks are green, or the repository has no continuous integration (CI) and a line says nothing verified the commit. Write the pull request's title and body. |
| 1    | broke                | The output says what was done and what was not. Fix what failed, then follow `Next:`.                                                                            |
| 2    | refused              | Nothing was pushed or opened. The reason says why; `Next:` says what to do.                                                                                      |
| 3    | ask                  | Reserved; not used by this version.                                                                                                                              |
| 4    | pending              | GitHub had not finished when `ils push` stopped waiting, for example checks still running or not yet reported. Run `ils push` again.                             |
| 5    | delivered, not green | The push happened; a check is red or missing, or the branch conflicts with or is behind the default branch. Follow `Next:`.                                      |

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
| `lines`     | array of strings | What happened, in order. Absent when empty.                                                                                          |
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

### After a green push

The pull request's title and body become the squash commit's message, so write them first:

```bash
gh pr edit --repo <host>/<owner>/<name> <N> --title "<title>" --body "<body>"
```

Then merge by hand. Pass the message explicitly: without `--subject` and `--body`, GitHub composes
it from the repository setting, and a pull request with one commit keeps that commit's message.

```bash
gh pr merge --repo <host>/<owner>/<name> <N> --squash --delete-branch \
  --match-head-commit <sha> --subject "<title> (#<N>)" --body "<body>"
```

`ils push` prints both commands on its `Next:` line with the repository, number, and commit filled
in. The title and body stay placeholders for you to write.

## Use with Claude Code

The repository ships one agent skill, `ils`. Install it with
[`skills`](https://github.com/vercel-labs/skills):

```bash
npx skills add jeffzi/ils
```

With the skill installed, `/ils push` runs the binary and relays its output, and the skill tells an
agent how to react to each exit code. The merge stays the user's decision.

## Contributing

The tasks need [Task](https://taskfile.dev), [prek](https://github.com/j178/prek), and
[dprint](https://dprint.dev/install/). `task --list` shows them all. `task check` runs every
pre-commit hook (format, lint, spelling) and `task test` runs the tests; run both before pushing.

## License

[MIT](LICENSE)
