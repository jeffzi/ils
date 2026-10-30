package result_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const (
	gitDir   = "/srv/store/meta"
	pullURL  = "https://github.com/jeffzi/ils/pull/12"
	checkURL = "https://github.com/jeffzi/ils/actions/runs/7/job/9"
	excerpt  = "lefthook.yml:3: hooks/ entry is not valid\nexit status 1"

	doneBody  = "Pushed tidy-readme to origin.\nEvery check is green.\n"
	brokeBody = "Pushed tidy-readme to origin.\nDid not open the pull request: HTTP 502\n"
	redBody   = "Pushed fix-prek to origin.\nPull request #12: " + pullURL + "\n" +
		"Latest commit: fix %s parsing in the pre-commit config\n" +
		"Check prek failed: " + checkURL + "\n" + excerpt + "\n"
	redNext = "fix the failing check, commit, and run ils push again"

	controlBytes = "one\x1b[1;33m two\b three\x7f four\u009b"

	fullSHA  = "702f76d1c3b1e9a04f5d2e6b7a8c9d0e1f2a3b4c"
	shortSHA = "702f76d"
	// shaNext is the next step of shaPush; the Next line keeps every sha in full.
	shaNext = "check out " + fullSHA + ", then run ils push again"
)

// redReason is the reason line of redPush.
func redReason() string { return labeled(result.DeliveredNotGreen, "the check prek is red") }

// brokeEnd is the reason and Next lines of a broke push.
func brokeEnd() string { return labeled(result.Broke, "GitHub did not answer") + nextLine("ils push") }

// labeled returns the reason line the text prints for reason, led by the label of outcome.
func labeled(outcome result.Outcome, reason string) string {
	return resulttest.ReasonLine(outcome, reason) + "\n"
}

// nextLine returns the Next line the text prints for next.
func nextLine(next string) string {
	return resulttest.NextLine(next) + "\n"
}

// donePush is a green push with nothing left to run.
func donePush() result.Result {
	return result.Result{
		Command: "push",
		Words:   []string{"push"},
		Outcome: result.Done,
		Lines: []result.Phrase{
			result.Say("Pushed %s to origin.", "tidy-readme"),
			result.Say("Every check is green."),
		},
		Next: result.NextNone(),
	}
}

// brokePush is a push that failed after its first write.
func brokePush() result.Result {
	return result.Result{
		Command: "push",
		Words:   []string{"push"},
		Outcome: result.Broke,
		Lines: []result.Phrase{
			result.Say("Pushed %s to origin.", "tidy-readme"),
			result.Say("Did not open the pull request: %s", "HTTP 502"),
		},
		Next:   result.NextCommand("push"),
		Reason: result.Say("GitHub did not answer"),
	}
}

// brokeLines returns the lines of brokePush as the renderers print them.
func brokeLines() []string {
	return strings.Split(strings.TrimSuffix(brokeBody, "\n"), "\n")
}

// brokeWithLine is brokePush with p as one more line.
func brokeWithLine(p result.Phrase) result.Result {
	return with(brokePush(), func(r *result.Result) { r.Lines = append(r.Lines, p) })
}

// refusedWithReason is refusedPush with p as its reason.
func refusedWithReason(p result.Phrase) result.Result {
	return with(refusedPush(), func(r *result.Result) { r.Reason = p })
}

// redWithNext is redPush with n as its next step.
func redWithNext(n result.Next) result.Result {
	return with(redPush(), func(r *result.Result) { r.Next = n })
}

// refusedPush is a refusal whose reason and Next carry interpolated names.
func refusedPush() result.Result {
	return result.Result{
		Command: "push",
		Words:   []string{"push"},
		Outcome: result.Refused,
		Next:    result.NextCommand("branch %s", "fix-prek-again"),
		Reason:  result.Say("the branch %s already landed with %s", "fix-prek", "lefthook.yml"),
	}
}

