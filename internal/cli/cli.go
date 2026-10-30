package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"charm.land/fang/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

// Command runs one landing command and returns its result.
type Command func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result

// App is one launch of ils: where it prints, when it was launched, its version, and the git it
// runs.
type App struct {
	Stdout io.Writer
	Stderr io.Writer
	// Environ is the process environment, read for the color profile of each stream: NO_COLOR,
	// CLICOLOR_FORCE and TERM among others.
	Environ []string
	// Version is the version of ils that `ils --version` prints; empty falls back to the version in
	// the binary's build info.
	Version string
	// runPush runs `ils push`; nil runs landing.Push.
	runPush Command
	// runMerge runs `ils merge`; nil runs landing.Merge.
	runMerge Command
	// runBranch runs `ils branch <name>`; nil runs landing.StartBranch.
	runBranch Command
	// runStatus runs `ils status`; nil runs landing.Status.
	runStatus Command
	// runRelease runs `ils release <version>`; nil runs landing.Release.
	runRelease Command
	// buildInfo reads the build info that an empty Version falls back to; nil reads
	// debug.ReadBuildInfo.
	buildInfo func() (*debug.BuildInfo, bool)
	// suspend stops the process when the stop signal of a terminal reaches it; nil sends the
	// process SIGSTOP.
	suspend func() error
	// StderrWidth returns the width in columns of the terminal stderr is, read at each frame of the
	// spinner; nil, or a width below 1, counts as 80.
	StderrWidth func() int
	// StderrForeground reports whether ils is the foreground job of the terminal stderr is, read at
	// each frame of the spinner. A job in the background still writes to its terminal, so while ils
	// is not the foreground job the spinner draws nothing. Nil counts as the foreground job.
	StderrForeground func() bool
	// Notify relays to c the signals sig, as signal.Notify does. While the spinner line shows, the
	// display listens through it for an interrupt, a termination or a quit, so that it can show the
	// cursor before the signal ends the process. From the start of a landing command on, ils also
	// listens through it for the stop signal of a terminal, so that it can take the spinner line
	// off stderr before the process stops. Nil listens for none.
	Notify func(c chan<- os.Signal, sig ...os.Signal)
	// Launch is when the process started; the deadline of every command counts from it.
	Launch time.Time
	// git runs every git call; its zero value runs in the working directory with the process's
	// environment.
	git git.Runner
	// StdoutTerminal and StderrTerminal report whether each stream is a terminal. TERM=dumb makes
	// either count as none.
	StdoutTerminal bool
	StderrTerminal bool
}

// Run parses args, the words after the program name, runs the command they name, prints its
// result on Stdout, and returns the exit code. Words that break the grammar are a refusal naming
// the word, and a request for help prints the help of the command and runs nothing.
//
// A result the renderer rejects is printed nowhere: the error goes to Stderr and the code is 1.
func (a App) Run(ctx context.Context, args []string) int {
	// The deadline is set once the command is known, by land: a push under the long wait runs under a
	// longer one.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	signals, stopListening := a.onSignal(cancel)
	defer stopListening()

	words := slices.DeleteFunc(slices.Clone(args), isOutputFlag)
	p := &parse{
		app:     a,
		words:   words,
		encoded: turnedOn(args, flagJSON),
		noColor: turnedOn(args, flagNoColor),
		signals: signals,
	}
	p.outProfile = a.profile(a.StdoutTerminal, p.noColor)

	root := p.root()
	// Cobra reads the words of the process itself when it is given none.
	root.SetArgs(append([]string{}, args...))
	root.SetOut(a.drawnAt(p.outProfile))
	root.SetErr(a.Stderr)

	// Fang queries the terminal behind the process's stdout for its background before it draws help
	// or an error, whatever the profile, and waits for the answer: a profile with no color has Cobra
	// run the words alone, with its own plain help, so that no query is ever written.
	var err error
	if !result.PenFor(p.outProfile).Color() {
		err = root.ExecuteContext(ctx)
	} else {
		// The error comes back from Execute, and is reported below as a result or on Stderr.
		silent := func(io.Writer, fang.Styles, error) {}
		err = fang.Execute(ctx, root,
			fang.WithoutCompletions(), fang.WithoutManpage(), fang.WithoutVersion(), fang.WithErrorHandler(silent),
			fang.WithColorSchemeFunc(fang.AnsiColorScheme))
	}

	refusal, isRefusal := errors.AsType[*refusedError](err)

	switch {
	case isRefusal:
		return a.show(refusal.result, "", p.encoded, p.outProfile)
	case err != nil:
		return a.fail(err)
	case p.result == nil:
		return result.Done.ExitCode()
	default:
		r := *p.result
		r.Lines = unprinted(r.Lines, p.printed, p.gitDir)

		return a.show(r, p.gitDir, p.encoded, p.outProfile)
	}
}

