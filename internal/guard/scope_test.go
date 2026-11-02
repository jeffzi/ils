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
		{name: "cd past its options", command: "cd -P -- sub && git status", want: []string{start, start + "/sub"}},
		{name: "subshell move stays inside", command: "(cd sub && git status); git commit", want: []string{start, start + "/sub", start}},
		{
			name:    "substitution move stays inside",
			command: `echo "$(cd sub; git status)"; git commit`,
			want:    []string{start, start, start + "/sub", start},
		},
		{name: "absolute cd after an unknown one", command: `cd "$d"; cd /srv/repo; git status`, want: []string{start, "", "/srv/repo"}},
		{name: "cd .. after an absolute cd ends a physical walk", command: "cd -P sub; cd /srv/repo; cd ..; git status", want: []string{start, start + "/sub", "/srv/repo", "/srv"}},
		{name: "call of a function whose body stays", command: "f() { true; }; cd /srv/repo; f; git status", want: []string{start, start, "/srv/repo", "/srv/repo"}},
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
		{name: "relative cd with CDPATH in the environment", command: "cd sub && git commit", env: map[string]string{"CDPATH": "/srv"}},
		{name: "relative cd with CDPATH assigned in the command", command: "CDPATH=/srv; cd sub && git commit"},
		{name: "move in an if body", command: "if test -d sub; then cd sub; fi; git commit"},
		{name: "move in a loop body", command: "while true; do cd sub; done; git commit"},
		{name: "move in a case arm", command: "case x in y) cd sub;; esac; git commit"},
		{name: "after an or list that may have moved", command: "true || cd sub; git commit"},
		{name: "cd -P through a link and back up", command: "cd -P link/..; git commit"},
		{name: "cd .. after cd -P inside eval", command: "eval 'cd -P sub'; cd ..; git commit"},
		{name: "move in a pipeline", command: "true | cd sub; git commit"},
		{name: "relative cd in a shell string given CDPATH by a prefix", command: "CDPATH=/srv bash -c 'cd sub && git commit'"},
		{name: "call after a subshell redefines a function whose body moves", command: "f() { cd sub; }; (f() { :; }); cd /srv/repo; f; git commit"},
		{name: "call after an uncalled function body redefines one that moves", command: "f() { cd sub; }; g() { f() { :; }; }; cd /srv/repo; f; git commit"},
		{name: "call after an alias value redefines a function whose body moves", command: "f() { cd sub; }; alias a='f() { :; }'; cd /srv/repo; f; git commit"},
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
		{name: "relative work tree", command: "GIT_WORK_TREE=wt git status", want: guard.GitTarget{Dir: start, WorkTree: start + "/wt"}},
		{name: "env argument", command: "env GIT_DIR=.git git status", want: guard.GitTarget{Dir: start, GitDir: start + "/.git"}},
		{
			name:    "relative export read where git runs",
			command: "export GIT_DIR=.git; cd sub; git status",
			want:    guard.GitTarget{Dir: start + "/sub", GitDir: start + "/sub/.git"},
		},
		{name: "prefix of an earlier eval", command: "GIT_DIR=/srv/repo.git eval true; git status", want: guard.GitTarget{Dir: start}},
		{name: "move in an earlier eval string", command: "GIT_DIR=/srv/repo.git eval 'cd sub'; git status", want: guard.GitTarget{Dir: start + "/sub"}},
		{name: "env removing another name", command: "env -u PAGER git status", want: guard.GitTarget{Dir: start}},
		{name: "--work-tree", command: "git --work-tree wt -C sub status", want: guard.GitTarget{Dir: start + "/sub", WorkTree: start + "/sub/wt"}},
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
		{name: "allowed git variable read under allexport", command: "set -a; read -r GIT_PAGER; git log"},
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

func marksOf(invocations []guard.Invocation) []guard.Mark {
	var got []guard.Mark
	for _, inv := range invocations {
		got = append(got, inv.Marks...)
	}

	return got
}

func TestParse_WhenAllexportIsOn_MarksAGitVariableABuiltinAssigns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "read", command: "set -a; read -r GIT_CONFIG_PARAMETERS <<< x; git commit -m x"},
		{name: "read under set -o allexport", command: "set -o allexport; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "printf -v", command: "set -a; printf -v GIT_CONFIG_PARAMETERS %s x; git status"},
		{name: "assign-default expansion", command: `set -a; echo "${GIT_CONFIG_PARAMETERS:=x}"; git status`},
		{name: "allexport turned on in an if body", command: "if true; then set -a; fi; read GIT_CONFIG_PARAMETERS; git status"},
		{name: "assign-default expansion in [[ ]]", command: "set -a; [[ -n ${GIT_CONFIG_PARAMETERS:=x} ]]; git status"},
		{name: "assign-default expansion in a case word", command: "set -a; case ${GIT_CONFIG_PARAMETERS:=x} in *) ;; esac; git status"},
		{name: "assign-default expansion in a for word list", command: "set -a; for v in ${GIT_CONFIG_PARAMETERS:=x}; do :; done; git status"},
		{name: "assign-default expansion in a declare argument", command: `set -a; declare v="${GIT_CONFIG_PARAMETERS:=x}"; git status`},
		{name: "read in a function defined before set -a and called after", command: "f() { read -r GIT_CONFIG_PARAMETERS; }; set -a; f; git status"},
		{name: "allexport turned on by eval", command: "eval 'set -a'; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "allexport turned on by a function call", command: "f() { set -a; }; f; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read in a bash -a string", command: "bash -a -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a bash -o allexport string", command: "bash -o allexport -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a bash -a string after a one-dash long option", command: "bash -norc -a -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a bash -a string after a one-dash long option taking a value", command: "bash -rcfile x -a -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a bash -o allexport string after a one-dash long option", command: "bash -norc -o allexport -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "printf -v in a string of bash found by the equals lookup", command: `=bash -a -c 'printf -v GIT_CONFIG_PARAMETERS %s "core.hooksPath=/dev/null"; git commit -m x'`},
		{name: "read in a string of sh found by the equals lookup", command: "=sh -o allexport -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a zsh --allexport string", command: "zsh --allexport -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a zsh string under a long option after -c", command: "zsh -c --allexport 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a zsh string under a long option spelled with capitals and underscores", command: "zsh --All_Export -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a zsh -o string spelled with capitals and underscores", command: "zsh -o ALL_EXPORT -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a zsh string after set -o spelled with capitals and underscores", command: "zsh -c 'set -o All_Export; read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read after set -o spelled with capitals and underscores in an unknown shell", command: "set -o All_Export; read -r GIT_CONFIG_PARAMETERS; git status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			want := guard.Mark{Kind: guard.GitVariable, Name: "GIT_CONFIG_PARAMETERS"}
			assert.Check(t, is.Contains(marksOf(got), want), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenAllexportIsOff_MarksNoGitVariableABuiltinAssigns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "turned on in a subshell", command: "(set -a); read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "turned on for a bash -a string only", command: "bash -a -c true; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read in a bash string without -a", command: "bash -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a bash string after set -o spelled with capitals", command: "bash -c 'set -o ALLEXPORT; read -r GIT_CONFIG_PARAMETERS; git status'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Check(t, is.Len(marksOf(got), 0), "Parse(%q) marks", tt.command)
		})
	}
}
