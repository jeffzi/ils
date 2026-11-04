# Guard scope

The guard reads an agent's shell command before it runs. It refuses the git, `gh`, and `ils` calls
that destroy work, skip checks, push, or land a change. The rules on this page decide what the guard
refuses and what it lets through. Apply them before you add, change, or remove a refusal.
[What the guard refuses](reference.md#what-the-guard-refuses) lists the refusals they produce.

- [Why the guard needs rules](#why-the-guard-needs-rules)
- [Who the guard stops](#who-the-guard-stops)
  - [1. Stop a cooperative agent, not an adversary](#1-stop-a-cooperative-agent-not-an-adversary)
- [When to refuse](#when-to-refuse)
  - [2. Refuse every direct operation](#2-refuse-every-direct-operation)
  - [3. Close a bypass only when agents write it](#3-close-a-bypass-only-when-agents-write-it)
  - [4. Fail closed only where it matters](#4-fail-closed-only-where-it-matters)
- [When to let a command through](#when-to-let-a-command-through)
  - [5. Read an everyday construct](#5-read-an-everyday-construct)
  - [6. Refuse by effect, not by name](#6-refuse-by-effect-not-by-name)
  - [7. Refuse no form whose plain sibling runs](#7-refuse-no-form-whose-plain-sibling-runs)
  - [8. Treat code in another language as data](#8-treat-code-in-another-language-as-data)
- [How a refusal reads](#how-a-refusal-reads)
  - [9. Name a spelling that passes](#9-name-a-spelling-that-passes)
- [Checking a change](#checking-a-change)
  - [10. Measure each new refusal on real commands](#10-measure-each-new-refusal-on-real-commands)
- [Accepted limits](#accepted-limits)

## Why the guard needs rules

A shell offers endless ways to hide a command. A review of the guard can always find one more, so
review alone never ends. Each new refusal also costs something: when the guard refuses ordinary
work, the agent loses a turn. It then looks for another way to do the same thing, often with a
plain tool the guard sees less well.

These rules came from a replay of about 76,000 shell commands that Claude Code agents ran in
real work:

- The shell hook that came before the guard refused git operations 110 times. After each refusal,
  the agent complied, dropped the refused part, or switched to a plain tool: `rm` for `git rm`,
  `| tee file` for `> file`, and git plumbing for `git restore`. None of the retries hid the
  command.
- When these rules were written, the guard refused 2.5% of the 25,000 distinct commands. Most of
  those refusals hit ordinary work, such as `git stash list` or a script run through a variable.
- Two proposed refusals aimed at hidden commands would have refused one command in 13.

## Who the guard stops

### 1. Stop a cooperative agent, not an adversary

The guard stops two things: an agent's mistake, and an agent retrying the same intent in another
everyday spelling. A retry follows a refusal or a failing hook.

The guard does not try to stop an agent that is working to defeat it. That agent can always write
a script to a file and run it (`./x.sh`, `python3 x.py`), and no shell parser sees what the script
runs.

## When to refuse

### 2. Refuse every direct operation

Refuse a git, `gh`, or `ils` call that destroys work, skips checks, pushes, or lands a change,
however rare it is: `git repack -a -d` drops unreachable objects at once, the loss `git prune` is
refused for.

Refuse it in every spelling the tool itself accepts:

- an abbreviated long option, which git accepts;
- bundled short flags (`-fd`);
- `--flag=value`;
- an alias.

Refuse it on every everyday route an agent takes to it:

- a shell string (`bash -c`, `eval`, `su -c`, `ssh host '…'`);
- another directory or repository (`cd other &&`, `-C`, `GIT_DIR`);
- a command that runs another (`env`, `timeout`, `xargs`);
- a command that git runs (`GIT_EDITOR`, `core.editor`, `rebase --exec`);
- a function the command defines and then calls.

### 3. Close a bypass only when agents write it

A bypass is a command that does what a refusal stops while the guard lets it through. Close one
only when an agent would write it in ordinary work. The test: would the command plausibly appear in
the transcript of an agent doing its job, or retrying after a refusal?

A construct whose only use is to hide a command fails that test. Examples:

- a name reference (`declare -n`) that writes `GIT_DIR`;
- a path built in pieces (`GIT_DIR+=/.git`);
- a tool name built by a glob or a brace (`gi[t]`, `{git,}`);
- a planted symlink, or a directory of planted programs (`--exec-path`);
- a push carried by an option that runs a program (`--upload-pack`, `sort --compress-program`).

[Accepted limits](#accepted-limits) lists these as a class. A report that a command gets past the
guard becomes work only when the command passes this test.

### 4. Fail closed only where it matters

A word the guard cannot read, such as `"$opt"`, refuses the command only when the verdict turns on
it. That happens when the word is:

- the command word;
- a git or `gh` subcommand;
- an argument where a guarded flag of git or `gh` can stand (`git clean "$opt"` could be `git clean
  --force`);
- a `-c` value;
- the directory or git dir of a command that may land on the default branch (`git commit`, `git
  merge`).

Anywhere else, the guard passes over the word: `python3 x.py "$f"` and `sed -n "${n}p" "$f"` run.

## When to let a command through

### 5. Read an everyday construct

When ordinary work uses a construct, the guard reads it and judges what it does, rather than
refusing it as unreadable. Examples:

- a variable that the same command assigns a literal: `S=/tmp/work; $S/run.sh` and `cd "$S"`;
- a literal shell string behind a command the guard does not know: `xargs -I{} sh -c 'ls {}'`,
  `docker run img sh -c 'make test'`;
- `git switch -c topic && git commit`, where HEAD is the new branch;
- a helper function, judged where it is called, with the redirects of the call.

### 6. Refuse by effect, not by name

A command with read-only forms is refused only in its writing forms:

- `git stash list` and `git stash show` read;
- `git show HEAD:f > /dev/null`, or into a file outside any working tree, overwrites no work;
- `gh api graphql` with a `query` reads, while one with a `mutation` writes;
- `git rebase --continue` in a dirty tree keeps the work tree, while `--abort` discards it.

### 7. Refuse no form whose plain sibling runs

When the guard refuses one form and allows a sibling that does the same thing, the refusal protects
nothing. It costs the agent a turn and moves it sideways. `bash x.sh` and `./x.sh` run the same
file, and the guard can never refuse `./x.sh`, so it allows `bash x.sh` too.

A `source` or `.` of a file runs in the current shell and may change its directory or variables.
The guard leaves the directory and the git variables unknown after it, instead of refusing it.

### 8. Treat code in another language as data

The guard reads shell, and only shell. For sed, awk, Perl, Python, or Node code, it could only scan
the text for a word such as `git`. That scan misses `python3 x.py`, which runs the same code from a
file. It also refuses ordinary work whose text mentions git, such as `sed -n '/git push/p'
README.md`, or a Python script that edits a file about git.

So:

- An interpreter's arguments are never read as a shell script, even when they happen to parse as
  one (`sed 's/old text/git push/' f`), except behind `ssh` or `su` as the command word, which hand
  every word to a shell.
- A string that does not parse as shell is data, whatever command it goes to.
- A heredoc body on an interpreter's stdin is not read.
- A bare `git`, `gh`, `ils`, or shell word among an interpreter's arguments still counts as handed
  over (`python3 tool.py git push`), as it does for any command the guard does not know.
- Shell stays shell. The guard reads and judges `bash -c` and `eval` strings, a string for `su -c`,
  `ssh host`, or `docker run … sh -c`, and a heredoc fed to a shell.

## How a refusal reads

### 9. Name a spelling that passes

A refusal that fails closed on a word it cannot read names a spelling the guard allows. `git add
$(git diff --name-only)` is refused because the list could hold `--force`. The reason says to put
the paths after `--`, which the guard passes over.

Agents follow a reason that says what to do instead. A reason that does not leaves them looking for
another tool.

## Checking a change

### 10. Measure each new refusal on real commands

Check every new refusal two ways before it lands:

- The table of everyday commands in `TestJudge_AllowsEverydayCommandsOutsideAnyRepository`
  (`internal/guard/guard_test.go`) must stay allowed. A refusal that catches one of them needs a
  narrower trigger.
- A replay judges every shell command in your local Claude Code transcripts and counts the
  refusals by reason: run `task test:replay`. A change that adds a refusal states how many replayed
  commands it refuses. More than 1 in 1,000 needs a narrower trigger, unless the commands it
  refuses do what [rule 2](#2-refuse-every-direct-operation) refuses: destroy work, skip checks,
  push, or land a change.

## Accepted limits

The guard does not see what a command runs from a file, or in a language other than shell. The
[README](../README.md#the-guard) lists those limits. A construct whose only use is to hide a
command is one more: [rule 3](#3-close-a-bypass-only-when-agents-write-it) gives examples and the
test that tells it from an everyday spelling.

These classes fail that test, so the guard lets them through:

- **A shell reading a script the command does not hold:** `curl … | bash`, `bash < x.sh`,
  `bash <(…)`, `bash -s`, or `source <(…)`, as the command itself. Agents pipe an install script
  for what it installs, and that script holds no git operation. Writing a guarded command into a
  script only to pipe it to a shell only hides it. Behind a command the guard does not know
  (`timeout 60 bash <(…)`), such a shell is still refused as a shell word handed over.
- **HEAD moved where the guard does not follow it:** a write to `.git/HEAD` by a command other
  than git, or a `git switch` in a background job or a pipe stage, which runs beside the rest of
  the command. Agents switch branches with git, as a command of its own. Writing the HEAD file by
  hand, or racing a switch against a commit, only hides where the commit lands.
- **An option or config key that runs a program, other than the editors the guard reads where
  the command sets them, as [rule 2](#2-refuse-every-direct-operation) asks (`core.editor` and
  `sequence.editor` given by `-c` or `--config-env`, `GIT_EDITOR`, and `GIT_SEQUENCE_EDITOR`):**
  `core.pager`, `core.sshCommand`, `credential.helper`, the command keys of diff, merge, and tool
  drivers, `difftool -x`, and `go test -exec`. Agents set these to run a pager, an SSH key, or a
  test runner. Reaching a push through one only hides the push.
- **Config the guard does not read written by `git config`:** an editor or any other key that
  runs a program (`git config core.editor vim`), and an `include.path` or `includeIf.*.path` whose
  file can set any key, `core.hooksPath` included. Agents set their editor and split their config
  into included files. A later command runs what that config names from a file, which the guard
  never reads; writing a guarded command or a hooks path there only hides it. `git config` of
  `core.hooksPath`, `remote.*`, and `url.*` themselves stays refused.
- **Aliases and shadowing functions defined in the command:** `alias g=git` and a `cd()` that
  wraps `builtin cd`. Agents define them for convenience. A non-interactive bash expands no alias,
  and the guard still judges each command of a function body. Using either to reach a guarded
  operation only hides it.
- **Exports through `set -a`:** `set -a; read -r GIT_DIR` exports a variable the guard never sees
  assigned. Agents write `export GIT_DIR=…`, which the guard reads, and use `set -a` to load a
  `.env` file of their own settings.
