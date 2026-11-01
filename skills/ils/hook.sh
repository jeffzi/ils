#!/usr/bin/env bash
# hook.sh fronts `ils hook claude` as the PreToolUse hook of Claude Code. Claude Code reads exit 2
# as a refusal and any other non-zero exit as a broken hook whose tool call still runs, so every
# way ils fails to answer becomes exit 2 here. ils runs as a child, never through exec, so that its
# exit can be mapped.
set -euo pipefail

if ! command -v ils >/dev/null; then
  printf 'ils hook: ils is not on PATH; install it with: ! task install\n' >&2
  exit 2
fi

code=0
ils hook claude || code=$?

case "$code" in
0 | 2) exit "$code" ;;
126 | 127)
  printf 'ils hook: ils could not start (exit %s); install it again with: ! task install\n' "$code" >&2
  exit 2
  ;;
*)
  printf 'ils hook: ils hook claude exited %s, so the command is refused\n' "$code" >&2
  exit 2
  ;;
esac
