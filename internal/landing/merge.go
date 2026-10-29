package landing

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// Each read of merge's own checks names what it reads as it follows "read" in a result, so it
// stays a noun phrase.
const (
	readLocalTrunk    = "the local trunk"
	readWorktrees     = "where the trunk is checked out"
	readRules         = "the trunk's rules"
	readMessage       = "the pull request's message"
	readStillOnOrigin = "whether origin still has the branch"
)

// mergeReserve is the time the wait leaves before the command's deadline, and the least that must
// be left at the first write: the merge, the delete on origin, and the local ending run in it, and
// a write the deadline cuts short leaves the run halfway.
const mergeReserve = 20 * time.Second

// conventionalTitle is a conventional commit subject, `<type>(<scope>)!: <summary>`: a type of
// lowercase ASCII letters, an optional scope that is not empty and holds no parenthesis or line
// break, an optional `!`, then `: ` and a summary that starts with a character other than a space.
// No list of types is enforced.
var conventionalTitle = regexp.MustCompile(`^[a-z]+(\([^()\r\n]+\))?!?: \S`)

// writeMessage is the judgment step that has the agent write the squash commit's message. Its names
// are the repository and the pull request's number; the title and the body stay placeholders for
// the agent to write. It stays untyped because result.NextStep accepts only constant expressions.
const writeMessage = "write the pull request's title and body as the squash commit's message with " +
	"`gh pr edit --repo %s %s --title … --body …`"

// mergeReady is what mergeChecks hands to the sequence that follows them, and what that
// sequence has found so far.
type mergeReady struct {
	onBranch
	// pull is the branch's open pull request against the trunk, or on a resume its merged one.
	pull       forge.Change
	protection forge.Protection
	// title is the pull request's title, and subject and body the squash commit's message; all
	// three are empty until the message is composed.
	title   string
	subject string
	body    string
	// ending is result.EndingTrunk or result.EndingDetached once the local ending ran, and empty
	// until then.
	ending string
	// lines say what the writes did so far, for the result that ends the run.
	lines []result.Phrase
	// waited is the zero value until the wait has ended.
	waited Waited
	// unverified is whether nothing reported for the head in a repository with no CI on its
	// trunk, which lets the merge go on as green checks do.
	unverified bool
	// elsewhere is whether a worktree other than this one has the trunk checked out.
	elsewhere bool
	// resumed is whether pull is a merged pull request that carries every commit of the branch,
	// which the run finishes instead of merging.
	resumed bool
	// merged is whether this run made the merge.
	merged bool
}

// Merge runs `ils merge`: its preconditions, then the merge of the branch's pull request.
//
// After the shared preconditions it checks, in this order and stopping at the first that fails:
// HEAD is on a branch other than the trunk; no tracked file is changed; the local trunk, if any,
// has no commit that origin's trunk, which it fetches, lacks; it reads whether another worktree
// has the trunk checked out; the branch has a commit past origin's trunk; no open pull request
// from the branch has a base other than the trunk; the branch has an open pull request against
// the trunk; the tip is that pull request's head; the pull request is not a draft; its title is a
// conventional commit subject; the repository's settings and the trunk's rules allow a squash
// merge. A check that fails, an origin that cannot be reached, and a read the context's deadline
// cut short are refusals; a read that fails for any other reason is broke. Once the pull request
// is known, every result carries the merge payload.
//
// Then it waits for the head's checks as push does, on a deadline mergeReserve before ctx's, and
// reads what lets the merge go on, as toWrite says. Then it writes, as land says.
//
// A branch with no open pull request against the trunk, whose merged pull request carries every
// commit of the branch, is finished without a merge, as resume says.
func Merge(ctx context.Context, deps Deps, req Request) result.Result {
	deps.startChecking()

	ready, stop := Preconditions(ctx, deps, req)
	if stop != nil {
		return *stop
	}

	m, refusal := mergeChecks(ctx, deps, req, ready)
	if refusal != nil {
		return *refusal
	}

	if m.resumed {
		return *m.resume(ctx, deps, req)
	}

	if stop := m.toWrite(ctx, deps, req); stop != nil {
		return *stop
	}

	return *m.land(ctx, deps, req)
}

