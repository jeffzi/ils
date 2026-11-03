//go:build unix

package guard_test

import (
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
)

func TestParse_ReportsWhetherHeadMayHaveMovedBeforeEachInvocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    []bool
	}{
		{name: "git switch", command: "git switch main; git commit", want: []bool{false, true}},
		{name: "symbolic-ref with a value", command: "git symbolic-ref HEAD refs/heads/main; git commit", want: []bool{false, true}},
		{name: "symbolic-ref read", command: "git symbolic-ref HEAD; git commit", want: []bool{false, false}},
		{name: "symbolic-ref short read", command: "git symbolic-ref --short HEAD; git commit", want: []bool{false, false}},
		{name: "branch -m", command: "git branch -m next; git commit", want: []bool{false, true}},
		{name: "branch create", command: "git branch next; git commit", want: []bool{false, false}},
		{name: "rebase naming a branch", command: "git rebase main topic; git commit", want: []bool{false, true}},
		{name: "rebase onto an upstream", command: "git rebase main; git commit", want: []bool{false, false}},
		{name: "rebase with a valued option", command: "git rebase --onto main base; git commit", want: []bool{false, false}},
		{name: "rebase --root naming a branch", command: "git rebase --root topic; git commit", want: []bool{false, true}},
		{name: "rebase --root naming no branch", command: "git rebase --root; git commit", want: []bool{false, false}},
		{name: "rebase short option taking a value", command: "git rebase -C 3 main; git commit", want: []bool{false, false}},
		{name: "rebase argument the guard cannot read", command: "git rebase --str main; git commit", want: []bool{false, true}},
		{name: "gh pr checkout", command: "gh pr checkout 12; git commit", want: []bool{false, true}},
		{name: "gh pr view", command: "gh pr view 12; git commit", want: []bool{false, false}},
		{name: "gh command word gh does not ship", command: "gh co 12; git commit", want: []bool{false, true}},
		{name: "prefix of branch --move", command: "git branch --mo next; git commit", want: []bool{false, true}},
		{name: "branch --m too short to name --move", command: "git branch --m next; git commit", want: []bool{false, false}},
		{name: "branch short-flag bundle without a move", command: "git branch -av; git commit", want: []bool{false, false}},
		{name: "branch long option sharing the move prefix", command: "git branch --merged; git commit", want: []bool{false, false}},
		{name: "branch -u bundle whose value holds an m", command: "git branch -um x; git commit", want: []bool{false, false}},
		{name: "branch -m before a -u in a short-flag bundle", command: "git branch -mu x; git commit", want: []bool{false, true}},
		{name: "option that prints and exits", command: "git --version; git commit", want: []bool{false, false}},
		{name: "pipeline stage that moves HEAD", command: "git switch x | cat; git commit", want: []bool{true, true, true}},
		{name: "pipeline stage after the commit moving HEAD", command: "git commit | git switch x", want: []bool{true, true}},
		{name: "pipeline whose stages move nothing", command: "git log | cat; git commit", want: []bool{false, false, false}},
		{name: "background job that moves HEAD", command: "git switch x & git commit", want: []bool{true, true}},
		{name: "background job that moves nothing", command: "git log & git commit", want: []bool{false, false}},
		{name: "background job beside a later command that moves HEAD", command: "git commit & git switch main", want: []bool{true, false}},
		{name: "background job beside a command further down the list that moves HEAD", command: "git commit & git log; git switch main", want: []bool{true, false, false}},
		{name: "background job beside a group that moves HEAD", command: "git commit & { git switch main; }", want: []bool{true, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := heads(parse(t, tt.command))

			assert.Check(t, is.DeepEqual(got, tt.want), "Parse(%q) head unknown", tt.command)
		})
	}
}

