//go:build unix

package cli

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/guard"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

const (
	flagCwd = "--cwd"

	// hookCommand reads -h itself because it disables Cobra flag parsing.
	flagHelpShort = landing.FlagHelpShort

	cwdUsage = "The directory the command runs in"

	// harnessClaude is the harness word of Claude Code, the one harness `ils hook` knows.
	harnessClaude = "claude"

	// claudeEvent is the hookEventName of the answer Claude Code reads from a PreToolUse hook.
	claudeEvent = "PreToolUse"

	// claudeDecisionAsk is the permissionDecision that has Claude Code ask the user first.
	claudeDecisionAsk = "ask"

	// claudeShellTool is the tool_name of Claude Code's shell tool, the one tool the guard judges.
	claudeShellTool = "Bash"

	// notLiteral stands, in the text of a parse, for a word whose text depends on what the shell
	// expands.
	notLiteral = "<not literal>"

	// unknownText stands, in the text of a parse, for a directory or git dir that is not known.
	unknownText = "unknown"
)

// ---------------------------------------------------------------------------
// The parse and guard commands
// ---------------------------------------------------------------------------

// parseCommand returns `ils parse --cwd <dir> -- '<command>'`, which reads the command as the
// guard does and keeps every invocation it holds as the result. It refuses words that give no
// directory, or anything but one word after `--`; it never refuses what the command holds.
func (p *parse) parseCommand() *cobra.Command {
	return p.cwdCommand(parseHeader(), parseWord, p.readCommand)
}

// cwdCommand completes c as the command named word that takes `--cwd <dir> -- '<command>'`: it
// refuses words that give no directory, or anything but one word after `--`, naming word and
// pointing at the help of word. Otherwise it keeps the result that run returns for the directory
// and the command as the result of the run.
func (p *parse) cwdCommand(
	c *cobra.Command,
	word string,
	run func(ctx context.Context, cwd, command string) (result.Result, error),
) *cobra.Command {
	var cwd string

	refuse := func(reason result.Phrase) error {
		return p.refuseWith(reason, result.NextCommand("%s "+flagHelp, word))
	}
	oneWord := result.Say("%s takes the command after %s as one quoted word", word, wordDash)
	given := func(typed string) result.Phrase {
		return result.Say("%s takes %s and the command after %s as one quoted word, and %s was given",
			word, flagCwd, wordDash, typed)
	}

	c.Args = func(c *cobra.Command, args []string) error {
		if c.ArgsLenAtDash() != 0 || len(args) != 1 {
			return refuse(oneWord)
		}

		if cwd == "" {
			return refuse(result.Say("%s takes the directory the command runs in as %s", word, flagCwd))
		}

		return nil
	}
	c.RunE = func(c *cobra.Command, args []string) error {
		r, err := run(c.Context(), cwd, args[0])
		if err != nil {
			return err
		}

		p.result = &r

		return nil
	}
	c.Flags().StringVar(&cwd, strings.TrimPrefix(flagCwd, "--"), "", cwdUsage)
	c.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		if _, ok := errors.AsType[*pflag.ValueRequiredError](err); ok {
			return refuse(result.Say("%s takes a directory, and none was given", flagCwd))
		}

		return p.flagRefusal(err, given)
	})

	return c
}

// caller returns the Caller of the guard for the directory dir, with the process environment and
// its HOME, and the git of the app.
func (p *parse) caller(dir string) guard.Caller {
	env := environment(p.app.Environ)
	logger := p.logger(p.app.profile(p.app.StderrTerminal, p.noColor))
	runner := p.app.git
	runner.Logger = logger

	return guard.Caller{Dir: dir, Env: env, Home: env["HOME"], Git: runner, GH: github.Runner{Logger: logger}}
}

// readCommand reads command as the guard does from cwd, made absolute, with the process environment
// and its HOME, through the git of the app, under the time limit of a command from launch. It
// returns the result that shows every invocation, or, when the command does not parse or a read of
// git fails, a broke result that says why.
func (p *parse) readCommand(ctx context.Context, cwd, command string) (result.Result, error) {
	req := landing.Request{Command: parseWord, Words: p.words}

	ctx, cancel := p.underTimeLimit(ctx)
	defer cancel()

	dir, err := filepath.Abs(cwd)
	if err != nil {
		return result.Result{}, fmt.Errorf("resolve the directory %s: %w", cwd, err)
	}

	invocations, err := guard.Parse(ctx, command, p.caller(dir))
	if err != nil {
		return unparsed(req, err), nil
	}

	payload := &result.Parse{Invocations: invocationsOf(invocations)}
	lines := make([]result.Phrase, 0, len(invocations))

	for _, shown := range payload.Invocations {
		lines = append(lines, invocationLine(shown))
	}

	return result.Result{
		Payload: &result.Payload{Parse: payload},
		Command: req.Command,
		Words:   req.Words,
		Lines:   lines,
		Next:    result.NextNone(),
		Outcome: result.Done,
	}, nil
}

