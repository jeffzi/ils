package guard

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jeffzi/ils/internal/git"
)

// refuseOriginRewrite refuses a `git remote` operation that removes or renames origin, or points it elsewhere.
func refuseOriginRewrite(_ context.Context, _ Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	if !ok || sub != "remote" {
		return Verdict{Outcome: Allow}, nil
	}

	args := inv.Git.Args
	for len(args) > 0 && args[0].Literal && remoteVerbosity(args[0].Text) {
		args = args[1:]
	}

	if len(args) == 0 {
		return Verdict{Outcome: Allow}, nil
	}

	op := args[0]
	rewrites := slices.Contains([]string{"remove", "rm", "rename", "set-url", "set-head"}, op.Text)
	if op.Literal && !rewrites {
		return Verdict{Outcome: Allow}, nil
	}

	options := readArgs(args[1:], knownOptions{})
	if !op.Literal || options.opaque() {
		return Verdict{}, &unreadArgumentError{sub: sub}
	}

	if len(options.positional) == 0 || options.positional[0].Text != git.OriginRemote {
		return Verdict{Outcome: Allow}, nil
	}

	reason := fmt.Sprintf("git remote %s origin changes the remote branches are pushed to and fetched from", op.Text)

	return refuse(reason), nil
}

// remoteVerbosity reports whether word is the one option `git remote` takes before its own
// subcommand: -v, bundled or not, or --verbose, --no-verbose or a start of either.
func remoteVerbosity(word string) bool {
	if name, ok := strings.CutPrefix(word, "--"); ok {
		startsLong := func(long string) bool { return strings.HasPrefix(long, name) }

		return name != "" && slices.ContainsFunc([]string{"verbose", "no-verbose"}, startsLong)
	}

	letters, ok := strings.CutPrefix(word, "-")

	return ok && letters != "" && strings.Trim(letters, "v") == ""
}
