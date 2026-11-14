package guard

import (
	"context"
	"fmt"
)

// refusePush refuses every `git push`, `git send-pack`, and `git http-push`, in any repository or
// none: `ils push` is the one way an agent pushes a branch.
func refusePush(_ context.Context, _ Caller, inv Invocation) (Verdict, error) {
	sub, ok := subcommand(inv)
	pushes := sub == "push" || sub == "send-pack" || sub == "http-push"
	if !ok || !pushes {
		return Verdict{Outcome: Allow}, nil
	}

	reason := fmt.Sprintf("git %s: `ils push` is the one way an agent pushes a branch; where `ils push` "+
		"refuses the origin, the user pushes by hand", sub)

	return refuse(reason), nil
}