// redPush is a delivered push with a red check named after a hook runner and an excerpt.
func redPush() result.Result {
	return result.Result{
		Command: "push",
		Words:   []string{"push"},
		Outcome: result.DeliveredNotGreen,
		Lines: []result.Phrase{
			result.Say("Pushed %s to origin.", "fix-prek"),
			result.Say("Pull request #%s: %s", 12, pullURL),
			result.Say("Latest commit: %s", "fix %s parsing in the pre-commit config"),
			result.Say("Check %s failed: %s", "prek", checkURL),
		},
		Next:    result.NextStep(redNext),
		Reason:  result.Say("the check %s is red", "prek"),
		Excerpt: excerpt,
	}
}

// shaLine is a checks line that names the commit fullSHA.
func shaLine() result.Phrase {
	return result.Say("Checks for %s: %s", result.SHA(fullSHA), result.Passed(9))
}

// shaReason is a reason that names the commit fullSHA.
func shaReason() result.Phrase {
	return result.Say("GitHub did not show %s", result.SHA(fullSHA))
}

// shaStep is a next step that names the commit fullSHA and the push command.
func shaStep() result.Next {
	return result.NextStep("check out %s, then run %s again", result.SHA(fullSHA), result.Command("push"))
}

// shaPush is a broke push whose last line, reason and next step each name the commit fullSHA.
func shaPush() result.Result {
	return with(brokeWithLine(shaLine()), func(r *result.Result) {
		r.Reason = shaReason()
		r.Next = shaStep()
	})
}

// with returns a copy of base that edit has changed, leaving base itself untouched.
func with[T any](base T, edit func(*T)) T {
	edit(&base)

	return base
}

// inputOf prints a row's result for the log of a failing row.
func inputOf(r result.Result) string {
	return fmt.Sprintf("%+v", r)
}

// ---------------------------------------------------------------------------
// Text renderer
// ---------------------------------------------------------------------------

// textCases returns one case per shape the text renderer prints.
func textCases() []pair {
	return []pair{
		{
			name:     "wording wrapping a command in backticks keeps them",
			gitDir:   gitDir,
			result:   tickedPush(),
			wantText: strings.Join(tickedPushLines(), "\n") + "\n" + labeled(result.Broke, tickedReason) + nextLine(tickedNext),
		},
	}
}

// excerptTextCases returns the cases where the excerpt's own text meets the reason line.
func excerptTextCases() []pair {
	return []pair{
		{
			name:     "excerpt ending in a newline gains no blank line",
			gitDir:   gitDir,
			result:   with(brokePush(), func(r *result.Result) { r.Excerpt = "one\ntwo\n" }),
			wantText: brokeBody + "one\ntwo\n" + brokeEnd(),
		},
		{
			name:     "excerpt of only newlines stays above the reason",
			gitDir:   gitDir,
			result:   with(brokePush(), func(r *result.Result) { r.Excerpt = "\n\n\n" }),
			wantText: brokeBody + "\n\n\n" + brokeEnd(),
		},
	}
}

// shaTextCases returns the cases where a commit sha is printed short or in full.
func shaTextCases() []pair {
	return []pair{
		brokeLine("sha in a line prints its first seven characters", shaLine(), "Checks for "+shortSHA+": 9 passed"),
		{
			name:     "sha in the reason prints its first seven characters",
			gitDir:   gitDir,
			result:   with(brokePush(), func(r *result.Result) { r.Reason = shaReason() }),
			wantText: brokeBody + labeled(result.Broke, "GitHub did not show "+shortSHA) + nextLine("ils push"),
		},
		redThen("sha in the Next line prints in full", shaStep(), shaNext),
		brokeLine("sha shorter than seven characters prints as it is",
			result.Say("Checks for %s.", result.SHA("4f2a9")), "Checks for 4f2a9."),
	}
}

// allTextCases returns every case with the plain text it prints.
func allTextCases() []pair {
	return slices.Concat(textCases(), excerptTextCases(), controlCases(), shaTextCases())
}

func TestRenderText(t *testing.T) {
	t.Parallel()

	for _, tt := range allTextCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: git dir %q, result %s", tt.gitDir, inputOf(tt.result))

			got, err := result.RenderText(tt.result, tt.gitDir)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, tt.wantText))
		})
	}
}

// ---------------------------------------------------------------------------
// JSON renderer
// ---------------------------------------------------------------------------

