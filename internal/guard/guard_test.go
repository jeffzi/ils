package guard_test

import (
	"cmp"
	"errors"
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

	checkVerdict(t, judged{command: command, want: guard.Refuse}, got, failure.Error())
}

func TestJudge_WhenAMarkNamesNothing_RefusesWithItsKindAsTheReason(t *testing.T) {
	t.Parallel()
	const command = "eval eval eval eval git push"

	got := guard.Judge(t.Context(), command, readerCaller())

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Equal(got.Reason, string(guard.DeepScript)), "Judge(%q) reason", command)
}

func TestJudge_WhenAMarkNamesWhatItFound_RefusesNamingItBesideTheKind(t *testing.T) {
	t.Parallel()
	const command = "export GIT_CONFIG_GLOBAL=/tmp/c"

	got := guard.Judge(t.Context(), command, readerCaller())

	checkVerdict(t, judged{command: command, want: guard.Refuse}, got, string(guard.GitVariable), "GIT_CONFIG_GLOBAL")
}

func TestJudge_WhenAToolWordIsHandedToAnotherCommand_RefusesNamingBoth(t *testing.T) {
	t.Parallel()
	const command = "xargs ./bin/git push"

	got := guard.Judge(t.Context(), command, callerBesideGit(t))

	checkVerdict(t, judged{command: command, want: guard.Refuse}, got, "xargs", "./bin/git")
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

	caller := callerOutsideAnyRepository(t)

	checkAllowed(t, caller, []namedCommand{
		{
			name:    "heredoc that mentions git on an interpreter's stdin",
			command: "python3 - <<'EOF'\ns = s.replace(\"git reset --hard\", \"git restore\")\nEOF",
		},
		{name: "stash list in a pipe", command: "git stash list | head -3"},
		{name: "script run through a variable the command assigned", command: "S=/tmp/work; mkdir -p $S && cd $S && $S/run.sh"},
		{name: "redirected call of a function wrapping a test runner", command: `T() { uv run pytest -q "$@"; }; T -k x > out.txt`},
		{name: "function declared and called in one and list", command: `mk() { echo "$1"; } && mk a && mk b`},
	})
}

func TestJudge_WhenAGitReadFails_RefusesSayingWhatBroke(t *testing.T) {
	t.Parallel()

	f := aliased(t)
	caller := callerFailingOn(f, f.Dir(), "config")

	checkJudgedRow(t, caller, judged{command: "git co topic", want: guard.Refuse, wantNamed: gittest.FailedSaid})
}

func TestJudge_WhenAWordCannotBeRead_RefusesNamingASpellingTheGuardAllows(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "variable as the command word", command: `"$c" status`, wantNamed: "as a literal, or assign its variable a literal"},
		{name: "shell string", command: `bash -c "$s"`, wantNamed: "write the string as a literal"},
		{name: "git subcommand", command: `git "$s"`, wantNamed: "write the subcommand as a literal"},
	})
}

func TestJudge_WhenAShellStringBehindAnotherCommandCannotBeRead_NamesALiteralString(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkJudgedRow(t, caller, judged{command: `xargs bash -c "$s"`, wantNamed: "write the -c string as a literal", want: guard.Refuse})
}

// omitting is a command Judge refuses with a reason that holds named and does not hold omitted.
type omitting struct {
	command string
	named   string
	omitted string
}

// checkRefusedOmitting fails unless Judge, run by caller, refuses the command of tt with a reason
// that holds tt.named and does not hold tt.omitted.
func checkRefusedOmitting(t *testing.T, caller guard.Caller, tt omitting) {
	t.Helper()

	got := guard.Judge(t.Context(), tt.command, caller)

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome, reason %q", tt.command, got.Reason)
	assert.Check(t, is.Contains(got.Reason, tt.named), "Judge(%q) reason", tt.command)
	assert.Check(t, !strings.Contains(got.Reason, tt.omitted), "Judge(%q) reason %q holds %q", tt.command, got.Reason, tt.omitted)
}

func TestJudge_WhenAGitArgumentCannotBeRead_NamesDoubleDashWhereItLetsTheCommandRun(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "branch -d", command: `git branch -d "$b"`, wantNamed: "after `--`"},
		{name: "commit", command: `git commit -m msg "$f"`, wantNamed: "after `--`"},
		{name: "several unreadable arguments", command: `git branch -d "$a" "$b"`, wantNamed: "after `--`"},
		{name: "mv", command: `git mv "$f" b`, wantNamed: "after `--`"},
	})
}

func TestJudge_WhenDoubleDashLeavesAnUnreadableGitArgumentRefused_NamesALiteral(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "update-ref", command: `git update-ref "$r" HEAD`, wantNamed: "write the argument as a literal"},
		{name: "config", command: `git config "$k"`, wantNamed: "write the argument as a literal"},
	})
}

