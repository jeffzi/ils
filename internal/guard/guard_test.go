//go:build unix

package guard_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
)

// byFirstWord asks or refuses an invocation whose first word says so, with its second word as the
// reason; it fails one whose first word is fail, with its second word as the error.
func byFirstWord(_ context.Context, _ guard.Caller, inv guard.Invocation) (guard.Verdict, error) {
	if len(inv.Words) < 2 {
		return guard.Verdict{Outcome: guard.Allow}, nil
	}

	reason := inv.Words[1].Text
	switch inv.Words[0].Text {
	case "ask":
		return guard.Verdict{Outcome: guard.Ask, Reason: reason}, nil
	case "refuse":
		return guard.Verdict{Outcome: guard.Refuse, Reason: reason}, nil
	case "fail":
		return guard.Verdict{}, errors.New(reason)
	}

	return guard.Verdict{Outcome: guard.Allow}, nil
}

func TestJudge_CombinesTheVerdictsOfItsInvocations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    guard.Verdict
	}{
		{name: "every invocation allowed", command: "echo a; echo b", want: guard.Verdict{Outcome: guard.Allow}},
		{name: "one invocation asking", command: "echo a; ask why", want: guard.Verdict{Outcome: guard.Ask, Reason: "why"}},
		{name: "two invocations asking", command: "ask first; ask second", want: guard.Verdict{Outcome: guard.Ask, Reason: "first"}},
		{name: "refusal after an ask", command: "ask why; refuse never", want: guard.Verdict{Outcome: guard.Refuse, Reason: "never"}},
		{name: "two refusals", command: "refuse first; refuse second", want: guard.Verdict{Outcome: guard.Refuse, Reason: "first"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := guard.JudgeWith(t.Context(), tt.command, guard.Caller{Home: home, Dir: start}, []guard.Rule{byFirstWord})

			assert.Check(t, is.DeepEqual(got, tt.want), "Judge(%q)", tt.command)
		})
	}
}

func TestJudge_WhenARuleFails_RefusesSayingWhatBroke(t *testing.T) {
	t.Parallel()
	const command = "echo a; fail disk"

	got := guard.JudgeWith(t.Context(), command, guard.Caller{Home: home, Dir: start}, []guard.Rule{byFirstWord})

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Contains(got.Reason, "disk"), "Judge(%q) reason", command)
}

func TestJudge_WhenTheCommandCannotBeParsed_RefusesNamingWhatFailed(t *testing.T) {
	t.Parallel()
	const command = "echo ok; rm -rf / )"
	caller := guard.Caller{Home: home, Dir: start}
	_, err := guard.Parse(t.Context(), command, caller)
	failure, ok := errors.AsType[*guard.ParseError](err)
	assert.Assert(t, ok, "Parse(%q) error = %v, want a *guard.ParseError", command, err)

	got := guard.Judge(t.Context(), command, caller)

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Contains(got.Reason, failure.Message), "Judge(%q) reason", command)
}

func TestJudge_RefusesAMarkNamingWhatItFound(t *testing.T) {
	t.Parallel()

	_, caller := standingOn(t, "main", "topic")
	tests := []struct {
		name      string
		command   string
		wantKind  guard.MarkKind
		wantNamed string
	}{
		{name: "mark without a name", command: "echo @(a|b)", wantKind: guard.ExtendedGlob},
		{name: "git variable", command: "export GIT_CONFIG_GLOBAL=/tmp/c", wantKind: guard.GitVariable, wantNamed: "GIT_CONFIG_GLOBAL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := guard.Judge(t.Context(), tt.command, caller)

			assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", tt.command)
			assert.Check(t, is.Contains(got.Reason, string(tt.wantKind)), "Judge(%q) reason", tt.command)
			assert.Check(t, is.Contains(got.Reason, tt.wantNamed), "Judge(%q) reason", tt.command)
		})
	}
}

func TestJudge_WhenAToolWordIsHandedToAnotherCommand_RefusesNamingBoth(t *testing.T) {
	t.Parallel()
	callerDir := t.TempDir()
	writeExecutable(t, filepath.Join(callerDir, "bin", "git"))
	const command = "xargs ./bin/git push"

	got := guard.Judge(t.Context(), command, guard.Caller{Home: home, Dir: callerDir})

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Contains(got.Reason, "xargs"), "Judge(%q) reason", command)
	assert.Check(t, is.Contains(got.Reason, "./bin/git"), "Judge(%q) reason", command)
}

func TestJudge_WhenNoInvocationRunsATool_AllowsWithoutStartingGit(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	recorder := &git.Recorder{}
	const command = "ls -la | grep x; echo git push"

	got := guard.Judge(t.Context(), command, guard.Caller{Home: home, Dir: f.Dir(), Git: git.Runner{Env: f.Env(), Recorder: recorder}})

	assert.Check(t, is.DeepEqual(got, guard.Verdict{Outcome: guard.Allow}), "Judge(%q)", command)
	assert.Check(t, is.Len(recorder.Calls(), 0), "Judge(%q) git calls", command)
}

func TestJudge_WhenAGitReadFails_RefusesSayingWhatBroke(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := aliased(t)
	env := append(f.Env(), gittest.FakeGit(t, gittest.Fake{FailOn: []string{"config"}})...)
	const command = "git co topic"

	got := guard.Judge(t.Context(), command, callerWithEnv(f.Dir(), env))

	assert.Check(t, is.Equal(got.Outcome, guard.Refuse), "Judge(%q) outcome", command)
	assert.Check(t, is.Contains(got.Reason, gittest.FailedSaid), "Judge(%q) reason", command)
}
