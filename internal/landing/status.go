package landing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// Status runs `ils status`: it reads where HEAD stands, how the branch stands against origin, and
// the branch's pull requests on GitHub, then says what comes next. It never refuses, and it ends
// done except when the local trunk cannot be read, which every landing command ends broke on: then
// it ends broke, naming that read.
//
// Every read is best-effort. A read that fails becomes a line naming what could not be read and
// why, and an entry of the payload's unread list; the reads that do not depend on it still run. A
// read the time limit or an interrupt cut short is such a line, and every read after it is skipped.
// The only writes are fetches from origin: its trunk, the branch's copy, the head of each merged
// pull request whose commits it checks, and a version tag whose commit the clone lacks. They store
// objects and move remote-tracking refs and FETCH_HEAD, nothing a user owns.
//
// A git older than the minimum or with no version ils can read, or an ssh that origin's URLs need
// and that is not installed, is the step every landing command refuses with first, and `Next:` is
// that step wherever HEAD stands.
//
// On a work branch `Next:` comes from push's own checks, run over the answers the report read, so
// it is the step `ils push` would end on before its first write, from the same answers of git and
// GitHub. A check that would end broke names `ils status` after the fix; a branch with no commit
// past origin's trunk is a first push; the fix of a local trunk ahead of origin's, checked out
// nowhere, runs `ils merge` after it on a green, mergeable pull request. When every check holds,
// the open pull request at the tip of origin's copy decides `Next:`, and `ils push` does otherwise.
// After a cut, a check that needs a read the cut skipped ends on `ils push`, and asks nothing.
//
// On the trunk it reports how the trunk stands against origin's, the commits it has that origin's
// trunk lacks, the last version tag on origin's trunk and the commits since it, whether origin's
// trunk tip is a release commit, and whether rules protect the trunk; `Next:` fixes what failed
// when the local trunk could not be read, or the fetch of origin's trunk failed other than by a cut
// or an origin that could not be reached, then fixes a local trunk ahead of origin's, and otherwise
// starts a branch.
//
// On a detached HEAD it lists the commits origin's trunk lacks, and `Next:` keeps them on a branch,
// or starts a branch when there are none; it fixes what failed when those commits could not be
// read, or origin's trunk could not be read or fetched and no last fetch stands in for it, other
// than by a cut or an origin that could not be reached, which branch refuses and names itself to
// run again. With no origin, `Next:` creates it on GitHub; outside a repository it says so.
//
// A gh that cannot read origin's repository on GitHub, or a repository ils does not push to, is a
// line naming the fix; what git alone tells is still reported, the reads of GitHub are left out, and
// on a work branch `Next:` is the step push would refuse with. With origin unreachable, origin's
// trunk and origin's copy of the branch are those of the last fetch, and a line says so. With origin
// turning the caller away, or the read of origin's URLs cut short, only the name of origin's trunk
// is that of the last fetch, and on a work branch `Next:` is the step push ends on; when the last
// fetch names no trunk either, where HEAD stands is unread, and `Next:` fixes what failed.
func Status(ctx context.Context, deps Deps, req Request) result.Result {
	deps.startChecking()

	r := &reading{deps: deps, req: req, survey: newSurvey(deps)}
	r.readVersion(ctx)
	r.readStanding(ctx)

	ended := req.ends(result.Done, result.Phrase{}, r.next(ctx))
	if r.trunkUnread != nil {
		ended.Outcome, ended.Reason = result.Broke, *r.trunkUnread
	}

	ended.Lines = r.lines
	ended.Payload = &result.Payload{Status: &r.status}

	return *ended
}