// mergeChecks runs merge's checks after the shared preconditions, and returns what the sequence
// needs or the result of the first that refuses.
func mergeChecks(ctx context.Context, deps Deps, req Request, ready Ready) (mergeReady, *result.Result) {
	on, stop := branchChecks(ctx, deps, req, ready)
	if stop != nil {
		return mergeReady{}, stop
	}

	m := mergeReady{onBranch: on}

	if stop = localTrunk(ctx, deps, req, ready.Trunk); stop != nil {
		return mergeReady{}, stop
	}

	elsewhere, err := deps.Repo.CheckedOutElsewhere(ctx, ready.Trunk)
	if err != nil {
		return mergeReady{}, req.unread(readWorktrees, err)
	}

	m.elsewhere = elsewhere

	m.tip, m.past, stop = commitsPast(ctx, deps, req, m.onBranch)
	if stop != nil {
		return mergeReady{}, stop
	}

	if stop = m.findPull(ctx, deps, req); stop != nil || m.resumed {
		return m, stop
	}

	if stop = m.pullChecks(ctx, deps, req); stop != nil {
		return mergeReady{}, m.carrying(stop)
	}

	return m, nil
}

// localTrunk refuses a local trunk with commits origin's trunk lacks, since the pull that ends a
// merge could not fast-forward it. A local trunk that does not exist is not refused.
func localTrunk(ctx context.Context, deps Deps, req Request, trunk string) *result.Result {
	ahead, past, err := deps.Repo.LocalTrunkAhead(ctx, trunk)
	if err != nil {
		return req.unread(readLocalTrunk, err)
	}

	if !ahead {
		return nil
	}

	return req.trunkAhead(trunk, past)
}

// findPull finds the branch's open pull request against the trunk or, without one, the merged
// pull request that carries every commit of the branch, which the run resumes. It refuses a
// branch that has neither, and a merged pull request that lacks commits of the branch, carrying
// that pull request in the payload. An open pull request beside a merged one that carries every
// commit is refused as `ils push` refuses it, with no payload: the branch already landed, so
// neither is a pull request the run takes.
func (m *mergeReady) findPull(ctx context.Context, deps Deps, req Request) *result.Result {
	deps.startFinding()

	open, landed, stop := pullRequest(ctx, deps, req, m.onBranch)

	switch {
	case stop == nil && open != nil && landed != nil:
		return m.alreadyLanded(req, *landed)
	case stop == nil && open != nil:
		m.pull = *open

		return nil
	case landed != nil:
		m.pull, m.resumed = *landed, true

		if stop != nil {
			return m.carrying(stop)
		}

		return nil
	case stop != nil:
		return stop
	}

	return req.Refused(
		result.Say("the branch %s has no open pull request against the trunk %s", result.Branch(m.branch), result.Branch(m.Trunk)),
		result.NextCommand(PushCommand))
}

// pullChecks runs the checks of the open pull request and of the squash merges the repository
// allows, and reads the trunk's rules.
func (m *mergeReady) pullChecks(ctx context.Context, deps Deps, req Request) *result.Result {
	if stop := m.atHead(ctx, deps, req); stop != nil {
		return stop
	}

	if m.pull.Draft {
		return m.draftStop(req)
	}

	if !conventionalTitle.MatchString(m.pull.Title) {
		return m.titleStop(req, m.pull.Title)
	}

	if !m.Repo.SquashAllowed {
		return req.Refused(
			result.Say("the settings of %s forbid squash merges: Allow squash merging is off", m.Repo.FullName()),
			result.NextStep("allow squash merges in the repository's settings"+thenRunAgain, req.ilsCommand()))
	}

	protection, err := deps.Forge.TrunkProtection(ctx, m.Repo, m.Trunk)
	if err != nil {
		return req.unread(readRules, err)
	}

	if !protection.SquashAllowed {
		return req.Refused(rulesetReason(protection.SquashForbiddenBy, m.Trunk),
			result.NextStep("allow squash merges in the trunk's ruleset"+thenRunAgain, req.ilsCommand()))
	}

	m.protection = protection

	return nil
}

