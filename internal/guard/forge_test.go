//go:build unix

package guard_test

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/guard"
	"github.com/jeffzi/ils/internal/logtest"
)

// judged is a command, the outcome Judge must give it, and a word its reason must hold.
type judged struct {
	name      string
	command   string
	wantNamed string
	want      guard.Outcome
}

// forgeCaller returns a caller working in an empty directory that is no repository.
func forgeCaller(t *testing.T) guard.Caller {
	t.Helper()

	return guard.Caller{Home: home, Dir: t.TempDir()}
}

// checkJudgedRow fails unless Judge, run by caller, gives the row's command its outcome. An allowed
// command gets a verdict with no reason; any other outcome gets a reason naming what the row names.
func checkJudgedRow(t *testing.T, caller guard.Caller, tt judged) {
	t.Helper()

	got := guard.Judge(t.Context(), tt.command, caller)

	if tt.want == guard.Allow {
		assert.Check(t, is.DeepEqual(got, guard.Verdict{Outcome: guard.Allow}), "Judge(%q)", tt.command)

		return
	}

	assert.Check(t, is.Equal(got.Outcome, tt.want), "Judge(%q) outcome, reason %q", tt.command, got.Reason)

	if tt.wantNamed != "" {
		assert.Check(t, is.Contains(got.Reason, tt.wantNamed), "Judge(%q) reason", tt.command)
	}
}

// checkJudged fails unless Judge gives every command its row's outcome, with a reason naming what
// the row names.
func checkJudged(t *testing.T, caller guard.Caller, tests []judged) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checkJudgedRow(t, caller, tt)
		})
	}
}

// checkJudgedInTurn is checkJudged for rows that share a scripted gh, which sets process
// environment, so no row runs in parallel.
func checkJudgedInTurn(t *testing.T, tests []judged) {
	t.Helper()

	caller := forgeCaller(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkJudgedRow(t, caller, tt)
		})
	}
}

func TestJudge_AsksBeforeALandingCommandRunAloneByItsBareName(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "ils merge", command: "ils merge", want: guard.Ask},
		{name: "ils release", command: "ils release 0.6.0", want: guard.Ask},
		{name: "gh pr merge", command: "gh pr merge 3 --squash", want: guard.Ask},
		{name: "gh release create", command: "gh release create v0.6.0", want: guard.Ask},
	})
}

func TestJudge_RefusesALandingCommandNotRunAloneByItsBareNameNamingIt(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "ils merge piped", command: "ils merge | cat", want: guard.Refuse, wantNamed: "ils merge"},
		{name: "ils release in the background", command: "ils release 0.6.0 &", want: guard.Refuse, wantNamed: "ils release"},
		{name: "gh pr merge redirected", command: "gh pr merge 3 > out.txt", want: guard.Refuse, wantNamed: "gh pr merge"},
		{name: "gh release create behind a backslash", command: `\gh release create v0.6.0`, want: guard.Refuse, wantNamed: "gh release create"},
	})
}

func TestJudge_RefusesAnAdminMergeInEveryForm(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "bare", command: "gh pr merge 3 --admin", want: guard.Refuse, wantNamed: "--admin"},
		{name: "with a value attached", command: "gh pr merge 3 --admin=true", want: guard.Refuse, wantNamed: "--admin"},
	})
}

func TestJudge_ReadsTheIlsCommandAsCobraFindsIt(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "unknown root flag takes the next word", command: "ils --force 0.6.0 release", want: guard.Ask, wantNamed: "ils release"},
		{name: "unknown short root flag takes the next word", command: "ils -x status merge", want: guard.Ask, wantNamed: "ils merge"},
		{name: "root switches take no word", command: "ils -d --json --no-color merge", want: guard.Ask, wantNamed: "ils merge"},
		{name: "attached value takes no word", command: "ils --json=true merge", want: guard.Ask, wantNamed: "ils merge"},
	})
}

func TestJudge_RefusesAPushOrBranchNotRunAloneByItsBareNameNamingIt(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "piped", command: "ils push | cat", want: guard.Refuse, wantNamed: "ils push"},
		{name: "in a list", command: "ils branch topic && true", want: guard.Refuse, wantNamed: "ils branch"},
	})
}

func TestJudge_AllowsAnyOtherIlsCommandInAnyForm(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "status piped", command: "ils status | cat", want: guard.Allow},
		{name: "flag alone", command: "ils --version && true", want: guard.Allow},
	})
}

