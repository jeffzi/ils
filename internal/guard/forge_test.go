//go:build unix

package guard_test

import (
	"maps"
	"path/filepath"
	"slices"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
)

func TestJudge_AsksBeforeALandingCommandRunAloneByItsBareName(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "ils merge", command: "ils merge", want: guard.Ask},
		{name: "gh pr merge", command: "gh pr merge 3 --squash", want: guard.Ask},
		{name: "help flag after an argument", command: "gh pr merge 1 --help", want: guard.Ask},
		{name: "argument not a literal after the end of flags", command: `gh pr merge -- "$n"`, want: guard.Ask},
	})
}

func TestJudge_AllowsALandingCommandAskedForItsHelpInAnyForm(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "ils merge piped", command: "ils merge --help | head", want: guard.Allow},
		{name: "gh pr merge in a subshell", command: "(gh pr merge --help)", want: guard.Allow},
	})
}

func TestJudge_WhenAGhArgumentIsNotALiteralThatMayBeAGuardedFlag_RefusesNamingDoubleDash(t *testing.T) {
	t.Parallel()
	command := `gh pr merge "$n"`

	got := guard.Judge(t.Context(), command, callerOutsideAnyRepository(t))

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome, reason %q", command, got.Reason)
	assert.Check(t, is.Contains(got.Reason, "`--`"), "Judge(%q) reason", command)
}

func TestJudge_WhenDoubleDashLeavesAGhArgumentThatMayBeAGuardedFlagRefused_NamesOnlyALiteral(t *testing.T) {
	t.Parallel()
	caller := callerOutsideAnyRepository(t)

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

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "ils merge piped", command: "ils merge | cat", want: guard.Refuse, wantNamed: "ils merge"},
		{name: "ils release in the background", command: "ils release 0.6.0 &", want: guard.Refuse, wantNamed: "ils release"},
		{name: "gh pr merge redirected", command: "gh pr merge 3 > out.txt", want: guard.Refuse, wantNamed: "gh pr merge"},
		{name: "gh release create behind a backslash", command: `\gh release create v0.6.0`, want: guard.Refuse, wantNamed: "gh release create"},
	})
}

func TestJudge_RefusesAnAdminMergeInEveryForm(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "bare", command: "gh pr merge 3 --admin", want: guard.Refuse},
		{name: "with a value attached", command: "gh pr merge 3 --admin=true", want: guard.Refuse},
	})
}

func TestJudge_RefusesAGuardedGhFlagWhoseAttachedValueIsNotALiteral(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "pr checkout force", command: `gh pr checkout 1 --force="$v"`, want: guard.Refuse, wantNamed: "literal"},
		{name: "api input", command: `gh api repos/o/r/pulls --input="$f"`, want: guard.Refuse},
	})
}

func TestJudge_PassesOverANonLiteralValueThatCannotSetAGuardedGhFlag(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "prefix rules out the guarded flag", command: `gh pr checkout 1 --branch="$b"`, want: guard.Allow},
		{name: "prefix rules out the api fields", command: `gh api repos/o/r --jq="$q"`, want: guard.Allow},
	})
}

func TestJudge_AllowsAGhGraphqlQuery(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
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

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
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

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
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

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "short method separate", command: `gh api graphql -X POST -f query='{ viewer { login } }'`, want: guard.Allow},
		{name: "long method separate in lower case", command: `gh api graphql --method post -f query='{ viewer { login } }'`, want: guard.Allow},
		{name: "long method separate before the endpoint", command: `gh api --method POST graphql -f query='{ viewer { login } }'`, want: guard.Allow},
	})
}

func TestJudge_RefusesAGhGraphqlCallWithAnExplicitPostByItsDocument(t *testing.T) {
	t.Parallel()
	caller := callerOutsideAnyRepository(t)

	tests := []struct {
		name    string
		command string
		named   string
	}{
		{name: "mutation", command: `gh api graphql -X POST -f query='mutation { a }'`, named: "mutation"},
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

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "unknown root flag takes the next word", command: "ils --force 0.6.0 release", want: guard.Ask},
		{name: "root switches take no word", command: "ils -d --json --no-color merge", want: guard.Ask},
		{name: "attached value takes no word", command: "ils --json=true merge", want: guard.Ask},
	})
}

func TestJudge_RefusesABranchNotRunAloneByItsBareNameNamingIt(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "in a list", command: "ils branch topic && true", want: guard.Refuse, wantNamed: "ils branch"},
	})
}

