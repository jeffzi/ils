//go:build unix

package guard_test

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/guard"
)

func wordsOf(invocations []guard.Invocation) []guard.Word {
	var got []guard.Word
	for _, inv := range invocations {
		got = append(got, inv.Words...)
	}

	return got
}

func TestParse_WhenTheCommandAssignedAVariableALiteral_ReadsItsExpansionAsThatLiteral(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    string
	}{
		{name: "plain assignment", command: "S=/tmp/work; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "expansion in braces followed by a colon", command: "S=/tmp/work; ${S}:h/run.sh", want: "/tmp/work:h/run.sh"},
		{name: "quoted expansion of a value holding a space", command: `S='/tmp/my work'; "$S/run.sh"`, want: "/tmp/my work/run.sh"},
		{name: "value starting with a tilde", command: "S=~/work; $S/run.sh", want: home + "/work/run.sh"},
		{name: "export", command: "export S=/tmp/work; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "readonly", command: "readonly S=/tmp/work; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "readonly after a plain assignment", command: "S=/srv; readonly S=/tmp/work; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "local inside its function body", command: "f() { local S=/tmp/work; $S/run.sh; }; f", want: "/tmp/work/run.sh"},
		{name: "later literal assignment", command: "S=/srv; S=/tmp/work; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "assignment that always ran before an and list", command: "S=/tmp/work && $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "loop body after the assignment", command: `S=/tmp/work; for f in a b; do $S/run.sh "$f"; done`, want: "/tmp/work/run.sh"},
		{name: "prefix of the command reading the earlier value", command: "S=/tmp/work; S=/srv $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "argument of a command", command: "S=/tmp/work; ls $S", want: "/tmp/work"},
		{name: "program run between", command: "S=/tmp/work; mkdir -p $S; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "builtin that writes no variable run between", command: "S=/tmp/work; echo x; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "word before an eval string declaring a name reference", command: "S=/tmp/work; $S/run.sh; eval 'declare -n R=T'", want: "/tmp/work/run.sh"},
		{name: "quoted expansion of a for loop value holding a space", command: `for S in '/tmp/my work'; do "$S/run.sh"; done`, want: "/tmp/my work/run.sh"},
		{name: "set of options and an option name run between", command: "S=/tmp/work; set -euo pipefail; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "set turning an option off run between", command: "S=/tmp/work; set +x; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "shopt run between", command: "S=/tmp/work; shopt -s nullglob; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "bare wait run between", command: "S=/tmp/work; wait; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "wait for a job run between", command: "S=/tmp/work; wait %1; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "trap on EXIT run between", command: "S=/tmp/work; trap 'echo done' EXIT; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "trap resetting a signal run between", command: "S=/tmp/work; trap - INT; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "trap on exit and interrupt signals run between", command: "S=/tmp/work; trap 'rm -f t' EXIT SIGINT term HUP 0; $S/run.sh", want: "/tmp/work/run.sh"},
		{name: "trap listing its traps run between", command: "S=/tmp/work; trap -p; $S/run.sh", want: "/tmp/work/run.sh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Check(t, is.Contains(wordsOf(got), literal(tt.want)), "Parse(%q) words", tt.command)
		})
	}
}

func TestParse_WhenAReadonlyMayNotGiveAVariableItsValue_ReadsItsExpansionUnknown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "assignment to a readonly name", command: "readonly S=/srv; S=/tmp/work; $S/run.sh"},
		{name: "readonly after a readonly whose value is not a literal", command: "readonly S=$(pwd); readonly S=/tmp/work; $S/run.sh"},
		{name: "readonly after declare -r", command: "declare -r S=$(pwd); readonly S=/tmp/work; $S/run.sh"},
		{name: "readonly after a readonly in an if body", command: "if test -d x; then readonly S=/srv; fi; readonly S=/tmp/work; $S/run.sh"},
		{name: "readonly after a readonly an eval string runs", command: "eval 'readonly S=$(pwd)'; readonly S=/tmp/work; $S/run.sh"},
		{name: "assignment to a name builtin readonly made readonly", command: "builtin readonly S=/srv; S=/tmp/work; $S/run.sh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			checkRunShReadUnknown(t, tt.command, got)
		})
	}
}

