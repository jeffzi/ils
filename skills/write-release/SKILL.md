---
name: write-release
description: >
  Use when preparing a release: bumping a version, refreshing lock files, moving the changelog's
  Unreleased entries into a version section, and landing the release commit through ils.
  Use when unsure which version to bump or what files need updates.
  Not for tagging or publishing the release: that is `ils release`, the user's to run.
  Not for changelog-only edits — use write-changelog.
  Not for commit messages — use write-commit.
argument-hint: "[version | patch | minor | major]"
user-invocable: false
allowed-tools: Read, Edit, Grep, Glob, Bash(grep *), Bash(git *), Bash(ils *), Bash(gh pr edit *), Bash(npm *), Bash(pnpm *), Bash(yarn *), Bash(uv *), Bash(poetry *), Bash(cargo *), Bash(bundle *), Bash(go *)
---

# Release

Bump versions everywhere, refresh lock files, update the changelog, and land it all as one release
commit, `chore: release vX.Y.Z`, on the trunk through `ils`. The skill ends there: tagging the
commit and publishing the GitHub release is `ils release X.Y.Z`, which the user runs.

## Version Resolution

`$ARGUMENTS` determines the target version:

| Argument                | Action                                                |
| ----------------------- | ----------------------------------------------------- |
| `1.2.3` or `v1.2.3`     | Use as-is (strip `v` for files, keep it for the tag)  |
| `patch` `minor` `major` | Bump that segment from the last tag                   |
| _(none)_                | Analyze commits since last tag, suggest bump, confirm |

The last tag is the one origin holds, never a local tag: on the trunk, `ils status` prints it as
the last version tag, with the number of commits since it.

### Suggesting a bump

When no version is given, read the commits on origin's trunk since the last tag (`git fetch origin
<trunk>`, then `git log --format=%s <last tag>..origin/<trunk>`) using Conventional Commits:

| Signal                                          | Bump                                |
| ----------------------------------------------- | ----------------------------------- |
| Any `BREAKING CHANGE:` footer or `!` after type | **major** (or **minor** if < 1.0.0) |
| Any `feat` type                                 | **minor** (or **patch** if < 1.0.0) |
| Only `fix`, `perf`, `refactor`, `chore`, etc.   | **patch**                           |

Present the suggestion with the commit evidence. Confirm before proceeding.

## Release Flow

### 0. Start from the trunk

The release commit must contain only release edits. Before anything else, check for uncommitted
work:

```bash
git status --short
```

If the tree is dirty, show the output and ask the user what to do — commit first, or abort. Do not
proceed with a dirty tree.

Then run `ils status` on the trunk. It reads the last tag and the commits since it from origin.

Then start the release branch, which takes the trunk as origin has it:

```bash
ils branch release-vX.Y.Z
```

### 1. Discover current version

Read the primary version file. Then search for the version string across the project to find every
file that needs bumping:

```bash
grep -rn "CURRENT_VERSION" --include='*.json' --include='*.toml' --include='*.cfg' \
  --include='*.py' --include='*.rb' --include='*.gemspec' --include='*.rs' .
```

Present all findings. Confirm the bump list with the user before editing.

### 2. Resolve target version

Apply version resolution rules. If no argument was given, suggest a bump level with commit evidence.
Confirm the target version with the user.

### 3. Bump version in all files

Edit every version-bearing file found in step 1. Do **not** hand-edit lock files — step 4 handles
them.

### 4. Refresh lock files

Run the ecosystem's lock command. **This is not optional.** A version bump without a lock file
refresh ships a broken install.

| Ecosystem       | Lock file           | Refresh command                   |
| --------------- | ------------------- | --------------------------------- |
| Node (npm)      | `package-lock.json` | `npm install --package-lock-only` |
| Node (pnpm)     | `pnpm-lock.yaml`    | `pnpm install --lockfile-only`    |
| Node (yarn)     | `yarn.lock`         | `yarn install`                    |
| Python (uv)     | `uv.lock`           | `uv lock`                         |
| Python (poetry) | `poetry.lock`       | `poetry lock`                     |
| Rust            | `Cargo.lock`        | `cargo check`                     |
| Ruby            | `Gemfile.lock`      | `bundle install`                  |
| Go              | `go.sum`            | `go mod tidy`                     |

If no lock file exists, skip. If the refresh command needs tool approval, surface it — never skip
silently.

### 5. Update changelog

If `CHANGELOG.md` exists:

1. Load `Skill(write-changelog)`.
2. Move `[Unreleased]` entries to a new `## [X.Y.Z] - YYYY-MM-DD` section.
3. Reset `[Unreleased]` to empty with its type headings cleared.
4. Update comparison links at the bottom.

