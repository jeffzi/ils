//go:build unix

package guard_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
)

func TestJudge_WhenSeveralInvocationsRefuse_NamesTheFirst(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkJudgedRow(t, caller, judged{command: "git clean -f; git branch -D x", want: guard.Refuse, wantNamed: "git clean"})
}

func TestJudge_WhenTheCommandCannotBeParsed_RefusesNamingWhatFailed(t *testing.T) {
	t.Parallel()
	const command = "echo ok; rm -rf / )"
	caller := readerCaller()
	_, err := guard.Parse(t.Context(), command, caller)
	failure, ok := errors.AsType[*guard.ParseError](err)
	assert.Assert(t, ok, "Parse(%q) error = %v, want a *guard.ParseError", command, err)

	got := guard.Judge(t.Context(), command, caller)

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Contains(got.Reason, failure.Error()), "Judge(%q) reason", command)
}

func TestJudge_WhenAMarkNamesNothing_RefusesWithItsKindAsTheReason(t *testing.T) {
	t.Parallel()
	const command = "echo @(a|b)"

	got := guard.Judge(t.Context(), command, readerCaller())

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Equal(got.Reason, string(guard.ExtendedGlob)), "Judge(%q) reason", command)
}

func TestJudge_WhenAMarkNamesWhatItFound_RefusesNamingItBesideTheKind(t *testing.T) {
	t.Parallel()
	const command = "export GIT_CONFIG_GLOBAL=/tmp/c"

	got := guard.Judge(t.Context(), command, readerCaller())

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Contains(got.Reason, string(guard.GitVariable)), "Judge(%q) reason", command)
	assert.Check(t, is.Contains(got.Reason, "GIT_CONFIG_GLOBAL"), "Judge(%q) reason", command)
}

func TestJudge_WhenAToolWordIsHandedToAnotherCommand_RefusesNamingBoth(t *testing.T) {
	t.Parallel()
	callerDir := t.TempDir()
	writeExecutable(t, filepath.Join(callerDir, "bin", "git"))
	const command = "xargs ./bin/git push"

	got := guard.Judge(t.Context(), command, guard.Caller{Home: home, Dir: callerDir})

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Contains(got.Reason, "xargs"), "Judge(%q) reason", command)
	assert.Check(t, is.Contains(got.Reason, "./bin/git"), "Judge(%q) reason", command)
}

func TestJudge_WhenNoInvocationRunsATool_AllowsWithoutStartingGit(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	caller, recorder := recordingCaller(f, f.Dir())
	const command = "ls -la | grep x; echo git push"

	got := guard.Judge(t.Context(), command, caller)

	assert.Check(t, is.DeepEqual(got, guard.Verdict{Outcome: guard.Allow}), "Judge(%q)", command)
	assert.Check(t, is.Len(recorder.Calls(), 0), "Judge(%q) git calls", command)
}

func TestJudge_AllowsEverydayCommandsOutsideAnyRepository(t *testing.T) {
	t.Parallel()

	caller := guard.Caller{Home: home, Dir: outsideDir(t)}

	checkAllowed(t, caller, []namedCommand{
		{name: "non-literal argument to a command the guard does not know", command: `sed -n "${n}p" "$f"`},
		{
			name:    "heredoc that mentions git on an interpreter's stdin",
			command: "python3 - <<'EOF'\ns = s.replace(\"git reset --hard\", \"git restore\")\nEOF",
		},
		{name: "git history written to /dev/null", command: "git show HEAD:f > /dev/null"},
		{name: "stash list in a pipe", command: "git stash list | head -3"},
		{name: "zsh null-glob qualifier", command: `rm -f "$d"/x*(N)`},
		{name: "search for a tool word", command: "rg -n git docs"},
		{name: "literal sh string behind xargs", command: "xargs -I{} sh -c 'ls {}'"},
		{name: "sed substitution whose text parses as a shell script", command: "sed 's/old text/git push/' f"},
		{name: "python code mentioning git, behind uv run", command: `uv run python -c 'import subprocess; subprocess.run(["git", "log"])'`},
		{name: "environment file sourced under allexport", command: "set -a && . ./.env && set +a && make deploy"},
		{name: "script run through a variable the command assigned", command: "S=/tmp/work; mkdir -p $S && cd $S && $S/run.sh"},
		{name: "redirected call of a function wrapping a test runner", command: `T() { uv run pytest -q "$@"; }; T -k x > out.txt`},
		{name: "function declared and called in one and list", command: `mk() { echo "$1"; } && mk a && mk b`},
	})
}

