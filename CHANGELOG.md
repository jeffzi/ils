# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Add `ils push`, which pushes the current branch, opens its pull request on the first push, waits
  for the checks of the pushed commit, and prints the verdict with the next step.
- Add the `ils` agent skill, which tells an agent when to run `ils push` and how to act on its
  output.
- Add styled help: `ils --help` and `ils push --help` print short usage text, with color when
  stdout is a terminal.
- Add `ils help [command]`, which prints the same help as `--help`.

[Unreleased]: https://github.com/jeffzi/ils/commits/main
