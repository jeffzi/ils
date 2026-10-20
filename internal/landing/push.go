package landing

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// The reads of push's own preconditions, as a result names them.
const (
	readTree         = "the working tree's changes"
	readTip          = "the branch's tip"
	readFetchedTrunk = "the commits on origin's trunk"
	readCommits      = "the branch's commits"
	readBranches     = "the local branches"
	readOnOrigin     = "whether origin has the branch"
	readCopy         = "origin's copy of the branch"
	readChanges      = "the branch's pull requests"
	readMergedHead   = "the merged pull request's head"
)

// startBranch is the judgment step out of a branch that cannot be pushed; its two names are
// the trunk. The new branch's name stays a placeholder for the agent to choose.
const startBranch = "start a branch from origin's trunk: " +
	"`git fetch origin %s && git switch -c <name> --no-track origin/%s`"

// startBranchStep is the startBranch step with both of its names set to the trunk.
func startBranchStep(trunk string) result.Next {
	return result.NextStep(startBranch, trunk, trunk)
}

// pushReady is what push's preconditions hand to the sequence that follows them.
type pushReady struct {
	// open is the branch's open pull request against the trunk, nil when it has none.
	open *forge.Change
	Ready
	branch string
	tip    string
	// copied is the commit origin's copy of the branch is on, empty when origin has none.
	copied string
	// past are the branch's commits that origin's trunk lacks, oldest first.
	past []git.Commit
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

func pushChecks(ctx context.Context, deps Deps, req Request, ready Ready) (pushReady, *result.Result) {
	p := pushReady{Ready: ready, branch: ready.Standing.Branch}

	if stop := req.onWorkBranch(ready); stop != nil {
		return pushReady{}, stop
	}

	if stop := cleanTree(ctx, deps, req); stop != nil {
		return pushReady{}, stop
	}

	var stop *result.Result

	p.tip, p.past, stop = commitsPast(ctx, deps, req, p)
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

	p.open, stop = pullRequest(ctx, deps, req, p)
	if stop != nil {
		return pushReady{}, stop
	}

	return p, nil
}

// onWorkBranch refuses a detached HEAD and the trunk.
func (r Request) onWorkBranch(ready Ready) *result.Result {
	next := startBranchStep(ready.Trunk)

	if ready.Standing.Kind != git.OnBranch {
		return r.refused(result.Say("HEAD is detached, so there is no branch to push"), next)
	}

	if ready.Standing.Branch == ready.Trunk {
		return r.refused(result.Say("HEAD is on the trunk %s, which ils never pushes", ready.Trunk), next)
	}

	return nil
}

// cleanTree refuses a tree with a changed tracked file.
func cleanTree(ctx context.Context, deps Deps, req Request) *result.Result {
	files, err := deps.Repo.DirtyFiles(ctx)
	if err != nil {
		return req.unread(readTree, err)
	}

	if len(files) > 0 {
		return req.refused(
			result.Say("tracked files have changes that are not committed: %s", strings.Join(files, ", ")),
			result.NextStep("commit them, then run ils push again"),
		)
	}

	return nil
}

// commitsPast fetches origin's trunk and returns the branch's tip and its commits past that
// trunk, refusing a branch that has none or has no commit at all.
func commitsPast(ctx context.Context, deps Deps, req Request, p pushReady) (tip string, past []git.Commit, stop *result.Result) {
	trunk := p.Trunk

	_, err := deps.Repo.Fetch(ctx, trunk)
	if err != nil {
		return "", nil, req.unreached(readFetchedTrunk, err,
			result.Say("origin could not be reached to fetch its trunk %s", trunk))
	}

	tip, stop = tipOf(ctx, deps, req, p)
	if stop != nil {
		return "", nil, stop
	}

	past, err = deps.Repo.CommitsPast(ctx, tip, trunk)
	if err != nil {
		return "", nil, req.unread(readCommits, err)
	}

	if len(past) == 0 {
		return "", nil, req.nothingPast(p)
	}

	return tip, past, nil
}

// tipOf returns the sha of the branch's tip, refusing a branch that has no commit at all.
func tipOf(ctx context.Context, deps Deps, req Request, p pushReady) (string, *result.Result) {
	tip, err := deps.Repo.HeadSHA(ctx)
	if err == nil {
		return tip, nil
	}

	// The read of the tip fails on a branch with no commit as it does on a broken repository, so
	// only the branch's ref tells the two apart.
	committed, refErr := deps.Repo.HasCommits(ctx, p.branch)
	if refErr != nil {
		return "", req.unread(readTip, refErr)
	}

	if !committed {
		return "", req.nothingPast(p)
	}

	return "", req.unread(readTip, err)
}

// nothingPast refuses a branch with no commit that origin's trunk lacks.
func (r Request) nothingPast(p pushReady) *result.Result {
	return r.refused(
		result.Say("the branch %s has no commit past origin's trunk %s", p.branch, p.Trunk),
		result.NextStep("commit the work, then run ils push again"),
	)
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

	stop := req.refused(sharedFirst(p.branch, others, len(own) > 0), eitherWayOut(len(others)))
	stop.Lines = listed(own)

	return stop
}

// eitherWayOut is the Next step of a branch that shares its first commit past the trunk with count
// other local branches: land them first, or remove them.
func eitherWayOut(count int) result.Next {
	if count == 1 {
		return result.NextStep("run ils push on the other branch and get it merged first when this branch builds " +
			"on it, or remove the other branch when it was cut from this one, then run ils push again")
	}

	return result.NextStep("run ils push on the other branches and get them merged first when this branch builds " +
		"on them, or remove the other branches when they were cut from this one, then run ils push again")
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
		return "", false, req.refused(
			result.Say("the branch %s and origin's copy of it have diverged", p.branch),
			result.NextStep("run `git merge origin/%s`, then run ils push again", p.branch),
		)
	case apart.Behind > 0:
		return "", false, req.refused(
			result.Say("the branch %s is behind origin's copy of it", p.branch),
			result.NextStep("run `git pull --ff-only origin %s`, then run ils push again", p.branch),
		)
	}

	return theirs, apart.Ahead == 0, nil
}

