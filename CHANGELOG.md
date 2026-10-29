# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Add `ils branch <name>`, which starts a work branch from the default branch as origin has it and
  refuses a name a branch already takes, locally or on origin.
- Add `ils status`, which says where the checkout stands and what to run next, and always exits 0.

### Changed

- Point every `Next:` line that spelled out cutting a branch by hand at `ils branch <name>`.
- Shorten the text output of `ils push`, `ils merge` and `ils status`: commits print as their first
  7 characters (JSON keeps them in full), checks still running are listed under one header, a zero
  tally is left out, the merge state is said in plain words and only when it explains the result,
  and every message about the time limit names its length.
- Drop the timing of a step that took under 100ms instead of printing `0s` or `100ms`.
- Answer a mistyped command such as `ils pish` with `did you mean push?` and point `Next:` at that
  command's `--help`.

### Fixed

- Stop blank rows from appearing after each step line when the terminal is narrowed or reflowed:
  the progress line was wiped by writing spaces across the row, and those spaces wrapped onto rows of
  their own. It is now erased instead.

## [0.3.0] - 2026-10-25

### Added

- Add a live progress display at a terminal, with `--no-color` to turn its color off (also honoring
  `NO_COLOR`, `CLICOLOR_FORCE`, and `TERM=dumb`).
- Add `-d`/`--debug`, which traces each `git` and `gh` call and each wait decision on stderr.
- Print the result and exit code of the step in progress when Ctrl-C or SIGTERM stops `ils push` or
  `ils merge`.

### Changed

- Print the reason of every outcome but done on the line above `Next:`, so a refusal's reason now
  follows its lines instead of leading them.
- Drop the `Next: none` line from the text of a merged result; its JSON still carries
  `"next": "none"`.
- Drop result lines that only restated the reason, from the text and from JSON `lines`.

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
- Add `ils merge`, which squash-merges the branch's pull request once its checks are green, deletes
  the branch, and leaves the default branch checked out.

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

- Fix `ils push` when origin's host is `ssh.github.com` or an ssh config alias for another
  `HostName`, including when git connects through `core.sshCommand`, `GIT_SSH_COMMAND` or
  `GIT_SSH`, and name the setting to fix when that ssh command is not installed.
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

[Unreleased]: https://github.com/jeffzi/ils/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/jeffzi/ils/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/jeffzi/ils/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/jeffzi/ils/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/jeffzi/ils/releases/tag/v0.1.0