func TestJudge_AllowsAnyOtherIlsCommandInAnyForm(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "status piped", command: "ils status | cat", want: guard.Allow},
		{name: "flag alone", command: "ils --version && true", want: guard.Allow},
	})
}

func TestJudge_RefusesAGhApiCallThatWritesToGitHub(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "api method separate", command: "gh api -X POST repos/x/y/issues", want: guard.Refuse, wantNamed: "POST"},
		{name: "api long method separate", command: "gh api --method DELETE repos/x/y", want: guard.Refuse, wantNamed: "DELETE"},
		// cspell:ignore XPUT -- gh reads -X with its value attached; the test needs that spelling.
		{name: "api method attached", command: "gh api -XPUT repos/x/y", want: guard.Refuse, wantNamed: "PUT"},
		{name: "api long method attached in lower case", command: "gh api --method=put repos/x/y", want: guard.Refuse, wantNamed: "put"},
		{name: "api method not literal", command: `gh api -X "$method" repos/x/y`, want: guard.Refuse},
		{name: "api graphql query with a method other than post", command: `gh api graphql -X DELETE -f query='{ viewer { login } }'`, want: guard.Refuse, wantNamed: "DELETE"},
		{name: "api method flag as the value of a field", command: "gh api -f --method=GET -f title=x repos/x/y/issues", want: guard.Refuse},
		{name: "api method flag as the value of a long valued flag", command: "gh api --jq --method=GET -f title=x repos/x/y/issues", want: guard.Refuse},
	})
}

func TestJudge_RefusesAGhPrCheckoutThatDiscardsWork(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "pr checkout short force", command: "gh pr checkout 12 -f", want: guard.Refuse},
	})
}

func TestJudge_GivesAGhBuiltInAliasTheVerdictOfTheCommandItStandsFor(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "pr co with force", command: "gh pr co --force 12", want: guard.Refuse},
		{name: "release new", command: "gh release new v1.2.3", want: guard.Ask},
		{name: "repo new with push", command: "gh repo new x --source=. --push", want: guard.Refuse},
	})
}

func TestJudge_RefusesAGhConfigGetThatPrintsTheToken(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "config get token", command: "gh config get oauth_token", want: guard.Refuse},
	})
}

func TestJudge_AllowsAGhApiCallThatWritesNothingToGitHub(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "api explicit get in lower case attached", command: "gh api --method=get search/issues -f q=1", want: guard.Allow},
	})
}

func TestJudge_AllowsAGhConfigGetOfAnotherKey(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "config get another key", command: "gh config get editor", want: guard.Allow},
	})
}

func TestJudge_ReadsGhsCommandPastItsValueFlags(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "short repo before merge", command: "gh pr -R o/r merge 3", want: guard.Ask},
	})
}

// pushCommands are the forms of `ils push` the guard judges by the visibility of origin.
func pushCommands() []string {
	return []string{"ils push", "ils push --wait"}
}

func explicitlyAllowed() guard.Verdict {
	return guard.Verdict{Outcome: guard.Allow, Explicit: true}
}

func visibilityArgs(host string) []string {
	return []string{"repo", "view", host + "/" + originRepo, "--json", "visibility"}
}

func visibilityAnswer(visibility string) ghtest.Answer {
	return ghtest.Answer{Stdout: `{"visibility":"` + visibility + `"}` + "\n"}
}

// seenAs scripts, for the rest of the test, a gh logged in to host that reads originRepo there
// as visibility. It sets GH_PATH for the whole process, so the test is not parallel.
func seenAs(t *testing.T, host, visibility string) {
	t.Helper()

	ghtest.Setup(t,
		ghtest.Exits(ghtest.LoggedIn(host), ghtest.LoginArgs(host)...),
		ghtest.Exits(visibilityAnswer(visibility), visibilityArgs(host)...),
	)
}

// pushingTo returns a caller working on a branch of a repository whose push URL names originRepo
// on host.
func pushingTo(t *testing.T, host string) guard.Caller {
	t.Helper()

	f, caller := standingOn(t, "main", "topic")
	setGitHubPushURL(f, host)

	return caller
}

// checkPushesJudged fails unless Judge, run by caller, gives each of pushCommands the outcome of
// want, with a reason holding every one of named.
func checkPushesJudged(t *testing.T, caller guard.Caller, want guard.Outcome, named ...string) {
	t.Helper()

	for _, command := range pushCommands() {
		got := guard.Judge(t.Context(), command, caller)

		checkVerdict(t, judged{command: command, want: want}, got, named...)
	}
}