// unparsed returns the broke result of a parse that failed with err: the command did not parse, or
// a read of git it needs failed. The error's text, which may name a config key or a path under a git
// dir, goes in as a name.
func unparsed(req landing.Request, err error) result.Result {
	reason := result.Say("a read of git the command needs failed: %s", err.Error())
	if _, ok := errors.AsType[*guard.ParseError](err); ok {
		reason = result.Say("the command does not parse: %s", err.Error())
	}

	return result.Result{
		Command: req.Command,
		Words:   req.Words,
		Reason:  reason,
		Next:    result.NextStep("fix what the reason names, then run %s again", result.Command(parseWord)),
		Outcome: result.Broke,
	}
}

// guardCommand returns `ils guard --cwd <dir> -- '<command>'`, which judges the command as the
// guard does: the result is done when the guard allows it, refused when it refuses it, and ask when
// it asks first, with every invocation it holds. It refuses words that give no directory, or
// anything but one word after `--`.
func (p *parse) guardCommand() *cobra.Command {
	return p.cwdCommand(guardHeader(), guardWord, func(ctx context.Context, cwd, command string) (result.Result, error) {
		return p.judgeCommand(ctx, cwd, command), nil
	})
}

// judgeCommand judges command as the guard does from cwd, made absolute, with the process
// environment and its HOME, through the git of the app, under the time limit of a command from
// launch. The result carries the verdict as its outcome and reason, and every invocation the
// command holds. Anything that keeps the guard from judging the command is a refusal that says
// what broke, never a broke result: a caller that gates on the exit code must not let it run.
func (p *parse) judgeCommand(ctx context.Context, cwd, command string) result.Result {
	req := landing.Request{Command: guardWord, Words: p.words}

	ctx, cancel := p.underTimeLimit(ctx)
	defer cancel()

	dir, err := filepath.Abs(cwd)
	if err != nil {
		return judged(req, nil, cannotJudge("the guard could not resolve the directory %s: %v", cwd, err))
	}

	verdict, invocations := guard.Assess(ctx, command, p.caller(dir))

	return judged(req, invocations, verdict)
}

// judged returns the result of verdict on a command holding invocations. Its reason, which may name
// a config key or a path under a git dir, goes in as a name.
func judged(req landing.Request, invocations []guard.Invocation, verdict guard.Verdict) result.Result {
	payload := &result.Guard{Invocations: invocationsOf(invocations)}

	r := result.Result{
		Payload: &result.Payload{Guard: payload},
		Command: req.Command,
		Words:   req.Words,
		Next:    result.NextNone(),
	}

	switch verdict.Outcome {
	case guard.Allow:
		r.Outcome = result.Done
		r.Lines = []result.Phrase{result.Say("The guard allows the command.")}
	case guard.Ask:
		r.Outcome = result.Ask
		r.Reason = result.Say("%s", verdict.Reason)
	case guard.Refuse:
		fallthrough
	default:
		// A verdict outside the table is a defect, and a defect must not let the command run.
		r.Outcome = result.Refused
		r.Reason = result.Say("%s", verdict.Reason)
	}

	return r
}

// environment returns the variables of environ, each `NAME=value`, by name. An entry with no `=`
// names nothing, and the first entry of a name wins, as os.Getenv reads it.
func environment(environ []string) map[string]string {
	env := make(map[string]string, len(environ))
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}

		if _, seen := env[name]; !seen {
			env[name] = value
		}
	}

	return env
}

// underTimeLimit returns ctx with the deadline of a command from launch, which counts the time
// limit from the launch of the app.
func (p *parse) underTimeLimit(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithDeadline(ctx, p.app.Launch.Add(landing.TimeLimit))
}

// ---------------------------------------------------------------------------
// The payload and its text
// ---------------------------------------------------------------------------

// invocationsOf returns invocations as the payload shows them, never nil.
func invocationsOf(invocations []guard.Invocation) []result.Invocation {
	shown := make([]result.Invocation, 0, len(invocations))
	for _, inv := range invocations {
		shown = append(shown, invocationOf(inv))
	}

	return shown
}

