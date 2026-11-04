//go:build unix

package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/guard"
)

func TestInvocation_Stdout_WhenStdoutGoesToAFile_ReturnsItsTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "truncate", command: "git show HEAD:f > f"},
		{name: "stdout and stderr", command: "git show HEAD:f &> f"},
		{name: "read-write on descriptor one", command: "git show HEAD:f 1<> f"},
		{name: "duplicate onto a file name", command: "git show HEAD:f >& f"},
		{name: "last stdout redirect wins", command: "git show HEAD:f > g > f"},
		{name: "redirect on an enclosing group", command: "{ git show HEAD:f; } > f"},
		{name: "stdout onto stderr after stderr goes to a file", command: "git show HEAD:f 2> f >&2"},
		{name: "group inside a redirected subshell", command: "({ git show HEAD:f; }) > f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inv := gitRun(t, tt.command)

			got, ok := inv.Stdout()

			assert.Assert(t, ok, "Parse(%q) reports stdout not redirected", tt.command)
			assert.Check(t, is.DeepEqual(got, guard.Word{Text: "f", Literal: true}), "Parse(%q) stdout", tt.command)
		})
	}
}

func TestInvocation_Stdout_WhenStdoutIsNotRedirectedToAFile_ReportsNone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "no redirect", command: "git show HEAD:f"},
		{name: "stderr only", command: "git show HEAD:f 2> err"},
		{name: "input only", command: "git show HEAD:f < in"},
		{name: "stdout duplicated onto stderr", command: "git show HEAD:f >&2"},
		{name: "read-write on standard input", command: "git show HEAD:f <> f"},
		{name: "stdout onto stderr before stderr goes to a file", command: "git show HEAD:f >&2 2> f"},
		{name: "stdout onto stderr inside a redirected group", command: "{ git show HEAD:f >&2; } > f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inv := gitRun(t, tt.command)

			got, ok := inv.Stdout()

			assert.Check(t, !ok, "Parse(%q) stdout = %+v, want none", tt.command, got)
		})
	}
}

func TestJudge_WhenAnExecWithNoCommandRedirects_AllowsGitHistoryShownAfterIt(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "exec at top level", command: "exec > out.log; git show HEAD:README.md"},
		{name: "exec in an eval string", command: "eval 'exec > out.log'; git show HEAD:README.md"},
		{name: "exec in a called function body", command: "f() { exec > out.log; }; f; git show HEAD:README.md"},
	})
}
