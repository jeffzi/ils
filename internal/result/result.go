package result

import (
	"fmt"
	"regexp"
	"strconv"
)

// ---------------------------------------------------------------------------
// Outcomes
// ---------------------------------------------------------------------------

// Outcome is how a command ended.
type Outcome int

// The outcomes, one per reaction a caller can have.
const (
	Done Outcome = iota
	Broke
	Refused
	// Ask is the guard's alone; it is listed so that no later outcome takes its code.
	Ask
	Pending
	DeliveredNotGreen
	// outcomeEnd follows the last outcome, so that ParseOutcome tries every one; a new outcome goes
	// before it.
	outcomeEnd
)

// ExitCode returns the process exit code for o.
func (o Outcome) ExitCode() int {
	switch o {
	case Done:
		return 0
	case Refused:
		return 2
	case Ask:
		return 3
	case Pending:
		return 4
	case DeliveredNotGreen:
		return 5
	case Broke:
		fallthrough
	default:
		// An outcome outside the table is a defect, and a defect must not exit 0.
		return 1
	}
}

// String returns the outcome's name as the JSON renderer prints it.
func (o Outcome) String() string {
	switch o {
	case Done:
		return "done"
	case Refused:
		return "refused"
	case Ask:
		return "ask"
	case Pending:
		return "pending"
	case DeliveredNotGreen:
		return "delivered-not-green"
	case Broke:
		fallthrough
	default:
		return "broke"
	}
}

// ParseOutcome returns the outcome whose String is name, and false when no outcome has that name.
func ParseOutcome(name string) (Outcome, bool) {
	for o := range outcomeEnd {
		if o.String() == name {
			return o, true
		}
	}

	return Done, false
}

// ---------------------------------------------------------------------------
// Marked names
// ---------------------------------------------------------------------------

const commandPrefix = "ils "

// wording is text written in this program's source. The type is unexported so
// that a caller can pass only a constant: text computed at run time, which
// could carry git's or GitHub's words past the wording check, does not compile.
type wording string

// kind is what a piece of a phrase holds, which decides how the styled
// renderer draws it.
type kind int

const (
	kindWording kind = iota
	kindName
	kindBranch
	kindPullRequest
	kindSHA
	kindURL
	kindCommand
	kindPassed
	kindSkipped
	kindZero
	kindTick
)

// Name is a name marked with its kind, for Say and the Next builders to
// interpolate. Its text prints as an unmarked name does; only the styled
// renderer tells the kinds apart.
type Name struct {
	// spans are the runs the name prints as: one for a single name, and for a
	// list each name and the wording that joins them.
	spans []span
}

func marked(text string, k kind) Name {
	return Name{spans: []span{{text: text, kind: k}}}
}

// Branch marks name as a branch.
func Branch(name string) Name {
	return marked(name, kindBranch)
}

// Branches marks each of names as a branch and joins them as "a, b and c",
// in the order given.
func Branches(names []string) Name {
	return listed(names, kindBranch, func(i int) string {
		if i == len(names)-1 {
			return " and "
		}

		return ", "
	})
}

// listed returns texts as names of kind k, each but the first preceded by
// the wording before(i) gives for its index i.
func listed(texts []string, k kind, before func(i int) string) Name {
	var list Name
	for i, text := range texts {
		if i > 0 {
			list.spans = append(list.spans, span{text: before(i), kind: kindWording})
		}

		list.spans = append(list.spans, span{text: text, kind: k})
	}

	return list
}

// PullRequest marks a pull request by its number, printed as "#<number>".
func PullRequest(number int) Name {
	return marked("#"+strconv.Itoa(number), kindPullRequest)
}

// PullRequestNumber marks a pull request by its bare number, printed as
// "<number>", as a gh command takes it.
func PullRequestNumber(number int) Name {
	return marked(strconv.Itoa(number), kindPullRequest)
}

// SHA marks sha as a commit SHA.
func SHA(sha string) Name {
	return marked(sha, kindSHA)
}

