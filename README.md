# ils

`ils` (Instrument Landing System) lands a change on GitHub in fixed, checked steps, so that a large
language model (LLM) agent cannot skip one, merge on red, or push to the wrong place. Each command
runs only standard `git` and `gh` commands, prints what it did and what to do next, and keeps no
state of its own.

This version has one command, `ils push`. `merge`, `branch`, `status`, and `release` are coming;
until `merge` exists, merge by hand with the line shown under
[After a green push](#after-a-green-push).

- [Example](#example)
- [Install](#install)
- [Start a repository](#start-a-repository)
- [Usage](#usage)
  - [What it refuses](#what-it-refuses)
  - [Exit codes](#exit-codes)
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

From a tag:

```bash
go install github.com/jeffzi/ils@latest
```

From a clone, `task install` builds the binary and copies it to the directory `go install` writes
to:

```bash
task install
```

### Requirements

- Go 1.27 or later, to build.
- [Task](https://taskfile.dev), to install from a clone.
- `git` on `PATH`.
- [`gh`](https://cli.github.com), installed and logged in to the host of `origin`
  (`gh auth login --hostname <host>`). `ils` owns no token and no login flow.
- A remote named `origin` on GitHub that you can push to and that is not a fork.

`ils` runs on macOS, Linux, and Windows.

## Start a repository

`ils` needs an `origin` before it can do anything. Set one up once, by hand:

1. Create the repository on GitHub and push to it:

   ```bash
   gh repo create --source . --push
   ```

2. Fetch once. With git 2.48 or later this also records, in the clone, which branch is origin's
   default:

   ```bash
   git fetch origin
   ```

3. Protect the default branch with a ruleset. In the repository on GitHub, open Settings, then
   Rules, then Rulesets, and create a branch ruleset that targets the default branch with these
   rules: require a pull request before merging, with squash as the only merge method; require
   status checks to pass; block force pushes. GitHub's guide to
   [creating rulesets for a repository](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository)
   has the details. Rulesets need a public repository or a paid GitHub plan; on a free private
   repository skip this step. `ils` works either way, and the ruleset is what holds when something
   other than `ils` merges.

## Usage

```bash
ils push          # push the branch, open its pull request on the first push, wait for the checks
ils push --json   # the same result as one JSON object
ils push --help   # usage, with every git and gh command the binary runs
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
- a repository with no remote named `origin`, an `origin` that cannot be reached, or one whose
  fetch and push URLs do not name one repository (they differ, or one is not an `https://`,
  `ssh://git@`, or `git@host:` repository URL);
- a missing `gh`, a `gh` that is not logged in to origin's host (which is also how a host that
  is not GitHub is refused), a fork, or a repository you cannot push to;
- a precondition read that the time limit cut short: run `ils push` again.

### Exit codes

| Exit | Means                | Reaction                                                                                                                             |
| ---- | -------------------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| 0    | done                 | The checks are green. Write the pull request's title and body.                                                                       |
| 1    | broke                | The output says what was done and what was not. Fix what failed, then follow `Next:`.                                                |
| 2    | refused              | Nothing was pushed or opened. The reason says why; `Next:` says what to do.                                                          |
| 3    | ask                  | Reserved; not used by this version.                                                                                                  |
| 4    | pending              | GitHub had not finished when `ils push` stopped waiting, for example checks still running or not yet reported. Run `ils push` again. |
| 5    | delivered, not green | The push happened; a check is red or missing, or the branch conflicts with or is behind the default branch. Follow `Next:`.          |

A verdict with no checks at all is not green: a change that nothing verified does not count as
verified.

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

`ils push` prints both lines with the repository, number, and commit filled in.

## Use with Claude Code

The repository is a Claude Code plugin with one skill, `ils`. Load it from a clone:

```bash
claude --plugin-dir /path/to/ils
```

With the plugin loaded, `/ils push` runs the binary and relays its output, and the skill tells an
agent how to react to each exit code. The merge stays the user's decision.

## Contributing

The tasks need [Task](https://taskfile.dev), [prek](https://github.com/j178/prek), and
[dprint](https://dprint.dev/install/). `task --list` shows them all. `task check` runs every
pre-commit hook (format, lint, spelling) and `task test` runs the tests; run both before pushing.

## License

[MIT](LICENSE)
