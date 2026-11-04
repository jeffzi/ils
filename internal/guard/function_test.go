//go:build unix

package guard_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/guard"
)

func TestParse_WhenASubshellDefinesAFunction_CallsAfterItRunTheOuterBody(t *testing.T) {
	t.Parallel()
	const command = "f() { true; }; (f() { cd sub; }); cd /srv/repo; f; git status"

	got := last(t, command)

	assert.Check(t, is.Equal(got.Dir, "/srv/repo"), "Parse(%q) directory of %v", command, got.Words)
}

func TestJudge_WhenACallCarriesARedirect_JudgesItsBodyUnderTheRedirect(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "git history onto a work tree file", command: "dump() { git show HEAD:README.md; }; dump > README.md", wantNamed: "git show"},
		{name: "onto the head file of the git dir", command: "f() { :; }; f > .git/HEAD; " + commitCommand, wantNamed: "cannot tell"},
	})
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
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")
	trunk, _ := standingOn(t, "main", "main")
	toTrunk := "cd " + quotedPath(trunk.Dir())
	const commits = "f() { git commit -am wip; }; "

	checkJudgedIn(t, caller, []placed{
		{name: "after a switch to the trunk", dir: f.Dir(), command: commits + "git switch main && f"},
		{name: "after a cd into a trunk checkout", dir: f.Dir(), command: commits + toTrunk + " && f"},
		{
			name:    "after an export of a trunk git dir",
			dir:     f.Dir(),
			command: commits + "export GIT_DIR=" + quotedPath(filepath.Join(trunk.Dir(), ".git")) + "; f",
		},
		{name: "inside a subshell", dir: f.Dir(), command: commits + "(" + toTrunk + " && f)"},
		{name: "inside a loop", dir: f.Dir(), command: commits + "for x in a; do " + toTrunk + " && f; done"},
		{name: "inside another function's body", dir: f.Dir(), command: commits + "g() { f; }; " + toTrunk + " && g"},
	}, expectation{outcome: guard.Refuse, named: "main"})
}

func TestJudge_WhenACallRunsABodyOffTheTrunk_Allows(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "commit defined and called on a topic branch", command: "f() { git commit -m x; }; f"},
	})
}

func TestJudge_WhenCallsGoPastThoseTheGuardFollows_RefusesNamingTheFunction(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "function that calls itself", command: "f() { f; }; f", wantNamed: "past the calls the guard follows: f"},
	})
}

func TestParse_WhenATopLevelCallGoesPastThoseTheGuardFollows_ReturnsItsMark(t *testing.T) {
	t.Parallel()
	command := "f() { :; }" + strings.Repeat("; f", 257)

	got := parse(t, command)

	assert.Assert(t, len(got) > 0, "Parse(%q) returned nothing", command)
	assert.Check(t, is.DeepEqual(got[len(got)-1].Marks, []guard.Mark{{Kind: guard.DeepCall, Name: "f"}}), "Parse(%q) last marks", command)
}

func TestJudge_RefusesAFunctionThatShadowsACommandTheGuardReadsThrough(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "cd", command: "cd() { builtin cd /srv; }; cd sub && git commit -m x", wantNamed: "cd"},
		{name: "builtin", command: "builtin() { :; }", wantNamed: "builtin"},
	})
}

func TestJudge_AllowsAFunctionThatHidesNoTool(t *testing.T) {
	t.Parallel()

	checkAllowed(t, readerCaller(), []namedCommand{
		{name: "call of a function with no redirect", command: "greet() { echo hi; }; greet"},
	})
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
			name:    "unset in the body of an earlier export",
			command: "f() { unset GIT_DIR; }; export GIT_DIR=/home/reader/repo/.git; f; git status",
			same:    "export GIT_DIR=/home/reader/repo/.git; unset GIT_DIR; git status",
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

func TestParse_WhenEachFunctionCallsThePreviousTwice_ReturnsWithoutExpandingEveryCall(t *testing.T) {
	t.Parallel()
	const depth = 30
	var command strings.Builder
	command.WriteString("f0() { :; }; ")
	for i := 1; i <= depth; i++ {
		fmt.Fprintf(&command, "f%d() { f%d; f%d; }; ", i, i-1, i-1)
	}
	fmt.Fprintf(&command, "f%d; git status", depth)
	cmd := command.String()

	got, err := guard.Parse(t.Context(), cmd, readerCaller())

	assert.NilError(t, err, "Parse(%q)", cmd)
	assert.Assert(t, len(got) > 0, "Parse(%q) holds no invocation", cmd)
	assert.Check(t, is.Equal(got[len(got)-1].Tool, guard.Git), "Parse(%q) last invocation", cmd)
}
