package landing

import (
	"context"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// readLocalTrunk names what the read of the local trunk covers as it follows "read" in a result,
// so it stays a noun phrase.
const readLocalTrunk = "the local trunk"

// trunkAhead returns the refusal of a local trunk with commits origin's trunk lacks, listing them,
// whose next step is its fix, or nil when it has none or does not exist; a read that fails is
// the result r.Unread gives for it. It reads origin/<trunk>, so each command calls it right
// after its own fetchTrunk.
func (r Request) trunkAhead(ctx context.Context, s *survey) *result.Result {
	ahead, read, err := s.trunkAhead(ctx)
	if err != nil {
		return r.Unread(read, err)
	}

	if ahead == nil {
		return nil
	}

	stop := r.Refused(ahead.reason(), ahead.fix(r.afterTrunkFix()))
	stop.Lines = listed(ahead.past)

	return stop
}

// afterTrunkFix is the command the fix of a local trunk checked out nowhere runs after it: the one
// ilsCommand names, except that a status names `ils merge` when its open pull request is green and
// mergeable, which a push would go on to.
func (r Request) afterTrunkFix() result.Name {
	if r.Command == StatusCommand && r.MergeReady {
		return result.Command(MergeCommand)
	}

	return r.ilsCommand()
}

// aheadTrunk is a local trunk with commits origin's trunk lacks, and where it is checked out.
type aheadTrunk struct {
	trunk string
	// worktree is the path of the other worktree that has the trunk checked out, empty when none
	// does.
	worktree string
	// past are the trunk's commits that origin's trunk lacks, oldest first.
	past []git.Commit
	// here is whether HEAD is on the trunk.
	here bool
}

// readTrunkAhead returns the local trunk as aheadTrunk reads it when it has commits origin's trunk
// lacks, and nil when it has none or does not exist; here is whether HEAD is on it. A read that
// fails is returned with the name of the read.
func readTrunkAhead(ctx context.Context, deps Deps, trunk string, here bool) (*aheadTrunk, string, error) {
	ahead, past, err := deps.Repo.LocalTrunkAhead(ctx, trunk)
	if err != nil {
		return nil, readLocalTrunk, err
	}

	if !ahead {
		return nil, "", nil
	}

	a := &aheadTrunk{trunk: trunk, past: past, here: here}
	if here {
		return a, "", nil
	}

	a.worktree, err = deps.Repo.WorktreeWith(ctx, trunk)
	if err != nil {
		return nil, readWorktrees, err
	}

	return a, "", nil
}

// reason is the reason every landing command gives for refusing the local trunk.
func (a aheadTrunk) reason() result.Phrase {
	return result.Say("the local trunk %s has commits that origin's trunk lacks, listed here", result.Branch(a.trunk))
}

// keepOffTrunkHere and keepOffTrunkNowhere are the fixes of a local trunk with commits origin's
// trunk lacks: each keeps them on a branch of their own and puts the local trunk back at origin's,
// after the keepOff they share. A trunk checked out in some worktree takes keepOffTrunkHere run
// there, since git moves no branch checked out; one checked out nowhere takes keepOffTrunkNowhere,
// which switches nothing. Each name is the trunk: twice for keepOffTrunkHere, three times for
// keepOffTrunkNowhere. They stay untyped because result.NextStep accepts only constant
// expressions.
const (
	keepOff = "keep those commits on a branch of their own and put the local trunk back at " +
		"origin's with "
	keepOffTrunkHere    = keepOff + "`! git switch -c <name> && git branch -f %s origin/%s`"
	keepOffTrunkNowhere = keepOff + "`! git branch <name> %s && git branch -f %s origin/%s`"
)

// fix is the next step of the local trunk, run where it is checked out: here when HEAD is on it,
// then a push of the new branch; in the other worktree that has it checked out, named by its path,
// then a push there; and, checked out nowhere, here, then again.
func (a aheadTrunk) fix(again result.Name) result.Next {
	trunk := result.Branch(a.trunk)
	push := result.Command(PushCommand)

	switch {
	case a.here:
		return result.NextStep(keepOffTrunkHere+", then run %s", trunk, trunk, push)
	case a.worktree != "":
		return result.NextStep("in the worktree at %s, "+keepOffTrunkHere+", then run %s there", a.worktree, trunk, trunk, push)
	}

	return result.NextStep(keepOffTrunkNowhere+thenRunAgain, trunk, trunk, trunk, again)
}
