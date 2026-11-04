//go:build unix

package guard_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/guard"
)

// linkTo returns a symbolic link to target, made in a new directory that holds no repository.
func linkTo(t *testing.T, target string) string {
	t.Helper()

	link := filepath.Join(outsideDir(t), "link")
	assert.NilError(t, os.Symlink(target, link))

	return link
}

// subdirectory makes the directory sub inside dir, and returns its path.
func subdirectory(t *testing.T, dir string) string {
	t.Helper()

	sub := filepath.Join(dir, "sub")
	assert.NilError(t, os.MkdirAll(sub, 0o750))

	return sub
}

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
		{name: "cd to a variable the command assigned a literal", command: `S=/srv/repo; cd "$S" && git status`, want: []string{start, start, "/srv/repo"}},
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
		{name: "after a source of a file", command: "source env.sh && git commit"},
		{name: "relative cd after a source of a file, which may set CDPATH", command: "source env.sh; cd /srv; cd sub; git commit"},
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
		{name: "move in an earlier eval string", command: "GIT_DIR=/srv/repo.git eval 'cd sub'; git status", want: guard.GitTarget{Dir: start + "/sub"}},
		{name: "env removing another name", command: "env -u PAGER git status", want: guard.GitTarget{Dir: start}},
		{name: "--work-tree", command: "git --work-tree wt -C sub status", want: guard.GitTarget{Dir: start + "/sub", WorkTree: start + "/sub/wt"}},
		{name: "non-literal -C then a relative --git-dir", command: `git -C "$d" --git-dir=.git status`, want: guard.GitTarget{Unknown: true}},
		{name: "non-literal GIT_DIR", command: `GIT_DIR="$d" git status`, want: guard.GitTarget{Dir: start, Unknown: true}},
		{name: "GIT_DIR given a variable the command assigned a literal", command: "S=/srv/repo.git; GIT_DIR=$S git status", want: guard.GitTarget{Dir: start, GitDir: "/srv/repo.git"}},
		{name: "absolute cd after a source of a file, which may set GIT_DIR", command: "source env.sh; cd /srv/repo; git status", want: guard.GitTarget{Dir: "/srv/repo", Unknown: true}},
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

	const command = "GIT_PAGER=cat git log"

	got := parse(t, command)

	assert.Assert(t, len(got) > 0, "Parse(%q) = %v", command, texts(got))
	for _, inv := range got {
		assert.Check(t, is.Len(inv.Marks, 0), "Parse(%q) marks of %v", command, inv.Words)
	}
}

func TestParse_WhenASubstitutionMovesHead_ReportsHeadUnknownForTheCommandHoldingIt(t *testing.T) {
	t.Parallel()

	const command = `{ git commit; } > "$(git switch main)"`

	got := parse(t, command)

	var commit []guard.Invocation
	for _, inv := range got {
		if len(inv.Words) > 1 && inv.Words[1].Text == "commit" {
			commit = append(commit, inv)
		}
	}
	assert.Assert(t, is.Len(commit, 1), "Parse(%q) = %v", command, texts(got))
	assert.Check(t, commit[0].HeadUnknown, "Parse(%q) git commit head unknown = false, want true", command)
}

func marksOf(invocations []guard.Invocation) []guard.Mark {
	var got []guard.Mark
	for _, inv := range invocations {
		got = append(got, inv.Marks...)
	}

	return got
}

func TestJudge_WhenAFileIsSourced_AllowsWhatNeedsNoKnownDirectory(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "source of a file", command: "source env.sh"},
		{name: "read after a dot of a file", command: ". ./env.sh && git status"},
	})
}

func TestJudge_WhenAFileIsSourcedBeforeAGhAlias_RefusesNamingTheConfigVariable(t *testing.T) {
	// Not parallel: it sets GH_PATH and the gh config location for the whole process.
	ghReadingConfig(t, "sy: repo view\n")

	checkJudgedRow(t, forgeCaller(t), judged{command: "source env.sh && gh sy", want: guard.Refuse, wantNamed: "GH_CONFIG_DIR"})
}

func TestJudge_WhenACdFollowsAVariableNamingATrunkClone_RefusesTheCommit(t *testing.T) {
	t.Parallel()
	f, _ := standingOn(t, "main", "main")
	caller := callerIn(f, outsideDir(t))

	checkJudgedRow(t, caller, judged{command: "D=" + quotedPath(f.Dir()) + `; cd "$D" && ` + commitCommand, want: guard.Refuse, wantNamed: "main"})
}

func TestJudge_WhenAForLoopRunsOverLiterals_AllowsWhatTheBodyRunsForEachValue(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "value in a path", command: "for v in lua5.1 luajit; do /opt/$v/bin/lua b.lua; done"},
		{name: "value after a variable the command assigned", command: "L=/opt/lua; for v in lua5.1 luajit; do $L/$v/bin/lua b.lua; done"},
		{name: "as many values as the bodies the guard walks, each walked twice", command: "for v in" + strings.Repeat(" ls", 128) + "; do $v; done"},
		{name: "two loops whose values fill the bodies the guard walks", command: "for v in" + strings.Repeat(" ls", 100) + "; do :; done; for v in" + strings.Repeat(" ls", 28) + "; do $v; done"},
		{name: "loop after a function whose loop and dry run fill the bodies the guard walks", command: "f() { for v in" + strings.Repeat(" ls", 40) + "; do :; done; }; f; for v in" + strings.Repeat(" ls", 8) + "; do $v; done"},
		{name: "loop in a git pager script that fills the bodies a first loop left", command: "for v in" + strings.Repeat(" ls", 100) + "; do :; done; git -c core.pager='for v in" + strings.Repeat(" ls", 28) + "; do $v; done' log"},
	})
}

