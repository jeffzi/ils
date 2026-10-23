package result

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// ErrNoReason reports a result that did not end as done and says no reason.
var ErrNoReason = errors.New("result has no reason")

// WordingError reports a line whose own wording names a hook or a path under
// the git dir.
type WordingError struct {
	// Line is the offending line, with its names in place.
	Line string
	// Word is the field of the wording that broke the rule, punctuation
	// included, or the git dir as handed in when the wording names it.
	Word string
}

// Error returns the offending line and word.
func (e *WordingError) Error() string {
	return fmt.Sprintf("line %q names a hook or a path under the git dir: %q", e.Line, e.Word)
}

// shown is a checked result with every phrase rendered. Both renderers print
// from it and from nothing else, so the two cannot drift.
type shown struct {
	Payload  *Payload `json:"payload,omitempty"`
	lines    [][]span
	reason   []span
	next     []span
	Command  string   `json:"command"`
	Words    []string `json:"words,omitempty"`
	Outcome  string   `json:"outcome"`
	Next     string   `json:"next"`
	Reason   string   `json:"reason,omitempty"`
	Excerpt  string   `json:"excerpt,omitempty"`
	Lines    []string `json:"lines,omitempty"`
	ExitCode int      `json:"exit_code"`
	outcome  Outcome
	nextNone bool
}

// RenderText returns r as the lines of what happened, the excerpt when there
// is one, the reason labeled by the outcome unless r is done, and one Next
// line unless nothing is left to do. gitDir is the resolved git dir, or empty
// outside a repository.
func RenderText(r Result, gitDir string) (string, error) {
	return RenderStyled(r, gitDir, colorprofile.NoTTY)
}

// RenderStyled returns r as RenderText does, drawn for a terminal of profile.
// A profile without color (NoTTY, ASCII) gets exactly what RenderText returns,
// with no escape sequence of this program's. Any other profile draws the
// outcome label, the word Next, and each name in the style of its kind; the
// wording and the excerpt stay unstyled. Escape sequences inside names and
// the excerpt print as they are, and the style is reset after each, so they
// never change how the text around them is drawn.
func RenderStyled(r Result, gitDir string, profile colorprofile.Profile) (string, error) {
	view, err := check(r, gitDir)
	if err != nil {
		return "", err
	}

	pen := pen{color: profile >= colorprofile.ANSI}

	var b strings.Builder
	for _, line := range view.lines {
		b.WriteString(pen.spans(line) + "\n")
	}

	if view.Excerpt != "" {
		b.WriteString(pen.thirdParty(strings.TrimSuffix(view.Excerpt, "\n")) + "\n")
	}

	if view.outcome != Done {
		label, style := view.outcome.label()
		b.WriteString(pen.styled(style, label+":") + " " + pen.spans(view.reason) + "\n")
	}

	if !view.nextNone {
		b.WriteString(pen.styled(ansi.NewStyle().Bold().Underline(true), "Next") + ": " + pen.spans(view.next) + "\n")
	}

	return b.String(), nil
}

// label returns the word the reason line of o starts with, and its style:
// yellow for a result that only needs another run, red for one that stopped.
func (o Outcome) label() (string, ansi.Style) {
	red := ansi.NewStyle().ForegroundColor(ansi.Red)
	yellow := ansi.NewStyle().ForegroundColor(ansi.Yellow)

	switch o {
	case Refused:
		return "Refused", red
	case Pending:
		return "Pending", yellow
	case DeliveredNotGreen:
		return "Delivered, not green", red
	case Ask:
		return "Ask", yellow
	case Done, Broke:
		fallthrough
	default:
		return "Broke", red
	}
}

// kindStyle returns the style a name of k is drawn in, and false for wording
// and names of no particular kind, which are drawn unstyled.
func kindStyle(k kind) (ansi.Style, bool) {
	switch k {
	case kindBranch, kindPullRequest, kindCommand:
		return ansi.NewStyle().Bold().ForegroundColor(ansi.Blue), true
	case kindSHA:
		return ansi.NewStyle().Faint(), true
	case kindURL:
		return ansi.NewStyle().Underline(true), true
	case kindCount:
		return ansi.NewStyle().Bold(), true
	case kindWording, kindName:
		fallthrough
	default:
		return nil, false
	}
}

// pen draws text with styles when its terminal shows color, and as plain
// text otherwise.
type pen struct {
	color bool
}

// styled returns text drawn in style, closed so that the style ends with it.
func (p pen) styled(style ansi.Style, text string) string {
	if !p.color {
		return text
	}

	return style.Styled(text)
}

// thirdParty returns text that is not this program's, followed by a reset
// when it holds an escape sequence that could carry a style past its end.
func (p pen) thirdParty(text string) string {
	if p.color && strings.Contains(text, "\x1b") {
		return text + ansi.ResetStyle
	}

	return text
}

