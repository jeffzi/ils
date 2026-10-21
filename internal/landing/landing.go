// Package landing runs the landing commands: preconditions first, then the sequence.
//
// It returns values and never prints, reads stdin, or exits.
package landing

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// Each read names what a precondition reads as it follows "read" in a result, as in "Could not read
// origin's URLs", so it stays a noun phrase.
const (
	readGitVersion = "git's version"
	readStanding   = "where HEAD stands"
	readURLs       = "origin's URLs"
	readIdentity   = "the repository's identity on GitHub"
	readTrunk      = "origin's trunk"
)

// thenRunAgain is the ending of a next step that names what to do before running the request's
// command again; its one name is the command. It stays untyped because result.NextStep accepts
// only constant expressions.
const thenRunAgain = ", then run ils %s again"

// writeMessage is the judgment step that has the agent write the squash commit's message. Its names
// are the repository and the pull request's number; the title and the body stay placeholders for
// the agent to write. It stays untyped because result.NextStep accepts only constant expressions.
const writeMessage = "write the pull request's title and body as the squash commit's message with " +
	"`gh pr edit --repo %s %s --title … --body …`"

// PushCommand and MergeCommand are the command words of `ils push` and `ils merge`. They stay
// untyped so that a next step can name them as result.NextCommand's constant wording.
const (
	PushCommand  = "push"
	MergeCommand = "merge"
)

// Deps is what a command takes: the forge and the repository it runs in.
type Deps struct {
	Forge Forge
	Repo  git.Repo
}

// Forge is the host of a repository, seen through the operations `push` and `merge` need.
type Forge interface {
	// Identity fills the fork, can-push and squash-allowed flags of the repository named by host,
	// owner, and name.
	Identity(ctx context.Context, repo forge.Repo) (forge.Repo, error)
	// FindChanges returns the open changes whose head is the branch, and the merged ones;
	// a change that was closed without merging is never returned.
	FindChanges(ctx context.Context, repo forge.Repo, branch string) ([]forge.Change, error)
	// OpenChange opens a change with an empty body, and returns forge.ErrChangeExists when the
	// branch already has one.
	OpenChange(ctx context.Context, repo forge.Repo, change forge.NewChange) (forge.ChangeRef, error)
	// Head returns the tip of the change the query names and whether it can land.
	Head(ctx context.Context, repo forge.Repo, query forge.HeadQuery) (forge.Head, error)
	TrunkProtection(ctx context.Context, repo forge.Repo, trunk string) (forge.Protection, error)
	Verdict(ctx context.Context, repo forge.Repo, query forge.VerdictQuery) (forge.Verdict, error)
	// HasTrunkCI reports whether the repository runs CI on the trunk: a workflow the forge runs
	// whose file is on the trunk. A trunk with no workflow file has none; it is not an error.
	HasTrunkCI(ctx context.Context, repo forge.Repo, trunk string) (bool, error)
	// Message returns the change's title and body, and the trailer block the forge would end its
	// squash commit with.
	Message(ctx context.Context, repo forge.Repo, number int) (forge.Message, error)
	// Land squash-merges the change with the landing's message, refused by the forge once the
	// change's head has moved past the landing's. It never deletes the change's branch.
	Land(ctx context.Context, repo forge.Repo, landing forge.Landing) error
	// DeleteBranch deletes the branch on the forge.
	DeleteBranch(ctx context.Context, repo forge.Repo, branch string) error
}

// Request is the command a result is for and the words it was given.
type Request struct {
	Command string
	Words   []string
}

// Ready is what the shared preconditions hand to the steps that follow them.
type Ready struct {
	// Trunk is origin's default branch, as origin just named it.
	Trunk string
	// Standing is the branch the command stands on, or a detached HEAD.
	Standing git.Standing
	// Repo is the repository origin names, with the forge's fork, can-push and squash-allowed flags.
	Repo forge.Repo
}

// Preconditions checks what every landing command needs, in a fixed order, and stops at the
// first that fails, having written nothing.
//
// The order is: git is installed and recent enough; there is an origin; its fetch and push URLs
// name one repository; the forge's tool is installed; it is logged in to origin's host; the
// repository is not a fork; the user can push; origin answers for its trunk. When all hold it
// returns the Ready and true. Otherwise it returns false and the result to end the command with:
// a refusal for a precondition that fails or a read the context's deadline cut short, and broke
// for a read that fails for any other reason.
func Preconditions(ctx context.Context, deps Deps, req Request) (Ready, result.Result, bool) {
	ready, stop := sharedChecks(ctx, deps, req)
	if stop != nil {
		return Ready{}, *stop, false
	}

	return ready, result.Result{}, true
}