// reading is a status being read: the facts found so far, the lines that say them, and whether a
// cut ended the reads.
type reading struct {
	open     *forge.Change
	landed   *forge.Change
	trunkTip string
	on       onBranch
	// survey holds the reads status shares with the checks of push and branch; deps makes the
	// reads only status makes, and those it counts against the last fetch when origin does not
	// answer.
	survey   *survey
	deps     Deps
	req      Request
	repo     forge.Repo
	lines    []result.Phrase
	unlanded []git.Commit
	ahead    *aheadTrunk
	// copied is the commit origin's copy of the branch is on, as read; empty when origin has none.
	copied string
	// unreadPulls, when not empty, is why GitHub was not read for the branch's pull requests, which a
	// work branch then lists as unread, so that a payload with no pull request and no such entry
	// means it has none.
	unreadPulls string
	// trunkUnread is the reason a read of the local trunk that failed other than by a cut gives,
	// which ends the status broke; nil when no such read failed.
	trunkUnread *result.Phrase
	// refused is the step the shared preconditions of every landing command refuse with for git, or
	// for the ssh origin's URLs need, or end on for origin's trunk when where HEAD stands cannot be
	// told without it. It comes before the step the standing decides everywhere but on a work
	// branch, whose step branchNext takes from push's checks instead.
	refused result.Next
	waited  Waited
	status  result.Status
	// pastRead is set once the branch's commits past origin's trunk are read, or the branch has no
	// commit at all, so that a merged pull request can be checked for them.
	pastRead bool
	running  bool
	cut      bool
	offline  bool
	// branchUnread is set when the fetch of origin's trunk, a read of the local trunk, or the read of
	// a detached HEAD's commits failed other than by a cut, or those commits went unread for want of
	// origin's trunk on an origin that answered: on the trunk or a detached HEAD, branch would end on
	// the same failure, so no step past the fix can be told. A work branch takes its step from push's
	// checks instead.
	branchUnread bool
	// trunkUnreached is set when origin could not be reached to fetch its trunk: branch refuses a
	// detached HEAD there and names itself to run again.
	trunkUnreached bool
	// noTrunk is set when origin turned away the read of its trunk, or a cut ended the read of
	// origin's URLs, and the last fetch names no trunk either: HEAD's branch cannot be told from the
	// trunk.
	noTrunk bool
}

func (r *reading) say(lines ...result.Phrase) {
	r.lines = append(r.lines, lines...)
}

// failed records that the read named read failed with err. A cut ends every read after it.
func (r *reading) failed(read string, err error) {
	if !endedEarly(err) {
		line, _ := couldNotRead(read, err)
		r.unread(line, read, err.Error())

		return
	}

	r.cut = true

	if interrupted(err) {
		r.unread(result.Say("Stopped reading %s: "+runInterrupted+", so the reads after it were skipped", read),
			read, runInterrupted)

		return
	}

	r.unread(result.Say("Stopped reading %s: "+timeLimitEnded+", so the reads after it were skipped", read, r.req.limitWords()),
		read, fmt.Sprintf(timeLimitEnded, r.req.limitWords()))
}

// unread says line and lists the read named read as unread, for the reason given.
func (r *reading) unread(line result.Phrase, read, reason string) {
	r.say(line)
	r.markUnread(read, reason)
}

func (r *reading) markUnread(read, reason string) {
	r.status.Unread = append(r.status.Unread, result.Unread{Read: read, Error: reason})
}

// failedBranch records that the read named read, one push, merge or branch also makes, failed with
// err and, unless a cut ended it, leaves the branch unread for next.
func (r *reading) failedBranch(read string, err error) {
	r.failed(read, err)
	r.branchUnread = r.branchUnread || !r.cut
}

// failedPrecondition records that the read named read, one the shared preconditions make, failed
// with err, and takes the step they end on for it: upgrading a git older than the minimum or with
// no readable version, installing a tool a read runs, or else, unless a cut ended the read, fixing
// what failed. The first such step stands, as the preconditions stop at the first failure.
func (r *reading) failedPrecondition(read string, err error) {
	r.failed(read, err)

	own := r.req.ownCommand()

	stop := r.req.tooOld(err, own)
	if stop == nil {
		stop = r.req.notInstalled(err, own)
	}

	switch {
	case !r.refused.None():
	case stop != nil:
		r.refused = stop.Next
	case !r.cut:
		r.refused = r.req.onceFixed()
	}
}

