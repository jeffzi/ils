// Package result holds the one value every command returns and the two
// renderers that show it.
package result

import (
	"errors"
	"fmt"
	"regexp"
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

// Phrase is a piece of output: fixed wording and the names it interpolates,
// kept apart so that only the wording is checked.
type Phrase struct {
	wording string
	names   []string
}

var lineBreaks = regexp.MustCompile(`[\r\n]+`)

// Say builds a Phrase from wording and the names that fill its %s
// placeholders, in order. A name is third-party text: a branch, a file, a
// check, a commit subject, a URL, a count. Each run of line breaks in a name
// becomes one space, so that a name cannot split a line or forge one.
func Say(w wording, names ...any) Phrase {
	phrase := Phrase{wording: string(w), names: make([]string, 0, len(names))}
	for _, name := range names {
		phrase.names = append(phrase.names, lineBreaks.ReplaceAllString(fmt.Sprint(name), " "))
	}

	return phrase
}

// ErrPlaceholders reports a phrase whose wording and names do not pair up.
var ErrPlaceholders = errors.New("placeholders and names differ in number")

// text returns the phrase with its names in place.
func (p Phrase) text() (string, error) {
	parts := strings.Split(p.wording, placeholder)
	placeholders := len(parts) - 1
	if placeholders != len(p.names) {
		return "", fmt.Errorf("%w: %q has %d placeholders and %d names",
			ErrPlaceholders, p.wording, placeholders, len(p.names))
	}

	var b strings.Builder
	for i, part := range parts {
		b.WriteString(part)
		if i < len(p.names) {
			b.WriteString(p.names[i])
		}
	}

	return b.String(), nil
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

// text returns the value printed after "Next:".
func (n Next) text() (string, error) {
	if n.kind == nextNone {
		return "none", nil
	}

	text, err := n.phrase.text()
	if err != nil {
		return "", err
	}

	if n.kind == nextCommand {
		return "ils " + text, nil
	}

	return text, nil
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
	Push *Push `json:"push,omitempty"`
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

// Check is one check or status reported on a commit.
type Check struct {
	Name     string `json:"name"`
	Bucket   string `json:"bucket"`
	Workflow string `json:"workflow"`
	Link     string `json:"link"`
}
