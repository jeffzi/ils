package result_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result"
)

const (
	gitDir   = "/srv/store/meta"
	pullURL  = "https://github.com/jeffzi/ils/pull/12"
	checkURL = "https://github.com/jeffzi/ils/actions/runs/7/job/9"
	excerpt  = "lefthook.yml:3: hooks/ entry is not valid\nexit status 1"
	wholeLog = "Run prek run --all-files\nreading .git/hooks/pre-commit\n" + excerpt + "\n"

	doneBody  = "Pushed tidy-readme to origin.\nEvery check is green.\n"
	brokeBody = "Pushed tidy-readme to origin.\nDid not open the pull request: HTTP 502\n"
	redBody   = "Pushed fix-prek to origin.\nPull request #12: " + pullURL + "\n" +
		"Latest commit: fix %s parsing in the pre-commit config\n" +
		"Check prek failed: " + checkURL + "\n" + excerpt + "\n"
	redNext = "fix the failing check, commit, and run ils push again"
)

// shown is the JSON renderer's object as a caller decodes it.
type shown struct {
	Payload  *result.Payload `json:"payload"`
	Command  string          `json:"command"`
	Outcome  string          `json:"outcome"`
	Next     string          `json:"next"`
	Reason   string          `json:"reason"`
	Excerpt  string          `json:"excerpt"`
	Words    []string        `json:"words"`
	Lines    []string        `json:"lines"`
	ExitCode int             `json:"exit_code"`
}

// ofPush returns s as the push command shows it.
func ofPush(s shown) shown {
	s.Command = "push"
	s.Words = []string{"push"}

	return s
}

// doneShown is the object the JSON renderer shows for donePush.
func doneShown() shown {
	return ofPush(shown{
		Outcome: "done", ExitCode: 0, Next: "none",
		Lines: []string{"Pushed tidy-readme to origin.", "Every check is green."},
	})
}

// brokeShown is the object the JSON renderer shows for brokePush.
func brokeShown() shown {
	return ofPush(shown{
		Outcome: "broke", ExitCode: 1, Next: "ils push", Reason: "GitHub did not answer",
		Lines: []string{"Pushed tidy-readme to origin.", "Did not open the pull request: HTTP 502"},
	})
}

// refusedShown is the object the JSON renderer shows for refusedPush.
func refusedShown() shown {
	return ofPush(shown{
		Outcome: "refused", ExitCode: 2, Next: "ils branch fix-prek-again",
		Reason: "the branch fix-prek already landed with lefthook.yml",
	})
}

// redShown is the object the JSON renderer shows for redPush.
func redShown() shown {
	return ofPush(shown{
		Outcome: "delivered-not-green", ExitCode: 5, Next: redNext,
		Reason: "the check prek is red", Excerpt: excerpt, Payload: redPush().Payload,
		Lines: []string{
			"Pushed fix-prek to origin.", "Pull request #12: " + pullURL,
			"Latest commit: fix %s parsing in the pre-commit config", "Check prek failed: " + checkURL,
		},
	})
}

// renderer is one of the package's two renderers.
type renderer struct {
	render func(result.Result, string) (string, error)
	name   string
}

// renderers lists both renderers, for behaviors the two must share.
func renderers() []renderer {
	return []renderer{
		{name: "text", render: result.RenderText},
		{name: "json", render: result.RenderJSON},
	}
}

// fixture is a named result, one per outcome.
type fixture struct {
	name   string
	result result.Result
}

// fixtures returns one valid result for every outcome a command returns.
func fixtures() []fixture {
	return []fixture{
		{name: "done", result: donePush()},
		{name: "broke", result: brokePush()},
		{name: "refused", result: refusedPush()},
		{name: "pending", result: pendingPush()},
		{name: "delivered, not green", result: redPush()},
	}
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

// pendingPush is a push whose checks were still running at the bound.
func pendingPush() result.Result {
	return result.Result{
		Command: "push",
		Words:   []string{"push"},
		Outcome: result.Pending,
		Lines: []result.Phrase{
			result.Say("Pushed %s to origin.", "tidy-readme"),
			result.Say("Still running after %s seconds: %s", 100, "build"),
		},
		Next:   result.NextCommand("push"),
		Reason: result.Say("the checks were still running at the bound"),
	}
}

// redPush is a delivered push with a red check named after a hook runner,
// an excerpt, and the full payload.
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
		Next:    result.NextStep("fix the failing check, commit, and run ils push again"),
		Reason:  result.Say("the check %s is red", "prek"),
		Excerpt: excerpt,
		Payload: &result.Payload{Push: &result.Push{
			Number:     12,
			URL:        pullURL,
			SHA:        "4f2a9c1",
			Verdict:    "red",
			MergeState: "blocked-other",
			Checks: []result.Check{
				{Name: "prek", Bucket: "fail", Workflow: "CI", Link: checkURL},
				{Name: "build", Bucket: "pass", Workflow: "CI", Link: checkURL + "0"},
			},
			FailedLog: wholeLog,
		}},
	}
}

