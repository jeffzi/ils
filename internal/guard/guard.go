package guard

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// Outcome is what the guard decides about a command. The outcomes run from the mildest to the
// strictest, and a command takes the strictest outcome of its invocations.
type Outcome int

// The outcomes of a verdict, mildest first. Ask leaves the decision to the user instead of the
// guard.
const (
	Allow Outcome = iota
	Ask
	Refuse
)

// Verdict is the guard's outcome for a command, with its reason written for the user. An allowed
// command carries no reason.
type Verdict struct {
	Reason  string
	Outcome Outcome
}

func refuse(reason string) Verdict {
	return Verdict{Outcome: Refuse, Reason: reason}
}

// rule judges one invocation of a command. A rule that cannot read the state it needs returns the
// error, and the guard refuses the command saying what broke.
type rule func(ctx context.Context, caller Caller, inv Invocation) (Verdict, error)

// rules returns the rules every invocation is judged by, in the order they run.
func rules() []rule {
	return []rule{
		refuseSkippedChecks, refuseDiscardingGit, refuseHistoryOverFile, refuseRebaseOverChanges,
		refuseRiskyWorktreeRemoval, refuseGuardedConfigWrite, refuseOriginRewrite,
	}
}

// Judge decides whether command may run for caller. It refuses a command it cannot parse, an
// invocation Parse marked, and a command whose state it failed to read; otherwise every invocation
// is judged by the guard's rules. The first refusal decides; failing that, the first ask; and a
// command no rule objects to is allowed.
//
// A command with no git, gh, or ils invocation and no mark starts no subprocess.
func Judge(ctx context.Context, command string, caller Caller) Verdict {
	return judge(ctx, command, caller, rules())
}

func judge(ctx context.Context, command string, caller Caller, rules []rule) Verdict {
	invocations, err := Parse(ctx, command, caller)
	if failure, ok := errors.AsType[*ParseError](err); ok {
		return refuse("the guard cannot read the command: " + failure.Error())
	}

	if err != nil {
		return failedRead(err)
	}

	verdict := Verdict{Outcome: Allow}
	for _, inv := range invocations {
		verdict = verdict.or(judgeInvocation(ctx, caller, inv, rules))
		if verdict.Outcome == Refuse {
			break
		}
	}

	return verdict
}

func judgeInvocation(ctx context.Context, caller Caller, inv Invocation, rules []rule) Verdict {
	if len(inv.Marks) > 0 {
		return refuse(markReason(inv, inv.Marks[0]))
	}

	verdict := Verdict{Outcome: Allow}
	for _, judgeBy := range rules {
		next, err := judgeBy(ctx, caller, inv)
		if err != nil {
			return failedRead(err)
		}

		verdict = verdict.or(next)
		if verdict.Outcome == Refuse {
			break
		}
	}

	return verdict
}

// or returns v unless other is stricter, so that the first verdict of the strictest outcome wins.
func (v Verdict) or(other Verdict) Verdict {
	if other.Outcome > v.Outcome {
		return other
	}

	return v
}

func failedRead(err error) Verdict {
	return refuse("the guard could not check the command: " + err.Error())
}

// markReason says what the mark found on inv, by the mark's name when it has one. A tool word
// handed to another command is named with that command.
func markReason(inv Invocation, mark Mark) string {
	name := mark.Name
	if mark.Kind == ToolArgument && len(inv.Words) > 0 {
		name = inv.Words[0].Text
		at := slices.IndexFunc(inv.Words[1:], func(w Word) bool { return isToolWord(w, inv.Dir) })
		if at >= 0 {
			name = fmt.Sprintf("%s runs %s", name, inv.Words[1+at].Text)
		}
	}

	if name == "" {
		return string(mark.Kind)
	}

	return fmt.Sprintf("%s: %s", mark.Kind, name)
}
