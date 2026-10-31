package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/guard"
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

// checkJudged fails unless Judge gives every command its row's outcome, with a reason naming what
// the row names.
func checkJudged(t *testing.T, caller guard.Caller, tests []judged) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := guard.Judge(t.Context(), tt.command, caller)

			assert.Check(t, is.Equal(got.Outcome, tt.want), "Judge(%q) outcome, reason %q", tt.command, got.Reason)
			assert.Check(t, is.Contains(got.Reason, tt.wantNamed), "Judge(%q) reason", tt.command)
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
			got := guard.Judge(t.Context(), tt.command, caller)

			assert.Check(t, is.Equal(got.Outcome, tt.want), "Judge(%q) outcome, reason %q", tt.command, got.Reason)
			assert.Check(t, is.Contains(got.Reason, tt.wantNamed), "Judge(%q) reason", tt.command)
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
		{name: "ils flag before the subcommand", command: "ils --json merge", want: guard.Ask},
	})
}

func TestJudge_RefusesALandingCommandNotRunAloneByItsBareNameNamingIt(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "ils merge piped", command: "ils merge | cat", want: guard.Refuse, wantNamed: "ils merge"},
		{name: "ils release in the background", command: "ils release 0.6.0 &", want: guard.Refuse, wantNamed: "ils release"},
		{name: "gh pr merge redirected", command: "gh pr merge 3 > out.txt", want: guard.Refuse, wantNamed: "gh pr merge"},
		{name: "gh release create behind a backslash", command: `\gh release create v0.6.0`, want: guard.Refuse, wantNamed: "gh release create"},
		{name: "ils merge quoted", command: `"ils" merge`, want: guard.Refuse, wantNamed: "ils merge"},
	})
}

func TestJudge_RefusesTheProbedFormsOfALandingCommand(t *testing.T) {
	t.Parallel()

	commands := []string{
		"timeout 60 ils merge",
		"FOO=1 ils merge",
		"env ils merge",
		"nohup ils merge",
		"ils merge > out.txt",
		"ils merge 2>&1",
		"ils merge | cat",
		"cd sub && ils merge",
		"true && ils merge",
		"ils merge; true",
		"(ils merge)",
		"bash -c 'ils merge'",
		"/Users/jeffzi/go/bin/ils merge",
	}
	tests := make([]judged, 0, len(commands))
	for _, command := range commands {
		tests = append(tests, judged{name: command, command: command, want: guard.Refuse, wantNamed: "on its own, by its bare name"})
	}
	checkJudged(t, forgeCaller(t), tests)
}

func TestJudge_RefusesAnAdminMergeInEveryForm(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "bare", command: "gh pr merge 3 --admin", want: guard.Refuse, wantNamed: "--admin"},
		{name: "piped", command: "gh pr merge --admin 3 | cat", want: guard.Refuse, wantNamed: "--admin"},
		{name: "with a value attached", command: "gh pr merge 3 --admin=true", want: guard.Refuse, wantNamed: "--admin"},
	})
}

func TestJudge_AllowsAPushOrBranchRunAloneByItsBareName(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "ils push", command: "ils push", want: guard.Allow},
		{name: "ils branch", command: "ils branch topic", want: guard.Allow},
		{name: "ils flag before the subcommand", command: "ils --json push", want: guard.Allow},
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
		{name: "teed", command: "ils push 2>&1 | tee push.log", want: guard.Refuse, wantNamed: "ils push"},
		{name: "in a list", command: "ils branch topic && true", want: guard.Refuse, wantNamed: "ils branch"},
		{name: "behind a wrapper", command: "nohup ils push", want: guard.Refuse, wantNamed: "ils push"},
		{name: "spelled as a path", command: "/usr/local/bin/ils branch topic", want: guard.Refuse, wantNamed: "ils branch"},
	})
}

