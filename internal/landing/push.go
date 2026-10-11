package landing

import (
	"context"
	"errors"
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

// pushReady is what push's preconditions hand to the sequence that follows them.
type pushReady struct {
	// open is the branch's open pull request against the trunk, nil when it has none.
	open *forge.Change
	Ready
	// branch is the branch HEAD is on.
	branch string
	// tip is the sha of the branch's tip.
	tip string
	// past are the branch's commits that origin's trunk lacks, oldest first.
	past []git.Commit
	// onOrigin is whether origin's copy of the branch is already at tip.
	onOrigin bool
}

// Push runs `ils push` up to the end of its preconditions and writes nothing.
//
// After the shared preconditions it checks, in this order and stopping at the first that
// fails: HEAD is on a branch other than the trunk; no tracked file is changed; the branch has
// a commit past origin's trunk, which it fetches; the first such commit is on no other local
// branch; the tip is not behind origin's copy of the branch and has not diverged from it; no
// open pull request from the branch has a base other than the trunk; no merged pull request
// from the branch already carries its commits. A precondition that fails, an origin that
// cannot be reached, and a read the context's deadline cut short are refusals; a read that
// fails for any other reason is broke. When all hold the result is broke and says that the
// sequence is not built.
func Push(ctx context.Context, deps Deps, req Request) result.Result {
	ready, stop, ok := Preconditions(ctx, deps, req)
	if !ok {
		return stop
	}

	_, refusal := pushChecks(ctx, deps, req, ready)
	if refusal != nil {
		return *refusal
	}

	return result.Result{
		Command: req.Command,
		Words:   req.Words,
		Outcome: result.Broke,
		Lines: []result.Phrase{
			result.Say("Every precondition holds, and the push sequence is not built yet."),
			result.Say("Nothing was written."),
		},
		Reason: result.Say("the push sequence is not built yet"),
		Next:   result.NextNone(),
	}
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

	if stop = cutFromTrunk(ctx, deps, req, p); stop != nil {
		return pushReady{}, stop
	}

	p.onOrigin, stop = originCopy(ctx, deps, req, p)
	if stop != nil {
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
	next := result.NextStep(startBranch, ready.Trunk, ready.Trunk)

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
// carries, and lists the commits that other branch lacks.
func cutFromTrunk(ctx context.Context, deps Deps, req Request, p pushReady) *result.Result {
	others, err := deps.Repo.BranchesContaining(ctx, p.past[0].SHA)
	if err != nil {
		return req.unread(readBranches, err)
	}

	if len(others) == 0 {
		return nil
	}

	other := others[0]

	var own []git.Commit

	for _, commit := range p.past {
		carried, err := deps.Repo.Contains(ctx, other, commit.SHA)
		if err != nil {
			return req.unread(readCommits, err)
		}

		if !carried {
			own = append(own, commit)
		}
	}

	if len(own) == 0 {
		return req.refused(
			result.Say("the branch %s was cut from the branch %s and not from the trunk, and has no commit of its own",
				p.branch, other),
			result.NextStep(startBranch, p.Trunk, p.Trunk),
		)
	}

	stop := req.refused(
		result.Say("the branch %s was cut from the branch %s and not from the trunk; its own commits are listed here",
			p.branch, other),
		result.NextStep(startBranch+", then bring this branch's own commits over with `git cherry-pick %s`",
			p.Trunk, p.Trunk, shas(own)),
	)
	stop.Lines = listed(own)

	return stop
}

// originCopy asks origin whether it has the branch and reports whether its copy is at the
// tip, refusing a tip that is behind that copy or has diverged from it.
func originCopy(ctx context.Context, deps Deps, req Request, p pushReady) (bool, *result.Result) {
	_, found, err := deps.Repo.RemoteBranch(ctx, p.branch)
	if err != nil {
		return false, req.unreached(readOnOrigin, err,
			result.Say("origin could not be reached to ask whether it has the branch %s", p.branch))
	}

	if !found {
		return false, nil
	}

	theirs, err := deps.Repo.Fetch(ctx, p.branch)
	if err != nil {
		return false, req.unreached(readCopy, err,
			result.Say("origin could not be reached to fetch its copy of the branch %s", p.branch))
	}

	apart, err := deps.Repo.Divergence(ctx, p.tip, theirs)
	if err != nil {
		return false, req.unread(readCopy, err)
	}

	switch {
	case apart.Behind > 0 && apart.Ahead > 0:
		return false, req.refused(
			result.Say("the branch %s and origin's copy of it have diverged", p.branch),
			result.NextStep("run `git merge origin/%s`, then run ils push again", p.branch),
		)
	case apart.Behind > 0:
		return false, req.refused(
			result.Say("the branch %s is behind origin's copy of it", p.branch),
			result.NextStep("run `git pull --ff-only origin %s`, then run ils push again", p.branch),
		)
	}

	return apart.Ahead == 0, nil
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
			result.NextStep(startBranch, p.Trunk, p.Trunk),
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

// unreached returns the result for a call to origin that failed with err: the refusal reason
// when origin did not answer, and what unread returns otherwise.
func (r Request) unreached(read string, err error, reason result.Phrase) *result.Result {
	// The deadline comes first: a call to origin that it cut short is also a *git.RemoteError.
	if errors.Is(err, context.DeadlineExceeded) {
		return r.unread(read, err)
	}

	if _, ok := errors.AsType[*git.RemoteError](err); ok {
		return r.refused(reason, result.NextCommand("push"))
	}

	return r.unread(read, err)
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
