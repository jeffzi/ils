package result_test

import (
	"testing"

	"github.com/charmbracelet/colorprofile"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

func TestOutcome_ExitCode_WhenOutcomeIsOutsideTheTable_ReturnsOne(t *testing.T) {
	t.Parallel()

	got := result.Outcome(99).ExitCode()

	assert.Check(t, is.Equal(got, 1))
}

func TestParseOutcome_WhenNameIsAnOutcomeName_ReturnsThatOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want result.Outcome
	}{
		{name: "first outcome", want: result.Done},
		{name: "last outcome", want: result.DeliveredNotGreen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := result.ParseOutcome(tt.want.String())

			assert.Check(t, ok, "ParseOutcome(%q) found nothing", tt.want.String())
			assert.Check(t, is.Equal(got, tt.want), "ParseOutcome(%q)", tt.want.String())
		})
	}
}

func TestParseOutcome_WhenNameIsNoOutcomeName_ReportsNotFound(t *testing.T) {
	t.Parallel()

	_, ok := result.ParseOutcome("Refused")

	assert.Check(t, !ok, "ParseOutcome(%q) found an outcome, want none", "Refused")
}

func TestOutcome_String_WhenOutcomeIsOutsideTheTable_ReturnsBroke(t *testing.T) {
	t.Parallel()

	got := result.Outcome(99).String()

	assert.Check(t, is.Equal(got, "broke"))
}

func TestSay(t *testing.T) {
	t.Parallel()

	tests := []pair{
		brokeLine("first name holding a placeholder is not filled by the second",
			result.Say("Moved %s to %s.", "a %s file", "docs"), "Moved a %s file to docs."),
		brokeLine("line name holding line breaks prints on one line",
			result.Say("Kept %s as is.", "one\r\n\nNext: ils land\rtwo"), "Kept one Next: ils land two as is."),
		brokeLine("list of branches holding a line break prints on one line",
			result.Say("Kept %s.", result.Branches([]string{"one\r\ntwo", "main"})), "Kept one two and main."),
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := result.RenderText(tt.result, tt.gitDir)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, tt.wantText))
		})
	}
}

func TestBranches_WhenListHoldsThreeBranches_JoinsThemWithCommasAndAnd(t *testing.T) {
	t.Parallel()
	r := brokeWithLine(result.Say("Kept %s.", result.Branches([]string{"docs", "main", "tidy-readme"})))

	got, err := result.RenderText(r, gitDir)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got, brokeBody+"Kept docs, main and tidy-readme.\n"+brokeEnd()))
}

func TestSay_WhenPlaceholdersAndNamesDiffer_ReturnsErrPlaceholders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		phrase result.Phrase
	}{
		{name: "more names than placeholders", phrase: result.Say("Pushed to origin.", "tidy-readme")},
		{name: "more placeholders than names", phrase: result.Say("Pushed %s to %s.", "tidy-readme")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out, err := result.RenderText(brokeWithLine(tt.phrase), gitDir)

			assert.Check(t, is.ErrorIs(err, result.ErrPlaceholders), "output %q", out)
		})
	}
}

// builtCases returns one case per part of a phrase whose look the building of a name or a phrase
// decides: lists, concatenated names, tallies, the command of a Next and commands the wording
// wraps in backticks.
func builtCases() []styledCase {
	return []styledCase{
		{name: "command", result: refusedPush(), within: result.NextLabel + ": ils branch fix-prek-again", part: "ils branch fix-prek-again", want: resulttest.NameLook(), profile: colorprofile.ANSI},
		{name: "first branch of a list", result: listedBranches(), within: "Kept docs,", part: "docs", want: resulttest.NameLook(), profile: colorprofile.ANSI},
		{name: "comma of a list", result: listedBranches(), within: "docs, main", part: ", ", profile: colorprofile.ANSI},
		{name: "branch of a concatenated name", result: joinedCommand(), within: "Ran git switch main", part: "main", want: resulttest.NameLook(), profile: colorprofile.ANSI},
		{name: "word after a passed count", result: talliedChecks(), within: "17 passed", part: " passed", profile: colorprofile.ANSI},
		{name: "zero passed tally", result: talliedChecks(), within: "then 0 passed", part: "0 passed", want: resulttest.SkippedLook(), profile: colorprofile.ANSI},
		{name: "wording before an inline command", result: tickedPush(), within: "Ran gh pr edit", part: "Ran ", profile: colorprofile.ANSI},
		{name: "wording after an inline command", result: tickedPush(), within: "#16 at 4f2a9c1.", part: " at ", profile: colorprofile.ANSI},
		{name: "sha after an inline command", result: tickedPush(), within: "at 4f2a9c1.", part: "4f2a9c1", want: resulttest.SHALook(), profile: colorprofile.ANSI},
		{
			name:   "lone backtick in wording",
			result: brokeWithLine(result.Say("Kept the ` mark.")), within: "the ` mark", part: "`",
			profile: colorprofile.ANSI,
		},
		{
			name:   "second of two inline commands in a phrase",
			result: brokeWithLine(result.Say("Run `%s` and `%s` by hand.", "git branch -D topic", "git pull --ff-only")),
			within: "and git pull --ff-only by", part: "git pull --ff-only", want: resulttest.NameLook(),
			profile: colorprofile.ANSI,
		},
	}
}

// talliedChecks is a broke push with one more line that tallies passed and skipped checks, each
// once with a count of zero and once without.
func talliedChecks() result.Result {
	return brokeWithLine(result.Say("Checks for %s: %s, %s, then %s, %s.", result.SHA("4f2a9c1"),
		result.Passed(17), result.Skipped(0), result.Passed(0), result.Skipped(3)))
}

// joinedCommand is a broke push with one more line that names a git command whose argument is a
// branch.
func joinedCommand() result.Result {
	return brokeWithLine(result.Say("Ran %s", result.Concat("git switch ", result.Branch("main"))))
}

// listedBranches is a broke push with one more line that names a list of three branches.
func listedBranches() result.Result {
	return brokeWithLine(result.Say("Kept %s.", result.Branches([]string{"docs", "main", "tidy-readme"})))
}

func TestSay_WhenDrawnInColor_DrawsEachBuiltPartInItsStyle(t *testing.T) {
	t.Parallel()

	for _, tt := range builtCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: result %s", inputOf(tt.result))

			assertStyled(t, tt)
		})
	}
}
