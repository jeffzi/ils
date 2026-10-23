package landing

import (
	"context"
	"errors"
	"strings"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// readProtection is the read push makes of the trunk's required checks after its writes, as a
// result names it.
const readProtection = "the trunk's required checks"

// writeThenMerge is the judgment step after a green push: writeMessage, then the merge. Its last
// name is the merge command.
const writeThenMerge = writeMessage + ", then run %s"

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

	if stop := d.withoutCI(ctx, deps, protection.RequiredChecks); stop != nil {
		return *stop
	}

	return *d.report(deps)
}

// pushTip pushes the branch to origin unless origin's copy is already at the tip.
func (d *delivery) pushTip(ctx context.Context, deps Deps) *result.Result {
	if d.onOrigin {
		deps.say(&d.lines, result.Say("%s is already on origin at %s; nothing pushed", d.markedBranch(), d.markedTip()))

		return nil
	}

	deps.started(result.Say("Pushing %s", d.markedBranch()), 0)

	err := deps.Repo.Push(ctx, d.branch)
	if err == nil {
		deps.say(&d.lines, result.Say("Pushed %s at %s", d.markedBranch(), d.markedTip()))

		return nil
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return d.endedSaying(
			result.Say("Started the push of %s at %s when the time limit ended; it may or may not have taken",
				d.markedBranch(), d.markedTip()),
			result.Broke, result.Say("the time limit ended during the push"), d.req.again())
	}

	if refused, ok := errors.AsType[*git.PushError](err); ok {
		return d.endedSaying(
			result.Say("Git refused the push of %s, so nothing was pushed: %s", d.markedBranch(), strings.TrimSpace(refused.Stderr)),
			result.Broke, result.Say("git refused the push"), d.req.onceFixed())
	}

	return d.endedSaying(
		result.Say("Could not push %s, so nothing was pushed: %s", d.markedBranch(), err.Error()),
		result.Broke, result.Say("could not push the branch"), d.req.onceFixed())
}

// findOrOpen takes the branch's open pull request against the trunk, and opens one titled with
// the branch's name when it has none.
func (d *delivery) findOrOpen(ctx context.Context, deps Deps) *result.Result {
	if d.open != nil {
		d.found(deps, *d.open)

		return nil
	}

	deps.started(result.Say("Opening a pull request against %s", result.Branch(d.Trunk)), 0)

	opened, err := deps.Forge.OpenChange(ctx, d.Repo, forge.NewChange{Title: d.branch, Base: d.Trunk, Head: d.branch})

	switch {
	case err == nil:
		d.number, d.url = opened.Number, opened.URL
		deps.say(&d.lines, result.Say("Opened pull request %s against %s, titled %s with no body: %s",
			result.PullRequest(opened.Number), result.Branch(d.Trunk), d.branch, result.URL(opened.URL)))

		return nil
	case errors.Is(err, forge.ErrChangeExists):
		return d.findAgain(ctx, deps)
	case errors.Is(err, context.DeadlineExceeded):
		return d.endedSaying(
			result.Say("Started opening the pull request when the time limit ended; it may or may not have been opened"),
			result.Broke, result.Say("the time limit ended while opening the pull request"),
			d.req.again())
	}

	return d.endedSaying(
		result.Say("No pull request was opened: %s", err.Error()),
		result.Broke, result.Say("could not open the pull request"), d.req.onceFixed())
}

// findAgain reads the branch's pull requests once more, after the forge said that one exists,
// and takes the open one against the trunk.
func (d *delivery) findAgain(ctx context.Context, deps Deps) *result.Result {
	deps.startFinding()

	changes, err := deps.Forge.FindChanges(ctx, d.Repo, d.branch)
	if err != nil {
		return d.unread(readChanges, err)
	}

	for _, change := range changes {
		if change.State == forge.ChangeOpen && change.Base == d.Trunk {
			d.found(deps, change)

			return nil
		}
	}

	return d.endedSaying(
		result.Say("No pull request was opened or found: GitHub reports one for %s and lists none open against %s",
			d.markedBranch(), result.Branch(d.Trunk)),
		result.Broke, result.Say("no pull request was opened or found"), d.req.onceFixed())
}

