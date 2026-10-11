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