func TestParse_WhenAnInvocationMayMoveHeadThroughGitsAliases_ReportsHeadUnknownAfterIt(t *testing.T) {
	t.Parallel()

	f := aliased(t)
	tests := []struct {
		name    string
		command string
		want    []bool
	}{
		{name: "alias of switch", command: "git sw main && git commit -m x", want: []bool{false, true}},
		{name: "alias that moves nothing", command: "git lg; git commit", want: []bool{false, false}},
		{name: "alias running a shell command", command: "git sh; git commit", want: []bool{false, true}},
		{name: "word that is neither command nor alias", command: "git taxi; git commit", want: []bool{false, true}},
		{name: "alias nested too deep", command: "git d1; git commit", want: []bool{false, true}},
		{name: "alias the guard cannot read", command: "git unclosed; git commit", want: []bool{false, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := heads(parseWith(t, tt.command, callerIn(f, f.Dir())))

			assert.Check(t, is.DeepEqual(got, tt.want), "Parse(%q) head unknown", tt.command)
		})
	}
}

func TestParse_WhenACommandMayWriteTheGitDirsHead_ReportsHeadUnknownAfterIt(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	gitDir := filepath.Join(f.Dir(), ".git")
	tests := []struct {
		name    string
		dir     string
		command string
		want    []bool
	}{
		{name: "redirect", dir: f.Dir(), command: "echo ref: refs/heads/x > .git/HEAD; git commit", want: []bool{false, true}},
		{name: "redirect to an absolute path", dir: f.Dir(), command: "echo x > {dir}/.git/HEAD; git commit", want: []bool{false, true}},
		{name: "redirect to a target that is not a literal", dir: f.Dir(), command: `echo x > "$where"; git commit`, want: []bool{false, true}},
		{name: "redirect to a work tree file named HEAD", dir: f.Dir(), command: "echo x > HEAD; git commit", want: []bool{false, false}},
		{name: "cp onto it", dir: f.Dir(), command: "cp saved .git/HEAD; git commit", want: []bool{false, true}},
		{name: "cp onto a destination that is not a literal", dir: f.Dir(), command: `cp saved "$where"; git commit`, want: []bool{false, true}},
		{name: "cp reading it", dir: f.Dir(), command: "cp .git/HEAD saved; git commit", want: []bool{false, false}},
		{name: "mv found by the equals lookup moving a HEAD file into it", dir: f.Dir(), command: "=mv HEAD .git; git commit -m x", want: []bool{false, true}},
		{name: "cp found by the equals lookup copying a HEAD file into it", dir: f.Dir(), command: "=cp -t .git HEAD; git commit -m x", want: []bool{false, true}},
		{name: "install -m value holding a t", dir: f.Dir(), command: "install -mt.git HEAD saved; git commit", want: []bool{false, false}},
		{name: "tee onto it", dir: f.Dir(), command: "tee .git/HEAD < saved; git commit", want: []bool{false, true}},
		{name: "dd writing HEAD through of= from the git dir", dir: gitDir, command: "dd if=saved of=HEAD; git commit", want: []bool{false, true}},
		{name: "ln -s linking to it", dir: f.Dir(), command: "ln -s .git/HEAD h; git commit", want: []bool{false, true}},
		{name: "command known only to read it", dir: f.Dir(), command: "grep ref .git/HEAD; git commit", want: []bool{false, false}},
		{name: "git given it as an argument", dir: f.Dir(), command: "git hash-object .git/HEAD; git commit", want: []bool{false, false}},
		{name: "sed editing another file in place", dir: f.Dir(), command: "sed -i s,a,b, notes; git commit", want: []bool{false, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			command := strings.ReplaceAll(tt.command, "{dir}", f.Dir())

			got := heads(parseWith(t, command, callerIn(f, tt.dir)))

			assert.Check(t, is.DeepEqual(got, tt.want), "Parse(%q) head unknown", command)
		})
	}
}