// invocationOf returns inv as the parse payload shows it; only a git invocation has a GitCall.
func invocationOf(inv guard.Invocation) result.Invocation {
	shown := result.Invocation{Tool: string(inv.Tool), Words: wordsOf(inv.Words), Dir: inv.Dir}
	for _, mark := range inv.Marks {
		shown.Marks = append(shown.Marks, result.Mark{Kind: string(mark.Kind), Name: mark.Name})
	}

	if inv.Tool == guard.Git {
		shown.Git = &result.GitCall{
			Args:          wordsOf(inv.Git.Args),
			GitDir:        inv.Git.GitDir,
			GitDirUnknown: inv.Git.GitDirUnknown,
		}
		if !inv.Git.NoSubcommand {
			shown.Git.Subcommand = new(wordOf(inv.Git.Subcommand))
		}
	}

	return shown
}

func wordOf(w guard.Word) result.Word {
	return result.Word{Text: w.Text, Literal: w.Literal}
}

func wordsOf(words []guard.Word) []result.Word {
	shown := make([]result.Word, 0, len(words))
	for _, w := range words {
		shown = append(shown, wordOf(w))
	}

	return shown
}

// invocationLine returns the one line the text of a parse prints for inv: its words, its directory,
// its marks, and for git the subcommand when git runs one, its arguments and the git dir it
// targets.
func invocationLine(inv result.Invocation) result.Phrase {
	dir := inv.Dir
	if dir == "" {
		dir = unknownText
	}

	marks := make([]string, 0, len(inv.Marks))
	for _, mark := range inv.Marks {
		text := mark.Kind
		if mark.Name != "" {
			text += " (" + mark.Name + ")"
		}

		marks = append(marks, text)
	}

	words, markList := wordTexts(inv.Words), orNone(strings.Join(marks, ", "))
	if inv.Git == nil {
		return result.Say("%s; directory %s; marks %s", words, dir, markList)
	}

	gitDir := orNone(inv.Git.GitDir)
	if inv.Git.GitDirUnknown {
		gitDir = unknownText
	}

	gitPart := "arguments " + orNone(wordTexts(inv.Git.Args)) + "; git dir " + gitDir
	if inv.Git.Subcommand != nil {
		gitPart = "subcommand " + wordText(*inv.Git.Subcommand) + "; " + gitPart
	}

	return result.Say("%s; directory %s; marks %s; %s", words, dir, markList, gitPart)
}

// wordText returns the text of w, or notLiteral when w is not a literal.
func wordText(w result.Word) string {
	if !w.Literal {
		return notLiteral
	}

	return w.Text
}

// wordTexts returns the words one space apart, each one that is not a literal as notLiteral.
func wordTexts(words []result.Word) string {
	texts := make([]string, 0, len(words))
	for _, w := range words {
		texts = append(texts, wordText(w))
	}

	return strings.Join(texts, " ")
}

// orNone returns text, or none when it is empty.
func orNone(text string) string {
	if text == "" {
		return "none"
	}

	return text
}

// ---------------------------------------------------------------------------
// The hook command and the Claude Code payload
// ---------------------------------------------------------------------------

// hookCommand returns `ils hook <harness>`, which reads the payload of the harness's pre-tool hook
// on Stdin and answers it in the harness's terms. It never exits 1, since a harness may read 1 as
// letting the command run: words it does not take, and every error, end the run with exit 2 and
// the reason on Stderr.
func (p *parse) hookCommand() *cobra.Command {
	c := hookHeader()
	// Cobra would refuse a flag through the result, which prints on Stdout; the words are read
	// here instead, so that every refusal lands on Stderr.
	c.RunE = func(c *cobra.Command, args []string) error {
		if len(args) == 1 && (args[0] == flagHelp || args[0] == flagHelpShort) {
			return pflag.ErrHelp
		}

		refused := result.Refused.ExitCode()
		harnesses := "the harness it runs under, one of: " + harnessClaude

		switch {
		case len(args) == 0:
			p.quit(refused, hookWord+" takes "+harnesses)
		case args[0] != harnessClaude:
			p.quit(refused, fmt.Sprintf("%s takes %s, and %s was given", hookWord, harnesses, args[0]))
		case len(args) > 1:
			p.quit(refused, fmt.Sprintf("%s takes one harness and no other word, and %s was given", hookWord, args[1]))
		default:
			p.hookClaude(c.Context())
		}

		return nil
	}

	return c
}