func TestJudge_AllowsAnyOtherIlsCommandInAnyForm(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "status piped", command: "ils status | cat", want: guard.Allow},
		{name: "status behind a wrapper", command: "env ils status", want: guard.Allow},
		{name: "flag before the subcommand redirected", command: "ils --json status > out.json", want: guard.Allow},
		{name: "help naming a landing command", command: "ils help merge | tee help.txt", want: guard.Allow},
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
		{name: "api raw field", command: "gh api repos/x/y/issues -f title=x", want: guard.Refuse, wantNamed: "gh api"},
		{name: "api long field", command: "gh api repos/x/y/issues --field title=x", want: guard.Refuse, wantNamed: "gh api"},
		{name: "api long raw field", command: "gh api repos/x/y/issues --raw-field title=x", want: guard.Refuse, wantNamed: "gh api"},
		{name: "api input", command: "gh api repos/x/y/issues --input body.json", want: guard.Refuse, wantNamed: "gh api"},
		{name: "api method not literal", command: `gh api -X "$method" repos/x/y`, want: guard.Refuse, wantNamed: "gh api"},
		{name: "repo sync", command: "gh repo sync", want: guard.Refuse, wantNamed: "gh repo sync"},
		{name: "repo delete", command: "gh repo delete o/r --yes", want: guard.Refuse, wantNamed: "gh repo delete"},
		{name: "repo edit", command: "gh repo edit --visibility public", want: guard.Refuse, wantNamed: "gh repo edit"},
		{name: "repo create with push", command: "gh repo create o/r --source . --push", want: guard.Refuse, wantNamed: "--push"},
		{name: "release delete", command: "gh release delete v0.6.0", want: guard.Refuse, wantNamed: "gh release delete"},
		{name: "release edit", command: "gh release edit v0.6.0 --draft", want: guard.Refuse, wantNamed: "gh release edit"},
		{name: "release upload", command: "gh release upload v0.6.0 ils.tar.gz", want: guard.Refuse, wantNamed: "gh release upload"},
		{name: "release delete-asset", command: "gh release delete-asset v0.6.0 ils.tar.gz", want: guard.Refuse, wantNamed: "gh release delete-asset"},
		{name: "auth token", command: "gh auth token", want: guard.Refuse, wantNamed: "gh auth token"},
		{name: "auth status short show token", command: "gh auth status -t", want: guard.Refuse, wantNamed: "-t"},
		{name: "auth status long show token", command: "gh auth status --show-token", want: guard.Refuse, wantNamed: "--show-token"},
		{name: "config get token", command: "gh config get oauth_token", want: guard.Refuse, wantNamed: "oauth_token"},
		{name: "config get token of a host", command: "gh config get -h github.com oauth_token", want: guard.Refuse, wantNamed: "oauth_token"},
		{name: "pr checkout short force", command: "gh pr checkout 12 -f", want: guard.Refuse, wantNamed: "gh pr checkout"},
		{name: "pr checkout long force", command: "gh pr checkout --force 12", want: guard.Refuse, wantNamed: "gh pr checkout"},
	})
}

func TestJudge_AllowsAGhCallThatWritesNothing(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "api read", command: "gh api repos/x/y", want: guard.Allow},
		{name: "api explicit get with a field", command: "gh api -X GET search/issues -f q=1", want: guard.Allow},
		{name: "api explicit get in lower case attached", command: "gh api --method=get search/issues -f q=1", want: guard.Allow},
		{name: "pr checkout", command: "gh pr checkout 12", want: guard.Allow},
		{name: "repo create without push", command: "gh repo create o/r --private", want: guard.Allow},
		{name: "auth status", command: "gh auth status", want: guard.Allow},
		{name: "config get another key", command: "gh config get editor", want: guard.Allow},
		{name: "alias list", command: "gh alias list", want: guard.Allow},
	})
}