// readVersion reads git's version, which every landing command checks first.
func (r *reading) readVersion(ctx context.Context) {
	if err := r.survey.gitVersion(ctx); err != nil {
		r.failedPrecondition(readGitVersion, err)
	}
}

// readStanding reads where HEAD stands and, on a work branch, everything a status reports of it.
func (r *reading) readStanding(ctx context.Context) {
	if r.cut {
		return
	}

	standing, err := r.survey.standing(ctx)
	if err != nil {
		r.readNoStanding(ctx, err)

		return
	}

	switch standing.Kind {
	case git.NoOrigin:
		r.status.Standing = result.StandingNoOrigin
		r.say(result.Say("This repository has no remote named origin"))

		return
	case git.Detached:
		r.readOrigin(ctx)
		r.readDetached(ctx)

		return
	case git.OnBranch:
	}

	r.on.branch = standing.Branch
	r.readOrigin(ctx)
	r.readIdentity(ctx)

	if r.noTrunk {
		r.unread(result.Say("Could not read %s: neither origin nor its last fetch names origin's trunk", readStanding),
			readStanding, "neither origin nor its last fetch names origin's trunk")

		if r.refused.None() {
			r.refused = r.req.onceFixed()
		}

		return
	}

	if standing.Branch == r.status.Trunk {
		r.readOnTrunk(ctx)

		return
	}

	r.status.Standing, r.status.Branch = result.StandingBranch, standing.Branch
	r.say(result.Say("On the branch %s", result.Branch(standing.Branch)))

	r.readTree(ctx)
	r.readCommits(ctx)
	r.readCopy(ctx)
	r.readShared(ctx)
	r.readLocalTrunk(ctx)
	r.readPulls(ctx)
}

// readTree reads the changed tracked files, which push and merge refuse.
func (r *reading) readTree(ctx context.Context) {
	if r.cut {
		return
	}

	files, err := r.survey.changed(ctx)
	if err != nil {
		r.failed(readTree, err)

		return
	}

	if len(files) > 0 {
		r.sayRefusal(r.req.dirtyTree(files))
	}
}

// readShared reads whether the branch's first commit past the trunk stops a push, as push reads it.
func (r *reading) readShared(ctx context.Context) {
	if r.cut || len(r.on.past) == 0 {
		return
	}

	found, read, err := r.firstShared(ctx)
	if err != nil {
		r.failed(read, err)

		return
	}

	if stop := found.stop(r.req, r.on.branch); stop != nil {
		r.sayRefusal(stop)
	}
}

// firstShared reads the other local branches that carry the branch's first commit past the trunk,
// as push reads them, once origin answered the reads of its copy of the branch. Otherwise it reads
// them against the copy as read, none or as last fetched, for the report alone.
func (r *reading) firstShared(ctx context.Context) (firstShared, string, error) {
	if !r.offline && r.copyRead(ctx) {
		return r.survey.firstShared(ctx)
	}

	return readFirstShared(ctx, r.deps, pushReady{onBranch: r.on, copied: r.copied})
}

// copyRead reports whether origin answered whether it has the branch and, when it has, the fetch of
// its copy, both of which readCopy asked first.
func (r *reading) copyRead(ctx context.Context) bool {
	found, err := r.survey.onOrigin(ctx)
	if err == nil && found {
		_, err = r.survey.copyFetch(ctx)
	}

	return err == nil
}

