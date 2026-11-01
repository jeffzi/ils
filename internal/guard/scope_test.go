//go:build unix

package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/guard"
)

func dirs(invocations []guard.Invocation) []string {
	got := make([]string, 0, len(invocations))
	for _, inv := range invocations {
		got = append(got, inv.Dir)
	}

	return got
}

func TestParse_ReportsTheDirectoryEachInvocationRunsIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{name: "cd to a relative path", command: "cd sub && git status", want: []string{start, start + "/sub"}},
		{name: "cd to an absolute path", command: "cd /srv/repo; git status", want: []string{start, "/srv/repo"}},
		{name: "cd past its options", command: "cd -P -- sub && git status", want: []string{start, start + "/sub"}},
		{name: "subshell move stays inside", command: "(cd sub && git status); git commit", want: []string{start, start + "/sub", start}},
		{
			name:    "substitution move stays inside",
			command: `echo "$(cd sub; git status)"; git commit`,
			want:    []string{start, start, start + "/sub", start},
		},
		{name: "group move holds after the group", command: "{ cd sub; }; git commit", want: []string{start, start + "/sub"}},
		{name: "absolute cd after an unknown one", command: `cd "$d"; cd /srv/repo; git status`, want: []string{start, "", "/srv/repo"}},
		{name: "cd every and-list operand agrees on", command: "cd sub && git status; git commit", want: []string{start, start + "/sub", start + "/sub"}},
		{name: "cd .. after an absolute cd ends a physical walk", command: "cd -P sub; cd /srv/repo; cd ..; git status", want: []string{start, start + "/sub", "/srv/repo", "/srv"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Check(t, is.DeepEqual(dirs(got), tt.want), "Parse(%q) directories", tt.command)
		})
	}
}

func TestParse_WhenTheDirectoryCannotBeFollowed_ReportsItUnknown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		env     map[string]string
		name    string
		command string
	}{
		{name: "popd", command: "pushd ../wt && go test ./... && popd && git commit"},
		{name: "cd to the previous directory", command: "cd - && git commit"},
		{name: "bare cd", command: "cd && git commit"},
		{name: "cd to a non-literal", command: `cd "$dir" && git commit`},
		{name: "relative cd after an unknown one", command: `cd "$dir"; cd sub; git commit`},
		{name: "relative cd with CDPATH in the environment", command: "cd sub && git commit", env: map[string]string{"CDPATH": "/srv"}},
		{name: "relative cd with CDPATH assigned in the command", command: "CDPATH=/srv; cd sub && git commit"},
		{name: "move in an if body", command: "if test -d sub; then cd sub; fi; git commit"},
		{name: "move in a loop body", command: "while true; do cd sub; done; git commit"},
		{name: "move in a case arm", command: "case x in y) cd sub;; esac; git commit"},
		{name: "move in a function body", command: "f() { cd sub; }; f; git commit"},
		{name: "after a move that may have failed", command: "cd sub || git commit"},
		{name: "after an or list that may have moved", command: "true || cd sub; git commit"},
		{name: "after an and list whose right side moved", command: "true && cd sub; git commit"},
		{name: "after an and list of three operands", command: "true && true && cd sub; git commit"},
		{name: "cd -P through a link and back up", command: "cd -P link/..; git commit"},
		{name: "cd .. after cd -P", command: "cd -P sub; cd ..; git commit"},
		{name: "cd .. after cd -P inside eval", command: "eval 'cd -P sub'; cd ..; git commit"},
		{name: "move in a pipeline", command: "true | cd sub; git commit"},
		{name: "relative cd in a shell string given CDPATH by a prefix", command: "CDPATH=/srv bash -c 'cd sub && git commit'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lastWith(t, tt.command, guard.Caller{Home: home, Dir: start, Env: tt.env})

			assert.Check(t, is.Equal(got.Dir, ""), "Parse(%q) directory of %v", tt.command, got.Words)
		})
	}
}

