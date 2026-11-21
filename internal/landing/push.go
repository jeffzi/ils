package landing

import (
	"context"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// readBranches, readOnOrigin and readCopy name what a read of the branch's local and origin copies
// covers, as a result names them.
const (
	readBranches = "the local branches"
	readOnOrigin = "whether origin has the branch"
	readCopy     = "origin's copy of the branch"
)

// pushReady is what pushChecks hands to the sequence that follows it.
type pushReady struct {
	// copied is the commit origin's copy of the branch is on, empty when origin has none.
	copied string
	// open is the branch's open pull request against the trunk, nil when it has none.
	open *forge.Change
	onBranch
	// onOrigin is whether origin's copy of the branch is already at tip.
	onOrigin bool
}

// Push runs `ils push`: its preconditions, then its two writes, then the wait for the checks.
//
// After the shared preconditions it checks, in this order and stopping at the first that
// fails: HEAD is on a branch other than the trunk; no tracked file is changed; the local trunk,
// if any, has no commit that origin's trunk, which it fetches, lacks; the branch has a commit
// past origin's trunk; the tip is not behind origin's copy of the branch and has not diverged
// from it; unless origin's copy carries the branch's first commit past the trunk, that commit is
// on no other local branch; no open pull request from the branch has a base other than the
// trunk; no merged pull request from the branch already carries its commits. A precondition that
// fails, an origin that cannot be reached, and a read the context's deadline cut short are
// refusals; a read that fails for any other reason is broke. A local trunk ahead of origin's is
// refused with the fix aheadTrunk.fix gives, and so is HEAD on such a trunk.
//
// When all hold it pushes the branch to origin, unless origin's copy is already at the tip,
// and opens the branch's pull request against the trunk, unless one is open. It then reads the
// checks the trunk requires and waits for the verdict of the pushed commit. Green is done; a
// red, missing, or absent verdict, a conflict, and a branch behind its trunk are delivered,
// not green; a wait the deadline ended is pending. From the push on nothing is a refusal: a
// write or a read that fails is broke, and the lines say what was written.
func Push(ctx context.Context, deps Deps, req Request) result.Result {
	deps.startChecking()

	s := newSurvey(deps)

	ready, stop := preconditions(ctx, s, req)
	if stop != nil {
		return *stop
	}

	p, refusal := pushChecks(ctx, s, req, ready)
	if refusal != nil {
		return *refusal
	}

	return deliver(ctx, deps, req, p)
}

// pushChecks runs push's checks of the branch after the shared preconditions, and returns what
// the sequence needs or the result of the first that refuses.
func pushChecks(ctx context.Context, s *survey, req Request, ready Ready) (pushReady, *result.Result) {
	on, stop := branchChecks(ctx, s, req, ready)
	if stop != nil {
		return pushReady{}, stop
	}

	p := pushReady{onBranch: on}

	p.tip, p.past, stop = commitsPast(ctx, s, req, p.onBranch)
	if stop != nil {
		return pushReady{}, stop
	}

	p.copied, p.onOrigin, stop = originCopy(ctx, s, req, p.branch)
	if stop != nil {
		return pushReady{}, stop
	}

	shared, read, err := s.firstShared(ctx)
	if err != nil {
		return pushReady{}, req.Unread(read, err)
	}

	if stop = shared.stop(req, p.branch); stop != nil {
		return pushReady{}, stop
	}

	var landed *forge.Change

	p.open, landed, stop = pullRequest(ctx, s, req, p.onBranch)
	if stop != nil {
		return pushReady{}, stop
	}

	if landed != nil {
		return pushReady{}, p.alreadyLanded(req, *landed)
	}

	return p, nil
}

// firstShared is what the read of the branch's first commit past the trunk found: the other local
// branches that also carry it, none when it does not stop the push, and the branch's commits none
// of them carry.
type firstShared struct {
	others []string
	own    []git.Commit
}

// readFirstShared reads the other local branches that carry the first commit past the trunk of the
// branch p stands on, as firstShared.stop judges them. A read that fails is returned with the name
// of the read. Origin's copy of the branch, if any, settles it when it carries that commit: the
// branch is already published with it, so another branch that shares it does not stop the push.
// Without that, git cannot tell which branch was cut from which, so the refusal names every way
// out: land the other branches first, remove them, or start a new branch and bring this branch's
// own commits over.
func readFirstShared(ctx context.Context, deps Deps, p pushReady) (firstShared, string, error) {
	first := p.past[0].SHA

	if p.copied != "" {
		published, err := deps.Repo.Contains(ctx, p.copied, first)
		if err != nil {
			return firstShared{}, readCopy, err
		}

		if published {
			return firstShared{}, "", nil
		}
	}

	others, err := deps.Repo.BranchesContaining(ctx, first)
	if err != nil {
		return firstShared{}, readBranches, err
	}

	if len(others) == 0 {
		return firstShared{}, "", nil
	}

	own, err := deps.Repo.OwnCommits(ctx, p.tip, p.Trunk, others)
	if err != nil {
		return firstShared{}, readCommits, err
	}

	return firstShared{others: others, own: own}, "", nil
}

// stop returns the refusal of the branch named branch when other local branches carry its first
// commit past the trunk, listing its commits none of them carry, and nil when none does.
func (f firstShared) stop(req Request, branch string) *result.Result {
	if len(f.others) == 0 {
		return nil
	}

	stop := req.Refused(sharedFirst(branch, f.others, len(f.own) > 0), eitherWayOut(len(f.others), req.ilsCommand()))
	stop.Lines = listed(f.own)

	return stop
}

// eitherWayOut is the Next step of a branch that shares its first commit past the trunk with count
// other local branches: land them first, or remove them, then run command again; or start a branch
// and bring this branch's own commits over.
func eitherWayOut(count int, command result.Name) result.Next {
	const restart = "; or " + startBranch + " and cherry-pick this branch's own commits onto it"

	push := result.Command(PushCommand)

	if count == 1 {
		return result.NextStep("run %s on the other branch and get it merged first when this branch builds "+
			"on it, or remove the other branch when it was cut from this one"+thenRunAgain+restart, push, command)
	}

	return result.NextStep("run %s on the other branches and get them merged first when this branch builds "+
		"on them, or remove the other branches when they were cut from this one"+thenRunAgain+restart, push, command)
}

// sharedFirst says that the branch shares its first commit past the trunk with the branches
// others, in the order git lists them, and whether it has commits of its own.
func sharedFirst(branch string, others []string, hasOwn bool) result.Phrase {
	const (
		opening   = "the branch %s shares its first commit past the trunk with the "
		undecided = "; ils cannot tell which was cut from which, and "
		withOne   = opening + "branch %s" + undecided
		withMany  = opening + "branches %s" + undecided
		ownListed = "its own commits are listed here"
		noOwn     = "it has no commit of its own"
	)

	own := result.Branch(branch)

	if len(others) == 1 {
		other := result.Branch(others[0])
		if hasOwn {
			return result.Say(withOne+ownListed, own, other)
		}

		return result.Say(withOne+noOwn, own, other)
	}

	named := result.Branches(others)

	if hasOwn {
		return result.Say(withMany+ownListed, own, named)
	}

	return result.Say(withMany+noOwn, own, named)
}

// originCopy asks origin whether it has the branch named branch and returns the commit its fetched
// copy is on, empty when origin has none, and whether that copy is at the tip. It refuses a tip that
// is behind that copy or has diverged from it.
func originCopy(ctx context.Context, s *survey, req Request, branch string) (theirs string, atTip bool, stop *result.Result) {
	found, err := s.onOrigin(ctx)
	if err != nil {
		return "", false, req.unreached(readOnOrigin, err,
			result.Say("origin could not be reached to ask whether it has the branch %s", result.Branch(branch)))
	}

	if !found {
		return "", false, nil
	}

	theirs, err = s.copyFetch(ctx)
	if err != nil {
		return "", false, req.unreached(readCopy, err,
			result.Say("origin could not be reached to fetch its copy of the branch %s", result.Branch(branch)))
	}

	apart, err := s.divergence(ctx)
	if err != nil {
		return "", false, req.Unread(readCopy, err)
	}

	switch {
	case apart.Behind > 0 && apart.Ahead > 0:
		return "", false, req.Refused(
			result.Say("the branch %s and origin's copy of it have diverged", result.Branch(branch)),
			divergedStep(branch, req.ilsCommand()))

	case apart.Behind > 0:
		return "", false, req.Refused(
			result.Say("the branch %s is behind origin's copy of it", result.Branch(branch)),
			behindStep(branch, req.ilsCommand()))

	}

	return theirs, apart.Ahead == 0, nil
}

// divergedStep is the next step of a branch that has diverged from origin's copy of it: merge that
// copy, then run command again.
func divergedStep(branch string, command result.Name) result.Next {
	return result.NextStep("run `git merge origin/%s`"+thenRunAgain, result.Branch(branch), command)
}

// behindStep is the next step of a branch behind origin's copy of it: fast-forward to that copy,
// then run command again.
func behindStep(branch string, command result.Name) result.Next {
	return result.NextStep("run `git pull --ff-only origin %s`"+thenRunAgain, result.Branch(branch), command)
}
