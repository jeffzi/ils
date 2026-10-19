# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Fix `ils push` when an ssh config maps origin's host to another `HostName`, including a config
  named by `core.sshCommand`, `GIT_SSH_COMMAND` or `GIT_SSH`: it now reads the host through the
  ssh command git uses.

## [0.1.0] - 2026-10-18

### Added

- Add `ils push`, which pushes the current branch, opens its pull request on the first push, and
  reports whether its checks passed; it requires `git` 2.34.1 or later and `gh` 2.50.0 or
  later.
- Add the `ils` agent skill, which tells an agent when to run `ils push` and how to act on its
  output.
- Add usage help, printed by `--help` on `ils` and on each command and by `ils help [command]`.

[Unreleased]: https://github.com/jeffzi/ils/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/jeffzi/ils/releases/tag/v0.1.0