func TestJudge_ReadsGhsCommandPastItsValueFlags(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "short repo before merge", command: "gh pr -R o/r merge 3", want: guard.Ask},
		{name: "long repo before merge piped", command: "gh pr --repo o/r merge 3 | cat", want: guard.Refuse, wantNamed: "gh pr merge"},
		{name: "repo value naming a command", command: "gh pr -R merge checkout 12", want: guard.Allow},
		{name: "short repo before release delete", command: "gh release -R o/r delete v0.6.0", want: guard.Refuse, wantNamed: "gh release delete"},
		{name: "short hostname before auth token", command: "gh auth -H github.com token", want: guard.Refuse, wantNamed: "gh auth token"},
		{name: "long hostname before auth token", command: "gh auth --hostname github.com token", want: guard.Refuse, wantNamed: "gh auth token"},
		{name: "subcommand flag before merge", command: "gh pr -b x merge --admin 3", want: guard.Refuse, wantNamed: "--admin"},
		{name: "host before config get", command: "gh config -h github.com get oauth_token", want: guard.Refuse, wantNamed: "oauth_token"},
		{name: "host before auth token", command: "gh auth -h github.com token", want: guard.Refuse, wantNamed: "gh auth token"},
		{name: "long flag before repo edit", command: "gh repo --description x edit", want: guard.Refuse, wantNamed: "gh repo edit"},
		{name: "help takes no word", command: "gh --help pr merge 3", want: guard.Ask, wantNamed: "gh pr merge"},
	})
}

func TestJudge_RefusesChangingGhAliases(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "set", command: "gh alias set pm 'pr merge'", want: guard.Refuse, wantNamed: "gh alias set"},
		{name: "import", command: "gh alias import aliases.yml", want: guard.Refuse, wantNamed: "gh alias import"},
		{name: "delete", command: "gh alias delete pm", want: guard.Refuse, wantNamed: "gh alias delete"},
	})
}

func TestJudge_JudgesTheExpansionOfAGhAlias(t *testing.T) {
	// Not parallel: ghtest.Setup sets GH_PATH for the whole process.
	const aliases = "co: pr checkout\n" +
		"dl: release delete\n" +
		`finder: '!gh issue list | grep "$1"'` + "\n" +
		"pm: pr merge\n" +
		"rd: release delete $1\n" +
		"two: release delete $1 $2\n" +
		"vw: pr $1\n"
	ghtest.Setup(t, ghtest.Says(aliases, "alias", "list"))

	checkJudgedInTurn(t, []judged{
		{name: "alias of a landing command", command: "gh pm 3", want: guard.Ask},
		{name: "alias of a write", command: "gh dl v0.6.0", want: guard.Refuse, wantNamed: "release delete"},
		{name: "alias of a read", command: "gh co 12", want: guard.Allow},
		{name: "shell alias", command: "gh finder bug", want: guard.Refuse, wantNamed: "finder"},
		{name: "neither a command nor an alias", command: "gh copilot suggest", want: guard.Refuse, wantNamed: "copilot"},
		{name: "argument put in place", command: "gh vw merge 3", want: guard.Ask, wantNamed: "gh pr merge"},
		{name: "argument put in place of a write", command: "gh rd v0.6.0", want: guard.Refuse, wantNamed: "gh release delete"},
		{name: "argument not a literal", command: `gh rd "$tag"`, want: guard.Refuse, wantNamed: "not a literal"},
		{name: "placeholder left unfilled", command: "gh two v0.6.0", want: guard.Refuse, wantNamed: "cannot read"},
	})
}

func TestJudge_WhenGhAliasListFails_RefusesNamingTheRead(t *testing.T) {
	// Not parallel: ghtest.Setup sets GH_PATH for the whole process.
	ghtest.Setup(t, ghtest.Exits(ghtest.ServerError(), "alias", "list"))
	const command = "gh pm 3"

	got := guard.Judge(t.Context(), command, forgeCaller(t))

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Contains(got.Reason, "alias list"), "Judge(%q) reason", command)
}