// with returns base after edit has changed it.
func with[T any](base T, edit func(*T)) T {
	edit(&base)

	return base
}

// inputOf prints a row's result for the log of a failing row, with the push payload that the
// result holds behind pointers written out.
func inputOf(r result.Result) string {
	text := fmt.Sprintf("%+v", r)
	if r.Payload != nil && r.Payload.Push != nil {
		text += fmt.Sprintf(", its payload push %+v", *r.Payload.Push)
	}

	return text
}

// decode parses the JSON renderer's output, which must be exactly one object.
func decode(t *testing.T, out string) shown {
	t.Helper()

	var got shown
	assert.NilError(t, json.Unmarshal([]byte(out), &got), "RenderJSON output is not one JSON object:\n%s", out)

	return got
}

// textCase pairs a result with the text the text renderer must print for it.
type textCase struct {
	name   string
	want   string
	result result.Result
}

// textCases returns one case per shape the text renderer prints.
func textCases() []textCase {
	return []textCase{
		{name: "done prints the lines then Next none", result: donePush(), want: doneBody + "Next: none\n"},
		{
			name:   "broke prints what was done and what was not",
			result: brokePush(),
			want:   brokeBody + "Next: ils push\n",
		},
		{
			name:   "refused prints the reason first",
			result: refusedPush(),
			want: "Refused: the branch fix-prek already landed with lefthook.yml\n" +
				"Next: ils branch fix-prek-again\n",
		},
		{
			name: "refused prints the reason before its lines and excerpt",
			result: with(refusedPush(), func(r *result.Result) {
				r.Lines = []result.Phrase{result.Say("Fetched origin.")}
				r.Excerpt = "one\ntwo"
			}),
			want: "Refused: the branch fix-prek already landed with lefthook.yml\n" +
				"Fetched origin.\none\ntwo\nNext: ils branch fix-prek-again\n",
		},
		{
			name:   "pending names the same command again",
			result: pendingPush(),
			want: "Pushed tidy-readme to origin.\nStill running after 100 seconds: build\n" +
				"Next: ils push\n",
		},
		{
			name:   "excerpt sits verbatim between the lines and Next",
			result: redPush(),
			want:   redBody + "Next: " + redNext + "\n",
		},
		{
			name:   "excerpt ending in a newline gains no blank line",
			result: with(donePush(), func(r *result.Result) { r.Excerpt = "one\ntwo\n" }),
			want:   doneBody + "one\ntwo\nNext: none\n",
		},
		{
			name:   "excerpt holding a Next line stays verbatim above the real one",
			result: with(donePush(), func(r *result.Result) { r.Excerpt = "Next: ils land\nmore" }),
			want:   doneBody + "Next: ils land\nmore\nNext: none\n",
		},
		{
			name:   "excerpt of only newlines stays above Next",
			result: with(donePush(), func(r *result.Result) { r.Excerpt = "\n\n\n" }),
			want:   doneBody + "\n\n\nNext: none\n",
		},
		{
			name: "excerpt of a result with no lines sits alone above Next",
			result: with(donePush(), func(r *result.Result) {
				r.Lines = nil
				r.Excerpt = "one\ntwo"
			}),
			want: "one\ntwo\nNext: none\n",
		},
	}
}

func TestRenderText(t *testing.T) {
	t.Parallel()

	for _, tt := range textCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: result %s", inputOf(tt.result))

			got, err := result.RenderText(tt.result, gitDir)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, tt.want))
		})
	}
}

// jsonCase pairs a result with the object the JSON renderer must show for it.
type jsonCase struct {
	name   string
	want   shown
	result result.Result
}

