package result_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/colorprofile"
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
	redNext   = "fix the failing check, commit, and run ils push again"
	redReason = "Delivered, not green: the check prek is red\n"

	brokeEnd = "Broke: GitHub did not answer\nNext: ils push\n"
	redEnd   = redReason + "Next: " + redNext + "\n"
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

// renderers lists the text and JSON renderers, for behaviors they must share.
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

// brokeWithLine is brokePush with p as one more line.
func brokeWithLine(p result.Phrase) result.Result {
	return with(brokePush(), func(r *result.Result) { r.Lines = append(r.Lines, p) })
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
			name:   "done prints its lines and no Next line",
			gitDir: gitDir,
			result: donePush(),
			want:   doneBody,
		},
		{
			name:   "broke prints what was done and what was not",
			gitDir: gitDir,
			result: brokePush(),
			want:   brokeBody + brokeEnd,
		},
		{
			name:   "refused with no lines prints only the reason above Next",
			gitDir: gitDir,
			result: refusedPush(),
			want: "Refused: the branch fix-prek already landed with lefthook.yml\n" +
				"Next: ils branch fix-prek-again\n",
		},
		{
			name:   "refused prints the reason after its lines and excerpt",
			gitDir: gitDir,
			result: with(refusedPush(), func(r *result.Result) {
				r.Lines = []result.Phrase{result.Say("Fetched origin.")}
				r.Excerpt = "one\ntwo"
			}),
			want: "Fetched origin.\none\ntwo\n" +
				"Refused: the branch fix-prek already landed with lefthook.yml\n" +
				"Next: ils branch fix-prek-again\n",
		},
		{
			name:   "pending prints its reason and names the same command again",
			gitDir: gitDir,
			result: pendingPush(),
			want: "Pushed tidy-readme to origin.\nStill running after 100 seconds: build\n" +
				"Pending: the checks were still running at the bound\nNext: ils push\n",
		},
		{
			name:   "excerpt sits verbatim between the lines and the reason",
			gitDir: gitDir,
			result: redPush(),
			want:   redBody + redEnd,
		},
		{
			name:   "merge payload prints nothing",
			gitDir: gitDir,
			result: doneMerge(),
			want:   "Merged pull request #12 into main.\n",
		},
	}
}

// excerptTextCases returns the cases where the excerpt's own text meets the reason line.
func excerptTextCases() []textCase {
	return []textCase{
		{
			name:   "excerpt ending in a newline gains no blank line",
			gitDir: gitDir,
			result: with(brokePush(), func(r *result.Result) { r.Excerpt = "one\ntwo\n" }),
			want:   brokeBody + "one\ntwo\n" + brokeEnd,
		},
		{
			name:   "excerpt holding a Next line stays verbatim above the real one",
			gitDir: gitDir,
			result: with(brokePush(), func(r *result.Result) { r.Excerpt = "Next: ils land\nmore" }),
			want:   brokeBody + "Next: ils land\nmore\n" + brokeEnd,
		},
		{
			name:   "excerpt of only newlines stays above the reason",
			gitDir: gitDir,
			result: with(brokePush(), func(r *result.Result) { r.Excerpt = "\n\n\n" }),
			want:   brokeBody + "\n\n\n" + brokeEnd,
		},
		{
			name:   "excerpt of a result with no lines sits alone above the reason",
			gitDir: gitDir,
			result: with(brokePush(), func(r *result.Result) {
				r.Lines = nil
				r.Excerpt = "one\ntwo"
			}),
			want: "one\ntwo\n" + brokeEnd,
		},
	}
}

// pairCases returns the results both renderers must handle, each with the
// text and the object it shows.
func pairCases() []pair {
	return slices.Concat(exemptCases(), nameCases(), controlCases())
}

// allTextCases returns every case with the plain text it prints.
func allTextCases() []textCase {
	cases := slices.Concat(textCases(), excerptTextCases())
	for _, p := range pairCases() {
		cases = append(cases, textCase{name: p.name, gitDir: p.gitDir, result: p.result, want: p.wantText})
	}

	return cases
}

func TestRenderText(t *testing.T) {
	t.Parallel()

	for _, tt := range allTextCases() {
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
		wantText: brokeBody + want + "\n" + brokeEnd,
		result:   brokeWithLine(p),
		want:     with(brokeShown(), func(s *resulttest.Shown) { s.Lines = append(s.Lines, want) }),
	}
}

