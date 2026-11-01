//go:build unix

package guard

import "context"

// Rule is the check a test hands JudgeWith in place of the guard's own rules.
type Rule = rule

// JudgeWith judges a command under the rules it is given instead of the guard's own.
func JudgeWith(ctx context.Context, command string, caller Caller, rules []Rule) Verdict {
	verdict, _ := judge(ctx, command, caller, rules)

	return verdict
}
