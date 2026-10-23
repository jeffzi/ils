// Package cli holds the grammar and the help of ils: it parses the words, runs the command they
// name under the one deadline, and renders its result.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"charm.land/fang/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/jeffzi/ils/internal/child"
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
	// buildInfo reads the build info that an empty Version falls back to; nil reads
	// debug.ReadBuildInfo.
	buildInfo func() (*debug.BuildInfo, bool)
	// StderrWidth returns the width in columns of the terminal stderr is, read at each frame of the
	// spinner; nil, or a width below 1, counts as 80.
	StderrWidth func() int
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
	ctx, cancel := context.WithDeadline(ctx, a.Launch.Add(bound))
	defer cancel()

	p := &parse{
		app: a, words: slices.DeleteFunc(slices.Clone(args), isOutputFlag), encoded: lastForm(args, flagJSON),
		noColor: lastForm(args, flagNoColor),
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
	return isTerminal && lookup(a.Environ, "TERM") != "dumb"
}

// ttyForce is the variable through which colorprofile is told that a stream is a terminal.
const ttyForce = "TTY_FORCE"

// profile returns the color profile of a stream that is a terminal or not, read from Environ: the
// plain profile when noColor or NO_COLOR asks for it, and colorprofile's reading otherwise, where
// TERM=dumb makes a terminal count as none.
//
// NO_COLOR is checked here because colorprofile honors it only on a terminal, and lets
// CLICOLOR_FORCE color a stream that is none.
func (a App) profile(terminal, noColor bool) colorprofile.Profile {
	if noColor || lookup(a.Environ, "NO_COLOR") != "" {
		return colorprofile.NoTTY
	}

	// colorprofile tells a terminal by its file descriptor or by TTY_FORCE. Whether the stream is a
	// terminal is decided by terminal alone, so a TTY_FORCE of the environment never counts.
	env := slices.DeleteFunc(slices.Clone(a.Environ), func(entry string) bool {
		return strings.HasPrefix(entry, ttyForce+"=")
	})
	if a.terminal(terminal) {
		env = append(env, ttyForce+"=1")
	}

	return colorprofile.Detect(io.Discard, env)
}

// lookup returns the value of key in env, the last one when env sets it more than once, and ""
// when env does not set it.
func lookup(env []string, key string) string {
	for _, entry := range slices.Backward(env) {
		if value, ok := strings.CutPrefix(entry, key+"="); ok {
			return value
		}
	}

	return ""
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

	body := strings.TrimSuffix(out, "\n")
	last := body[strings.LastIndex(body, "\n")+1:]
	hinted := a.terminal(a.StdoutTerminal) && r.Outcome == result.Pending
	if hinted && strings.HasPrefix(ansi.Strip(last), "Next: ") {
		out = body + "  " + result.PenFor(profile).Styled(ansi.NewStyle().Faint(), pendingHint) + "\n"
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
	for _, flag := range []string{flagDebug, flagDebugShort, flagNoColor} {
		if word == flag || strings.HasPrefix(word, flag+"=") {
			return true
		}
	}

	return isForm(word, flagJSON)
}

// lastForm reports whether the words typed turn flag on: the last of its forms the grammar takes
// among them decides, and none leaves it off.
//
// It reads the words and not the parsed flag, since parsing stops at the first flag it refuses
// and a refusal is still rendered as the words after it ask.
func lastForm(args []string, flag string) bool {
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
	result *result.Result
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
	// outProfile is the color profile of Stdout, read once per run.
	outProfile colorprofile.Profile
	// encoded is whether the words typed select the JSON renderer, and noColor whether they turn
	// color off.
	encoded, noColor bool
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
	boolFlag(root.Flags(), &p.version, flagNames{long: flagVersion}, "Print the version of ils")

	p.addOutputFlags(root)

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

// addOutputFlags adds to root, for every command under it, the flags that change how a command
// prints and nothing it does: --json, -d/--debug and --no-color.
func (p *parse) addOutputFlags(root *cobra.Command) {
	flags := root.PersistentFlags()

	boolFlag(flags, new(strictBool), flagNames{long: flagJSON}, "Print the result as one JSON object")
	boolFlag(flags, &p.debug, flagNames{long: flagDebug, short: flagDebugShort},
		"Trace each program ils runs and each decision of the wait on stderr")
	boolFlag(flags, new(strictBool), flagNames{long: flagNoColor}, "Print without color, as NO_COLOR does")
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
			p.land(c.Context(), run, landing.Request{Command: help.word, Words: p.words})

			return nil
		},
	}
	c.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return p.flagRefusal(err, extra) })

	return c
}