// drawnAt returns Stdout as Cobra and Fang write to it, help included: every escape sequence a
// write carries is brought down to profile, so that help is drawn no richer than a result.
//
// Fang, which runs only on a profile with color, draws help at the profile it reads from the writer
// it is given and from the process environment, never from Environ or --no-color. A Stdout that is
// a file stays one, so that Fang still finds a terminal behind it and styles help there.
func (a App) drawnAt(profile colorprofile.Profile) io.Writer {
	out := &colorprofile.Writer{Forward: a.Stdout, Profile: profile}
	if file, ok := a.Stdout.(term.File); ok {
		return drawnFile{File: file, out: out}
	}

	return out
}

type drawnFile struct {
	term.File
	out *colorprofile.Writer
}

// Write writes p to the file through out.
func (f drawnFile) Write(p []byte) (int, error) {
	n, err := f.out.Write(p)
	if err != nil {
		return n, fmt.Errorf("write to stdout: %w", err)
	}

	return n, nil
}

// terminal reports whether a stream that isTerminal counts as one: TERM=dumb makes it none.
func (a App) terminal(isTerminal bool) bool {
	kind, _ := git.EnvValue(a.Environ, "TERM")

	return isTerminal && kind != "dumb"
}

// ttyForce is the variable through which colorprofile is told that a stream is a terminal.
const ttyForce = "TTY_FORCE"

