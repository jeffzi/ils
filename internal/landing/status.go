package landing

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// Status runs `ils status`: it reads where HEAD stands, how the branch stands against origin, and
// the branch's pull requests on GitHub, then says what comes next. It never refuses and always ends
// done.
//
// Every read is best-effort. A read that fails becomes a line naming what could not be read and
// why, and an entry of the payload's unread list; the reads that do not depend on it still run. A
// read the time limit or an interrupt cut short is such a line, and every read after it is skipped.
// The only writes are fetches from origin: its trunk, the branch's copy, the head of each merged
// pull request whose commits it checks, and a version tag whose commit the clone lacks. They store
// objects and move remote-tracking refs and FETCH_HEAD, nothing a user owns.
//
// On a work branch `Next:` mirrors the order in which `ils push` refuses and ends, so that it never
// names a command that would refuse: an open pull request against a base other than the trunk,
// unless the branch is behind origin's copy or has no commit past the trunk, which push refuses
// first; then a merged pull request that carries the branch's commits, then origin's copy of the
// branch, then the branch's commits past the trunk, then the open pull request.
//
// On the trunk it reports how the trunk stands against origin's, the last version tag on origin's
// trunk and the commits since it, whether origin's trunk tip is a release commit, and whether rules
// protect the trunk; `Next:` starts a branch.
//
// On a detached HEAD it lists the commits origin's trunk lacks, and `Next:` keeps them on a branch,
// or starts a branch when there are none. With no origin, `Next:` creates it on GitHub; outside a
// repository it says so.
//
// A gh that cannot read origin's repository on GitHub, or a repository ils does not push to, is a
// line naming the fix; what git alone tells is still reported, the reads of GitHub are left out, and
// on a work branch `Next:` is the step push would refuse with. With origin unreachable, origin's
// trunk and origin's copy of the branch are those of the last fetch, and a line says so.
func Status(ctx context.Context, deps Deps, req Request) result.Result {
	deps.startChecking()

	r := &reading{deps: deps}
	r.readStanding(ctx)

	done := req.ends(result.Done, result.Phrase{}, r.next())
	done.Lines = r.lines
	done.Payload = &result.Payload{Status: &r.status}

	return *done
}

// reading is a status being read: the facts found so far, the lines that say them, and whether a
// cut ended the reads.
type reading struct {
	open      *forge.Change
	elsewhere *forge.Change
	landed    *forge.Change
	trunkTip  string
	on        onBranch
	deps      Deps
	repo      forge.Repo
	lines     []result.Phrase
	unlanded  []git.Commit
	blocked   result.Next
	// dirty, shared and trunkAhead are the steps push and merge refuse with for changed tracked
	// files, a first commit another local branch carries, and a local trunk ahead of origin's.
	dirty, shared, trunkAhead result.Next
	// copied is the commit origin's copy of the branch is on, as read; empty when origin has none.
	copied string
	// unreadPulls, when not empty, is why GitHub was not read for the branch's pull requests.
	unreadPulls string
	waited      Waited
	status      result.Status
	pastRead    bool
	running     bool
	cut         bool
	offline     bool
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

	r.unread(result.Say("Stopped reading %s: "+timeLimitEnded+", so the reads after it were skipped", read, timeLimitWords),
		read, fmt.Sprintf(timeLimitEnded, timeLimitWords))
}

// unread says line and lists the read named read as unread, for the reason given.
func (r *reading) unread(line result.Phrase, read, reason string) {
	r.say(line)
	r.markUnread(read, reason)
}

// markUnread lists the read named read as unread, for the reason given.
func (r *reading) markUnread(read, reason string) {
	r.status.Unread = append(r.status.Unread, result.Unread{Read: read, Error: reason})
}

// readStanding reads where HEAD stands and, on a work branch, everything a status reports of it.
func (r *reading) readStanding(ctx context.Context) {
	standing, err := r.deps.Repo.Standing(ctx)
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

	files, err := r.deps.Repo.DirtyFiles(ctx)
	if err != nil {
		r.failed(readTree, err)

		return
	}

	if len(files) > 0 {
		r.dirty = r.refusal(Request{Command: PushCommand}.dirtyTree(files))
	}
}