// SHAs marks each of shas as a commit SHA and joins them with spaces, in the
// order given, as a git command takes them.
func SHAs(shas []string) Name {
	return listed(shas, kindSHA, func(int) string { return " " })
}

// URL marks url as a URL.
func URL(url string) Name {
	return marked(url, kindURL)
}

// Passed marks n as a tally of passed checks, printed as "<n> passed".
func Passed(n int) Name {
	return tally(n, "passed", kindPassed)
}

// Skipped marks n as a tally of skipped checks, printed as "<n> skipped".
func Skipped(n int) Name {
	return tally(n, "skipped", kindSkipped)
}

// tally returns n followed by word, with n marked as k and word left as
// wording. A tally of zero is marked whole as zero, whatever it counts, so that
// it reads as nothing to look at.
func tally(n int, word string, k kind) Name {
	if n == 0 {
		return marked("0 "+word, kindZero)
	}

	return Name{spans: []span{{text: strconv.Itoa(n), kind: k}, {text: " " + word, kind: kindWording}}}
}

// Command marks an ils command by its words after "ils", such as "push",
// printed as "ils push".
func Command(words string) Name {
	return marked(commandPrefix+words, kindCommand)
}

// Concat returns parts printed one after another as one name, as a git
// command and its arguments are: each [Name] keeps the kind it was marked
// with, and any other value is a name of no particular kind.
func Concat(parts ...any) Name {
	var whole Name
	for _, part := range parts {
		whole.spans = append(whole.spans, asName(part).spans...)
	}

	return whole
}

// asName returns v as the name Say interpolates: v itself when it is a
// [Name], and otherwise its text as a name of no particular kind.
func asName(v any) Name {
	if given, ok := v.(Name); ok {
		return given
	}

	return marked(fmt.Sprint(v), kindName)
}

// ---------------------------------------------------------------------------
// Phrases
// ---------------------------------------------------------------------------

// span is one run of a rendered phrase: its own wording, or one name.
type span struct {
	text string
	kind kind
}

// Phrase is a piece of output: fixed wording and the names it interpolates,
// kept apart so that only the wording is checked.
type Phrase struct {
	wording string
	names   []Name
}

var lineBreaks = regexp.MustCompile(`[\r\n]+`)

// Say builds a Phrase from wording and the names that fill its %s
// placeholders, in order. A name is third-party text: a branch, a file, a
// check, a commit subject, a URL, a tally of checks. A [Name] keeps the kind it was
// marked with; any other value is a name of no particular kind. Each run of
// line breaks in a name becomes one space, so that a name cannot split a line
// or forge one.
func Say(w wording, names ...any) Phrase {
	phrase := Phrase{wording: string(w), names: make([]Name, 0, len(names))}
	for _, name := range names {
		given := asName(name)

		oneLine := Name{spans: make([]span, 0, len(given.spans))}
		for _, s := range given.spans {
			oneLine.spans = append(oneLine.spans, span{text: lineBreaks.ReplaceAllString(s.text, " "), kind: s.kind})
		}

		phrase.names = append(phrase.names, oneLine)
	}

	return phrase
}

// ---------------------------------------------------------------------------
// Next
// ---------------------------------------------------------------------------

type nextKind int

const (
	nextNone nextKind = iota
	nextCommand
	nextStep
)

// Next is what to do after a command: an ils command, a judgment step in
// plain words, or nothing.
type Next struct {
	phrase Phrase
	kind   nextKind
}

// NextCommand builds a Next that names an ils command. The wording is the
// command's words after "ils", such as "push" or "branch %s".
func NextCommand(w wording, names ...any) Next {
	return Next{kind: nextCommand, phrase: Say(w, names...)}
}

// NextStep builds a Next that is a judgment step in plain words.
func NextStep(w wording, names ...any) Next {
	return Next{kind: nextStep, phrase: Say(w, names...)}
}

// NextNone builds a Next that says nothing is left to do.
func NextNone() Next {
	return Next{}
}