func TestParse_ReportsWhereEachGitInvocationPointsGit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		env     map[string]string
		want    guard.GitTarget
	}{
		{
			name:    "GIT_DIR in the environment",
			command: "git status",
			env:     map[string]string{"GIT_DIR": "/srv/repo.git"},
			want:    guard.GitTarget{Dir: start, GitDir: "/srv/repo.git"},
		},
		{name: "assignment prefix", command: "GIT_DIR=/srv/repo.git git status", want: guard.GitTarget{Dir: start, GitDir: "/srv/repo.git"}},
		{name: "relative work tree", command: "GIT_WORK_TREE=wt git status", want: guard.GitTarget{Dir: start, WorkTree: start + "/wt"}},
		{name: "env argument", command: "env GIT_DIR=.git git status", want: guard.GitTarget{Dir: start, GitDir: start + "/.git"}},
		{name: "export earlier in the list", command: "export GIT_DIR=/srv/repo.git; git status", want: guard.GitTarget{Dir: start, GitDir: "/srv/repo.git"}},
		{
			name:    "relative export read where git runs",
			command: "export GIT_DIR=.git; cd sub; git status",
			want:    guard.GitTarget{Dir: start + "/sub", GitDir: start + "/sub/.git"},
		},
		{name: "prefix of an earlier command", command: "GIT_DIR=/srv/repo.git true; git status", want: guard.GitTarget{Dir: start}},
		{name: "prefix of an earlier eval", command: "GIT_DIR=/srv/repo.git eval true; git status", want: guard.GitTarget{Dir: start}},
		{name: "move in an earlier eval string", command: "GIT_DIR=/srv/repo.git eval 'cd sub'; git status", want: guard.GitTarget{Dir: start + "/sub"}},
		{name: "env removing another name", command: "env -u PAGER git status", want: guard.GitTarget{Dir: start}},
		{name: "-C", command: "git -C sub status", want: guard.GitTarget{Dir: start + "/sub"}},
		{name: "each -C relative to the one before", command: "git -C a -C ../b status", want: guard.GitTarget{Dir: start + "/b"}},
		{
			name:    "relative --git-dir read after every -C",
			command: "git -C a --git-dir=.git -C b status",
			want:    guard.GitTarget{Dir: start + "/a/b", GitDir: start + "/a/b/.git"},
		},
		{
			name:    "--git-dir over GIT_DIR",
			command: "GIT_DIR=/srv/one.git git --git-dir /srv/two.git status",
			want:    guard.GitTarget{Dir: start, GitDir: "/srv/two.git"},
		},
		{name: "--work-tree", command: "git --work-tree wt -C sub status", want: guard.GitTarget{Dir: start + "/sub", WorkTree: start + "/sub/wt"}},
		{name: "non-literal -C", command: `git -C "$d" status`, want: guard.GitTarget{}},
		{name: "non-literal -C then a relative --git-dir", command: `git -C "$d" --git-dir=.git status`, want: guard.GitTarget{Unknown: true}},
		{name: "non-literal GIT_DIR", command: `GIT_DIR="$d" git status`, want: guard.GitTarget{Dir: start, Unknown: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lastWith(t, tt.command, guard.Caller{Home: home, Dir: start, Env: tt.env})

			assert.Check(t, is.DeepEqual(got.Target, tt.want), "Parse(%q) target", tt.command)
		})
	}
}

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

func TestParse_WhenEnvDropsAVariable_ReportsTheTargetOfTheCommandWithoutIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		same    string
	}{
		{name: "prefix removed by env -u", command: "GIT_DIR=/a env -u GIT_DIR git status", same: "env -u GIT_DIR git status"},
		{name: "env argument removed by a later env -u", command: "env GIT_DIR=/a env -u GIT_DIR git status", same: "env -u GIT_DIR git status"},
		{name: "shell string under env -u", command: "env -u GIT_DIR bash -c 'git status'", same: "env -u GIT_DIR git status"},
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

func TestParse_MarksAGitVariableOffTheAllowList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    string
	}{
		{name: "assignment prefix", command: "GIT_CONFIG_COUNT=1 git status", want: "GIT_CONFIG_COUNT"},
		{name: "env argument", command: "env GIT_EXEC_PATH=/tmp git status", want: "GIT_EXEC_PATH"},
		{name: "assignment-only command", command: "GIT_CONFIG_PARAMETERS=x", want: "GIT_CONFIG_PARAMETERS"},
		{name: "export of an assignment", command: "export GIT_CONFIG_GLOBAL=/tmp/c", want: "GIT_CONFIG_GLOBAL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Assert(t, len(got) > 0, "Parse(%q) = %v", tt.command, texts(got))
			want := []guard.Mark{{Kind: guard.GitVariable, Name: tt.want}}
			assert.Check(t, is.DeepEqual(got[0].Marks, want), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenAVariableIsNoUnlistedGitVariable_MarksNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "allowed git variable", command: "GIT_PAGER=cat git log"},
		{name: "variable outside git's names", command: "PAGER=cat git log"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			for _, inv := range got {
				assert.Check(t, is.Len(inv.Marks, 0), "Parse(%q) marks of %v", tt.command, inv.Words)
			}
		})
	}
}

func TestParse_WhenAGitVariableNamesACommandGitRuns_ParsesItsCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{name: "sequence editor as an assignment prefix", command: "GIT_SEQUENCE_EDITOR='git push origin main; true' git rebase -i HEAD~1", want: []string{"git", "push", "origin", "main"}},
		{name: "editor through env", command: "env GIT_EDITOR='git push origin main' git commit", want: []string{"git", "push", "origin", "main"}},
		{name: "pager exported earlier in the list", command: "export GIT_PAGER='git push origin main'; git log", want: []string{"git", "push", "origin", "main"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Check(t, is.Contains(texts(got), tt.want), "Parse(%q)", tt.command)
		})
	}
}

func TestParse_WhenACommandAGitVariableNamesCannotBeRead_MarksIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    guard.MarkKind
		index   int
	}{
		{name: "non-literal editor", command: `GIT_EDITOR="$e" git commit`, want: guard.DynamicScript},
		{name: "pager past the third level", command: "eval eval eval GIT_PAGER=less git log", index: 3, want: guard.DeepScript},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Assert(t, len(got) > tt.index, "Parse(%q) = %v", tt.command, texts(got))
			assert.Check(t, is.DeepEqual(got[tt.index].Marks, []guard.Mark{{Kind: tt.want}}), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenASubstitutionMovesHead_ReportsHeadUnknownForTheCommandHoldingIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "argument", command: `git commit -m "$(git switch main)"`},
		{name: "group redirect target", command: `{ git commit; } > "$(git switch main)"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			var commit []guard.Invocation
			for _, inv := range got {
				if len(inv.Words) > 1 && inv.Words[1].Text == "commit" {
					commit = append(commit, inv)
				}
			}
			assert.Assert(t, is.Len(commit, 1), "Parse(%q) = %v", tt.command, texts(got))
			assert.Check(t, commit[0].HeadUnknown, "Parse(%q) git commit head unknown = false, want true", tt.command)
		})
	}
}