// found takes change as the branch's pull request and says so.
func (d *delivery) found(deps Deps, change forge.Change) {
	d.number, d.url = change.Number, change.URL
	deps.say(&d.lines, result.Say("Pull request %s: %s", result.PullRequest(change.Number), result.URL(change.URL)))
}

func (d *delivery) markedBranch() result.Name {
	return result.Branch(d.branch)
}

func (d *delivery) markedTip() result.Name {
	return result.SHA(d.tip)
}

// unread returns the result for a read that failed with err after the writes: pending when the
// deadline cut it short, since nothing more will change and the next run resumes on the same
// commit, and broke otherwise.
func (d *delivery) unread(read string, err error) *result.Result {
	if errors.Is(err, context.DeadlineExceeded) {
		return d.ended(result.Pending, cutShortReading(read), d.req.again())
	}

	line, reason := couldNotRead(read, err)

	return d.endedSaying(line, result.Broke, reason, d.req.onceFixed())
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
	verdict, mergeState, checks := d.waited.payloadFields()

	return &result.Push{
		URL:        d.url,
		Number:     d.number,
		SHA:        d.tip,
		Verdict:    verdict,
		MergeState: mergeState,
		Checks:     checks,
		FailedLog:  d.waited.Verdict.FailedLog.Text,
	}
}

func (d *delivery) sayMergeState() {
	d.lines = append(d.lines, d.waited.mergeStateLine())
}

// report returns the result the wait's outcome maps to, telling the progress hook the checks line
// of green checks.
func (d *delivery) report(deps Deps) *result.Result {
	switch d.waited.Outcome {
	case WaitGreen:
		deps.say(&d.lines, d.waited.checksLine(d.tip))

		return d.doneMerging()
	case WaitNothingReported:
		return d.nothingReported()
	default:
	}

	report, _ := d.waited.stopReport(d.req.ilsCommand(), d.tip, d.Trunk, d.number)
	d.lines = append(d.lines, report.lines...)

	ended := d.ended(report.outcome(result.DeliveredNotGreen), report.reason, report.next)
	ended.Excerpt = report.excerpt

	return ended
}

// doneMerging says the merge state and ends the push done, with ils merge as the next step.
func (d *delivery) doneMerging() *result.Result {
	d.sayMergeState()

	return d.ended(result.Done, result.Phrase{}, d.mergeNext())
}

// mergeNext is the step after a push that is done: write the squash commit's message, then
// run ils merge, once a review is in when the merge state asks for one.
func (d *delivery) mergeNext() result.Next {
	repo, number, merge := d.Repo.Slug(), result.PullRequestNumber(d.number), result.Command(MergeCommand)

	// A review is not the agent's to give, so it is asked for and the push is still done.
	if d.waited.MergeState == forge.MergeBlockedByReview {
		return result.NextStep("request a review of the pull request first, then "+writeThenMerge, repo, number, merge)
	}

	return result.NextStep(writeThenMerge, repo, number, merge)
}

// withoutCI asks the forge whether the repository has CI on its trunk, for a pushed commit that
// nothing reported for while the trunk requires none of its checks. It returns nil when the
// question does not arise or the repository has CI; otherwise the push ends as a green push does,
// with a line saying nothing verified the commit: done, unless the merge state read after the
// checks says the pull request conflicts with the trunk or is behind it. A deadline that ends
// before or inside the read ends the push pending, and any other failure of the read ends it broke.
func (d *delivery) withoutCI(ctx context.Context, deps Deps, required []string) *result.Result {
	without, err := d.waited.trunkWithoutCI(ctx, deps, d.Ready, required)

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		report := d.waited.ciCutShortReport()
		d.lines = append(d.lines, report.lines...)

		return d.ended(result.Pending, report.reason, report.next)
	case err != nil:
		return d.unread(readCI, err)
	case !without:
		return nil
	}

	d.lines = append(d.lines, noCI(d.Trunk, d.tip))

	if d.waited.apartWithoutCI() {
		return d.report(deps)
	}

	return d.doneMerging()
}

// nothingReported is a pushed commit that no check or status reported for.
func (d *delivery) nothingReported() *result.Result {
	d.sayMergeState()

	return d.ended(result.DeliveredNotGreen, nothingReported(d.tip, d.number),
		result.NextStep("report that no check or status reported for the pushed commit, and stop"))
}
