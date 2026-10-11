package landing

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

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

// Push runs `ils push`: its preconditions, then its two writes, then the wait for the checks.
//
// After the shared preconditions it checks, in this order and stopping at the first that
// fails: HEAD is on a branch other than the trunk; no tracked file is changed; the branch has
// a commit past origin's trunk, which it fetches; the first such commit is on no other local
// branch; the tip is not behind origin's copy of the branch and has not diverged from it; no
// open pull request from the branch has a base other than the trunk; no merged pull request
// from the branch already carries its commits. A precondition that fails, an origin that
// cannot be reached, and a read the context's deadline cut short are refusals; a read that
// fails for any other reason is broke.
//
// When all hold it pushes the branch to origin, unless origin's copy is already at the tip,
// and opens the branch's pull request against the trunk, unless one is open. It then reads the
// checks the trunk requires and waits for the verdict of the pushed commit. Green is done; a
// red, missing, or absent verdict, a conflict, and a branch behind its trunk are delivered,
// not green; a wait the deadline ended is pending. From the push on nothing is a refusal: a
// write or a read that fails is broke, and the lines say what was written.
func Push(ctx context.Context, deps Deps, req Request) result.Result {
	ready, stop, ok := Preconditions(ctx, deps, req)
	if !ok {
		return stop
	}

	p, refusal := pushChecks(ctx, deps, req, ready)
	if refusal != nil {
		return *refusal
	}

	return deliver(ctx, deps, req, p)
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

// The reads push makes after its writes, as a result names them.
const (
	readProtection = "the trunk's required checks"
	readHead       = "the pull request's head"
	readMergeState = "whether the pull request can merge"
	readChecks     = "the checks of the pushed commit"
)

// excerptLines is how many lines of a failing check's log a red push prints, counted from the
// log's end: the excerpt is bounded so that the failure stays readable in a terminal, and the
// payload carries the whole log.
const excerptLines = 60

// mergeByHand is the judgment step after a green push. Its names are the repository and the
// pull request's number, twice, then the pushed sha and the number again; the title and the
// body stay placeholders for the agent to write.
const mergeByHand = "write the pull request's title and body as the squash commit's message with " +
	"`gh pr edit --repo %s %s --title … --body …`, then merge by hand with " +
	"`gh pr merge --repo %s %s --squash --delete-branch --match-head-commit %s " +
	`--subject "<title> (#%s)" --body "<body>"` + "`"

// delivery is a push past its preconditions: the lines it has said so far, the pull request
// it found or opened, and what its wait found.
type delivery struct {
	// url is the pull request's; it is empty until one is found or opened.
	url string
	req Request
	// lines say what happened so far, in order.
	lines []result.Phrase
	pushReady
	// waited is the zero value until the wait has ended.
	waited Waited
	// number is the pull request's; it is zero until one is found or opened.
	number int
}

// deliver pushes the branch, finds or opens its pull request, and reports the verdict of the
// pushed commit.
func deliver(ctx context.Context, deps Deps, req Request, p pushReady) result.Result {
	d := &delivery{req: req, pushReady: p}

	if stop := d.pushTip(ctx, deps); stop != nil {
		return *stop
	}

	if stop := d.findOrOpen(ctx, deps); stop != nil {
		return *stop
	}

	protection, err := deps.Forge.TrunkProtection(ctx, d.Repo, d.Trunk)
	if err != nil {
		return *d.unread(readProtection, err)
	}

	waited, err := AwaitVerdict(ctx, deps, Pushed{
		SHA: d.tip, Required: protection.RequiredChecks, Repo: d.Repo, Number: d.number,
	})
	if err != nil {
		return *d.unread(waitRead(err))
	}

	d.waited = waited

	return *d.report(deps.Clock.Now())
}

// waitRead returns the read a failed wait names and the error of that read.
func waitRead(err error) (string, error) {
	failed, ok := errors.AsType[*WaitError](err)
	if !ok {
		return readChecks, err
	}

	switch failed.Stage {
	case StageHead:
		return readHead, failed.Err
	case StageMergeState:
		return readMergeState, failed.Err
	default:
		return readChecks, failed.Err
	}
}

// pushTip pushes the branch to origin unless origin's copy is already at the tip.
func (d *delivery) pushTip(ctx context.Context, deps Deps) *result.Result {
	if d.onOrigin {
		d.lines = append(d.lines, result.Say("%s is already on origin at %s; nothing pushed", d.branch, d.tip))

		return nil
	}

	err := deps.Repo.Push(ctx, d.branch)
	if err == nil {
		d.lines = append(d.lines, result.Say("Pushed %s at %s", d.branch, d.tip))

		return nil
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return d.endedSaying(
			result.Say("Started the push of %s at %s when the time limit ended; it may or may not have taken",
				d.branch, d.tip),
			result.Broke, result.Say("the time limit ended during the push"), result.NextCommand("push"))
	}

	if refused, ok := errors.AsType[*git.PushError](err); ok {
		return d.endedSaying(
			result.Say("Git refused the push of %s, so nothing was pushed: %s", d.branch, strings.TrimSpace(refused.Stderr)),
			result.Broke, result.Say("git refused the push"), onceFixed())
	}

	return d.endedSaying(
		result.Say("Could not push %s, so nothing was pushed: %s", d.branch, err.Error()),
		result.Broke, result.Say("could not push the branch"), onceFixed())
}

// findOrOpen takes the branch's open pull request against the trunk, and opens one titled with
// the branch's name when it has none.
func (d *delivery) findOrOpen(ctx context.Context, deps Deps) *result.Result {
	if d.open != nil {
		d.found(*d.open)

		return nil
	}

	opened, err := deps.Forge.OpenChange(ctx, d.Repo, forge.NewChange{Title: d.branch, Base: d.Trunk, Head: d.branch})

	switch {
	case err == nil:
		d.number, d.url = opened.Number, opened.URL
		d.lines = append(d.lines, result.Say("Opened pull request #%s against %s, titled %s with no body: %s",
			opened.Number, d.Trunk, d.branch, opened.URL))

		return nil
	case errors.Is(err, forge.ErrChangeExists):
		return d.findAgain(ctx, deps)
	case errors.Is(err, context.DeadlineExceeded):
		return d.endedSaying(
			result.Say("Started opening the pull request when the time limit ended; it may or may not have been opened"),
			result.Broke, result.Say("the time limit ended while opening the pull request"),
			result.NextCommand("push"))
	}

	return d.endedSaying(
		result.Say("No pull request was opened: %s", err.Error()),
		result.Broke, result.Say("could not open the pull request"), onceFixed())
}

// findAgain reads the branch's pull requests once more, after the forge said that one exists,
// and takes the open one against the trunk.
func (d *delivery) findAgain(ctx context.Context, deps Deps) *result.Result {
	changes, err := deps.Forge.FindChanges(ctx, d.Repo, d.branch)
	if err != nil {
		return d.unread(readChanges, err)
	}

	for _, change := range changes {
		if change.State == forge.ChangeOpen && change.Base == d.Trunk {
			d.found(change)

			return nil
		}
	}

	return d.endedSaying(
		result.Say("No pull request was opened or found: GitHub reports one for %s and lists none open against %s",
			d.branch, d.Trunk),
		result.Broke, result.Say("no pull request was opened or found"), onceFixed())
}

// found takes change as the branch's pull request.
func (d *delivery) found(change forge.Change) {
	d.number, d.url = change.Number, change.URL
	d.lines = append(d.lines, result.Say("Pull request #%s: %s", change.Number, change.URL))
}

// onceFixed is the judgment step after a failure the user has to look at.
func onceFixed() result.Next {
	return result.NextStep("fix what failed, then run ils push again")
}

// unread returns the result for a read that failed with err after the writes: pending when the
// deadline cut it short, since nothing more will change and the next run resumes on the same
// commit, and broke otherwise.
func (d *delivery) unread(read string, err error) *result.Result {
	if errors.Is(err, context.DeadlineExceeded) {
		return d.endedSaying(
			result.Say("The time limit ended while reading %s; nothing more will change", read),
			result.Pending, result.Say("the time limit ended while reading %s", read), result.NextCommand("push"))
	}

	return d.endedSaying(
		result.Say("Could not read %s: %s", read, err.Error()),
		result.Broke, result.Say("could not read %s", read), onceFixed())
}

// endedSaying adds line to the lines said so far, then returns what ended returns.
func (d *delivery) endedSaying(
	line result.Phrase, outcome result.Outcome, reason result.Phrase, next result.Next,
) *result.Result {
	d.lines = append(d.lines, line)

	return d.ended(outcome, reason, next)
}

// ended returns the result of the push with the lines said so far and, once the pull request
// is known, the payload.
func (d *delivery) ended(outcome result.Outcome, reason result.Phrase, next result.Next) *result.Result {
	ended := &result.Result{
		Command: d.req.Command,
		Words:   d.req.Words,
		Outcome: outcome,
		Lines:   d.lines,
		Reason:  reason,
		Next:    next,
	}
	if d.number != 0 {
		ended.Payload = &result.Payload{Push: d.payload()}
	}

	return ended
}

// payload returns the structured detail of the push: the pull request, the sha, and what the
// wait found, which is no verdict and an unknown merge state before the wait has ended.
func (d *delivery) payload() *result.Push {
	state := d.waited.MergeState
	if state == "" {
		state = forge.MergeUnknown
	}

	checks := make([]result.Check, 0, len(d.waited.Deciding))
	for _, check := range d.waited.Deciding {
		checks = append(checks, result.Check{
			Name: check.Name, Bucket: string(check.Bucket), Workflow: check.Workflow, Link: check.Link,
		})
	}

	return &result.Push{
		URL:        d.url,
		Number:     d.number,
		SHA:        d.tip,
		Verdict:    string(d.waited.Verdict.State()),
		MergeState: string(state),
		Checks:     checks,
		FailedLog:  d.waited.Verdict.FailedLog.Text,
	}
}

// mergeState says the merge state as GitHub spells it, with the word ils has for it.
func (d *delivery) mergeState() {
	d.lines = append(d.lines, result.Say("GitHub: %s (%s)", d.waited.RawMergeState, d.waited.MergeState))
}

// report returns the result the wait's outcome maps to; now is when the wait ended.
func (d *delivery) report(now time.Time) *result.Result {
	switch d.waited.Outcome {
	case WaitGreen:
		return d.green()
	case WaitRed:
		return d.red()
	case WaitConflicting:
		return d.apart(result.Say("the pull request conflicts with the trunk"))
	case WaitBehind:
		return d.apart(result.Say("the pull request is behind the trunk"))
	case WaitNeverReported:
		return d.neverReported()
	case WaitNothingReported:
		return d.nothingReported()
	default:
		return d.pending(now)
	}
}

// green is every deciding check passed or skipped.
func (d *delivery) green() *result.Result {
	d.lines = append(d.lines,
		result.Say("Checks for %s: %s passed, %s skipped", d.tip, d.waited.Passed, d.waited.Skipped))
	d.mergeState()

	repo := d.Repo.Host + "/" + d.Repo.Owner + "/" + d.Repo.Name
	names := []any{repo, d.number, repo, d.number, d.tip, d.number}
	next := result.NextStep(mergeByHand, names...)

	// A review is not the agent's to give, so it is asked for and the push is still done.
	if d.waited.MergeState == forge.MergeBlockedByReview {
		next = result.NextStep("request a review of the pull request first, then "+mergeByHand, names...)
	}

	return d.ended(result.Done, result.Phrase{}, next)
}

// red is a deciding check that failed or was canceled, with the end of its log as the excerpt.
func (d *delivery) red() *result.Result {
	failed, _ := d.waited.Verdict.Failed()

	line := result.Say("Check %s failed: %s", failed.Name, failed.Link)
	reason := result.Say("the check %s failed", failed.Name)

	if failed.Canceled {
		line = result.Say("Check %s was canceled: %s", failed.Name, failed.Link)
		reason = result.Say("the check %s was canceled", failed.Name)
	}

	d.lines = append(d.lines, line)
	d.mergeState()

	ended := d.ended(result.DeliveredNotGreen, reason,
		result.NextStep("fix the failing check, commit, and run ils push again"))
	ended.Excerpt = excerpt(failed.Name, d.waited.Verdict.FailedLog)

	return ended
}

// excerpt returns the last excerptLines lines of the failed log of check, or one line saying
// why there is no log.
func excerpt(check string, log forge.FailedLog) string {
	switch log.State {
	case forge.FailedLogRead:
		lines := strings.Split(strings.TrimRight(log.Text, "\n"), "\n")

		return strings.Join(lines[max(0, len(lines)-excerptLines):], "\n")
	case forge.FailedLogNoRun:
		return "no log: " + check + " is not a workflow run"
	default:
		return "no log: the log of " + check + " could not be read"
	}
}

// apart is a pull request that cannot merge until the trunk is merged into the branch; no
// check was read.
func (d *delivery) apart(reason result.Phrase) *result.Result {
	d.mergeState()

	return d.ended(result.DeliveredNotGreen, reason, result.NextStep(
		"merge the trunk into the branch with `git merge origin/%s`, resolve the conflicts, commit, "+
			"and run ils push again", d.Trunk))
}

// neverReported is a check the trunk requires that has no check on the pushed commit.
func (d *delivery) neverReported() *result.Result {
	d.lines = append(d.lines,
		result.Say("Required and never reported: %s", strings.Join(d.waited.Verdict.MissingRequired, ", ")))
	d.mergeState()

	return d.ended(result.DeliveredNotGreen, result.Say("a required check never reported"),
		result.NextStep("report that a required check never reported, and stop"))
}

// nothingReported is a pushed commit that no check or status reported for.
func (d *delivery) nothingReported() *result.Result {
	d.lines = append(d.lines,
		result.Say("No check or status reported for %s on pull request #%s", d.tip, d.number))
	d.mergeState()

	return d.ended(result.DeliveredNotGreen,
		result.Say("no check or status reported for %s on pull request #%s", d.tip, d.number),
		result.NextStep("report that no check or status reported for the pushed commit, and stop"))
}

// pending is a wait the deadline ended, at the stage it was in; now is when it ended.
func (d *delivery) pending(now time.Time) *result.Result {
	var reason result.Phrase

	switch d.waited.Stage {
	case StageHead:
		d.lines = append(d.lines, result.Say("GitHub does not show %s on pull request #%s yet", d.tip, d.number))
		reason = result.Say("GitHub did not show the pushed commit on the pull request before the time limit")
	case StageMergeState:
		d.lines = append(d.lines,
			result.Say("GitHub has not said whether pull request #%s can merge yet", d.number))
		reason = result.Say("GitHub did not say whether the pull request can merge before the time limit")
	default:
		seconds := int(now.Sub(d.waited.Since) / time.Second)
		for _, check := range d.waited.Verdict.Pending() {
			d.lines = append(d.lines, result.Say("Still running after %s seconds: %s", seconds, check.Name))
		}

		d.mergeState()

		reason = result.Say("the checks were still running at the time limit")
	}

	return d.ended(result.Pending, reason, result.NextCommand("push"))
}