// None reports whether n says nothing is left to do, which the text renderers print no Next line
// for.
func (n Next) None() bool {
	return n.kind == nextNone
}

// ---------------------------------------------------------------------------
// Result and payloads
// ---------------------------------------------------------------------------

// Result is what one command did and what comes next.
type Result struct {
	// Payload is the structured detail the JSON renderer carries. It is
	// third-party text and is never checked for words.
	Payload *Payload
	Command string
	// Excerpt is third-party text such as a bounded part of a CI log, printed
	// between the lines and Next with its tabs and line breaks kept and every
	// other control character spelled out. It is never checked for words.
	Excerpt string
	// Reason says why the command did not end as done. It is set on every
	// outcome but Done.
	Reason  Phrase
	Words   []string
	Lines   []Phrase
	Next    Next
	Outcome Outcome
}

// Payload is a result's structured detail, one field per command that has any.
type Payload struct {
	Push    *Push    `json:"push,omitempty"`
	Merge   *Merge   `json:"merge,omitempty"`
	Status  *Status  `json:"status,omitempty"`
	Release *Release `json:"release,omitempty"`
}

// The words of Release.Verdict.
const (
	// ReleaseGreen is a release commit whose checks passed.
	ReleaseGreen = "green"
	// ReleaseRed is a release commit one of whose checks failed or was canceled.
	ReleaseRed = "red"
	// ReleasePending is a wait for the checks of the release commit that ended before they
	// concluded, including when none had reported yet.
	ReleasePending = "pending"
	// ReleaseNone is a release commit no check or status reported for. The outcome tells a trunk
	// without CI, which goes on, from one with CI, which refuses.
	ReleaseNone = "none"
	// ReleaseForced is a gate --force skipped.
	ReleaseForced = "forced"
	// ReleaseSkipped is a gate skipped because origin already held the tag at the release commit: a
	// release resumed after an earlier run tagged it.
	ReleaseSkipped = "skipped"
	// ReleaseUnchecked is a release that stopped before the gate read the checks of the release
	// commit, or whose read of the checks failed.
	ReleaseUnchecked = "unchecked"
)

// Release is the structured detail of a release.
type Release struct {
	Version string `json:"version"`
	Tag     string `json:"tag"`
	// SHA is the release commit.
	SHA string `json:"sha"`
	// Verdict is how the CI gate ended, in the words ReleaseGreen and its siblings name.
	Verdict string `json:"verdict"`
	// URL is the link of the GitHub release once it was created or found, and empty otherwise.
	URL        string `json:"url"`
	Prerelease bool   `json:"prerelease"`
	TagOnly    bool   `json:"tag_only"`
}

// Push is the structured detail of a push.
type Push struct {
	URL        string  `json:"url"`
	SHA        string  `json:"sha"`
	Verdict    string  `json:"verdict"`
	MergeState string  `json:"merge_state"`
	FailedLog  string  `json:"failed_log,omitempty"`
	Checks     []Check `json:"checks"`
	Number     int     `json:"number"`
}

// The words of Merge.Ending once the local ending ran.
const (
	// EndingTrunk is a checkout left on the trunk at origin's tip.
	EndingTrunk = "trunk"
	// EndingDetached is a checkout left detached at origin's trunk, the trunk being checked out in
	// another worktree.
	EndingDetached = "detached"
)

// Merge is the structured detail of a merge. Verdict, MergeState and Checks are in the words of
// Push.
//
// A verdict of none means no deciding check reported, or the checks were not read. A merge state
// of unknown and no checks mean the checks were not read, or none of them decides.
type Merge struct {
	URL string `json:"url"`
	// SHA is the head that was merged or found merged.
	SHA        string `json:"sha"`
	Verdict    string `json:"verdict"`
	MergeState string `json:"merge_state"`
	// Subject and Body are the squash commit's message once it is composed after the wait, and
	// empty before. A merge that stops at the 20-second reserve carries them unsent.
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// Ending is trunk or detached once every step of the local ending ran, and empty otherwise.
	Ending string  `json:"ending"`
	Checks []Check `json:"checks"`
	Number int     `json:"number"`
	// Merged is true when this run made the merge, and false when it found the pull request
	// merged or did not merge it.
	Merged bool `json:"merged"`
}

