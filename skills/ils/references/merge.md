# Running `ils merge`

Read this only after the user asked for the merge, as The merge in SKILL.md defines the ask.

## Before you run it

Run `ils status` on the branch, and show the user the title and body it prints for the open pull
request. They become the squash commit's message, and the consent prompt shows only the command.
Then run `ils merge` as the skill's "Run it" section says: bare, with the user's words.

## After it exits

The user's ask covers that one merge until it exits 0, 1, or 2. On exit 0, report the merge and
where the checkout ended. Exit 2 ends the ask, and the reason is the answer: a title that is not a
conventional commit subject sends you to the `gh pr edit` step, and a red check to the fix step.
When that step ends, tell the user and stop: the next `ils merge` needs a new ask.

On exit 4, run what `Next:` names. If it is `ils push`, that push waits for the checks; when it
exits 0, run `ils merge` again, showing the title and body first. If it is `ils merge`, GitHub
already made the merge and `ils merge` is finishing it; run it again without showing them, since
the message is already on the trunk.

`ils merge` has no `--force`; a merge on red is the user's, typed by hand.