// pullRequest returns the branch's open pull request against the trunk, nil when it has none,
// refusing an open one against another base and a merged one that carries the branch's commits.
func pullRequest(ctx context.Context, deps Deps, req Request, p pushReady) (*forge.Change, *result.Result) {
	changes, err := deps.Forge.FindChanges(ctx, p.Repo, p.branch)
	if err != nil {
		return nil, req.unread(readChanges, err)
	}

	var open *forge.Change

	for _, change := range changes {
		if change.State != forge.ChangeOpen {
			continue
		}

		if change.Base != p.Trunk {
			return nil, req.refused(
				result.Say("pull request #%s from %s is open against %s and not against the trunk %s",
					change.Number, p.branch, change.Base, p.Trunk),
				result.NextStep("close it or change its base with `gh pr edit %s --base %s`, then run ils push again",
					change.Number, p.Trunk),
			)
		}

		open = &change
	}

	for _, change := range changes {
		if change.State != forge.ChangeMerged {
			continue
		}

		if stop := p.landed(ctx, deps, req, change); stop != nil {
			return nil, stop
		}
	}

	return open, nil
}

// landed refuses a branch that shares a commit past the trunk with the head of the merged
// pull request change: all of it landed, or all but the commits it lists.
func (p pushReady) landed(ctx context.Context, deps Deps, req Request, change forge.Change) *result.Result {
	head, err := deps.Repo.Fetch(ctx, "refs/pull/"+strconv.Itoa(change.Number)+"/head")
	if err != nil {
		return req.unreached(readMergedHead, err,
			result.Say("origin could not be reached to fetch the head of pull request #%s", change.Number))
	}

	theirs, err := deps.Repo.CommitsPast(ctx, head, p.Trunk)
	if err != nil {
		return req.unread(readMergedHead, err)
	}

	shared := slices.ContainsFunc(theirs, func(commit git.Commit) bool {
		return slices.ContainsFunc(p.past, func(mine git.Commit) bool { return mine.SHA == commit.SHA })
	})
	if !shared {
		return nil
	}

	unlanded, err := deps.Repo.Unlanded(ctx, head, p.tip, p.Trunk)
	if err != nil {
		return req.unread(readCommits, err)
	}

	if len(unlanded) == 0 {
		return req.refused(
			result.Say("the branch %s already landed with pull request #%s", p.branch, change.Number),
			startBranchStep(p.Trunk),
		)
	}

	stop := req.refused(
		result.Say("pull request #%s from %s was merged without the commits listed here", change.Number, p.branch),
		result.NextStep(startBranch+", then bring those commits over with `git cherry-pick %s`; "+
			"expect a conflict where the squash touched the same files", p.Trunk, p.Trunk, shas(unlanded)),
	)
	stop.Lines = listed(unlanded)

	return stop
}

// shas returns the shas of commits in order, separated by spaces.
func shas(commits []git.Commit) string {
	list := make([]string, 0, len(commits))
	for _, commit := range commits {
		list = append(list, commit.SHA)
	}

	return strings.Join(list, " ")
}

// listed returns one line per commit: its sha and its subject.
func listed(commits []git.Commit) []result.Phrase {
	lines := make([]result.Phrase, 0, len(commits))
	for _, commit := range commits {
		lines = append(lines, result.Say("%s %s", commit.SHA, commit.Subject))
	}

	return lines
}
