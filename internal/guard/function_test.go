//go:build unix

package guard_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
)

func TestParse_WhenASubshellDefinesAFunction_CallsAfterItRunTheOuterBody(t *testing.T) {
	t.Parallel()
	const command = "f() { true; }; (f() { cd sub; }); cd /srv/repo; f; git status"

	got := last(t, command)

	assert.Check(t, is.Equal(got.Dir, "/srv/repo"), "Parse(%q) directory of %v", command, got.Words)
}

func TestJudge_WhenACallsRedirectLandsWhereTheCallRuns_RefusesGitHistoryTheBodyWritesFromElsewhere(t *testing.T) {
	t.Parallel()

	f, _ := standingOn(t, "main", "topic")
	caller := callerIn(f, filepath.Dir(f.Dir()))
	command := "dump() { cd " + gittest.ShellWord(outsideDir(t)) + " && git -C " + gittest.ShellWord(f.Dir()) + " show HEAD:go.mod; }; cd repo && dump > go.mod"

	checkJudgedRow(t, caller, judged{command: command, want: guard.Refuse, wantNamed: "writes over " + filepath.Join(f.Dir(), "go.mod")})
}

func TestJudge_WhenACallMayRunAnOuterBodyUnderARedirect_JudgesThatBodyUnderTheRedirect(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "redefined in an if branch", command: "dump() { git show HEAD:f; }; { if true; then dump() { :; }; fi; dump; } > f", wantNamed: "git show"},
		{name: "redefined in an uncalled function body", command: "dump() { git show HEAD:f; }; { g() { dump() { :; }; }; dump; } > f", wantNamed: "git show"},
		{name: "redefined in an alias value", command: "dump() { git show HEAD:f; }; { alias a='dump() { :; }'; dump; } > f", wantNamed: "git show"},
	})
}

func TestJudge_WhenACallRunsABodyWhoseCommitLandsOnTheTrunk_RefusesNamingTheTrunk(t *testing.T) {
	// Not parallel: onGitHubCom sets GH_PATH for the whole process.
	f, caller := standingOn(t, "main", "topic")
	trunk, _ := standingOn(t, "main", "main")
	onGitHubCom(t, f, trunk)
	toTrunk := "cd " + gittest.ShellWord(trunk.Dir())
	const commits = "f() { git commit -am wip; }; "

	checkJudgedInInTurn(t, caller, []placed{
		{name: "after a switch to the trunk", command: commits + "git switch main && f"},
		{name: "after a cd into a trunk checkout", command: commits + toTrunk + " && f"},
		{
			name:    "after an export of a trunk git dir",
			command: commits + "export GIT_DIR=" + gittest.ShellWord(filepath.Join(trunk.Dir(), ".git")) + "; f",
		},
		{name: "inside a subshell", command: commits + "(" + toTrunk + " && f)"},
		{name: "inside a loop", command: commits + "for x in a; do " + toTrunk + " && f; done"},
		{name: "inside another function's body", command: commits + "g() { f; }; " + toTrunk + " && g"},
	}, expectation{outcome: guard.Refuse, named: onMain})
}

func TestJudge_WhenACallRunsABodyOffTheTrunk_Allows(t *testing.T) {
	// Not parallel: standingOnGitHub sets GH_PATH for the whole process.
	_, caller := standingOnGitHub(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "commit defined and called on a topic branch", command: "f() { git commit -m x; }; f"},
	})
}

func TestJudge_WhenADeclaredFunctionIsNeverCalled_JudgesWhatFollowsAsIfItWereAbsent(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "body that changes directory", command: "f() { cd sub && make; }; git commit -m x"},
	})
}

func TestJudge_WhenACallRunsABodyThatChangesDirectory_RefusesTheCommitAfterIt(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "relative cd in the body", command: "f() { cd sub; }; f; git commit -m x"},
	})
}

func TestJudge_WhenCallsGoPastThoseTheGuardFollows_RefusesNamingTheFunction(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "function that calls itself", command: "f() { f; }; f", wantNamed: "past the calls the guard follows: f"},
		{name: "chain where each function calls the one before twice", command: doublingChain(30), wantNamed: "past the calls the guard follows: f"},
		{name: "call whose dry runs alone go past the walks", command: "h() { :; }; g() { " + strings.Repeat("h; ", 25) + "}; f() { g; }; f; git status", wantNamed: "past the calls the guard follows: f"},
	})
}

func TestJudge_WhenNestedCallsStayWithinThoseTheGuardFollows_Allows(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "each function calls the next twice", command: "f() { g; g; }; g() { h; h; }; h() { :; }; f; git status"},
	})
}

// doublingChain is a command defining count functions f0, f1, …, each calling the one before
// twice, then calling the last.
func doublingChain(count int) string {
	var command strings.Builder
	command.WriteString("f0() { :; }; ")
	for i := 1; i < count; i++ {
		fmt.Fprintf(&command, "f%d() { f%d; f%d; }; ", i, i-1, i-1)
	}
	fmt.Fprintf(&command, "f%d; git status", count-1)
	return command.String()
}

func TestParse_WhenATopLevelCallGoesPastThoseTheGuardFollows_ReturnsItsMark(t *testing.T) {
	t.Parallel()
	command := "f() { :; }" + strings.Repeat("; f", 257)

	got := parse(t, command)

	assert.Assert(t, len(got) > 0, "Parse(%q) returned nothing", command)
	assert.Check(t, is.DeepEqual(got[len(got)-1].Marks, []guard.Mark{{Kind: guard.DeepCall, Name: "f"}}), "Parse(%q) last marks", command)
}

func TestParse_WhenACallRunsABodyThatSetsAGitVariable_ReportsTheTargetTheBodyLeaves(t *testing.T) {
	t.Parallel()

	checkSameTarget(t, []sameTarget{
		{
			name:    "export in the body over an earlier export",
			command: "f() { export GIT_DIR=/srv/x.git; }; export GIT_DIR=/home/reader/repo/.git; f; git status",
			same:    "export GIT_DIR=/srv/x.git; git status",
		},
		{
			name:    "export in two definitions that agree",
			command: "f() { export GIT_DIR=/srv/x.git; }; if true; then f() { export GIT_DIR=/srv/x.git; }; fi; f; git status",
			same:    "export GIT_DIR=/srv/x.git; git status",
		},
	})
}

func TestParse_WhenTwoDefinitionsACallMayRunLeaveAGitVariableDifferently_ReportsTheTargetUnknown(t *testing.T) {
	t.Parallel()
	const command = "f() { export GIT_DIR=/srv/x.git; }; if true; then f() { export GIT_DIR=/srv/y.git; }; fi; f; git status"

	got := last(t, command)

	assert.Check(t, got.Target.Unknown, "Parse(%q) target = %+v, want unknown", command, got.Target)
}