// land runs the landing command run for req against the repository ils runs in and its GitHub
// origin, and keeps its result, the repository's git dir and the step lines it printed. With debug
// on, the trace gets every git and gh call, the read of the git dir included, and every decision
// of the wait. The display is told how the command advances, and is gone from the terminal by the
// time land returns.
func (p *parse) land(ctx context.Context, run Command, req landing.Request) {
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

	r := func() result.Result {
		shown.start(ctx, gitDir)
		defer shown.end()

		return run(ctx, deps, req)
	}()

	p.result, p.gitDir, p.printed = &r, gitDir, shown.printed
}

// logger returns the logger of the debug trace on Stderr, drawn for profile, when the debug flag is
// on, and nil when it is off.
func (p *parse) logger(profile colorprofile.Profile) *slog.Logger {
	if !p.debug {
		return nil
	}

	a := p.app

	return slog.New(trace{w: a.Stderr, launch: a.Launch, pen: result.PenFor(profile), mu: &sync.Mutex{}})
}

// trace is the log handler of the debug trace: it writes each record as one line on w, timed from
// launch. A git or gh call reads `<tool> <args> exit <code> <duration>`, with the failure in place
// of `exit <code>` for a call that did not exit; any other record reads its message, then each
// attribute as key=value. When styled, the time and the duration are dim.
type trace struct {
	w      io.Writer
	launch time.Time
	// mu keeps the lines of calls logged at once from interleaving.
	mu    *sync.Mutex
	group string
	attrs []slog.Attr
	pen   result.Pen
}

// Enabled reports that every level is written.
func (trace) Enabled(context.Context, slog.Level) bool {
	return true
}

// Handle writes r as one line, and returns the error of the write.
func (h trace) Handle(_ context.Context, r slog.Record) error {
	attrs := slices.Clone(h.attrs)
	r.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, h.grouped(attr))

		return true
	})

	body := r.Message
	if call, ok := h.call(r.Message, attrs); ok {
		body = call
	} else {
		for _, attr := range attrs {
			body += " " + attr.Key + "=" + attr.Value.String()
		}
	}

	line := h.dim(fmt.Sprintf("+%.3fs", r.Time.Sub(h.launch).Seconds())) + " " + body + "\n"

	h.mu.Lock()
	defer h.mu.Unlock()

	if _, err := io.WriteString(h.w, line); err != nil {
		return fmt.Errorf("write a debug line: %w", err)
	}

	return nil
}

// call returns the line body of a record of one git or gh call, and false when the record is not
// one.
func (h trace) call(message string, attrs []slog.Attr) (string, bool) {
	if message != child.ExecMessage {
		return "", false
	}

	var (
		tool, end string
		args      []string
		took      time.Duration
	)

	for _, attr := range attrs {
		switch attr.Key {
		case child.KeyTool:
			tool = attr.Value.String()
		case child.KeyArgs:
			args, _ = attr.Value.Any().([]string)
		case child.KeyExit:
			end = "exit " + attr.Value.String()
		case child.KeyError:
			end = attr.Value.String()
		case child.KeyDuration:
			took, _ = attr.Value.Any().(time.Duration)
		}
	}

	return strings.Join(append([]string{tool}, args...), " ") + " " + end + " " + h.dim(took.Round(time.Millisecond).String()), true
}

