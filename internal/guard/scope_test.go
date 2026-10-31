package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/guard"
)

// parseWith returns the invocations of command, read by a caller in start whose environment is env.
func parseWith(t *testing.T, command string, env map[string]string) []guard.Invocation {
	t.Helper()

	got, err := guard.Parse(command, guard.Caller{Home: home, Dir: start, Env: env})
	assert.NilError(t, err, "Parse(%q)", command)

	return got
}

// last returns the last invocation of command, read by a caller in start whose environment is env.
func last(t *testing.T, command string, env map[string]string) guard.Invocation {
	t.Helper()

	got := parseWith(t, command, env)
	assert.Assert(t, len(got) > 0, "Parse(%q) holds no invocation", command)

	return got[len(got)-1]
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
		{name: "no move", command: "git status", want: []string{start}},
		{name: "cd to a relative path", command: "cd sub && git status", want: []string{start, start + "/sub"}},
		{name: "cd to an absolute path", command: "cd /srv/repo; git status", want: []string{start, "/srv/repo"}},
		{name: "pushd to a parent's sibling", command: "pushd ../wt && git status", want: []string{start, "/home/reader/wt"}},
		{name: "each cd relative to the one before", command: "cd a; cd ../b; git status", want: []string{start, start + "/a", start + "/b"}},
		{name: "cd to a tilde path", command: "cd ~/x && git status", want: []string{start, home + "/x"}},
		{name: "cd past its options", command: "cd -P -- sub && git status", want: []string{start, start + "/sub"}},
		{name: "subshell move stays inside", command: "(cd sub && git status); git commit", want: []string{start, start + "/sub", start}},
		{
			name:    "substitution move stays inside",
			command: `echo "$(cd sub; git status)"; git commit`,
			want:    []string{start, start, start + "/sub", start},
		},
		{name: "group move holds after the group", command: "{ cd sub; }; git commit", want: []string{start, start + "/sub"}},
		{name: "group redirect substitution before the group's move", command: `{ cd sub; } > "$(git status)"`, want: []string{start, start}},
		{name: "and list after the move", command: "cd sub && git status && git commit", want: []string{start, start + "/sub", start + "/sub"}},
		{name: "absolute cd after an unknown one", command: `cd "$d"; cd /srv/repo; git status`, want: []string{start, "", "/srv/repo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parseWith(t, tt.command, nil)

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
		{name: "pushd with no directory", command: "pushd && git commit"},
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
		{name: "move in a pipeline", command: "true | cd sub; git commit"},
		{name: "relative cd in a shell string given CDPATH by env", command: "env CDPATH=/srv bash -c 'cd sub && git commit'"},
		{name: "relative cd in a shell string given CDPATH by a prefix", command: "CDPATH=/srv bash -c 'cd sub && git commit'"},
		{name: "relative cd in a heredoc given CDPATH by a prefix", command: "CDPATH=/srv bash <<'EOF'\ncd sub && git commit\nEOF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := last(t, tt.command, tt.env)

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
		{name: "nothing set", command: "git status", want: guard.GitTarget{Dir: start}},
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
		{name: "export in a subshell", command: "(export GIT_DIR=/srv/repo.git); git status", want: guard.GitTarget{Dir: start}},
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
		{name: "past the value of -c", command: "git -c core.pager=cat -C sub status", want: guard.GitTarget{Dir: start + "/sub"}},
		{name: "a subcommand's own -C", command: "git log -C sub", want: guard.GitTarget{Dir: start}},
		{name: "non-literal -C", command: `git -C "$d" status`, want: guard.GitTarget{}},
		{name: "non-literal -C then a relative --git-dir", command: `git -C "$d" --git-dir=.git status`, want: guard.GitTarget{Unknown: true}},
		{name: "non-literal GIT_DIR", command: `GIT_DIR="$d" git status`, want: guard.GitTarget{Dir: start, Unknown: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := last(t, tt.command, tt.env)

			assert.Check(t, is.DeepEqual(got.Target, tt.want), "Parse(%q) target", tt.command)
		})
	}
}

func TestParse_WhenAShellStringRunsGit_ReportsTheTargetOfTheEnvironmentItStartsWith(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "assignment prefix", command: "GIT_DIR=/srv/x.git bash -c 'git status'"},
		{name: "env argument", command: "env GIT_DIR=/srv/x.git bash -c 'git status'"},
		{name: "heredoc fed to a shell", command: "GIT_DIR=/srv/x.git bash <<'EOF'\ngit status\nEOF"},
		{name: "eval under an assignment prefix", command: "GIT_DIR=/srv/x.git eval 'git status'"},
		{name: "eval under an env argument", command: "env GIT_DIR=/srv/x.git eval 'git status'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := last(t, tt.command, nil)

			want := guard.GitTarget{Dir: start, GitDir: "/srv/x.git"}
			assert.Check(t, is.DeepEqual(got.Target, want), "Parse(%q) target", tt.command)
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

			got := last(t, tt.command, nil)

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
		{name: "prefixes cleared by env -i", command: "GIT_DIR=/a GIT_WORK_TREE=/b env -i git status", same: "env -i git status"},
		{name: "env argument removed by a later env -u", command: "env GIT_DIR=/a env -u GIT_DIR git status", same: "env -u GIT_DIR git status"},
		{name: "shell string under env -i", command: "env -i bash -c 'git status'", same: "env -i git status"},
		{name: "shell string under env -u", command: "env -u GIT_DIR bash -c 'git status'", same: "env -u GIT_DIR git status"},
		{name: "eval string under env -i", command: "env -i eval 'git status'", same: "env -i git status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			want := last(t, tt.same, nil).Target

			got := last(t, tt.command, nil)

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
		index   int
	}{
		{name: "assignment prefix", command: "GIT_CONFIG_COUNT=1 git status", want: "GIT_CONFIG_COUNT"},
		{name: "env argument", command: "env GIT_EXEC_PATH=/tmp git status", want: "GIT_EXEC_PATH"},
		{name: "assignment-only command", command: "GIT_CONFIG_PARAMETERS=x", want: "GIT_CONFIG_PARAMETERS"},
		{name: "export of an assignment", command: "export GIT_CONFIG_GLOBAL=/tmp/c", want: "GIT_CONFIG_GLOBAL"},
		{name: "export of a name", command: "export GIT_CONFIG_COUNT", want: "GIT_CONFIG_COUNT"},
		{name: "inside a substitution", command: `echo "$(GIT_CONFIG_COUNT=1 git status)"`, index: 1, want: "GIT_CONFIG_COUNT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parseWith(t, tt.command, nil)

			assert.Assert(t, len(got) > tt.index, "Parse(%q) = %v", tt.command, texts(got))
			want := []guard.Mark{{Kind: guard.GitVariable, Name: tt.want}}
			assert.Check(t, is.DeepEqual(got[tt.index].Marks, want), "Parse(%q) marks", tt.command)
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

			got := only(t, tt.command)

			assert.Check(t, is.Len(got.Marks, 0), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_ReportsWhetherHeadMayHaveMovedBeforeEachInvocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    []bool
	}{
		{name: "git switch", command: "git switch main && git commit", want: []bool{false, true}},
		{name: "symbolic-ref with a value", command: "git symbolic-ref HEAD refs/heads/main; git commit", want: []bool{false, true}},
		{name: "symbolic-ref read", command: "git symbolic-ref HEAD; git commit", want: []bool{false, false}},
		{name: "branch -m", command: "git branch -m next; git commit", want: []bool{false, true}},
		{name: "branch -M", command: "git branch -M next; git commit", want: []bool{false, true}},
		{name: "branch --move", command: "git branch --move next; git commit", want: []bool{false, true}},
		{name: "branch create", command: "git branch next; git commit", want: []bool{false, false}},
		{name: "rebase naming a branch", command: "git rebase main topic; git commit", want: []bool{false, true}},
		{name: "rebase onto an upstream", command: "git rebase main; git commit", want: []bool{false, false}},
		{name: "rebase with a valued option", command: "git rebase --onto main base; git commit", want: []bool{false, false}},
		{name: "rebase --root naming a branch", command: "git rebase --root topic; git commit", want: []bool{false, true}},
		{name: "rebase --root onto a base naming a branch", command: "git rebase --root --onto x topic; git commit", want: []bool{false, true}},
		{name: "rebase --root naming no branch", command: "git rebase --root; git commit", want: []bool{false, false}},
		{name: "bisect", command: "git bisect start; git commit", want: []bool{false, true}},
		{name: "gh pr checkout", command: "gh pr checkout 12; git commit", want: []bool{false, true}},
		{name: "gh pr view", command: "gh pr view 12; git commit", want: []bool{false, false}},
		{name: "switch behind global options", command: "git -C sub switch main; git commit", want: []bool{false, true}},
		{name: "switch in a subshell", command: "(git switch main); git commit", want: []bool{false, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			invocations := parseWith(t, tt.command, nil)

			got := make([]bool, 0, len(invocations))
			for _, inv := range invocations {
				got = append(got, inv.HeadUnknown)
			}

			assert.Check(t, is.DeepEqual(got, tt.want), "Parse(%q) head unknown", tt.command)
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
		{name: "assignment prefix", command: `MSG="$(git switch main)" git commit`},
		{name: "redirect target", command: `git commit > "$(git switch main)"`},
		{name: "group redirect target", command: `{ git commit; } > "$(git switch main)"`},
		{name: "subshell redirect target", command: `(git commit) > "$(git switch main)"`},
		{name: "loop redirect target", command: `while true; do git commit; done > "$(git switch main)"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parseWith(t, tt.command, nil)

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
