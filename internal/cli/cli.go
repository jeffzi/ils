// Package cli holds the grammar and the help of ils: it parses the words, runs the command they
// name under the one deadline, and renders its result.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"charm.land/fang/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

// bound is the time a whole command may take, counted from launch.
const bound = 100 * time.Second

// Command runs one landing command and returns its result.
type Command func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result

// App is one launch of ils: where it prints, when it was launched, its version, and the git it
// runs.
type App struct {
	Stdout io.Writer
	Stderr io.Writer
	// Version is the version of ils that `ils --version` prints; empty falls back to the version in
	// the binary's build info.
	Version string
	// runPush runs `ils push`; nil runs landing.Push.
	runPush Command
	// runMerge runs `ils merge`; nil runs landing.Merge.
	runMerge Command
	// buildInfo reads the build info that an empty Version falls back to; nil reads
	// debug.ReadBuildInfo.
	buildInfo func() (*debug.BuildInfo, bool)
	// Launch is when the process started; the deadline of every command counts from it.
	Launch time.Time
	// git runs every git call; its zero value runs in the working directory with the process's
	// environment.
	git git.Runner
}

// Run parses args, the words after the program name, runs the command they name, prints its
// result on Stdout, and returns the exit code. Words that break the grammar are a refusal naming
// the word, and a request for help prints the help of the command and runs nothing.
//
// A result the renderer rejects is printed nowhere: the error goes to Stderr and the code is 1.
func (a App) Run(ctx context.Context, args []string) int {
	ctx, cancel := context.WithDeadline(ctx, a.Launch.Add(bound))
	defer cancel()

	p := &parse{app: a, words: slices.DeleteFunc(slices.Clone(args), isJSONFlag)}

	root := p.root()
	// Cobra reads the words of the process itself when it is given none.
	root.SetArgs(append([]string{}, args...))
	root.SetOut(a.Stdout)
	root.SetErr(a.Stderr)

	// The error comes back from Execute, and is reported below as a result or on Stderr.
	silent := func(io.Writer, fang.Styles, error) {}
	err := fang.Execute(ctx, root,
		fang.WithoutCompletions(), fang.WithoutManpage(), fang.WithoutVersion(), fang.WithErrorHandler(silent),
		fang.WithColorSchemeFunc(fang.AnsiColorScheme))

	refusal, isRefusal := errors.AsType[*refusedError](err)
	encoded := asJSON(args)

	switch {
	case isRefusal:
		return a.show(refusal.result, "", encoded)
	case err != nil:
		return a.fail(err)
	case p.result == nil:
		return result.Done.ExitCode()
	default:
		return a.show(*p.result, p.gitDir, encoded)
	}
}

// Version returns the version of the main module that info, the build info of the running binary,
// names, without its leading v. It returns "(devel)" when ok is false, as [debug.ReadBuildInfo]
// reports for a binary built without module support, and when info names no version.
func Version(info *debug.BuildInfo, ok bool) string {
	const devel = "(devel)"

	if !ok || info.Main.Version == "" || info.Main.Version == devel {
		return devel
	}

	return strings.TrimPrefix(info.Main.Version, "v")
}

// printedVersion returns the version that ils --version prints: the App.Version field when it is
// set, otherwise the version of the build info.
func (a App) printedVersion() string {
	if a.Version != "" {
		return a.Version
	}

	if a.buildInfo == nil {
		return Version(debug.ReadBuildInfo())
	}

	return Version(a.buildInfo())
}

// show prints r on Stdout as text or as JSON and returns the exit code of its outcome.
func (a App) show(r result.Result, gitDir string, encoded bool) int {
	render := result.RenderText
	if encoded {
		render = result.RenderJSON
	}

	out, err := render(r, gitDir)
	if err != nil {
		return a.fail(err)
	}

	return a.print(out, r.Outcome.ExitCode())
}

// isJSONFlag reports whether word is the JSON flag in one of the forms the grammar takes.
func isJSONFlag(word string) bool {
	return word == flagJSON || word == flagJSONTrue || word == flagJSONFalse
}

// asJSON reports whether the words typed select the JSON renderer: the last form of the flag
// among them decides.
//
// It reads the words and not the parsed flag, since parsing stops at the first unknown flag and
// a refusal for that flag is still rendered as the words after it ask.
func asJSON(args []string) bool {
	for _, word := range slices.Backward(args) {
		if isJSONFlag(word) {
			return word != flagJSONFalse
		}
	}

	return false
}

