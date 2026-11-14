//go:build unix

package guard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// readDeadline bounds every state read of one command, the calls to origin included, so the guard
// answers well before the hook that runs it times out.
const readDeadline = 5 * time.Second

// Outcome is what the guard decides about a command. The outcomes run from the mildest to the
// strictest, and a command takes the strictest outcome of its invocations.
type Outcome int

const (
	// Allow is the zero value, so a Verdict that sets no outcome lets the command run.
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
	// Explicit marks an allowed command the guard lets run on its own word, so that a hook answers
	// allow for it rather than leave the decision to the permission rules.
	Explicit bool
}

func refuse(reason string) Verdict {
	return Verdict{Outcome: Refuse, Reason: reason}
}

// rule judges one invocation of a command. A rule that cannot read the state it needs returns the
// error, and the guard refuses the command saying what broke; one that cannot read a git argument
// it decides by returns an *unreadArgumentError, and the guard refuses the command naming a
// spelling of the argument it allows.
type rule func(ctx context.Context, caller Caller, inv Invocation) (Verdict, error)

// rules returns the rules every invocation of one command is judged by, in the order they run.
// The trunk rule keeps what origin answered for the rest of the command, so each command takes a
// new set. refusePush runs first: it refuses every form of what it judges, so no argument the
// guard cannot read changes its verdict, and its reason names the one way to push.
func rules() []rule {
	return []rule{
		refusePush, refuseSkippedChecks, refuseDiscardingGit, refuseHistoryOverFile, refuseRebaseOverChanges,
		refuseRiskyWorktreeRemoval, refuseGuardedConfigWrite, refuseOriginRewrite,
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

	verdict, err := judgeByRules(ctx, caller, inv, checks)
	if unread, ok := errors.AsType[*unreadArgumentError](err); ok {
		allows := func(in Invocation) bool {
			rejudged, rejudgeErr := judgeByRules(ctx, caller, in, checks)

			return rejudgeErr == nil && rejudged.Outcome == Allow
		}

		return refuse(unread.Error() + passingArgumentSpelling(inv, allows))
	}

	if err != nil {
		return failedRead(err)
	}

	return verdict
}

// judgeByRules judges inv by checks, up to the first refusal, and returns the error of the first
// rule that fails.
func judgeByRules(ctx context.Context, caller Caller, inv Invocation, checks []rule) (Verdict, error) {
	verdict := Verdict{Outcome: Allow}
	for _, judgeBy := range checks {
		next, err := judgeBy(ctx, caller, inv)
		if err != nil {
			return Verdict{}, err
		}

		verdict = verdict.or(next)
		if verdict.Outcome == Refuse {
			break
		}
	}

	return verdict, nil
}

// passingArgumentSpelling returns how to write the git arguments of inv the guard cannot read so
// that the guard allows inv, as allows judges it: the option in full, when one of them is a
// literal that starts several options; after `--`, when inv with each of them moved there is
// allowed; and as a literal otherwise. A form allows cannot judge is not allowed.
func passingArgumentSpelling(inv Invocation, allows func(Invocation) bool) string {
	moved, prefix, ok := argumentsAfterDoubleDash(inv)
	switch {
	case prefix != "":
		return "; spell out the option `" + prefix + "` in full"
	case ok && allows(moved):
		return "; put the argument after `--`, where git reads no word as an option"
	default:
		return "; write the argument as a literal"
	}
}

// argumentsAfterDoubleDash returns inv with each git argument that keeps readArgs from reading
// the arguments moved after `--`. ok is false when there is none, and when one is a literal: a
// literal is unreadable only as the start of several options, which `--` would make a path, and
// prefix is then its option name, the text before any `=`.
func argumentsAfterDoubleDash(inv Invocation) (moved Invocation, prefix string, ok bool) {
	sub, ok := subcommand(inv)
	if !ok {
		return Invocation{}, "", false
	}

	known := optionsOf(sub)
	kept := slices.Clone(inv.Git.Args)

	var unread []Word
	// A word makes the arguments unreadable once the words up to it are; those before it read.
	for i := 0; i < len(kept); i++ {
		if !readArgs(kept[:i+1], known).unreadable {
			continue
		}

		if kept[i].Literal {
			name, _, _ := strings.Cut(kept[i].Text, "=")

			return Invocation{}, name, false
		}

		unread = append(unread, kept[i])
		kept = slices.Delete(kept, i, i+1)
		i--
	}

	if len(unread) == 0 {
		return Invocation{}, "", false
	}

	doubleDash := Word{Text: "--", Literal: true}
	if !slices.Contains(kept, doubleDash) {
		kept = append(kept, doubleDash)
	}

	kept = append(kept, unread...)
	inv.Git.Args = kept

	return inv, "", true
}

// or returns v unless other is stricter, so that the first verdict of the strictest outcome wins.
// Two allows are explicit when either is: the rules that find nothing to object to in an invocation
// do not take back the allow another rule gives it on the guard's own word.
func (v Verdict) or(other Verdict) Verdict {
	switch {
	case other.Outcome > v.Outcome:
		return other
	case v.Outcome == Allow && other.Outcome == Allow:
		v.Explicit = v.Explicit || other.Explicit
	}

	return v
}

func failedRead(err error) Verdict {
	return refuse("the guard could not check the command: " + err.Error())
}

// markReason says what the mark found on inv, by the mark's name when it has one, and for a word
// the guard cannot read, how to spell it so that the guard reads it. A tool handed to another
// command is named with that command. A git word the guard could not read as an alias because it
// cannot tell which repository git runs in says so.
func markReason(inv Invocation, mark Mark) string {
	detail := mark.Name
	aliasInUnknownRepo := (mark.Kind == UnknownSubcommand || mark.Kind == UnreadableAlias) && inv.Git.GitDirUnknown &&
		inv.Git.Subcommand.Literal && isAliasName(mark.Name)
	switch {
	case mark.Kind == ToolArgument && len(inv.Words) > 0:
		detail = handedToolDetail(inv, mark.Name)
	case aliasInUnknownRepo:
		detail = fmt.Sprintf("%s: the guard cannot tell which repository git runs in, so it cannot read its "+
			"aliases; %s", mark.Name, moveFirst)
	}

	reason := string(mark.Kind)
	if detail != "" {
		reason += ": " + detail
	}

	if hint := readableSpelling(inv, mark.Kind); hint != "" {
		reason += "; " + hint
	}

	return reason
}

// moveFirst is what a refusal says to do when the guard cannot tell where a git command runs, or
// which branch HEAD is on, because of a move earlier in the command or a directory it cannot read.
const moveFirst = "run the `cd`, switch, or other move as its own command first, and write any directory as a literal"

// readableSpelling says how to write the word a mark of kind found on inv so that the guard reads
// it; empty for a kind that marks no word the guard cannot read.
func readableSpelling(inv Invocation, kind MarkKind) string {
	switch {
	case kind == UnknownSubcommand && !inv.Git.Subcommand.Literal:
		return "write the subcommand as a literal"
	case kind == DynamicCommand:
		return "write the command word as a literal, or assign its variable a literal earlier in the same command"
	case kind == DynamicScript:
		return "write the string as a literal"
	default:
		return ""
	}
}

// handedToolDetail names the command of inv that runs tool, one of its arguments, and how to run
// tool so that the guard reads it: a shell whose `-c` string is not a literal is run with a
// literal one, and anything else on its own, by its bare name.
func handedToolDetail(inv Invocation, tool string) string {
	runner := inv.Words[0].Text
	toolAt := 1 + slices.IndexFunc(inv.Words[1:], func(w Word) bool { return w.Literal && w.Text == tool })
	if toolAt > 0 && isShell(inv.Words[toolAt]) {
		set := readShellOptions(programName(inv.Words[toolAt]), inv.Words[toolAt+1:])
		if set.command && len(set.operands) > 0 && !set.operands[0].Literal {
			return fmt.Sprintf("%s runs %s with a -c string that is not a literal: write the -c string as a literal",
				runner, tool)
		}
	}

	return fmt.Sprintf("%s runs %s: run %s on its own, by its bare name", runner, tool, tool)
}
