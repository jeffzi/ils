package result_test

import (
	"errors"
	"slices"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

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
		result:   brokeWithLine(p),
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
		line("prek around a name marked as a branch", "prek", "Ran prek on main.",
			result.Say("Ran prek on %s.", result.Branch("main"))),
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
			result: refusedWithReason(p),
		}
	}
	next := func(name, wantWord, wantLine string, n result.Next) rejected {
		return rejected{
			name: name, gitDir: gitDir, wantLine: wantLine, wantWord: wantWord,
			result: redWithNext(n),
		}
	}

	return []rejected{
		reason("reason naming a hook", "pre-push", "the pre-push step failed on main",
			result.Say("the pre-push step failed on %s", "main")),
		next("judgment step naming a hook", "prek,", "run prek, then ils push again",
			result.NextStep("run prek, then ils push again")),
		next("command naming a hook", "hooks/", "ils push hooks/", result.NextCommand("push hooks/")),
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

func TestRender_WhenOwnWordingNamesHookOrGitDir_ReturnsWordingError(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(lineCases(), dirCases(), otherCases()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("row: git dir %q, result %s", tt.gitDir, inputOf(tt.result))

			out, err := result.RenderText(tt.result, tt.gitDir)

			wordingErr, ok := errors.AsType[*result.WordingError](err)
			assert.Assert(t, ok, "RenderText error = %v, want a *result.WordingError; output %q", err, out)
			assert.Check(t, is.Equal(wordingErr.Line, tt.wantLine))
			assert.Check(t, is.Equal(wordingErr.Word, tt.wantWord))
			assert.Check(t, is.Equal(out, ""))
		})
	}
}

func TestWordingError_Error_WhenLineNamesHook_QuotesLineAndWord(t *testing.T) {
	t.Parallel()
	err := &result.WordingError{Line: "The pre-commit step ran.", Word: "pre-commit"}

	got := err.Error()

	assert.Check(t, is.Equal(got,
		`line "The pre-commit step ran." names a hook or a path under the git dir: "pre-commit"`))
}