// readLocalTrunk reads whether the local trunk has commits origin's trunk lacks, which every
// landing command refuses, and says the commits and the reason; next later names the fix.
func (r *reading) readLocalTrunk(ctx context.Context) {
	if r.cut || r.on.Trunk == "" {
		return
	}

	ahead, read, err := r.trunkAhead(ctx)

	switch {
	case err != nil:
		r.failedTrunk(read, err)
	case ahead != nil:
		r.ahead = ahead
		r.say(listed(ahead.past)...)
		r.say(ahead.reason())
	}
}

// trunkAhead reads the local trunk as every landing command does, or, when origin did not answer,
// against origin's trunk as last fetched.
func (r *reading) trunkAhead(ctx context.Context) (*aheadTrunk, string, error) {
	if r.offline {
		return readTrunkAhead(ctx, r.deps, r.on.Trunk, r.status.Standing == result.StandingTrunk)
	}

	return r.survey.trunkAhead(ctx)
}

// failedTrunk records that the read named read, a read of the local trunk, failed with err and,
// unless a cut ended it, keeps the reason that ends the status broke.
func (r *reading) failedTrunk(read string, err error) {
	r.failedBranch(read, err)
	if !r.cut {
		_, reason := couldNotRead(read, err)
		r.trunkUnread = &reason
	}
}

// sayRefusal says the lines and reason of stop, a refusal push would end on.
func (r *reading) sayRefusal(stop *result.Result) {
	r.say(stop.Lines...)
	r.say(stop.Reason)
}

// readNoStanding settles a read of where HEAD stands that failed with err: outside a repository
// it says so, and anywhere else the read fails.
func (r *reading) readNoStanding(ctx context.Context, err error) {
	if endedEarly(err) {
		r.failed(readStanding, err)

		return
	}

	missing, isMissing := errors.AsType[*git.NoRepositoryError](r.deps.Repo.CheckRepository(ctx))
	if !isMissing {
		r.failed(readStanding, err)

		return
	}

	r.status.Standing = result.StandingNoRepository
	r.say(result.Say("Not in a git repository git can use: %s", missing.Error()))
}

// readDetached reads the commits of the detached HEAD origin's trunk lacks, once origin's trunk is
// fetched, and lists them. A read of them that fails other than by a cut, or an origin's trunk that
// neither origin nor the last fetch gave though origin answered, leaves them unread for next.
func (r *reading) readDetached(ctx context.Context) {
	r.status.Standing = result.StandingDetached

	var (
		own []git.Commit
		err error
	)

	switch {
	case r.cut:
	case r.on.Trunk == "":
		// The read or fetch of origin's trunk that failed has already said why. An origin that could
		// not be reached leaves starting a branch the next step, as branch refuses there and names
		// itself to run again; offline with no trunk is an unanswered read no last fetch stands in for.
		r.branchUnread = !r.trunkUnreached && !r.offline
	case r.offline:
		own, err = r.deps.Repo.CommitsPast(ctx, "HEAD", r.on.Trunk)
	default:
		own, err = r.survey.detached(ctx)
	}

	if len(own) == 0 {
		r.say(result.Say("HEAD is detached"))
	} else {
		r.say(detachedWith(r.on.Trunk))
		r.say(listed(own)...)
	}

	for _, commit := range own {
		r.status.Unlanded = append(r.status.Unlanded, commit.SHA)
	}

	if err != nil {
		r.failedBranch(readHeadCommits, err)
	}
}

// readOrigin reads the repository origin's URLs name and origin's trunk, and fetches that trunk. A
// read of the URLs cut short leaves only the trunk's name, as the clone last fetched it.
func (r *reading) readOrigin(ctx context.Context) {
	origin, err := r.survey.origin(ctx)

	switch {
	case err != nil:
		r.failedPrecondition(readURLs, err)
		r.unreadPulls = err.Error()
	case !origin.oneRepo:
		r.block(r.req.twoRepositories(origin.urls), "origin's URLs do not name one GitHub repository")
	default:
		r.repo = origin.repo
	}

	if r.cut {
		r.learnTrunk(ctx)

		return
	}

	trunk, err := r.survey.trunk(ctx)
	if err != nil {
		r.readLastFetch(ctx, err)

		return
	}

	r.status.Trunk = trunk

	fetched, err := r.survey.trunkFetch(ctx)
	if err != nil {
		// Push and branch refuse a trunk fetch that could not reach origin, and run again; they end
		// broke on any other failure.
		if errors.Is(err, git.ErrRemote) && !endedEarly(err) {
			r.failed(readFetchedTrunk, err)
			r.trunkUnreached = true
		} else {
			r.failedBranch(readFetchedTrunk, err)
		}

		return
	}

	r.on.Trunk, r.trunkTip = trunk, fetched
}