// jsonCases returns one case per outcome the JSON renderer shows.
func jsonCases() []jsonCase {
	return []jsonCase{
		{name: "done carries no reason, excerpt, or payload", result: donePush(), want: doneShown()},
		{name: "broke", result: brokePush(), want: brokeShown()},
		{name: "refused", result: refusedPush(), want: refusedShown()},
		{
			name:   "pending",
			result: pendingPush(),
			want: ofPush(shown{
				Outcome: "pending", ExitCode: 4, Next: "ils push",
				Reason: "the checks were still running at the bound",
				Lines:  []string{"Pushed tidy-readme to origin.", "Still running after 100 seconds: build"},
			}),
		},
		{
			name:   "delivered, not green carries the excerpt and the whole failed log",
			result: redPush(),
			want:   redShown(),
		},
		{
			name: "a command of several words carries its name and each word",
			result: with(donePush(), func(r *result.Result) {
				r.Command = "land"
				r.Words = []string{"stack", "land"}
			}),
			want: with(doneShown(), func(s *shown) {
				s.Command = "land"
				s.Words = []string{"stack", "land"}
			}),
		},
	}
}

// Golden strings for the JSON renderer's exact bytes.
const (
	redGolden = `{"payload":{"push":{"url":"https://github.com/jeffzi/ils/pull/12","sha":"4f2a9c1",` +
		`"verdict":"red","merge_state":"blocked-other",` +
		`"failed_log":"Run prek run --all-files\nreading .git/hooks/pre-commit\n` +
		`lefthook.yml:3: hooks/ entry is not valid\nexit status 1\n",` +
		`"checks":[{"name":"prek","bucket":"fail","workflow":"CI",` +
		`"link":"https://github.com/jeffzi/ils/actions/runs/7/job/9"},` +
		`{"name":"build","bucket":"pass","workflow":"CI",` +
		`"link":"https://github.com/jeffzi/ils/actions/runs/7/job/90"}],"number":12}},` +
		`"command":"push","words":["push"],"outcome":"delivered-not-green",` +
		`"next":"fix the failing check, commit, and run ils push again",` +
		`"reason":"the check prek is red",` +
		`"excerpt":"lefthook.yml:3: hooks/ entry is not valid\nexit status 1",` +
		`"lines":["Pushed fix-prek to origin.","Pull request #12: https://github.com/jeffzi/ils/pull/12",` +
		`"Latest commit: fix %s parsing in the pre-commit config",` +
		`"Check prek failed: https://github.com/jeffzi/ils/actions/runs/7/job/9"],"exit_code":5}` + "\n"
	doneGolden = `{"command":"push","words":["push"],"outcome":"done","next":"none",` +
		`"lines":["Pushed tidy-readme to origin.","Every check is green."],"exit_code":0}` + "\n"
)

func TestRenderJSON_PrintsOneObjectOnOneLineWithExactKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		want   string
		result result.Result
	}{
		{name: "red push carries every field", result: redPush(), want: redGolden},
		{name: "bare done leaves unset fields out", result: donePush(), want: doneGolden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: result %s", inputOf(tt.result))

			got, err := result.RenderJSON(tt.result, gitDir)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, tt.want))
		})
	}
}

// largeLogPush is a red push whose failed log is 1 MiB of one repeated line.
func largeLogPush() (result.Result, string) {
	const size = 1 << 20
	line := "reading the build cache for the next step\n"
	log := strings.Repeat(line, size/len(line)+1)[:size]

	return with(redPush(), func(r *result.Result) { r.Payload.Push.FailedLog = log }), log
}

func TestRenderJSON_WhenFailedLogIsLarge_CarriesItWhole(t *testing.T) {
	t.Parallel()
	pushed, log := largeLogPush()

	out, err := result.RenderJSON(pushed, gitDir)
	assert.NilError(t, err)

	got := decode(t, out).Payload.Push.FailedLog
	assert.Check(t, got == log,
		"RenderJSON() failed log has %d bytes and differs, want the %d bytes handed in", len(got), len(log))
}

func TestRenderText_WhenFailedLogIsLarge_LeavesItOut(t *testing.T) {
	t.Parallel()
	pushed, _ := largeLogPush()

	got, err := result.RenderText(pushed, gitDir)
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(got, redBody+"Next: "+redNext+"\n"))
}