func TestJudge_RefusesAGhWriteNamingIt(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "api method separate", command: "gh api -X POST repos/x/y/issues", want: guard.Refuse, wantNamed: "gh api"},
		{name: "api long method separate", command: "gh api --method DELETE repos/x/y", want: guard.Refuse, wantNamed: "gh api"},
		// cspell:ignore XPUT -- gh reads -X with its value attached; the test needs that spelling.
		{name: "api method attached", command: "gh api -XPUT repos/x/y", want: guard.Refuse, wantNamed: "gh api"},
		{name: "api long method attached in lower case", command: "gh api --method=put repos/x/y", want: guard.Refuse, wantNamed: "gh api"},
		{name: "api typed field", command: "gh api repos/x/y/issues -F title=x", want: guard.Refuse, wantNamed: "gh api"},
		{name: "api method not literal", command: `gh api -X "$method" repos/x/y`, want: guard.Refuse, wantNamed: "gh api"},
		{name: "repo sync", command: "gh repo sync", want: guard.Refuse, wantNamed: "gh repo sync"},
		{name: "auth token", command: "gh auth token", want: guard.Refuse, wantNamed: "gh auth token"},
		{name: "api method flag as the value of a field", command: "gh api -f --method=GET -f title=x repos/x/y/issues", want: guard.Refuse, wantNamed: "gh api"},
		{name: "api method flag as the value of a long valued flag", command: "gh api --jq --method=GET -f title=x repos/x/y/issues", want: guard.Refuse, wantNamed: "gh api"},
		{name: "config get token", command: "gh config get oauth_token", want: guard.Refuse, wantNamed: "oauth_token"},
		{name: "pr checkout short force", command: "gh pr checkout 12 -f", want: guard.Refuse, wantNamed: "gh pr checkout"},
	})
}

func TestJudge_AllowsAGhCallThatWritesNothing(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "api read", command: "gh api repos/x/y", want: guard.Allow},
		{name: "api explicit get with a field", command: "gh api -X GET search/issues -f q=1", want: guard.Allow},
		{name: "api explicit get in lower case attached", command: "gh api --method=get search/issues -f q=1", want: guard.Allow},
		{name: "pr checkout", command: "gh pr checkout 12", want: guard.Allow},
		{name: "config get another key", command: "gh config get editor", want: guard.Allow},
	})
}

func TestJudge_ReadsGhsCommandPastItsValueFlags(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "short repo before merge", command: "gh pr -R o/r merge 3", want: guard.Ask},
		{name: "long repo before merge piped", command: "gh pr --repo o/r merge 3 | cat", want: guard.Refuse, wantNamed: "gh pr merge"},
	})
}

func TestJudge_RefusesChangingGhAliases(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "set", command: "gh alias set pm 'pr merge'", want: guard.Refuse, wantNamed: "gh alias set"},
	})
}

func TestJudge_JudgesTheExpansionOfAGhAlias(t *testing.T) {
	// Not parallel: ghtest.Setup sets GH_PATH for the whole process.
	const aliases = `finder: '!gh issue list | grep "$1"'` + "\n" +
		"mg: pr merge $1\n" +
		"pm: pr merge\n" +
		"rd: release delete $1\n" +
		"two: release delete $1 $2\n" +
		"vw: pr $1\n"
	ghtest.Setup(t, ghtest.Says(aliases, "alias", "list"))

	checkJudgedInTurn(t, []judged{
		{name: "alias of a landing command", command: "gh pm 3", want: guard.Ask},
		{name: "shell alias", command: "gh finder bug", want: guard.Refuse, wantNamed: "finder"},
		{name: "neither a command nor an alias", command: "gh copilot suggest", want: guard.Refuse, wantNamed: "copilot"},
		{name: "argument put in place", command: "gh vw merge 3", want: guard.Ask, wantNamed: "gh pr merge"},
		{name: "argument not a literal", command: `gh rd "$tag"`, want: guard.Refuse, wantNamed: "not a literal"},
		{name: "placeholder left unfilled", command: "gh two v0.6.0", want: guard.Refuse, wantNamed: "cannot read"},
		{name: "arguments past the placeholders appended", command: "gh mg 3 --admin", want: guard.Refuse, wantNamed: "--admin"},
	})
}

func TestJudge_WhenGhRunnerLogs_LogsTheAliasLookup(t *testing.T) {
	// Not parallel: ghtest.Setup sets GH_PATH for the whole process.
	ghtest.Setup(t, ghtest.Says("pm: pr merge\n", "alias", "list"))
	logger, sink := logtest.New()
	caller := guard.Caller{Home: home, Dir: t.TempDir(), GH: github.Runner{Logger: logger}}

	guard.Judge(t.Context(), "gh pm 3", caller)

	assert.Check(t, is.Len(sink.Records(), 1), "records the alias lookup logged")
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

	checkJudgedInTurn(t, []judged{
		{name: "config dir assigned", command: "GH_CONFIG_DIR=" + configDir + " gh sy", want: guard.Refuse, wantNamed: "gh repo sync"},
		{name: "config dir through env", command: "env GH_CONFIG_DIR=" + configDir + " gh sy", want: guard.Refuse, wantNamed: "gh repo sync"},
		{name: "config dir exported earlier", command: "export GH_CONFIG_DIR=" + configDir + "; gh sy", want: guard.Refuse, wantNamed: "gh repo sync"},
		{name: "config home assigned", command: "XDG_CONFIG_HOME=" + configHome + " gh sy", want: guard.Refuse, wantNamed: "gh repo sync"},
		{name: "home assigned", command: "HOME=" + userHome + " gh sy", want: guard.Refuse, wantNamed: "gh repo sync"},
	})
}

func TestJudge_RefusesAGhAliasWhoseConfigTheGuardCannotReadNamingTheVariable(t *testing.T) {
	// Not parallel: it sets GH_PATH and the gh config location for the whole process.
	ghReadingConfig(t, "sy: repo view\n")

	checkJudgedInTurn(t, []judged{
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
