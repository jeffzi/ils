package result_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
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

// ofPush returns s as the push command shows it.
func ofPush(s resulttest.Shown) resulttest.Shown {
	s.Command = "push"
	s.Words = []string{"push"}

	return s
}

// doneShown is the object the JSON renderer shows for donePush.
func doneShown() resulttest.Shown {
	return ofPush(resulttest.Shown{
		Outcome: "done", ExitCode: 0, Next: "none",
		Lines: []string{"Pushed tidy-readme to origin.", "Every check is green."},
	})
}

// brokeShown is the object the JSON renderer shows for brokePush.
func brokeShown() resulttest.Shown {
	return ofPush(resulttest.Shown{
		Outcome: "broke", ExitCode: 1, Next: "ils push", Reason: "GitHub did not answer",
		Lines: []string{"Pushed tidy-readme to origin.", "Did not open the pull request: HTTP 502"},
	})
}

// refusedShown is the object the JSON renderer shows for refusedPush.
func refusedShown() resulttest.Shown {
	return ofPush(resulttest.Shown{
		Outcome: "refused", ExitCode: 2, Next: "ils branch fix-prek-again",
		Reason: "the branch fix-prek already landed with lefthook.yml",
	})
}

// redShown is the object the JSON renderer shows for redPush.
func redShown() resulttest.Shown {
	return ofPush(resulttest.Shown{
		Outcome: "delivered-not-green", ExitCode: 5, Next: redNext,
		Reason: "the check prek is red", Excerpt: excerpt, Payload: redPush().Payload,
		Lines: []string{
			"Pushed fix-prek to origin.", "Pull request #12: " + pullURL,
			"Latest commit: fix %s parsing in the pre-commit config", "Check prek failed: " + checkURL,
		},
	})
}

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
		Next:    result.NextStep(redNext),
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

// mergedPayload is the merge payload of a pull request this run merged, ending on the trunk, with
// tail appended to each of its strings.
func mergedPayload(tail string) *result.Payload {
	return &result.Payload{Merge: &result.Merge{
		URL:        pullURL + tail,
		Number:     12,
		SHA:        "4f2a9c1" + tail,
		Verdict:    "green" + tail,
		MergeState: "clean" + tail,
		Checks: []result.Check{
			{Name: "build" + tail, Bucket: "pass" + tail, Workflow: "CI" + tail, Link: checkURL + "0" + tail},
		},
		Subject: "Tidy the readme (#12)" + tail,
		Body:    "Shorter install steps.\n" + tail,
		Merged:  true,
		Ending:  result.EndingTrunk + tail,
	}}
}

// pushPayload is the payload of redPush with tail appended to each of its strings.
func pushPayload(tail string) *result.Payload {
	push := *redPush().Payload.Push
	push.URL += tail
	push.SHA += tail
	push.Verdict += tail
	push.MergeState += tail
	push.FailedLog += tail
	push.Checks = nil

	for _, check := range redPush().Payload.Push.Checks {
		push.Checks = append(push.Checks, result.Check{
			Name: check.Name + tail, Bucket: check.Bucket + tail, Workflow: check.Workflow + tail, Link: check.Link + tail,
		})
	}

	return &result.Payload{Push: &push}
}

// doneMerge is a merge this run made, ending on the trunk.
func doneMerge() result.Result {
	return result.Result{
		Command: "merge",
		Words:   []string{"merge"},
		Outcome: result.Done,
		Lines:   []result.Phrase{result.Say("Merged pull request #%s into %s.", 12, "main")},
		Next:    result.NextNone(),
		Payload: mergedPayload(""),
	}
}

// doneMergeShown is the object the JSON renderer shows for doneMerge.
func doneMergeShown() resulttest.Shown {
	return resulttest.Shown{
		Command: "merge", Words: []string{"merge"}, Outcome: "done", ExitCode: 0, Next: "none",
		Lines: []string{"Merged pull request #12 into main."}, Payload: mergedPayload(""),
	}
}