// profile returns the color profile of a stream that is a terminal or not, read from Environ: the
// plain profile when noColor or NO_COLOR asks for it, and colorprofile's reading otherwise, where
// TERM=dumb makes a terminal count as none.
//
// NO_COLOR is checked here because colorprofile honors it only on a terminal, and lets
// CLICOLOR_FORCE color a stream that is none.
func (a App) profile(isTerminal, noColor bool) colorprofile.Profile {
	if noColorEnv, _ := git.EnvValue(a.Environ, "NO_COLOR"); noColor || noColorEnv != "" {
		return colorprofile.NoTTY
	}

	// colorprofile tells a terminal by its file descriptor or by TTY_FORCE. Whether the stream is a
	// terminal is decided by isTerminal alone, so a TTY_FORCE of the environment never counts.
	env := slices.DeleteFunc(slices.Clone(a.Environ), func(entry string) bool {
		return strings.HasPrefix(entry, ttyForce+"=")
	})
	if a.terminal(isTerminal) {
		env = append(env, ttyForce+"=1")
	}

	return colorprofile.Detect(io.Discard, env)
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

// show prints r on Stdout as JSON, or as text drawn for profile, and returns the exit code of its
// outcome. On a terminal Stdout, the Next line of a pending result ends with a hint that the next
// run waits again.
func (a App) show(r result.Result, gitDir string, encoded bool, profile colorprofile.Profile) int {
	if encoded {
		out, err := result.RenderJSON(r, gitDir)
		if err != nil {
			return a.fail(err)
		}

		return a.print(out, r.Outcome.ExitCode())
	}

	out, err := result.RenderStyled(r, gitDir, profile)
	if err != nil {
		return a.fail(err)
	}

	if a.terminal(a.StdoutTerminal) && r.Outcome == result.Pending && !r.Next.None() {
		hint := result.PenFor(profile).Styled(result.FaintStyle(), pendingHint)
		out = strings.TrimSuffix(out, "\n") + "  " + hint + "\n"
	}

	return a.print(out, r.Outcome.ExitCode())
}

// isForm reports whether word is flag in one of the forms the grammar takes: bare, =true or
// =false.
func isForm(word, flag string) bool {
	return word == flag || word == flag+"=true" || word == flag+"=false"
}

// isOutputFlag reports whether word is the JSON flag in one of the forms the grammar takes, or the
// debug or no-color flag in any form, a value the grammar refuses included: none of them changes
// what a command does, so none is among the words of a result.
func isOutputFlag(word string) bool {
	return slices.ContainsFunc([]string{flagDebug, flagDebugShort, flagNoColor}, func(flag string) bool {
		return word == flag || strings.HasPrefix(word, flag+"=")
	}) || isForm(word, flagJSON)
}

// turnedOn reports whether the words typed turn flag on: the last of its forms the grammar takes
// among them decides, and none leaves it off.
//
// It reads the words and not the parsed flag, since parsing stops at the first flag it refuses
// and a refusal is still rendered as the words after it ask.
func turnedOn(args []string, flag string) bool {
	for _, word := range slices.Backward(args) {
		if isForm(word, flag) {
			return word != flag+"=false"
		}
	}

	return false
}

// parse is one parse of the words typed: the commands it builds refuse through an error and
// leave the result of the command that ran here.
type parse struct {
	// result is the result of the command that ran; nil when none did.
	result  *result.Result
	signals *endSignals
	// gitDir is the resolved git dir when the command ran in a repository.
	gitDir string
	// words are the words typed, without the JSON, debug and no-color flags.
	words []string
	// printed are the plain texts of the step lines the run printed on Stdout as it went.
	printed []string
	app     App
	// version is read by root's Args so that `ils --version` passes with no command word.
	version strictBool
	debug   strictBool
	// wait is the --wait flag of push, the one command that registers it and so the one that may run
	// under the long wait.
	wait strictBool
	// outProfile is the color profile of Stdout, read once per run.
	outProfile colorprofile.Profile
	// encoded is whether the words typed select the JSON renderer, and noColor whether they turn
	// color off.
	encoded, noColor bool
}

// refusedError carries a refusal up through Cobra, which only knows errors.
type refusedError struct {
	result result.Result
}

// Error returns a fixed text; the refusal itself travels in the result field.
func (e *refusedError) Error() string {
	return "the words were refused"
}

// refuse refuses the words typed for reason, with the help of ils as the next step.
func (p *parse) refuse(reason result.Phrase) error {
	return p.refuseWith(reason, result.NextCommand(flagHelp))
}

// refuseWith refuses the words typed for reason, with next as the next step.
func (p *parse) refuseWith(reason result.Phrase, next result.Next) error {
	command := ""
	if len(p.words) > 0 {
		command = p.words[0]
	}

	req := landing.Request{Command: command, Words: p.words}

	return &refusedError{result: *req.Refused(reason, next)}
}

func notCommand(word string) result.Phrase {
	return result.Say("%s is not a command", word)
}

// notRootCommand refuses word as no command of root. A word close to exactly one command, as
// Cobra's suggestions judge it, names that command in the reason and its help as the next step:
// the help runs nothing, so following the next step after a typo cannot start a push or a merge.
func (p *parse) notRootCommand(root *cobra.Command, word string) error {
	suggested := root.SuggestionsFor(word)
	if len(suggested) != 1 {
		return p.refuse(notCommand(word))
	}

	return p.refuseWith(
		result.Say("%s is not a command; did you mean %s?", word, suggested[0]),
		result.NextCommand("%s "+flagHelp, suggested[0]),
	)
}

// firstWord returns the first word c was given: a leading `--` first, then the first word typed.
// It reports false when c was given no word.
func firstWord(c *cobra.Command, args []string) (string, bool) {
	if c.ArgsLenAtDash() == 0 {
		return wordDash, true
	}

	if len(args) > 0 {
		return args[0], true
	}

	return "", false
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

// root returns the command of no word, with every other command under it.
func (p *parse) root() *cobra.Command {
	root := &cobra.Command{
		Use:  "ils",
		Long: rootLong,
		// Every error comes back from Execute and is reported as a result or on Stderr, so Cobra
		// prints neither the error nor the usage.
		SilenceErrors: true,
		SilenceUsage:  true,
		// `ils completion` is refused as no command.
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		// Cobra asks Args only of a runnable command. Args refuses every run but `ils --version`,
		// which RunE prints.
		Args: func(c *cobra.Command, args []string) error {
			if word, ok := firstWord(c, args); ok {
				return p.notRootCommand(c, word)
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
		// Cobra defaults the distance only on the path ils bypasses, so SuggestionsFor needs it set.
		SuggestionsMinimumDistance: 2,
	}

	// The flag has no short form: -v stays free, and it is the root's own, so `ils push --version`
	// is an extra word of push.
	boolFlag(root.Flags(), &p.version, flagNames{long: flagVersion}, "Print the version of ils")

	p.addOutputFlags(root)

	// Cobra adds the help flag only after it has found the command, and until then takes the
	// word after a flag it does not know for that flag's value: `ils --help push` would print
	// the help of the root.
	root.InitDefaultHelpFlag()
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return p.flagRefusal(err, notCommand) })
	root.AddCommand(
		p.push(),
		p.landing(landingHelp{word: landing.MergeCommand, short: mergeShort, long: withTimeLimit(mergeLong), examples: mergeExamples}, orDefault(p.app.runMerge, landing.Merge)),
		p.landing(landingHelp{word: landing.StatusCommand, short: statusShort, long: statusLong, examples: statusExamples}, orDefault(p.app.runStatus, landing.Status)),
		p.named(landingHelp{word: landing.BranchCommand, arg: "name", short: branchShort, long: branchLong, examples: branchExamples}, orDefault(p.app.runBranch, landing.StartBranch)),
		p.release(),
	)

	return root
}

// addOutputFlags adds to root, for every command under it, the flags that change how a command
// prints and nothing it does: --json, -d/--debug and --no-color.
func (p *parse) addOutputFlags(root *cobra.Command) {
	flags := root.PersistentFlags()

	boolFlag(flags, new(strictBool), flagNames{long: flagJSON}, "Print the result as one JSON object")
	boolFlag(flags, &p.debug, flagNames{long: flagDebug, short: flagDebugShort},
		"Trace each program ils runs and each decision of the wait on stderr")
	boolFlag(flags, new(strictBool), flagNames{long: flagNoColor}, "Print without color, as NO_COLOR does")
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

// flagNames is the long and the short spelling of a flag, with their dashes. short is empty for a
// flag with no short form.
type flagNames struct {
	long, short string
}

// boolFlag registers value on flags under names as a flag that stands alone for true.
func boolFlag(flags *pflag.FlagSet, value pflag.Value, names flagNames, usage string) {
	flag := flags.VarPF(value, strings.TrimPrefix(names.long, "--"), strings.TrimPrefix(names.short, "-"), usage)
	flag.NoOptDefVal = "true"
}

// landingHelp is what the help says of a landing command; arg names the one word that follows
// the command, and is empty for a command that takes none.
type landingHelp struct {
	word, arg, short, long, examples string
}

// orDefault returns run, or fallback when run is nil.
func orDefault(run, fallback Command) Command {
	if run == nil {
		return fallback
	}

	return run
}

// landing returns the landing command help names, which takes no other word and runs run.
func (p *parse) landing(help landingHelp, run Command) *cobra.Command {
	extra := func(typed string) result.Phrase {
		return result.Say("%s takes no other word, and %s was given", help.word, typed)
	}

	c := &cobra.Command{
		Use:     help.word,
		Short:   help.short,
		Long:    help.long,
		Example: help.examples,
		Args: func(c *cobra.Command, args []string) error {
			if word, ok := firstWord(c, args); ok {
				return p.refuse(extra(word))
			}

			return nil
		},
		RunE: func(c *cobra.Command, _ []string) error {
			req := landing.Request{Command: help.word, Words: p.words}
			p.waitOn(c, &req)

			p.land(c.Context(), run, req)

			return nil
		},
	}
	c.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return p.flagRefusal(err, extra) })

	return c
}

// waitOn sets on req the --wait typed to c and whether c runs under the long wait: as --wait says
// when it was typed, and otherwise when stdout is a terminal. A command that registers no --wait,
// every one but push, keeps the short wait whatever stdout is.
func (p *parse) waitOn(c *cobra.Command, req *landing.Request) {
	flag := c.Flags().Lookup(strings.TrimPrefix(flagWait, "--"))
	if flag == nil {
		return
	}

	switch {
	case !flag.Changed:
		req.LongWait = p.app.terminal(p.app.StdoutTerminal)
	case bool(p.wait):
		req.LongWait, req.WaitFlag = true, landing.WaitFlagTrue
	default:
		req.WaitFlag = landing.WaitFlagFalse
	}
}

// push returns `ils push`, which runs the push command of the app, or landing.Push when it has
// none, under the long wait when --wait is typed, or when stdout is a terminal and no --wait is.
func (p *parse) push() *cobra.Command {
	c := p.landing(landingHelp{
		word: landing.PushCommand, short: pushShort, long: withTimeLimits(pushLong), examples: pushExamples,
	}, orDefault(p.app.runPush, landing.Push))

	boolFlag(c.Flags(), &p.wait, flagNames{long: flagWait}, withTimeLimits(waitUsage))

	return c
}

// named returns the landing command help names, which takes one help.arg and no other word and
// runs run, with the word as typed as the one argument of its request.
func (p *parse) named(help landingHelp, run Command) *cobra.Command {
	extra := func(typed string) result.Phrase {
		return result.Say("%s takes one %s and no other word, and %s was given", help.word, help.arg, typed)
	}

	c := &cobra.Command{
		Use:     help.word + " <" + help.arg + ">",
		Short:   help.short,
		Long:    help.long,
		Example: help.examples,
		Args: func(c *cobra.Command, args []string) error {
			// `--` in place of the name, or right after it, is the first word beyond the name.
			if dash := c.ArgsLenAtDash(); dash == 0 || dash == 1 {
				return p.refuse(extra(wordDash))
			}

			if len(args) > 1 {
				return p.refuse(extra(args[1]))
			}

			if len(args) == 0 {
				return p.refuse(result.Say("%s takes one %s, and none was given", help.word, help.arg))
			}

			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			p.land(c.Context(), run, landing.Request{Command: help.word, Words: p.words, Args: args})

			return nil
		},
	}
	c.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return p.flagRefusal(err, extra) })

	return c
}

