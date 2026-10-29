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

// ---------------------------------------------------------------------------
// Text rendering
// ---------------------------------------------------------------------------

// RenderText returns r as the lines of what happened, the excerpt when there
// is one, the reason labeled by the outcome unless r is done, and one Next
// line unless nothing is left to do. A commit SHA in a line or in the reason
// prints as its first seven characters; the Next line prints every SHA in
// full. gitDir is the resolved git dir, or empty outside a repository.
//
// It returns ErrNoReason when r is not done and has no reason, a *WordingError
// when the program's own wording names a hook or a path under gitDir, and an
// error wrapping ErrPlaceholders when a wording's placeholders and names do not
// pair up.
func RenderText(r Result, gitDir string) (string, error) {
	return RenderStyled(r, gitDir, colorprofile.NoTTY)
}

// RenderStyled returns r as RenderText does, drawn for a terminal of profile.
// A profile without color (NoTTY, ASCII) gets exactly what RenderText returns,
// with no escape sequence of this program's. Any other profile draws the
// outcome label, the word Next, and each name in the style of its kind, and
// draws a command the wording wraps in backticks without them, everything
// between drawn as a command; the rest of the wording and the excerpt stay
// unstyled. On every profile, the control characters inside names and the
// excerpt print spelled out as visible text, such as \x1b for ESC, so that
// third-party text never drives the terminal and the only escape sequences
// printed are this program's own styling. It returns the errors of RenderText.
func RenderStyled(r Result, gitDir string, profile colorprofile.Profile) (string, error) {
	view, err := check(r, gitDir)
	if err != nil {
		return "", err
	}

	pen := PenFor(profile)

	var b strings.Builder
	for _, line := range view.lines {
		b.WriteString(pen.spans(shortSHAs(line)))
		b.WriteString("\n")
	}

	if view.Excerpt != "" {
		excerpt := strings.TrimSuffix(view.Excerpt, "\n")
		b.WriteString(visible(excerpt))
		b.WriteString("\n")
	}

	if view.outcome != Done {
		label, style := view.outcome.label()
		b.WriteString(pen.Styled(style, label+":"))
		b.WriteString(" ")
		b.WriteString(pen.spans(shortSHAs(view.reason)))
		b.WriteString("\n")
	}

	if !view.noNext {
		nextStyle := ansi.NewStyle().Bold().Underline(true)
		b.WriteString(pen.Styled(nextStyle, NextLabel))
		b.WriteString(": ")
		b.WriteString(pen.spans(view.next))
		b.WriteString("\n")
	}

	return b.String(), nil
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

// NextLabel is the word the Next line starts with.
const NextLabel = "Next"

// Label returns the word the reason line of o starts with. Done prints no
// reason line, and gets the label of Broke.
func (o Outcome) Label() string {
	label, _ := o.label()

	return label
}

// label returns the word the reason line of o starts with, and its style:
// yellow for a result that only needs another run, red for one that stopped.
func (o Outcome) label() (string, ansi.Style) {
	red := ansi.NewStyle().ForegroundColor(ansi.Red)

	switch o {
	case Refused:
		return "Refused", red
	case Pending:
		return "Pending", YellowStyle()
	case DeliveredNotGreen:
		return "Delivered, not green", red
	case Ask:
		return "Ask", YellowStyle()
	case Done, Broke:
		fallthrough
	default:
		return "Broke", red
	}
}

// GreenStyle returns the style of what went well, such as the count of passed
// checks.
func GreenStyle() ansi.Style {
	return ansi.NewStyle().ForegroundColor(ansi.Green)
}

// YellowStyle returns the style of what is in progress or only needs another run, such as the
// spinner and a pending result's label.
func YellowStyle() ansi.Style {
	return ansi.NewStyle().ForegroundColor(ansi.Yellow)
}

// FaintStyle returns the style of what needs no attention, such as a SHA or a
// tally of zero.
func FaintStyle() ansi.Style {
	return ansi.NewStyle().Faint()
}

// kindStyle returns the style a name of k is drawn in, and false for wording,
// ticks and names of no particular kind, which are drawn unstyled.
func kindStyle(k kind) (ansi.Style, bool) {
	switch k {
	case kindBranch, kindPullRequest, kindCommand:
		return ansi.NewStyle().Bold().ForegroundColor(ansi.Blue), true
	case kindSHA, kindSkipped, kindZero:
		return FaintStyle(), true
	case kindPassed:
		return GreenStyle(), true
	case kindURL:
		return ansi.NewStyle().Underline(true), true
	case kindWording, kindName, kindTick:
		fallthrough
	default:
		return nil, false
	}
}

// ---------------------------------------------------------------------------
// Pen and visible text
// ---------------------------------------------------------------------------

// Pen draws text with styles when its terminal shows color, and as plain
// text otherwise.
type Pen struct {
	color bool
}

// PenFor returns the pen of a terminal of profile: one that draws in styles
// from the ANSI profile up, and as plain text below it.
func PenFor(profile colorprofile.Profile) Pen {
	return Pen{color: profile >= colorprofile.ANSI}
}

// Color reports whether p draws in styles.
func (p Pen) Color() bool {
	return p.color
}

// Styled returns text drawn in style, closed so that the style ends with it,
// and text as it is when p draws no styles.
func (p Pen) Styled(style ansi.Style, text string) string {
	if !p.color {
		return text
	}

	return style.Styled(text)
}

// spans returns spans drawn one by one, each name in the style of its kind and
// with its control characters spelled out by visible. A pen that draws in
// styles leaves out the ticks around an inline command, whose style shows it.
func (p Pen) spans(spans []span) string {
	var b strings.Builder
	for _, s := range spans {
		style, ok := kindStyle(s.kind)
		switch {
		case s.kind == kindTick:
			if !p.color {
				b.WriteString(s.text)
			}
		case ok:
			b.WriteString(p.Styled(style, visible(s.text)))
		case s.kind == kindWording:
			b.WriteString(s.text)
		default:
			b.WriteString(visible(s.text))
		}
	}

	return b.String()
}

// shaWidth is how many characters of a commit SHA a line or a reason prints,
// git's own shortest abbreviation.
const shaWidth = 7

// shortSHAs returns spans with each commit SHA cut to its first shaWidth
// characters. The Next line does not go through it: a command it names, such
// as a cherry-pick, keeps every SHA as the commit's full name.
func shortSHAs(spans []span) []span {
	short := slices.Clone(spans)
	for i, s := range short {
		if s.kind == kindSHA && len(s.text) > shaWidth {
			short[i].text = s.text[:shaWidth]
		}
	}

	return short
}

// visible returns third-party text with each control character that could
// drive the terminal spelled out, so that it prints as text and cannot move
// the cursor, erase a line, or change the style of what follows:
//
//   - ESC, DEL and every other C0 control as \x and two lowercase hex digits,
//     such as \x1b for ESC and \x08 for a backspace;
//   - each C1 control, U+0080 to U+009F, as \u and four, such as \u009b.
//
// Tab, line feed and carriage return stay as they are: a name holds no line
// break by the time it is rendered, and an excerpt keeps its lines. Bytes that
// are not valid UTF-8 stay as they are too.
func visible(text string) string {
	if !strings.ContainsFunc(text, spelledOut) {
		return text
	}

	var b strings.Builder
	for rest := text; rest != ""; {
		r, size := utf8.DecodeRuneInString(rest)
		switch {
		case !spelledOut(r):
			b.WriteString(rest[:size])
		case r < utf8.RuneSelf:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}

		rest = rest[size:]
	}

	return b.String()
}

func spelledOut(r rune) bool {
	return unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r'
}

// ---------------------------------------------------------------------------
// JSON rendering
// ---------------------------------------------------------------------------

// Object is the one JSON object RenderJSON writes, and what a caller decodes
// it into. Its fields print in the order they are declared.
type Object struct {
	Payload  *Payload `json:"payload,omitempty"`
	Command  string   `json:"command"`
	Words    []string `json:"words,omitempty"`
	Outcome  string   `json:"outcome"`
	Next     string   `json:"next"`
	Reason   string   `json:"reason,omitempty"`
	Excerpt  string   `json:"excerpt,omitempty"`
	Lines    []string `json:"lines,omitempty"`
	ExitCode int      `json:"exit_code"`
}

// RenderJSON returns r as one JSON object on one line. gitDir is the resolved
// git dir, or empty outside a repository. It returns the errors of RenderText,
// and an error wrapping the encoder's when the object cannot be encoded.
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

// ---------------------------------------------------------------------------
// Wording check
// ---------------------------------------------------------------------------

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
	lines   [][]span
	reason  []span
	next    []span
	Object  `json:",inline"`
	outcome Outcome
	noNext  bool
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
		noNext:   r.Next.None(),
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

	if view.next, err = checked(r.Command, r.Next.phrase.wording, gitDir, r.Next.spans); err != nil {
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
		return nil, fmt.Errorf("render %s result: %w", command, err)
	}

	if word, found := forbidden(ownWording, gitDir); found {
		return nil, &WordingError{Line: joined(spans), Word: word}
	}

	return spans, nil
}

// joined returns the text of spans, as the plain renderers print it.
func joined(spans []span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.text)
	}

	return b.String()
}

// spans returns the value printed after "Next:". Every span of an ils command
// is drawn as the command, except the ticks of an inline command, which stay
// ticks so a color pen can leave them out.
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

	command := []span{{text: commandPrefix, kind: kindCommand}}
	for _, s := range spans {
		if s.kind != kindTick {
			s.kind = kindCommand
		}
		command = append(command, s)
	}

	return command, nil
}

// forbidden returns what in ownWording names a hook or a path under the git dir, and whether
// anything does: gitDir itself when the wording names a path under it, else the first word that
// names a git dir or a hook.
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
