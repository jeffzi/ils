// Package forge names, in the landing flow's terms, what a command asks of the host
// that keeps the repository, its changes, and their checks.
package forge

import (
	"errors"
	"slices"
)

// ---------------------------------------------------------------------------
// Changes
// ---------------------------------------------------------------------------

// Repo identifies a repository on a forge.
type Repo struct {
	Host  string
	Owner string
	Name  string
	Fork  bool
	// CanPush reports whether the logged-in user may push to the repository.
	CanPush bool
	// SquashAllowed reports whether the repository's settings allow squash merges. The rules on a
	// branch can still forbid them: see Protection.
	SquashAllowed bool
}

// Slug names the repository as host/owner/name, the way gh's `--repo` takes it.
func (r Repo) Slug() string {
	return r.Host + "/" + r.Owner + "/" + r.Name
}

// ChangeState is where a change stands in its life.
type ChangeState string

// The states of a change.
const (
	ChangeOpen   ChangeState = "open"
	ChangeClosed ChangeState = "closed"
	ChangeMerged ChangeState = "merged"
)

// Change is a branch proposed for landing on a base branch.
type Change struct {
	URL   string
	Title string
	// Base is the branch the change lands on.
	Base string
	// HeadSHA is the commit the forge holds as the tip of the change.
	HeadSHA string
	State   ChangeState
	Number  int
	Draft   bool
}

// NewChange describes a change to open; its body is always empty.
type NewChange struct {
	Title string
	// Base is the branch the change lands on.
	Base string
	// Head is the branch that carries the change.
	Head string
}

// ChangeRef points at a change that was just opened.
type ChangeRef struct {
	URL    string
	Number int
}

// Message is what a change holds toward the commit that lands it.
type Message struct {
	Title string
	Body  string
	// Trailers is the trailer block the forge would end a squash commit with when the logged-in
	// user lands the change, one trailer per entry in the forge's order; it may be empty.
	Trailers []string
}

// Landing asks the forge to squash a change onto its base.
type Landing struct {
	// HeadSHA is the tip whose checks were read: the forge refuses the landing once the change
	// has moved past it.
	HeadSHA string
	Subject string
	// Body is the commit body, landed as given even when empty.
	Body   string
	Number int
}

// MergeState is whether the forge can land a change, and what stops it when it cannot.
type MergeState string

// The merge states a command switches on, whatever the forge calls them.
const (
	MergeClean                 MergeState = "clean"
	MergeBehind                MergeState = "behind"
	MergeConflicting           MergeState = "conflicting"
	MergeDraft                 MergeState = "draft"
	MergeBlockedByReview       MergeState = "blocked-by-review"
	MergeBlockedByMissingCheck MergeState = "blocked-by-missing-check"
	MergeBlockedOther          MergeState = "blocked-other"
	// MergeUnknown is the answer while the forge has not worked the state out yet.
	MergeUnknown MergeState = "unknown"
)

// Head is the tip of a change as the forge sees it, with whether that tip can land.
type Head struct {
	SHA        string
	MergeState MergeState
	// RawMergeState is the forge's own value behind MergeState, kept for printing.
	RawMergeState string
}

// HeadQuery names the change whose tip is wanted.
type HeadQuery struct {
	// Required names the checks the trunk's protection requires; it may be empty.
	Required []string
	Number   int
}

// Protection is what the forge enforces on the trunk.
type Protection struct {
	// RequiredChecks names the checks a change must pass to land; it may be empty.
	RequiredChecks []string
	// SquashForbiddenBy holds the ids of the rulesets whose rules on the trunk forbid a squash
	// merge, in the order the forge lists them; it is empty when SquashAllowed is true.
	SquashForbiddenBy []int
	// SquashAllowed reports whether the rules on the trunk allow a squash merge. The repository's
	// settings can still forbid one: see Repo.
	SquashAllowed bool
}

// ---------------------------------------------------------------------------
// Checks and runs
// ---------------------------------------------------------------------------

// Bucket is the outcome of one check.
type Bucket string

// The outcomes of a check.
const (
	BucketPass     Bucket = "pass"
	BucketFail     Bucket = "fail"
	BucketPending  Bucket = "pending"
	BucketSkipped  Bucket = "skipped"
	BucketCanceled Bucket = "canceled"
)

// Check is one check on a commit.
type Check struct {
	Name     string
	Bucket   Bucket
	Workflow string
	Link     string
	// Required reports whether the trunk's protection names the check.
	Required bool
}

// RunStatus is how far a workflow run has gone.
type RunStatus string

// The statuses of a workflow run.
const (
	RunQueued     RunStatus = "queued"
	RunInProgress RunStatus = "in-progress"
	// RunAwaitingPerson is a run that starts only once someone approves it.
	RunAwaitingPerson RunStatus = "awaiting-person"
	RunCompleted      RunStatus = "completed"
)

// Run is one workflow run on a commit.
type Run struct {
	Name   string
	Status RunStatus
}

// ---------------------------------------------------------------------------
// Verdicts
// ---------------------------------------------------------------------------

