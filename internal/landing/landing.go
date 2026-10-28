package landing

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

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
// command again; its one name is the command, as Request.ilsCommand marks it. It stays untyped
// because result.NextStep accepts only constant expressions.
const thenRunAgain = ", then run %s again"

// timeLimitEnded and runInterrupted are the clauses a result says a call or a wait was cut short
// with: by the end of the time limit, or by an interrupt. They stay untyped so that result.Say
// accepts wording built on them.
const (
	timeLimitEnded = "the time limit ended"
	runInterrupted = "ils was interrupted"
)

// interrupted reports whether err says an interrupt cut a call short. Only an interrupt cancels
// the context a command runs under, so every cancellation is one.
func interrupted(err error) bool {
	return errors.Is(err, context.Canceled)
}

// timedOut reports whether err says the time limit cut a call short.
func timedOut(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}

// endedEarly reports whether err says the time limit or an interrupt cut a call short.
func endedEarly(err error) bool {
	return timedOut(err) || interrupted(err)
}

// PushCommand and MergeCommand are the command words of `ils push` and `ils merge`. They stay
// untyped so that a next step can name them as result.NextCommand's constant wording.
const (
	PushCommand  = "push"
	MergeCommand = "merge"
)

// Deps is what a command takes: the forge and the repository it runs in, and what it tells how it
// advances to. Neither Progress nor Logger changes the result a command returns; either may be nil.
type Deps struct {
	Forge    Forge
	Progress Progress
	// Logger gets one debug record, with the message "wait", for each decision of the wait for the
	// checks. It is passed to nothing else: git and the forge take their own.
	Logger *slog.Logger
	Repo   git.Repo
}

// Progress is told how a landing command advances, as it advances. A step runs from one Started
// to the next, or to the end of the command.
type Progress interface {
	// Started is told that the step label names began; limit is the most it can last, zero when
	// nothing but the command's end limits it.
	Started(label result.Phrase, limit time.Duration)
	// Said is told that the running step succeeded, saying line: the exact line the result will
	// hold. A step that fails or is cut short says nothing.
	Said(line result.Phrase)
	// Counted is told, at each read of the checks, that done of the total deciding checks have
	// concluded. It is not told while no check has reported.
	Counted(done, total int)
}

// started tells the progress hook, if any, that the step label names began, limited to limit.
func (d Deps) started(label result.Phrase, limit time.Duration) {
	if d.Progress != nil {
		d.Progress.Started(label, limit)
	}
}

// said tells the progress hook, if any, that the running step succeeded saying line.
func (d Deps) said(line result.Phrase) {
	if d.Progress != nil {
		d.Progress.Said(line)
	}
}

// say adds line, the line of a step that succeeded, to lines, and tells the progress hook, if any.
func (d Deps) say(lines *[]result.Phrase, line result.Phrase) {
	*lines = append(*lines, line)
	d.said(line)
}

// startChecking tells the progress hook, if any, that the checks a command makes before it writes
// anything began.
func (d Deps) startChecking() {
	d.started(result.Say("Checking the branch and origin"), 0)
}

// startFinding tells the progress hook, if any, that the search for the branch's pull request began.
func (d Deps) startFinding() {
	d.started(result.Say("Finding the pull request"), 0)
}

// Forge is the host of a repository, seen through the operations `push` and `merge` need.
type Forge interface {
	// Identity fills the fork, can-push and squash-allowed flags of the repository named by host,
	// owner, and name.
	Identity(ctx context.Context, repo forge.Repo) (forge.Repo, error)
	// FindChanges returns the changes whose head is the branch, open, closed without merging,
	// and merged, each marked with its state.
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
	DeleteBranch(ctx context.Context, repo forge.Repo, branch string) error
}

// Request is the command a result is for and the words it was given.
type Request struct {
	Command string
	Words   []string
	// Args are the command's positional words, as typed: for `ils branch`, the name.
	Args []string
}

// ilsCommand is the request's command and its positional words, marked as an ils command, which
// prints as "ils <command> <args>": the command to run again as typed.
func (r Request) ilsCommand() result.Name {
	return result.Command(r.commandLine())
}

// commandLine is the request's command followed by its positional words.
func (r Request) commandLine() string {
	return strings.Join(append([]string{r.Command}, r.Args...), " ")
}

// Ready is what the shared preconditions hand to the steps that follow them.
type Ready struct {
	// Trunk is origin's default branch, as origin just named it.
	Trunk string
	// Standing is the branch the command stands on, or a detached HEAD.
	Standing git.Standing
	// Repo is the repository origin names, with the forge's fork, can-push and squash-allowed flags.
	// GitPreconditions leaves it empty.
	Repo forge.Repo
}