func TestRenderJSON(t *testing.T) {
	t.Parallel()

	for _, tt := range jsonCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: result %s", inputOf(tt.result))

			out, err := result.RenderJSON(tt.result, gitDir)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(decode(t, out), tt.want))
		})
	}
}

func TestRender_ForEveryOutcome_TextLinesAppearInJSON(t *testing.T) {
	t.Parallel()

	for _, tt := range fixtures() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: result %s", inputOf(tt.result))

			text, textErr := result.RenderText(tt.result, gitDir)
			out, jsonErr := result.RenderJSON(tt.result, gitDir)
			assert.NilError(t, errors.Join(textErr, jsonErr))

			got := decode(t, out)
			carried := append(strings.Split(got.Excerpt, "\n"), got.Lines...)
			carried = append(carried, "Next: "+got.Next, "Refused: "+got.Reason)
			for line := range strings.Lines(text) {
				line = strings.TrimSuffix(line, "\n")
				assert.Check(t, is.Contains(carried, line), "JSON:\n%s", out)
			}
		})
	}
}

// rejected is a result whose own wording names a hook or a git dir path.
type rejected struct {
	name     string
	gitDir   string
	wantLine string
	result   result.Result
}

// rejectedLine is a broke push with one more line, whose wording must be
// rejected against the resolved git dir dir.
func rejectedLine(name, dir string, p result.Phrase, wantLine string) rejected {
	return rejected{
		name:     name,
		gitDir:   dir,
		wantLine: wantLine,
		result:   with(brokePush(), func(r *result.Result) { r.Lines = append(r.Lines, p) }),
	}
}