type jsonCase struct {
	// pick keeps the fields of the decoded object the row is about.
	pick   func(s resulttest.Shown) resulttest.Shown
	name   string
	gitDir string
	want   resulttest.Shown
	result result.Result
}

// linesReasonNext keeps the lines, the reason and the next step of a decoded object.
func linesReasonNext(s resulttest.Shown) resulttest.Shown {
	return resulttest.Shown{Lines: s.Lines, Reason: s.Reason, Next: s.Next}
}

// jsonCases returns the cases whose text the JSON renderer carries as it was handed in.
func jsonCases() []jsonCase {
	return []jsonCase{
		{
			name:   "wording wrapping a command in backticks keeps them",
			gitDir: gitDir,
			result: tickedPush(),
			pick:   linesReasonNext,
			want:   resulttest.Shown{Lines: tickedPushLines(), Reason: tickedReason, Next: tickedNext},
		},
		{
			name:   "name holding an escape sequence carries the escape raw",
			gitDir: gitDir,
			result: brokeWithLine(result.Say("Kept %s as is.", "x\x1b[31m")),
			pick:   func(s resulttest.Shown) resulttest.Shown { return resulttest.Shown{Lines: s.Lines} },
			want:   resulttest.Shown{Lines: append(brokeLines(), "Kept x\x1b[31m as is.")},
		},
		{
			name:   "excerpt holding control bytes carries them raw",
			gitDir: gitDir,
			result: with(donePush(), func(r *result.Result) { r.Excerpt = controlBytes }),
			pick:   func(s resulttest.Shown) resulttest.Shown { return resulttest.Shown{Excerpt: s.Excerpt} },
			want:   resulttest.Shown{Excerpt: controlBytes},
		},
		{
			name:   "sha in a line, the reason and the next carries in full",
			gitDir: gitDir,
			result: shaPush(),
			pick:   linesReasonNext,
			want: resulttest.Shown{
				Lines:  append(brokeLines(), "Checks for "+fullSHA+": 9 passed"),
				Reason: "GitHub did not show " + fullSHA,
				Next:   shaNext,
			},
		},
		{
			name:   "done carries none as its next",
			gitDir: gitDir,
			result: donePush(),
			pick:   func(s resulttest.Shown) resulttest.Shown { return resulttest.Shown{Next: s.Next} },
			want:   resulttest.Shown{Next: "none"},
		},
	}
}

func TestRenderJSON(t *testing.T) {
	t.Parallel()

	for _, tt := range jsonCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: git dir %q, result %s", tt.gitDir, inputOf(tt.result))

			out, err := result.RenderJSON(tt.result, tt.gitDir)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(tt.pick(resulttest.Decode(t, out)), tt.want))
		})
	}
}

func TestRenderJSON_WhenResultHasLinesExcerptReasonAndNext_CarriesWhatTheTextShows(t *testing.T) {
	t.Parallel()
	text, err := result.RenderText(redPush(), gitDir)
	assert.NilError(t, err)

	out, err := result.RenderJSON(redPush(), gitDir)
	assert.NilError(t, err)

	resulttest.AssertCarried(t, text, resulttest.Decode(t, out))
}

// ---------------------------------------------------------------------------
// Shared pair cases
// ---------------------------------------------------------------------------

type pair struct {
	name     string
	gitDir   string
	wantText string
	result   result.Result
}

// brokeLine is a broke push with one more line, printed as want.
func brokeLine(name string, p result.Phrase, want string) pair {
	return pair{
		name:     name,
		gitDir:   gitDir,
		wantText: brokeBody + want + "\n" + brokeEnd(),
		result:   brokeWithLine(p),
	}
}

// refusedFor is a refusal whose reason is p, printed as want.
func refusedFor(name string, p result.Phrase, want string) pair {
	return pair{
		name:     name,
		gitDir:   gitDir,
		wantText: labeled(result.Refused, want) + nextLine("ils branch fix-prek-again"),
		result:   refusedWithReason(p),
	}
}

// redThen is a red push whose Next is n, printed as want.
func redThen(name string, n result.Next, want string) pair {
	return pair{
		name:     name,
		gitDir:   gitDir,
		wantText: redBody + redReason() + nextLine(want),
		result:   redWithNext(n),
	}
}