// release returns `ils release <version>`, which runs the release command of the app, or
// landing.Release when it has none, with --force and --tag-only set on its request as typed.
func (p *parse) release() *cobra.Command {
	run := orDefault(p.app.runRelease, landing.Release)

	var force, tagOnly strictBool

	withFlags := func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result {
		req.Force, req.TagOnly = bool(force), bool(tagOnly)

		return run(ctx, deps, req)
	}

	c := p.named(landingHelp{
		word: landing.ReleaseCommand, arg: "version",
		short: releaseShort, long: withTimeLimit(releaseLong), examples: releaseExamples,
	}, withFlags)

	boolFlag(c.Flags(), &force, flagNames{long: flagForce}, "Tag the release commit without waiting for its CI")
	boolFlag(c.Flags(), &tagOnly, flagNames{long: flagTagOnly}, "Stop once the tag is pushed, creating no GitHub release")

	return c
}

// land runs the landing command run for req against the repository ils runs in and its GitHub
// origin, until req's time limit from launch, and keeps its result, the repository's git dir and
// the step lines it printed. With debug on, the trace gets every git and gh call, the read of the
// git dir included, and every decision of the wait. The display is told how the command advances, and is gone from the terminal by the
// time land returns, or by the time an interrupt, a termination or a quit ends the process, or the
// stop signal of a terminal stops it.
func (p *parse) land(ctx context.Context, run Command, req landing.Request) {
	ctx, cancel := context.WithDeadline(ctx, p.app.Launch.Add(req.Limit()))
	defer cancel()

	errProfile := p.app.profile(p.app.StderrTerminal, p.noColor)
	logger := p.logger(errProfile)
	runner := p.app.git
	runner.Logger = logger
	repo := git.Repo{Runner: runner}

	// Outside a repository there is no git dir for a line to name, and whatever else stops
	// this read stops the command's own first read too, whose result says so.
	gitDir, _ := repo.GitDir(ctx)

	shown := p.display(errProfile)
	deps := landing.Deps{Forge: &github.Forge{Logger: logger}, Repo: repo, Logger: logger, Progress: shown}

	p.app.onStop(shown)

	r := func() result.Result {
		// The display is handed to the listener before it starts and taken back only after end has
		// cleared its line, so that no signal in between ends ils with the cursor hidden.
		defer p.signals.during(shown.drop)()

		shown.start(ctx, gitDir)
		defer shown.end()

		return run(ctx, deps, req)
	}()

	p.result, p.gitDir, p.printed = &r, gitDir, shown.printed
}