// checkPushesAllowedExplicitly fails unless Judge, run by caller, allows each of pushCommands on
// its own word.
func checkPushesAllowedExplicitly(t *testing.T, caller guard.Caller) {
	t.Helper()

	for _, command := range pushCommands() {
		got := guard.Judge(t.Context(), command, caller)

		assert.Check(t, is.DeepEqual(got, explicitlyAllowed()), "Judge(%q)", command)
	}
}

func TestJudge_WhenOriginIsPublic_AllowsAnIlsPushExplicitly(t *testing.T) {
	// Not parallel: seenAs sets GH_PATH for the whole process.
	caller := pushingTo(t, githubHost)
	seenAs(t, githubHost, "PUBLIC")

	checkPushesAllowedExplicitly(t, caller)
}

func TestJudge_WhenOriginIsNotPublic_AsksBeforeAnIlsPushSayingItIsPrivate(t *testing.T) {
	for _, visibility := range []string{"PRIVATE", "INTERNAL"} {
		t.Run(visibility, func(t *testing.T) {
			// Not parallel: seenAs sets GH_PATH for the whole process.
			caller := pushingTo(t, githubHost)
			seenAs(t, githubHost, visibility)

			checkPushesJudged(t, caller, guard.Ask, "private")
		})
	}
}

func TestJudge_WhenOriginsVisibilityCannotBeRead_AsksBeforeAnIlsPushSayingWhy(t *testing.T) {
	tests := []struct {
		setup func(t *testing.T)
		name  string
		named string
	}{
		{name: "gh missing", setup: withoutGh, named: "https://cli.github.com"},
		{
			name: "no login for the host",
			setup: func(t *testing.T) {
				t.Helper()
				ghtest.Setup(t,
					ghtest.Exits(ghtest.LoggedOut(), ghtest.LoginArgs(enterpriseHost)...),
					ghtest.Exits(ghtest.LoggedIn(githubHost), ghtest.LoginArgs(githubHost)...),
					ghtest.Exits(visibilityAnswer("PUBLIC"), visibilityArgs(enterpriseHost)...),
				)
			},
			named: enterpriseHost,
		},
		{
			name: "failed read",
			setup: func(t *testing.T) {
				t.Helper()
				ghtest.Setup(t,
					ghtest.Exits(ghtest.LoggedIn(enterpriseHost), ghtest.LoginArgs(enterpriseHost)...),
					ghtest.Exits(ghtest.ServerError(), visibilityArgs(enterpriseHost)...),
				)
			},
			named: "HTTP 500",
		},
		{
			name: "reply that does not decode",
			setup: func(t *testing.T) {
				t.Helper()
				ghtest.Setup(t,
					ghtest.Exits(ghtest.LoggedIn(enterpriseHost), ghtest.LoginArgs(enterpriseHost)...),
					ghtest.Says(`{"visibility":`, visibilityArgs(enterpriseHost)...),
				)
			},
			named: "repo view",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: the setup sets GH_PATH for the whole process.
			caller := pushingTo(t, enterpriseHost)
			tt.setup(t)

			checkPushesJudged(t, caller, guard.Ask, tt.named)
		})
	}
}

func TestJudge_ReadsOriginsVisibilityWithTheGhConfigTheIlsPushSets(t *testing.T) {
	routes := map[string][]string{"login": ghtest.LoginArgs(githubHost), "view": visibilityArgs(githubHost)}
	seen := func(visibility string) map[string]ghtest.Answer {
		return map[string]ghtest.Answer{"login": ghtest.LoggedIn(githubHost), "view": visibilityAnswer(visibility)}
	}
	tests := []struct {
		name    string
		process string
		command string
		want    guard.Verdict
	}{
		{name: "public to the command's config", process: "PRIVATE", command: "PUBLIC", want: explicitlyAllowed()},
		{name: "private to the command's config", process: "PUBLIC", command: "PRIVATE", want: guard.Verdict{Outcome: guard.Ask}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: ghReadingConfig sets GH_PATH and the gh config location for the whole process.
			caller := pushingTo(t, githubHost)
			ghReadingConfig(t, routes, seen(tt.process))
			caller.Env = maps.Clone(caller.Env)
			if caller.Env == nil {
				caller.Env = map[string]string{}
			}
			caller.Env["GH_CONFIG_DIR"] = configHolding(t, "", seen(tt.command))
			command := "ils push"

			got := guard.Judge(t.Context(), command, caller)

			assert.Check(t, is.Equal(got.Outcome, tt.want.Outcome), "Judge(%q) outcome, reason %q", command, got.Reason)
			assert.Check(t, is.Equal(got.Explicit, tt.want.Explicit), "Judge(%q) explicit", command)
		})
	}
}