// rulesetReason says that the rulesets with the ids ids forbid squash merges on trunk.
func rulesetReason(ids []int, trunk string) result.Phrase {
	if len(ids) == 1 {
		return result.Say("the ruleset %s on the trunk %s forbids squash merges", ids[0], result.Branch(trunk))
	}

	named := make([]string, len(ids))
	for i, id := range ids {
		named[i] = strconv.Itoa(id)
	}

	return result.Say("the rulesets %s on the trunk %s forbid squash merges", strings.Join(named, ", "), result.Branch(trunk))
}

func (m *mergeReady) draftStop(req Request) *result.Result {
	return req.Refused(
		result.Say("pull request %s is a draft", result.PullRequest(m.pull.Number)),
		result.NextStep("mark it ready with `gh pr ready --repo %s %s`"+thenRunAgain, m.Repo.Slug(),
			result.PullRequestNumber(m.pull.Number),
			req.ilsCommand()))
}

// titleStop refuses a pull request titled title, which is not a conventional commit subject.
func (m *mergeReady) titleStop(req Request, title string) *result.Result {
	number := m.pull.Number

	return req.Refused(
		result.Say("the title of pull request %s, \"%s\", is not a conventional commit subject "+
			"of the form type(scope)!: summary", result.PullRequest(number), title),
		result.NextStep(writeMessage+thenRunAgain, m.Repo.Slug(), result.PullRequestNumber(number), req.ilsCommand()))
}

// atHead refuses a tip that is not the head of the pull request, saying whether it is behind
// that head, past it, or diverged from it. A tip that is origin's head of the pull request while
// the forge still lists an earlier one becomes the head the run waits on, carries and merges.
func (m *mergeReady) atHead(ctx context.Context, deps Deps, req Request) *result.Result {
	if m.tip == m.pull.HeadSHA {
		return nil
	}

	head, stop := req.fetchPullHead(ctx, deps, m.pull.Number, readHead)
	if stop != nil {
		return stop
	}

	apart, err := deps.Repo.Divergence(ctx, m.tip, head)
	if err != nil {
		return req.unread(readHead, err)
	}

	if stop = m.apartFromHead(req, apart); stop != nil {
		return stop
	}

	m.pull.HeadSHA = head

	return nil
}

// apartFromHead refuses a tip apart from the pull request's head, with ils push as the next step.
func (m *mergeReady) apartFromHead(req Request, apart git.Divergence) *result.Result {
	push := result.NextCommand(PushCommand)
	branch, pull := result.Branch(m.branch), result.PullRequest(m.pull.Number)

	switch {
	case apart.Ahead > 0 && apart.Behind > 0:
		return req.Refused(result.Say("the tip of %s and the head of pull request %s have diverged: "+
			"each has commits the other lacks", branch, pull), push)

	case apart.Behind > 0:
		return req.Refused(result.Say("the tip of %s is behind the head of pull request %s: "+
			"the pull request has commits the tip lacks", branch, pull), push)

	case apart.Ahead > 0:
		return req.Refused(result.Say("the tip of %s is past the head of pull request %s: "+
			"the tip has commits the pull request lacks, so they were never pushed", branch, pull), push)

	}

	return nil
}

// toWrite runs what comes after merge's checks and before its first write: the wait for the
// checks and the merge state read after them, on a deadline mergeReserve before ctx's; then the
// pull request's message; then whether mergeReserve is still left. It returns the result that
// stops the merge, or nil when the merge can be made.
func (m *mergeReady) toWrite(ctx context.Context, deps Deps, req Request) *result.Result {
	waitCtx, cancel := beforeDeadline(ctx, mergeReserve)
	defer cancel()

	if stop := m.wait(waitCtx, deps, req); stop != nil {
		return stop
	}

	if stop := m.settle(waitCtx, deps, req); stop != nil {
		return stop
	}

	if stop := m.compose(ctx, deps, req); stop != nil {
		return stop
	}

	return m.reserved(ctx, req)
}

// pushed is the pull request's head as the wait for its checks reads it.
func (m *mergeReady) pushed() Pushed {
	return Pushed{SHA: m.pull.HeadSHA, Required: m.protection.RequiredChecks, Repo: m.Repo, Number: m.pull.Number}
}

// wait waits for the checks of the pull request's head as push does, and returns the result their
// verdict stops the merge with, or nil when they are green or nothing reported in a repository with
// no CI on its trunk.
func (m *mergeReady) wait(ctx context.Context, deps Deps, req Request) *result.Result {
	waited, err := AwaitVerdict(ctx, deps, m.pushed())
	if err != nil {
		return m.carrying(req.unread(waitRead(err)))
	}

	m.waited = waited

	return m.withoutCI(ctx, deps, req)
}