// parse is one parse of the words typed: the commands it builds refuse through an error and
// leave the result of the command that ran here.
type parse struct {
	// result is the result of the command that ran; nil when none did.
	result *result.Result
	// gitDir is the resolved git dir when the command ran in a repository.
	gitDir string
	// words are the words typed, without the JSON flag.
	words []string
	app   App
	// version is read by root's Args so that `ils --version` passes with no command word.
	version strictBool
}

// refusedError carries a refusal up through Cobra, which only knows errors.
type refusedError struct {
	result result.Result
}

// Error returns a fixed text; the refusal itself travels in the result field.
func (e *refusedError) Error() string {
	return "the words were refused"
}

func (p *parse) refuse(reason result.Phrase) error {
	command := ""
	if len(p.words) > 0 {
		command = p.words[0]
	}

	req := landing.Request{Command: command, Words: p.words}

	return &refusedError{result: *req.Refused(reason, result.NextCommand(flagHelp))}
}

// root returns the command of no word, with every other command under it.
func (p *parse) root() *cobra.Command {
	root := &cobra.Command{
		Use:  "ils",
		Long: rootLong,
		// Cobra asks Args only of a runnable command. Args refuses every run but `ils --version`,
		// which RunE prints.
		Args: func(c *cobra.Command, args []string) error {
			if c.ArgsLenAtDash() == 0 {
				return p.refuse(notCommand(wordDash))
			}

			if len(args) > 0 {
				return p.refuse(notCommand(args[0]))
			}

			if !p.version {
				return p.refuse(result.Say("no command was given"))
			}

			return nil
		},
		RunE: func(c *cobra.Command, _ []string) error {
			if _, err := fmt.Fprintf(c.OutOrStdout(), "ils version %s\n", p.app.printedVersion()); err != nil {
				return fmt.Errorf("write the version: %w", err)
			}

			return nil
		},
		Example: "ils push",
	}

	// The flag has no short form: -v stays free, and it is the root's own, so `ils push --version`
	// is an extra word of push.
	versionFlag := root.Flags().VarPF(&p.version, flagVersion[len("--"):], "", "Print the version of ils")
	versionFlag.NoOptDefVal = "true"

	jsonFlag := root.PersistentFlags().VarPF(new(strictBool), flagJSON[len("--"):], "", "Print the result as one JSON object")
	jsonFlag.NoOptDefVal = "true"

	// Cobra adds the help flag only after it has found the command, and until then takes the
	// word after a flag it does not know for that flag's value: `ils --help push` would print
	// the help of the root.
	root.InitDefaultHelpFlag()
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return p.flagRefusal(err, notCommand) })
	root.AddCommand(
		p.landing(landingHelp{word: landing.PushCommand, short: pushShort, long: withBound(pushLong), examples: pushExamples}, p.app.runPush, landing.Push),
		p.landing(landingHelp{word: landing.MergeCommand, short: mergeShort, long: withBound(mergeLong), examples: mergeExamples}, p.app.runMerge, landing.Merge),
	)

	for _, later := range []string{wordBranch, wordRelease, wordStatus} {
		root.AddCommand(&cobra.Command{
			Use:    later,
			Hidden: true,
			Args:   cobra.ArbitraryArgs,
			// The command is refused whatever follows it, an unknown flag included.
			FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
			RunE: func(c *cobra.Command, _ []string) error {
				return p.refuse(result.Say("%s is not in this version", c.Name()))
			},
		})
	}

	return root
}

// landingHelp is the word of a landing command and the help it prints.
type landingHelp struct {
	word, short, long, examples string
}

// landing returns the landing command help names, which takes no other word and runs run, or
// fallback when run is nil.
func (p *parse) landing(help landingHelp, run, fallback Command) *cobra.Command {
	if run == nil {
		run = fallback
	}

	extra := func(typed string) result.Phrase { return extraWord(help.word, typed) }

	c := &cobra.Command{
		Use:     help.word,
		Short:   help.short,
		Long:    help.long,
		Example: help.examples,
		Args: func(c *cobra.Command, args []string) error {
			if c.ArgsLenAtDash() == 0 {
				return p.refuse(extra(wordDash))
			}

			if len(args) > 0 {
				return p.refuse(extra(args[0]))
			}

			return nil
		},
		RunE: func(c *cobra.Command, _ []string) error {
			r, gitDir := p.app.land(c.Context(), run, landing.Request{Command: help.word, Words: p.words})
			p.result, p.gitDir = &r, gitDir

			return nil
		},
	}
	c.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return p.flagRefusal(err, extra) })

	return c
}

// extraWord is the reason command refuses word, which it was given besides its own.
func extraWord(command, word string) result.Phrase {
	return result.Say("%s takes no other word, and %s was given", command, word)
}

func notCommand(word string) result.Phrase {
	return result.Say("%s is not a command", word)
}

