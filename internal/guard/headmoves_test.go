//go:build unix

package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

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
		{name: "symbolic-ref with a reason", command: "git symbolic-ref -m why HEAD refs/heads/main; git commit", want: []bool{false, true}},
		{name: "symbolic-ref with an attached reason", command: "git symbolic-ref -mx HEAD refs/heads/main; git commit", want: []bool{false, true}},
		{name: "symbolic-ref short read", command: "git symbolic-ref --short HEAD; git commit", want: []bool{false, false}},
		{name: "branch -m", command: "git branch -m next; git commit", want: []bool{false, true}},
		{name: "branch create", command: "git branch next; git commit", want: []bool{false, false}},
		{name: "rebase naming a branch", command: "git rebase main topic; git commit", want: []bool{false, true}},
		{name: "rebase onto an upstream", command: "git rebase main; git commit", want: []bool{false, false}},
		{name: "rebase with a valued option", command: "git rebase --onto main base; git commit", want: []bool{false, false}},
		{name: "rebase --root naming a branch", command: "git rebase --root topic; git commit", want: []bool{false, true}},
		{name: "rebase --root naming no branch", command: "git rebase --root; git commit", want: []bool{false, false}},
		{name: "rebase short option taking a value", command: "git rebase -C 3 main; git commit", want: []bool{false, false}},
		{name: "rebase prefix of a valued long option", command: "git rebase --ont main base; git commit", want: []bool{false, false}},
		{name: "rebase prefix of --root naming a branch", command: "git rebase --ro topic; git commit", want: []bool{false, true}},
		{name: "rebase argument the guard cannot read", command: "git rebase --str main; git commit", want: []bool{false, true}},
		{name: "gh pr checkout", command: "gh pr checkout 12; git commit", want: []bool{false, true}},
		{name: "gh pr view", command: "gh pr view 12; git commit", want: []bool{false, false}},
		{name: "gh command word gh does not ship", command: "gh co 12; git commit", want: []bool{false, true}},
		{name: "branch -M leading a short-flag bundle", command: "git branch -Mf next; git commit", want: []bool{false, true}},
		{name: "branch -m after the first flag of a short-flag bundle", command: "git branch -fm next; git commit", want: []bool{false, true}},
		{name: "prefix of branch --move", command: "git branch --mo next; git commit", want: []bool{false, true}},
		{name: "branch --m too short to name --move", command: "git branch --m next; git commit", want: []bool{false, false}},
		{name: "branch short-flag bundle without a move", command: "git branch -av; git commit", want: []bool{false, false}},
		{name: "branch long option sharing the move prefix", command: "git branch --merged; git commit", want: []bool{false, false}},
		{name: "branch -u bundle whose value holds an m", command: "git branch -um x; git commit", want: []bool{false, false}},
		{name: "option that prints and exits", command: "git --version; git commit", want: []bool{false, false}},
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