// readLastFetch settles a read of origin's trunk that failed with err. When origin could not be
// reached, origin's trunk and its tip are those of the last fetch, and so is everything counted
// against origin after them; a clone that never learned origin's trunk fails that read, yet still
// counts the branch against origin's copy as last fetched. When origin turned the caller away, the
// read fails, push ends on it, and only the trunk's name is that of the last fetch. Anything else
// fails the read.
func (r *reading) readLastFetch(ctx context.Context, err error) {
	if !git.Unanswered(err) || endedEarly(err) {
		r.failed(readTrunk, err)

		return
	}

	if errors.Is(err, git.ErrRefused) {
		r.failed(readTrunk, err)
		r.learnTrunk(ctx)

		return
	}

	trunk, found, lastErr := r.deps.Repo.LastFetchedTrunk(ctx)

	var tip string
	if found {
		tip, found, lastErr = r.deps.Repo.LastFetched(ctx, trunk)
	}

	switch {
	case lastErr != nil:
		r.failed(readTrunk, lastErr)

		return
	case !found:
		// The clone never learned origin's trunk, yet origin's copy of the branch, as last fetched,
		// still counts the branch.
		r.failed(readTrunk, err)
	default:
		r.status.Trunk, r.on.Trunk, r.trunkTip = trunk, trunk, tip
		// The read of origin's trunk still failed, whatever the last fetch stands in for.
		r.markUnread(readTrunk, err.Error())
	}

	r.offline = true
	r.say(result.Say("Origin could not be reached, so what is counted against origin is counted against the last fetch"))
}

// learnTrunk takes the name of origin's trunk as the clone last fetched it. It asks origin nothing,
// so it runs past ctx's end: a cut that skipped the reads of origin still leaves HEAD's branch told
// apart from the trunk. A read that fails, or a last fetch that names no trunk, sets noTrunk.
func (r *reading) learnTrunk(ctx context.Context) {
	trunk, _, err := r.deps.Repo.LastFetchedTrunk(context.WithoutCancel(ctx))
	if err != nil {
		r.failed(readTrunk, err)
	} else {
		r.status.Trunk = trunk
	}

	r.noTrunk = r.status.Trunk == ""
}

// readIdentity reads origin's repository on GitHub, which every read of GitHub after it needs. A gh
// that cannot read it, or a repository ils does not push to, is the line push refuses with, which
// names the fix, and leaves out the reads of GitHub.
func (r *reading) readIdentity(ctx context.Context) {
	origin := r.repo
	r.repo = forge.Repo{}

	if r.cut || origin == (forge.Repo{}) {
		return
	}

	repo, err := r.survey.identity(ctx)
	if err != nil {
		if stop := r.req.toolRefusal(err); stop != nil {
			r.block(stop, err.Error())
			r.markUnread(readIdentity, err.Error())

			return
		}

		r.failed(readIdentity, err)
		r.unreadPulls = err.Error()

		return
	}

	if stop := r.req.repoRefusal(repo); stop != nil {
		r.block(stop, "ils does not push to "+origin.FullName())

		return
	}

	r.repo = repo
}

// block says the reason of the refusal stop that push would end on, and lists the branch's pull
// requests as unread for the reason why, since GitHub is not read after it.
func (r *reading) block(stop *result.Result, why string) {
	r.say(stop.Reason)
	r.unreadPulls = why
}

