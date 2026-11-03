//go:build unix

package guard_test

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/guard"
	"github.com/jeffzi/ils/internal/logtest"
)

func TestJudge_JudgesTheExpansionOfAGhAlias(t *testing.T) {
	// Not parallel: ghtest.Setup sets GH_PATH for the whole process.
	const aliases = `finder: '!gh issue list | grep "$1"'` + "\n" +
		"mg: pr merge $1\n" +
		"pm: pr merge\n" +
		"rd: release delete $1\n" +
		"two: release delete $1 $2\n" +
		"vw: pr $1\n"
	ghtest.Setup(t, ghtest.Says(aliases, "alias", "list"))

	checkJudgedInTurn(t, forgeCaller(t), []judged{
		{name: "alias of a landing command", command: "gh pm 3", want: guard.Ask},
		{name: "shell alias", command: "gh finder bug", want: guard.Refuse, wantNamed: "finder"},
		{name: "neither a command nor an alias", command: "gh copilot suggest", want: guard.Refuse, wantNamed: "copilot"},
		{name: "argument put in place", command: "gh vw merge 3", want: guard.Ask},
		{name: "argument not a literal", command: `gh rd "$tag"`, want: guard.Refuse, wantNamed: "not a literal"},
		{name: "placeholder left unfilled", command: "gh two v0.6.0", want: guard.Refuse, wantNamed: "cannot read"},
		{name: "arguments past the placeholders appended", command: "gh mg 3 --admin", want: guard.Refuse},
	})
}

func TestJudge_WhenGhRunnerLogs_LogsTheAliasLookup(t *testing.T) {
	// Not parallel: ghtest.Setup sets GH_PATH for the whole process.
	ghtest.Setup(t, ghtest.Says("pm: pr merge\n", "alias", "list"))
	logger, sink := logtest.New()
	caller := guard.Caller{Home: home, Dir: t.TempDir(), GH: github.Runner{Logger: logger}}

	guard.Judge(t.Context(), "gh pm 3", caller)

	want := logtest.ExecAttrs(t, child.Exec{Tool: "gh", Args: []string{"alias", "list"}})
	assert.Check(t, is.DeepEqual(logtest.CallAttrs(t, sink.Records()), want), "record the alias lookup logged")
}

// ghReadingConfig points GH_PATH, for the rest of the test, at a gh that answers `alias list` with
// the file aliases of the config directory gh reads: GH_CONFIG_DIR, else XDG_CONFIG_HOME/gh, else
// HOME/.config/gh. The process's own config directory holds processAliases.
func ghReadingConfig(t *testing.T, processAliases string) {
	t.Helper()

	const script = `#!/bin/sh
case "$*" in
--version) echo "gh version ` + github.MinimumVersion + `" ;;
"alias list") cat "${GH_CONFIG_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/gh}/aliases" ;;
*) exit 123 ;;
esac
`

	gh := filepath.Join(t.TempDir(), "gh")
	//nolint:gosec // G306: the fake gh must be executable to stand in for gh.
	assert.NilError(t, os.WriteFile(gh, []byte(script), 0o700))
	t.Setenv("GH_PATH", gh)
	t.Setenv("GH_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", configHolding(t, ".config/gh", processAliases))
}

// configHolding returns a new directory whose subdirectory sub is a gh config directory holding
// aliases.
func configHolding(t *testing.T, sub, aliases string) string {
	t.Helper()

	root := t.TempDir()
	dir := filepath.Join(root, sub)
	assert.NilError(t, os.MkdirAll(dir, 0o700))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "aliases"), []byte(aliases), 0o600))

	return root
}

func TestJudge_ExpandsAGhAliasFromTheConfigTheInvocationsGhReads(t *testing.T) {
	// Not parallel: it sets GH_PATH and the gh config location for the whole process.
	ghReadingConfig(t, "sy: repo view\n")
	const aliases = "sy: repo sync\n"
	configDir := configHolding(t, "", aliases)
	configHome := configHolding(t, "gh", aliases)
	userHome := configHolding(t, ".config/gh", aliases)

	checkJudgedInTurn(t, forgeCaller(t), []judged{
		{name: "config dir assigned", command: "GH_CONFIG_DIR=" + configDir + " gh sy", want: guard.Refuse, wantNamed: "gh repo sync"},
		{name: "config home assigned", command: "XDG_CONFIG_HOME=" + configHome + " gh sy", want: guard.Refuse, wantNamed: "gh repo sync"},
		{name: "home assigned", command: "HOME=" + userHome + " gh sy", want: guard.Refuse, wantNamed: "gh repo sync"},
	})
}

func TestJudge_RefusesAGhAliasWhoseConfigTheGuardCannotReadNamingTheVariable(t *testing.T) {
	// Not parallel: it sets GH_PATH and the gh config location for the whole process.
	ghReadingConfig(t, "sy: repo view\n")

	checkJudgedInTurn(t, forgeCaller(t), []judged{
		{name: "config dir not a literal", command: `GH_CONFIG_DIR="$d" gh sy`, want: guard.Refuse, wantNamed: "GH_CONFIG_DIR"},
		{name: "config home exported not a literal", command: `export XDG_CONFIG_HOME="$d"; gh sy`, want: guard.Refuse, wantNamed: "XDG_CONFIG_HOME"},
		{name: "home exported in an if body", command: "if test -d x; then export HOME=/srv; fi; gh sy", want: guard.Refuse, wantNamed: "HOME"},
	})
}

func TestJudge_WhenGhAliasListFails_RefusesNamingTheRead(t *testing.T) {
	// Not parallel: ghtest.Setup sets GH_PATH for the whole process.
	ghtest.Setup(t, ghtest.Exits(ghtest.ServerError(), "alias", "list"))

	checkJudgedRow(t, forgeCaller(t), judged{command: "gh pm 3", want: guard.Refuse, wantNamed: "alias list"})
}