// doneExcerpt is a done push carrying text as its excerpt.
func doneExcerpt(name, text string) pair {
	return pair{
		name:     name,
		gitDir:   gitDir,
		wantText: doneBody + text + "\n",
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
		wantText: redBody + redReason + "Next: " + want + "\n",
		result:   with(redPush(), func(r *result.Result) { r.Next = n }),
		want:     with(redShown(), func(s *resulttest.Shown) { s.Next = want }),
	}
}

// carrying returns p with the object it shows holding raw wherever the text prints visible: the
// JSON renderer carries third-party text as it was handed in.
func (p pair) carrying(visible, raw string) pair {
	swap := func(s string) string { return strings.ReplaceAll(s, visible, raw) }
	p.want.Next = swap(p.want.Next)
	p.want.Reason = swap(p.want.Reason)
	p.want.Excerpt = swap(p.want.Excerpt)
	p.want.Lines = slices.Clone(p.want.Lines)
	for i, line := range p.want.Lines {
		p.want.Lines[i] = swap(line)
	}

	return p
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
			wantText: redBody + redEnd,
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
		brokeLine("name marked as a branch holding a hook",
			result.Say("Kept %s.", result.Branch("pre-commit")), "Kept pre-commit."),
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
		brokeLine("names marked with a kind print as their plain text",
			result.Say("Pushed %s at %s to %s: %s, %s checks, run %s.",
				result.Branch("tidy-readme"), result.SHA("4f2a9c1"), result.PullRequest(12),
				result.URL(pullURL), result.Count(3), result.Command("push")),
			"Pushed tidy-readme at 4f2a9c1 to #12: "+pullURL+", 3 checks, run ils push."),
		brokeLine("marked name holding a line break prints on one line",
			result.Say("Kept %s as is.", result.Branch("one\r\ntwo")), "Kept one two as is."),
		brokeLine("marked name holding a placeholder is not filled by the next name",
			result.Say("Moved %s to %s.", result.Branch("a %s b"), result.Branch("docs")), "Moved a %s b to docs."),
		brokeLine("list of one branch prints the branch",
			result.Say("Kept %s.", result.Branches([]string{"docs"})), "Kept docs."),
		brokeLine("list of two branches prints them joined by and",
			result.Say("Kept %s.", result.Branches([]string{"docs", "main"})), "Kept docs and main."),
		brokeLine("list of three branches prints them joined by commas and and",
			result.Say("Kept %s.", result.Branches([]string{"docs", "main", "tidy-readme"})), "Kept docs, main and tidy-readme."),
		brokeLine("pull request number marked bare prints without a sign",
			result.Say("Ran gh pr ready %s.", result.PullRequestNumber(12)), "Ran gh pr ready 12."),
		brokeLine("list of SHAs prints them joined by spaces",
			result.Say("Ran git cherry-pick %s.", result.SHAs([]string{"4f2a9c1", "b7e03d2"})),
			"Ran git cherry-pick 4f2a9c1 b7e03d2."),
		brokeLine("list of branches holding a line break prints on one line",
			result.Say("Kept %s.", result.Branches([]string{"one\r\ntwo", "main"})), "Kept one two and main."),
	}
}

// tabCaseName and invalidUTF8CaseName name the controlCases rows whose text the renderers print
// unchanged, so they hold no control rune to look for.
const (
	tabCaseName         = "name holding a tab prints it as it is"
	invalidUTF8CaseName = "name holding invalid UTF-8 prints its bytes as they are"
)

