package result_test

import (
	"errors"
	"slices"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result"
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