func TestJudge_WhenAForLoopValueIsAGuardedWord_RefusesAsThatWord(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "value after an unguarded one", command: "for g in ls git; do $g push; done", wantNamed: "git push"},
		{name: "value before an unguarded one", command: "for g in git ls; do $g push; done", wantNamed: "git push"},
	})
}

func TestJudge_WhenTheGuardCannotReadAForLoopValue_RefusesItAsACommandWord(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "glob word after a literal", command: "for f in ls *.sh; do $f; done"},
		{name: "command substitution after a literal", command: "for f in ls $(ls); do $f; done"},
		{name: "positional parameters", command: `for f in "$@"; do $f; done`},
		{name: "more values than the bodies the guard walks, each walked twice", command: "for v in" + strings.Repeat(" ls", 129) + "; do $v; done"},
		{name: "second loop past the bodies a first loop left", command: "for v in" + strings.Repeat(" ls", 100) + "; do :; done; for v in" + strings.Repeat(" ls", 29) + "; do $v; done"},
		{name: "loop past the bodies a function's loop and its dry run left", command: "f() { for v in" + strings.Repeat(" ls", 40) + "; do :; done; }; f; for v in" + strings.Repeat(" ls", 12) + "; do $v; done"},
		{name: "loop in a git pager script past the bodies a first loop left", command: "for v in" + strings.Repeat(" ls", 100) + "; do :; done; git -c core.pager='for v in" + strings.Repeat(" ls", 29) + "; do $v; done' log"},
	})
}

func TestParse_WhenTheGuardCannotBeSureOfAVariable_ReadsItsExpansionUnknown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "append assignment", command: "S=/tmp/work; S+=/x; $S/run.sh"},
		{name: "read", command: "S=/tmp/work; read -r S; $S/run.sh"},
		{name: "printf -v", command: "S=/tmp/work; printf -v S %s /srv; $S/run.sh"},
		{name: "for loop name", command: "S=/tmp/work; for S in /srv; do :; done; $S/run.sh"},
		{name: "assign-default expansion", command: `S=/tmp/work; echo "${S:=/srv}"; $S/run.sh`},
		{name: "unset", command: "S=/tmp/work; unset S; $S/run.sh"},
		{name: "value from a command substitution", command: "S=/tmp/work; S=$(pwd); $S/run.sh"},
		{name: "array value", command: "S=(/tmp/work); $S/run.sh"},
		{name: "element assignment", command: "S=/tmp/work; S[1]=/srv; $S/run.sh"},
		{name: "assignment in a subshell", command: "(S=/tmp/work); $S/run.sh"},
		{name: "assignment in a pipeline stage", command: "true | S=/tmp/work; $S/run.sh"},
		{name: "assignment in a background job", command: "S=/tmp/work & $S/run.sh"},
		{name: "assignment that may not run in an and list", command: "true && S=/tmp/work; $S/run.sh"},
		{name: "assignment in an if body", command: "if test -d x; then S=/tmp/work; fi; $S/run.sh"},
		{name: "assignment in a loop body", command: "for f in a; do S=/tmp/work; done; $S/run.sh"},
		{name: "call of a function whose body assigns it", command: "f() { S=/tmp/work; }; f; $S/run.sh"},
		{name: "local outside a function", command: "local S=/tmp/work; $S/run.sh"},
		{name: "declare -n before the assignment", command: "declare -n R=T; S=/tmp/work; $S/run.sh"},
		{name: "typeset -n after the word", command: "S=/tmp/work; $S/run.sh; typeset -n R=T"},
		{name: "local -n in a function", command: "f() { local -n R=T; }; S=/tmp/work; $S/run.sh"},
		{name: "declare -n through builtin", command: "builtin declare -n R=T; S=/tmp/work; $S/run.sh"},
		{name: "typeset -n through command with an option", command: "command -p typeset -n R=T; S=/tmp/work; $S/run.sh"},
		{name: "declare with an attribute through command", command: "command declare -u S; S=/tmp/work; $S/run.sh"},
		{name: "zsh integer", command: "integer S; S=/tmp/work; $S/run.sh"},
		{name: "eval string declaring a name reference later in a loop", command: "S=/tmp/work; for f in a b; do $S/run.sh; eval 'declare -n R=T'; done"},
		{name: "assignment prefix of the command reading it", command: "S=/tmp/work $S/run.sh"},
		{name: "source of a file after the assignment", command: "S=/tmp/work; source env.sh; $S/run.sh"},
		{name: "shell string of another shell", command: "S=/tmp/work; bash -c '$S/run.sh'"},
		{name: "expansion with an operator", command: "S=/tmp/work/x; ${S%/x}/run.sh"},
		{name: "unquoted expansion of a value holding a space", command: "S='/tmp/my work'; $S/run.sh"},
		{name: "unquoted expansion of a value holding a glob character", command: "S='/tmp/*'; $S/run.sh"},
		{name: "unquoted expansion after IFS is assigned", command: "IFS=/; S=/tmp/work; $S/run.sh"},
		{name: "declare with an attribute that changes the value", command: "declare -u S=/tmp/work; $S/run.sh"},
		{name: "unquoted expansion of a for loop value holding a backslash", command: `for S in '/tmp/a\b'; do $S/run.sh; done`},
		{name: "assignment after an attribute that changes the value", command: "typeset -l S; S=/TMP/work; $S/run.sh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			checkRunShReadUnknown(t, tt.command, got)
		})
	}
}
