package landing

import (
	"context"
	"strings"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/result"
)

// The reads of push's own checks of the branch, as a result names them.
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
// fails: HEAD is on a branch other than the trunk; no tracked file is changed; the branch has
// a commit past origin's trunk, which it fetches; the tip is not behind origin's copy of the
// branch and has not diverged from it; unless origin's copy carries the branch's first commit
// past the trunk, that commit is on no other local branch; no open pull request from the
// branch has a base other than the trunk; no merged pull request from the branch already
// carries its commits. A precondition that fails, an origin that cannot be reached, and a read
// the context's deadline cut short are refusals; a read that fails for any other reason is
// broke.
//
// When all hold it pushes the branch to origin, unless origin's copy is already at the tip,
// and opens the branch's pull request against the trunk, unless one is open. It then reads the
// checks the trunk requires and waits for the verdict of the pushed commit. Green is done; a
// red, missing, or absent verdict, a conflict, and a branch behind its trunk are delivered,
// not green; a wait the deadline ended is pending. From the push on nothing is a refusal: a
// write or a read that fails is broke, and the lines say what was written.
func Push(ctx context.Context, deps Deps, req Request) result.Result {
	ready, stop, ok := Preconditions(ctx, deps, req)
	if !ok {
		return stop
	}

	p, refusal := pushChecks(ctx, deps, req, ready)
	if refusal != nil {
		return *refusal
	}

	return deliver(ctx, deps, req, p)
}

// pushChecks runs push's checks of the branch after the shared preconditions, and returns what
// the sequence needs or the result of the first that refuses.
func pushChecks(ctx context.Context, deps Deps, req Request, ready Ready) (pushReady, *result.Result) {
	p := pushReady{Ready: ready, branch: ready.Standing.Branch}

	if stop := req.onWorkBranch(ready); stop != nil {
		return pushReady{}, stop
	}

	if stop := cleanTree(ctx, deps, req); stop != nil {
		return pushReady{}, stop
	}

	if stop := fetchTrunk(ctx, deps, req, ready.Trunk); stop != nil {
		return pushReady{}, stop
	}

	var stop *result.Result

	p.tip, p.past, stop = commitsPast(ctx, deps, req, p.onBranch)
	if stop != nil {
		return pushReady{}, stop
	}

	p.copied, p.onOrigin, stop = originCopy(ctx, deps, req, p)
	if stop != nil {
		return pushReady{}, stop
	}

	if stop = cutFromTrunk(ctx, deps, req, p); stop != nil {
		return pushReady{}, stop
	}

	var landed *forge.Change

	p.open, landed, stop = pullRequest(ctx, deps, req, p.onBranch)
	if stop != nil {
		return pushReady{}, stop
	}

	if landed != nil {
		return pushReady{}, p.alreadyLanded(req, *landed)
	}

	return p, nil
}

// cutFromTrunk refuses a branch whose first commit past the trunk another local branch also
// carries, and lists the commits none of those branches carry. Origin's copy of the branch, if
// any, settles it when it carries that commit: the branch is already published with it, so
// another branch that shares it does not stop the push. Without that, git cannot tell which
// branch was cut from which, so the refusal names both ways out.
func cutFromTrunk(ctx context.Context, deps Deps, req Request, p pushReady) *result.Result {
	first := p.past[0].SHA

	if p.copied != "" {
		published, err := deps.Repo.Contains(ctx, p.copied, first)
		if err != nil {
			return req.unread(readCopy, err)
		}

		if published {
			return nil
		}
	}

	others, err := deps.Repo.BranchesContaining(ctx, first)
	if err != nil {
		return req.unread(readBranches, err)
	}

	if len(others) == 0 {
		return nil
	}

	own, err := deps.Repo.OwnCommits(ctx, p.tip, p.Trunk, others)
	if err != nil {
		return req.unread(readCommits, err)
	}

	stop := req.Refused(sharedFirst(p.branch, others, len(own) > 0), eitherWayOut(len(others), req.Command))
	stop.Lines = listed(own)

	return stop
}

// eitherWayOut is the Next step of a branch that shares its first commit past the trunk with count
// other local branches: land them first, or remove them, then run command again.
func eitherWayOut(count int, command string) result.Next {
	if count == 1 {
		return result.NextStep("run ils push on the other branch and get it merged first when this branch builds "+
			"on it, or remove the other branch when it was cut from this one"+thenRunAgain, command)
	}

	return result.NextStep("run ils push on the other branches and get them merged first when this branch builds "+
		"on them, or remove the other branches when they were cut from this one"+thenRunAgain, command)
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

	if len(others) == 1 {
		if hasOwn {
			return result.Say(withOne+ownListed, branch, others[0])
		}

		return result.Say(withOne+noOwn, branch, others[0])
	}

	last := len(others) - 1
	named := strings.Join(others[:last], ", ") + " and " + others[last]

	if hasOwn {
		return result.Say(withMany+ownListed, branch, named)
	}

	return result.Say(withMany+noOwn, branch, named)
}

// originCopy asks origin whether it has the branch and returns the commit its fetched copy is
// on, empty when origin has none, and whether that copy is at the tip. It refuses a tip that is
// behind that copy or has diverged from it.
func originCopy(ctx context.Context, deps Deps, req Request, p pushReady) (theirs string, atTip bool, stop *result.Result) {
	_, found, err := deps.Repo.RemoteBranch(ctx, p.branch)
	if err != nil {
		return "", false, req.unreached(readOnOrigin, err,
			result.Say("origin could not be reached to ask whether it has the branch %s", p.branch))
	}

	if !found {
		return "", false, nil
	}

	theirs, err = deps.Repo.Fetch(ctx, p.branch)
	if err != nil {
		return "", false, req.unreached(readCopy, err,
			result.Say("origin could not be reached to fetch its copy of the branch %s", p.branch))
	}

	apart, err := deps.Repo.Divergence(ctx, p.tip, theirs)
	if err != nil {
		return "", false, req.unread(readCopy, err)
	}

	switch {
	case apart.Behind > 0 && apart.Ahead > 0:
		return "", false, req.Refused(
			result.Say("the branch %s and origin's copy of it have diverged", p.branch),
			result.NextStep("run `git merge origin/%s`"+thenRunAgain, p.branch, req.Command))

	case apart.Behind > 0:
		return "", false, req.Refused(
			result.Say("the branch %s is behind origin's copy of it", p.branch),
			result.NextStep("run `git pull --ff-only origin %s`"+thenRunAgain, p.branch, req.Command))

	}

	return theirs, apart.Ahead == 0, nil
}
