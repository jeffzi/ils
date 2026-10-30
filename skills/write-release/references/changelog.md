# Writing CHANGELOG.md

Step 5 of the release reads this file when the `## [Unreleased]` section of `CHANGELOG.md` has no
entries. The template below is for a user who asks to start a changelog.

## A new CHANGELOG.md

```markdown
# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [X.Y.Z] - YYYY-MM-DD

### Added

- Add …

[Unreleased]: https://github.com/<owner>/<repo>/compare/vX.Y.Z...HEAD

[X.Y.Z]: https://github.com/<owner>/<repo>/compare/<last tag>...vX.Y.Z
```

With no earlier tag, the `[X.Y.Z]` link is `https://github.com/<owner>/<repo>/releases/tag/vX.Y.Z`.

## Writing entries from commits

Read the commits since the last tag with `git log --format=%s <last tag>..origin/<trunk>`.

- Group them under `### Added`, `### Changed`, `### Deprecated`, `### Removed`, `### Fixed`, and
  `### Security`, in that order, keeping only the headings that have entries.
- One sentence per entry, in the imperative, saying what changed for the user: "Add `--json`
  output to `list`", not "Added …" or "`list` now has …".
- Start a breaking change with `**Breaking:**` and add the step users take to migrate.
- Leave out what users neither see nor act on: `ci`, `test`, `docs`, `refactor`, `style`, and
  `build` commits; dependency bumps, unless one fixes a security advisory (`### Security`) or
  raises what users must install; and cosmetic output (colors, dimmed text, spacing), whatever the
  commit's type. A commit that is not notable stays out even though users would see it.