// sharedChecks runs the shared preconditions in order and returns the result that stops at the
// first that fails.
func sharedChecks(ctx context.Context, deps Deps, req Request) (Ready, *result.Result) {
	if err := deps.Repo.CheckVersion(ctx); err != nil {
		if stop := req.tooOld(err); stop != nil {
			return Ready{}, stop
		}

		return Ready{}, req.unread(readGitVersion, err)
	}

	standing, err := deps.Repo.Standing(ctx)
	if err != nil {
		return Ready{}, req.unread(readStanding, err)
	}

	if standing.Kind == git.NoOrigin {
		return Ready{}, req.Refused(
			result.Say("this repository has no remote named origin"),
			result.NextStep("create the repository on GitHub and push to it with `gh repo create --source . --push`"))
	}

	origin, stop := originRepo(ctx, deps, req)
	if stop != nil {
		return Ready{}, stop
	}

	repo, stop := identity(ctx, deps, req, origin)
	if stop != nil {
		return Ready{}, stop
	}

	trunk, stop := trunkOf(ctx, deps, req)
	if stop != nil {
		return Ready{}, stop
	}

	return Ready{Trunk: trunk, Standing: standing, Repo: repo}, nil
}

// originRepo returns the repository both of origin's URLs name.
func originRepo(ctx context.Context, deps Deps, req Request) (forge.Repo, *result.Result) {
	urls, err := deps.Repo.OriginURLs(ctx)

	_, unparsed := errors.AsType[*git.OriginError](err)
	if err != nil && !unparsed {
		return forge.Repo{}, req.unread(readURLs, err)
	}

	if unparsed || !urls.Fetch.Same(urls.Push) {
		return forge.Repo{}, req.Refused(
			result.Say("origin's fetch URL %s and push URL %s do not name one GitHub repository", urls.FetchURL, urls.PushURL),
			result.NextStep("point both of origin's URLs at one GitHub repository with `git remote set-url`, by hand"))
	}

	return forge.Repo{Host: urls.Fetch.Host, Owner: urls.Fetch.Owner, Name: urls.Fetch.Name}, nil
}

// identity returns origin with the forge's fork, can-push and squash-allowed flags, once the
// forge's tool is installed, recent enough and logged in, the repository is not a fork, and the user
// can push to it.
func identity(ctx context.Context, deps Deps, req Request, origin forge.Repo) (forge.Repo, *result.Result) {
	repo, err := deps.Forge.Identity(ctx, origin)
	if err != nil {
		return forge.Repo{}, req.identityFailure(err)
	}

	byHand := result.NextStep("stop and tell the user to push by hand")

	if repo.Fork {
		return forge.Repo{}, req.Refused(
			result.Say("%s is a fork, which ils does not push to; the user pushes by hand", repo.FullName()),
			byHand)
	}

	if !repo.CanPush {
		return forge.Repo{}, req.Refused(
			result.Say("the logged-in user has no push permission on %s; the user pushes by hand", repo.FullName()),
			byHand)
	}

	return repo, nil
}

// trunkOf returns origin's default branch, asked of origin.
func trunkOf(ctx context.Context, deps Deps, req Request) (string, *result.Result) {
	trunk, err := deps.Repo.Trunk(ctx)
	if err == nil {
		return trunk, nil
	}

	reason := result.Say("origin could not be reached to ask for its trunk")

	// An origin that answers without naming a trunk is refused like one that did not answer.
	if errors.Is(err, git.ErrNoSymref) {
		return "", req.Refused(reason, req.again())
	}

	return "", req.unreached(readTrunk, err, reason)
}

// identityFailure returns the result for a forge identity call that failed with err: the
// refusal tooOld returns for a gh that is older than the minimum or names no readable version,
// a refusal naming `gh auth login` for a host gh is not logged in to, and what unread returns
// for any other failure.
func (r Request) identityFailure(err error) *result.Result {
	if stop := r.tooOld(err); stop != nil {
		return stop
	}

	if loggedOut, ok := errors.AsType[*forge.NotLoggedInError](err); ok {
		host := loggedOut.Host

		return r.Refused(
			result.Say("gh is not logged in to %s; log in with `gh auth login --hostname %s`, "+
				"or push by hand if %s is not GitHub", host, host, host),
			result.NextStep("log in to %s"+thenRunAgain, host, r.Command))

	}

	return r.unread(readIdentity, err)
}