// withoutCI asks whether the repository has CI on its trunk, for a head that nothing reported for
// while the trunk requires none of its checks, and returns the stop of the wait's outcome. Without
// CI the merge goes on as green checks let it, unless the merge state says the pull request
// conflicts with the trunk or is behind it.
func (m *mergeReady) withoutCI(ctx context.Context, deps Deps, req Request) *result.Result {
	without, err := m.waited.trunkWithoutCI(ctx, deps, m.Ready, m.protection.RequiredChecks)

	switch {
	case endedEarly(err):
		return m.reported(req, m.waited.ciCutShortReport(interrupted(err)), nil)
	case err != nil:
		return m.carrying(req.unread(readCI, err))
	}

	if without {
		m.unverified = true
		m.waited.apartWithoutCI()
	}

	return m.verdictStop(req)
}

// verdictStop returns the result the wait's outcome stops the merge with, or nil when it lets the
// merge go on.
func (m *mergeReady) verdictStop(req Request) *result.Result {
	if m.waited.Outcome == WaitNothingReported && !m.unverified {
		stop := req.Refused(nothingReported(m.pull.HeadSHA, m.pull.Number), result.NextStep("report that no check or status reported for the head, and stop"))
		stop.Lines = m.waited.mergeStateLines()

		return m.carrying(stop)
	}

	report, stops := m.waited.stopReport(req.ilsCommand(), m.pull.HeadSHA, m.Trunk, m.pull.Number)
	if !stops {
		return nil
	}

	return m.reported(req, report, m.unverifiedLine())
}

// settle reads the merge state again as the wait polls it while GitHub has not worked it out, and
// then refuses every state but clean. A head read there on another commit than the one the checks
// were read for is refused as the wait refuses a moved head. The deadline or mergeWindow ending that
// poll is pending, as it is for the wait. Once it polled for a clean state, it tells the progress
// hook the wait for the checks goes on, so that the checks line is said under that step.
func (m *mergeReady) settle(ctx context.Context, deps Deps, req Request) *result.Result {
	w := waiter{deps: deps, pushed: m.pushed()}

	read := forge.Head{SHA: m.pull.HeadSHA, MergeState: m.waited.MergeState, RawMergeState: m.waited.RawMergeState}

	moved, polled, err := w.settled(ctx, &m.waited, read)
	if moved {
		report, _ := m.waited.stopReport(req.ilsCommand(), m.pull.HeadSHA, m.Trunk, m.pull.Number)

		return m.reported(req, report, nil)
	}

	switch {
	case err == nil:
		stop := m.stateStop(req)
		if stop == nil && polled {
			w.startChecks(ctx)
		}

		return stop
	case m.waited.endPending(StageMergeState, err):
		return m.reported(req, m.waited.pendingReport(m.pull.HeadSHA, m.pull.Number), []result.Phrase{m.summary()})
	default:
		return m.carrying(req.unread(readMergeState, err))
	}
}

// stateStop refuses a merge state other than clean, once the checks let the merge go on: green, or
// nothing reported on a trunk with no CI.
func (m *mergeReady) stateStop(req Request) *result.Result {
	pull := result.PullRequest(m.pull.Number)

	switch state := m.waited.MergeState; state {
	case forge.MergeClean:
		return nil
	case forge.MergeConflicting, forge.MergeBehind:
		m.waited.takeApart()

		return m.verdictStop(req)
	case forge.MergeDraft:
		return m.stated(m.draftStop(req))
	case forge.MergeBlockedByReview:
		return m.stated(req.Refused(result.Say("pull request %s requires a review", pull),
			result.NextStep("request a review, and stop: a review is not the agent's to give")))
	case forge.MergeBlockedByMissingCheck:
		return m.stated(req.Refused(result.Say("a check the trunk requires has no check on %s", result.SHA(m.pull.HeadSHA)),
			result.NextStep("report that the required check has no check on the head, and stop")),
			m.missingLine())
	default:
		return m.stated(req.Refused(
			result.Say("GitHub blocks pull request %s for a reason ils does not read: %s", pull, result.URL(m.pull.URL)),
			result.NextStep("report that GitHub blocks the merge, and stop")))
	}
}