// controlCases returns results whose third-party text holds control bytes, which the text prints as
// visible escapes and the object it shows carries as they were handed in.
func controlCases() []pair {
	const raw = "one\x1b[1;33m two\b three\x7f four\u009b"

	return []pair{
		brokeLine("name holding an escape sequence prints the escape as visible text",
			result.Say("Kept %s as is.", "x\x1b[31m"), `Kept x\x1b[31m as is.`).carrying(`\x1b`, "\x1b"),
		brokeLine("marked name holding an escape sequence prints the escape as visible text",
			result.Say("Kept %s as is.", result.Branch("x\x1b[4m")), `Kept x\x1b[4m as is.`).carrying(`\x1b`, "\x1b"),
		brokeLine("name opening a hyperlink prints the sequence as visible text",
			result.Say("Kept %s as is.", "x"+openLink+"y"), `Kept x\x1b]8;;https://e.example\x07y as is.`).
			carrying(`\x1b`, "\x1b").carrying(`\x07`, "\a"),
		brokeLine("name holding a backspace prints it as visible text",
			result.Say("Kept %s as is.", "x\by"), `Kept x\x08y as is.`).carrying(`\x08`, "\b"),
		brokeLine("name holding a bell prints it as visible text",
			result.Say("Kept %s as is.", "x\ay"), `Kept x\x07y as is.`).carrying(`\x07`, "\a"),
		brokeLine("name holding a delete prints it as visible text",
			result.Say("Kept %s as is.", "x\x7fy"), `Kept x\x7fy as is.`).carrying(`\x7f`, "\x7f"),
		brokeLine("name holding a C1 control prints it as visible text",
			result.Say("Kept %s as is.", "x\u009by"), `Kept x\u009by as is.`).carrying(`\u009b`, "\u009b"),
		brokeLine(tabCaseName,
			result.Say("Kept %s as is.", "x\ty"), "Kept x\ty as is."),
		brokeLine(invalidUTF8CaseName,
			result.Say("Kept %s as is.", "x\xff more"), "Kept x\xff more as is.").carrying("\xff", "�"),
		refusedFor("reason name holding an escape sequence prints the escape as visible text",
			result.Say("the file %s is in the way", "x\x1b[31m"), `the file x\x1b[31m is in the way`).
			carrying(`\x1b`, "\x1b"),
		redThen("command name holding a cursor sequence cannot erase the Next line",
			result.NextCommand("branch %s", "x\x1b[2K\x1b[1Gy"), `ils branch x\x1b[2K\x1b[1Gy`).carrying(`\x1b`, "\x1b"),
		redThen("step name holding a backspace prints it as visible text",
			result.NextStep("rename %s, then run ils push again", "x\by"), `rename x\x08y, then run ils push again`).
			carrying(`\x08`, "\b"),
		{
			name:     "excerpt holding control bytes prints them as visible text",
			gitDir:   gitDir,
			wantText: doneBody + `one\x1b[1;33m two\x08 three\x7f four\u009b` + "\n",
			result:   with(donePush(), func(r *result.Result) { r.Excerpt = raw }),
			want:     with(doneShown(), func(s *resulttest.Shown) { s.Excerpt = raw }),
		},
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

func TestRenderStyled_WhenProfileIsBelowANSI_PrintsThePlainText(t *testing.T) {
	t.Parallel()
	r := markedPush()
	want, err := result.RenderText(r, gitDir)
	assert.NilError(t, err)

	got, err := result.RenderStyled(r, gitDir, colorprofile.ASCII)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got, want))
}

func TestRenderStyled_WhenProfileIsTrueColor_DrawsAsAtANSI(t *testing.T) {
	t.Parallel()
	r := markedPush()
	want, err := result.RenderStyled(r, gitDir, colorprofile.ANSI)
	assert.NilError(t, err)

	got, err := result.RenderStyled(r, gitDir, colorprofile.TrueColor)
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got, want))
}

func TestRenderStyled_WhenProfileHasColor_DrawsThePlainText(t *testing.T) {
	t.Parallel()

	for _, tt := range allTextCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: git dir %q, result %s", tt.gitDir, inputOf(tt.result))

			out, err := result.RenderStyled(tt.result, tt.gitDir, colorprofile.ANSI)
			assert.NilError(t, err)

			assert.Check(t, is.Equal(resulttest.Draw(t, out).String(), resulttest.Draw(t, tt.want).String()))
		})
	}
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
			result.Say("%s checks passed.", result.Count(3)),
		},
		Reason: result.Say("GitHub did not merge %s into %s", result.PullRequest(12), result.Branch("main")),
		Next:   result.NextStep("rebase %s, then run %s again", result.Branch("tidy-readme"), result.Command("push")),
	}
}

type styledCase struct {
	name   string
	within string
	part   string
	want   resulttest.Look
	result result.Result
}

// nameLook is how the styled renderer draws a branch, a pull request number and a command.
func nameLook() resulttest.Look {
	return resulttest.Look{Color: "blue", Bold: true}
}

// stoppedLabelLook is how it draws the label of an outcome that stopped short of done.
func stoppedLabelLook() resulttest.Look {
	return resulttest.Look{Color: "red"}
}

// shaLook is how it draws a SHA.
func shaLook() resulttest.Look {
	return resulttest.Look{Dim: true}
}

// nextLook is how it draws the Next label.
func nextLook() resulttest.Look {
	return resulttest.Look{Bold: true, Underline: true}
}

