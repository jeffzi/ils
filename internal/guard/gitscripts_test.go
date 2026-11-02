//go:build unix

package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

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
		{name: "rebase -x with an attached value", command: "git rebase -x' git push origin main' HEAD~1", want: [][]string{{"git", "rebase", "-x git push origin main", "HEAD~1"}, pushed}},
		{name: "rebase -x in a short-flag bundle", command: "git rebase -ix 'git push origin main' HEAD~1", want: [][]string{{"git", "rebase", "-ix", "git push origin main", "HEAD~1"}, pushed}},
		{name: "pager of one command given by -c", command: "git -c pager.log='git push origin main' log", want: [][]string{{"git", "-c", "pager.log=git push origin main", "log"}, pushed}},
		{name: "key spelled in another case", command: "git -c CORE.Pager='git push origin main' log", want: [][]string{{"git", "-c", "CORE.Pager=git push origin main", "log"}, pushed}},
		{name: "pager key spelled in another case", command: "git -c Pager.Log='git push origin main' log", want: [][]string{{"git", "-c", "Pager.Log=git push origin main", "log"}, pushed}},
		{name: "core.pager given by --config-env", command: "P='git push origin main' git --config-env=core.pager=P log", want: [][]string{{"git", "--config-env=core.pager=P", "log"}, pushed}},
		{name: "diff driver textconv given by -c", command: "git -c diff.x.textconv='git push origin main' diff", want: [][]string{{"git", "-c", "diff.x.textconv=git push origin main", "diff"}, pushed}},
		{name: "filter driver smudge given by -c", command: "git -c filter.x.smudge='git push origin main' status", want: [][]string{{"git", "-c", "filter.x.smudge=git push origin main", "status"}, pushed}},
		{name: "merge driver given by -c", command: "git -c merge.x.driver='git push origin main' status", want: [][]string{{"git", "-c", "merge.x.driver=git push origin main", "status"}, pushed}},
		{name: "credential helper run by the shell", command: "git -c credential.helper='!git push origin main' fetch", want: [][]string{{"git", "-c", "credential.helper=!git push origin main", "fetch"}, pushed}},
		{name: "credential helper of a URL, key in another case", command: "git -c credential.https://h.Helper='!git push origin main' fetch", want: [][]string{{"git", "-c", "credential.https://h.Helper=!git push origin main", "fetch"}, pushed}},
		{name: "credential helper named by its suffix", command: "git -c credential.helper=store fetch", want: [][]string{{"git", "-c", "credential.helper=store", "fetch"}, {"git", "credential-store"}}},
		{name: "difftool command given by -c", command: "git -c difftool.x.cmd='git push origin main' difftool", want: [][]string{{"git", "-c", "difftool.x.cmd=git push origin main", "difftool"}, pushed}},
		{name: "mergetool command given by -c", command: "git -c mergetool.x.cmd='git push origin main' mergetool", want: [][]string{{"git", "-c", "mergetool.x.cmd=git push origin main", "mergetool"}, pushed}},
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

func TestParse_WhenAGitAliasRunsARebaseExec_ParsesTheExecCommand(t *testing.T) {
	t.Parallel()

	f := aliased(t)
	tests := []struct {
		name    string
		command string
	}{
		{name: "exec given after an alias of rebase", command: "git rb -x 'git push origin main'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parseWith(t, tt.command, callerIn(f, f.Dir()))

			assert.Check(t, is.Contains(texts(got), []string{"git", "push", "origin", "main"}), "Parse(%q)", tt.command)
		})
	}
}

func TestJudge_RefusesAGitCommandRunningAnArgumentItCannotRead(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkJudged(t, caller, []judged{
		{name: "bisect run", command: `git bisect run "$c"`, wantNamed: "bisect run", want: guard.Refuse},
		{name: "submodule foreach", command: `git submodule foreach "$c"`, wantNamed: "submodule foreach", want: guard.Refuse},
		{name: "difftool -x", command: `git difftool -x "$c" HEAD~1`, wantNamed: "-x", want: guard.Refuse},
		{name: "difftool --extcmd", command: `git difftool --extcmd="$c" HEAD~1`, wantNamed: "--extcmd", want: guard.Refuse},
		{name: "difftool word that could be an option", command: `git difftool "$o" HEAD~1`, wantNamed: "difftool", want: guard.Refuse},
		{name: "difftool short-flag bundle ending in a non-literal", command: `git difftool -y"$o" HEAD~1`, wantNamed: "difftool", want: guard.Refuse},
		{name: "difftool long option completed by a non-literal", command: `git difftool --e"$x" HEAD~1`, wantNamed: "difftool", want: guard.Refuse},
		{name: "filter-branch filter", command: `git filter-branch --index-filter "$c" HEAD`, wantNamed: "--index-filter", want: guard.Refuse},
		{name: "for-each-repo", command: `git for-each-repo --config=x.repo "$c"`, wantNamed: "for-each-repo", want: guard.Refuse},
	})
}

func TestJudge_AllowsAGitCommandRunningALiteralItAllows(t *testing.T) {
	t.Parallel()
	_, caller := standingOn(t, "main", "topic")

	checkJudged(t, caller, []judged{
		{name: "bisect run", command: "git bisect run make test", want: guard.Allow},
		{name: "submodule foreach", command: "git submodule foreach 'git status'", want: guard.Allow},
		{name: "difftool -x", command: "git difftool -x 'diff -u' HEAD~1", want: guard.Allow},
		{name: "difftool command given by -c", command: "git -c difftool.x.cmd='diff -u' difftool --tool=x HEAD~1", want: guard.Allow},
		{name: "difftool against a revision", command: "git difftool HEAD~1", want: guard.Allow},
		{name: "difftool with a tool", command: "git difftool --tool=meld", want: guard.Allow},
		{name: "difftool without prompting", command: "git difftool -y", want: guard.Allow},
		{name: "for-each-repo", command: "git for-each-repo --config=x.repo status", want: guard.Allow},
	})
}
