# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-10-22

### Added

- Add `ils --version`, which prints the version of `ils`.
- Add `ils merge`, which squash-merges the branch's pull request once its checks are green, deletes
  the branch, and leaves the default branch checked out.

### Changed

- Name `ils merge` in the next step of a green `ils push`, in place of a `gh pr merge` command to
  run by hand.
- **Breaking:** Require `gh` 2.73.0 or later, since every older release fails the `gh pr edit` step
  that a green `ils push` names. Upgrade `gh` from <https://cli.github.com>.

### Fixed

- Fix `ils push` reporting a green verdict from checks that belong to another commit, either one
  pushed while it waited or a fast check that passed before the push's workflow runs started.
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

[Unreleased]: https://github.com/jeffzi/ils/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/jeffzi/ils/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/jeffzi/ils/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/jeffzi/ils/releases/tag/v0.1.0
