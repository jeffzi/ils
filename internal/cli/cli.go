// Package cli holds the grammar and the usage text of ils: it parses the words, runs the command
// they name under the one deadline, and renders its result.
package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

// bound is the time a whole command may take, counted from launch.
const bound = 100 * time.Second

const (
	// exitDone is the exit code of a help request, which prints its text and runs nothing.
	exitDone = 0
	// exitBroke is the exit code when the result itself could not be printed.
	exitBroke = 1
)

// Command runs one landing command and returns its result.
type Command func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result

// App is one launch of ils: where it prints, when it was launched, and the git it runs.
type App struct {
	Stdout io.Writer
	Stderr io.Writer
	// Push runs `ils push`; nil runs landing.Push.
	Push Command
	// Launch is when the process started; the deadline of every command counts from it.
	Launch time.Time
	// Git runs every git call: its Dir is the working directory, and a nil Env inherits the
	// process's.
	Git git.Runner
}

// Run parses args, the words after the program name, runs the command they name, prints its
// result on Stdout, and returns the exit code.
//
// A result the renderer rejects is printed nowhere: the error goes to Stderr and the code is 1.
func (a App) Run(ctx context.Context, args []string) int {
	asJSON := slices.Contains(args, flagJSON)
	help := slices.Contains(args, flagHelp) || slices.Contains(args, flagHelpShort)
	words := slices.DeleteFunc(slices.Clone(args), func(word string) bool {
		return word == flagJSON || word == flagHelp || word == flagHelpShort
	})

	if help {
		return a.print(usage(words), exitDone)
	}

	ctx, cancel := context.WithDeadline(ctx, a.Launch.Add(bound))
	defer cancel()

	r, gitDir := a.run(ctx, words)

	render := result.RenderText
	if asJSON {
		render = result.RenderJSON
	}

	out, err := render(r, gitDir)
	if err != nil {
		return a.fail(err)
	}

	return a.print(out, r.Outcome.ExitCode())
}

// run returns the result of the command the words name, and the resolved git dir when the
// command ran in a repository. Words that break the grammar are a refusal naming the word.
func (a App) run(ctx context.Context, words []string) (r result.Result, gitDir string) {
	if len(words) == 0 {
		return refused(words, result.Say("no command was given")), ""
	}

	switch words[0] {
	case wordPush:
		if len(words) > 1 {
			return refused(words, result.Say("push takes no other word, and %s was given", words[1])), ""
		}

		return a.push(ctx, words)
	case wordBranch, wordMerge, wordRelease, wordStatus:
		return refused(words, result.Say("%s is not in this version", words[0])), ""
	default:
		return refused(words, result.Say("%s is not a command", words[0])), ""
	}
}

// push runs `ils push` in the repository Git stands in.
func (a App) push(ctx context.Context, words []string) (r result.Result, gitDir string) {
	repo := git.Repo{Runner: a.Git}

	// Outside a repository there is no git dir for a line to name, and whatever else stops
	// this read stops the command's own first read too, whose result says so.
	gitDir, _ = repo.GitDir(ctx)

	run := a.Push
	if run == nil {
		run = landing.Push
	}

	deps := landing.Deps{Forge: github.NewForge(github.Runner{}), Repo: repo}

	return run(ctx, deps, landing.Request{Command: words[0], Words: words}), gitDir
}

// refused is the refusal of words that break the grammar.
func refused(words []string, reason result.Phrase) result.Result {
	command := ""
	if len(words) > 0 {
		command = words[0]
	}

	return result.Result{
		Command: command,
		Words:   words,
		Outcome: result.Refused,
		Reason:  reason,
		Next:    result.NextCommand("--help"),
	}
}

// print writes text to Stdout and returns code, or 1 when the write fails.
func (a App) print(text string, code int) int {
	_, err := io.WriteString(a.Stdout, text)
	if err != nil {
		return a.fail(fmt.Errorf("write the result: %w", err))
	}

	return code
}

// fail reports err on Stderr and returns the exit code of a command that broke.
func (a App) fail(err error) int {
	// Nothing is left to report a failed write to Stderr on.
	_, _ = fmt.Fprintf(a.Stderr, "ils: %v\n", err)

	return exitBroke
}