// readCommits reads the branch's tip and, once origin's trunk is fetched, its commits past it.
func (r *reading) readCommits(ctx context.Context) {
	if r.cut {
		return
	}

	got, err := r.survey.tip(ctx)
	tip := got.tip

	switch {
	case err != nil:
		r.failed(readTip, err)

		return
	case !got.committed:
		r.pastRead = r.on.Trunk != ""
		r.say(result.Say("The branch %s has no commit", result.Branch(r.on.branch)))

		return
	}

	r.on.tip = tip

	if r.on.Trunk == "" {
		return
	}

	past, err := r.commitsPast(ctx)
	if err != nil {
		r.failed(readCommits, err)

		return
	}

	r.on.past, r.pastRead = past, true
	if len(past) == 0 {
		r.say(result.Say("The branch %s has no commit past origin's trunk %s",
			result.Branch(r.on.branch), result.Branch(r.on.Trunk)))
	}
}

// commitsPast reads the branch's commits past origin's trunk as push does, or, when origin did not
// answer, past origin's trunk as last fetched.
func (r *reading) commitsPast(ctx context.Context) ([]git.Commit, error) {
	if r.offline {
		return r.deps.Repo.CommitsPast(ctx, r.on.tip, r.on.Trunk)
	}

	return r.survey.past(ctx)
}

// readCopy asks origin whether it has the branch, fetches its copy, and counts the commits the
// branch and that copy each have that the other lacks.
func (r *reading) readCopy(ctx context.Context) {
	if r.cut {
		return
	}

	branch := result.Branch(r.on.branch)

	theirs, found, read, err := r.copyTip(ctx)

	switch {
	case err != nil:
		r.failed(read, err)

		return
	case !found:
		r.say(result.Say("The branch %s is not on origin", branch))

		return
	}

	r.status.OnOrigin, r.copied = true, theirs

	if r.on.tip == "" {
		return
	}

	apart, err := r.divergence(ctx, theirs)
	if err != nil {
		r.failed(readCopy, err)

		return
	}

	r.sayApart(apart, result.Concat("branch ", branch), result.Concat("origin's copy of it"))
}

// divergence counts the commits the branch and origin's copy of it, at theirs, each have that the
// other lacks, as push does, or against the copy as last fetched when origin did not answer.
func (r *reading) divergence(ctx context.Context, theirs string) (git.Divergence, error) {
	if r.offline {
		return r.deps.Repo.Divergence(ctx, r.on.tip, theirs)
	}

	return r.survey.divergence(ctx)
}

// sayApart records how many commits ours and theirs each have that the other lacks, as apart
// counts them, and says it. ours names the local side after "The" ("trunk"), and theirs names
// origin's side ("origin's trunk").
func (r *reading) sayApart(apart git.Divergence, ours, theirs result.Name) {
	r.status.Ahead, r.status.Behind = apart.Ahead, apart.Behind

	switch ahead, behind := nounCount(apart.Ahead, "commit"), nounCount(apart.Behind, "commit"); {
	case apart.Ahead > 0 && apart.Behind > 0:
		r.say(result.Say("The %s and %s have diverged: %s ahead, %s behind", ours, theirs, ahead, behind))
	case apart.Ahead > 0:
		r.say(result.Say("The %s is ahead of %s by %s", ours, theirs, ahead))
	case apart.Behind > 0:
		r.say(result.Say("The %s is behind %s by %s", ours, theirs, behind))
	default:
		r.say(result.Say("The %s is up to date with %s", ours, theirs))
	}
}

