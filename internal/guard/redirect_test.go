//go:build unix

package guard_test

import (
	"path/filepath"
	"testing"

	"github.com/jeffzi/ils/internal/guard"
)

func TestJudge_WhenGitHistoryGoesToAWorkTreeFileThroughStdout_RefusesNamingTheFile(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")
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
	rows := make([]judged, 0, len(tests))
	for _, tt := range tests {
		rows = append(rows, judged{name: tt.name, command: tt.command, want: guard.Refuse, wantNamed: filepath.Join(f.Dir(), "f")})
	}

	checkJudged(t, caller, rows)
}

func TestJudge_WhenGitHistoryReachesNoFileThroughStdout_AllowsIt(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "stderr only", command: "git show HEAD:f 2> err"},
		{name: "input only", command: "git show HEAD:f < in"},
		{name: "stdout duplicated onto stderr", command: "git show HEAD:f >&2"},
		{name: "read-write on standard input", command: "git show HEAD:f <> f"},
		{name: "stdout onto stderr before stderr goes to a file", command: "git show HEAD:f >&2 2> f"},
		{name: "stdout onto stderr inside a redirected group", command: "{ git show HEAD:f >&2; } > f"},
	})
}

func TestJudge_WhenAnExecWithNoCommandRedirects_AllowsGitHistoryShownAfterIt(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "exec at top level", command: "exec > out.log; git show HEAD:README.md"},
	})
}