func TestJudge_WhenALiteralStartsSeveralOptionsOneTakingAValue_NamesItToSpellOutInFull(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkJudgedRow(t, caller, judged{command: `git branch --s "$b"`, want: guard.Refuse, wantNamed: "`--s` in full"})
}

// judged is a command, the outcome Judge must give it, and a word its reason must hold.
type judged struct {
	name      string
	command   string
	wantNamed string
	want      guard.Outcome
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

	checkVerdict(t, tt, got, tt.wantNamed)
}

// checkVerdict fails unless got, Judge's verdict on the row's command, has the row's outcome and a
// reason holding every one of named.
func checkVerdict(t *testing.T, tt judged, got guard.Verdict, named ...string) {
	t.Helper()

	assert.Check(t, is.Equal(got.Outcome, tt.want), "Judge(%q) outcome, reason %q", tt.command, got.Reason)

	for _, part := range named {
		assert.Check(t, is.Contains(got.Reason, part), "Judge(%q) reason", tt.command)
	}
}

// eachRow runs check for every row, in a test run named by name.
func eachRow[Row any](t *testing.T, rows []Row, name func(Row) string, check func(t *testing.T, row Row)) {
	t.Helper()

	for _, row := range rows {
		t.Run(name(row), func(t *testing.T) {
			check(t, row)
		})
	}
}

// checkJudged fails unless Judge gives every command its row's outcome, with a reason naming what
// the row names.
func checkJudged(t *testing.T, caller guard.Caller, tests []judged) {
	t.Helper()

	eachRow(t, tests, func(tt judged) string { return tt.name }, func(t *testing.T, tt judged) {
		t.Parallel()

		checkJudgedRow(t, caller, tt)
	})
}

// checkJudgedInTurn is checkJudged for rows that share process environment (the gh GH_PATH
// names), so no row runs in parallel.
func checkJudgedInTurn(t *testing.T, caller guard.Caller, tests []judged) {
	t.Helper()

	eachRow(t, tests, func(tt judged) string { return tt.name }, func(t *testing.T, tt judged) {
		checkJudgedRow(t, caller, tt)
	})
}

// refusal is a command the guard refuses, with the text its reason must hold.
type refusal struct {
	name      string
	command   string
	wantNamed string
}

// checkRefusals fails unless Judge refuses every command naming what its row wants named.
func checkRefusals(t *testing.T, tests []refusal) {
	t.Helper()

	_, caller := standingOn(t, "main", "topic")
	rows := make([]judged, 0, len(tests))
	for _, tt := range tests {
		rows = append(rows, judged{name: tt.name, command: tt.command, wantNamed: tt.wantNamed, want: guard.Refuse})
	}

	checkJudged(t, caller, rows)
}

type namedCommand struct {
	name    string
	command string
}

// checkAllowed fails unless Judge, run by caller, allows every command.
func checkAllowed(t *testing.T, caller guard.Caller, tests []namedCommand) {
	t.Helper()

	rows := make([]judged, 0, len(tests))
	for _, tt := range tests {
		rows = append(rows, judged{name: tt.name, command: tt.command, want: guard.Allow})
	}

	checkJudged(t, caller, rows)
}

type placed struct {
	name    string
	dir     string
	command string
}

// expectation is the outcome Judge must give, with a reason holding named.
type expectation struct {
	named   string
	outcome guard.Outcome
}

// checkJudgedIn fails unless Judge, run by caller from each row's directory, or from the caller's
// own when the row names none, gives the row's command the outcome want states.
func checkJudgedIn(t *testing.T, caller guard.Caller, tests []placed, want expectation) {
	t.Helper()

	eachRow(t, tests, func(tt placed) string { return tt.name }, func(t *testing.T, tt placed) {
		t.Parallel()

		checkPlacedRow(t, caller, tt, want)
	})
}

// checkJudgedInInTurn is checkJudgedIn for rows that share process environment (the gh GH_PATH
// names), so no row runs in parallel.
func checkJudgedInInTurn(t *testing.T, caller guard.Caller, tests []placed, want expectation) {
	t.Helper()

	eachRow(t, tests, func(tt placed) string { return tt.name }, func(t *testing.T, tt placed) {
		checkPlacedRow(t, caller, tt, want)
	})
}

// checkPlacedRow fails unless Judge, run by caller from the row's directory, or from the caller's
// own when the row names none, gives the row's command the outcome want states.
func checkPlacedRow(t *testing.T, caller guard.Caller, tt placed, want expectation) {
	t.Helper()

	in := caller
	in.Dir = cmp.Or(tt.dir, caller.Dir)

	checkJudgedRow(t, in, judged{command: tt.command, wantNamed: want.named, want: want.outcome})
}