// styledCases returns one case per part of a result the styled renderer draws in its own style.
func styledCases() []styledCase {
	return []styledCase{
		{name: "wording", result: markedPush(), within: "Pushed tidy", part: "Pushed "},
		{name: "branch", result: markedPush(), within: "Pushed tidy-readme", part: "tidy-readme", want: nameLook()},
		{name: "sha", result: markedPush(), within: "at 4f2a9c1", part: "4f2a9c1", want: shaLook()},
		{name: "pull request number", result: markedPush(), within: "request #12", part: "#12", want: nameLook()},
		{name: "url", result: markedPush(), within: pullURL, part: pullURL, want: resulttest.Look{Underline: true}},
		{name: "count", result: markedPush(), within: "3 checks", part: "3", want: resulttest.Look{Bold: true}},
		{name: "wording after a name", result: markedPush(), within: "3 checks passed.", part: " checks passed."},
		{name: "broke label", result: markedPush(), within: "Broke: GitHub", part: "Broke:", want: stoppedLabelLook()},
		{name: "reason wording", result: markedPush(), within: "Broke: GitHub did", part: " GitHub did"},
		{name: "pull request number in the reason", result: markedPush(), within: "merge #12", part: "#12", want: nameLook()},
		{name: "branch in the reason", result: markedPush(), within: "into main", part: "main", want: nameLook()},
		{name: "Next", result: markedPush(), within: "Next: rebase", part: "Next", want: nextLook()},
		{name: "Next colon", result: markedPush(), within: "Next: rebase", part: ":"},
		{name: "step wording", result: markedPush(), within: "Next: rebase", part: " rebase"},
		{name: "branch in the step", result: markedPush(), within: "rebase tidy-readme", part: "tidy-readme", want: nameLook()},
		{name: "command in the step", result: markedPush(), within: "run ils push again", part: "ils push", want: nameLook()},
		{name: "step wording after a command", result: markedPush(), within: "ils push again", part: " again"},
		{name: "refused label", result: refusedPush(), within: "Refused: the", part: "Refused:", want: stoppedLabelLook()},
		{name: "command", result: refusedPush(), within: "Next: ils branch fix-prek-again", part: "ils branch fix-prek-again", want: nameLook()},
		{name: "pending label", result: pendingPush(), within: "Pending: the", part: "Pending:", want: resulttest.Look{Color: "yellow"}},
		{
			name: "delivered, not green label", result: redPush(), within: "Delivered, not green: the",
			part: "Delivered, not green:", want: stoppedLabelLook(),
		},
		{name: "excerpt", result: redPush(), within: excerpt, part: excerpt},
		{name: "unmarked name", result: donePush(), within: "Pushed tidy-readme to origin.", part: "tidy-readme"},
		{name: "first branch of a list", result: listedBranches(), within: "Kept docs,", part: "docs", want: nameLook()},
		{name: "comma of a list", result: listedBranches(), within: "docs, main", part: ", "},
		{name: "middle branch of a list", result: listedBranches(), within: ", main and", part: "main", want: nameLook()},
		{name: "and of a list", result: listedBranches(), within: "main and tidy-readme", part: " and "},
		{name: "last branch of a list", result: listedBranches(), within: "and tidy-readme.", part: "tidy-readme", want: nameLook()},
		{name: "first SHA of a list", result: pickedSHAs(), within: "pick 4f2a9c1", part: "4f2a9c1", want: shaLook()},
		{name: "space of a list of SHAs", result: pickedSHAs(), within: "4f2a9c1 b7e03d2", part: " "},
		{name: "last SHA of a list", result: pickedSHAs(), within: "b7e03d2.", part: "b7e03d2", want: shaLook()},
		{name: "pull request number marked bare", result: pickedSHAs(), within: "ready 12", part: "12", want: nameLook()},
		{name: "text of a concatenated name", result: joinedCommand(), within: "Ran `git switch main`", part: "git switch "},
		{name: "branch of a concatenated name", result: joinedCommand(), within: "Ran `git switch main`", part: "main", want: nameLook()},
	}
}

// joinedCommand is a broke push with one more line that names a git command whose argument is a
// branch.
func joinedCommand() result.Result {
	return brokeWithLine(result.Say("Ran `%s`", result.Concat("git switch ", result.Branch("main"))))
}

// pickedSHAs is a broke push whose next step names a pull request by its bare number and a list
// of two SHAs.
func pickedSHAs() result.Result {
	return with(brokePush(), func(r *result.Result) {
		r.Next = result.NextStep("run gh pr ready %s and git cherry-pick %s.",
			result.PullRequestNumber(12), result.SHAs([]string{"4f2a9c1", "b7e03d2"}))
	})
}

