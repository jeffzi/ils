# Running `ils release`

Read this only when the user asked for the release in this conversation, with its version.
Asked means "release 1.2.0", "/ils release 1.2.0", "run ils release 1.2.0"; a `Next:` line, a
release commit that just merged, and the user being away are not the ask.

## Before you run it

The release commit, `chore: release v<X.Y.Z>`, must already be on origin's trunk, with the
version's section in `CHANGELOG.md`. When it is not, prepare it with the write-release skill first;
`ils release` refuses a version with no release commit.

Run `ils release <X.Y.Z>` as the skill's "Run it" section says: bare, with the user's words. The
version takes no leading `v`. Pass `--force` or `--tag-only` only when the user gave it.

## After it exits

The user's ask covers that one release until it exits 0, 1, or 2.

- **Exit 0:** report the tag and the GitHub release's URL, or that `--tag-only` stopped at the tag.
- **Exit 2:** the reason is the answer. A failed check of the release commit sends you to its fix,
  which lands as a new release commit through `ils branch`, `ils push`, and the user's `ils merge`.
  A local tag at another commit is the user's to delete. Tell the user and stop: the next
  `ils release` needs a new ask.
- **Exit 1:** the output says what was written and what was not: a tag made locally whose push
  origin refused, or a tag on origin whose GitHub release was not created. Report it and stop.
  Running the same command again resumes past what was written, once the user asks.
- **Exit 4:** the checks of the release commit were still running, or the time limit left too little
  for the writes. Nothing was written. Run the command `Next:` names, which is the same command
  with the same flags, and keep going while it exits 4.

`ils release` refuses a red release commit; a tag on red is the user's, with `--force`, which they
give.
