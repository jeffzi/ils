# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Add `ils branch <name>`, which starts a work branch from the default branch as origin has it and
  refuses a name a branch already takes, locally or on origin.
- Add `ils status`, which says where the checkout stands and what to run next, and exits 0
  whatever it finds.

### Changed

- Point every `Next:` line that spelled out cutting a branch by hand at `ils branch <name>`.
- Shorten the lines of `ils push` and `ils merge`, JSON `lines` included, by leaving out zero check
  tallies and a clean merge state.
- Suggest the closest command when a command name is mistyped, and point `Next:` at its `--help`.
- Show how long each check still running at the time limit has run on GitHub.
- End `ils merge` pending instead of refused while a check the default branch requires still runs.

### Fixed

- Point `Next:` of a green `ils push` to a draft or blocked pull request at the step `ils merge`
  would ask for, instead of a merge it would refuse.
- Fix `ils push` saying nothing was pushed when origin stopped answering or a signal killed `git`
  mid-push, although the push may have reached origin.
- Name a required check still running, with the new `merge_state` `blocked-by-running-check`,
  instead of blaming a branch rule `ils` cannot name.

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

[Unreleased]: https://github.com/jeffzi/ils/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/jeffzi/ils/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/jeffzi/ils/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/jeffzi/ils/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/jeffzi/ils/releases/tag/v0.1.0
