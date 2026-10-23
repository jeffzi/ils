// Package result holds the one value every command returns and the two
// renderers that show it.
package result

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

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

// placeholder marks where a name goes in a wording.
const placeholder = "%s"

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
	kindCount
	kindCommand
)

// Name is a name marked with its kind, for Say and the Next builders to
// interpolate. Its text prints as an unmarked name does; only the styled
// renderer tells the kinds apart.
type Name struct {
	// spans are the runs the name prints as: one for a single name, and for a
	// list each name and the wording that joins them.
	spans []span
}

// marked returns text as a name of kind k.
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

// Count marks n as a count.
func Count(n int) Name {
	return marked(strconv.Itoa(n), kindCount)
}

// Command marks an ils command by its words after "ils", such as "push",
// printed as "ils push".
func Command(words string) Name {
	return marked("ils "+words, kindCommand)
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

// span is one run of a rendered phrase: its own wording, or one name.
type span struct {
	text string
	kind kind
}

// joined returns the text of spans, as the plain renderers print it.
func joined(spans []span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.text)
	}

	return b.String()
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
// check, a commit subject, a URL, a count. A [Name] keeps the kind it was
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

// ErrPlaceholders reports a phrase whose wording and names do not pair up.
var ErrPlaceholders = errors.New("placeholders and names differ in number")

// spans returns the phrase with its names in place, as runs of wording and
// names.
func (p Phrase) spans() ([]span, error) {
	parts := strings.Split(p.wording, placeholder)
	placeholders := len(parts) - 1
	if placeholders != len(p.names) {
		return nil, fmt.Errorf("%w: %q has %d placeholders and %d names",
			ErrPlaceholders, p.wording, placeholders, len(p.names))
	}

	spans := make([]span, 0, len(parts)+len(p.names))
	for i, part := range parts {
		spans = append(spans, span{text: part, kind: kindWording})
		if i < len(p.names) {
			spans = append(spans, p.names[i].spans...)
		}
	}

	return spans, nil
}

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

// spans returns the value printed after "Next:". Every span of an ils command
// is drawn as the command.
func (n Next) spans() ([]span, error) {
	if n.kind == nextNone {
		return []span{{text: "none", kind: kindWording}}, nil
	}

	spans, err := n.phrase.spans()
	if err != nil {
		return nil, err
	}

	if n.kind != nextCommand {
		return spans, nil
	}

	command := []span{{text: "ils ", kind: kindCommand}}
	for _, s := range spans {
		command = append(command, span{text: s.text, kind: kindCommand})
	}

	return command, nil
}

// wording returns the fixed text of n, as the wording check reads it.
func (n Next) wording() string {
	return n.phrase.wording
}

// Result is what one command did and what comes next.
type Result struct {
	// Payload is the structured detail the JSON renderer carries. It is
	// third-party text and is never checked for words.
	Payload *Payload
	Command string
	// Excerpt is third-party text such as a bounded part of a CI log, printed
	// verbatim between the lines and Next. It is never checked for words.
	Excerpt string
	// Reason says why the command did not end as done. It is set on every
	// outcome but Done.
	Reason Phrase
	Words  []string
	// Lines say what happened, in order.
	Lines   []Phrase
	Next    Next
	Outcome Outcome
}

// Payload is a result's structured detail, one field per command that has any.
type Payload struct {
	Push  *Push  `json:"push,omitempty"`
	Merge *Merge `json:"merge,omitempty"`
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

// Check is one check or status reported on a commit.
type Check struct {
	Name     string `json:"name"`
	Bucket   string `json:"bucket"`
	Workflow string `json:"workflow"`
	Link     string `json:"link"`
}