// controlCases returns results whose third-party text holds control bytes, which the text prints as
// visible escapes.
func controlCases() []pair {
	return []pair{
		brokeLine("name holding an escape sequence prints the escape as visible text",
			result.Say("Kept %s as is.", "x\x1b[31m"), `Kept x\x1b[31m as is.`),
		brokeLine("name holding a tab prints it as it is",
			result.Say("Kept %s as is.", "x\ty"), "Kept x\ty as is."),
		brokeLine("name holding invalid UTF-8 beside a control byte prints its bytes as they are",
			result.Say("Kept %s as is.", "x\x1b\xff more"), `Kept x\x1b`+"\xff more as is."),
		refusedFor("reason name holding an escape sequence prints the escape as visible text",
			result.Say("the file %s is in the way", "x\x1b[31m"), `the file x\x1b[31m is in the way`),
		redThen("command name holding a cursor sequence cannot erase the Next line",
			result.NextCommand("branch %s", "x\x1b[2K\x1b[1Gy"), `ils branch x\x1b[2K\x1b[1Gy`),
		{
			name:     "excerpt holding control bytes prints them as visible text",
			gitDir:   gitDir,
			wantText: doneBody + `one\x1b[1;33m two\x08 three\x7f four\u009b` + "\n",
			result:   with(donePush(), func(r *result.Result) { r.Excerpt = controlBytes }),
		},
	}
}

// ---------------------------------------------------------------------------
// Malformed results
// ---------------------------------------------------------------------------

func TestRenderText_WhenOutcomeIsNotDoneAndReasonIsEmpty_ReturnsErrNoReason(t *testing.T) {
	t.Parallel()
	r := with(brokePush(), func(r *result.Result) { r.Reason = result.Phrase{} })

	out, err := result.RenderText(r, gitDir)

	assert.Check(t, is.ErrorIs(err, result.ErrNoReason), "output %q", out)
}

// ---------------------------------------------------------------------------
// Styled renderer
// ---------------------------------------------------------------------------

func TestRenderStyled_WhenProfileIsBelowANSI_PrintsThePlainText(t *testing.T) {
	t.Parallel()
	want := "Pushed tidy-readme at 4f2a9c1 to origin.\nPull request #12: " + pullURL + "\nChecks: 3 passed, 0 skipped.\n" +
		labeled(result.Broke, "GitHub did not merge #12 into main") + nextLine("rebase tidy-readme, then run ils push again")

	got, err := result.RenderStyled(markedPush(), gitDir, colorprofile.ASCII)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got, want))
}

// markedPush is a broke push whose lines, reason and next step mark each name with its kind.
func markedPush() result.Result {
	return result.Result{
		Command: "push",
		Words:   []string{"push"},
		Outcome: result.Broke,
		Lines: []result.Phrase{
			result.Say("Pushed %s at %s to origin.", result.Branch("tidy-readme"), result.SHA("4f2a9c1")),
			result.Say("Pull request %s: %s", result.PullRequest(12), result.URL(pullURL)),
			result.Say("Checks: %s, %s.", result.Passed(3), result.Skipped(0)),
		},
		Reason: result.Say("GitHub did not merge %s into %s", result.PullRequest(12), result.Branch("main")),
		Next:   result.NextStep("rebase %s, then run %s again", result.Branch("tidy-readme"), result.Command("push")),
	}
}

type styledCase struct {
	name    string
	within  string
	part    string
	want    resulttest.Look
	result  result.Result
	profile colorprofile.Profile
}

// styledCases returns one case per part of a result the styled renderer draws in its own style.
func styledCases() []styledCase {
	return []styledCase{
		{
			name: "branch is drawn as a name on a true color profile", result: markedPush(), within: "Pushed tidy-readme",
			part: "tidy-readme", want: resulttest.NameLook(), profile: colorprofile.TrueColor,
		},
		{name: "step wording is drawn unstyled", result: markedPush(), within: resulttest.NextLine("rebase"), part: " rebase", want: resulttest.Look{}, profile: colorprofile.ANSI},
		{name: "passed count is drawn green", result: talliedChecks(), within: "17 passed", part: "17", want: resulttest.PassedLook(), profile: colorprofile.ANSI},
		{name: "command is drawn as a name", result: refusedPush(), within: resulttest.NextLine("ils branch fix-prek-again"), part: "ils branch fix-prek-again", want: resulttest.NameLook(), profile: colorprofile.ANSI},
	}
}