// claudeToolUse is the part of the payload of Claude Code's PreToolUse hook that the guard reads
// before it knows the tool. Only a call of the shell tool has its ToolInput read, as
// claudeShellInput.
type claudeToolUse struct {
	ToolName  string         `json:"tool_name"`
	Cwd       string         `json:"cwd"`
	ToolInput jsontext.Value `json:"tool_input"`
}

// claudeShellInput is the tool_input of a call of the shell tool.
type claudeShellInput struct {
	Command string `json:"command"`
}

// claudeAsk is the answer on stdout that has Claude Code ask the user before the tool runs.
type claudeAsk struct {
	HookSpecificOutput claudeDecision `json:"hookSpecificOutput"`
}

type claudeDecision struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason"`
}

// hookClaude answers the payload of Claude Code's PreToolUse hook on Stdin with the verdict of the
// guard: allow is exit 0 with nothing printed, since Claude Code shows the stderr of exit 0 to no
// one; refuse is exit 2 with the reason on Stderr, which Claude Code hands the model; ask is exit 0
// with the ask as JSON on Stdout.
func (p *parse) hookClaude(ctx context.Context) {
	done, refused := result.Done.ExitCode(), result.Refused.ExitCode()
	verdict := p.claudeVerdict(ctx)

	switch verdict.Outcome {
	case guard.Allow:
		p.quit(done, "")
	case guard.Ask:
		out, err := json.Marshal(claudeAsk{HookSpecificOutput: claudeDecision{
			HookEventName:            claudeEvent,
			PermissionDecision:       claudeDecisionAsk,
			PermissionDecisionReason: verdict.Reason,
		}})
		if err == nil {
			_, err = p.app.Stdout.Write(append(out, '\n'))
		}

		if err != nil {
			p.quit(refused, fmt.Sprintf("the hook could not write its ask: %v", err))

			return
		}

		p.quit(done, "")
	case guard.Refuse:
		fallthrough
	default:
		// A verdict outside the table is a defect, and a defect must not let the command run.
		p.quit(refused, verdict.Reason)
	}
}

// cannotJudge returns the Refuse verdict of a failure that kept the guard from judging, which must not
// let the command run.
func cannotJudge(format string, args ...any) guard.Verdict {
	return guard.Verdict{Outcome: guard.Refuse, Reason: fmt.Sprintf(format, args...)}
}

// claudeVerdict returns the verdict of the guard on the payload of Claude Code's PreToolUse hook on
// Stdin, judged under the time limit of a command from launch: the command of a Bash call judged
// from its cwd, and allow for any other tool. A payload that cannot be read, or that names no tool,
// no command, or no cwd that is an absolute path to a directory that exists, is a refusal that says
// what broke.
func (p *parse) claudeVerdict(ctx context.Context) guard.Verdict {
	var payload []byte
	if p.app.Stdin != nil {
		var err error
		if payload, err = io.ReadAll(p.app.Stdin); err != nil {
			return cannotJudge("the hook could not read its payload on stdin: %v", err)
		}
	}

	var use claudeToolUse
	if err := json.Unmarshal(payload, &use); err != nil {
		return cannotJudge("the hook's payload is not the JSON of a PreToolUse call: %v", err)
	}

	switch {
	case use.ToolName == "":
		return cannotJudge("the hook's payload holds no tool_name")
	case use.ToolName != claudeShellTool:
		return guard.Verdict{Outcome: guard.Allow}
	}

	var input claudeShellInput
	if len(use.ToolInput) > 0 {
		if err := json.Unmarshal(use.ToolInput, &input); err != nil {
			return cannotJudge("the hook's payload holds a tool_input that is not the JSON of a Bash call: %v", err)
		}
	}

	switch {
	case input.Command == "":
		return cannotJudge("the hook's payload holds no tool_input.command")
	case use.Cwd == "":
		return cannotJudge("the hook's payload holds no cwd")
	case !filepath.IsAbs(use.Cwd):
		return cannotJudge("the hook's payload holds a cwd that is not absolute: %s", use.Cwd)
	}

	info, err := os.Stat(use.Cwd)
	if err != nil {
		return cannotJudge("the guard cannot read the directory the command runs in: %v", err)
	}
	if !info.IsDir() {
		return cannotJudge("the hook's payload holds a cwd that is not a directory: %s", use.Cwd)
	}

	ctx, cancel := p.underTimeLimit(ctx)
	defer cancel()

	return guard.Judge(ctx, input.Command, p.caller(use.Cwd))
}