// missingLine names the trunk's required checks that no check on the head bears the name of, or
// every required check when each has one, since GitHub then expects one ils cannot tell apart.
func (m *mergeReady) missingLine() result.Phrase {
	missing := slices.DeleteFunc(slices.Clone(m.protection.RequiredChecks), func(name string) bool {
		return slices.ContainsFunc(m.waited.Verdict.Checks, func(check forge.Check) bool { return check.Name == name })
	})
	if len(missing) == 0 {
		return result.Say("Required by the trunk: %s", strings.Join(m.protection.RequiredChecks, ", "))
	}

	return result.Say("Required by the trunk with no check on %s: %s", result.SHA(m.pull.HeadSHA), strings.Join(missing, ", "))
}

// compose reads the pull request's message and makes the squash commit's out of it: the title
// with the pull request's number as the subject, and the body followed by the trailer block.
func (m *mergeReady) compose(ctx context.Context, deps Deps, req Request) *result.Result {
	message, err := deps.Forge.Message(ctx, m.Repo, m.pull.Number)
	if err != nil {
		return m.carrying(req.unread(readMessage, err))
	}

	if !conventionalTitle.MatchString(message.Title) {
		return m.stated(m.titleStop(req, message.Title))
	}

	m.title = message.Title
	m.subject = message.Title + " (#" + strconv.Itoa(m.pull.Number) + ")"
	m.body = squashBody(message)

	return nil
}

// squashBody is the body kept as written, then a blank line and the trailer block: the block
// alone when the body is empty, and the body alone when there are no trailers.
func squashBody(message forge.Message) string {
	trailers := strings.Join(message.Trailers, "\n")

	switch {
	case trailers == "":
		return message.Body
	case message.Body == "":
		return trailers
	default:
		return message.Body + "\n\n" + trailers
	}
}

// reserved ends the merge pending, with nothing written, when less than mergeReserve is left
// before ctx's deadline.
func (m *mergeReady) reserved(ctx context.Context, req Request) *result.Result {
	if !deadlineWithin(ctx, mergeReserve) {
		return nil
	}

	stop := req.ends(result.Pending,
		result.Say("fewer than %s of the %s time limit were left for the merge", mergeReserve, TimeLimit),
		result.NextCommand(PushCommand))
	stop.Lines = slices.Concat([]result.Phrase{m.summary()}, m.waited.mergeStateLines(), []result.Phrase{
		result.Say("Fewer than %s of the %s time limit were left for the merge, so nothing was written",
			mergeReserve, TimeLimit),
	})

	return m.carrying(stop)
}

// summary says what let the merge go on: the head's checks, or no CI on the trunk.
func (m *mergeReady) summary() result.Phrase {
	if m.unverified {
		return noCI(m.Trunk, m.pull.HeadSHA)
	}

	return m.waited.checksLine(m.pull.HeadSHA)
}

func (m *mergeReady) unverifiedLine() []result.Phrase {
	if !m.unverified {
		return nil
	}

	return []result.Phrase{noCI(m.Trunk, m.pull.HeadSHA)}
}

// reported returns the result report makes, pending or refused, with lead printed before its lines.
func (m *mergeReady) reported(req Request, report waitReport, lead []result.Phrase) *result.Result {
	ended := req.ends(report.outcome(result.Refused), report.reason, report.next)
	ended.Lines = slices.Concat(lead, report.lines)
	ended.Excerpt = report.excerpt

	return m.carrying(ended)
}

// stated returns stop with the lines that say what let the merge go on, then lines, then the merge
// state unless it is clean.
func (m *mergeReady) stated(stop *result.Result, lines ...result.Phrase) *result.Result {
	stop.Lines = slices.Concat([]result.Phrase{m.summary()}, lines, m.waited.mergeStateLines())

	return m.carrying(stop)
}

// carrying returns ended with the merge payload of what the run has found and done so far.
func (m *mergeReady) carrying(ended *result.Result) *result.Result {
	verdict, mergeState, checks := m.waited.payloadFields()

	ended.Payload = &result.Payload{Merge: &result.Merge{
		URL:        m.pull.URL,
		Number:     m.pull.Number,
		SHA:        m.pull.HeadSHA,
		Verdict:    verdict,
		MergeState: mergeState,
		Checks:     checks,
		Subject:    m.subject,
		Body:       m.body,
		Merged:     m.merged,
		Ending:     m.ending,
	}}

	return ended
}

