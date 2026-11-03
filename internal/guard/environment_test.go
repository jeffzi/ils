//go:build unix

package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

func TestParse_WhenTheGitTargetCannotBeFollowed_ReportsItUnknown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "env clearing the environment", command: "env -i git status"},
		{name: "env removing GIT_DIR", command: "env -u GIT_DIR git status"},
		{name: "unset of GIT_WORK_TREE", command: "unset GIT_WORK_TREE; git status"},
		{name: "shell variable that may be exported", command: "GIT_DIR=/srv/repo.git; git status"},
		{name: "export in an if body", command: "if test -d x; then export GIT_DIR=/srv/repo.git; fi; git status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := last(t, tt.command)

			assert.Check(t, got.Target.Unknown, "Parse(%q) target = %+v, want unknown", tt.command, got.Target)
		})
	}
}

// sameTarget is a command whose git target must equal that of the command same.
type sameTarget struct {
	name    string
	command string
	same    string
}

// checkSameTarget fails unless the last invocation of every row's command has the git target of
// the last invocation of its same command.
func checkSameTarget(t *testing.T, tests []sameTarget) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			want := last(t, tt.same).Target

			got := last(t, tt.command)

			assert.Check(t, is.DeepEqual(got.Target, want), "Parse(%q) target, want that of %q", tt.command, tt.same)
		})
	}
}

func TestParse_WhenEnvDropsAVariable_ReportsTheTargetOfTheCommandWithoutIt(t *testing.T) {
	t.Parallel()

	checkSameTarget(t, []sameTarget{
		{name: "prefix removed by env -u", command: "GIT_DIR=/a env -u GIT_DIR git status", same: "env -u GIT_DIR git status"},
		{name: "env argument removed by a later env -u", command: "env GIT_DIR=/a env -u GIT_DIR git status", same: "env -u GIT_DIR git status"},
		{name: "shell string under env -u", command: "env -u GIT_DIR bash -c 'git status'", same: "env -u GIT_DIR git status"},
	})
}
