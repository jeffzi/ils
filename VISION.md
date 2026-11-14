# Vision

`ils` lets a developer hand an agent the landing of a change on GitHub without watching each
step. It replaces the landing instructions the developer would otherwise write for the agent as
prose. `ils` is opinionated: it lands every change the same way in every repository where it is
installed.

- [The problem](#the-problem)
- [Principles](#principles)
- [Non-goals](#non-goals)
- [Where ils fits](#where-ils-fits)
- [Scope and maintenance](#scope-and-maintenance)

## The problem

A developer can tell an agent how to land a change, but the agent follows prose loosely. It skips a
step or paraphrases it. [README.md](README.md) lists what goes wrong, and each mistake reaches the
branch everyone else builds on. A developer who checks every step has handed nothing over.

## Principles

The principles are ranked: when two conflict, the lower number wins.

1. **The only path to the default branch is a green pull request.** Every change reaches it as one
   squash commit whose checks passed on its pull request. A broken commit followed by a fix is the
   failure `ils` exists to prevent. A small change takes the same branch and pull request as a
   large one. Adding a follow-up to the open branch cuts its cost; skipping the pull request never
   does. Closes: a direct-push mode for small fixes, a merge on pending or skipped checks.
2. **The developer decides what other people see.** The merge and the release wait for the
   developer's word. The agent carries a change to a green pull request with its title and body
   written, then stops. On a private repository the push waits for the developer's word too, since
   each push starts CI runs that spend Actions minutes. A workflow that sets its own push timing,
   such as one that pushes once after its last step, decides when it pushes. When the developer
   asks for a step that breaks #1, the agent says so before taking it. Closes: a merge as soon as
   the checks pass, a release as soon as the release commit lands.
3. **Enforce, don't instruct.** A step that must not fail lives in the binary, which checks its
   preconditions and refuses with a reason. A command exists when it makes a mistake impossible,
   not when it saves typing. Closes: an `ils` command that wraps a `git` command and refuses
   nothing, a rule left in a skill's prose that the binary could check.
4. **Stop a cooperative agent's mistakes, not an adversary.** The guard stops a slip, and the
   everyday retry that follows a refusal. An agent set on defeating the guard can always run a
   script from a file, which no shell parser reads. Closes: a refusal of a construct whose only use
   is to hide a command ([guard scope](docs/guard-scope.md), rule 3).
5. **Stay out of ordinary work.** Each refusal costs the agent a turn and sends it to a blunter
   tool. The guard refuses a command by its effect, never by its name, and never refuses a form
   whose plain sibling runs. Closes: a refusal of `git stash list` because `git stash` can drop
   work.
6. **Every result is one an agent can act on.** Each command ends with an exit code and a `Next:`
   line that names the following step, so the agent needs nothing else from the output. A run that
   was stopped finishes when the same command runs again. The guard's ask before a push on a private
   repository is the developer's word under #2, not a prompt of `ils`. Closes: an interactive
   prompt from `ils` itself, an outcome stated only in prose.
7. **Keep no state of its own.** `ils` reads only what git and GitHub hold: the branch, its pull
   request, its checks, and the tags. Closes: a
   local database or cache of pull request state.

## Non-goals

- **A sandbox.** `ils` does not contain a hostile agent, because a script file always gets past a
  shell parser (#4). The agent's permission rules or a sandbox do that job.
- **A CI system.** `ils` waits for the checks GitHub reports and merges only once they pass. It
  never runs them. GitHub Actions or another CI service runs them.
- **A `git` wrapper.** `ils` covers landing, not version control (#3). The agent commits, diffs,
  and rebases with `git`.
- **Branch protection.** `ils` never writes the rules that protect the default branch, and its
  guard refuses a commit on the default branch whether a rule protects it or not. A GitHub ruleset
  still stops the pushes `ils` never sees, and `ils merge` honors what the ruleset requires.

## Where ils fits

`ils` is the right choice when an agent writes code in a GitHub repository that runs CI, and every
change should land as a pull request whose checks passed.

It is the wrong choice when:

- the developer pushes to the default branch by design;
- the repository lives outside GitHub;
- the work needs stacked pull requests;
- the agent should merge on its own (#2).

[Use with Claude Code](README.md#use-with-claude-code) says how to leave a repository out.

It works beside the alternatives a newcomer weighs it against:

- **A skill or `CLAUDE.md` that describes the flow.** It costs nothing to write, but prose is what
  agents skip.
- **Branch protection alone.** GitHub rejects the bad push, but the agent learns of it late and
  gets no next step. `ils` refuses before anything leaves the machine.
- **`git` and `gh` by hand.** `ils` runs the same commands, and adds the order and the refusals.

## Scope and maintenance

One maintainer builds `ils`. In scope: landing a change on GitHub, from the branch to the release,
and the guard that keeps an agent on that path. A change to the guard follows
[guard scope](docs/guard-scope.md), including its replay measurement. The maintainer closes a
request outside this scope by citing this page, not by judging its code.

The maintainer revisits this page at each minor release.