func TestParse_WhenTheShellMaySetAVariableOnItsOwn_ReadsItsExpansionUnknown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "builtin that writes a named variable", command: "S=/tmp/work; print -v S /srv; $S/run.sh"},
		{name: "printf given a format that is not a literal", command: `S=/tmp/work; printf "$F" S /srv; $S/run.sh`},
		{name: "read of IFS before the assignment", command: "read IFS <<< ,; S=/tmp/work,x; $S/run.sh"},
		{name: "for loop over IFS before the assignment", command: "for IFS in ,; do :; done; S=/tmp/work,x; $S/run.sh"},
		{name: "builtin naming IFS before the assignment", command: "print -v IFS ,; S=/tmp/work,x; $S/run.sh"},
		{name: "zsh match variable after a regex test", command: "MATCH=/tmp/work; [[ a =~ a ]]; $MATCH/run.sh"},
		{name: "zsh set -A writing an array", command: "S=/tmp/work; set -A S /srv; $S/run.sh"},
		{name: "zsh set +A writing an array", command: "S=/tmp/work; set +A S /srv; $S/run.sh"},
		{name: "wait -p writing the job id", command: "S=/tmp/work; wait -p S; $S/run.sh"},
		{name: "trap on DEBUG running before later commands", command: "S=/tmp/work; trap 'S=/srv' DEBUG; $S/run.sh"},
		{name: "trap on RETURN running before later commands", command: "S=/tmp/work; trap 'S=/srv' RETURN; $S/run.sh"},
		{name: "trap on ERR running before later commands", command: "S=/tmp/work; trap 'S=/srv' err; $S/run.sh"},
		{name: "zsh trap on ZERR running before later commands", command: "S=/tmp/work; trap 'S=/srv' ZERR; $S/run.sh"},
		{name: "set given an argument that is not a literal", command: `S=/tmp/work; set "$O" S /srv; $S/run.sh`},
		{name: "wait given an argument that is not a literal", command: `S=/tmp/work; wait "$O" S; $S/run.sh`},
		{name: "trap given an action that is not a literal", command: `S=/tmp/work; trap "$H" EXIT; $S/run.sh`},
		{name: "trap given a signal that is not a literal", command: `S=/tmp/work; trap 'S=/srv' "$SIG"; $S/run.sh`},
		{name: "trap on a child exit running between later commands", command: "S=/tmp/work; trap 'S=/srv' CHLD; true; $S/run.sh"},
		{name: "zsh trap on a debug signal spelled with its prefix", command: "S=/tmp/work; trap 'S=/srv' SIGDEBUG; $S/run.sh"},
		{name: "trap on a signal given by number", command: "S=/tmp/work; trap 'S=/srv' 13; $S/run.sh"},
		{name: "shopt given an argument that is not a literal", command: `S=/tmp/work; shopt -s "$opt"; $S/run.sh`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			checkRunShReadUnknown(t, tt.command, got)
		})
	}
}

// checkRunShReadUnknown checks that got, the invocations Parse returns for command, read no command
// word as a literal run.sh and read at least one command word unknown.
func checkRunShReadUnknown(t *testing.T, command string, got []guard.Invocation) {
	t.Helper()

	var unread bool
	for _, inv := range got {
		if len(inv.Words) == 0 {
			continue
		}
		first := inv.Words[0]
		unread = unread || !first.Literal
		assert.Check(t, !first.Literal || !strings.HasSuffix(first.Text, "run.sh"), "Parse(%q) reads %q", command, first.Text)
	}
	assert.Check(t, unread, "Parse(%q) = %v, want a command word read unknown", command, texts(got))
}

func TestJudge_WhenAVariableTheCommandAssignedHoldsAGuardedWord_RefusesAsThatWord(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "command word", command: "G=git; $G push", wantNamed: "git push"},
		{name: "option of git clean", command: "F=--force; git clean $F", wantNamed: "deletes untracked files"},
	})
}
