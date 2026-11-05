//go:build unix

package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

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

// checkJudgedInTurn is checkJudged for rows that share process environment (a scripted gh or git
// on PATH), so no row runs in parallel.
func checkJudgedInTurn(t *testing.T, caller guard.Caller, tests []judged) {
	t.Helper()

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
		{name: "gh pr merge", command: "gh pr merge 3 --squash", want: guard.Ask},
		{name: "help flag after an argument", command: "gh pr merge 1 --help", want: guard.Ask},
		{name: "argument not a literal after the end of flags", command: `gh pr merge -- "$n"`, want: guard.Ask},
	})
}

func TestJudge_AllowsALandingCommandAskedForItsHelpInAnyForm(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "ils merge piped", command: "ils merge --help | head", want: guard.Allow},
		{name: "gh pr merge in a subshell", command: "(gh pr merge --help)", want: guard.Allow},
	})
}

func TestJudge_RefusesAGhArgumentNotALiteralThatMayBeAGuardedFlagNamingTheSpellingsThatPass(t *testing.T) {
	t.Parallel()
	command := `gh pr merge "$n"`

	got := guard.Judge(t.Context(), command, forgeCaller(t))

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome, reason %q", command, got.Reason)
	assert.Check(t, is.Contains(got.Reason, "`--`"), "Judge(%q) reason", command)
}