`ils release` publishes that section as the GitHub release's notes, and refuses a release commit
whose `CHANGELOG.md` has no section, or an empty one, for the version. If no `CHANGELOG.md` exists,
ask the user whether to create one.

### 6. Verify

Run the project's test suite, linter, and type checker **after** the version bump. All must pass
before proceeding. A release commit on broken code is not a release.

### 7. Commit

Load `Skill(write-commit)` for message rules. The release commit bundles everything — version bumps,
lock file changes, and changelog — into one commit.

Subject: `chore: release vX.Y.Z` — no body needed, the changelog has the details. `ils release`
finds the release commit by this subject on the trunk; any other subject is not a release commit.

This is a workflow-prescribed commit: write-commit's interactive confirmation flow does not apply.
Confirm the staged file list with the user before committing.

### 8. Land the release commit

Push it and open its pull request:

```bash
ils push
```

Act on its exit code and `Next:` as the `ils` skill says. On exit 0, set the pull request's title
to `chore: release vX.Y.Z` and its body to empty with the `gh pr edit` command `Next:` prints: the
squash commit takes the pull request's title, so that title is the release commit's subject.

The merge is the user's word. Tell them to run `ils merge`, or run it when they ask for it.

### 9. Hand over the release

Once the release commit is on the trunk, stop and tell the user to run:

```bash
ils release X.Y.Z
```

It waits for the release commit's checks, tags it `vX.Y.Z`, pushes the tag, and publishes the
GitHub release from the changelog section. Never run it, `git tag`, or `gh release create` yourself
from this skill.

## Version Discovery Reference

Common version-bearing files by ecosystem:

| Ecosystem        | Version files                                  |
| ---------------- | ---------------------------------------------- |
| Node             | `package.json`                                 |
| Python (PEP 621) | `pyproject.toml` (`[project]` → `version`)     |
| Python (legacy)  | `setup.cfg`, `setup.py`                        |
| Python (runtime) | `__init__.py` or `_version.py` (`__version__`) |
| Rust             | `Cargo.toml` (`[package]` → `version`)         |
| Ruby             | `*.gemspec`, `lib/**/version.rb`               |
| Go               | — (tags are the version)                       |
| Generic          | `VERSION`, `.version`                          |

**Do not assume one file is enough.** A Python project may have the version in `pyproject.toml` AND
`__init__.py`. A Node project may have it in `package.json` AND a `version.ts` constant. Search.

## Common Mistakes

| Mistake                                             | Reality                                                                 |
| --------------------------------------------------- | ----------------------------------------------------------------------- |
| "Lock file will update on next install"             | Next install is someone else's machine. Ship it correct.                |
| "Only package.json needs bumping"                   | `grep` for the version — it's in more files than you think.             |
| "Working tree is clean enough"                      | Uncommitted work gets swept into the release commit. Check first.       |
| "I'll write the changelog later"                    | `ils release` refuses a release commit without the version's section.   |
| "Tests passed before the bump"                      | Before is not after. Run them after the version bump.                   |
| "Split version bump and changelog into two commits" | One release, one commit. Splitting fragments the history.               |
| "The project has no lock file, skip refresh"        | Correct — but verify the lock file truly doesn't exist, don't guess.    |
| Using `release:` as the commit type                 | `release` is not a valid CC type. Use `chore: release vX.Y.Z`.          |
| Listing changes in the commit body                  | The changelog has the details. The commit body adds nothing.            |
| "`git describe` gives the last tag"                 | A local tag may never have reached origin. Read it from `ils status`.   |
| "The release commit merged, so I'll tag it"         | Tagging is `ils release`, the user's to run. Tell them and stop.        |
| "Commit the release on the trunk, it's one commit"  | Every change lands through a branch and its pull request: `ils branch`. |

## Red Flags — Stop and Reassess

- About to run `git tag`, push a tag, `gh release create`, or `ils release`
- About to read the last tag from local tags instead of `ils status`
- About to proceed with a dirty working tree
- About to commit without running tests after the version bump
- Lock file exists but wasn't refreshed
- Version string found in a file that wasn't bumped
- Creating multiple commits for one release
- Skipping changelog update when `CHANGELOG.md` exists
- About to run `ils merge` the user did not ask for
