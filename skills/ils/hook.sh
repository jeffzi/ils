#!/usr/bin/env bash
# hook.sh fronts `ils hook claude` as the PreToolUse hook of Claude Code. Claude Code reads exit 2
# as a refusal and any other non-zero exit as a broken hook whose tool call still runs, so every
# way ils fails to answer becomes exit 2 here. ils runs as a child, never through exec, so that its
# exit can be mapped.
set -euo pipefail

refuse() {
  printf 'ils hook: %s\n' "$1" >&2
  exit 2
}

if ! command -v ils >/dev/null; then
  refuse 'ils is not on PATH; install it with: ! go install github.com/jeffzi/ils@latest, or ! task install from a clone of ils'
fi

main() {
  local code=0
  ils hook claude || code=$?

  case "$code" in
  0 | 2) exit "$code" ;;
  126 | 127) refuse "ils could not start (exit $code); install it again with: ! go install github.com/jeffzi/ils@latest, or ! task install from a clone of ils" ;;
  *) refuse "ils hook claude exited $code, so the command is refused" ;;
  esac
}

main "$@"