// The words of Status.Standing.
const (
	// StandingBranch is HEAD on a work branch.
	StandingBranch   = "branch"
	StandingTrunk    = "trunk"
	StandingDetached = "detached"
	// StandingNoOrigin is a repository with no remote named origin.
	StandingNoOrigin = "no-origin"
	// StandingNoRepository is a directory in no git repository.
	StandingNoRepository = "no-repository"
)

// The words of Status.Protection, in the words of the forge's rules.
const (
	// ProtectionProtected is a trunk at least one rule applies to.
	ProtectionProtected = "protected"
	// ProtectionNone is a trunk no rule applies to.
	ProtectionNone = "none"
	// ProtectionUnavailable is a trunk whose repository's plan does not allow rules.
	ProtectionUnavailable = "unavailable"
)

// Status is the structured detail of a status: what it read of the branch, origin and GitHub.
// A fact whose read failed keeps its zero value, and Unread names that read.
type Status struct {
	// Standing is one of the Standing words, and empty when where HEAD stands could not be read.
	Standing string `json:"standing"`
	Branch   string `json:"branch"`
	Trunk    string `json:"trunk"`
	// LastTag is, on the trunk, the highest version tag origin holds whose commit is on origin's
	// trunk, and empty when there is none.
	LastTag string `json:"last_tag"`
	// Protection is, on the trunk, one of the Protection words.
	Protection string `json:"protection"`
	// PullRequest is the branch's open pull request against the trunk, nil when it has none.
	PullRequest *OpenPull `json:"pull_request"`
	// PastPullRequests are the branch's closed and merged pull requests, in the order GitHub
	// lists them.
	PastPullRequests []PastPull `json:"past_pull_requests"`
	// Unlanded are, on a detached HEAD, the shas of its commits origin's trunk lacks, oldest first.
	Unlanded []string `json:"unlanded"`
	Unread   []Unread `json:"unread"`
	// Ahead and Behind count the commits the branch and origin's copy of it each have that the
	// other lacks; both are zero when origin has no copy. On the trunk they count against origin's
	// trunk.
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
	// SinceTag is, on the trunk, the number of commits on origin's trunk since LastTag.
	SinceTag int  `json:"since_tag"`
	OnOrigin bool `json:"on_origin"`
	// ReleaseCommit is, on the trunk, whether origin's trunk tip is a release commit, whose subject
	// is `chore: release v<X.Y.Z>`, with an optional pre-release part such as `-rc.1`, with or
	// without a trailing ` (#N)`.
	ReleaseCommit bool `json:"release_commit"`
}

// OpenPull is the open pull request of a status. Verdict, MergeState and Checks are in the words of
// Push, and so are the none and unknown they keep when their read failed.
type OpenPull struct {
	URL        string  `json:"url"`
	Title      string  `json:"title"`
	Body       string  `json:"body"`
	Verdict    string  `json:"verdict"`
	MergeState string  `json:"merge_state"`
	Checks     []Check `json:"checks"`
	Number     int     `json:"number"`
	// TitleOK is whether the title is a conventional commit subject, as `ils merge` requires.
	TitleOK bool `json:"title_ok"`
}

// PastPull is a closed or merged pull request of a status.
type PastPull struct {
	State  string `json:"state"`
	Number int    `json:"number"`
}

// Unread is a read of a status that failed: what it read, as the text names it, and why it failed.
type Unread struct {
	Read  string `json:"read"`
	Error string `json:"error"`
}

// Check is one check or status reported on a commit.
type Check struct {
	Name     string `json:"name"`
	Bucket   string `json:"bucket"`
	Workflow string `json:"workflow"`
	Link     string `json:"link"`
}