// tooOld returns the refusal for a tool whose version check failed with err because the tool
// is older than the minimum or names no readable version, and nil for any other failure.
func (r Request) tooOld(err error) *result.Result {
	old, ok := errors.AsType[*child.ToolTooOldError](err)
	if !ok {
		return nil
	}

	reason := result.Say("%s %s is older than %s, the oldest version ils works with; upgrade it from %s",
		old.Tool, old.Version, old.Minimum, old.InstallURL)
	if old.Version == "" {
		reason = result.Say("%s printed `%s`, which names no version ils can read; ils needs %s %s or newer, from %s",
			old.Tool, old.Line, old.Tool, old.Minimum, old.InstallURL)
	}

	return r.Refused(reason, result.NextStep("upgrade %s"+thenRunAgain, old.Tool, r.Command))
}

// notInstalled returns the refusal for a call that failed with err because the tool it runs is
// not installed, and nil for any other failure. A tool the user configured git to run as its ssh
// command is refused naming the setting to fix; any other tool, naming where to get it.
func (r Request) notInstalled(err error) *result.Result {
	if command, ok := errors.AsType[*git.SSHCommandMissingError](err); ok {
		return r.Refused(
			result.Say("%s, the ssh command %s names, is not found", command.Program, command.Setting),
			result.NextStep("fix %s"+thenRunAgain, command.Setting, r.Command))
	}

	missing, ok := errors.AsType[*child.ToolMissingError](err)
	if !ok {
		return nil
	}

	return r.Refused(
		result.Say("%s is not installed; install it from %s", missing.Tool, missing.InstallURL),
		result.NextStep("install %s"+thenRunAgain, missing.Tool, r.Command))
}

// ends returns the result that ends the request as outcome, with reason and next and no lines
// or payload.
func (r Request) ends(outcome result.Outcome, reason result.Phrase, next result.Next) *result.Result {
	return &result.Result{
		Command: r.Command,
		Words:   r.Words,
		Outcome: outcome,
		Reason:  reason,
		Next:    next,
	}
}

// Refused returns the result that ends the request as Refused, with reason and next, and no
// lines, because nothing was written.
func (r Request) Refused(reason result.Phrase, next result.Next) *result.Result {
	return r.ends(result.Refused, reason, next)
}

// unread returns the result for a read made before any write that failed with err: a refusal
// when the tool the read runs is not installed or the context's deadline cut the read short,
// since nothing was written and running again is safe, and broke otherwise.
func (r Request) unread(read string, err error) *result.Result {
	if stop := r.notInstalled(err); stop != nil {
		return stop
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return r.Refused(
			result.Say("the time limit ended while reading %s; nothing was written", read),
			r.again())
	}

	line, reason := couldNotRead(read, err)

	broke := r.ends(result.Broke, reason, r.onceFixed())
	broke.Lines = []result.Phrase{line, result.Say("Nothing was written.")}

	return broke
}

// couldNotRead returns the line and the reason that say a read failed with err.
func couldNotRead(read string, err error) (line, reason result.Phrase) {
	return result.Say("Could not read %s: %s", read, err.Error()), result.Say("could not read %s", read)
}

// onceFixed is the judgment step after a failure the user has to look at, ending in the
// request's command.
func (r Request) onceFixed() result.Next {
	return result.NextStep("fix what failed"+thenRunAgain, r.Command)
}

// again is the next step that runs the request's command again.
func (r Request) again() result.Next {
	return result.NextCommand("%s", r.Command)
}

// unreached returns the result for a call to origin that failed with err: the refusal reason
// when origin did not answer, and what unread returns otherwise.
func (r Request) unreached(read string, err error, reason result.Phrase) *result.Result {
	// The deadline comes first: a call to origin that it cut short is also a *git.RemoteError.
	if errors.Is(err, context.DeadlineExceeded) {
		return r.unread(read, err)
	}

	if _, ok := errors.AsType[*git.RemoteError](err); ok {
		return r.Refused(reason, r.again())
	}

	return r.unread(read, err)
}

// The reads of the checks of the branch, as a result names them.
const (
	readTree         = "the working tree's changes"
	readTip          = "the branch's tip"
	readFetchedTrunk = "the commits on origin's trunk"
	readCommits      = "the branch's commits"
	readChanges      = "the branch's pull requests"
	readMergedHead   = "the merged pull request's head"
)

// startBranch is the judgment step out of a branch that cannot land; its two names are the
// trunk. The new branch's name stays a placeholder for the agent to choose.
const startBranch = "start a branch from origin's trunk: " +
	"`git fetch origin %s && git switch -c <name> --no-track origin/%s`"