// spans returns spans drawn one by one, each name in the style of its kind.
func (p pen) spans(spans []span) string {
	var b strings.Builder
	for _, s := range spans {
		style, ok := kindStyle(s.kind)
		switch {
		case ok:
			b.WriteString(p.styled(style, s.text))
		case s.kind == kindWording:
			b.WriteString(s.text)
		default:
			b.WriteString(p.thirdParty(s.text))
		}
	}

	return b.String()
}

// RenderJSON returns r as one JSON object on one line. gitDir is the resolved
// git dir, or empty outside a repository.
func RenderJSON(r Result, gitDir string) (string, error) {
	view, err := check(r, gitDir)
	if err != nil {
		return "", err
	}

	// Third-party text may hold invalid UTF-8, which RenderText prints as is.
	// The encoder replaces each invalid byte with U+FFFD rather than failing
	// the result.
	out, err := json.Marshal(view, json.Deterministic(true), jsontext.AllowInvalidUTF8(true))
	if err != nil {
		return "", fmt.Errorf("encode %s result: %w", r.Command, err)
	}

	return string(out) + "\n", nil
}

// check renders every phrase of r and rejects a result whose own wording
// names a hook or a path under the git dir.
func check(r Result, gitDir string) (shown, error) {
	view := shown{
		Command:  r.Command,
		Words:    r.Words,
		Outcome:  r.Outcome.String(),
		ExitCode: r.Outcome.ExitCode(),
		Excerpt:  r.Excerpt,
		Payload:  r.Payload,
		outcome:  r.Outcome,
		nextNone: r.Next.kind == nextNone,
	}

	for _, line := range r.Lines {
		spans, err := checked(r.Command, line.wording, gitDir, line.spans)
		if err != nil {
			return shown{}, err
		}

		view.lines = append(view.lines, spans)
		view.Lines = append(view.Lines, joined(spans))
	}

	var err error
	if view.reason, err = checked(r.Command, r.Reason.wording, gitDir, r.Reason.spans); err != nil {
		return shown{}, err
	}

	if view.next, err = checked(r.Command, r.Next.wording(), gitDir, r.Next.spans); err != nil {
		return shown{}, err
	}

	view.Reason = joined(view.reason)
	view.Next = joined(view.next)

	if r.Outcome != Done && view.Reason == "" {
		return shown{}, fmt.Errorf("%w: %s ended as %s", ErrNoReason, r.Command, r.Outcome)
	}

	return view, nil
}

// checked returns the spans render gives for the command named command,
// unless ownWording breaks the rule. The rule reads the unstyled wording, and
// the error names the line unstyled.
func checked(command, ownWording, gitDir string, render func() ([]span, error)) ([]span, error) {
	spans, err := render()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", command, err)
	}

	if word, found := forbidden(ownWording, gitDir); found {
		return nil, &WordingError{Line: joined(spans), Word: word}
	}

	return spans, nil
}

// forbidden returns the first part of ownWording that names a hook or a path
// under the git dir.
func forbidden(ownWording, gitDir string) (string, bool) {
	// The git dir is matched against the whole wording, since a path may hold spaces.
	if dir := strings.TrimRight(folded(gitDir), "/"); dir != "" && namesPath(folded(ownWording), dir) {
		return gitDir, true
	}

	for word := range strings.FieldsSeq(ownWording) {
		if namesGitDir(word) || namesHook(word) {
			return word, true
		}
	}

	return "", false
}

// namesPath reports whether text names dir or a path under it. A longer name
// that starts with dir, as "metadata" does for "meta", is a sibling and not a
// match.
func namesPath(text, dir string) bool {
	for {
		_, rest, found := strings.Cut(text, dir)
		if !found {
			return false
		}

		if next, _ := utf8.DecodeRuneInString(rest); !inName(next) {
			return true
		}

		text = rest
	}
}

// inName reports whether r is a character a file name commonly carries on
// past a prefix.
func inName(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
}

// folded returns path in the one spelling two paths are compared in:
// lower case, with forward slashes.
func folded(path string) string {
	return strings.ReplaceAll(strings.ToLower(path), `\`, "/")
}

// namesGitDir reports whether word has .git as a path component.
func namesGitDir(word string) bool {
	separator := func(r rune) bool { return r == '/' || r == '\\' }
	for component := range strings.FieldsFuncSeq(word, separator) {
		// A sentence may end on the component, as in "nothing in .git."
		before, found := strings.CutSuffix(strings.TrimRight(component, ".,;:!?)'\"`"), ".git")
		if found && !endsInName(before) {
			return true
		}
	}

	return false
}

// endsInName reports whether text ends in a character of a file name, which
// makes a following .git a suffix, as in "ils.git", and not a component.
func endsInName(text string) bool {
	last, _ := utf8.DecodeLastRuneInString(text)

	return text != "" && (inName(last) || last == '.')
}

// namesHook reports whether word holds a hook name, in any case.
func namesHook(word string) bool {
	hookNames := []string{
		"pre-commit", "commit-msg", "pre-push", "post-commit", "prek", "lefthook", "hooks/",
	}

	lowered := strings.ToLower(word)

	return slices.ContainsFunc(hookNames, func(hook string) bool { return strings.Contains(lowered, hook) })
}
