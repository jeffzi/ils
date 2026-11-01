//go:build unix

package guard_test

import "testing"

func TestJudge_RefusesEveryPush(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "plain push", command: "git push", wantNamed: "git push"},
	})
}
