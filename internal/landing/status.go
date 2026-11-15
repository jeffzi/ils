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
// `Next:` comes from the checks of the command that would run where HEAD stands, run over the
// answers the report read, so it is the step that command would end on before its first write,
// from the same answers of git and GitHub. A check that would end broke names `ils status` after
// the fix. After a cut, a check that needs a read the cut skipped ends there, as cut short, and
// asks nothing.
//
// On a work branch the checks are push's. A branch with no commit past origin's trunk is a first
// push; the fix of a local trunk ahead of origin's, checked out nowhere, runs `ils merge` after it
// on a green, mergeable pull request. When every check holds, the open pull request at the tip of
// origin's copy decides `Next:`, and `ils push` does otherwise; a cut ends on `ils push`.
//
// On the trunk it reports how the trunk stands against origin's, the commits it has that origin's
// trunk lacks, the last version tag on origin's trunk and the commits since it, whether origin's
// trunk tip is a release commit, and whether rules protect the trunk. On a detached HEAD it lists
// the commits origin's trunk lacks, and the local trunk's, as a work branch does. On both the
// checks are branch's, leaving out its two checks of the name: they need git alone, so gh and
// origin's URLs never decide `Next:` there. A step of branch's that runs it again, or every check
// holding, is `ils branch <name>`.
//
// With no origin, `Next:` creates it on GitHub; outside a repository, or with where HEAD stands cut
// short, there is none. With where HEAD stands otherwise unread, `Next:` is the step of the checks
// every landing command makes first, upgrading git, and fixes what failed when they hold.
//
// A gh that cannot read origin's repository on GitHub, or a repository ils does not push to, is a
// line naming the fix; what git alone tells is still reported, and the reads of GitHub are left
// out. With origin unreachable, origin's trunk and origin's copy of the branch are those of the
// last fetch, and a line says so. With origin turning the caller away, or the read of origin's URLs
// cut short, only the name of origin's trunk is that of the last fetch; when the last fetch names no
// trunk either, where HEAD stands is unread.
func Status(ctx context.Context, deps Deps, req Request) result.Result {
	deps.startChecking()

	r := &reading{deps: deps, req: req, survey: newSurvey(deps)}
	r.readVersion(ctx)
	r.readStanding(ctx)

	next, held := r.next(ctx)

	ended := req.ends(result.Done, result.Phrase{}, next)
	if r.trunkUnread != nil {
		// A check that stops first keeps its step. Where every check holds, the failed read is the
		// report's alone, as the trunk's count against origin's is, and its fix is the step.
		if held {
			next = req.onceFixed()
		}

		ended = req.Broke(*r.trunkUnread, next)
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
	// copied is the commit origin's copy of the branch is on, as read; empty when origin has none.
	copied string
	// unreadPulls, when not empty, is why GitHub was not read for the branch's pull requests, which a
	// work branch then lists as unread, so that a payload with no pull request and no such entry
	// means it has none.
	unreadPulls string
	// trunkUnread is the reason a read of the local trunk that failed other than by a cut gives,
	// which ends the status broke; nil when no such read failed.
	trunkUnread *result.Phrase
	waited      Waited
	status      result.Status
	// pastRead is set once the branch's commits past origin's trunk are read, or the branch has no
	// commit at all, so that a merged pull request can be checked for them.
	pastRead bool
	running  bool
	cut      bool
	offline  bool
	// noTrunk is set when origin turned away the read of its trunk, or a cut ended the read of
	// origin's URLs, and the last fetch names no trunk either: HEAD's branch cannot be told from the
	// trunk, so where HEAD stands is unread.
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

// readVersion reads git's version, which every landing command checks first.
func (r *reading) readVersion(ctx context.Context) {
	if err := r.survey.gitVersion(ctx); err != nil {
		r.failed(readGitVersion, err)
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
		r.readIdentity(ctx)
		r.readDetached(ctx)
		r.readLocalTrunk(ctx)

		return
	case git.OnBranch:
	}

	r.on.branch = standing.Branch
	r.readOrigin(ctx)
	r.readIdentity(ctx)

	if r.noTrunk {
		r.unread(result.Say("Could not read %s: neither origin nor its last fetch names origin's trunk", readStanding),
			readStanding, "neither origin nor its last fetch names origin's trunk")

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
	r.failed(read, err)
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
// fetched or named by the last fetch, and lists them. When origin named its trunk but did not
// answer the fetch of it, they are counted against the last fetch.
func (r *reading) readDetached(ctx context.Context) {
	r.status.Standing = result.StandingDetached

	var (
		own []git.Commit
		err error
	)

	switch {
	case r.cut:
	case r.offline && r.on.Trunk != "":
		own, err = r.deps.Repo.CommitsPast(ctx, "HEAD", r.on.Trunk)
	case r.offline:
		// The last fetch named no trunk either, and that read has already said why.
	case r.on.Trunk != "" || r.fetchUnanswered(ctx):
		own, err = r.survey.detached(ctx)
	default:
		// The read or fetch of origin's trunk that failed has already said why.
	}

	if len(own) == 0 {
		r.say(result.Say("HEAD is detached"))
	} else {
		r.say(detachedWith(r.status.Trunk))
		r.say(listed(own)...)
	}

	for _, commit := range own {
		r.status.Unlanded = append(r.status.Unlanded, commit.SHA)
	}

	if err != nil {
		r.failed(readHeadCommits, err)
	}
}

// fetchUnanswered reports whether origin named its trunk and then did not answer the fetch of it,
// as readOrigin asked them.
func (r *reading) fetchUnanswered(ctx context.Context) bool {
	if _, err := r.survey.trunk(ctx); err != nil {
		return false
	}

	_, err := r.survey.trunkFetch(ctx)

	return errors.Is(err, git.ErrRemote) && !endedEarly(err)
}

// readOrigin reads the repository origin's URLs name and origin's trunk, and fetches that trunk. A
// read of the URLs cut short leaves only the trunk's name, as the clone last fetched it.
func (r *reading) readOrigin(ctx context.Context) {
	origin, err := r.survey.origin(ctx)

	switch {
	case err != nil:
		r.failed(readURLs, err)
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
		r.failed(readFetchedTrunk, err)

		return
	}

	r.on.Trunk, r.trunkTip = trunk, fetched
}

// readLastFetch settles a read of origin's trunk that failed with err. When origin could not be
// reached, origin's trunk and its tip are those of the last fetch, and so is everything counted
// against origin after them; a clone that never learned origin's trunk fails that read, yet still
// counts the branch against origin's copy as last fetched. When origin turned the caller away, the
// read fails, the checks of the command that would run end on it, and only the trunk's name is that
// of the last fetch. Anything else fails the read.
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
// that cannot read it, or a repository ils does not push to, says the reason push refuses with,
// which names the fix, and leaves out the reads of GitHub.
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

// block says the reason of stop, a refusal of the gh check or of origin's URLs, and lists the
// branch's pull requests as unread for the reason why, since GitHub is not read after it. Only a
// work branch ends on that refusal: off one, it is said alone.
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

// next is the step a status ends on, from the checks of the command that would run where HEAD
// stands: on a work branch, the step branchNext names; on the trunk or a detached HEAD, the step
// startNext names. With no origin, or where HEAD stands unread, it is the step of the checks every
// landing command makes first, upgrading git or creating origin, and fix what failed when they hold;
// outside a repository, or with where HEAD stands cut short, there is none. held reports whether
// the step is that of every check holding. Once a cut ended the report, the record is settled
// first: a check that needs a read the cut skipped ends there, as cut short, and asks nothing.
func (r *reading) next(ctx context.Context) (step result.Next, held bool) {
	if err := ctx.Err(); r.cut && err != nil {
		r.survey.settle(err)
	}

	switch r.status.Standing {
	case result.StandingBranch:
		return r.branchNext(ctx)
	case result.StandingTrunk, result.StandingDetached:
		return r.startNext(ctx)
	case result.StandingNoRepository:
		return result.NextNone(), false
	case result.StandingNoOrigin:
	}

	if _, err := r.survey.standing(ctx); endedEarly(err) {
		return result.NextNone(), false
	}

	if _, stop := gitStanding(ctx, r.survey, r.req); stop != nil {
		return stop.Next, false
	}

	return r.req.onceFixed(), false
}

// branchNext is the step a status on a work branch ends on: the step of the first of push's checks
// that stops, run over the answers the report read, and with none, the step the open pull request
// decides when origin's copy is at the tip, and `ils push` otherwise.
func (r *reading) branchNext(ctx context.Context) (result.Next, bool) {
	req := r.req
	req.ChecksOf, req.MergeReady = PushCommand, r.open != nil && r.mergeable()

	ready, stop := preconditions(ctx, r.survey, req)
	if stop != nil {
		return stop.Next, false
	}

	p, stop := pushChecks(ctx, r.survey, req, ready)
	if stop != nil {
		return stop.Next, false
	}

	if !p.onOrigin || p.open == nil {
		return pushAgain(), true
	}

	return r.pullNext(p), true
}

// startNext is the step a status on the trunk or a detached HEAD ends on: the step of the first of
// branch's checks that stops, run over the answers the report read and leaving out the two checks
// of the name, and with none, starting a branch.
func (r *reading) startNext(ctx context.Context) (result.Next, bool) {
	req := r.req
	req.ChecksOf = BranchCommand

	ready, stop := gitPreconditions(ctx, r.survey, req)
	if stop != nil {
		return stop.Next, false
	}

	if _, stop := (newBranch{Ready: ready}).checks(ctx, r.survey, req); stop != nil {
		return stop.Next, false
	}

	return startBranchStep(), true
}

// mergeable reports whether the wait found the head green and the pull request clean, or waiting
// on a review alone.
func (r *reading) mergeable() bool {
	state := r.waited.MergeState

	return r.waited.Outcome == WaitGreen && (state == forge.MergeClean || state == forge.MergeBlockedByReview)
}

// pullNext is the step the open pull request at the tip of p's branch decides: the step `ils push`
// ends a wait on that found the same verdict and merge state. What one read cannot settle is left
// to `ils push`: checks still running, a verdict or merge state not read, a merge state GitHub has
// not decided on green checks, and nothing reported, where push's end turns on a read of the
// trunk's CI that status does not make.
func (r *reading) pullNext(p pushReady) result.Next {
	w := r.waited
	// As in push's wait, the merge state read after green checks can find the pull request apart
	// from the trunk.
	if w.Outcome == WaitGreen {
		w.takeApart()
	}

	unsettled := w.Outcome == "" || w.MergeState == "" || w.Outcome == WaitNothingReported ||
		w.Outcome == WaitGreen && w.MergeState == forge.MergeUnknown
	if unsettled {
		return pushAgain()
	}

	return w.endReport(r.req.ending(), p, r.open.Number).next
}
