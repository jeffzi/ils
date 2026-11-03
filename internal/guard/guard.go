//go:build unix

package guard

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// readDeadline bounds every state read of one command, the calls to origin included, so the guard
// answers well before the hook that runs it times out.
const readDeadline = 5 * time.Second

// Outcome is what the guard decides about a command. The outcomes run from the mildest to the
// strictest, and a command takes the strictest outcome of its invocations.
type Outcome int

const (
	// Allow lets the command run.
	Allow Outcome = iota
	// Ask leaves the decision to the user instead of the guard.
	Ask
	// Refuse stops the command and shows the user the reason.
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

// rules returns the rules every invocation of one command is judged by, in the order they run.
// The trunk rule keeps what origin answered for the rest of the command, so each command takes a
// new set.
func rules() []rule {
	return []rule{
		refuseSkippedChecks, refuseDiscardingGit, refuseHistoryOverFile, refuseRebaseOverChanges,
		refuseRiskyWorktreeRemoval, refuseGuardedConfigWrite, refuseOriginRewrite, refusePush,
		trunks{}.refuseOnTrunk, judgeForge,
	}
}

// Judge decides whether command may run for caller. It refuses a command it cannot parse, an
// invocation Parse marked, and a command whose state it failed to read; otherwise every invocation
// is judged by the guard's rules. The first refusal decides; failing that, the first ask; and a
// command no rule objects to is allowed. Every state read of the command shares one deadline of
// readDeadline.
//
// A command with no git, gh, or ils invocation and no mark starts no subprocess.
func Judge(ctx context.Context, command string, caller Caller) Verdict {
	verdict, _ := Assess(ctx, command, caller)

	return verdict
}

// Assess judges command as Judge does, and also returns the invocations it judged, so that a
// caller showing them reads the command once: those Parse returns, and after each call of a
// function the command defines, the commands of the body it runs, placed where the call stands. A
// command that does not parse, or whose state Parse failed to read, returns no invocation.
func Assess(ctx context.Context, command string, caller Caller) (Verdict, []Invocation) {
	ctx, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()

	invocations, err := parse(ctx, command, caller)
	if failure, ok := errors.AsType[*ParseError](err); ok {
		return refuse("the guard cannot read the command: " + failure.Error()), nil
	}

	if err != nil {
		return failedRead(err), nil
	}

	checks := rules()
	verdict := Verdict{Outcome: Allow}
	for _, inv := range invocations {
		verdict = verdict.or(judgeInvocation(ctx, caller, inv, checks))
		if verdict.Outcome == Refuse {
			break
		}
	}

	return verdict, invocations
}

func judgeInvocation(ctx context.Context, caller Caller, inv Invocation, checks []rule) Verdict {
	if len(inv.Marks) > 0 {
		return refuse(markReason(inv, inv.Marks[0]))
	}

	verdict := Verdict{Outcome: Allow}
	for _, judgeBy := range checks {
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

// markReason says what the mark found on inv, by the mark's name when it has one. A tool handed
// to another command is named with that command. A git word the guard could not read as an alias
// because it cannot tell which repository git runs in says so.
func markReason(inv Invocation, mark Mark) string {
	detail := mark.Name
	switch {
	case mark.Kind == ToolArgument && len(inv.Words) > 0:
		detail = fmt.Sprintf("%s runs %s: run %s on its own, by its bare name", inv.Words[0].Text, mark.Name, mark.Name)
	case (mark.Kind == UnknownSubcommand || mark.Kind == UnreadableAlias) && inv.Git.GitDirUnknown &&
		inv.Git.Subcommand.Literal && isAliasName(mark.Name):
		detail = fmt.Sprintf("%s: the guard cannot tell which repository git runs in, so it cannot read its "+
			"aliases; %s", mark.Name, moveFirst)
	}

	if detail == "" {
		return string(mark.Kind)
	}

	return fmt.Sprintf("%s: %s", mark.Kind, detail)
}
