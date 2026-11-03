//go:build unix

package guard_test

import "testing"

func TestJudge_RefusesEveryPush(t *testing.T) {
	t.Parallel()

	checkRefusals(t, []refusal{
		{name: "plain push", command: "git push", wantNamed: "git push"},
		{name: "send-pack", command: "git send-pack origin main", wantNamed: "git send-pack"},
		{name: "http-push", command: "git http-push https://example.com/repo.git main", wantNamed: "git http-push"},
	})
}
