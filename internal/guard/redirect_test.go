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
		{name: "earlier exec with no command", command: "exec > f; git show HEAD:f"},
		{name: "stdout onto stderr after stderr goes to a file", command: "git show HEAD:f 2> f >&2"},
		{name: "group inside a redirected subshell", command: "({ git show HEAD:f; }) > f"},
		{name: "earlier exec with no command in a group", command: "{ exec > f; }; git show HEAD:f"},
		{name: "earlier exec in a group with only stderr redirected", command: "{ exec > f; } 2> err; git show HEAD:f"},
		{name: "earlier exec behind command", command: "command exec > f; git show HEAD:f"},
		{name: "earlier exec behind stacked wrappers", command: "command builtin exec > f; git show HEAD:f"},
		{name: "stderr on a file in the then branch only", command: "if c; then exec 2> f 3>&1; else exec 3> g 2>&1; fi; git show HEAD:f >&2"},
		{name: "descriptor three on a file in an if with no else", command: "exec 2> g 3>&1; if c; then exec 3> f 2>&1; fi; git show HEAD:f >&3"},
		{name: "stderr on a file in one case arm only", command: "case x in a) exec 2> f 3>&1;; b) exec 3> g 2>&1;; esac; git show HEAD:f >&2"},
		{name: "stderr on a file before an and list that moves it", command: "exec 2> f 3>&1 && exec 3> g 2>&1; git show HEAD:f >&2"},
		{name: "stderr on a file before a for loop that moves it", command: "exec 2> f 3>&1; for i in $list; do exec 3> g 2>&1; done; git show HEAD:f >&2"},
		{name: "stderr on a file before a function that moves it", command: "exec 2> f 3>&1; h() { exec 3> g 2>&1; }; git show HEAD:f >&2"},
		{name: "exec in a group with stdout redirected keeps its stderr", command: "{ exec 2> f; } > g; git show HEAD:f >&2"},
		{name: "exec in a group with stdout redirected keeps a copy of it", command: "{ exec 3>&1; } > f; git show HEAD:f >&3"},
		{name: "exec in an eval with stdout redirected keeps its stderr", command: "eval 'exec 2> f' > g; git show HEAD:f >&2"},
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
		{name: "earlier exec with no command in a subshell", command: "(exec > f); git show HEAD:f"},
		{name: "later exec with no command", command: "git show HEAD:f; exec > f"},
		{name: "earlier exec in a group with stdout redirected", command: "{ exec > f; } > g; git show HEAD:f"},
		{name: "stdout off a file on both sides of an if", command: "if c; then exec 2> f 3>&1; else exec 3> g 2>&1; fi; git show HEAD:f"},
		{name: "stdout onto a descriptor one branch closes", command: "if c; then exec 3> f; else exec >&3 3>&-; fi; git show HEAD:f"},
		{name: "stdout onto stderr after stderr returns to stdout", command: "if c; then exec 2> f; else exec 2> g; fi; exec 2>&1; git show HEAD:f >&2"},
		{name: "exec in a group whose stdout and stderr both went elsewhere", command: "{ exec 2> f; } &> g; git show HEAD:f >&2"},
		{name: "exec in a group duplicated onto a file name", command: "{ exec 2> f; } >& g; git show HEAD:f >&2"},
		{name: "exec in a group with standard input redirected", command: "{ exec 0> f; } < in; git show HEAD:f >&0"},
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