func TestJudge_RefusesAnIlsPushNotRunAloneByItsBareNameNamingIt(t *testing.T) {
	t.Parallel()

	checkJudged(t, callerOutsideAnyRepository(t), []judged{
		{name: "piped", command: "ils push | cat", want: guard.Refuse, wantNamed: "ils push"},
		{name: "in an and list", command: "ils push && true", want: guard.Refuse, wantNamed: "ils push"},
	})
}

func TestJudge_WhenAWholeIlsPushHasAnotherArgument_LeavesItToThePermissionRules(t *testing.T) {
	t.Parallel()
	command := "ils push --json"

	got := guard.Judge(t.Context(), command, callerOutsideAnyRepository(t))

	assert.Check(t, is.DeepEqual(got, guard.Verdict{Outcome: guard.Allow}), "Judge(%q)", command)
}

func TestJudge_WhenTheGuardCannotTellWhereAnIlsPushRuns_AsksSayingSo(t *testing.T) {
	t.Parallel()

	got := guard.Judge(t.Context(), "ils push", guard.Caller{Home: home})

	checkVerdict(t, judged{command: "ils push", want: guard.Ask}, got, "cannot tell where the command runs")
}

func TestJudge_ReadsTheVisibilityOfTheRepositoryWhoseGitDirTheIlsPushRunsWith(t *testing.T) {
	// Not parallel: ghtest.Setup sets GH_PATH for the whole process.
	here := pushingTo(t, githubHost)
	there := gittest.New(t, "main")
	setGitHubPushURL(there, enterpriseHost)
	thereGitDir := filepath.Join(there.Dir(), ".git")
	ghtest.Setup(t,
		ghtest.Exits(ghtest.LoggedIn(githubHost), ghtest.LoginArgs(githubHost)...),
		ghtest.Exits(visibilityAnswer("PUBLIC"), visibilityArgs(githubHost)...),
		ghtest.Exits(ghtest.LoggedIn(enterpriseHost), ghtest.LoginArgs(enterpriseHost)...),
		ghtest.Exits(visibilityAnswer("PRIVATE"), visibilityArgs(enterpriseHost)...),
	)
	tests := []struct {
		caller func() guard.Caller
		name   string
		named  string
		want   guard.Outcome
	}{
		{
			name: "set by the command's environment",
			caller: func() guard.Caller {
				caller := here
				caller.Env = map[string]string{"GIT_DIR": thereGitDir}
				return caller
			},
			want: guard.Ask, named: enterpriseHost,
		},
		{
			name: "set only in the guard's own git environment",
			caller: func() guard.Caller {
				caller := here
				caller.Git.Env = append(slices.Clone(caller.Git.Env), "GIT_DIR="+thereGitDir)
				return caller
			},
			want: guard.Allow,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := guard.Judge(t.Context(), "ils push", tt.caller())

			checkVerdict(t, judged{command: "ils push", want: tt.want}, got, tt.named)
		})
	}
}

func TestJudge_WhenOriginNamesNoRepository_AllowsAnIlsPushExplicitly(t *testing.T) {
	tests := []struct {
		arrange func(t *testing.T, f *gittest.Fixture) string
		name    string
	}{
		{
			name:    "no origin",
			arrange: func(_ *testing.T, f *gittest.Fixture) string { f.RemoveOrigin(); return f.Dir() },
		},
		{
			name: "push URL a local path",
			arrange: func(_ *testing.T, f *gittest.Fixture) string {
				f.SetPushURL(filepath.ToSlash(f.OriginDir()))
				return f.Dir()
			},
		},
		{name: "outside any repository", arrange: func(t *testing.T, _ *gittest.Fixture) string { t.Helper(); return outsideDir(t) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: seenAs sets GH_PATH for the whole process.
			f, caller := standingOn(t, "main", "topic")
			seenAs(t, githubHost, "PRIVATE")
			caller.Dir = tt.arrange(t, f)

			checkPushesAllowedExplicitly(t, caller)
		})
	}
}
