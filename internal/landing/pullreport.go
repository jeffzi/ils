package landing

import (
	"context"
	"strings"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/result"
)

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
				result.PastPull{Number: change.Number, State: change.State})
			r.say(r.pastLine(change))
		}
	}

	for _, change := range changes {
		// As in pullRequest, only a merge into the trunk can land the branch.
		if change.State == forge.ChangeMerged && change.Base == r.status.Trunk && r.landed == nil {
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

// pastLine says the closed or merged pull request change, naming the base it was merged into when
// that is not the trunk.
func (r *reading) pastLine(change forge.Change) result.Phrase {
	pull := result.PullRequest(change.Number)
	if change.State == forge.ChangeMerged && r.status.Trunk != "" && change.Base != r.status.Trunk {
		return result.Say("Pull request %s: merged into %s and not into the trunk %s",
			pull, result.Branch(change.Base), result.Branch(r.status.Trunk))
	}

	return result.Say("Pull request %s: %s", pull, string(change.State))
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
		head = forge.Head{}
	}

	if r.cut {
		return
	}

	// Checks of a commit other than the tip say nothing of the branch, so neither they nor a merge
	// step are reported until GitHub shows the tip as the head.
	if moved := r.otherHead(head.SHA); moved != "" {
		r.say(result.Say("The head of pull request %s is %s and not the branch's tip %s, so its checks are not reported",
			result.PullRequest(number), result.SHA(moved), result.SHA(r.on.tip)))

		return
	}

	r.waited.MergeState = head.MergeState

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

// otherHead returns the commit that the listing of the branch's pull requests, or shown, the read
// of the open one's head, names as its head when that is not the branch's tip. It returns "" when
// both name the tip, when the tip is unknown, and for an empty shown, a head that was not read.
func (r *reading) otherHead(shown string) string {
	switch {
	case r.on.tip == "":
		return ""
	case r.open.HeadSHA != r.on.tip:
		return r.open.HeadSHA
	case shown != "" && shown != r.on.tip:
		return shown
	default:
		return ""
	}
}

func checkNames(checks []forge.Check) string {
	var names []string
	for _, check := range checks {
		names = append(names, check.Name)
	}

	return strings.Join(names, ", ")
}

func runningName(check forge.Check) result.Phrase {
	return result.Say(pendingBullet+"%s", check.Name)
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
	case outcome == WaitNeverReported:
		if len(w.Deciding) > 0 {
			r.say(w.checksLine(r.open.HeadSHA))
		}

		r.say(w.neverReportedLine())
	case verdict.State() == forge.VerdictNone:
		r.say(result.Say("No check or status reported for %s", sha))
	default:
		r.say(w.checksLine(r.open.HeadSHA))
	}
}
