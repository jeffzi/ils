package landing

import (
	"context"
	"slices"
	"strings"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// Each read of the checks of the branch names what it reads as it follows "read" in a result, so
// it stays a noun phrase.
const (
	readTree         = "the working tree's changes"
	readTip          = "the branch's tip"
	readFetchedTrunk = "the commits on origin's trunk"
	readCommits      = "the branch's commits"
	readChanges      = "the branch's pull requests"
	readMergedHead   = "the merged pull request's head"
)

// startBranch is the judgment step out of a branch that cannot land. The new branch's name stays a
// placeholder for the agent to choose.
const startBranch = "start a branch with `ils branch <name>`"

// startBranchStep is the next step that starts a branch from origin's trunk.
func startBranchStep() result.Next {
	return result.NextStep(startBranch)
}

// onBranch is the work branch a landing command stands on, once the checks of the branch have
// read its tip and its commits past origin's trunk.
type onBranch struct {
	Ready
	branch string
	tip    string
	// past are the branch's commits that origin's trunk lacks, oldest first.
	past []git.Commit
}

// branchChecks runs the checks push and merge both open with: on a work branch, then a clean tree,
// then origin's trunk fetched. It returns the branch ready stands on, or the result of the first
// that refuses.
func branchChecks(ctx context.Context, deps Deps, req Request, ready Ready) (onBranch, *result.Result) {
	if stop := req.onWorkBranch(ctx, deps, ready); stop != nil {
		return onBranch{}, stop
	}

	if stop := cleanTree(ctx, deps, req); stop != nil {
		return onBranch{}, stop
	}

	if _, stop := fetchTrunk(ctx, deps, req, ready.Trunk); stop != nil {
		return onBranch{}, stop
	}

	return onBranch{Ready: ready, branch: ready.Standing.Branch}, nil
}

// cleanTree refuses a tree with a changed tracked file.
func cleanTree(ctx context.Context, deps Deps, req Request) *result.Result {
	files, err := deps.Repo.DirtyFiles(ctx)
	if err != nil {
		return req.unread(readTree, err)
	}

	if len(files) > 0 {
		return req.dirtyTree(files)
	}

	return nil
}

// dirtyTree refuses a tree whose changed tracked files are files.
func (r Request) dirtyTree(files []string) *result.Result {
	return r.Refused(
		result.Say("tracked files have changes that are not committed: %s", strings.Join(files, ", ")),
		result.NextStep("commit them"+thenRunAgain, r.ilsCommand()))
}

// fetchTrunk fetches origin's trunk, so that the reads of origin/<trunk> that follow are current,
// and returns the sha it fetched.
func fetchTrunk(ctx context.Context, deps Deps, req Request, trunk string) (string, *result.Result) {
	sha, err := deps.Repo.Fetch(ctx, trunk)
	if err != nil {
		return "", req.unreached(readFetchedTrunk, err,
			result.Say("origin could not be reached to fetch its trunk %s", result.Branch(trunk)))
	}

	return sha, nil
}

// commitsPast returns the branch's tip and its commits past origin's trunk, which fetchTrunk
// must have fetched, refusing a branch that has none or has no commit at all.
func commitsPast(ctx context.Context, deps Deps, req Request, b onBranch) (tip string, past []git.Commit, stop *result.Result) {
	tip, stop = tipOf(ctx, deps, req, b)
	if stop != nil {
		return "", nil, stop
	}

	past, err := deps.Repo.CommitsPast(ctx, tip, b.Trunk)
	if err != nil {
		return "", nil, req.unread(readCommits, err)
	}

	if len(past) == 0 {
		return "", nil, req.nothingPast(b)
	}

	return tip, past, nil
}

// tipOf returns the sha of the branch's tip, refusing a branch that has no commit at all.
func tipOf(ctx context.Context, deps Deps, req Request, b onBranch) (string, *result.Result) {
	tip, err := deps.Repo.HeadSHA(ctx)
	if err == nil {
		return tip, nil
	}

	// The read of the tip fails on a branch with no commit as it does on a broken repository, so
	// only the branch's ref tells the two apart.
	committed, refErr := deps.Repo.HasCommits(ctx, b.branch)
	if refErr != nil {
		return "", req.unread(readTip, refErr)
	}

	if !committed {
		return "", req.nothingPast(b)
	}

	return "", req.unread(readTip, err)
}

// pullRequest returns the branch's open pull request against the trunk, nil when it has none,
// and the first merged pull request from the branch whose commits all landed, nil when none
// did, with its HeadSHA the head fetched from origin. It refuses an open pull request against
// another base, and a merged one that carries some of the branch's commits but not the ones
// listed; with that refusal, landed is that merged pull request.
func pullRequest(ctx context.Context, deps Deps, req Request, b onBranch) (open, landed *forge.Change, stop *result.Result) {
	changes, err := deps.Forge.FindChanges(ctx, b.Repo, b.branch)
	if err != nil {
		return nil, nil, req.unread(readChanges, err)
	}

	for _, change := range changes {
		if change.State != forge.ChangeOpen {
			continue
		}

		if change.Base != b.Trunk {
			return nil, nil, req.Refused(
				result.Say("pull request %s from %s is open against %s and not against the trunk %s",
					result.PullRequest(change.Number), result.Branch(b.branch), result.Branch(change.Base), result.Branch(b.Trunk)),
				elsewhereStep(change.Number, b.Trunk, req.ilsCommand()))
		}

		open = &change
	}

	for _, change := range changes {
		if change.State != forge.ChangeMerged {
			continue
		}

		head, all, refusal := b.landedWith(ctx, deps, req, change)
		change.HeadSHA = head

		switch {
		case refusal != nil && head == "":
			return nil, nil, refusal
		case refusal != nil:
			return nil, &change, refusal
		case all:
			return open, &change, nil
		}
	}

	return open, nil, nil
}

// elsewhereStep is the next step of a branch whose open pull request number is against a base
// other than the trunk: close it or move its base to the trunk, then run command again.
func elsewhereStep(number int, trunk string, command result.Name) result.Next {
	return result.NextStep("close it or change its base with `gh pr edit %s --base %s`"+thenRunAgain,
		result.PullRequestNumber(number), result.Branch(trunk), command)
}

// landedWith reports whether the merged pull request change carries every commit of the branch
// past the trunk, by patch: false when it shares none of them. It refuses a branch that shares a
// commit with it and has commits it lacks, listing those. It returns the pull request's head as
// fetched from origin, or nothing when a read failed.
func (b onBranch) landedWith(ctx context.Context, deps Deps, req Request, change forge.Change) (string, bool, *result.Result) {
	head, stop := req.fetchPullHead(ctx, deps, change.Number, readMergedHead)
	if stop != nil {
		return "", false, stop
	}

	shared, unlanded, read, err := b.carriedBy(ctx, deps, head)
	if err != nil {
		return "", false, req.unread(read, err)
	}

	if !shared {
		return head, false, nil
	}

	if len(unlanded) == 0 {
		return head, true, nil
	}

	stop = req.Refused(
		result.Say("pull request %s from %s was merged without the commits listed here",
			result.PullRequest(change.Number), result.Branch(b.branch)),
		unlandedStep(unlanded))

	stop.Lines = listed(unlanded)

	return head, false, stop
}

// carriedBy reports whether the merged head head shares a commit past the trunk with the branch,
// and, when it does, the branch's commits it lacks by patch. A read that fails is returned by name
// with its error.
func (b onBranch) carriedBy(ctx context.Context, deps Deps, head string) (shared bool, unlanded []git.Commit, read string, err error) {
	theirs, err := deps.Repo.CommitsPast(ctx, head, b.Trunk)
	if err != nil {
		return false, nil, readMergedHead, err
	}

	shared = slices.ContainsFunc(theirs, func(commit git.Commit) bool {
		return slices.ContainsFunc(b.past, func(mine git.Commit) bool { return mine.SHA == commit.SHA })
	})
	if !shared {
		return false, nil, "", nil
	}

	unlanded, err = deps.Repo.Unlanded(ctx, head, b.tip, b.Trunk)
	if err != nil {
		return false, nil, readCommits, err
	}

	return true, unlanded, "", nil
}

// unlandedStep is the next step of a branch merged without the commits unlanded: start a branch and
// bring those commits over.
func unlandedStep(unlanded []git.Commit) result.Next {
	return result.NextStep(startBranch+", then bring those commits over with `git cherry-pick %s`; "+
		"expect a conflict where the squash touched the same files", shas(unlanded))
}

// alreadyLanded refuses a branch whose every commit landed with the merged pull request landed:
// its work is on the trunk, so only a new branch has anything left to land.
func (b onBranch) alreadyLanded(req Request, landed forge.Change) *result.Result {
	return req.Refused(
		result.Say("the branch %s already landed with pull request %s", result.Branch(b.branch), result.PullRequest(landed.Number)),
		startBranchStep())
}

func shas(commits []git.Commit) result.Name {
	list := make([]string, 0, len(commits))
	for _, commit := range commits {
		list = append(list, commit.SHA)
	}

	return result.SHAs(list)
}

func listed(commits []git.Commit) []result.Phrase {
	lines := make([]result.Phrase, 0, len(commits))
	for _, commit := range commits {
		lines = append(lines, result.Say("%s %s", result.SHA(commit.SHA), commit.Subject))
	}

	return lines
}
