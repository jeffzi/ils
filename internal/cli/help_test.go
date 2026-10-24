package cli_test

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

const (
	// rootHelp, pushHelp and mergeHelp are how the help of ils, of push and of merge each start.
	rootHelp  = "ils lands a branch on GitHub in fixed, checked steps"
	pushHelp  = "Push the current branch, open its pull request on the first push"
	mergeHelp = "Squash-merge the pull request of the current branch once its checks are green"

	// mergeSummary is the line that `ils --help` lists merge with.
	mergeSummary = "merge Squash and merge the pull request once its checks are green"
	// mergeUsage is the usage and the examples the plain help of merge ends with, before its flags.
	mergeUsage = "Usage: ils merge [flags] Examples: # Merge once the checks are green ils merge " +
		"# The same result as one JSON object ils merge --json"
)

// flat returns the words of text in order, one space apart, so that a check reads past the padding
// that help lays its columns out with.
func flat(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// shownHelp is what the help of one command shows: phrases its text holds, and the commands and
// flags it lists.
type shownHelp struct {
	phrases  []string
	commands []string
	flags    []string
}

// helpOf returns the help of ils, of push and of merge.
func helpOf() (root, push, merge shownHelp) {
	own := []string{"-d", "-h", "--json", "--no-color"}
	root = shownHelp{
		phrases:  []string{rootHelp, "--version Print the version of ils", mergeSummary},
		commands: []string{"help", "merge", "push"},
		flags:    []string{"-d", "-h", "--json", "--no-color", "--version"},
	}
	push = shownHelp{phrases: []string{pushHelp}, flags: own}
	merge = shownHelp{
		phrases: []string{
			mergeHelp,
			"squash-merges the pull request once its checks are green",
			"deletes the branch",
			"never pushes",
			"leaves the trunk checked out",
			mergeUsage,
		},
		flags: own,
	}

	return root, push, merge
}

func TestApp_Run_WhenAskedForHelp_PrintsTheHelpOfTheCommand(t *testing.T) {
	t.Parallel()

	root, push, merge := helpOf()
	tests := []struct {
		args []string
		want shownHelp
	}{
		{args: []string{"-h"}, want: root},
		{args: []string{"--help"}, want: root},
		{args: []string{"help"}, want: root},
		{args: []string{"pish", "--help"}, want: root},
		{args: []string{"-h", "pish"}, want: root},
		{args: []string{"help", "pish"}, want: root},
		{args: []string{"--json", "-h"}, want: root},
		{args: []string{"--help", "--json"}, want: root},
		{args: []string{"help", "--json"}, want: root},
		{args: []string{"help", "pish", "--json"}, want: root},
		{args: []string{"push", "-h"}, want: push},
		{args: []string{"push", "--help"}, want: push},
		{args: []string{"--help", "push"}, want: push},
		{args: []string{"help", "push"}, want: push},
		{args: []string{"push", "extra", "--help"}, want: push},
		{args: []string{"push", "--json", "-h"}, want: push},
		{args: []string{"push", "-d", "--no-color", "--help"}, want: push},
		{args: []string{"--json", "push", "--help"}, want: push},
		{args: []string{"help", "push", "--json"}, want: push},
		{args: []string{"merge", "-h"}, want: merge},
		{args: []string{"merge", "--help"}, want: merge},
		{args: []string{"help", "merge"}, want: merge},
		{args: []string{"merge", "--json", "--help"}, want: merge},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			assert.Check(t, is.Equal(got.code, exitDone))
			assert.Check(t, is.Equal(got.stderr, ""))
			help := flat(got.stdout)
			for _, want := range tt.want.phrases {
				assert.Check(t, is.Contains(help, want))
			}
			assert.Check(t, is.DeepEqual(entries(got.stdout, "Available Commands:"), tt.want.commands))
			assert.Check(t, is.DeepEqual(entries(got.stdout, "Flags:", "Global Flags:"), tt.want.flags, anyOrder()))
			assert.Check(t, is.Len(listedCalls(got.stdout), 0))
		})
	}
}

