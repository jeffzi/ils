// Package landing runs the landing commands: preconditions first, then the sequence.
//
// It returns values and never prints, reads stdin, or exits.
package landing

import (
	"context"
	"errors"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// The reads of the preconditions, as a result names them.
const (
	readStanding = "where HEAD stands"
	readURLs     = "origin's URLs"
	readIdentity = "the repository's identity on GitHub"
	readTrunk    = "origin's trunk"
)

// Deps is what a command takes: the forge and the repository it runs in.
type Deps struct {
	Forge forge.Forge
	Repo  git.Repo
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
// The order is: there is an origin; its fetch and push URLs name one repository; the forge's
// tool is installed; it is logged in to origin's host; the repository is not a fork; the user
// can push; origin answers for its trunk. When all hold it returns the Ready and true.
// Otherwise it returns false and the result to end the command with: a refusal for a
// precondition that fails or a read the context's deadline cut short, and broke for a read
// that fails for any other reason.
func Preconditions(ctx context.Context, deps Deps, req Request) (Ready, result.Result, bool) {
	ready, stop := check(ctx, deps, req)
	if stop != nil {
		return Ready{}, *stop, false
	}

	return ready, result.Result{}, true
}

func check(ctx context.Context, deps Deps, req Request) (Ready, *result.Result) {
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
// installed and logged in, the repository is not a fork, and the user can push to it.
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

	// The deadline comes first: a call to origin that it cut short is also a *git.RemoteError.
	if errors.Is(err, context.DeadlineExceeded) {
		return "", req.unread(readTrunk, err)
	}

	_, unreached := errors.AsType[*git.RemoteError](err)
	if unreached || errors.Is(err, git.ErrNoSymref) {
		return "", req.refused(
			result.Say("origin could not be reached to ask for its trunk"),
			result.NextCommand("push"),
		)
	}

	return "", req.unread(readTrunk, err)
}

// identityFailure returns the result for a forge identity call that failed with err.
func (r Request) identityFailure(err error) *result.Result {
	if missing, ok := errors.AsType[*forge.ToolMissingError](err); ok {
		return r.refused(
			result.Say("%s is not installed; install it with `%s`", missing.Tool, missing.InstallHint),
			result.NextStep("install %s, then run ils push again", missing.Tool),
		)
	}

	if loggedOut, ok := errors.AsType[*forge.NotLoggedInError](err); ok {
		host := loggedOut.Host

		return r.refused(
			result.Say("gh is not logged in to %s; log in with `gh auth login --hostname %s`, "+
				"or push by hand if %s is not GitHub", host, host, host),
			result.NextStep("log in to %s, then run ils push again", host),
		)
	}

	return r.unread(readIdentity, err)
}

// refused returns a refusal of the request.
func (r Request) refused(reason result.Phrase, next result.Next) *result.Result {
	return &result.Result{
		Command: r.Command,
		Words:   r.Words,
		Outcome: result.Refused,
		Reason:  reason,
		Next:    next,
	}
}

// unread returns the result for a precondition read that failed with err: a refusal when the
// context's deadline cut it short, since nothing was written and running again is safe, and
// broke otherwise.
func (r Request) unread(read string, err error) *result.Result {
	if errors.Is(err, context.DeadlineExceeded) {
		return r.refused(
			result.Say("the time limit ended while reading %s; nothing was written", read),
			result.NextCommand("push"),
		)
	}

	return &result.Result{
		Command: r.Command,
		Words:   r.Words,
		Outcome: result.Broke,
		Lines: []result.Phrase{
			result.Say("Could not read %s: %s", read, err.Error()),
			result.Say("Nothing was written."),
		},
		Reason: result.Say("could not read %s", read),
		Next:   result.NextStep("fix what failed, then run ils push again"),
	}
}
