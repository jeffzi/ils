//go:build unix

package guard_test

import (
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
)

func TestParse_WhenGitRunsAShellCommandItHolds_ParsesItsCommands(t *testing.T) {
	t.Parallel()

	pushed := []string{"git", "push", "origin", "main"}
	tests := []struct {
		name    string
		command string
		want    [][]string
	}{
		{name: "rebase --exec with a separate value", command: "git rebase --exec 'git push origin main' HEAD~1", want: [][]string{{"git", "rebase", "--exec", "git push origin main", "HEAD~1"}, pushed}},
		{name: "rebase --exec with an attached value", command: "git rebase --exec='git push origin main' HEAD~1", want: [][]string{{"git", "rebase", "--exec=git push origin main", "HEAD~1"}, pushed}},
		{name: "rebase --exec spelled as a prefix", command: "git rebase --exe 'git push origin main' HEAD~1", want: [][]string{{"git", "rebase", "--exe", "git push origin main", "HEAD~1"}, pushed}},
		{name: "rebase -x with a separate value", command: "git rebase -x 'git push origin main' HEAD~1", want: [][]string{{"git", "rebase", "-x", "git push origin main", "HEAD~1"}, pushed}},
		{
			name:    "rebase -x in the body of a function the command calls",
			command: "f() { git rebase -x 'git push origin main' HEAD~1; }; f",
			want:    [][]string{{"git", "rebase", "-x", "git push origin main", "HEAD~1"}, pushed, {"f"}},
		},
		{name: "rebase -x with an attached value", command: "git rebase -x' git push origin main' HEAD~1", want: [][]string{{"git", "rebase", "-x git push origin main", "HEAD~1"}, pushed}},
		{name: "rebase -x in a short-flag bundle", command: "git rebase -ix 'git push origin main' HEAD~1", want: [][]string{{"git", "rebase", "-ix", "git push origin main", "HEAD~1"}, pushed}},
		{name: "rebase --exec after a -X value holding an x", command: "git rebase -Xx --exec 'git push origin main' main", want: [][]string{{"git", "rebase", "-Xx", "--exec", "git push origin main", "main"}, pushed}},
		{name: "core.editor key spelled in another case", command: "git -c CORE.Editor='git push origin main' commit", want: [][]string{{"git", "-c", "CORE.Editor=git push origin main", "commit"}, pushed}},
		{
			name:    "sequence.editor given by -c",
			command: "git -c sequence.editor='git push origin main' rebase -i HEAD~1",
			want:    [][]string{{"git", "-c", "sequence.editor=git push origin main", "rebase", "-i", "HEAD~1"}, pushed},
		},
		{name: "bisect run", command: "git bisect run git push origin main", want: [][]string{{"git", "bisect", "run", "git", "push", "origin", "main"}, pushed}},
		{name: "submodule foreach", command: "git submodule foreach 'git push origin main'", want: [][]string{{"git", "submodule", "foreach", "git push origin main"}, pushed}},
		{name: "submodule foreach after its options", command: "git submodule foreach --recursive 'git push origin main'", want: [][]string{{"git", "submodule", "foreach", "--recursive", "git push origin main"}, pushed}},
		{name: "filter-branch filter", command: "git filter-branch --tree-filter 'git push origin main' HEAD", want: [][]string{{"git", "filter-branch", "--tree-filter", "git push origin main", "HEAD"}, pushed}},
		{name: "for-each-repo with an attached --config", command: "git for-each-repo --config=x.repo push origin main", want: [][]string{{"git", "for-each-repo", "--config=x.repo", "push", "origin", "main"}, pushed}},
		{name: "for-each-repo with a separate --config", command: "git for-each-repo --config x.repo push origin main", want: [][]string{{"git", "for-each-repo", "--config", "x.repo", "push", "origin", "main"}, pushed}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Check(t, is.DeepEqual(texts(got), tt.want), "Parse(%q)", tt.command)
		})
	}
}

func TestParse_WhenAGitValueIsNoShellCommand_ParsesNothingMore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "cherry-pick -x flag", command: "git cherry-pick -x abc123"},
		{name: "-c key git does not run", command: "git -c user.name='git push origin main' log"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.Len(got.Marks, 0), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenAShellCommandGitRunsCannotBeRead_MarksIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    guard.MarkKind
		index   int
	}{
		{name: "non-literal rebase --exec value", command: `git rebase --exec "$c" HEAD~1`, want: guard.DynamicScript},
		{name: "non-literal attached rebase -x value", command: `git rebase -x"$c" HEAD~1`, want: guard.DynamicScript},
		{name: "rebase argument that could be an exec option", command: `git rebase "$opt" HEAD~1`, want: guard.DynamicScript},
		{name: "non-literal core.editor value", command: `git -c core.editor="$e" commit`, want: guard.DynamicScript},
		{name: "core.editor given by --config-env from an assignment", command: "ED=vim git --config-env=core.editor=ED commit", want: guard.DynamicScript},
		{name: "rebase --exec past the third level", command: "eval eval eval git rebase -x true HEAD~1", index: 3, want: guard.DeepScript},
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

func TestParse_WhenAGitAliasOfRebaseIsGivenAnExec_ParsesTheExecCommand(t *testing.T) {
	t.Parallel()
	f := aliased(t)
	const command = "git rb -x 'git push origin main'"

	got := parseWith(t, command, callerIn(f, f.Dir()))

	assert.Check(t, is.Contains(texts(got), []string{"git", "push", "origin", "main"}), "Parse(%q)", command)
}

func TestJudge_RefusesAGitCommandRunningAnArgumentItCannotRead(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "bisect run", command: `git bisect run "$c"`, wantNamed: "bisect run"},
		{name: "submodule foreach", command: `git submodule foreach "$c"`, wantNamed: "submodule foreach"},
		{name: "filter-branch filter", command: `git filter-branch --index-filter "$c" HEAD`, wantNamed: "--index-filter"},
		{name: "for-each-repo", command: `git for-each-repo --config=x.repo "$c"`, wantNamed: "for-each-repo"},
		{name: "core.editor that is not a literal", command: `git -c core.editor="$ED" commit`},
	})
}