// endSignals is the one listener of a run for the interrupt, the termination and the quit of the
// process, from the start of Run to its end, so that every one of these signals is counted alike
// whenever it lands.
type endSignals struct {
	// leave takes the spinner line off stderr; nil while no display runs.
	leave func()
	mu    sync.Mutex
}

// onSignal listens through Notify, for nothing when Notify is nil, for the interrupt, the
// termination and the quit of the process until the returned stop is called. The first interrupt
// or termination calls interrupt, which ends the run, so that ils prints the result of the step it
// stopped. Every other of these signals calls the leave of the display that runs, if one does,
// then ends the process as the signal would have had ils never listened.
//
// A process that started with the interrupt ignored, as a background job of a non-interactive
// shell does, is not listened to for it: Notify would take the ignore back, so the interrupt is
// left out and goes on being ignored.
func (a App) onSignal(interrupt func()) (signals *endSignals, stop func()) {
	signals = &endSignals{}
	if a.Notify == nil {
		return signals, func() {}
	}

	landed := make(chan os.Signal, 1)
	done := make(chan struct{})

	listened := []os.Signal{syscall.SIGTERM, syscall.SIGQUIT}
	if !signal.Ignored(os.Interrupt) {
		listened = append(listened, os.Interrupt)
	}

	a.Notify(landed, listened...)

	var wg sync.WaitGroup
	wg.Go(func() {
		spare := true
		for {
			sig, ok := nextSignal(landed, done)
			if !ok {
				return
			}
			if spare && sig != syscall.SIGQUIT {
				spare = false
				interrupt()

				continue
			}
			signals.leaveDisplay()
			signal.Stop(landed)
			die(sig)

			return
		}
	})

	return signals, func() {
		signal.Stop(landed)
		close(done)
		wg.Wait()
	}
}