// dim returns text drawn dim when the trace is styled.
func (h trace) dim(text string) string {
	return h.pen.Styled(ansi.NewStyle().Faint(), text)
}

// grouped returns attr with its key under the group of h.
func (h trace) grouped(attr slog.Attr) slog.Attr {
	attr.Key = h.group + attr.Key

	return attr
}

// WithAttrs returns a handler that writes attrs on every line after the record's own message.
func (h trace) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = slices.Clone(h.attrs)
	for _, attr := range attrs {
		h.attrs = append(h.attrs, h.grouped(attr))
	}

	return h
}

// WithGroup returns a handler that writes the key of every attribute after it under name.
func (h trace) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	h.group += name + "."

	return h
}

// display shows how a landing command advances on the terminals ils prints to: one spinner line
// for the running step on stderr, and each step line on stdout as soon as its step succeeds. A
// stream that gets neither is left untouched. A failed write is not reported: the display never
// changes the result or the exit code, and a step line that could not be written is not printed.
type display struct {
	// stdout and stderr are nil when the stream gets no step line and no spinner.
	stdout, stderr io.Writer
	width          func() int
	stop           chan struct{}
	// began is when the running step began: the last Started, or the last Said after it.
	began  time.Time
	gitDir string
	// label is the running step drawn for stderr, and empty while no step runs.
	label string
	// printed are the plain texts of the step lines written on stdout.
	printed                []string
	limit                  time.Duration
	frame, done, total     int
	wg                     sync.WaitGroup
	mu                     sync.Mutex
	outProfile, errProfile colorprofile.Profile
	// drawn is whether a spinner line stands on stderr.
	drawn bool
}

const (
	// frameEvery is how often the spinner turns and its timer is redrawn.
	frameEvery = 100 * time.Millisecond

	// fallbackWidth is the width in columns of a terminal whose width is unknown.
	fallbackWidth = 80

	pendingHint = "# waits again; nothing to fix"
)

// display returns the display of a landing command run with the words typed, stderr drawn for
// errProfile: the spinner on a terminal stderr unless debug is on, and the step lines on a terminal
// stdout unless the result is JSON.
func (p *parse) display(errProfile colorprofile.Profile) *display {
	a := p.app
	d := &display{
		width:      a.StderrWidth,
		outProfile: p.outProfile,
		errProfile: errProfile,
	}

	if a.terminal(a.StdoutTerminal) && !p.encoded {
		d.stdout = a.Stdout
	}

	if a.terminal(a.StderrTerminal) && !bool(p.debug) {
		d.stderr = a.Stderr
	}

	return d
}

// start turns the spinner, if any, until end or until ctx ends; gitDir is the git dir the lines are
// checked against.
func (d *display) start(ctx context.Context, gitDir string) {
	d.gitDir = gitDir
	if d.stderr == nil {
		return
	}

	d.stop = make(chan struct{})
	d.wg.Go(func() {
		ticker := time.NewTicker(frameEvery)
		defer ticker.Stop()

		for {
			select {
			case <-d.stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.mu.Lock()
				d.frame++
				d.draw()
				d.mu.Unlock()
			}
		}
	})
}

// end stops the spinner and takes its line off stderr.
func (d *display) end() {
	if d.stop != nil {
		close(d.stop)
		d.wg.Wait()
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.clear()
}

// Started draws the spinner line of the step label names, timed from now.
func (d *display) Started(label result.Phrase, limit time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// A label the wording check rejects is not drawn; the landing tests keep every label in it.
	d.label, _ = renderLine(label, d.gitDir, d.errProfile)
	d.limit, d.done, d.total, d.began = limit, 0, 0, time.Now()
	d.draw()
}

// Said takes the spinner line off stderr and prints line on stdout as the running step's line,
// with the time since the last Started or Said, whichever came later.
func (d *display) Said(line result.Phrase) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.clear()
	d.label = ""

	// A line said without a step started for it is timed from the line before it.
	now := time.Now()
	took := now.Sub(d.began).Round(frameEvery).String()
	d.began = now

	if d.stdout == nil {
		return
	}

	text, err := renderLine(line, d.gitDir, d.outProfile)
	if err != nil {
		return
	}

	pen := result.PenFor(d.outProfile)
	green := ansi.NewStyle().ForegroundColor(ansi.Green)
	step := pen.Styled(green, "✓") + " " + text + " " + pen.Styled(green.Faint(), took) + "\n"

	if _, err := io.WriteString(d.stdout, step); err == nil {
		d.printed = append(d.printed, plainText(line, d.gitDir))
	}
}