// readShared reads whether the branch's first commit past the trunk stops a push, as push reads it.
func (r *reading) readShared(ctx context.Context) {
	if r.cut || len(r.on.past) == 0 {
		return
	}

	stop, read, err := sharedFirstCommit(ctx, r.deps, Request{Command: PushCommand},
		pushReady{onBranch: r.on, copied: r.copied})

	switch {
	case err != nil:
		r.failed(read, err)
	case stop != nil:
		r.shared = r.refusal(stop)
	}
}

// readLocalTrunk reads whether the local trunk has commits origin's trunk lacks, which merge refuses.
func (r *reading) readLocalTrunk(ctx context.Context) {
	if r.cut || r.on.Trunk == "" {
		return
	}

	ahead, past, err := r.deps.Repo.LocalTrunkAhead(ctx, r.on.Trunk)

	switch {
	case err != nil:
		r.failed(readLocalTrunk, err)
	case ahead:
		r.trunkAhead = r.refusal(Request{Command: MergeCommand}.trunkAhead(r.on.Trunk, past))
	}
}

// refusal says the reason and lines of stop, a refusal push or merge would end on, and returns its
// step.
func (r *reading) refusal(stop *result.Result) result.Next {
	r.say(stop.Lines...)
	r.say(stop.Reason)

	return stop.Next
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
// fetched, and lists them.
func (r *reading) readDetached(ctx context.Context) {
	r.status.Standing = result.StandingDetached

	var (
		own []git.Commit
		err error
	)

	if !r.cut && r.on.Trunk != "" {
		own, err = r.deps.Repo.CommitsPast(ctx, "HEAD", r.on.Trunk)
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
		r.failed(readHeadCommits, err)
	}
}

// readOrigin reads the repository origin's URLs name and origin's trunk, and fetches that trunk.
func (r *reading) readOrigin(ctx context.Context) {
	urls, repo, oneRepo, err := readOriginRepo(ctx, r.deps)

	switch {
	case err != nil:
		r.failed(readURLs, err)

		if !r.cut {
			r.skippedPulls(err.Error())
		}
	case !oneRepo:
		r.block(Request{Command: PushCommand}.twoRepositories(urls), "origin's URLs do not name one GitHub repository")
	default:
		r.repo = repo
	}

	if r.cut {
		return
	}

	trunk, err := r.deps.Repo.Trunk(ctx)
	if err != nil {
		r.readLastFetch(ctx, err)

		return
	}

	r.status.Trunk = trunk

	fetched, err := r.deps.Repo.Fetch(ctx, trunk)
	if err != nil {
		r.failed(readFetchedTrunk, err)

		return
	}

	r.on.Trunk, r.trunkTip = trunk, fetched
}

// readLastFetch settles a read of origin's trunk that failed with err. When origin could not be
// reached, origin's trunk and its tip are those of the last fetch, and so is everything counted
// against origin after them; a clone that never learned origin's trunk fails that read, yet still
// counts the branch against origin's copy as last fetched. Anything else fails the read.
func (r *reading) readLastFetch(ctx context.Context, err error) {
	if _, unreached := errors.AsType[*git.RemoteError](err); !unreached || endedEarly(err) {
		r.failed(readTrunk, err)

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

// readIdentity reads origin's repository on GitHub, which every read of GitHub after it needs. A gh
// that cannot read it, or a repository ils does not push to, is the line push refuses with, which
// names the fix, and leaves out the reads of GitHub.
func (r *reading) readIdentity(ctx context.Context) {
	origin := r.repo
	r.repo = forge.Repo{}

	if r.cut || origin == (forge.Repo{}) {
		return
	}

	push := Request{Command: PushCommand}

	repo, err := r.deps.Forge.Identity(ctx, origin)
	if err != nil {
		if stop := push.toolRefusal(err); stop != nil {
			r.block(stop, err.Error())
			r.markUnread(readIdentity, err.Error())

			return
		}

		r.failed(readIdentity, err)

		if !r.cut {
			r.skippedPulls(err.Error())
		}

		return
	}

	if stop := push.repoRefusal(repo); stop != nil {
		r.block(stop, "ils does not push to "+origin.FullName())

		return
	}

	r.repo = repo
}

// block says the refusal stop that push would end on, takes its step as status's next, and lists
// the branch's pull requests as unread for the reason why, since GitHub is not read after it.
func (r *reading) block(stop *result.Result, why string) {
	r.say(stop.Reason)
	r.blocked = stop.Next
	r.skippedPulls(why)
}

// skippedPulls records why GitHub is not read for the branch's pull requests, which a work branch
// then lists as unread, so that a payload with no pull request and no such entry means it has none.
func (r *reading) skippedPulls(why string) {
	r.unreadPulls = why
}

// readCommits reads the branch's tip and, once origin's trunk is fetched, its commits past it.
func (r *reading) readCommits(ctx context.Context) {
	if r.cut {
		return
	}

	tip, committed, err := r.on.readTip(ctx, r.deps)

	switch {
	case err != nil:
		r.failed(readTip, err)

		return
	case !committed:
		r.pastRead = r.on.Trunk != ""
		r.say(result.Say("The branch %s has no commit", result.Branch(r.on.branch)))

		return
	}

	r.on.tip = tip

	if r.on.Trunk == "" {
		return
	}

	past, err := r.deps.Repo.CommitsPast(ctx, tip, r.on.Trunk)
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

// readCopy asks origin whether it has the branch, fetches its copy, and counts the commits the
// branch and that copy each have that the other lacks.
func (r *reading) readCopy(ctx context.Context) {
	if r.cut {
		return
	}

	branch := result.Branch(r.on.branch)

	theirs, found, read, err := r.copyTip(ctx)

	r.status.OnOrigin = found

	switch {
	case err != nil:
		r.failed(read, err)

		return
	case !found:
		r.say(result.Say("The branch %s is not on origin", branch))

		return
	}

	r.copied = theirs

	if r.on.tip == "" {
		return
	}

	apart, err := r.deps.Repo.Divergence(ctx, r.on.tip, theirs)
	if err != nil {
		r.failed(readCopy, err)

		return
	}

	r.status.Ahead, r.status.Behind = apart.Ahead, apart.Behind

	switch ahead, behind := nounCount(apart.Ahead, "commit"), nounCount(apart.Behind, "commit"); {
	case apart.Ahead > 0 && apart.Behind > 0:
		r.say(result.Say("The branch %s and origin's copy of it have diverged: %s ahead, %s behind", branch, ahead, behind))
	case apart.Ahead > 0:
		r.say(result.Say("The branch %s is ahead of origin's copy of it by %s", branch, ahead))
	case apart.Behind > 0:
		r.say(result.Say("The branch %s is behind origin's copy of it by %s", branch, behind))
	default:
		r.say(result.Say("The branch %s is up to date with origin's copy of it", branch))
	}
}

// copyTip returns the tip of origin's copy of the branch and whether origin has one: fetched now, or
// as last fetched when origin could not be reached. A read that fails is named by read.
func (r *reading) copyTip(ctx context.Context) (theirs string, found bool, read string, err error) {
	if r.offline {
		theirs, found, err = r.deps.Repo.LastFetched(ctx, r.on.branch)

		return theirs, found, readCopy, err
	}

	_, found, err = r.deps.Repo.RemoteBranch(ctx, r.on.branch)
	if err != nil || !found {
		return "", found, readOnOrigin, err
	}

	theirs, err = r.deps.Repo.Fetch(ctx, r.on.branch)

	return theirs, true, readCopy, err
}

// readPulls reads the branch's pull requests: the open one against the trunk, and every closed or
// merged one, each merged one checked for the branch's commits it carries.
func (r *reading) readPulls(ctx context.Context) {
	if r.cut {
		return
	}

	if r.repo == (forge.Repo{}) {
		if r.unreadPulls != "" {
			r.markUnread(readChanges, r.unreadPulls)
		}

		return
	}

	changes, err := r.deps.Forge.FindChanges(ctx, r.repo, r.on.branch)
	if err != nil {
		r.failed(readChanges, err)

		return
	}

	for _, change := range changes {
		switch {
		case change.State == forge.ChangeOpen && r.status.Trunk != "" && change.Base == r.status.Trunk:
			r.open = &change
		case change.State == forge.ChangeOpen && r.status.Trunk != "":
			r.elsewhere = &change
			r.say(result.Say("Pull request %s is open against %s and not against the trunk %s",
				result.PullRequest(change.Number), result.Branch(change.Base), result.Branch(r.status.Trunk)))
		case change.State != forge.ChangeOpen:
			r.status.PastPullRequests = append(r.status.PastPullRequests,
				result.PastPull{Number: change.Number, State: string(change.State)})
			r.say(result.Say("Pull request %s: %s", result.PullRequest(change.Number), string(change.State)))
		}
	}

	for _, change := range changes {
		if change.State == forge.ChangeMerged && r.landed == nil {
			r.readLanded(ctx, change)
		}
	}

	switch {
	case r.open != nil:
		r.readOpen(ctx)
	case r.status.Trunk != "":
		r.say(result.Say("The branch %s has no open pull request against the trunk %s",
			result.Branch(r.on.branch), result.Branch(r.status.Trunk)))
	}
}

// readLanded fetches the head of the merged pull request change and reads whether it carries
// every commit of the branch past the trunk, as push's check of a merged head does.
func (r *reading) readLanded(ctx context.Context, change forge.Change) {
	if r.cut || !r.pastRead {
		return
	}

	head, err := r.deps.Repo.FetchPullHead(ctx, change.Number)
	if err != nil {
		r.failed(readMergedHead, err)

		return
	}

	shared, unlanded, read, err := r.on.carriedBy(ctx, r.deps, head)

	switch pull := result.PullRequest(change.Number); {
	case err != nil:
		r.failed(read, err)
	case !shared:
	case len(unlanded) == 0:
		r.landed = &change
		r.say(result.Say("Pull request %s landed every commit of the branch", pull))
	case r.unlanded == nil:
		r.unlanded = unlanded
		r.say(result.Say("Pull request %s was merged without the commits listed here", pull))
		r.say(listed(unlanded)...)
	}
}

// readOpen reads the open pull request: its title and body, the checks the trunk requires, its
// merge state, and the verdict of its head's checks.
func (r *reading) readOpen(ctx context.Context) {
	pull := &result.OpenPull{
		Number: r.open.Number, URL: r.open.URL, Title: r.open.Title,
		TitleOK: conventionalTitle.MatchString(r.open.Title),
	}
	r.status.PullRequest = pull

	r.say(result.Say("Pull request %s: %s", result.PullRequest(pull.Number), result.URL(pull.URL)))

	if pull.TitleOK {
		r.say(result.Say("Title: \"%s\"", pull.Title))
	} else {
		r.say(result.Say("Title: \"%s\", not a conventional commit subject of the form type(scope)!: summary", pull.Title))
	}

	r.readBody(ctx, pull)
	r.readChecks(ctx)

	pull.Verdict, pull.MergeState, pull.Checks = r.waited.payloadFields()
}

func (r *reading) readBody(ctx context.Context, pull *result.OpenPull) {
	if r.cut {
		return
	}

	message, err := r.deps.Forge.Message(ctx, r.repo, pull.Number)
	if err != nil {
		r.failed(readMessage, err)

		return
	}

	pull.Body = message.Body
	if pull.Body == "" {
		r.say(result.Say("No body"))
	} else {
		r.say(result.Say("Body: %s", pull.Body))
	}
}

// readChecks reads the checks the trunk requires, then the open pull request's merge state and the
// verdict of its head's checks, both of which take those required checks.
func (r *reading) readChecks(ctx context.Context) {
	if r.cut {
		return
	}

	protection, err := r.deps.Forge.TrunkProtection(ctx, r.repo, r.status.Trunk)
	if err != nil {
		r.failed(readProtection, err)

		return
	}

	required, number := protection.RequiredChecks, r.open.Number

	head, headErr := r.deps.Forge.Head(ctx, r.repo, forge.HeadQuery{Number: number, Required: required})
	if headErr != nil {
		r.failed(readMergeState, headErr)
	} else {
		r.waited.MergeState = head.MergeState
	}

	if r.cut {
		return
	}

	verdict, err := r.deps.Forge.Verdict(ctx, r.repo, forge.VerdictQuery{SHA: r.open.HeadSHA, Number: number, Required: required})
	if err != nil {
		r.failed(readChecks, err)
	} else {
		r.takeVerdict(verdict, required)
	}

	if headErr == nil {
		r.say(r.waited.mergeStateLines()...)
	}
}

func checkNames(checks []forge.Check) string {
	var names []string
	for _, check := range checks {
		names = append(names, check.Name)
	}

	return strings.Join(names, ", ")
}

// takeVerdict records how verdict, read once and judged as the wait judges each read, stands, and
// says it.
func (r *reading) takeVerdict(verdict forge.Verdict, required []string) {
	outcome, done := judge(verdict, required)

	w := &r.waited
	w.take(verdict, outcome)
	// A red verdict still waiting for its failed log is red all the same.
	r.running = !done && outcome != WaitRed

	sha := result.SHA(r.open.HeadSHA)

	switch {
	case outcome == WaitRed:
		r.say(result.Say("Failed on %s: %s", sha, checkNames(verdict.FailedChecks())))
	case r.running && len(verdict.Checks) == 0:
		r.say(result.Say("No check has reported for %s yet; a workflow run is still going", sha))
	case r.running && len(verdict.Pending()) == 0:
		// Every check concluded, yet a workflow run still going can add one.
		r.say(w.checksLine(r.open.HeadSHA))
		r.say(result.Say("A workflow run on %s is still going, so more checks may report", sha))
	case r.running:
		pending := verdict.Pending()
		r.say(runningList(result.Say("Still running on %s (%s):", sha, nounCount(len(pending), "check")), pending, runningName)...)
	case verdict.State() == forge.VerdictNone:
		r.say(result.Say("No check or status reported for %s", sha))
	default:
		r.say(w.checksLine(r.open.HeadSHA))
	}
}

// next is the step a status ends on: on the trunk, start a branch; on a detached HEAD, keep its
// commits on a branch, or start one when it has none; with no origin, create it; on a work branch,
// the first of the steps below that holds, in the order push refuses and ends in, each built as push
// builds it.
func (r *reading) next() result.Next {
	switch r.status.Standing {
	case result.StandingTrunk:
		return startBranchStep()
	case result.StandingDetached:
		if len(r.status.Unlanded) > 0 {
			return keepDetachedStep()
		}

		return startBranchStep()
	case result.StandingNoOrigin:
		return createOriginStep()
	case result.StandingBranch:
	default:
		return result.NextNone()
	}

	if !r.blocked.None() {
		return r.blocked
	}

	if !r.dirty.None() {
		return r.dirty
	}

	s, push := r.status, result.Command(PushCommand)
	nothingPast := r.pastRead && len(r.on.past) == 0

	switch {
	case !r.shared.None() && s.Behind == 0 && !nothingPast:
		return r.shared
	case r.elsewhere != nil && s.Behind == 0 && !nothingPast:
		return elsewhereStep(r.elsewhere.Number, s.Trunk, push)
	case r.landed != nil:
		return startBranchStep()
	case len(r.unlanded) > 0:
		return unlandedStep(r.unlanded)
	case s.Ahead > 0 && s.Behind > 0:
		return divergedStep(s.Branch, push)
	case s.Behind > 0:
		return behindStep(s.Branch, push)
	case nothingPast:
		return firstPushStep()
	case !s.OnOrigin || s.Ahead > 0 || r.open == nil:
		return result.NextCommand(PushCommand)
	}

	return r.pullNext()
}

// pullNext is the step the open pull request decides: its checks still running, red, the pull
// request apart from the trunk, then the merge, once a review is in when it waits on one. Any
// other state is left to `ils push`, which reports it.
func (r *reading) pullNext() result.Next {
	w := r.waited
	push := result.Command(PushCommand)
	state := w.MergeState
	mergeable := w.Outcome == WaitGreen && (state == forge.MergeClean || state == forge.MergeBlockedByReview)

	switch {
	case r.running:
		return result.NextCommand(PushCommand)
	case w.Outcome == WaitRed || w.takeApart():
		report, _ := w.stopReport(push, r.open.HeadSHA, r.status.Trunk, r.open.Number)

		return report.next
	case mergeable && !r.trunkAhead.None():
		return r.trunkAhead
	case mergeable:
		return mergeStep(r.repo, r.open.Number, state)
	}

	return result.NextCommand(PushCommand)
}