// flagRefusal turns a flag Cobra could not parse into a refusal: a value that is neither true
// nor false is named with its flag, and an unknown flag gets the reason unknown gives for the flag
// as it was typed. Any other error is returned as it is.
func (p *parse) flagRefusal(err error, unknown func(typed string) result.Phrase) error {
	if invalid, ok := errors.AsType[*pflag.InvalidValueError](err); ok {
		return p.refuse(result.Say("--%s takes true or false, and %s was given", invalid.GetFlag().Name, invalid.GetValue()))
	}

	missing, ok := errors.AsType[*pflag.NotExistError](err)
	if !ok {
		return err
	}

	// pflag names an unknown flag without its dashes; a group of short flags is named whole.
	typed := "--" + missing.GetSpecifiedName()
	if group := missing.GetSpecifiedShortnames(); group != "" {
		typed = "-" + group
	}

	return p.refuse(unknown(typed))
}

// strictBool is the value of a flag that takes no value but true and false.
type strictBool bool

// String returns "true" or "false".
func (b *strictBool) String() string {
	return fmt.Sprint(bool(*b))
}

// Set accepts only "true" and "false" and returns an error for any other value.
func (b *strictBool) Set(value string) error {
	switch value {
	case "true":
		*b = true
	case "false":
		*b = false
	default:
		return errors.New("neither true nor false")
	}

	return nil
}

// Type returns "bool" so pflag's help shows the flag without a value placeholder.
func (*strictBool) Type() string {
	return "bool"
}

// land runs the landing command run for req against the repository ils runs in and its GitHub
// origin, and returns its result with the repository's git dir.
func (a App) land(ctx context.Context, run Command, req landing.Request) (r result.Result, gitDir string) {
	repo := git.Repo{Runner: a.git}

	// Outside a repository there is no git dir for a line to name, and whatever else stops
	// this read stops the command's own first read too, whose result says so.
	gitDir, _ = repo.GitDir(ctx)

	deps := landing.Deps{Forge: &github.Forge{}, Repo: repo}

	return run(ctx, deps, req), gitDir
}

// print writes text to Stdout and returns code, or 1 when the write fails.
func (a App) print(text string, code int) int {
	_, err := io.WriteString(a.Stdout, text)
	if err != nil {
		return a.fail(fmt.Errorf("write the result: %w", err))
	}

	return code
}

func (a App) fail(err error) int {
	// Nothing is left to report a failed write to Stderr on.
	_, _ = fmt.Fprintf(a.Stderr, "ils: %v\n", err)

	// The result itself could not be printed.
	return result.Broke.ExitCode()
}

const (
	wordBranch  = "branch"
	wordRelease = "release"
	wordStatus  = "status"

	flagJSON      = "--json"
	flagJSONTrue  = flagJSON + "=true"
	flagJSONFalse = flagJSON + "=false"
	flagHelp      = "--help"
	flagVersion   = "--version"

	// wordDash is the terminator after which every word is a positional argument.
	wordDash = "--"
)

// withBound returns long with its one %d verb set to bound in seconds, so that the help says the
// limit the command enforces.
func withBound(long string) string {
	return fmt.Sprintf(long, int(bound.Seconds()))
}

// The help says how to call a command and nothing else: what to do with an exit code and with
// the next step is said by the result of each run.
const (
	rootLong = `ils lands a branch on GitHub in fixed, checked steps: push, pull request,
CI verdict. It exists so that a coding agent cannot push to the wrong
place, skip the checks, or call a red build done.

Docs: https://github.com/jeffzi/ils`

	pushShort = "Push the branch, open its pull request, wait for the checks"
	pushLong  = `Push the current branch, open its pull request on the first push, and
wait up to %d seconds for the checks of the pushed commit. It never
forces a push and it does not merge.

It refuses before pushing anything when the branch is not ready, for
example on the trunk (origin's default branch), with uncommitted
changes, or behind origin's copy of it. The output gives the reason and
the next step.`

	pushExamples = `# Push and wait for the checks
ils push

# The same result as one JSON object
ils push --json`

	mergeShort = "Squash and merge the pull request once its checks are green"
	mergeLong  = `Squash-merge the pull request of the current branch once its checks are
green, within %d seconds of the launch.

It squash-merges the pull request once its checks are green, deletes the
branch on origin and in the clone, and leaves the trunk checked out at
origin's tip. It never pushes: push the branch with ils push first.

It refuses before merging anything when the pull request is not ready,
for example when a check is red, when it is a draft, or when its title is
not a conventional commit title. The output gives the reason and the next
step.`

	mergeExamples = `# Merge once the checks are green
ils merge

# The same result as one JSON object
ils merge --json`
)
