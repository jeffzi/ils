//go:build unix

package guard_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/guard"
)

func TestJudge_JudgesTheExpansionOfAGhAlias(t *testing.T) {
	// Not parallel: ghtest.Setup sets GH_PATH for the whole process.
	const aliases = `finder: '!gh issue list | grep "$1"'` + "\n" +
		"mg: pr merge $1\n" +
		"rd: release delete $1\n" +
		"two: release delete $1 $2\n" +
		"vw: pr $1\n"
	ghtest.Setup(t, ghtest.Says(aliases, "alias", "list"))

	checkJudgedInTurn(t, callerOutsideAnyRepository(t), []judged{
		{name: "shell alias", command: "gh finder bug", want: guard.Refuse, wantNamed: "finder"},
		{name: "neither a command nor an alias", command: "gh copilot suggest", want: guard.Refuse, wantNamed: "copilot"},
		{name: "argument put in place", command: "gh vw merge 3", want: guard.Ask},
		{name: "argument not a literal", command: `gh rd "$tag"`, want: guard.Refuse, wantNamed: "not a literal"},
		{name: "placeholder left unfilled", command: "gh two v0.6.0", want: guard.Refuse, wantNamed: "cannot read"},
		{name: "arguments past the placeholders appended", command: "gh mg 3 --admin", want: guard.Refuse},
	})
}

// ghReadingConfig points GH_PATH, for the rest of the test, at a gh that answers each argv of
// routes with the answer held under its file name in the config directory gh reads: GH_CONFIG_DIR,
// else XDG_CONFIG_HOME/gh, else HOME/.config/gh. A call whose file is missing there exits 1, and any
// other call exits 123. The process's own config directory, under HOME, holds process.
func ghReadingConfig(t *testing.T, routes map[string][]string, process map[string]ghtest.Answer) {
	t.Helper()

	var cases strings.Builder
	for file, argv := range routes {
		cases.WriteString(`"` + strings.Join(argv, " ") + `") answer ` + file + " ;;\n")
	}

	script := `#!/bin/sh
dir="${GH_CONFIG_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/gh}"
answer() {
	test -f "$dir/$1" || exit 1
	cat "$dir/$1"
	cat "$dir/$1.stderr" >&2
	exit "$(cat "$dir/$1.code")"
}
case "$*" in
--version) echo "gh version ` + github.MinimumVersion + `" ;;
` + cases.String() + `*) exit 123 ;;
esac
`

	gh := filepath.Join(t.TempDir(), github.Program)
	//nolint:gosec // G306: the fake gh must be executable to stand in for gh.
	assert.NilError(t, os.WriteFile(gh, []byte(script), 0o700))
	t.Setenv(github.PathVariable, gh)
	t.Setenv("GH_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", configHolding(t, ".config/gh", process))
}

// configHolding returns a new directory whose subdirectory sub is a gh config directory holding
// each of answers under its file name, as ghReadingConfig reads it.
func configHolding(t *testing.T, sub string, answers map[string]ghtest.Answer) string {
	t.Helper()

	root := t.TempDir()
	dir := filepath.Join(root, sub)
	assert.NilError(t, os.MkdirAll(dir, 0o700))

	for name, answer := range answers {
		files := map[string]string{name: answer.Stdout, name + ".stderr": answer.Stderr, name + ".code": strconv.Itoa(answer.Code)}
		for file, content := range files {
			assert.NilError(t, os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600))
		}
	}

	return root
}

func aliasRoutes() map[string][]string {
	return map[string][]string{"aliases": {"alias", "list"}}
}

// aliasesHolding is a gh config whose `gh alias list` answers aliases, held under the file name
// aliasRoutes routes that call to.
func aliasesHolding(aliases string) map[string]ghtest.Answer {
	return map[string]ghtest.Answer{"aliases": {Stdout: aliases}}
}

func TestJudge_ExpandsAGhAliasFromTheConfigTheInvocationsGhReads(t *testing.T) {
	// Not parallel: it sets GH_PATH and the gh config location for the whole process.
	ghReadingConfig(t, aliasRoutes(), aliasesHolding("sy: repo view\n"))
	aliases := aliasesHolding("sy: repo sync\n")
	configDir := configHolding(t, "", aliases)
	configHome := configHolding(t, "gh", aliases)
	userHome := configHolding(t, ".config/gh", aliases)

	checkJudgedInTurn(t, callerOutsideAnyRepository(t), []judged{
		{name: "config dir assigned", command: "GH_CONFIG_DIR=" + configDir + " gh sy", want: guard.Refuse, wantNamed: "gh repo sync"},
		{name: "config home assigned", command: "XDG_CONFIG_HOME=" + configHome + " gh sy", want: guard.Refuse, wantNamed: "gh repo sync"},
		{name: "home assigned", command: "HOME=" + userHome + " gh sy", want: guard.Refuse, wantNamed: "gh repo sync"},
	})
}

func TestJudge_RefusesAGhAliasWhoseConfigTheGuardCannotReadNamingTheVariable(t *testing.T) {
	// Not parallel: it sets GH_PATH and the gh config location for the whole process.
	ghReadingConfig(t, aliasRoutes(), aliasesHolding("sy: repo view\n"))

	checkJudgedInTurn(t, callerOutsideAnyRepository(t), []judged{
		{name: "config dir not a literal", command: `GH_CONFIG_DIR="$d" gh sy`, want: guard.Refuse, wantNamed: "GH_CONFIG_DIR"},
		{name: "config home exported not a literal", command: `export XDG_CONFIG_HOME="$d"; gh sy`, want: guard.Refuse, wantNamed: "XDG_CONFIG_HOME"},
		{name: "home exported in an if body", command: "if test -d x; then export HOME=/srv; fi; gh sy", want: guard.Refuse, wantNamed: "HOME"},
	})
}

func TestJudge_WhenGhAliasListFails_RefusesNamingTheRead(t *testing.T) {
	// Not parallel: ghtest.Setup sets GH_PATH for the whole process.
	ghtest.Setup(t, ghtest.Exits(ghtest.ServerError(), "alias", "list"))

	checkJudgedRow(t, callerOutsideAnyRepository(t), judged{command: "gh pm 3", want: guard.Refuse, wantNamed: "alias list"})
}