func TestJudge_WhenAGitReadFails_RefusesSayingWhatBroke(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := aliased(t)
	caller := callerFailingOn(t, f, f.Dir(), "config")

	checkJudgedRow(t, caller, judged{command: "git co topic", want: guard.Refuse, wantNamed: gittest.FailedSaid})
}

func TestJudge_WhenAWordCannotBeRead_RefusesNamingASpellingTheGuardAllows(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "variable as the command word", command: `"$c" status`, wantNamed: "as a literal, or assign its variable a literal"},
		{name: "command substitution as the command word", command: `$(which git) status`, wantNamed: "as a literal, or assign its variable a literal"},
		{name: "shell string", command: `bash -c "$s"`, wantNamed: "write the string as a literal"},
		{name: "git subcommand", command: `git "$s"`, wantNamed: "write the subcommand as a literal"},
	})
}

func TestJudge_WhenAShellStringBehindAnotherCommandCannotBeRead_NamesALiteralString(t *testing.T) {
	t.Parallel()

	checkRefusedOmitting(t, `xargs bash -c "$s"`, "write the -c string as a literal", "on its own")
}

// checkRefusedOmitting fails unless Judge refuses command with a reason that holds named and does
// not hold omitted.
func checkRefusedOmitting(t *testing.T, command, named, omitted string) {
	t.Helper()

	_, caller := standingOn(t, "main", "topic")

	got := guard.Judge(t.Context(), command, caller)

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome, reason %q", command, got.Reason)
	assert.Check(t, is.Contains(got.Reason, named), "Judge(%q) reason", command)
	assert.Check(t, !strings.Contains(got.Reason, omitted), "Judge(%q) reason %q holds %q", command, got.Reason, omitted)
}

func TestJudge_WhenAGitArgumentCannotBeRead_NamesDoubleDashWhereItLetsTheCommandRun(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "branch -d", command: `git branch -d "$b"`, wantNamed: "after `--`"},
		{name: "switch", command: `git switch "$b"`, wantNamed: "after `--`"},
		{name: "add", command: `git add "$f"`, wantNamed: "after `--`"},
		{name: "clean -n", command: `git clean -n "$f"`, wantNamed: "after `--`"},
		{name: "commit", command: `git commit -m msg "$f"`, wantNamed: "after `--`"},
		{name: "restore --staged", command: `git restore --staged "$f"`, wantNamed: "after `--`"},
		{name: "several unreadable arguments", command: `git branch -d "$a" "$b"`, wantNamed: "after `--`"},
	})
}

func TestJudge_WhenDoubleDashLeavesAnUnreadableGitArgumentRefused_NamesALiteral(t *testing.T) {
	t.Parallel()

	tests := []namedCommand{
		{name: "update-ref", command: `git update-ref "$r" HEAD`},
		{name: "config", command: `git config "$k"`},
		{name: "restore without --staged", command: `git restore "$f"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, caller := standingOn(t, "main", "topic")

			checkJudgedRow(t, caller, judged{command: tt.command, want: guard.Refuse, wantNamed: "write the argument as a literal"})
		})
	}
}

func TestJudge_WhenALiteralStartsSeveralOptionsOneTakingAValue_NamesItToSpellOutInFull(t *testing.T) {
	t.Parallel()
	command := `git branch --s "$b"`
	_, caller := standingOn(t, "main", "topic")

	got := guard.Judge(t.Context(), command, caller)

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome, reason %q", command, got.Reason)
	assert.Check(t, is.Contains(got.Reason, "`--s` in full"), "Judge(%q) reason", command)
}
