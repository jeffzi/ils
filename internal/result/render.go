package result

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrPlaceholders reports a phrase whose wording and names do not pair up.
var ErrPlaceholders = errors.New("placeholders and names differ in number")

// ErrNoReason reports a result that did not end as done and says no reason.
var ErrNoReason = errors.New("result has no reason")

// WordingError reports a line whose own wording names a hook or a path under
// the git dir.
type WordingError struct {
	// Line is the offending line, with its names in place.
	Line string
	// Word is the word of the wording that broke the rule.
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
	Command  string   `json:"command"`
	Words    []string `json:"words,omitempty"`
	Outcome  string   `json:"outcome"`
	Next     string   `json:"next"`
	Reason   string   `json:"reason,omitempty"`
	Excerpt  string   `json:"excerpt,omitempty"`
	Lines    []string `json:"lines,omitempty"`
	ExitCode int      `json:"exit_code"`
	refused  bool
}

// RenderText returns r as the lines of what happened, the excerpt when there
// is one, and one Next line. gitDir is the resolved git dir, or empty outside
// a repository.
func RenderText(r Result, gitDir string) (string, error) {
	view, err := check(r, gitDir)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	if view.refused {
		b.WriteString("Refused: " + view.Reason + "\n")
	}

	for _, line := range view.Lines {
		b.WriteString(line + "\n")
	}

	if view.Excerpt != "" {
		b.WriteString(strings.TrimSuffix(view.Excerpt, "\n") + "\n")
	}

	b.WriteString("Next: " + view.Next + "\n")

	return b.String(), nil
}

// RenderJSON returns r as one JSON object on one line. gitDir is the resolved
// git dir, or empty outside a repository.
func RenderJSON(r Result, gitDir string) (string, error) {
	view, err := check(r, gitDir)
	if err != nil {
		return "", err
	}

	out, err := json.Marshal(view, json.Deterministic(true))
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
		refused:  r.Outcome == Refused,
	}

	for _, line := range r.Lines {
		text, err := checked(r.Command, line.wording, gitDir, line.text)
		if err != nil {
			return shown{}, err
		}

		view.Lines = append(view.Lines, text)
	}

	var err error
	if view.Reason, err = checked(r.Command, r.Reason.wording, gitDir, r.Reason.text); err != nil {
		return shown{}, err
	}

	if view.Next, err = checked(r.Command, r.Next.wording(), gitDir, r.Next.text); err != nil {
		return shown{}, err
	}

	if r.Outcome != Done && view.Reason == "" {
		return shown{}, fmt.Errorf("%w: %s ended as %s", ErrNoReason, r.Command, r.Outcome)
	}

	return view, nil
}

// checked returns the text render gives for the command named command, unless
// ownWording breaks the rule.
func checked(command, ownWording, gitDir string, render func() (string, error)) (string, error) {
	text, err := render()
	if err != nil {
		return "", fmt.Errorf("%s: %w", command, err)
	}

	if word, found := forbidden(ownWording, gitDir); found {
		return "", &WordingError{Line: text, Word: word}
	}

	return text, nil
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

	return text != "" && (unicode.IsLetter(last) || unicode.IsDigit(last) || strings.ContainsRune("._-", last))
}

// namesHook reports whether word holds a hook name, in any case.
func namesHook(word string) bool {
	// hookNames are the words that name a hook or a tool that runs hooks.
	hookNames := []string{
		"pre-commit", "commit-msg", "pre-push", "post-commit", "prek", "lefthook", "hooks/",
	}

	lowered := strings.ToLower(word)
	for _, hook := range hookNames {
		if strings.Contains(lowered, hook) {
			return true
		}
	}

	return false
}