func TestJudge_AllowsAGitCommandRunningALiteralItAllows(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "bisect run", command: "git bisect run make test"},
		{name: "submodule foreach", command: "git submodule foreach 'git status'"},
		{name: "difftool against a revision", command: "git difftool HEAD~1"},
		{name: "difftool with a tool", command: "git difftool --tool=meld"},
		{name: "difftool without prompting", command: "git difftool -y"},
		{name: "for-each-repo", command: "git for-each-repo --config=x.repo status"},
	})
}

func TestJudge_AllowsAGitCommandWhoseConfigOrOptionValueItDoesNotReadAsAScript(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "core.sshCommand", command: `git -c core.sshCommand='git push origin main' fetch`},
		{name: "core.pager", command: `git -c core.pager='git push origin main' log`},
		{name: "pager of a command", command: `git -c pager.log='git push origin main' log`},
		{name: "credential helper", command: `git -c credential.helper='!git push origin main' fetch`},
		{name: "command key of a tool", command: `git -c difftool.x.cmd='git push origin main' difftool --tool=x HEAD~1`},
		{name: "difftool -x", command: `git difftool -x "$c" HEAD~1`},
		{name: "difftool --extcmd", command: `git difftool --extcmd="$c" HEAD~1`},
		{name: "--config-env of a key it does not read", command: "git --config-env=http.extraHeader=AUTH fetch"},
		{name: "core.sshCommand that is not a literal", command: `git -c core.sshCommand="ssh -i $KEY" fetch`},
		{name: "core.pager that is not a literal", command: `git -c core.pager="$P" log`},
	})
}

func TestJudge_RefusesAGitCommandWhoseConfigTheGuardRefusesWhateverItsValue(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "--config-env of the hooks path from an assignment", command: "AUTH=x git --config-env=core.hooksPath=AUTH commit -m x"},
		{name: "alias given by -c that pushes", command: "git -c alias.p=push p", wantNamed: "push"},
	})
}

// nestedRepositories returns a repository on main holding the nested repositories inner and
// docs/inner, both cloned from its origin, and a caller working at its top.
func nestedRepositories(t *testing.T) (*gittest.Fixture, guard.Caller) {
	t.Helper()

	f := gittest.New(t, "main")
	f.Git("clone", "--quiet", f.OriginDir(), "inner")
	f.Git("clone", "--quiet", f.OriginDir(), filepath.Join("docs", "inner"))

	return f, callerIn(f, f.Dir())
}

const innerCommit = `"git -C inner commit --allow-empty -m x"`

func TestParse_WhenGitRunsAScriptAtTheTopOfTheWorkTree_ReadsItsGitCommandsFromTheTop(t *testing.T) {
	t.Parallel()

	f, caller := nestedRepositories(t)
	want := filepath.Join(f.Dir(), "inner", ".git")
	tests := []struct {
		name    string
		command string
	}{
		{name: "rebase --exec below the top by -C", command: "git -C docs rebase --exec " + innerCommit + " origin/main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lastWith(t, tt.command, caller).Git

			assert.Check(t, !got.GitDirUnknown, "Parse(%q) reports the git dir unknown", tt.command)
			assert.Check(t, is.Equal(got.GitDir, want), "Parse(%q) git dir", tt.command)
		})
	}
}

func TestParse_WhenWhereGitRunsAScriptCannotBeTold_ReportsItsDirectoryUnknown(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")
	tests := []struct {
		name    string
		command string
	}{
		{name: "rebase --exec in a bare repository", command: "git --git-dir=" + quotedPath(f.OriginDir()) + " rebase --exec 'git status' main"},
		{name: "rebase --exec in a target it cannot read", command: `GIT_DIR="$d" git rebase --exec 'git status' main`},
		{name: "submodule foreach", command: "git submodule foreach 'git status'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lastWith(t, tt.command, caller)

			assert.Check(t, is.Equal(got.Dir, ""), "Parse(%q) directory of %v", tt.command, got.Words)
		})
	}
}