// The words of the grammar: the command of this version, the commands of later ones, and the flags.
const (
	wordPush    = "push"
	wordBranch  = "branch"
	wordMerge   = "merge"
	wordRelease = "release"
	wordStatus  = "status"

	flagJSON      = "--json"
	flagHelp      = "--help"
	flagHelpShort = "-h"
)

// summary lists every command, the ones of later versions included, so that typing one gets an
// answer.
const summary = `ils lands a branch on GitHub, one command per step.

Commands:
  ils push            push the branch, open its pull request on the first push, wait for the checks
  ils branch <name>   not in this version
  ils merge           not in this version
  ils release <X.Y.Z> not in this version
  ils status          not in this version

Flags, anywhere among the words:
  --json              print the result as one JSON object
  -h, --help          print this text, or a command's own after its name

Exit codes: 0 done, 1 broke, 2 refused, 4 pending, 5 delivered and not green.
Every command ends within 100 seconds of its launch.
`

// pushUsage names, under "By hand:", every git and gh call `ils push` makes, one line per call.
// A test runs push and holds the lines to the calls it recorded, in both directions: a call
// added to push without its line here fails, and so does a line no call stands behind.
const pushUsage = `ils push

Pushes the current branch to origin under its own name, never forced, opens its pull
request against the trunk on the first push, and waits for the checks of the pushed commit.
It refuses the trunk, a detached HEAD, a changed tracked file, a branch with no commit past
origin's trunk, a branch cut from another branch, a branch behind or diverged from origin's
copy, and a branch whose pull request already merged.

In the lines below <name> is the branch, <trunk> is origin's default branch, and <r> is the
repository as <host>/<owner>/<repo>, read from origin's URL.

By hand:
  git rev-parse --absolute-git-dir
  git remote get-url origin
  git remote get-url --push origin
  git symbolic-ref --quiet HEAD
  gh auth status --hostname <host>
  gh repo view <r> --json isFork,viewerPermission
  git ls-remote --symref origin HEAD
  git status --porcelain=v1 -z --untracked-files=no
  git fetch origin <trunk>
  git rev-parse FETCH_HEAD
  git rev-parse HEAD
  git show-ref --verify --quiet refs/heads/<name>
  git log --reverse --topo-order --format='%H %s' --no-merges <sha> ^origin/<trunk>
  git for-each-ref --format='%(refname)' refs/heads/ --contains <sha>
  git merge-base --is-ancestor <sha> <name>
  git ls-remote --exit-code origin refs/heads/<name>
  git fetch origin <name>
  git rev-list --left-right --count <sha>...<sha>
  gh pr list --repo <r> --head <name> --state open --json number,url,title,baseRefName,headRefOid,isDraft
  gh pr list --repo <r> --head <name> --state merged --json number,url,title,baseRefName,headRefOid,isDraft
  git fetch origin refs/pull/<N>/head
  git log --reverse --topo-order --format='%H %s' --no-merges --cherry-pick --right-only <sha>...<sha> ^origin/<trunk>
  git push -u origin <name>
  gh pr create --repo <r> --title <name> --body '' --base <trunk> --head <name>
  gh api --hostname <host> repos/<owner>/<repo>/rules/branches/<trunk>
  gh pr view --repo <r> <N> --json headRefOid,mergeable,mergeStateStatus,isDraft,reviewDecision
  gh pr checks --repo <r> <N> --json name,bucket,workflow,link,state
  gh run list --repo <r> --commit <sha> --json databaseId,status,event,name,workflowName
  gh run view --repo <r> <id> --log-failed

Then, by hand:
  gh pr edit --repo <r> <N> --title … --body …
  gh pr merge --repo <r> <N> --squash --delete-branch --match-head-commit <sha> --subject "<title> (#<N>)" --body "<body>"
`

// usage returns the text for the words typed beside a help flag: the command's own after its
// name, and otherwise the summary followed by the text of each command of this version.
func usage(words []string) string {
	if len(words) > 0 && words[0] == wordPush {
		return pushUsage
	}

	return summary + "\n" + pushUsage
}
