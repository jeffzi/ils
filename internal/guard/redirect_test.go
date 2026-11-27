package guard_test

import (
	"path/filepath"
	"testing"

	"github.com/jeffzi/ils/internal/guard"
)

func TestJudge_WhenGitHistoryGoesToAWorkTreeFileThroughStdout_RefusesNamingTheFile(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")

	checkJudgedIn(t, caller, []placed{
		{name: "truncate", command: "git show HEAD:f > f"},
		{name: "stdout and stderr", command: "git show HEAD:f &> f"},
		{name: "read-write on descriptor one", command: "git show HEAD:f 1<> f"},
		{name: "duplicate onto a file name", command: "git show HEAD:f >& f"},
		{name: "last stdout redirect wins", command: "git show HEAD:f > g > f"},
		{name: "redirect on an enclosing group", command: "{ git show HEAD:f; } > f"},
		{name: "stdout onto stderr after stderr goes to a file", command: "git show HEAD:f 2> f >&2"},
		{name: "group inside a redirected subshell", command: "({ git show HEAD:f; }) > f"},
		{name: "pipe into tee inside a group redirected away", command: "{ git show HEAD:f | tee f; } > /dev/null"},
		{name: "last stage of a pipe inside a redirected group", command: "{ x | git show HEAD:f; } > f"},
	}, expectation{outcome: guard.Refuse, named: filepath.Join(f.Dir(), "f")})
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
		{name: "first stage of a pipe inside a function called with a redirect", command: "dump() { git show HEAD:x | wc -l; }; dump > f"},
	})
}