// land makes the merge, deletes the branch on origin, and runs the local ending, each only once
// the one before it went through. A write that fails is broke, saying what went through. Every
// result from here on holds the checks line, so it is the line the wait's step says.
func (m *mergeReady) land(ctx context.Context, deps Deps, req Request) *result.Result {
	summary := m.summary()
	m.lines = append([]result.Phrase{summary}, m.waited.mergeStateLines()...)

	if !m.unverified {
		deps.said(summary)
	}

	if stop := m.merge(ctx, deps, req); stop != nil {
		return stop
	}

	return m.finish(ctx, deps, req)
}

// finish deletes the branch on origin and runs the local ending, the second only once the first
// went through.
func (m *mergeReady) finish(ctx context.Context, deps Deps, req Request) *result.Result {
	if stop := m.deleteOnOrigin(ctx, deps, req); stop != nil {
		return stop
	}

	return m.end(ctx, deps, req)
}

// merge squash-merges the pull request with the composed message, at the head whose checks were
// read. An interrupt that ended ctx before the merge is sent ends the run pending with nothing
// written, as too little time left does in reserved. A merge that fails or that the time limit or
// an interrupt cut short may still have gone through, so its next step is ils merge, whose run
// resumes once it did.
func (m *mergeReady) merge(ctx context.Context, deps Deps, req Request) *result.Result {
	pull := result.PullRequest(m.pull.Number)

	deps.started(result.Say("Merging %s", pull), untilTheEnd)

	if interrupted(ctx.Err()) {
		return m.stopped(
			req.ends(result.Pending, result.Say(runInterrupted+" before the merge"), result.NextCommand(PushCommand)),
			result.Say(runInterrupted+" before the merge, so nothing was written"))
	}

	err := deps.Forge.Land(ctx, m.Repo, forge.Landing{
		Number: m.pull.Number, HeadSHA: m.pull.HeadSHA, Subject: m.subject, Body: m.body,
	})

	switch {
	case interrupted(err):
		return m.broke(req, result.Say(runInterrupted+" during the merge"), req.again(),
			result.Say("Started the merge of pull request %s when "+runInterrupted+"; "+
				"it may or may not have gone through", pull))
	case timedOut(err):
		return m.broke(req, result.Say(timeLimitEnded+" during the merge", TimeLimit), req.again(),
			result.Say("Started the merge of pull request %s when "+timeLimitEnded+"; "+
				"it may or may not have gone through", pull, TimeLimit))
	case err != nil:
		return m.broke(req, result.Say("GitHub did not report the merge as done"), req.again(),
			result.Say("GitHub did not report the merge of pull request %s as done: %s", pull, err.Error()))
	}

	m.merged = true
	deps.say(&m.lines, result.Say("Merged pull request %s into %s: \"%s\"", pull, result.Branch(m.Trunk), m.title))

	return nil
}

// deleteOnOrigin deletes the branch on origin. A delete that fails while origin no longer lists
// the branch counts as done, since GitHub can delete a merged branch on its own. A delete an
// interrupt cut short is broke without asking origin, since it may or may not have gone through.
// When origin cannot be asked whether it still lists the branch, the result says why instead of
// where the branch stands, naming the interrupt when one ended the question. On a resume, which
// merged nothing, the delete is the first write: an interrupt before it ends the run pending, with
// nothing written.
func (m *mergeReady) deleteOnOrigin(ctx context.Context, deps Deps, req Request) *result.Result {
	deps.started(result.Say("Deleting %s on origin", result.Branch(m.branch)), untilTheEnd)

	if !m.merged {
		if stop := m.unfinished(ctx, req); stop != nil {
			return stop
		}
	}

	err := deps.Forge.DeleteBranch(ctx, m.Repo, m.branch)
	if err == nil {
		deps.say(&m.lines, result.Say("Deleted the branch %s on origin", result.Branch(m.branch)))

		return nil
	}

	if interrupted(err) {
		return m.broke(req, result.Say(runInterrupted+" while deleting the branch on origin"), req.again(),
			result.Say("Started the delete of the branch %s on origin when "+runInterrupted+"; "+
				"it may or may not have gone through", result.Branch(m.branch)))
	}

	_, listed, askErr := deps.Repo.RemoteBranch(ctx, m.branch)

	switch {
	case askErr != nil:
		unasked, _ := couldNotRead(readStillOnOrigin, askErr)
		if interrupted(askErr) {
			unasked = result.Say(runInterrupted+" while reading %s", readStillOnOrigin)
		}

		return m.broke(req, result.Say("the branch could not be deleted on origin"), req.again(),
			result.Say("Could not delete the branch %s on origin: %s", result.Branch(m.branch), err.Error()), unasked)
	case listed:
		return m.broke(req, result.Say("the branch is still on origin"), req.again(),
			result.Say("Could not delete the branch %s on origin, so it is still there: %s", result.Branch(m.branch), err.Error()))
	}

	deps.say(&m.lines, m.goneLine())

	return nil
}