// startBranchStep is the startBranch step with both of its names set to the trunk.
func startBranchStep(trunk string) result.Next {
	return result.NextStep(startBranch, trunk, trunk)
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

// onWorkBranch refuses a detached HEAD and the trunk.
func (r Request) onWorkBranch(ready Ready) *result.Result {
	next := startBranchStep(ready.Trunk)

	if ready.Standing.Kind != git.OnBranch {
		return r.Refused(result.Say("HEAD is detached, so there is no branch to push"), next)
	}

	if ready.Standing.Branch == ready.Trunk {
		return r.Refused(result.Say("HEAD is on the trunk %s, which ils never pushes", ready.Trunk), next)
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
		return req.Refused(
			result.Say("tracked files have changes that are not committed: %s", strings.Join(files, ", ")),
			result.NextStep("commit them"+thenRunAgain, req.Command))
	}

	return nil
}

// fetchTrunk fetches origin's trunk, so that the reads of origin/<trunk> that follow are current.
func fetchTrunk(ctx context.Context, deps Deps, req Request, trunk string) *result.Result {
	if _, err := deps.Repo.Fetch(ctx, trunk); err != nil {
		return req.unreached(readFetchedTrunk, err,
			result.Say("origin could not be reached to fetch its trunk %s", trunk))
	}

	return nil
}

// fetchPullHead fetches the head of pull request number from origin and returns its sha. A failed
// fetch is the stop of read, a refusal when origin could not be reached.
func (r Request) fetchPullHead(ctx context.Context, deps Deps, number int, read string) (string, *result.Result) {
	head, err := deps.Repo.Fetch(ctx, "refs/pull/"+strconv.Itoa(number)+"/head")
	if err != nil {
		return "", r.unreached(read, err,
			result.Say("origin could not be reached to fetch the head of pull request #%s", number))
	}

	return head, nil
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

// nothingPast refuses a branch with no commit that origin's trunk lacks. Its next step is the push
// of the new work, which for `ils merge` is a first push and not a run again.
func (r Request) nothingPast(b onBranch) *result.Result {
	next := result.NextStep("commit the work, then run ils push again")
	if r.Command == MergeCommand {
		next = result.NextStep("commit the work, then run ils push")
	}

	return r.Refused(result.Say("the branch %s has no commit past origin's trunk %s", b.branch, b.Trunk), next)
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
				result.Say("pull request #%s from %s is open against %s and not against the trunk %s",
					change.Number, b.branch, change.Base, b.Trunk),
				result.NextStep("close it or change its base with `gh pr edit %s --base %s`"+thenRunAgain,
					change.Number, b.Trunk, req.Command))
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

// landedWith reports whether the merged pull request change carries every commit of the branch
// past the trunk, by patch: false when it shares none of them. It refuses a branch that shares a
// commit with it and has commits it lacks, listing those. It returns the pull request's head as
// fetched from origin, or nothing when a read failed.
func (b onBranch) landedWith(ctx context.Context, deps Deps, req Request, change forge.Change) (string, bool, *result.Result) {
	head, stop := req.fetchPullHead(ctx, deps, change.Number, readMergedHead)
	if stop != nil {
		return "", false, stop
	}

	theirs, err := deps.Repo.CommitsPast(ctx, head, b.Trunk)
	if err != nil {
		return "", false, req.unread(readMergedHead, err)
	}

	shared := slices.ContainsFunc(theirs, func(commit git.Commit) bool {
		return slices.ContainsFunc(b.past, func(mine git.Commit) bool { return mine.SHA == commit.SHA })
	})
	if !shared {
		return head, false, nil
	}

	unlanded, err := deps.Repo.Unlanded(ctx, head, b.tip, b.Trunk)
	if err != nil {
		return "", false, req.unread(readCommits, err)
	}

	if len(unlanded) == 0 {
		return head, true, nil
	}

	stop = req.Refused(
		result.Say("pull request #%s from %s was merged without the commits listed here", change.Number, b.branch),
		result.NextStep(startBranch+", then bring those commits over with `git cherry-pick %s`; "+
			"expect a conflict where the squash touched the same files", b.Trunk, b.Trunk, shas(unlanded)))

	stop.Lines = listed(unlanded)

	return head, false, stop
}

// alreadyLanded refuses a branch whose every commit landed with the merged pull request landed:
// its work is on the trunk, so only a new branch has anything left to land.
func (b onBranch) alreadyLanded(req Request, landed forge.Change) *result.Result {
	return req.Refused(
		result.Say("the branch %s already landed with pull request #%s", b.branch, landed.Number),
		startBranchStep(b.Trunk))
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
