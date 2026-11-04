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
		{name: "key spelled in another case", command: "git -c CORE.Pager='git push origin main' log", want: [][]string{{"git", "-c", "CORE.Pager=git push origin main", "log"}, pushed}},
		{name: "pager key spelled in another case", command: "git -c Pager.Log='git push origin main' log", want: [][]string{{"git", "-c", "Pager.Log=git push origin main", "log"}, pushed}},
		{name: "core.pager given by --config-env", command: "P='git push origin main' git --config-env=core.pager=P log", want: [][]string{{"git", "--config-env=core.pager=P", "log"}, pushed}},
		{name: "credential helper run by the shell", command: "git -c credential.helper='!git push origin main' fetch", want: [][]string{{"git", "-c", "credential.helper=!git push origin main", "fetch"}, pushed}},
		{name: "credential helper named by its suffix", command: "git -c credential.helper=store fetch", want: [][]string{{"git", "-c", "credential.helper=store", "fetch"}, {"git", "credential-store"}}},
		{name: "bisect run", command: "git bisect run git push origin main", want: [][]string{{"git", "bisect", "run", "git", "push", "origin", "main"}, pushed}},
		{name: "submodule foreach", command: "git submodule foreach 'git push origin main'", want: [][]string{{"git", "submodule", "foreach", "git push origin main"}, pushed}},
		{name: "submodule foreach after its options", command: "git submodule foreach --recursive 'git push origin main'", want: [][]string{{"git", "submodule", "foreach", "--recursive", "git push origin main"}, pushed}},
		{name: "difftool --extcmd with an attached value", command: "git difftool --extcmd='git push origin main' HEAD~1", want: [][]string{{"git", "difftool", "--extcmd=git push origin main", "HEAD~1"}, pushed}},
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
		{name: "empty credential helper", command: "git -c credential.helper= fetch"},
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
		{name: "non-literal core.pager value", command: `git -c core.pager="$p" log`, want: guard.DynamicScript},
		{name: "core.pager variable the parse cannot read", command: "git --config-env core.pager=ABSENT log", want: guard.DynamicScript},
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
		{name: "difftool -x", command: `git difftool -x "$c" HEAD~1`, wantNamed: "-x"},
		{name: "difftool --extcmd", command: `git difftool --extcmd="$c" HEAD~1`, wantNamed: "--extcmd"},
		{name: "difftool word that could be an option", command: `git difftool "$o" HEAD~1`, wantNamed: "difftool"},
		{name: "difftool short-flag bundle ending in a non-literal", command: `git difftool -y"$o" HEAD~1`, wantNamed: "difftool"},
		{name: "difftool long option completed by a non-literal", command: `git difftool --e"$x" HEAD~1`, wantNamed: "difftool"},
		{name: "filter-branch filter", command: `git filter-branch --index-filter "$c" HEAD`, wantNamed: "--index-filter"},
		{name: "for-each-repo", command: `git for-each-repo --config=x.repo "$c"`, wantNamed: "for-each-repo"},
	})
}

func TestJudge_AllowsAGitCommandRunningALiteralItAllows(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "bisect run", command: "git bisect run make test"},
		{name: "submodule foreach", command: "git submodule foreach 'git status'"},
		{name: "difftool -x", command: "git difftool -x 'diff -u' HEAD~1"},
		{name: "difftool command given by -c", command: "git -c difftool.x.cmd='diff -u' difftool --tool=x HEAD~1"},
		{name: "difftool against a revision", command: "git difftool HEAD~1"},
		{name: "difftool with a tool", command: "git difftool --tool=meld"},
		{name: "difftool without prompting", command: "git difftool -y"},
		{name: "for-each-repo", command: "git for-each-repo --config=x.repo status"},
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
		{name: "difftool --extcmd below the top by -C", command: "git -C docs difftool --extcmd " + innerCommit + " HEAD~1"},
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
