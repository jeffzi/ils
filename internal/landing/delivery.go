package landing

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// The reads push makes after its writes, as a result names them.
const (
	readProtection = "the trunk's required checks"
	readHead       = "the pull request's head"
	readMergeState = "whether the pull request can merge"
	readChecks     = "the checks of the pushed commit"
	readCI         = "whether the repository has CI"
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
// pushed commit. When nothing reported for the commit and the trunk requires no check, it also
// asks the forge whether the repository has CI on its trunk: without CI, the push ends as a green
// push does.
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

	// A trunk that requires checks has CI, so only one that requires none is asked about.
	if waited.Outcome == WaitNothingReported && len(protection.RequiredChecks) == 0 {
		if stop := d.withoutCI(ctx, deps); stop != nil {
			return *stop
		}
	}

	return *d.report()
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
			result.Broke, result.Say("the time limit ended during the push"), pushAgain())
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
			pushAgain())
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

func (d *delivery) found(change forge.Change) {
	d.number, d.url = change.Number, change.URL
	d.lines = append(d.lines, result.Say("Pull request #%s: %s", change.Number, change.URL))
}

// unread returns the result for a read that failed with err after the writes: pending when the
// deadline cut it short, since nothing more will change and the next run resumes on the same
// commit, and broke otherwise.
func (d *delivery) unread(read string, err error) *result.Result {
	if errors.Is(err, context.DeadlineExceeded) {
		line, reason := cutShortReading(read)

		return d.endedSaying(line, result.Pending, reason, pushAgain())
	}

	line, reason := couldNotRead(read, err)

	return d.endedSaying(line, result.Broke, reason, onceFixed())
}

// cutShortReading returns the line and the reason that say the time limit ended while reading
// read.
func cutShortReading(read string) (line, reason result.Phrase) {
	return result.Say("The time limit ended while reading %s; nothing more will change", read),
		result.Say("the time limit ended while reading %s", read)
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
	ended := d.req.ends(outcome, reason, next)
	ended.Lines = d.lines

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

// sayMergeState says the merge state as GitHub spells it, with the word ils has for it.
func (d *delivery) sayMergeState() {
	d.lines = append(d.lines, result.Say("GitHub: %s (%s)", d.waited.RawMergeState, d.waited.MergeState))
}

// sayChecks says how many deciding checks passed and how many were skipped.
func (d *delivery) sayChecks() {
	d.lines = append(d.lines,
		result.Say("Checks for %s: %s passed, %s skipped", d.tip, d.waited.Passed, d.waited.Skipped))
}

// report returns the result the wait's outcome maps to.
func (d *delivery) report() *result.Result {
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
		return d.pending()
	}
}

// green is every deciding check passed or skipped.
func (d *delivery) green() *result.Result {
	d.sayChecks()

	return d.doneMerging()
}

// doneMerging says the merge state and ends the push done, with merging by hand as the next step.
func (d *delivery) doneMerging() *result.Result {
	d.sayMergeState()

	return d.ended(result.Done, result.Phrase{}, d.mergeNext())
}

// mergeNext is the step after a push that is done: merge by hand, once a review is in when the
// merge state asks for one.
func (d *delivery) mergeNext() result.Next {
	repo := d.Repo.Slug()
	names := []any{repo, d.number, repo, d.number, d.tip, d.number}

	// A review is not the agent's to give, so it is asked for and the push is still done.
	if d.waited.MergeState == forge.MergeBlockedByReview {
		return result.NextStep("request a review of the pull request first, then "+mergeByHand, names...)
	}

	return result.NextStep(mergeByHand, names...)
}

// withoutCI asks the forge whether the repository has CI on its trunk, for a pushed commit that
// nothing reported for. It returns nil when the repository has CI; otherwise the push ends as a
// green push does, with a line saying nothing verified the commit: done, unless the merge state
// read after the checks says the pull request conflicts with the trunk or is behind it. A deadline
// that ends before or inside the read ends the push pending, and any other failure of the read
// ends it broke.
func (d *delivery) withoutCI(ctx context.Context, deps Deps) *result.Result {
	hasCI := false

	err := ctx.Err()
	if err == nil {
		hasCI, err = deps.Forge.HasTrunkCI(ctx, d.Repo, d.Trunk)
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		line, reason := cutShortReading(readCI)
		d.lines = append(d.lines, line)
		d.sayMergeState()

		return d.ended(result.Pending, reason, pushAgain())
	case err != nil:
		return d.unread(readCI, err)
	case hasCI:
		return nil
	}

	d.lines = append(d.lines, result.Say("No CI is configured on %s, so nothing verified %s", d.Trunk, d.tip))

	if d.waited.apartWithoutCI(d.waited.MergeState) {
		return d.report()
	}

	return d.doneMerging()
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
	d.sayMergeState()

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
	case forge.FailedLogInProgress:
		return "no log: the log of " + check + " was not available yet; its workflow run had not finished"
	default:
		return "no log: the log of " + check + " could not be read"
	}
}