// dirCases returns results whose wording names the resolved git dir in a
// spelling other than the one handed in.
func dirCases() []rejected {
	const windowsDir = `C:\Work\Store\meta`

	return []rejected{
		rejectedLine("handed-in dir ends in a separator", gitDir+"/",
			result.Say("Wrote /srv/store/meta/HEAD"), "Wrote /srv/store/meta/HEAD"),
		rejectedLine("handed-in dir ends in a separator, wording names it bare", gitDir+"/",
			result.Say("Moved /srv/store/meta aside."), "Moved /srv/store/meta aside."),
		rejectedLine("wording ends in a separator", gitDir,
			result.Say("Emptied /srv/store/meta/ today."), "Emptied /srv/store/meta/ today."),
		rejectedLine("wording uses backslashes", gitDir,
			result.Say(`Wrote \srv\store\meta\HEAD`), `Wrote \srv\store\meta\HEAD`),
		rejectedLine("handed-in dir uses backslashes", windowsDir,
			result.Say("Wrote C:/Work/Store/meta/HEAD"), "Wrote C:/Work/Store/meta/HEAD"),
		rejectedLine("handed-in dir uses backslashes and ends in one", windowsDir+`\`,
			result.Say("Wrote C:/Work/Store/meta/HEAD"), "Wrote C:/Work/Store/meta/HEAD"),
		rejectedLine("wording differs in case", gitDir,
			result.Say("Wrote /SRV/Store/Meta/HEAD"), "Wrote /SRV/Store/Meta/HEAD"),
		rejectedLine("handed-in dir differs in case", "/SRV/Store/Meta",
			result.Say("Wrote /srv/store/meta/HEAD"), "Wrote /srv/store/meta/HEAD"),
		rejectedLine("resolved git dir holding spaces", "/srv/my store/meta dir",
			result.Say("Wrote /srv/my store/meta dir/HEAD"), "Wrote /srv/my store/meta dir/HEAD"),
	}
}

// lineCases returns results whose offending wording sits in a line.
func lineCases() []rejected {
	line := func(name, wantLine string, p result.Phrase) rejected {
		return rejectedLine(name, gitDir, p, wantLine)
	}

	return []rejected{
		line("git dir component in backticks", "Wrote `.git/config` today.", result.Say("Wrote `.git/config` today.")),
		line("git dir component in quotes", "Wrote '.git/config' today.", result.Say("Wrote '.git/config' today.")),
		line("git dir component in parentheses", "Wrote (.git/config) today.", result.Say("Wrote (.git/config) today.")),
		line("git dir component after a flag", "Ran with --git-dir=.git/x today.",
			result.Say("Ran with --git-dir=.git/x today.")),
		line("pre-commit, capital", "Pre-commit ran.", result.Say("Pre-commit ran.")),
		line("prek, capital", "Prek ran.", result.Say("Prek ran.")),
		line("lefthook, capital", "Lefthook ran.", result.Say("Lefthook ran.")),
		line("hooks directory, capital", "Hooks/ skipped.", result.Say("Hooks/ skipped.")),
		line("git dir component, slash", "Wrote .git/config", result.Say("Wrote .git/config")),
		line("git dir component, backslash", `Wrote .git\config`, result.Say(`Wrote .git\config`)),
		line("git dir component, nested", "Removed work/.git/index.lock", result.Say("Removed work/.git/index.lock")),
		line("git dir alone", "Nothing in .git changed.", result.Say("Nothing in .git changed.")),
		line("resolved git dir", "Wrote /srv/store/meta/HEAD", result.Say("Wrote /srv/store/meta/HEAD")),
		line("pre-commit", "The pre-commit step ran.", result.Say("The pre-commit step ran.")),
		line("commit-msg", "The commit-msg step ran.", result.Say("The commit-msg step ran.")),
		line("pre-push", "The pre-push step ran.", result.Say("The pre-push step ran.")),
		line("post-commit", "The post-commit step ran.", result.Say("The post-commit step ran.")),
		line("prek", "Ran prek on main.", result.Say("Ran prek on %s.", "main")),
		line("lefthook", "Ran lefthook.", result.Say("Ran lefthook.")),
		line("hooks directory", "Skipped hooks/ entirely.", result.Say("Skipped hooks/ entirely.")),
	}
}

// otherCases returns results whose offending wording sits outside the lines.
func otherCases() []rejected {
	reason := func(name, wantLine string, p result.Phrase) rejected {
		return rejected{
			name: name, gitDir: gitDir, wantLine: wantLine,
			result: with(refusedPush(), func(r *result.Result) { r.Reason = p }),
		}
	}
	next := func(name, wantLine string, n result.Next) rejected {
		return rejected{
			name: name, gitDir: gitDir, wantLine: wantLine,
			result: with(redPush(), func(r *result.Result) { r.Next = n }),
		}
	}

	return []rejected{
		reason("reason naming a hook", "the pre-push step failed on main",
			result.Say("the pre-push step failed on %s", "main")),
		reason("reason naming the resolved git dir", "cannot write /srv/store/meta/HEAD",
			result.Say("cannot write /srv/store/meta/HEAD")),
		next("judgment step naming a hook", "run prek, then ils push again",
			result.NextStep("run prek, then ils push again")),
		next("judgment step naming a git dir path", "remove .git/index.lock, then run ils push again",
			result.NextStep("remove .git/index.lock, then run ils push again")),
		next("command naming a hook", "ils push hooks/", result.NextCommand("push hooks/")),
		next("command naming a git dir path", "ils push .git/config", result.NextCommand("push .git/config")),
		next("command naming the resolved git dir", "ils push /srv/store/meta",
			result.NextCommand("push /srv/store/meta")),
	}
}

func TestRender_WhenOwnWordingNamesHookOrGitDir_ReturnsErrorNamingLine(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(lineCases(), dirCases(), otherCases()) {
		for _, r := range renderers() {
			t.Run(tt.name+"/"+r.name, func(t *testing.T) {
				t.Parallel()
				t.Logf("row: git dir %q, result %s", tt.gitDir, inputOf(tt.result))

				out, err := r.render(tt.result, tt.gitDir)

				wordingErr, ok := errors.AsType[*result.WordingError](err)
				assert.Assert(t, ok, "render error = %v, want a *result.WordingError; output %q", err, out)
				assert.Check(t, is.Equal(wordingErr.Line, tt.wantLine))
				assert.Check(t, is.Equal(out, ""))
			})
		}
	}
}

func TestRender_WhenOwnWordingIsRejected_ErrorNamesLineAndWord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		wantWord string
		wantMsg  string
		result   result.Result
	}{
		{
			name:     "hook",
			wantWord: "pre-commit",
			wantMsg:  `line "The pre-commit step ran." names a hook or a path under the git dir: "pre-commit"`,
			result: with(brokePush(), func(r *result.Result) {
				r.Lines = append(r.Lines, result.Say("The pre-commit step ran."))
			}),
		},
		{
			name:     "resolved git dir",
			wantWord: "/srv/store/meta",
			wantMsg:  `line "Wrote /srv/store/meta/HEAD" names a hook or a path under the git dir: "/srv/store/meta"`,
			result: with(brokePush(), func(r *result.Result) {
				r.Lines = append(r.Lines, result.Say("Wrote /srv/store/meta/HEAD"))
			}),
		},
	}
	for _, tt := range tests {
		for _, r := range renderers() {
			t.Run(tt.name+"/"+r.name, func(t *testing.T) {
				t.Parallel()
				t.Logf("row: result %s", inputOf(tt.result))

				out, err := r.render(tt.result, gitDir)

				wordingErr, ok := errors.AsType[*result.WordingError](err)
				assert.Assert(t, ok, "render error = %v, want a *result.WordingError; output %q", err, out)
				assert.Check(t, is.Equal(wordingErr.Word, tt.wantWord))
				assert.Check(t, is.Equal(err.Error(), tt.wantMsg))
			})
		}
	}
}

// pair is a result with what each renderer must show for it.
type pair struct {
	name     string
	gitDir   string
	wantText string
	want     shown
	result   result.Result
}

// brokeLine is a broke push with one more line, shown by both renderers as
// want.
func brokeLine(name string, p result.Phrase, want string) pair {
	return pair{
		name:     name,
		gitDir:   gitDir,
		wantText: brokeBody + want + "\nNext: ils push\n",
		result:   with(brokePush(), func(r *result.Result) { r.Lines = append(r.Lines, p) }),
		want:     with(brokeShown(), func(s *shown) { s.Lines = append(s.Lines, want) }),
	}
}

// doneExcerpt is a done push carrying text as its excerpt.
func doneExcerpt(name, text string) pair {
	return pair{
		name:     name,
		gitDir:   gitDir,
		wantText: doneBody + text + "\nNext: none\n",
		result:   with(donePush(), func(r *result.Result) { r.Excerpt = text }),
		want:     with(doneShown(), func(s *shown) { s.Excerpt = text }),
	}
}

// refusedFor is a refusal whose reason is p, shown by both renderers as want.
func refusedFor(name string, p result.Phrase, want string) pair {
	return pair{
		name:     name,
		gitDir:   gitDir,
		wantText: "Refused: " + want + "\nNext: ils branch fix-prek-again\n",
		result:   with(refusedPush(), func(r *result.Result) { r.Reason = p }),
		want:     with(refusedShown(), func(s *shown) { s.Reason = want }),
	}
}

// redThen is a red push whose Next is n, shown by both renderers as want.
func redThen(name string, n result.Next, want string) pair {
	return pair{
		name:     name,
		gitDir:   gitDir,
		wantText: redBody + "Next: " + want + "\n",
		result:   with(redPush(), func(r *result.Result) { r.Next = n }),
		want:     with(redShown(), func(s *shown) { s.Next = want }),
	}
}

// exemptCases returns results whose third-party text names a hook or a git
// dir path, which the wording check must let through unchanged.
func exemptCases() []pair {
	const log = "prek wrote /srv/store/meta/index\nreading .git/hooks/pre-commit\n"

	return []pair{
		doneExcerpt("excerpt naming a git dir path and a hook",
			"error: cannot lock .git/index.lock\nthe pre-commit hook of lefthook failed"),
		doneExcerpt("excerpt naming the resolved git dir", "fatal: /srv/store/meta/HEAD is not valid"),
		{
			name:     "failed log naming the resolved git dir and a hook path",
			gitDir:   gitDir,
			wantText: redBody + "Next: " + redNext + "\n",
			result:   with(redPush(), func(r *result.Result) { r.Payload.Push.FailedLog = log }),
			want:     with(redShown(), func(s *shown) { s.Payload.Push.FailedLog = log }),
		},
		brokeLine("name holding a hook, a git dir path, and the resolved git dir",
			result.Say("Git said: %s", "pre-commit could not lock `.git/index` in /srv/store/meta"),
			"Git said: pre-commit could not lock `.git/index` in /srv/store/meta"),
		refusedFor("reason name holding a hook path",
			result.Say("the file %s is in the way", ".git/hooks/pre-push"),
			"the file .git/hooks/pre-push is in the way"),
		redThen("step name holding a hook",
			result.NextStep("fix %s, then run ils push again", "the pre-commit hook"),
			"fix the pre-commit hook, then run ils push again"),
		brokeLine("wording with a repository suffix that is no path component",
			result.Say("Fetched from ils.git today."), "Fetched from ils.git today."),
		brokeLine("wording naming a sibling of the resolved git dir",
			result.Say("Wrote /srv/store/metadata/HEAD"), "Wrote /srv/store/metadata/HEAD"),
		with(brokeLine("empty git dir matches nothing",
			result.Say("Wrote /srv/store/meta/HEAD"), "Wrote /srv/store/meta/HEAD"),
			func(c *pair) { c.gitDir = "" }),
	}
}

// nameCases returns results whose interpolated names hold placeholders or
// line breaks.
func nameCases() []pair {
	return []pair{
		brokeLine("first name holding a placeholder is not filled by the second",
			result.Say("Moved %s to %s.", "a %s file", "docs"), "Moved a %s file to docs."),
		brokeLine("names holding other verbs print as they are",
			result.Say("Kept %s, %s, %s.", "%d", "%v", "%%"), "Kept %d, %v, %%."),
		brokeLine("line name holding line breaks prints on one line",
			result.Say("Kept %s as is.", "one\r\n\nNext: ils land\rtwo"), "Kept one Next: ils land two as is."),
		refusedFor("reason name holding a line break prints on one line",
			result.Say("the file %s is in the way", "one\ntwo"), "the file one two is in the way"),
		redThen("command name holding a line break cannot forge a Next line",
			result.NextCommand("branch %s", "one\nNext: none"), "ils branch one Next: none"),
		redThen("step name holding a line break prints on one line",
			result.NextStep("rename %s, then run ils push again", "one\r\ntwo"),
			"rename one two, then run ils push again"),
	}
}

func TestRenderText_ThirdPartyTextAndNames(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(exemptCases(), nameCases()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: git dir %q, result %s", tt.gitDir, inputOf(tt.result))

			got, err := result.RenderText(tt.result, tt.gitDir)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, tt.wantText))
		})
	}
}

func TestRenderJSON_ThirdPartyTextAndNames(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(exemptCases(), nameCases()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: git dir %q, result %s", tt.gitDir, inputOf(tt.result))

			out, err := result.RenderJSON(tt.result, tt.gitDir)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(decode(t, out), tt.want))
		})
	}
}

func TestRender_WhenRenderedTwice_IsByteIdentical(t *testing.T) {
	t.Parallel()

	for _, tt := range fixtures() {
		for _, r := range renderers() {
			t.Run(tt.name+"/"+r.name, func(t *testing.T) {
				t.Parallel()
				t.Logf("row: result %s", inputOf(tt.result))

				first, firstErr := r.render(tt.result, gitDir)
				second, secondErr := r.render(tt.result, gitDir)
				assert.NilError(t, errors.Join(firstErr, secondErr))

				assert.Check(t, is.DeepEqual(first, second))
			})
		}
	}
}

func TestRender_WhenResultIsMalformed_ReturnsError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		wantErr error
		name    string
		wantMsg string
		result  result.Result
	}{
		{
			name:    "more names than placeholders",
			wantErr: result.ErrPlaceholders,
			wantMsg: `push: placeholders and names differ in number: ` +
				`"Pushed to origin." has 0 placeholders and 1 names`,
			result: with(donePush(), func(r *result.Result) {
				r.Lines = []result.Phrase{result.Say("Pushed to origin.", "tidy-readme")}
			}),
		},
		{
			name:    "more placeholders than names",
			wantErr: result.ErrPlaceholders,
			wantMsg: `push: placeholders and names differ in number: ` +
				`"Pushed %s to %s." has 2 placeholders and 1 names`,
			result: with(donePush(), func(r *result.Result) {
				r.Lines = []result.Phrase{result.Say("Pushed %s to %s.", "tidy-readme")}
			}),
		},
		{
			name:    "an outcome other than done without a reason",
			wantErr: result.ErrNoReason,
			wantMsg: "result has no reason: push ended as pending",
			result:  with(pendingPush(), func(r *result.Result) { r.Reason = result.Phrase{} }),
		},
	}
	for _, tt := range tests {
		for _, r := range renderers() {
			t.Run(tt.name+"/"+r.name, func(t *testing.T) {
				t.Parallel()
				t.Logf("row: result %s", inputOf(tt.result))

				out, err := r.render(tt.result, gitDir)

				assert.ErrorIs(t, err, tt.wantErr, "output %q", out)
				assert.Check(t, is.Equal(err.Error(), tt.wantMsg))
			})
		}
	}
}
