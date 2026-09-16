# AGENTS.md

## Local overrides

If `AGENTS.local.md` exists at the repo root, read it and let its instructions take precedence over
this file. It is gitignored for personal, machine-specific preferences and never committed.

## Commands

`Taskfile.yml` wraps the common workflows — run `task --list` to see them. These are the only
entrypoints; never bypass them by calling `go` directly for tasks that `task` covers — the tasks
manage flags and tool invocations.

- `task fmt` — format all Go source files via golangci-lint fmt.
- `task test` — run the test suite with race detection (append args after `--`).
- `task cov` — run tests with coverage and generate an HTML report.
- `task check` — run every check (fmt, lint, test, vuln).
- `task tidy` — tidy module dependencies.
- `task clean` — remove build artifacts.

## Git hygiene

- Never run `git commit --no-verify`, `git commit -n`, or anything else that skips the prek hooks —
  the hooks are the gate, not an obstacle.
- Fix a failing check at its source. Never edit a test to make it pass; never widen a lint ignore to
  silence a real finding.

## Linter and type-checker configuration

Treat lint config as fixed. Never add to an ignore list, disable a rule, lower a severity, or
exclude a file to get a check passing — fix the code instead. A suppression is warranted only when
the finding is a genuine false positive or the rule cannot apply (e.g. a generated file, a documented
upstream bug); then suppress at the narrowest scope — an inline `//nolint:<linter> // reason` with
a reason — not in the shared config. When the same inline directive keeps recurring for the same
rule, that is a signal the rule may deserve a config-level ignore — propose it to the user and wait
for explicit approval; never promote a suppression into config on your own.

## Doc comments

Go doc comments (`//` before declarations). None required on unexported functions. A one-line doc
comment is complete on its own when the name says the rest — the comment always starts with the
declared name: `// FuncName does X.` Package comments use `// Package name does X.` A function you
edit gets its doc comment brought to this shape even if you did not write it. Tests: no doc comments
on test functions; the name carries the intent.

## Spelling (cspell)

Treat a cspell failure as a prompt to reword, not to grow the dictionary. Prefer plain words in
prose and identifiers. A word earns a `cspell.json` entry only when it comes from outside the
project and cannot be renamed — command names, API identifiers, package names, proper nouns, domain
vocabulary (e.g. `golangci`, `gofumpt`, `govulncheck`). In tests, never invent gibberish that needs
a suppression — any real word works for filler data.