// listedCalls returns the lines of help that name a `git` or `gh` command, wherever the name
// stands in the line and however it is marked: in backticks, after a bullet, or bare.
func listedCalls(help string) []string {
	var found []string

	for line := range strings.Lines(help) {
		for word := range strings.FieldsSeq(line) {
			if name := strings.Trim(word, "`*-•:,.;()"); name == "git" || name == "gh" {
				found = append(found, strings.TrimSpace(line))

				break
			}
		}
	}

	return found
}

// listedUnder returns the first word of each line the help lists under heading, with the comma that
// follows a short flag dropped, or nil when the help has no such heading.
func listedUnder(help, heading string) []string {
	_, rest, listed := strings.Cut(help, "\n"+heading+"\n")
	if !listed {
		return nil
	}

	var found []string

	for line := range strings.SplitSeq(rest, "\n") {
		words := strings.Fields(line)
		if len(words) == 0 {
			break
		}

		found = append(found, strings.TrimSuffix(words[0], ","))
	}

	return found
}

// entries returns the first word of each line the help lists under any of headings, each a line of
// its own, with the comma that follows a short flag dropped.
func entries(help string, headings ...string) []string {
	var found []string

	for _, heading := range headings {
		found = append(found, listedUnder(help, heading)...)
	}

	return found
}

func TestApp_Run_WhenAskedForHelpInARepository_CallsNeitherGitNorGh(t *testing.T) {
	tests := [][]string{{"push", "--help"}, {"merge", "--help"}, {"merge", "-h"}, {"help", "merge"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			s := newScene(t, scenario{})

			got := launch(t, s.app(), args...)

			assert.Check(t, is.Equal(got.code, exitDone))
			assert.Check(t, is.Len(s.recorder.Calls(), 0))
			assert.Check(t, is.Len(s.fake.Calls(), 0))
		})
	}
}

func TestApp_Run_WhenHelpIsAskedForWithColorOffOnAColorTerminal_PrintsItWithNoEscapeSequence(t *testing.T) {
	// Fang styles help from the process environment, which forced color would leave styled were Fang
	// to run.
	t.Setenv(forceColor, "1")

	tests := []struct {
		name string
		env  []string
		args []string
	}{
		{name: "flag on the root", env: []string{colorTerm}, args: []string{"--help", "--no-color"}},
		{name: "flag on push", env: []string{colorTerm}, args: []string{"push", "--no-color", "--help"}},
		{name: "flag on merge", env: []string{colorTerm}, args: []string{"merge", "-h", "--no-color=true"}},
		{name: "no color on the root", env: []string{colorTerm, noColor}, args: []string{"--help"}},
		{name: "no color on push", env: []string{colorTerm, noColor}, args: []string{"push", "--help"}},
		{name: "no color on merge", env: []string{colorTerm, noColor}, args: []string{"help", "merge"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := launch(t, painted(t, tt.env, true, true), tt.args...)

			assert.Check(t, is.Equal(got.code, exitDone))
			assert.Check(t, is.Contains(got.stdout, "Usage:"))
			plain(t, "the help", got.stdout)
		})
	}
}

func TestApp_Run_WhenHelpIsAskedForOnAColorTerminal_StylesIt(t *testing.T) {
	// Fang styles help only when the process environment lets it; ils's profile then decides.
	t.Setenv(forceColor, "1")

	tests := [][]string{{"--help"}, {"push", "--help"}, {"merge", "--no-color=false", "--help"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got := launch(t, painted(t, []string{colorTerm}, true, false), args...)

			assert.Check(t, is.Equal(got.code, exitDone))
			assert.Check(t, strings.Contains(got.stdout, escape), "the help carries no escape sequence:\n%q", got.stdout)
		})
	}
}
