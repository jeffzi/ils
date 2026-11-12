//go:build unix

package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
)

type headRow struct {
	name    string
	command string
	want    []bool
}

// checkHeads fails unless Parse, run by caller, reports for each row's command the head-unknown
// flags the row wants.
func checkHeads(t *testing.T, caller guard.Caller, tests []headRow) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := heads(parseWith(t, tt.command, caller))

			assert.Check(t, is.DeepEqual(got, tt.want), "Parse(%q) head unknown", tt.command)
		})
	}
}

func TestParse_ReportsWhetherHeadMayHaveMovedBeforeEachInvocation(t *testing.T) {
	t.Parallel()

	checkHeads(t, readerCaller(), []headRow{
		{name: "git switch", command: "git switch main; git commit", want: []bool{false, true}},
		{name: "symbolic-ref with a value", command: "git symbolic-ref HEAD refs/heads/main; git commit", want: []bool{false, true}},
		{name: "symbolic-ref read", command: "git symbolic-ref HEAD; git commit", want: []bool{false, false}},
		{name: "branch -m", command: "git branch -m next; git commit", want: []bool{false, true}},
		{name: "branch create", command: "git branch next; git commit", want: []bool{false, false}},
		{name: "rebase naming a branch", command: "git rebase main topic; git commit", want: []bool{false, true}},
		{name: "rebase onto an upstream", command: "git rebase main; git commit", want: []bool{false, false}},
		{name: "rebase with a valued option", command: "git rebase --onto main base; git commit", want: []bool{false, false}},
		{name: "rebase --root naming a branch", command: "git rebase --root topic; git commit", want: []bool{false, true}},
		{name: "rebase --root naming no branch", command: "git rebase --root; git commit", want: []bool{false, false}},
		{name: "rebase argument the guard cannot read", command: "git rebase --str main; git commit", want: []bool{false, true}},
		{name: "gh pr checkout", command: "gh pr checkout 12; git commit", want: []bool{false, true}},
		{name: "gh pr co, gh's own alias of checkout", command: "gh pr co 12; git commit", want: []bool{false, true}},
		{name: "gh pr view", command: "gh pr view 12; git commit", want: []bool{false, false}},
		{name: "gh command word gh does not ship", command: "gh co 12; git commit", want: []bool{false, true}},
		{name: "branch --m too short to name --move", command: "git branch --m next; git commit", want: []bool{false, false}},
		{name: "branch long option sharing the move prefix", command: "git branch --merged; git commit", want: []bool{false, false}},
		{name: "branch -u bundle whose value holds an m", command: "git branch -um x; git commit", want: []bool{false, false}},
		{name: "branch -m before a -u in a short-flag bundle", command: "git branch -mu x; git commit", want: []bool{false, true}},
		{name: "branch --move", command: "git branch --move old new; git commit", want: []bool{false, true}},
		{name: "branch -M", command: "git branch -M old new; git commit", want: []bool{false, true}},
		{name: "branch -m after a flag in a short-flag bundle", command: "git branch -fm old new; git commit", want: []bool{false, true}},
		{name: "branch -m as the value of a short option", command: "git branch -u -m; git commit", want: []bool{false, false}},
		{name: "branch option the guard cannot read", command: "git branch $f old new; git commit", want: []bool{false, true}},
		{name: "branch option the guard cannot read before an ambiguous one", command: "git branch $f --m; git commit", want: []bool{false, true}},
		{name: "rebase of a glob that may expand to the upstream and the branch", command: "git rebase m*; git commit", want: []bool{false, true}},
		{name: "option that prints and exits", command: "git --version; git commit", want: []bool{false, false}},
		{name: "pipeline stage that moves HEAD before an and list", command: "git switch x | cat && git commit", want: []bool{false, false, false}},
		{name: "background job that moves HEAD", command: "git switch x & git commit", want: []bool{false, false}},
	})
}

func TestParse_WhenAnInvocationMayMoveHeadThroughGitsAliases_ReportsHeadUnknownAfterIt(t *testing.T) {
	t.Parallel()

	f := aliased(t)

	checkHeads(t, callerIn(f, f.Dir()), []headRow{
		{name: "alias of switch", command: "git sw main && git commit -m x", want: []bool{false, true}},
		{name: "alias that moves nothing", command: "git lg; git commit", want: []bool{false, false}},
		{name: "alias running a shell command", command: "git sh; git commit", want: []bool{false, true}},
	})
}

func TestJudge_WhenABackgroundJobSwitchesBranch_AllowsACommitAfterIt(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")

	checkAllowed(t, caller, []namedCommand{
		{name: "switch to the trunk in a script git runs in the background", command: "git rebase --exec 'git switch main' HEAD & git commit -m x"},
	})
}

func TestJudge_WhenAnAndListSwitchesOffTheTrunk_AllowsWhatFollowsOnTheNamedBranch(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "main")

	checkAllowed(t, caller, []namedCommand{
		{name: "switch to a branch", command: "git switch topic && " + commitCommand},
		{name: "switch -c", command: "git switch -c topic && " + commitCommand},
		{name: "switch -c with a start point", command: "git switch -c topic main && " + commitCommand},
		{name: "squash merge after the switch", command: "git switch topic && git merge --squash other"},
		{name: "commit further down the list", command: "git switch -c topic && git add -A && " + commitCommand},
	})
}

func TestJudge_WhenAnAndListSwitchesToTheTrunk_RefusesACommitAfterIt(t *testing.T) {
	// Not parallel: protectTrunk sets GH_PATH for the whole process.
	f, caller := standingOn(t, "main", "topic")
	protectTrunk(t, "main", f)

	checkJudgedInInTurn(t, caller, []placed{
		{name: "switch to the trunk", command: "git switch main && " + commitCommand},
		{name: "later switch to the trunk", command: "git switch -c next && git switch main && " + commitCommand},
	}, expectation{outcome: guard.Refuse, named: "main"})
}

func TestJudge_WhenHeadMovesWhereTheGuardCannotFollowIt_RefusesALaterCommit(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")
	other, _ := standingOn(t, "main", "topic")

	checkJudgedIn(t, caller, []placed{
		{name: "switch joined by ||", command: "git switch topic || " + commitCommand},
		{name: "switch -c naming a non-literal", command: `git switch -c "$b" && ` + commitCommand},
		{name: "switch to the previous branch", command: "git switch - && " + commitCommand},
		{name: "switch to a branch the reflog names", command: "git switch @{-1} && " + commitCommand},
		{name: "switch in another git dir", command: "git -C " + gittest.ShellWord(other.Dir()) + " switch -c topic && " + commitCommand},
		{name: "switch inside a function body", command: "f() { git switch topic && :; } && " + commitCommand},
		{name: "switch inside a subshell", command: "(git switch topic && :) && " + commitCommand},
		{name: "switch inside a loop", command: "for x in a; do git switch topic && :; done && " + commitCommand},
		{name: "move after the switch", command: "git switch -c topic && gh pr checkout 12 && " + commitCommand},
		{name: "bisect", command: "git bisect bad && " + commitCommand},
		{name: "switch through a function named git", command: "git() { true; }; git switch -c topic && command " + commitCommand},
		{name: "call of a function whose body moves HEAD", command: "f() { git switch main; }; git switch -c topic && f && " + commitCommand},
		{name: "function declared after the switch", command: "git switch -c topic && f() { " + commitCommand + "; }"},
	}, expectation{outcome: guard.Refuse, named: "cannot tell"})
}