// FailedLogState is whether the log of a failing check was read, and why not when it was not.
type FailedLogState string

// The states of a failed log; the zero value belongs to a verdict that is not red.
const (
	// FailedLogRead is a log whose whole text was read.
	FailedLogRead FailedLogState = "read"
	// FailedLogNoRun is a failing check that no workflow run stands behind, such as
	// a status another service reports, so that no log exists.
	FailedLogNoRun FailedLogState = "no-run"
	// FailedLogUnreadable is a failing check with a workflow run whose log could not be read.
	FailedLogUnreadable FailedLogState = "unreadable"
	// FailedLogInProgress is a failing check whose workflow run has not completed: the
	// forge gives a run's log only once every job of it has, so the log is not available yet.
	FailedLogInProgress FailedLogState = "in-progress"
)

// FailedLog is the log of the check Verdict.Failed names; it is the zero value
// when the verdict is not red.
type FailedLog struct {
	// Check is the name of the check whose log was sought.
	Check string
	// Text is the whole log when State is FailedLogRead, and empty otherwise.
	Text  string
	State FailedLogState
}

// VerdictQuery names the commit of a change whose verdict is wanted.
type VerdictQuery struct {
	SHA string
	// Required names the checks that decide; when empty, every check decides.
	Required []string
	Number   int
}

// VerdictState is the summary of a verdict.
type VerdictState string

// The summaries of a verdict.
const (
	// VerdictGreen is at least one deciding check, each one passed or skipped.
	VerdictGreen VerdictState = "green"
	// VerdictRed is a deciding check that failed or was canceled.
	VerdictRed VerdictState = "red"
	// VerdictPending is a deciding check still running or of an outcome that is
	// not known, with none failed or canceled.
	VerdictPending VerdictState = "pending"
	// VerdictNone is no deciding check at all.
	VerdictNone VerdictState = "none"
)

// FailedCheck is the deciding check that makes a verdict red.
type FailedCheck struct {
	Name string
	Link string
	// Canceled reports whether the check was canceled and did not fail.
	Canceled bool
}

// Verdict is what the checks say about one commit of a change.
type Verdict struct {
	FailedLog FailedLog
	// Checks are the checks on the commit. Where the trunk's protection names
	// required checks, only those with Required set decide; otherwise each one does.
	Checks []Check
	// MissingRequired names the required checks with no check on the commit.
	MissingRequired []string
	Runs            []Run
}

// Deciding returns the checks the state is read from: the required ones where the trunk's
// protection names required checks, and every check otherwise.
func (v Verdict) Deciding() []Check {
	isRequired := func(check Check) bool { return check.Required }
	if len(v.MissingRequired) == 0 && !slices.ContainsFunc(v.Checks, isRequired) {
		return v.Checks
	}

	var deciding []Check

	for _, check := range v.Checks {
		if check.Required {
			deciding = append(deciding, check)
		}
	}

	return deciding
}

// State returns the summary of the deciding checks.
//
// A required check that is missing does not change it: MissingRequired says so on its own.
func (v Verdict) State() VerdictState {
	deciding := v.Deciding()

	switch _, failed := v.Failed(); {
	case failed:
		return VerdictRed
	case len(deciding) == 0:
		return VerdictNone
	case len(v.Pending()) > 0:
		return VerdictPending
	default:
		return VerdictGreen
	}
}

// Failed returns the first deciding check that failed or was canceled, and whether there is one.
func (v Verdict) Failed() (FailedCheck, bool) {
	for _, check := range v.Deciding() {
		canceled := check.Bucket == BucketCanceled
		if check.Bucket == BucketFail || canceled {
			return FailedCheck{Name: check.Name, Link: check.Link, Canceled: canceled}, true
		}
	}

	return FailedCheck{}, false
}

// Pending returns the deciding checks still running, in the order of Checks.
//
// A check whose bucket is none of the known ones counts as still running, so
// that an outcome nobody can read never passes for a good one.
func (v Verdict) Pending() []Check {
	var pending []Check

	for _, check := range v.Deciding() {
		switch check.Bucket {
		case BucketPass, BucketSkipped, BucketFail, BucketCanceled:
		default:
			pending = append(pending, check)
		}
	}

	return pending
}

// Running reports whether a workflow run is queued or in progress; a run that
// awaits a person is not, since nothing says it will ever start.
func (v Verdict) Running() bool {
	return slices.ContainsFunc(v.Runs, func(run Run) bool {
		return run.Status == RunQueued || run.Status == RunInProgress
	})
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// ErrChangeExists reports that the branch already has an open change.
var ErrChangeExists = errors.New("a change already exists for the branch")

// NotLoggedInError reports that the user is not logged in to the forge's host.
type NotLoggedInError struct {
	// Err is the failed login check.
	Err  error
	Host string
}

// Error names the host the user is not logged in to.
func (e *NotLoggedInError) Error() string {
	return "not logged in to " + e.Host
}

// Unwrap returns the failed login check.
func (e *NotLoggedInError) Unwrap() error {
	return e.Err
}