// listedBranches is a broke push with one more line that names a list of three branches.
func listedBranches() result.Result {
	return brokeWithLine(result.Say("Kept %s.", result.Branches([]string{"docs", "main", "tidy-readme"})))
}

// openLink is a hyperlink sequence that third-party text opens and never closes.
const openLink = "\x1b]8;;https://e.example\x07"

// escapeCases returns results whose third-party text holds an escape sequence, each with a part
// next to that text that keeps its own style.
func escapeCases() []styledCase {
	return []styledCase{
		{
			name:   "wording after a name that opens a color",
			result: brokeWithLine(result.Say("Kept %s as is.", "x\x1b[31m")),
			within: `x\x1b[31m as is.`, part: " as is.",
		},
		{
			name:   "wording after a marked name that opens an underline",
			result: brokeWithLine(result.Say("Kept %s as is.", result.Branch("x\x1b[4m"))),
			within: `x\x1b[4m as is.`, part: " as is.",
		},
		{
			name:   "visible escape inside a marked name",
			result: brokeWithLine(result.Say("Kept %s as is.", result.Branch("x\x1b[4m"))),
			within: `x\x1b[4m as is.`, part: `x\x1b[4m`, want: nameLook(),
		},
		{
			name:   "command after a name that resets every style",
			result: with(redPush(), func(r *result.Result) { r.Next = result.NextCommand("branch %s now", "a\x1b[0mb") }),
			within: `branch a\x1b[0mb now`, part: " now", want: nameLook(),
		},
		{
			name:   "wording after a name that opens a hyperlink",
			result: brokeWithLine(result.Say("Kept %s as is.", "x"+openLink+"y")),
			within: `x\x1b]8;;https://e.example\x07y as is.`, part: " as is.",
		},
		{
			name:   "wording after a marked name that opens a hyperlink",
			result: brokeWithLine(result.Say("Kept %s as is.", result.Branch("x"+openLink+"y"))),
			within: `x\x1b]8;;https://e.example\x07y as is.`, part: " as is.",
		},
		{
			name:   "label after an excerpt that opens a hyperlink",
			result: with(brokePush(), func(r *result.Result) { r.Excerpt = "one" + openLink + " two" }),
			within: "Broke: GitHub", part: "Broke:", want: stoppedLabelLook(),
		},
		{
			name:   "label after an excerpt that opens bold yellow",
			result: with(brokePush(), func(r *result.Result) { r.Excerpt = "one\x1b[1;33m" }),
			within: "Broke: GitHub", part: "Broke:", want: stoppedLabelLook(),
		},
		{
			name:   "Next after an excerpt that opens bold yellow",
			result: with(brokePush(), func(r *result.Result) { r.Excerpt = "one\x1b[1;33m" }),
			within: "Next: ils push", part: "Next", want: nextLook(),
		},
	}
}

func TestRenderStyled_WhenProfileHasColor_DrawsEachPartInItsStyle(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(styledCases(), escapeCases()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: result %s", inputOf(tt.result))

			out, err := result.RenderStyled(tt.result, gitDir, colorprofile.ANSI)
			assert.NilError(t, err)

			got := resulttest.Draw(t, out).LookOf(t, tt.within, tt.part)
			assert.Check(t, is.Equal(got, tt.want), "output %q", out)
		})
	}
}

// ownStyling matches the select graphic rendition and hyperlink sequences the styled renderer
// draws with.
var ownStyling = regexp.MustCompile("\x1b\\[[0-9;:]*m|\x1b\\]8;[^\x07\x1b]*(?:\x07|\x1b\\\\)")

func TestRenderStyled_WhenThirdPartyTextHoldsControlBytes_PrintsNoEscapeButItsOwnStyling(t *testing.T) {
	t.Parallel()

	isControl := func(r rune) bool { return unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' }
	cases := slices.DeleteFunc(controlCases(), func(c pair) bool {
		return c.name == tabCaseName || c.name == invalidUTF8CaseName
	})

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: result %s", inputOf(tt.result))

			out, err := result.RenderStyled(tt.result, tt.gitDir, colorprofile.ANSI)
			assert.NilError(t, err)

			assert.Check(t, !strings.ContainsFunc(ownStyling.ReplaceAllString(out, ""), isControl), "output %q", out)
		})
	}
}
