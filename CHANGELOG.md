# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Add `ils guard` and `ils parse`, which judge a shell command an agent is about to run against the
  `git`, `gh`, and `ils` rules and show how the guard reads it.
- Add `ils hook claude` and the `ils` skill's `hook.sh`, which run the guard as Claude Code's
  `PreToolUse` hook and block every command when `ils` cannot answer.

### Changed

- Make the `ils` agent skill ask once where new work lands when the checkout is on a work branch,
  instead of starting a new branch on its own.
- Report a `gh` reachable only through a relative `PATH` entry as missing on every operating system.

### Fixed

- Fix `ils merge` deleting a branch whose pull request merged into a base other than the default
  branch, and `ils push` and `ils status` reading that pull request as landed.
- Fix an `ils` run failing as if origin could not be reached while another run fetched the same
  branch from another worktree.
- Fix `ils status` naming `ils push` or the merge step next when those commands would refuse or
  break on the same state.
- Refuse a mistyped command name after `ils help` or beside `--help`, and a malformed flag such as
  `---x`, instead of printing the root help or ending broke.
- Fix `--json` output splitting in two for a reader that breaks lines at U+0085 (NEL).
- Fix the `ils` agent skill leaving later commits out of the squash commit's message by rewriting
  the pull request's title and body after every green push.

## [0.5.0] - 2026-10-31

### Added

- Add `ils release <X.Y.Z>`, which tags and publishes a release once its release commit's checks
  are green.
- Add the `write-release` agent skill, which lands the release commit through `ils` and leaves
  `ils release` to the user.

### Changed

- Make the `ils` agent skill wait for slow CI with one background `ils push --wait` instead of
  repeated foreground `ils push` calls.
- Report a pre-release's release commit as a release commit in `ils status`.
- Make `ils push` and `ils merge` wait longer for the checks when stdout is a terminal, with
  `--wait` to choose the long or the short wait anywhere.

### Fixed

- Report Ctrl-C or SIGTERM during the wait for the checks as an interrupt, not as the time limit
  running out.

## [0.4.0] - 2026-10-30

### Added

- Add `ils branch <name>`, which starts a work branch from origin's default branch.
- Add `ils status`, which says where the checkout stands and what to run next.

### Changed

- Point every `Next:` line that spelled out cutting a branch by hand at `ils branch <name>`.
- Suggest the closest command when a command name is mistyped, and point `Next:` at its `--help`.
- Show how long each check still running at the time limit has run on GitHub.
- Carry what the hooks and `git` printed for a push `git` refused in the new JSON field `excerpt`,
  not in the last entry of `lines`.

### Fixed

- Point `Next:` of a green `ils push` to a draft or blocked pull request at the step `ils merge`
  would ask for, instead of a merge it would refuse.
- Fix `ils push` reporting a push that may have reached origin as stopped when origin stopped
  answering, a signal killed `git`, or a pre-push hook's last line lacked a newline, and dropping a
  refused push's reason from the hook's stdout.
- End `ils merge` pending while a required check still runs, with the new `merge_state`
  `blocked-by-running-check`, instead of refusing it for a branch rule `ils` cannot name.

## [0.3.0] - 2026-10-25

### Added

- Add a live progress display at a terminal, with `--no-color` to turn its color off.
- Add `-d`/`--debug`, which traces each `git` and `gh` call and each wait decision on stderr.
- Print the result and exit code of the step in progress when Ctrl-C or SIGTERM stops `ils push` or
  `ils merge`.

### Changed

- Drop `Next: none` from a merged result's text, and drop lines that only restated the reason from
  both the text and JSON `lines`.

### Fixed

- Fix help and refusals stalling for seconds when color is off.
- Fix `ils push` blaming the merge state instead of naming the checks still running when the time
  limit ends its wait.

### Security

- Print control characters in third-party text, such as hook output and file names, as visible
  text, so they can no longer restyle or rewrite the output, its `Next:` line included.

## [0.2.0] - 2026-10-22

### Added

- Add `ils --version`, which prints the version of `ils`.
- Add `ils merge`, which squash-merges the branch's pull request once its checks are green.

### Changed

- Name `ils merge` in the next step of a green `ils push`, in place of a `gh pr merge` command to
  run by hand.
- **Breaking:** Require `gh` 2.73.0 or later, since every older release fails the `gh pr edit` step
  that a green `ils push` names.

### Fixed

- Fix `ils push` reporting a green verdict from checks that belong to another commit.
- Fix `ils push` failing when a local tag has the same name as the branch.
- Fix `ils push` ending red with no log excerpt when GitHub had not yet published the failed
  check's log.

## [0.1.1] - 2026-10-20

### Fixed

- Fix `ils push` when `git` reaches origin through `ssh.github.com`, an ssh config alias, or a
  custom ssh command, and name the setting to fix when that command is not installed.
- Fix `ils push` refusing a published branch because another local branch was cut from it, and
  advising a `git cherry-pick` that dropped commits the default branch lacks.

## [0.1.0] - 2026-10-18

### Added

- Add `ils push`, which pushes the current branch, opens its pull request on the first push, and
  reports whether its checks passed; it requires `git` 2.34.1 or later and `gh` 2.50.0 or
  later.
- Add the `ils` agent skill, which tells an agent when to run `ils push` and how to act on its
  output.
- Add usage help, printed by `--help` on `ils` and on each command and by `ils help [command]`.

[Unreleased]: https://github.com/jeffzi/ils/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/jeffzi/ils/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/jeffzi/ils/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/jeffzi/ils/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/jeffzi/ils/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/jeffzi/ils/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/jeffzi/ils/releases/tag/v0.1.0
