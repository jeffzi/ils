// Package landing runs the landing commands: preconditions first, then the sequence.
//
// It returns values and never prints, reads stdin, or exits.
package landing

import (
	"context"
	"errors"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// The reads of the preconditions, as a result names them.
const (
	readGitVersion = "git's version"
	readStanding   = "where HEAD stands"
	readURLs       = "origin's URLs"
	readIdentity   = "the repository's identity on GitHub"
	readTrunk      = "origin's trunk"
)

// thenPushAgain is the ending of a next step that names what to do before running ils push again.
// It stays untyped because result.NextStep accepts only constant expressions.
const thenPushAgain = ", then run ils push again"

// Deps is what a command takes: the forge and the repository it runs in.
type Deps struct {
	Forge Forge
	Repo  git.Repo
}

// Forge is the host of a repository, seen through the operations `push` needs.
type Forge interface {
	// Identity fills the fork and can-push flags of the repository named by host, owner, and name.
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
	// Repo is the repository origin names, with the forge's fork and can-push flags.
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
		return Ready{}, req.refused(
			result.Say("this repository has no remote named origin"),
			result.NextStep("create the repository on GitHub and push to it with `gh repo create --source . --push`"),
		)
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
		return forge.Repo{}, req.refused(
			result.Say("origin's fetch URL %s and push URL %s do not name one GitHub repository", urls.FetchURL, urls.PushURL),
			result.NextStep("point both of origin's URLs at one GitHub repository with `git remote set-url`, by hand"),
		)
	}

	return forge.Repo{Host: urls.Fetch.Host, Owner: urls.Fetch.Owner, Name: urls.Fetch.Name}, nil
}

// identity returns origin with the forge's fork and can-push flags, once the forge's tool is
// installed, recent enough and logged in, the repository is not a fork, and the user can push to it.
func identity(ctx context.Context, deps Deps, req Request, origin forge.Repo) (forge.Repo, *result.Result) {
	repo, err := deps.Forge.Identity(ctx, origin)
	if err != nil {
		return forge.Repo{}, req.identityFailure(err)
	}

	byHand := result.NextStep("stop and tell the user to push by hand")

	if repo.Fork {
		return forge.Repo{}, req.refused(
			result.Say("%s/%s is a fork, which ils does not push to; the user pushes by hand", repo.Owner, repo.Name),
			byHand,
		)
	}

	if !repo.CanPush {
		return forge.Repo{}, req.refused(
			result.Say("the logged-in user has no push permission on %s/%s; the user pushes by hand", repo.Owner, repo.Name),
			byHand,
		)
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
		return "", req.refused(reason, pushAgain())
	}

	return "", req.unreached(readTrunk, err, reason)
}

// identityFailure returns the result for a forge identity call that failed with err.
func (r Request) identityFailure(err error) *result.Result {
	if stop := r.notInstalled(err); stop != nil {
		return stop
	}

	if stop := r.tooOld(err); stop != nil {
		return stop
	}

	if loggedOut, ok := errors.AsType[*forge.NotLoggedInError](err); ok {
		host := loggedOut.Host

		return r.refused(
			result.Say("gh is not logged in to %s; log in with `gh auth login --hostname %s`, "+
				"or push by hand if %s is not GitHub", host, host, host),
			result.NextStep("log in to %s"+thenPushAgain, host),
		)
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

	return r.refused(reason, result.NextStep("upgrade %s"+thenPushAgain, old.Tool))
}

// notInstalled returns the refusal for a call that failed with err because the tool it runs is
// not installed, naming where to get the tool, and nil for any other failure.
func (r Request) notInstalled(err error) *result.Result {
	missing, ok := errors.AsType[*child.ToolMissingError](err)
	if !ok {
		return nil
	}

	return r.refused(
		result.Say("%s is not installed; install it from %s", missing.Tool, missing.InstallURL),
		result.NextStep("install %s"+thenPushAgain, missing.Tool),
	)
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

// refused returns the result that ends the request as Refused, with reason and next, and no
// lines, because nothing was written.
func (r Request) refused(reason result.Phrase, next result.Next) *result.Result {
	return r.ends(result.Refused, reason, next)
}

// unread returns the result for a read made before the push that failed with err: a refusal
// when the tool the read runs is not installed or the context's deadline cut the read short,
// since nothing was written and running again is safe, and broke otherwise.
func (r Request) unread(read string, err error) *result.Result {
	if stop := r.notInstalled(err); stop != nil {
		return stop
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return r.refused(
			result.Say("the time limit ended while reading %s; nothing was written", read),
			pushAgain(),
		)
	}

	line, reason := couldNotRead(read, err)

	broke := r.ends(result.Broke, reason, onceFixed())
	broke.Lines = []result.Phrase{line, result.Say("Nothing was written.")}

	return broke
}

// couldNotRead returns the line and the reason that say a read failed with err.
func couldNotRead(read string, err error) (line, reason result.Phrase) {
	return result.Say("Could not read %s: %s", read, err.Error()), result.Say("could not read %s", read)
}

// onceFixed is the judgment step after a failure the user has to look at.
func onceFixed() result.Next {
	return result.NextStep("fix what failed" + thenPushAgain)
}

func pushAgain() result.Next {
	return result.NextCommand("push")
}

// unreached returns the result for a call to origin that failed with err: the refusal reason
// when origin did not answer, and what unread returns otherwise.
func (r Request) unreached(read string, err error, reason result.Phrase) *result.Result {
	// The deadline comes first: a call to origin that it cut short is also a *git.RemoteError.
	if errors.Is(err, context.DeadlineExceeded) {
		return r.unread(read, err)
	}

	if _, ok := errors.AsType[*git.RemoteError](err); ok {
		return r.refused(reason, pushAgain())
	}

	return r.unread(read, err)
}