const (
	// tickedReason and tickedNext are the reason and next step of tickedPush as a plain profile
	// prints them.
	tickedReason = "`gh pr edit #16` failed"
	tickedNext   = "run `gh pr ready 16`, then run ils push again"
)

// tickedPush is a broke push whose lines, reason and next step wrap commands in backticks.
func tickedPush() result.Result {
	return with(brokePush(), func(r *result.Result) {
		r.Lines = append(r.Lines,
			result.Say("Ran `gh pr edit --repo %s %s` at %s.", "jeffzi/ils", result.PullRequest(16), result.SHA("4f2a9c1")))
		r.Reason = result.Say("`gh pr edit %s` failed", result.PullRequest(16))
		r.Next = result.NextStep("run `gh pr ready %s`, then run %s again", result.PullRequestNumber(16), result.Command("push"))
	})
}

// tickedPushLines returns the lines of tickedPush as a plain profile prints them.
func tickedPushLines() []string {
	return append(brokeLines(), "Ran `gh pr edit --repo jeffzi/ils #16` at 4f2a9c1.")
}

func TestRenderStyled_WhenProfileHasColor_DrawsEachPartInItsStyle(t *testing.T) {
	t.Parallel()

	assertLooks(t, styledCases())
}

// assertLooks checks, case by case, that each part is drawn in the wanted look.
func assertLooks(t *testing.T, cases []styledCase) {
	t.Helper()

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: result %s", inputOf(tt.result))

			got, out := drawnLook(t, tt)

			assert.Check(t, is.Equal(got, tt.want), "output %q", out)
		})
	}
}

// drawnLook renders the row's result at its profile and returns how part is drawn within its
// surrounding text, with the rendered output for the failure message.
func drawnLook(t *testing.T, tt styledCase) (resulttest.Look, string) {
	t.Helper()

	out, err := result.RenderStyled(tt.result, gitDir, tt.profile)
	assert.NilError(t, err)

	return resulttest.Draw(t, out).LookOf(t, tt.within, tt.part), out
}

// ---------------------------------------------------------------------------
// Wording check
// ---------------------------------------------------------------------------

// rejected is a result whose own wording names a hook or a git dir path. wantLine is set only
// where the line holds a name filled in, the one case where the line differs from the wording.
type rejected struct {
	name     string
	gitDir   string
	wantLine string
	wantWord string
	result   result.Result
}

// rejectedLine is a broke push with one more line, whose wording must be
// rejected against the resolved git dir.
func rejectedLine(name, dir string, p result.Phrase) rejected {
	return rejected{
		name:   name,
		gitDir: dir,
		result: brokeWithLine(p),
	}
}

// naming returns r with word as the word that must break the rule.
func (r rejected) naming(word string) rejected {
	r.wantWord = word

	return r
}