// Counted redraws the spinner line with done of total checks concluded.
func (d *display) Counted(done, total int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.done, d.total = done, total
	d.draw()
}

// draw writes the spinner line of the running step over the one stderr shows, cut to one column
// short of the terminal's width so that it never wraps, and padded with spaces to that width so
// that nothing of the line before is left. The caller holds mu.
//
// It moves the cursor with a carriage return alone: no control sequence, which a plain profile
// never writes.
func (d *display) draw() {
	if d.stderr == nil || d.label == "" {
		d.clear()

		return
	}

	pen := result.PenFor(d.errProfile)
	yellow := ansi.NewStyle().ForegroundColor(ansi.Yellow)
	frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

	line := pen.Styled(yellow, string(frames[d.frame%len(frames)])) + " " + d.label + " " +
		pen.Styled(yellow, time.Since(d.began).Truncate(time.Second).String())
	if d.limit > 0 {
		line += " " + pen.Styled(ansi.NewStyle().Faint(), "of "+d.limit.Round(time.Second).String())
	}
	if d.total > 0 {
		line += fmt.Sprintf(", %d of %d done", d.done, d.total)
	}

	columns := d.columns()
	line = ansi.Truncate(line, columns, "")
	padding := strings.Repeat(" ", columns-ansi.StringWidth(line))
	if pen.Color() {
		line += ansi.ResetStyle
	}

	_, _ = io.WriteString(d.stderr, "\r"+line+padding)
	d.drawn = true
}

// clear takes the spinner line off stderr by writing spaces over it, leaving the cursor at the
// start of its row. The caller holds mu.
func (d *display) clear() {
	if !d.drawn {
		return
	}

	_, _ = io.WriteString(d.stderr, "\r"+strings.Repeat(" ", d.columns())+"\r")
	d.drawn = false
}

// columns returns how many columns the spinner line may take: one short of the terminal's width,
// read now, so that the cursor never reaches the last column; an unknown width counts as
// fallbackWidth.
func (d *display) columns() int {
	width := fallbackWidth
	if d.width != nil {
		if w := d.width(); w > 0 {
			width = w
		}
	}

	return width - 1
}

// renderLine returns line drawn for profile as the result renderer draws it, or the renderer's error.
func renderLine(line result.Phrase, gitDir string, profile colorprofile.Profile) (string, error) {
	out, err := result.RenderStyled(result.Result{Lines: []result.Phrase{line}, Next: result.NextNone()}, gitDir, profile)
	if err != nil {
		return "", fmt.Errorf("render the line of a step: %w", err)
	}

	return strings.TrimSuffix(out, "\n"), nil
}

// plainText returns the text of line as the text renderer prints it, and "" when it rejects it.
func plainText(line result.Phrase, gitDir string) string {
	out, _ := renderLine(line, gitDir, colorprofile.NoTTY)

	return out
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
	wordBranch  = "branch"
	wordRelease = "release"
	wordStatus  = "status"

	flagJSON    = "--json"
	flagHelp    = "--help"
	flagVersion = "--version"

	flagDebug      = "--debug"
	flagDebugShort = "-d"
	flagNoColor    = "--no-color"

	// wordDash is the terminator after which every word is a positional argument.
	wordDash = "--"
)
