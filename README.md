# ils

`ils` (Instrument Landing System) lands a change on GitHub in fixed, checked steps. A large
language model (LLM) agent that runs it cannot skip a step or push to the wrong place. Each
command runs only standard `git` and `gh` commands, prints what it did and what to do next, and
keeps no state of its own.

`ils` has five commands. `ils branch <name>` starts a work branch from the default branch
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
- [Branch](#branch)
- [Push](#push)
  - [After a green push](#after-a-green-push)
- [Merge](#merge)
  - [How merge ends](#how-merge-ends)
  - [A merge made elsewhere](#a-merge-made-elsewhere)
- [Status](#status)
- [Release](#release)
  - [Resume a broken release](#resume-a-broken-release)
  - [Release by hand](#release-by-hand)
- [Reference](#reference)
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
with one `Next:` line (a `next` field in the JavaScript Object Notation (JSON) that `--json`
prints). A merged result has nothing left to do: its text has no `Next:` line, and its JSON carries
`"next": "none"`. The exit code says which reaction fits, so a caller that reads no prose still
reacts correctly.

Each run ends with one of six outcomes, and each outcome has its own exit code: done, broke,
refused, ask, pending, or delivered, not green. [Exit codes](docs/reference.md#exit-codes) says
what each means.

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
ils merge --wait  # the same, waiting up to 14 minutes 40 seconds for the checks even off a terminal
ils branch fix-help  # start the branch fix-help from the default branch as origin has it
ils status        # where the checkout stands, and what to run next
ils release 1.2.0 # tag the release commit of v1.2.0 once its checks are green, then publish it
ils release 1.2.0 --tag-only  # the same, stopping once the tag is on origin
ils release 1.2.0 --wait=false  # the same, waiting at most 80 seconds even at a terminal
ils help          # usage of ils and its commands
ils help push     # the same as ils push --help
ils --version     # print the version of ils
```

`push`, `merge`, `branch`, `status`, and `release` each take `--json`, `-d` or `--debug`,
`--no-color`, and `--help` or `-h`. `push`, `merge`, and `release` also take `--wait`, and
`release` alone `--force` and `--tag-only`.

## Branch

Run `ils branch <name>` before you start new work. It:

1. fetches the default branch from origin;
2. creates `<name>`, exactly as typed, at origin's copy of the default branch, with no upstream;
3. switches to it, carrying your uncommitted changes and untracked files along.

The new branch starts at origin's default branch whatever you stand on. Another work branch's
commits never ride along into the new pull request, and a stale local default branch never holds
it back. When the local default branch has commits origin lacks, a line names them: they stay on
the local default branch. `ils branch` needs `git` alone: it runs without `gh`, and with an
`origin` that is not on GitHub.

`ils branch --json` prints the [JSON object](docs/reference.md#json-output) every command prints,
with no payload.

By hand, the same steps are:

```bash
git fetch origin main
git ls-remote --heads origin   # no branch named fix-help, a path prefix of it, or under it
git switch -c fix-help --no-track origin/main
```

[What branch refuses](docs/reference.md#what-branch-refuses) lists every refusal.

## Push

Run `ils push` on a work branch with everything committed. It:

1. checks every precondition before it pushes or opens anything, and refuses with a reason when
   one does not hold;
2. pushes the branch by name, never with a force flag;
3. opens a pull request against origin's default branch when the branch has none, titled with the
   branch name and with an empty body;
4. waits for the checks of the pushed commit until you stop it or its
   [time limit](docs/reference.md#time-limits) has passed since the command started, then prints
   the verdict.

Run it again at any time: on a commit that is already pushed it pushes nothing and reports the
verdict again.

When another commit reaches the pull request while `ils push` waits, the checks GitHub reports are
no longer those of the pushed commit. `ils push` then prints no verdict and ends
[refused](docs/reference.md#exit-codes), naming the pull request's new head. `Next:` says to pull
the new commit, then run `ils push` again, or `ils push --wait=false` if you gave `--wait=false`.
The push itself already happened.

[What push refuses](docs/reference.md#what-push-refuses) lists every refusal, and
[Push JSON output](docs/reference.md#push-json-output) describes `ils push --json`.

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

## Merge

Run `ils merge` on the branch whose pull request you want to land. It:

1. checks every precondition before it writes anything, and refuses with a reason when one does
   not hold;
2. waits for the checks of the pull request's head, as `ils push` does, until you stop it or it
   reaches its [time limit](docs/reference.md#time-limits), which keeps 20 seconds for the writes
   that follow;
3. squash-merges the pull request, with its title plus `(#<N>)` as the subject and its body as
   the commit body. The trailer block GitHub adds (`Signed-off-by` and `Co-authored-by` lines)
   follows the body;
4. deletes the branch on origin and in the clone, and leaves the default branch checked out at
   origin's tip.

`ils merge` never pushes and has no `--force`. It refuses a commit that is not on the pull request
yet: push it with `ils push` first. When the checks are still running, or fewer than 20
seconds are left for the merge, `ils merge` ends pending with nothing written, and `Next:` says to
run `ils push`, which waits for the checks and then names `ils merge` again.

[What merge refuses](docs/reference.md#what-merge-refuses) lists every refusal, and
[Merge JSON output](docs/reference.md#merge-json-output) describes `ils merge --json`.

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

When Ctrl-C or SIGTERM (the signal `kill` sends by default) stops `ils merge`, the output says that
`ils` was interrupted, and `Next:` says what to run.

### A merge made elsewhere

When the branch's pull request is already merged, on the web or by a run of `ils merge` that was
cut short, `ils merge` merges nothing and reads no check. It deletes the branch on origin if it is
still there, then ends as [How merge ends](#how-merge-ends) describes. This also covers a pull
request that got an "Update branch" or a committed suggestion on the web before its merge. When
the branch has commits the merge did not carry, `ils merge` refuses and lists them, and `Next:`
says to start a new branch and cherry-pick them.

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

[Status JSON output](docs/reference.md#status-json-output) describes `ils status --json`.

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
4. waits for the checks of the release commit, as `ils merge` does, until you stop it or it
   reaches its [time limit](docs/reference.md#time-limits). It counts the runs a push started on
   the release commit. It leaves out the runs that judge something else: a run another workflow started
   (`workflow_run`), a scheduled or manual run, and a Dependabot run. GitHub files a `workflow_run`
   run under the default branch's tip whatever started it, pull request CI included;
5. tags the release commit `v<X.Y.Z>` with an annotated tag and pushes the tag to origin;
6. creates the GitHub release of the tag, titled with the tag and with the notes as its body, and
   marked as a pre-release when the version has a pre-release part.

`--force` skips the wait for the checks: the output says the CI gate was skipped and nothing
verified the commit. Every other refusal still holds. `--tag-only` ends once the tag is on origin,
and creates no GitHub release.

When the checks are still running, or fewer than 20 seconds are left for the writes, `ils release`
ends pending with nothing written, and `Next:` is the same command, flags included. A repository
with no CI on its default branch goes on, and a line says nothing verified the commit.

[What release refuses](docs/reference.md#what-release-refuses) lists every refusal, and
[Release JSON output](docs/reference.md#release-json-output) describes `ils release --json`.

### Resume a broken release

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

## Reference

[docs/reference.md](docs/reference.md) holds the details a caller looks up:

- [Exit codes](docs/reference.md#exit-codes), and what to do after each;
- [Time limits](docs/reference.md#time-limits), how long each command waits for the checks;
- [Stopping a run](docs/reference.md#stopping-a-run) with Ctrl-C or SIGTERM;
- [Refusals](docs/reference.md#refusals), every precondition each command checks;
- [JSON output](docs/reference.md#json-output), the object `--json` prints and each command's
  payload;
- [Terminal output](docs/reference.md#terminal-output): the spinner, step lines, color, and `-d`.

## Use with Claude Code

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

## Contributing

The tasks need [Task](https://taskfile.dev), [prek](https://github.com/j178/prek), and
[dprint](https://dprint.dev/install/). `task --list` shows them all. `task check` runs every
pre-commit hook (format, lint, spelling) and `task test` runs the tests; run both before pushing.

## License

[MIT](LICENSE)