// dirCases returns results whose wording names the resolved git dir in a
// spelling other than the one handed in. The word that breaks the rule is the
// git dir as it was handed in.
func dirCases() []rejected {
	const (
		windowsDir = `C:\Work\Store\meta`
		spacedDir  = "/srv/my store/meta dir"
		shoutedDir = "/SRV/Store/Meta"
	)

	return []rejected{
		rejectedLine("handed-in dir ends in a separator", gitDir+"/",
			result.Say("Wrote /srv/store/meta/HEAD")).naming(gitDir + "/"),
		rejectedLine("wording uses backslashes", gitDir,
			result.Say(`Wrote \srv\store\meta\HEAD`)).naming(gitDir),
		rejectedLine("handed-in dir uses backslashes", windowsDir,
			result.Say("Wrote C:/Work/Store/meta/HEAD")).naming(windowsDir),
		rejectedLine("handed-in dir uses backslashes and ends in one", windowsDir+`\`,
			result.Say("Wrote C:/Work/Store/meta/HEAD")).naming(windowsDir + `\`),
		rejectedLine("wording differs in case", gitDir,
			result.Say("Wrote /SRV/Store/Meta/HEAD")).naming(gitDir),
		rejectedLine("handed-in dir differs in case", shoutedDir,
			result.Say("Wrote /srv/store/meta/HEAD")).naming(shoutedDir),
		rejectedLine("resolved git dir holding spaces", spacedDir,
			result.Say("Wrote /srv/my store/meta dir/HEAD")).naming(spacedDir),
		rejectedLine("wording naming a sibling before the git dir", gitDir,
			result.Say("Wrote /srv/store/metadata and /srv/store/meta/HEAD")).naming(gitDir),
	}
}

// lineCases returns results whose offending wording sits in a line.
func lineCases() []rejected {
	line := func(name, wantWord string, p result.Phrase) rejected {
		return rejectedLine(name, gitDir, p).naming(wantWord)
	}

	return []rejected{
		line("git dir component in backticks", "`.git/config`", result.Say("Wrote `.git/config` today.")),
		line("pre-commit, capital", "Pre-commit", result.Say("Pre-commit ran.")),
		line("git dir component, backslash", `.git\config`, result.Say(`Wrote .git\config`)),
		line("git dir component, nested", "work/.git/index.lock", result.Say("Removed work/.git/index.lock")),
		line("git dir component ending a sentence", ".git.", result.Say("Nothing changed in .git.")),
	}
}

// otherCases returns results whose offending wording sits outside the lines.
func otherCases() []rejected {
	return []rejected{
		{
			name: "reason naming a hook", gitDir: gitDir,
			wantLine: "the pre-push step failed on main", wantWord: "pre-push",
			result: refusedWithReason(result.Say("the pre-push step failed on %s", "main")),
		},
		{
			name: "judgment step naming a hook", gitDir: gitDir, wantWord: "prek,",
			result: redWithNext(result.NextStep("run prek, then ils push again")),
		},
	}
}

// exemptCases returns results whose own wording comes close to a hook or a git dir path without
// naming one, which the wording check must let through unchanged.
func exemptCases() []pair {
	return []pair{
		brokeLine("wording with a repository suffix that is no path component",
			result.Say("Fetched from ils.git today."), "Fetched from ils.git today."),
		brokeLine("wording naming a sibling of the resolved git dir",
			result.Say("Wrote /srv/store/metadata/HEAD"), "Wrote /srv/store/metadata/HEAD"),
		with(brokeLine("empty git dir matches nothing",
			result.Say("Wrote /srv/store/meta/HEAD"), "Wrote /srv/store/meta/HEAD"),
			func(c *pair) { c.gitDir = "" }),
	}
}

func TestRenderText_WhenOwnWordingNamesHookOrGitDir_ReturnsWordingError(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(lineCases(), dirCases(), otherCases()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: git dir %q, result %s", tt.gitDir, inputOf(tt.result))

			out, err := result.RenderText(tt.result, tt.gitDir)

			wordingErr, ok := errors.AsType[*result.WordingError](err)
			assert.Assert(t, ok, "RenderText error = %v, want a *result.WordingError; output %q", err, out)
			if tt.wantLine != "" {
				assert.Check(t, is.Equal(wordingErr.Line, tt.wantLine))
			}
			assert.Check(t, is.Equal(wordingErr.Word, tt.wantWord))
		})
	}
}

func TestRenderText_WhenOwnWordingOnlyResemblesHookOrGitDir_PrintsIt(t *testing.T) {
	t.Parallel()

	for _, tt := range exemptCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: git dir %q, result %s", tt.gitDir, inputOf(tt.result))

			got, err := result.RenderText(tt.result, tt.gitDir)
			assert.NilError(t, err)

			assert.Check(t, is.Equal(got, tt.wantText))
		})
	}
}

func TestWordingError_Error_WhenBothFieldsSet_NamesBothFields(t *testing.T) {
	t.Parallel()
	err := &result.WordingError{Line: "The step ran.", Word: "pre-commit"}

	got := err.Error()

	assert.Check(t, is.Contains(got, err.Line))
	assert.Check(t, is.Contains(got, err.Word))
}