func TestJudge_WhenAnAndListSwitchesOffTheTrunk_AllowsWhatFollowsOnTheNamedBranch(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "main")

	checkAllowed(t, caller, []namedCommand{
		{name: "switch to a branch", command: "git switch topic && " + commitCommand},
		{name: "switch -c", command: "git switch -c topic && " + commitCommand},
		{name: "switch --create", command: "git switch --create topic && " + commitCommand},
		{name: "switch -C", command: "git switch -C topic && " + commitCommand},
		{name: "switch --force-create", command: "git switch --force-create topic && " + commitCommand},
		{name: "switch -c with a start point", command: "git switch -c topic main && " + commitCommand},
		{name: "squash merge after the switch", command: "git switch topic && git merge --squash other"},
		{name: "commit further down the list", command: "git switch -c topic && git add -A && " + commitCommand},
	})
}

func TestJudge_WhenAnAndListSwitchesToTheTrunk_RefusesACommitAfterIt(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")

	checkJudgedIn(t, caller, []placed{
		{name: "switch to the trunk", dir: f.Dir(), command: "git switch main && " + commitCommand},
		{name: "switch -C onto the trunk", dir: f.Dir(), command: "git switch -C main && " + commitCommand},
		{name: "later switch to the trunk", dir: f.Dir(), command: "git switch -c next && git switch main && " + commitCommand},
	}, expectation{outcome: guard.Refuse, named: "main"})
}

func TestJudge_WhenHeadMovesWhereTheGuardCannotFollowIt_RefusesALaterCommit(t *testing.T) {
	t.Parallel()

	f, caller := standingOn(t, "main", "topic")
	other, _ := standingOn(t, "main", "topic")
	dir := f.Dir()

	checkJudgedIn(t, caller, []placed{
		{name: "switch joined by ;", dir: dir, command: "git switch topic; " + commitCommand},
		{name: "switch joined by ||", dir: dir, command: "git switch topic || " + commitCommand},
		{name: "switch piped into the list", dir: dir, command: "git switch topic | cat && " + commitCommand},
		{name: "switch in the background", dir: dir, command: "git switch topic & " + commitCommand},
		{name: "switch -c naming a non-literal", dir: dir, command: `git switch -c "$b" && ` + commitCommand},
		{name: "switch to the previous branch", dir: dir, command: "git switch - && " + commitCommand},
		{name: "switch --detach", dir: dir, command: "git switch --detach topic && " + commitCommand},
		{name: "switch in another git dir", dir: dir, command: "git -C " + quotedPath(other.Dir()) + " switch -c topic && " + commitCommand},
		{name: "switch inside a function body", dir: dir, command: "f() { git switch topic && :; } && " + commitCommand},
		{name: "switch inside a subshell", dir: dir, command: "(git switch topic && :) && " + commitCommand},
		{name: "switch inside a loop", dir: dir, command: "for x in a; do git switch topic && :; done && " + commitCommand},
		{name: "move after the switch", dir: dir, command: "git switch -c topic && gh pr checkout 12 && " + commitCommand},
		{name: "rebase naming a branch", dir: dir, command: "git rebase main topic && " + commitCommand},
		{name: "bisect", dir: dir, command: "git bisect bad && " + commitCommand},
		{name: "branch -m", dir: dir, command: "git branch -m topic && " + commitCommand},
		{name: "symbolic-ref with a value", dir: dir, command: "git symbolic-ref HEAD refs/heads/topic && " + commitCommand},
		{name: "gh pr checkout", dir: dir, command: "gh pr checkout 12 && " + commitCommand},
		{name: "switch through a function named git", dir: dir, command: "git() { true; }; git switch -c topic && command " + commitCommand},
		{name: "call of a function whose body moves HEAD", dir: dir, command: "f() { git switch main; }; git switch -c topic && f && " + commitCommand},
		{name: "function declared after the switch", dir: dir, command: "git switch -c topic && f() { " + commitCommand + "; }"},
	}, expectation{outcome: guard.Refuse, named: "cannot tell"})
}