// brokeMerge is a merge that failed before it read the checks or sent a message.
func brokeMerge() result.Result {
	return result.Result{
		Command: "merge",
		Words:   []string{"merge"},
		Outcome: result.Broke,
		Next:    result.NextCommand("merge"),
		Reason:  result.Say("GitHub did not answer"),
		Payload: &result.Payload{Merge: &result.Merge{
			URL: pullURL, Number: 12, SHA: "4f2a9c1", Verdict: "none", MergeState: "unknown",
		}},
	}
}

// with returns a copy of base that edit has changed, leaving base itself untouched.
func with[T any](base T, edit func(*T)) T {
	edit(&base)

	return base
}

// inputOf prints a row's result for the log of a failing row, with the push and merge payloads
// that the result holds behind pointers written out.
func inputOf(r result.Result) string {
	text := fmt.Sprintf("%+v", r)
	if r.Payload == nil {
		return text
	}
	if r.Payload.Push != nil {
		text += fmt.Sprintf(", its payload push %+v", *r.Payload.Push)
	}
	if r.Payload.Merge != nil {
		text += fmt.Sprintf(", its payload merge %+v", *r.Payload.Merge)
	}

	return text
}

type textCase struct {
	name   string
	gitDir string
	want   string
	result result.Result
}

// textCases returns one case per shape the text renderer prints.
func textCases() []textCase {
	return []textCase{
		{
			name:   "done prints the lines then Next none",
			gitDir: gitDir,
			result: donePush(),
			want:   doneBody + "Next: none\n",
		},
		{
			name:   "broke prints what was done and what was not",
			gitDir: gitDir,
			result: brokePush(),
			want:   brokeBody + "Next: ils push\n",
		},
		{
			name:   "refused prints the reason first",
			gitDir: gitDir,
			result: refusedPush(),
			want: "Refused: the branch fix-prek already landed with lefthook.yml\n" +
				"Next: ils branch fix-prek-again\n",
		},
		{
			name:   "refused prints the reason before its lines and excerpt",
			gitDir: gitDir,
			result: with(refusedPush(), func(r *result.Result) {
				r.Lines = []result.Phrase{result.Say("Fetched origin.")}
				r.Excerpt = "one\ntwo"
			}),
			want: "Refused: the branch fix-prek already landed with lefthook.yml\n" +
				"Fetched origin.\none\ntwo\nNext: ils branch fix-prek-again\n",
		},
		{
			name:   "pending names the same command again",
			gitDir: gitDir,
			result: pendingPush(),
			want: "Pushed tidy-readme to origin.\nStill running after 100 seconds: build\n" +
				"Next: ils push\n",
		},
		{
			name:   "excerpt sits verbatim between the lines and Next",
			gitDir: gitDir,
			result: redPush(),
			want:   redBody + "Next: " + redNext + "\n",
		},
		{
			name:   "merge payload prints nothing",
			gitDir: gitDir,
			result: doneMerge(),
			want:   "Merged pull request #12 into main.\nNext: none\n",
		},
	}
}

// excerptTextCases returns the cases where the excerpt's own text meets the Next line.
func excerptTextCases() []textCase {
	return []textCase{
		{
			name:   "excerpt ending in a newline gains no blank line",
			gitDir: gitDir,
			result: with(donePush(), func(r *result.Result) { r.Excerpt = "one\ntwo\n" }),
			want:   doneBody + "one\ntwo\nNext: none\n",
		},
		{
			name:   "excerpt holding a Next line stays verbatim above the real one",
			gitDir: gitDir,
			result: with(donePush(), func(r *result.Result) { r.Excerpt = "Next: ils land\nmore" }),
			want:   doneBody + "Next: ils land\nmore\nNext: none\n",
		},
		{
			name:   "excerpt of only newlines stays above Next",
			gitDir: gitDir,
			result: with(donePush(), func(r *result.Result) { r.Excerpt = "\n\n\n" }),
			want:   doneBody + "\n\n\nNext: none\n",
		},
		{
			name:   "excerpt of a result with no lines sits alone above Next",
			gitDir: gitDir,
			result: with(donePush(), func(r *result.Result) {
				r.Lines = nil
				r.Excerpt = "one\ntwo"
			}),
			want: "one\ntwo\nNext: none\n",
		},
	}
}

