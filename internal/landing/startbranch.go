package landing

import (
	"context"
	"errors"
	"strings"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// The reads of `ils branch`, as a result names them.
const (
	readName        = "whether git can hold the name as a branch"
	readTaking      = "the branches that take the name"
	readHeadCommits = "the commits on HEAD"
)

// StartBranch runs `ils branch <name>`, the name being the one word of req.Args: it starts a work
// branch of that name, exactly as typed, at origin's trunk as just fetched, with no upstream, and
// puts HEAD on it, carrying uncommitted changes and untracked files. It needs git alone.
//
// After the git-only preconditions it checks, in this order and stopping at the first that fails:
// git can hold the name as a branch; origin's trunk fetches; no branch takes the name, locally or on
// origin; a detached HEAD has no commit origin's trunk lacks. A precondition that fails, an origin
// that cannot be reached, and a read the context's deadline cut short are refusals; a read that
// fails for any other reason is broke.
//
// The one write, the switch to the new branch, comes after every read. A file the switch would
// overwrite is a refusal naming it, with nothing written. A local trunk with commits origin's trunk
// lacks does not stop the command: a line names those commits, which stay on the local trunk.
func StartBranch(ctx context.Context, deps Deps, req Request) result.Result {
	deps.startChecking()

	ready, stop := GitPreconditions(ctx, deps, req)
	if stop != nil {
		return *stop
	}

	n := newBranch{Ready: ready, name: req.Args[0]}

	start, stop := n.checks(ctx, deps, req)
	if stop != nil {
		return *stop
	}

	lines, stop := aheadLines(ctx, deps, req, ready.Trunk)
	if stop != nil {
		return *stop
	}

	if stop := n.create(ctx, deps, req); stop != nil {
		return *stop
	}

	started := result.Say("Started the branch %s at %s, origin's trunk %s, with no upstream",
		result.Branch(n.name), result.SHA(start), result.Branch(ready.Trunk))

	return result.Result{
		Command: req.Command,
		Words:   req.Words,
		Outcome: result.Done,
		Lines:   append([]result.Phrase{started}, lines...),
		Next:    result.NextStep("do the work, commit it, then run %s", result.Command(PushCommand)),
	}
}

// newBranch is the branch `ils branch` starts: its name, as typed, and what the git-only
// preconditions found.
type newBranch struct {
	name string
	Ready
}

// checks runs the checks of `ils branch` after the git-only preconditions, and returns the sha of
// origin's trunk it fetched or the result of the first that refuses.
func (n newBranch) checks(ctx context.Context, deps Deps, req Request) (string, *result.Result) {
	if stop := validName(ctx, deps, req, n.name); stop != nil {
		return "", stop
	}

	start, stop := fetchTrunk(ctx, deps, req, n.Trunk)
	if stop != nil {
		return "", stop
	}

	if stop := freeName(ctx, deps, req, n.name); stop != nil {
		return "", stop
	}

	if n.Standing.Kind == git.Detached {
		if stop := detachedKept(ctx, deps, req, n.Trunk); stop != nil {
			return "", stop
		}
	}

	return start, nil
}

// anotherName is the judgment step after a name the command cannot start a branch under.
func anotherName(req Request) result.Next {
	return result.NextStep("choose another name, then run %s with it", result.Command(req.Command))
}

// validName refuses a name git cannot hold as a branch.
func validName(ctx context.Context, deps Deps, req Request, name string) *result.Result {
	err := deps.Repo.CheckBranchName(ctx, name)
	if err == nil {
		return nil
	}

	if _, refused := errors.AsType[*git.BranchNameError](err); refused {
		return req.Refused(result.Say("git cannot hold %s as a branch name", result.Branch(name)), anotherName(req))
	}

	return req.unread(readName, err)
}

// freeName refuses a name a branch takes, locally first, then on origin, naming the first that does.
func freeName(ctx context.Context, deps Deps, req Request, name string) *result.Result {
	local, onOrigin, err := deps.Repo.BranchesTaking(ctx, name)
	if err != nil {
		return req.unreached(readTaking, err, result.Say("origin could not be reached to list its branches"))
	}

	if len(local) > 0 {
		return req.Refused(
			result.Say("the branch %s takes the name %s locally", result.Branch(local[0]), result.Branch(name)),
			anotherName(req))
	}

	if len(onOrigin) > 0 {
		return req.Refused(
			result.Say("the branch %s on origin takes the name %s", result.Branch(onOrigin[0]), result.Branch(name)),
			anotherName(req))
	}

	return nil
}

// detachedKept refuses a detached HEAD with commits origin's trunk lacks, which the switch would
// leave on no branch, and lists them.
func detachedKept(ctx context.Context, deps Deps, req Request, trunk string) *result.Result {
	own, err := deps.Repo.CommitsPast(ctx, "HEAD", trunk)
	if err != nil {
		return req.unread(readHeadCommits, err)
	}

	if len(own) == 0 {
		return nil
	}

	stop := req.Refused(
		result.Say("HEAD is detached with commits origin's trunk %s lacks, listed here", result.Branch(trunk)),
		result.NextStep("keep them on a branch with `git switch -c <name>`, then run %s", result.Command(PushCommand)))
	stop.Lines = listed(own)

	return stop
}

// aheadLines returns the lines that name the local trunk's commits origin's trunk lacks, which stay
// on the local trunk, and none when it has none.
func aheadLines(ctx context.Context, deps Deps, req Request, trunk string) ([]result.Phrase, *result.Result) {
	ahead, past, err := deps.Repo.LocalTrunkAhead(ctx, trunk)
	if err != nil {
		return nil, req.unread(readLocalTrunk, err)
	}

	if !ahead {
		return nil, nil
	}

	return []result.Phrase{result.Say("The commits %s stay on the local trunk %s, which origin's trunk lacks",
		shas(past), result.Branch(trunk))}, nil
}

// create creates the branch at origin's trunk and puts HEAD on it. A file the switch would
// overwrite is a refusal naming it; any other failure is broke.
func (n newBranch) create(ctx context.Context, deps Deps, req Request) *result.Result {
	name := n.name

	err := deps.Repo.CreateBranch(ctx, name, n.Trunk)
	if err == nil {
		return nil
	}

	if exit, ok := errors.AsType[*child.ExitError](err); ok {
		if files := overwritten(exit.Stderr); len(files) > 0 {
			return req.Refused(
				result.Say("the switch to %s would overwrite %s; nothing was written",
					result.Branch(name), strings.Join(files, ", ")),
				result.NextStep("commit or move those files"+thenRunAgain, req.ilsCommand()))
		}
	}

	broke := req.ends(result.Broke, result.Say("could not start the branch %s", result.Branch(name)), req.onceFixed())
	broke.Lines = []result.Phrase{result.Say("Could not start the branch %s: %s", result.Branch(name), err.Error())}

	return broke
}

// overwritten returns the files git lists, each on a line of its own indented by a tab, as the
// ones a refused switch would overwrite.
func overwritten(stderr string) []string {
	var files []string

	for line := range strings.SplitSeq(stderr, "\n") {
		if file, indented := strings.CutPrefix(line, "\t"); indented {
			files = append(files, strings.TrimSpace(file))
		}
	}

	return files
}
