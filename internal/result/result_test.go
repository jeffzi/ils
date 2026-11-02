package result_test

import (
	"testing"

	"github.com/charmbracelet/colorprofile"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

func TestSay(t *testing.T) {
	t.Parallel()

	tests := []pair{
		brokeLine("line name holding line breaks prints on one line",
			result.Say("Kept %s as is.", "one\r\n\nNext: ils land\rtwo"), "Kept one Next: ils land two as is."),
		brokeLine("name holding Unicode line and paragraph separators prints on one line",
			result.Say("Kept %s as is.", "one Next: ils land two"), "Kept one Next: ils land two as is."),
		brokeLine("list of branches holding a line break prints on one line",
			result.Say("Kept %s.", result.Branches([]string{"one\r\ntwo", "main"})), "Kept one two and main."),
		brokeLine("list of three branches is joined with commas, then and",
			result.Say("Kept %s.", result.Branches([]string{"docs", "main", "tidy-readme"})), "Kept docs, main and tidy-readme."),
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

// builtCases returns one case per part of a phrase whose look the building of a name decides:
// lists, concatenated names and tallies.
func builtCases() []styledCase {
	return []styledCase{
		{name: "first branch of a list", result: listedBranches(), within: "Kept docs,", part: "docs", want: resulttest.NameLook(), profile: colorprofile.ANSI},
		{name: "comma of a list is drawn unstyled", result: listedBranches(), within: "docs, main", part: ", ", want: resulttest.Look{}, profile: colorprofile.ANSI},
		{name: "branch of a concatenated name", result: joinedCommand(), within: "Ran git switch main", part: "main", want: resulttest.NameLook(), profile: colorprofile.ANSI},
		{name: "word after a passed count is drawn unstyled", result: talliedChecks(), within: "17 passed", part: " passed", want: resulttest.Look{}, profile: colorprofile.ANSI},
		{name: "zero passed tally", result: talliedChecks(), within: "then 0 passed", part: "0 passed", want: resulttest.SkippedLook(), profile: colorprofile.ANSI},
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

	assertLooks(t, builtCases())
}