// pairCases returns the results both renderers must handle, each with the
// text and the object it shows.
func pairCases() []pair {
	return slices.Concat(exemptCases(), nameCases())
}

func TestRenderText(t *testing.T) {
	t.Parallel()

	cases := slices.Concat(textCases(), excerptTextCases())
	for _, p := range pairCases() {
		cases = append(cases, textCase{name: p.name, gitDir: p.gitDir, result: p.result, want: p.wantText})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: git dir %q, result %s", tt.gitDir, inputOf(tt.result))

			got, err := result.RenderText(tt.result, tt.gitDir)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(got, tt.want))
		})
	}
}

type jsonCase struct {
	name   string
	gitDir string
	want   resulttest.Shown
	result result.Result
}

// jsonCases returns one case per outcome the JSON renderer shows.
func jsonCases() []jsonCase {
	return []jsonCase{
		{name: "broke", gitDir: gitDir, result: brokePush(), want: brokeShown()},
		{name: "refused", gitDir: gitDir, result: refusedPush(), want: refusedShown()},
		{
			name:   "pending",
			gitDir: gitDir,
			result: pendingPush(),
			want: ofPush(resulttest.Shown{
				Outcome: "pending", ExitCode: 4, Next: "ils push",
				Reason: "the checks were still running at the bound",
				Lines:  []string{"Pushed tidy-readme to origin.", "Still running after 100 seconds: build"},
			}),
		},
		{
			name:   "a command of several words carries its name and each word",
			gitDir: gitDir,
			result: with(donePush(), func(r *result.Result) {
				r.Command = "land"
				r.Words = []string{"stack", "land"}
			}),
			want: with(doneShown(), func(s *resulttest.Shown) {
				s.Command = "land"
				s.Words = []string{"stack", "land"}
			}),
		},
		{
			name:   "merge payload strings print each invalid byte as a replacement character",
			gitDir: gitDir,
			result: with(doneMerge(), func(r *result.Result) { r.Payload = mergedPayload("\xff\xfe") }),
			want:   with(doneMergeShown(), func(s *resulttest.Shown) { s.Payload = mergedPayload("��") }),
		},
		{
			name:   "push payload strings print each invalid byte as a replacement character",
			gitDir: gitDir,
			result: with(redPush(), func(r *result.Result) { r.Payload = pushPayload("\xff\xfe") }),
			want:   with(redShown(), func(s *resulttest.Shown) { s.Payload = pushPayload("��") }),
		},
		{
			name:   "merge that ended detached prints its ending",
			gitDir: gitDir,
			result: with(doneMerge(), func(r *result.Result) { r.Payload.Merge.Ending = result.EndingDetached }),
			want:   with(doneMergeShown(), func(s *resulttest.Shown) { s.Payload.Merge.Ending = "detached" }),
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
	doneMergeGolden = `{"payload":{"merge":{"url":"https://github.com/jeffzi/ils/pull/12","sha":"4f2a9c1",` +
		`"verdict":"green","merge_state":"clean","subject":"Tidy the readme (#12)",` +
		`"body":"Shorter install steps.\n","ending":"trunk",` +
		`"checks":[{"name":"build","bucket":"pass","workflow":"CI",` +
		`"link":"https://github.com/jeffzi/ils/actions/runs/7/job/90"}],"number":12,"merged":true}},` +
		`"command":"merge","words":["merge"],"outcome":"done","next":"none",` +
		`"lines":["Merged pull request #12 into main."],"exit_code":0}` + "\n"
	brokeMergeGolden = `{"payload":{"merge":{"url":"https://github.com/jeffzi/ils/pull/12","sha":"4f2a9c1",` +
		`"verdict":"none","merge_state":"unknown","subject":"","body":"","ending":"",` +
		`"checks":[],"number":12,"merged":false}},` +
		`"command":"merge","words":["merge"],"outcome":"broke","next":"ils merge",` +
		`"reason":"GitHub did not answer","exit_code":1}` + "\n"
)

func TestRenderJSON_WhenResultIsWellFormed_PrintsExactBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		want   string
		result result.Result
	}{
		{name: "red push carries every field", result: redPush(), want: redGolden},
		{name: "bare done leaves unset fields out", result: donePush(), want: doneGolden},
		{name: "done merge carries every merge field", result: doneMerge(), want: doneMergeGolden},
		{name: "merge that read and sent nothing prints its empty fields", result: brokeMerge(), want: brokeMergeGolden},
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

func TestRenderJSON(t *testing.T) {
	t.Parallel()

	cases := jsonCases()
	for _, p := range pairCases() {
		cases = append(cases, jsonCase{name: p.name, gitDir: p.gitDir, result: p.result, want: p.want})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: git dir %q, result %s", tt.gitDir, inputOf(tt.result))

			out, err := result.RenderJSON(tt.result, tt.gitDir)
			assert.NilError(t, err)

			assert.Check(t, is.DeepEqual(resulttest.Decode(t, out), tt.want))
		})
	}
}