// goneLine says the branch was gone from origin before ils deleted it.
func (m *mergeReady) goneLine() result.Phrase {
	return result.Say("The branch %s was already gone from origin", result.Branch(m.branch))
}

// unfinished returns the result that ends a resume pending, with nothing written, when an
// interrupt ended ctx, and nil otherwise. A deadline that alone ended ctx does not count: the
// deadline is checked ahead, against mergeReserve.
func (m *mergeReady) unfinished(ctx context.Context, req Request) *result.Result {
	if !interrupted(ctx.Err()) {
		return nil
	}

	return m.stopped(
		req.ends(result.Pending, result.Say(runInterrupted+" before finishing the merge"), req.again()),
		result.Say(runInterrupted+" before finishing the merge, so nothing was written"))
}

// broke returns the result of a write that failed: broke with reason and next, and the lines said
// so far followed by lines.
func (m *mergeReady) broke(req Request, reason result.Phrase, next result.Next, lines ...result.Phrase) *result.Result {
	return m.stopped(req.ends(result.Broke, reason, next), lines...)
}

// stopped returns ended, the result that ends the run, with the lines said so far followed by
// lines.
func (m *mergeReady) stopped(ended *result.Result, lines ...result.Phrase) *result.Result {
	ended.Lines = slices.Concat(m.lines, lines)

	return m.carrying(ended)
}

// endStep is one git call of the local ending: the command a result names it by, the call, and
// the next step when it fails.
type endStep struct {
	run     func(ctx context.Context) error
	command result.Name
	next    result.Next
}

// localEnding is a local ending: where it leaves the checkout, its steps in order, and the lines
// it says once every step ran.
type localEnding struct {
	ending string
	steps  []endStep
	done   []result.Phrase
}

// end runs the local ending, on the trunk unless another worktree has it checked out, and ends the
// run done, with the lines said so far and where the checkout ended.
func (m *mergeReady) end(ctx context.Context, deps Deps, req Request) *result.Result {
	repo, trunk, branch := deps.Repo, m.Trunk, m.branch
	deps.started(result.Say("Moving to origin's tip of %s", result.Branch(trunk)), untilTheEnd)
	drop := endStep{
		run:     func(ctx context.Context) error { return repo.DeleteBranch(ctx, branch) },
		command: result.Concat("git branch -D ", result.Branch(branch)),
	}

	if m.elsewhere {
		drop.next = result.NextStep("run `git branch -D %s` by hand", result.Branch(branch))

		return m.endSteps(ctx, deps, req, localEnding{ending: result.EndingDetached, steps: []endStep{
			{
				run: func(ctx context.Context) error {
					_, err := repo.Fetch(ctx, trunk)

					return err
				},
				command: result.Concat("git fetch origin ", result.Branch(trunk)), next: req.onceFixed(),
			},
			{
				run:     func(ctx context.Context) error { return repo.DetachAtOrigin(ctx, trunk) },
				command: result.Concat("git switch --detach refs/remotes/origin/", result.Branch(trunk)), next: req.onceFixed(),
			},
			drop,
		}, done: []result.Phrase{
			result.Say("Detached at origin's tip of %s; deleted local %s", result.Branch(trunk), result.Branch(branch)),
			result.Say("The trunk %s is checked out in another worktree and updates there on its next "+
				"`git pull --ff-only`", result.Branch(trunk)),
		}})
	}

	drop.next = result.NextStep("run `git branch -D %s` and `git pull --ff-only origin %s` by hand",
		result.Branch(branch), result.Branch(trunk))

	return m.endSteps(ctx, deps, req, localEnding{ending: result.EndingTrunk, steps: []endStep{
		{
			run:     func(ctx context.Context) error { return repo.Switch(ctx, trunk) },
			command: result.Concat("git switch ", result.Branch(trunk)), next: req.onceFixed(),
		},
		drop,
		{
			run:     func(ctx context.Context) error { return repo.FastForwardTrunk(ctx, trunk) },
			command: result.Concat("git pull --ff-only origin ", result.Branch(trunk)),
			next:    result.NextStep("run `git pull --ff-only origin %s` by hand", result.Branch(trunk)),
		},
	}, done: []result.Phrase{
		result.Say("Switched to %s at origin's tip; deleted local %s", result.Branch(trunk), result.Branch(branch)),
	}})
}