// apart is a pull request that cannot merge until the trunk is merged into the branch. The
// checks are said only when some decided: they were green, and the merge state read after them
// found it apart.
func (d *delivery) apart(reason result.Phrase) *result.Result {
	if len(d.waited.Deciding) > 0 {
		d.sayChecks()
	}

	d.sayMergeState()

	return d.ended(result.DeliveredNotGreen, reason, result.NextStep(
		"merge the trunk into the branch with `git merge origin/%s`, resolve the conflicts, commit, "+
			"and run ils push again", d.Trunk))
}

// neverReported is a check the trunk requires that has no check on the pushed commit.
func (d *delivery) neverReported() *result.Result {
	d.lines = append(d.lines,
		result.Say("Required and never reported: %s", strings.Join(d.waited.Verdict.MissingRequired, ", ")))
	d.sayMergeState()

	return d.ended(result.DeliveredNotGreen, result.Say("a required check never reported"),
		result.NextStep("report that a required check never reported, and stop"))
}

// nothingReported is a pushed commit that no check or status reported for.
func (d *delivery) nothingReported() *result.Result {
	d.lines = append(d.lines,
		result.Say("No check or status reported for %s on pull request #%s", d.tip, d.number))
	d.sayMergeState()

	return d.ended(result.DeliveredNotGreen,
		result.Say("no check or status reported for %s on pull request #%s", d.tip, d.number),
		result.NextStep("report that no check or status reported for the pushed commit, and stop"))
}

// pending is a wait that ended before the forge had an answer, at the stage it was in.
func (d *delivery) pending() *result.Result {
	var reason result.Phrase

	switch d.waited.Stage {
	case StageHead:
		d.lines = append(d.lines, result.Say("GitHub does not show %s on pull request #%s yet", d.tip, d.number))
		reason = result.Say("GitHub did not show the pushed commit on the pull request before the time limit")
	case StageMergeState:
		d.lines = append(d.lines,
			result.Say("GitHub has not said whether pull request #%s can merge yet", d.number))
		reason = result.Say("GitHub did not say whether the pull request can merge before the time limit")

		// The stage's own window ended the wait: the time limit was not reached.
		if d.waited.Window > 0 {
			reason = result.Say("GitHub did not say whether the pull request can merge within %s seconds",
				int(d.waited.Window/time.Second))
		}
	default:
		reason = d.unconcluded()
	}

	return d.ended(result.Pending, reason, pushAgain())
}

// unconcluded says what the checks stage had found when the deadline ended it, and returns the
// reason: no verdict read at all, nothing reported, a workflow run with no check yet, or the
// checks still running.
func (d *delivery) unconcluded() result.Phrase {
	verdict := d.waited.Verdict
	reason := result.Say("the checks were still running at the time limit")

	switch {
	case d.waited.Since.IsZero():
		var line result.Phrase

		line, reason = cutShortReading(readChecks)
		d.lines = append(d.lines, line)
	case silent(verdict):
		d.lines = append(d.lines,
			result.Say("No check or status has reported for %s on pull request #%s yet", d.tip, d.number))
		reason = result.Say("no check or status had reported at the time limit")
	case len(verdict.Checks) == 0 && verdict.Running():
		d.lines = append(d.lines, result.Say(
			"No check has reported for %s on pull request #%s yet; a workflow run is still going", d.tip, d.number))
		reason = result.Say("the checks had not reported at the time limit")
	default:
		seconds := int(time.Since(d.waited.Since) / time.Second)
		for _, check := range verdict.Pending() {
			d.lines = append(d.lines, result.Say("Still running after %s seconds: %s", seconds, check.Name))
		}
	}

	d.sayMergeState()

	return reason
}