func TestRenderJSON_WhenPayloadHoldsInvalidUTF8_LeavesTheResultAsItWas(t *testing.T) {
	t.Parallel()
	invalid := func() *result.Payload {
		return &result.Payload{Push: pushPayload("\xff").Push, Merge: mergedPayload("\xff").Merge}
	}
	r := with(redPush(), func(r *result.Result) { r.Payload = invalid() })

	_, err := result.RenderJSON(r, gitDir)
	assert.NilError(t, err)

	assert.Check(t, is.DeepEqual(r.Payload, invalid()))
}

// rejected is a result whose own wording names a hook or a git dir path.
type rejected struct {
	name     string
	gitDir   string
	wantLine string
	wantWord string
	result   result.Result
}

// rejectedLine is a broke push with one more line, whose wording must be
// rejected against the resolved git dir.
func rejectedLine(name, dir string, p result.Phrase, wantLine string) rejected {
	return rejected{
		name:     name,
		gitDir:   dir,
		wantLine: wantLine,
		result:   with(brokePush(), func(r *result.Result) { r.Lines = append(r.Lines, p) }),
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
			result.Say("Wrote /srv/store/meta/HEAD"), "Wrote /srv/store/meta/HEAD").naming(gitDir + "/"),
		rejectedLine("handed-in dir ends in a separator, wording names it bare", gitDir+"/",
			result.Say("Moved /srv/store/meta aside."), "Moved /srv/store/meta aside.").naming(gitDir + "/"),
		rejectedLine("wording ends in a separator", gitDir,
			result.Say("Emptied /srv/store/meta/ today."), "Emptied /srv/store/meta/ today.").naming(gitDir),
		rejectedLine("wording uses backslashes", gitDir,
			result.Say(`Wrote \srv\store\meta\HEAD`), `Wrote \srv\store\meta\HEAD`).naming(gitDir),
		rejectedLine("handed-in dir uses backslashes", windowsDir,
			result.Say("Wrote C:/Work/Store/meta/HEAD"), "Wrote C:/Work/Store/meta/HEAD").naming(windowsDir),
		rejectedLine("handed-in dir uses backslashes and ends in one", windowsDir+`\`,
			result.Say("Wrote C:/Work/Store/meta/HEAD"), "Wrote C:/Work/Store/meta/HEAD").naming(windowsDir + `\`),
		rejectedLine("wording differs in case", gitDir,
			result.Say("Wrote /SRV/Store/Meta/HEAD"), "Wrote /SRV/Store/Meta/HEAD").naming(gitDir),
		rejectedLine("handed-in dir differs in case", shoutedDir,
			result.Say("Wrote /srv/store/meta/HEAD"), "Wrote /srv/store/meta/HEAD").naming(shoutedDir),
		rejectedLine("resolved git dir holding spaces", spacedDir,
			result.Say("Wrote /srv/my store/meta dir/HEAD"), "Wrote /srv/my store/meta dir/HEAD").naming(spacedDir),
	}
}

// lineCases returns results whose offending wording sits in a line.
func lineCases() []rejected {
	line := func(name, wantWord, wantLine string, p result.Phrase) rejected {
		return rejectedLine(name, gitDir, p, wantLine).naming(wantWord)
	}

	return []rejected{
		line("git dir component in backticks", "`.git/config`",
			"Wrote `.git/config` today.", result.Say("Wrote `.git/config` today.")),
		line("git dir component in quotes", "'.git/config'",
			"Wrote '.git/config' today.", result.Say("Wrote '.git/config' today.")),
		line("git dir component in parentheses", "(.git/config)",
			"Wrote (.git/config) today.", result.Say("Wrote (.git/config) today.")),
		line("git dir component after a flag", "--git-dir=.git/x",
			"Ran with --git-dir=.git/x today.", result.Say("Ran with --git-dir=.git/x today.")),
		line("pre-commit, capital", "Pre-commit", "Pre-commit ran.", result.Say("Pre-commit ran.")),
		line("prek, capital", "Prek", "Prek ran.", result.Say("Prek ran.")),
		line("lefthook, capital", "Lefthook", "Lefthook ran.", result.Say("Lefthook ran.")),
		line("hooks directory, capital", "Hooks/", "Hooks/ skipped.", result.Say("Hooks/ skipped.")),
		line("git dir component, slash", ".git/config", "Wrote .git/config", result.Say("Wrote .git/config")),
		line("git dir component, backslash", `.git\config`, `Wrote .git\config`, result.Say(`Wrote .git\config`)),
		line("git dir component, nested", "work/.git/index.lock",
			"Removed work/.git/index.lock", result.Say("Removed work/.git/index.lock")),
		line("git dir alone", ".git", "Nothing in .git changed.", result.Say("Nothing in .git changed.")),
		line("resolved git dir", gitDir, "Wrote /srv/store/meta/HEAD", result.Say("Wrote /srv/store/meta/HEAD")),
		line("pre-commit", "pre-commit", "The pre-commit step ran.", result.Say("The pre-commit step ran.")),
		line("commit-msg", "commit-msg", "The commit-msg step ran.", result.Say("The commit-msg step ran.")),
		line("pre-push", "pre-push", "The pre-push step ran.", result.Say("The pre-push step ran.")),
		line("post-commit", "post-commit", "The post-commit step ran.", result.Say("The post-commit step ran.")),
		line("prek", "prek", "Ran prek on main.", result.Say("Ran prek on %s.", "main")),
		line("lefthook with the full stop that ends the line", "lefthook.",
			"Ran lefthook.", result.Say("Ran lefthook.")),
		line("hooks directory", "hooks/", "Skipped hooks/ entirely.", result.Say("Skipped hooks/ entirely.")),
	}
}

// otherCases returns results whose offending wording sits outside the lines.
func otherCases() []rejected {
	reason := func(name, wantWord, wantLine string, p result.Phrase) rejected {
		return rejected{
			name: name, gitDir: gitDir, wantLine: wantLine, wantWord: wantWord,
			result: with(refusedPush(), func(r *result.Result) { r.Reason = p }),
		}
	}
	next := func(name, wantWord, wantLine string, n result.Next) rejected {
		return rejected{
			name: name, gitDir: gitDir, wantLine: wantLine, wantWord: wantWord,
			result: with(redPush(), func(r *result.Result) { r.Next = n }),
		}
	}

	return []rejected{
		reason("reason naming a hook", "pre-push", "the pre-push step failed on main",
			result.Say("the pre-push step failed on %s", "main")),
		reason("reason naming the resolved git dir", gitDir, "cannot write /srv/store/meta/HEAD",
			result.Say("cannot write /srv/store/meta/HEAD")),
		next("judgment step naming a hook", "prek,", "run prek, then ils push again",
			result.NextStep("run prek, then ils push again")),
		next("judgment step naming a git dir path", ".git/index.lock,",
			"remove .git/index.lock, then run ils push again",
			result.NextStep("remove .git/index.lock, then run ils push again")),
		next("command naming a hook", "hooks/", "ils push hooks/", result.NextCommand("push hooks/")),
		next("command naming a git dir path", ".git/config", "ils push .git/config",
			result.NextCommand("push .git/config")),
		next("command naming the resolved git dir", gitDir, "ils push /srv/store/meta",
			result.NextCommand("push /srv/store/meta")),
	}
}

func TestRender_WhenOwnWordingNamesHookOrGitDir_ReturnsWordingError(t *testing.T) {
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
				assert.Check(t, is.Equal(wordingErr.Word, tt.wantWord))
				assert.Check(t, is.Equal(out, ""))
			})
		}
	}
}

func TestWordingError_Error_WhenLineNamesHook_QuotesLineAndWord(t *testing.T) {
	t.Parallel()
	err := &result.WordingError{Line: "The pre-commit step ran.", Word: "pre-commit"}

	got := err.Error()

	assert.Check(t, is.Equal(got,
		`line "The pre-commit step ran." names a hook or a path under the git dir: "pre-commit"`))
}

type pair struct {
	name     string
	gitDir   string
	wantText string
	want     resulttest.Shown
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
		want:     with(brokeShown(), func(s *resulttest.Shown) { s.Lines = append(s.Lines, want) }),
	}
}

// doneExcerpt is a done push carrying text as its excerpt.
func doneExcerpt(name, text string) pair {
	return pair{
		name:     name,
		gitDir:   gitDir,
		wantText: doneBody + text + "\nNext: none\n",
		result:   with(donePush(), func(r *result.Result) { r.Excerpt = text }),
		want:     with(doneShown(), func(s *resulttest.Shown) { s.Excerpt = text }),
	}
}

// refusedFor is a refusal whose reason is p, shown by both renderers as want.
func refusedFor(name string, p result.Phrase, want string) pair {
	return pair{
		name:     name,
		gitDir:   gitDir,
		wantText: "Refused: " + want + "\nNext: ils branch fix-prek-again\n",
		result:   with(refusedPush(), func(r *result.Result) { r.Reason = p }),
		want:     with(refusedShown(), func(s *resulttest.Shown) { s.Reason = want }),
	}
}

// redThen is a red push whose Next is n, shown by both renderers as want.
func redThen(name string, n result.Next, want string) pair {
	return pair{
		name:     name,
		gitDir:   gitDir,
		wantText: redBody + "Next: " + want + "\n",
		result:   with(redPush(), func(r *result.Result) { r.Next = n }),
		want:     with(redShown(), func(s *resulttest.Shown) { s.Next = want }),
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
			want:     with(redShown(), func(s *resulttest.Shown) { s.Payload.Push.FailedLog = log }),
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

func TestRender_WhenResultIsMalformed_ReturnsError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		wantErr error
		name    string
		result  result.Result
	}{
		{
			name:    "more names than placeholders",
			wantErr: result.ErrPlaceholders,
			result: with(donePush(), func(r *result.Result) {
				r.Lines = []result.Phrase{result.Say("Pushed to origin.", "tidy-readme")}
			}),
		},
		{
			name:    "more placeholders than names",
			wantErr: result.ErrPlaceholders,
			result: with(donePush(), func(r *result.Result) {
				r.Lines = []result.Phrase{result.Say("Pushed %s to %s.", "tidy-readme")}
			}),
		},
		{
			name:    "an outcome other than done without a reason",
			wantErr: result.ErrNoReason,
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
			})
		}
	}
}