// during makes leave the call that takes the display off stderr before a signal ends the process,
// until the returned done is called.
func (s *endSignals) during(leave func()) (done func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.leave = leave

	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		s.leave = nil
	}
}

// leaveDisplay takes the display that runs, if one does, off stderr.
func (s *endSignals) leaveDisplay() {
	s.mu.Lock()
	leave := s.leave
	s.mu.Unlock()

	if leave != nil {
		leave()
	}
}

// nextSignal returns the next signal on signals, or false when done is closed first and no signal
// is left.
func nextSignal(signals <-chan os.Signal, done <-chan struct{}) (os.Signal, bool) {
	select {
	case sig := <-signals:
		return sig, true
	case <-done:
		// stop detached the channel before closing done, so a signal that reached it before
		// stop was called is already buffered; select picks at random when both are ready.
		select {
		case sig := <-signals:
			return sig, true
		default:
			return nil, false
		}
	}
}

// die sends sig, which no channel is notified of any longer, to the process, so that it ends the
// process as it would have had ils never listened for it; a process that ignored sig before ils
// listened goes on ignoring it. Where sig cannot be sent, as on Windows, the process is killed.
//
// It panics when the process can be neither signaled nor killed.
func die(sig os.Signal) {
	self, err := os.FindProcess(os.Getpid())
	if err == nil && self.Signal(sig) != nil {
		err = self.Kill()
	}
	if err != nil {
		panic(fmt.Sprintf("end ils on %v: %v", sig, err))
	}
}

// unprinted returns lines without the ones already printed: each text in printed takes out the
// first line left that reads the same, so a line said twice is still printed once more.
func unprinted(lines []result.Phrase, printed []string, gitDir string) []result.Phrase {
	if len(printed) == 0 {
		return lines
	}

	left := slices.Clone(printed)
	var kept []result.Phrase

	for _, line := range lines {
		if i := slices.Index(left, plainText(line, gitDir)); i >= 0 {
			left = slices.Delete(left, i, i+1)

			continue
		}

		kept = append(kept, line)
	}

	return kept
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
	flagJSON    = "--json"
	flagHelp    = "--help"
	flagVersion = "--version"

	flagDebug      = "--debug"
	flagDebugShort = "-d"
	flagNoColor    = "--no-color"

	flagForce   = "--force"
	flagTagOnly = "--tag-only"
	flagWait    = "--wait"

	// wordDash is the terminator after which every word is a positional argument.
	wordDash = "--"

	pendingHint = "# waits again; nothing to fix"
)