func TestJudge_WhenDoubleDashLeavesAGhArgumentThatMayBeAGuardedFlagRefused_NamesOnlyALiteral(t *testing.T) {
	t.Parallel()
	caller := forgeCaller(t)

	tests := []struct {
		name    string
		command string
	}{
		{name: "value of a long flag", command: `gh pr merge --body "$n"`},
		{name: "value of a shorthand that takes one", command: `gh pr checkout -b "$n"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checkRefusedOmitting(t, caller, omitting{command: tt.command, named: "literal", omitted: "`--`"})
		})
	}
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
		{name: "bare", command: "gh pr merge 3 --admin", want: guard.Refuse},
		{name: "with a value attached", command: "gh pr merge 3 --admin=true", want: guard.Refuse},
	})
}

func TestJudge_RefusesAGuardedGhFlagWhoseAttachedValueIsNotALiteral(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "pr checkout force", command: `gh pr checkout 1 --force="$v"`, want: guard.Refuse, wantNamed: "literal"},
		{name: "api input", command: `gh api repos/o/r/pulls --input="$f"`, want: guard.Refuse},
	})
}

func TestJudge_PassesOverANonLiteralValueThatCannotSetAGuardedGhFlag(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "flag of a command with no guarded flag", command: `gh pr view 1 --json="$f"`, want: guard.Allow},
		{name: "prefix rules out the guarded flag", command: `gh pr checkout 1 --branch="$b"`, want: guard.Allow},
		{name: "prefix rules out the api fields", command: `gh api repos/o/r --jq="$q"`, want: guard.Allow},
	})
}

func TestJudge_AllowsAGhGraphqlQuery(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "shorthand query", command: `gh api graphql -f query='{ viewer { login } }'`, want: guard.Allow},
		{
			name:    "query with variables beside it",
			command: `gh api graphql -f query='query($n: Int!) { viewer { repositories(first: $n) { totalCount } } }' -F n=3`,
			want:    guard.Allow,
		},
		{
			name:    "query holding mutation as a nested name",
			command: `gh api graphql -f query='{ viewer { mutation: login } }'`,
			want:    guard.Allow,
		},
		{
			name:    "operation whose name starts with mutation",
			command: `gh api graphql -f query='query mutationLog { viewer { login } }'`,
			want:    guard.Allow,
		},
		{
			name:    "query attached to the long field flag",
			command: `gh api graphql --raw-field=query='{ viewer { login } }'`,
			want:    guard.Allow,
		},
		{name: "no field and no input", command: "gh api graphql", want: guard.Allow},
		{
			name:    "endpoint after a flag that takes a value",
			command: `gh api -H 'Accept: application/json' graphql -f query='{ viewer { login } }'`,
			want:    guard.Allow,
		},
	})
}

func TestJudge_RefusesAGhGraphqlMutation(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{
			name:    "mutation",
			command: `gh api graphql -f query='mutation { closeIssue(input: {issueId: "x"}) { clientMutationId } }'`,
			want:    guard.Refuse,
		},
		{
			name:    "query beside a mutation",
			command: `gh api graphql -f query='query A { viewer { login } } mutation B { closeIssue(input: {issueId: "x"}) { clientMutationId } }'`,
			want:    guard.Refuse,
		},
		{name: "query field on another endpoint", command: `gh api search/issues -f query='{ viewer { login } }'`, want: guard.Refuse},
		{
			name:    "mutation after a comment holding a brace",
			command: "gh api graphql -f query='# {\nmutation { closeIssue(input: {issueId: \"x\"}) { clientMutationId } }'",
			want:    guard.Refuse,
		},
		{
			name:    "mutation after a block string holding a quote and a parenthesis",
			command: `gh api graphql -f query='{ a(x: """ " ) """) } mutation { closeIssue(input: {issueId: "x"}) { clientMutationId } }'`,
			want:    guard.Refuse,
		},
		{
			name:    "mutation after a string holding an escaped quote",
			command: `gh api graphql -f query='{ a(x: "\")") } mutation { closeIssue(input: {issueId: "x"}) { clientMutationId } }'`,
			want:    guard.Refuse,
		},
	})
}

func TestJudge_RefusesAGhGraphqlDocumentTheGuardCannotReadSayingToPassItInline(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "query read from a file", command: "gh api graphql -F query=@q.graphql", want: guard.Refuse, wantNamed: "inline"},
		{name: "query not a literal", command: `gh api graphql -f query="$q"`, want: guard.Refuse, wantNamed: "inline"},
		{name: "body from --input", command: "gh api graphql --input q.json", want: guard.Refuse, wantNamed: "inline"},
		{
			name:      "word not a literal that may be a field flag",
			command:   `gh api graphql -f query='{ viewer { login } }' --f"$x" y`,
			want:      guard.Refuse,
			wantNamed: "inline",
		},
		{name: "fields with no query", command: "gh api graphql -F n=3", want: guard.Refuse, wantNamed: "inline"},
		{
			name:      "field name not a literal beside a query",
			command:   `gh api graphql -f query='{ viewer { login } }' -f q"$n"`,
			want:      guard.Refuse,
			wantNamed: "inline",
		},
	})
}

func TestJudge_AllowsAGhGraphqlQueryWithAnExplicitPostInAnySpelling(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "short method separate", command: `gh api graphql -X POST -f query='{ viewer { login } }'`, want: guard.Allow},
		// cspell:ignore XPOST -- gh reads -X with its value attached; the test needs that spelling.
		{name: "short method attached", command: `gh api graphql -XPOST -f query='{ viewer { login } }'`, want: guard.Allow},
		{name: "long method separate in lower case", command: `gh api graphql --method post -f query='{ viewer { login } }'`, want: guard.Allow},
		{name: "long method attached", command: `gh api graphql --method=POST -f query='{ viewer { login } }'`, want: guard.Allow},
		{name: "short method separate before the endpoint", command: `gh api -X POST graphql -f query='{ viewer { login } }'`, want: guard.Allow},
		{name: "long method separate before the endpoint", command: `gh api --method POST graphql -f query='{ viewer { login } }'`, want: guard.Allow},
	})
}

func TestJudge_RefusesAGhGraphqlCallWithAnExplicitPostByItsDocument(t *testing.T) {
	t.Parallel()
	caller := forgeCaller(t)

	tests := []struct {
		name    string
		command string
		named   string
	}{
		{name: "mutation", command: `gh api graphql -X POST -f query='mutation { a }'`, named: "mutation"},
		{
			name:    "mutation after a long method separate before the endpoint",
			command: `gh api --method POST graphql -f query='mutation { a }'`,
			named:   "mutation",
		},
		{name: "query read from a file", command: "gh api graphql --method=POST -F query=@q.graphql", named: "inline"},
		{name: "query not a literal", command: `gh api graphql -X POST -f query="$q"`, named: "inline"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checkRefusedOmitting(t, caller, omitting{command: tt.command, named: tt.named, omitted: "POST"})
		})
	}
}

func TestJudge_ReadsTheIlsCommandAsCobraFindsIt(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "unknown root flag takes the next word", command: "ils --force 0.6.0 release", want: guard.Ask},
		{name: "unknown short root flag takes the next word", command: "ils -x status merge", want: guard.Ask},
		{name: "root switches take no word", command: "ils -d --json --no-color merge", want: guard.Ask},
		{name: "attached value takes no word", command: "ils --json=true merge", want: guard.Ask},
	})
}

func TestJudge_RefusesAPushOrBranchNotRunAloneByItsBareNameNamingIt(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
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

func TestJudge_RefusesAGhApiCallThatWritesToGitHub(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "api method separate", command: "gh api -X POST repos/x/y/issues", want: guard.Refuse, wantNamed: "POST"},
		{name: "api long method separate", command: "gh api --method DELETE repos/x/y", want: guard.Refuse, wantNamed: "DELETE"},
		// cspell:ignore XPUT -- gh reads -X with its value attached; the test needs that spelling.
		{name: "api method attached", command: "gh api -XPUT repos/x/y", want: guard.Refuse, wantNamed: "PUT"},
		{name: "api long method attached in lower case", command: "gh api --method=put repos/x/y", want: guard.Refuse, wantNamed: "put"},
		{name: "api method not literal", command: `gh api -X "$method" repos/x/y`, want: guard.Refuse},
		{name: "api graphql query with a method other than post", command: `gh api graphql -X DELETE -f query='{ viewer { login } }'`, want: guard.Refuse, wantNamed: "DELETE"},
		{name: "api post with a query field to another endpoint", command: `gh api -X POST repos/x/y/issues -f query='{ viewer { login } }'`, want: guard.Refuse, wantNamed: "POST"},
		{
			name:      "api long post separate before another endpoint with a query field",
			command:   `gh api --method POST repos/x/y/issues -f query='{ viewer { login } }'`,
			want:      guard.Refuse,
			wantNamed: "POST",
		},
		{name: "api method flag as the value of a field", command: "gh api -f --method=GET -f title=x repos/x/y/issues", want: guard.Refuse},
		{name: "api method flag as the value of a long valued flag", command: "gh api --jq --method=GET -f title=x repos/x/y/issues", want: guard.Refuse},
	})
}

func TestJudge_RefusesAGhPrCheckoutThatDiscardsWork(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "pr checkout short force", command: "gh pr checkout 12 -f", want: guard.Refuse},
	})
}

func TestJudge_RefusesAGhConfigGetThatPrintsTheToken(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "config get token", command: "gh config get oauth_token", want: guard.Refuse},
	})
}

func TestJudge_AllowsAGhApiCallThatWritesNothingToGitHub(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "api read", command: "gh api repos/x/y", want: guard.Allow},
		{name: "api explicit get in lower case attached", command: "gh api --method=get search/issues -f q=1", want: guard.Allow},
	})
}

func TestJudge_AllowsAGhPrCheckoutThatKeepsWork(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "pr checkout", command: "gh pr checkout 12", want: guard.Allow},
	})
}

func TestJudge_AllowsAGhConfigGetOfAnotherKey(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "config get another key", command: "gh config get editor", want: guard.Allow},
	})
}

func TestJudge_ReadsGhsCommandPastItsValueFlags(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "short repo before merge", command: "gh pr -R o/r merge 3", want: guard.Ask},
	})
}

func TestJudge_AllowsChangingGhAliases(t *testing.T) {
	t.Parallel()

	checkJudged(t, forgeCaller(t), []judged{
		{name: "set", command: "gh alias set co 'pr checkout'", want: guard.Allow},
		{name: "import", command: "gh alias import aliases.yml", want: guard.Allow},
		{name: "delete", command: "gh alias delete co", want: guard.Allow},
	})
}