// copyTip returns the tip of origin's copy of the branch and whether origin has one: fetched now, or
// as last fetched when origin could not be reached. A read that fails is named by read.
func (r *reading) copyTip(ctx context.Context) (theirs string, found bool, read string, err error) {
	if r.offline {
		theirs, found, err = r.deps.Repo.LastFetched(ctx, r.on.branch)

		return theirs, found, readCopy, err
	}

	found, err = r.survey.onOrigin(ctx)
	if err != nil || !found {
		return "", found, readOnOrigin, err
	}

	theirs, err = r.survey.copyFetch(ctx)

	return theirs, true, readCopy, err
}

// next is the step a status ends on: on a work branch, the step branchNext names; elsewhere, the
// step the shared preconditions refuse with, wherever HEAD stands; otherwise on the trunk, the step
// trunkNext names; on a detached HEAD, fix what failed when its commits past origin's trunk could
// not be read other than for an origin that could not be reached, keep its commits on a branch, or
// start one when it has none; with no origin, create it.
func (r *reading) next(ctx context.Context) result.Next {
	if r.status.Standing == result.StandingBranch {
		return r.branchNext(ctx)
	}

	if !r.refused.None() {
		return r.refused
	}

	switch r.status.Standing {
	case result.StandingTrunk:
		return r.trunkNext()
	case result.StandingDetached:
		if r.branchUnread {
			return r.req.onceFixed()
		}

		if len(r.status.Unlanded) > 0 {
			return keepDetachedStep(result.Command(PushCommand))
		}

		return startBranchStep()
	case result.StandingNoOrigin:
		return createOriginStep()
	default:
		return result.NextNone()
	}
}

// branchNext is the step a status on a work branch ends on: the step of the first of push's checks
// that stops, run over the answers the report read, and with none, the step the open pull request
// decides when origin's copy is at the tip, and `ils push` otherwise. Once a cut ended the report,
// the record is settled first: a check that needs a read the cut skipped ends there, as cut short,
// and asks nothing.
func (r *reading) branchNext(ctx context.Context) result.Next {
	if err := ctx.Err(); r.cut && err != nil {
		r.survey.settle(err)
	}

	req := r.req
	req.MergeReady = r.open != nil && r.mergeable()

	ready, stop := preconditions(ctx, r.survey, req)
	if stop != nil {
		return stop.Next
	}

	p, stop := pushChecks(ctx, r.survey, req, ready)
	if stop != nil {
		return stop.Next
	}

	if !p.onOrigin || p.open == nil {
		return pushAgain()
	}

	return r.pullNext()
}

// trunkNext is the step a status on the trunk ends on: fix what failed when the local trunk could
// not be read, as push ends on it, or the fetch of origin's trunk failed other than by a cut or an
// origin that could not be reached, as branch ends on it, then the fix of a local trunk ahead of
// origin's, and otherwise start a branch. An unreached origin still starts a branch: branch refuses
// there and names itself to run again.
func (r *reading) trunkNext() result.Next {
	switch {
	case r.branchUnread:
		return r.req.onceFixed()
	case r.ahead != nil:
		return r.ahead.fix(result.Command(PushCommand))
	}

	return startBranchStep()
}

// mergeable reports whether the wait found the head green and the pull request clean, or waiting
// on a review alone.
func (r *reading) mergeable() bool {
	state := r.waited.MergeState

	return r.waited.Outcome == WaitGreen && (state == forge.MergeClean || state == forge.MergeBlockedByReview)
}

// pullNext is the step the open pull request decides: a head other than the branch's tip, or its
// checks still running, both left to `ils push`; then red, the pull request apart from the trunk,
// then the merge, once a review is in when it waits on one. Any other state is left to `ils push`,
// which reports it.
func (r *reading) pullNext() result.Next {
	w := r.waited

	switch {
	case r.running:
		return pushAgain()
	case w.Outcome == WaitRed || w.takeApart():
		report, _ := w.stopReport(r.req.ending(), r.open.HeadSHA, r.status.Trunk, r.open.Number)

		return report.next
	case r.mergeable():
		return mergeStep(r.repo, r.open.Number, w.MergeState)
	}

	return pushAgain()
}
