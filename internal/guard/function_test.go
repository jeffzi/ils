//go:build unix

package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

func TestParse_WhenASubshellDefinesAFunction_CallsAfterItRunTheOuterBody(t *testing.T) {
	t.Parallel()
	const command = "f() { true; }; (f() { cd sub; }); cd /srv/repo; f; git status"

	got := last(t, command)

	assert.Check(t, is.Equal(got.Dir, "/srv/repo"), "Parse(%q) directory of %v", command, got.Words)
}

func TestJudge_WhenACallOfAFunctionTheCommandDefinesCarriesARedirect_RefusesNamingTheFunction(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "stdout to a file", command: "dump() { git show HEAD:f; }; dump > f", wantNamed: "dump"},
		{name: "function keyword", command: "function dump { git show HEAD:f; }; dump > f", wantNamed: "dump"},
	})
}

func TestJudge_WhenACallMayRunAnOuterBodyThatLacksItsRedirect_RefusesNamingTheFunction(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "redefined in a subshell", command: "dump() { git show HEAD:f; }; { (dump() { :; }); dump; } > f", wantNamed: "dump"},
		{name: "redefined in an if branch", command: "dump() { git show HEAD:f; }; { if true; then dump() { :; }; fi; dump; } > f", wantNamed: "dump"},
		{name: "redefined in an uncalled function body", command: "dump() { git show HEAD:f; }; { g() { dump() { :; }; }; dump; } > f", wantNamed: "dump"},
		{name: "redefined in an alias value", command: "dump() { git show HEAD:f; }; { alias a='dump() { :; }'; dump; } > f", wantNamed: "dump"},
	})
}

func TestJudge_RefusesAFunctionThatShadowsACommandTheGuardReadsThrough(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "cd", command: "cd() { builtin cd /srv; }; cd sub && git commit -m x", wantNamed: "cd"},
		{name: "builtin", command: "builtin() { :; }", wantNamed: "builtin"},
		{name: "command", command: "command() { :; }", wantNamed: "command"},
		{name: "env", command: "env() { :; }", wantNamed: "env"},
		{name: "nohup", command: "nohup() { :; }", wantNamed: "nohup"},
		{name: "nice", command: "nice() { :; }", wantNamed: "nice"},
		{name: "exec", command: "exec() { :; }", wantNamed: "exec"},
		{name: "function keyword", command: "function nohup { :; }", wantNamed: "nohup"},
	})
}

func TestJudge_AllowsAFunctionOrAliasThatHidesNoTool(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "call of a function with no redirect", command: "greet() { echo hi; }; greet"},
		{name: "alias naming no tool", command: "alias ll='ls -l'; ll"},
		{name: "alias listing", command: "alias"},
		{name: "alias printing one", command: "alias ll"},
	})
}

func TestParse_WhenACallRunsABodyThatSetsAGitVariable_ReportsTheTargetTheBodyLeaves(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		same    string
	}{
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			want := last(t, tt.same).Target

			got := last(t, tt.command)

			assert.Check(t, is.DeepEqual(got.Target, want), "Parse(%q) target, want that of %q", tt.command, tt.same)
		})
	}
}

func TestParse_WhenTwoDefinitionsACallMayRunLeaveAGitVariableDifferently_ReportsTheTargetUnknown(t *testing.T) {
	t.Parallel()
	const command = "f() { export GIT_DIR=/srv/x.git; }; if true; then f() { export GIT_DIR=/srv/y.git; }; fi; f; git status"

	got := last(t, command)

	assert.Check(t, got.Target.Unknown, "Parse(%q) target = %+v, want unknown", command, got.Target)
}