// Preconditions checks what every landing command needs, in a fixed order, and stops at the
// first that fails, having written nothing.
//
// The order is: git is installed and recent enough; there is an origin; its fetch and push URLs
// name one repository; the forge's tool is installed; it is logged in to origin's host; the
// repository is not a fork; the user can push; origin answers for its trunk. When all hold it
// returns the Ready and a nil result. Otherwise it returns the result to end the command with:
// a refusal for a precondition that fails or a read the context's deadline cut short, and broke
// for a read that fails for any other reason.
func Preconditions(ctx context.Context, deps Deps, req Request) (Ready, *result.Result) {
	standing, stop := gitStanding(ctx, deps, req)
	if stop != nil {
		return Ready{}, stop
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

// GitPreconditions checks the preconditions of Preconditions that need git alone, in the same
// order and with the same results: git is installed and recent enough; there is an origin; origin
// answers for its trunk. It never calls the forge, and the Ready it returns has no Repo.
func GitPreconditions(ctx context.Context, deps Deps, req Request) (Ready, *result.Result) {
	standing, stop := gitStanding(ctx, deps, req)
	if stop != nil {
		return Ready{}, stop
	}

	trunk, stop := trunkOf(ctx, deps, req)
	if stop != nil {
		return Ready{}, stop
	}

	return Ready{Trunk: trunk, Standing: standing}, nil
}

// gitStanding returns where HEAD stands once git is installed and recent enough and there is an
// origin.
func gitStanding(ctx context.Context, deps Deps, req Request) (git.Standing, *result.Result) {
	if err := deps.Repo.CheckVersion(ctx); err != nil {
		if stop := req.tooOld(err); stop != nil {
			return git.Standing{}, stop
		}

		return git.Standing{}, req.unread(readGitVersion, err)
	}

	standing, err := deps.Repo.Standing(ctx)
	if err != nil {
		return git.Standing{}, req.unread(readStanding, err)
	}

	if standing.Kind == git.NoOrigin {
		return git.Standing{}, req.Refused(result.Say("this repository has no remote named origin"), createOriginStep())
	}

	return standing, nil
}

// createOriginStep is the next step of a repository with no origin: create it on GitHub.
func createOriginStep() result.Next {
	return result.NextStep("create the repository on GitHub and push to it with `gh repo create --source . --push`")
}

// originRepo returns the repository both of origin's URLs name.
func originRepo(ctx context.Context, deps Deps, req Request) (forge.Repo, *result.Result) {
	urls, repo, oneRepo, err := readOriginRepo(ctx, deps)

	switch {
	case err != nil:
		return forge.Repo{}, req.unread(readURLs, err)
	case !oneRepo:
		return forge.Repo{}, req.twoRepositories(urls)
	}

	return repo, nil
}

// readOriginRepo reads origin's URLs and returns them with the repository they name, and whether
// they name one: both URLs the same GitHub repository. A URL git's answer cannot be parsed into
// names none rather than failing the read; err is a read of readURLs that failed.
func readOriginRepo(ctx context.Context, deps Deps) (urls git.OriginURLs, repo forge.Repo, oneRepo bool, err error) {
	urls, err = deps.Repo.OriginURLs(ctx)
	if _, unparsed := errors.AsType[*git.OriginError](err); unparsed {
		return urls, forge.Repo{}, false, nil
	}

	if err != nil {
		return urls, forge.Repo{}, false, err
	}

	if !urls.Fetch.Same(urls.Push) {
		return urls, forge.Repo{}, false, nil
	}

	return urls, forge.Repo{Host: urls.Fetch.Host, Owner: urls.Fetch.Owner, Name: urls.Fetch.Name}, true, nil
}

// twoRepositories refuses origin's URLs urls, which do not name one GitHub repository.
func (r Request) twoRepositories(urls git.OriginURLs) *result.Result {
	return r.Refused(
		result.Say("origin's fetch URL %s and push URL %s do not name one GitHub repository",
			result.URL(urls.FetchURL), result.URL(urls.PushURL)),
		result.NextStep("point both of origin's URLs at one GitHub repository with `git remote set-url`, by hand"))
}

// identity returns origin with the forge's fork, can-push and squash-allowed flags, once the
// forge's tool is installed, recent enough and logged in, the repository is not a fork, and the user
// can push to it.
func identity(ctx context.Context, deps Deps, req Request, origin forge.Repo) (forge.Repo, *result.Result) {
	repo, err := deps.Forge.Identity(ctx, origin)
	if err != nil {
		return forge.Repo{}, req.identityFailure(err)
	}

	if stop := req.repoRefusal(repo); stop != nil {
		return forge.Repo{}, stop
	}

	return repo, nil
}

// repoRefusal returns the refusal of repo, as the forge's identity read filled it, when it is a fork
// or the user cannot push to it, and nil when ils pushes to it.
func (r Request) repoRefusal(repo forge.Repo) *result.Result {
	byHand := result.NextStep("stop and tell the user to push by hand")

	if repo.Fork {
		return r.Refused(
			result.Say("%s is a fork, which ils does not push to; the user pushes by hand", repo.FullName()),
			byHand)
	}

	if !repo.CanPush {
		return r.Refused(
			result.Say("the logged-in user has no push permission on %s; the user pushes by hand", repo.FullName()),
			byHand)
	}

	return nil
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

// identityFailure returns the result for a forge identity call that failed with err: the refusal
// toolRefusal returns, and what unread returns for any other failure.
func (r Request) identityFailure(err error) *result.Result {
	if stop := r.toolRefusal(err); stop != nil {
		return stop
	}

	return r.unread(readIdentity, err)
}

// toolRefusal returns the refusal for a forge call that failed with err because of gh itself: the
// refusal tooOld returns for a gh that is older than the minimum or names no readable version, a
// refusal naming `gh auth login` for a host gh is not logged in to, and the refusal notInstalled
// returns for a gh that is not installed. It returns nil for any other failure.
func (r Request) toolRefusal(err error) *result.Result {
	if stop := r.tooOld(err); stop != nil {
		return stop
	}

	if loggedOut, ok := errors.AsType[*forge.NotLoggedInError](err); ok {
		host := loggedOut.Host

		return r.Refused(
			result.Say("gh is not logged in to %s; log in with `gh auth login --hostname %s`, "+
				"or push by hand if %s is not GitHub", host, host, host),
			result.NextStep("log in to %s"+thenRunAgain, host, r.ilsCommand()))
	}

	return r.notInstalled(err)
}

// tooOld returns the refusal for a tool whose version check failed with err because the tool
// is older than the minimum or names no readable version, and nil for any other failure.
func (r Request) tooOld(err error) *result.Result {
	old, ok := errors.AsType[*child.ToolTooOldError](err)
	if !ok {
		return nil
	}

	reason := result.Say("%s %s is older than %s, the oldest version ils works with; upgrade it from %s",
		old.Tool, old.Version, old.Minimum, result.URL(old.InstallURL))
	if old.Version == "" {
		reason = result.Say("%s printed `%s`, which names no version ils can read; ils needs %s %s or newer, from %s",
			old.Tool, old.Line, old.Tool, old.Minimum, result.URL(old.InstallURL))
	}

	return r.Refused(reason, result.NextStep("upgrade %s"+thenRunAgain, old.Tool, r.ilsCommand()))
}

// notInstalled returns the refusal for a call that failed with err because the tool it runs is
// not installed, and nil for any other failure. A tool the user configured git to run as its ssh
// command is refused naming the setting to fix; any other tool, naming where to get it.
func (r Request) notInstalled(err error) *result.Result {
	if command, ok := errors.AsType[*git.SSHCommandMissingError](err); ok {
		return r.Refused(
			result.Say("%s, the ssh command %s names, is not found", command.Program, command.Setting),
			result.NextStep("fix %s"+thenRunAgain, command.Setting, r.ilsCommand()))
	}

	missing, ok := errors.AsType[*child.ToolMissingError](err)
	if !ok {
		return nil
	}

	return r.Refused(
		result.Say("%s is not installed; install it from %s", missing.Tool, result.URL(missing.InstallURL)),
		result.NextStep("install %s"+thenRunAgain, missing.Tool, r.ilsCommand()))
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
// when the tool the read runs is not installed or the time limit or an interrupt cut the read
// short, since nothing was written and running again is safe, and broke otherwise.
func (r Request) unread(read string, err error) *result.Result {
	if stop := r.notInstalled(err); stop != nil {
		return stop
	}

	if endedEarly(err) {
		reason := result.Say(timeLimitEnded+" while reading %s; nothing was written", read)
		if interrupted(err) {
			reason = result.Say(runInterrupted+" while reading %s; nothing was written", read)
		}

		return r.Refused(reason, r.again())
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
	return result.NextStep("fix what failed"+thenRunAgain, r.ilsCommand())
}

// again is the next step that runs the request's command again.
func (r Request) again() result.Next {
	return result.NextCommand("%s", r.commandLine())
}

// unreached returns the result for a call to origin that failed with err: the refusal reason
// when origin did not answer, and what unread returns otherwise.
func (r Request) unreached(read string, err error, reason result.Phrase) *result.Result {
	// The time limit and an interrupt are excluded: a call to origin that either cut short is also a
	// *git.RemoteError, and unread words it as what cut it short.
	if _, ok := errors.AsType[*git.RemoteError](err); ok && !endedEarly(err) {
		return r.Refused(reason, r.again())
	}

	return r.unread(read, err)
}

// fetchPullHead fetches the head of pull request number from origin and returns its sha. A failed
// fetch is the stop of read, a refusal when origin could not be reached.
func (r Request) fetchPullHead(ctx context.Context, deps Deps, number int, read string) (string, *result.Result) {
	head, err := deps.Repo.FetchPullHead(ctx, number)
	if err != nil {
		return "", r.unreached(read, err,
			result.Say("origin could not be reached to fetch the head of pull request %s", result.PullRequest(number)))
	}

	return head, nil
}