// endSteps runs the steps of local in order and ends the run done where local leaves the checkout,
// saying what local says once done, the first of which is the line the ending's step says. A step
// that fails is broke, with lines that say which steps ran, which failed with its message, and
// which did not run. A step an interrupt stopped is broke the same way, saying the interrupt
// stopped it in place of its message.
func (m *mergeReady) endSteps(ctx context.Context, deps Deps, req Request, local localEnding) *result.Result {
	var ran []result.Phrase

	for i, step := range local.steps {
		if err := step.run(ctx); err != nil {
			failed := result.Say("`%s` failed: %s", step.command, err.Error())
			reason := result.Say("`%s` failed", step.command)

			if interrupted(err) {
				failed = result.Say("`%s` stopped when "+runInterrupted, step.command)
				reason = result.Say(runInterrupted+" during `%s`", step.command)
			}

			lines := slices.Concat(ran, []result.Phrase{failed})
			for _, rest := range local.steps[i+1:] {
				lines = append(lines, result.Say("Not run: `%s`", rest.command))
			}

			return m.broke(req, reason, step.next, lines...)
		}

		ran = append(ran, result.Say("Ran `%s`", step.command))
	}

	m.ending = local.ending
	deps.said(local.done[0])
	finished := req.ends(result.Done, result.Phrase{}, result.NextNone())
	finished.Lines = slices.Concat(m.lines, local.done)

	return m.carrying(finished)
}

// resume finishes a branch whose merged pull request carries every commit of it, without a merge
// and without reading any check: the delete on origin while origin still lists the branch, then
// the local ending. Fewer than mergeReserve left before the first write ends it pending, and so
// does an interrupt before it, whether that write is the delete on origin or the local ending.
// Asking origin whether it still lists the branch is a read before any write: a failure there is
// broke, an origin that does not answer included, and the time limit or an interrupt that cut it
// short is refused.
func (m *mergeReady) resume(ctx context.Context, deps Deps, req Request) *result.Result {
	m.lines = []result.Phrase{
		result.Say("Pull request %s was already merged into %s", result.PullRequest(m.pull.Number), result.Branch(m.Trunk)),
	}

	_, listed, err := deps.Repo.RemoteBranch(ctx, m.branch)
	if err != nil {
		stop := req.unread(readStillOnOrigin, err)
		stop.Lines = slices.Concat(m.lines, stop.Lines)

		return m.carrying(stop)
	}

	if deadlineWithin(ctx, mergeReserve) {
		stop := req.ends(result.Pending,
			result.Say("fewer than %s of the %s time limit were left to finish the merge", mergeReserve, TimeLimit),
			req.again())
		stop.Lines = slices.Concat(m.lines, []result.Phrase{
			result.Say("Fewer than %s of the %s time limit were left to finish the merge, so nothing was written",
				mergeReserve, TimeLimit),
		})

		return m.carrying(stop)
	}

	// The delete on origin checks for an interrupt itself, so only the local ending needs it here.
	if !listed {
		if stop := m.unfinished(ctx, req); stop != nil {
			return stop
		}

		m.lines = append(m.lines, m.goneLine())

		return m.end(ctx, deps, req)
	}

	return m.finish(ctx, deps, req)
}
