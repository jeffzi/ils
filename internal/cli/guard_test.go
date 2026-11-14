//go:build unix

package cli_test

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/spf13/pflag"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
	"mvdan.cc/sh/v3/shell"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// exitAsk is the code ils guard exits with when it asks.
const exitAsk = 3

// The commands that stand for each verdict of the guard: it allows the first, refuses the second
// and asks about the third.
const (
	allowedCommand = "echo hi"
	refusedCommand = "ils merge | cat"
	askedCommand   = "ils merge"
)

// ---------------------------------------------------------------------------
// ils parse
// ---------------------------------------------------------------------------

// parsed returns the parse payload of the JSON object out holds, and stops the test when it holds
// none.
func parsed(t *testing.T, out string) *result.Parse {
	t.Helper()

	printed := resulttest.Decode(t, out)
	assert.Assert(t, printed.Payload != nil && printed.Payload.Parse != nil, "output holds no parse payload:\n%s", out)

	return printed.Payload.Parse
}

// guarded returns the guard payload of the JSON object out holds, and stops the test when it holds
// none.
func guarded(t *testing.T, out string) *result.Guard {
	t.Helper()

	printed := resulttest.Decode(t, out)
	assert.Assert(t, printed.Payload != nil && printed.Payload.Guard != nil, "output holds no guard payload:\n%s", out)

	return printed.Payload.Guard
}

func literals(texts ...string) []result.Word {
	words := make([]result.Word, 0, len(texts))
	for _, text := range texts {
		words = append(words, result.Word{Text: text, Literal: true})
	}

	return words
}

// judge returns the verdict of the guard on command in dir, with git's environment env.
func judge(t *testing.T, command, dir string, env []string) guard.Verdict {
	t.Helper()

	return guard.Judge(t.Context(), command, guard.Caller{Dir: dir, Git: git.Runner{Env: env}})
}

func TestApp_Run_WhenParseIsGivenACommandWithTheJSONFlag_PrintsEveryInvocation(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	want := &result.Parse{Invocations: []result.Invocation{
		{
			Tool: "git", Words: literals("git", "status", "-s"), Dir: f.Dir(),
			Git: &result.GitCall{Subcommand: &result.Word{Text: "status", Literal: true}, Args: literals("-s"), GitDir: filepath.Join(f.Dir(), ".git"), Dir: f.Dir()},
		},
		{Words: []result.Word{{Text: "echo", Literal: true}, {}}, Dir: f.Dir()},
	}}

	got := launch(t, outsideWith(t, f.Env()), "parse", "--cwd", f.Dir(), "--json", "--", `git status -s && echo "$x"`)

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.DeepEqual(parsed(t, got.stdout), want, cmpopts.EquateEmpty()))
}

func TestApp_Run_WhenParseRunsAsText_PrintsTheGitCallOfAGitInvocation(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)

	got := launch(t, outsideWith(t, f.Env()), "parse", "--cwd", f.Dir(), "--", "git status")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(got.stdout, "subcommand status"))
	assert.Check(t, is.Contains(got.stdout, filepath.Join(f.Dir(), ".git")))
}

func TestApp_Run_WhenParseRunsAsText_PrintsTheDirectoryGitRunsIn(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	sub := filepath.Join(f.Dir(), "sub")
	assert.NilError(t, os.Mkdir(sub, 0o750))

	got := launch(t, outsideWith(t, f.Env()), "parse", "--cwd", f.Dir(), "--", "git -C sub status")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(got.stdout, sub), "stdout = %q", got.stdout)
}

func TestApp_Run_WhenParseRunsAsText_PrintsANonLiteralWordApartFromAnEmptyOne(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)

	got := launch(t, outsideWith(t, f.Env()), "parse", "--cwd", f.Dir(), "--", `echo "$x"; echo ""`)

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	lines := outputLines(got.stdout)
	assert.Assert(t, is.Len(lines, 2), "stdout = %q", got.stdout)
	assert.Check(t, lines[0] != lines[1], "a word that is not literal prints as the empty literal does: %q", lines[0])
}

func TestApp_Run_WhenParseRunsAsText_PrintsEachMarkNamingWhatItMarks(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)

	got := launch(t, outsideWith(t, f.Env()), "parse", "--cwd", f.Dir(), "--", "GIT_CONFIG_COUNT=1 git status")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(got.stdout, string(guard.GitVariable)), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(got.stdout, "GIT_CONFIG_COUNT"), "stdout = %q", got.stdout)
}

func TestApp_Run_WhenParseRunsAsText_PrintsWhatItCannotPlace(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	tests := []struct {
		name    string
		dir     string
		command string
		want    string
	}{
		{name: "directory after a cd that is not literal", dir: f.Dir(), command: `cd "$x"; echo hi`, want: "directory unknown"},
		{name: "git dir after a cd that is not literal", dir: f.Dir(), command: `cd "$x"; git status`, want: "git dir unknown"},
		{name: "directory git runs in after a cd that is not literal", dir: f.Dir(), command: `cd "$x"; git status`, want: "git runs in unknown"},
		{name: "directory outside any repository", dir: t.TempDir(), command: "git status", want: "git dir none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outsideWith(t, f.Env()), "parse", "--cwd", tt.dir, "--", tt.command)

			assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
			lines := outputLines(got.stdout)
			last := lines[len(lines)-1]
			assert.Check(t, is.Contains(last, tt.want), "stdout = %q", got.stdout)
		})
	}
}

func TestApp_Run_WhenParseRunsAsTextOnGitWithNoSubcommand_PrintsNoSubcommand(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)

	got := launch(t, outsideWith(t, f.Env()), "parse", "--cwd", f.Dir(), "--", "git --version")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, !strings.Contains(got.stdout, "subcommand"), "stdout = %q", got.stdout)
}

func TestApp_Run_WhenParseRunsWithHOMEInTheEnvironment_ExpandsTheTildeWithIt(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	app := outsideWith(t, f.Env())
	app.Environ = []string{"HOME=/home/reader"}

	got := launch(t, app, "parse", "--cwd", f.Dir(), "--json", "--", "ls ~/notes")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	invocations := parsed(t, got.stdout).Invocations
	assert.Assert(t, is.Len(invocations, 1))
	assert.Check(t, is.DeepEqual(invocations[0].Words, literals("ls", "/home/reader/notes")))
}

func TestApp_Run_WhenParseRunsWithGIT_DIRInTheEnvironment_ReportsThatGitDir(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	app := outsideWith(t, f.Env())
	gitDir := filepath.Join(f.Dir(), ".git")
	app.Environ = []string{"GIT_DIR=" + gitDir}

	got := launch(t, app, "parse", "--cwd", t.TempDir(), "--json", "--", "git status")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	invocations := parsed(t, got.stdout).Invocations
	assert.Assert(t, is.Len(invocations, 1))
	assert.Assert(t, invocations[0].Git != nil, "invocation %+v holds no git call", invocations[0])
	assert.Check(t, is.Equal(invocations[0].Git.GitDir, gitDir))
}

func TestApp_Run_WhenParseIsGivenARelativeDirectory_ReadsItFromTheWorkingDirectory(t *testing.T) {
	f := gittest.New(t, trunk)
	assert.NilError(t, os.Mkdir(filepath.Join(f.Dir(), "sub"), 0o750))
	t.Chdir(f.Dir())

	got := launch(t, outsideWith(t, f.Env()), "parse", "--cwd", "sub", "--json", "--", "echo hi")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	invocations := parsed(t, got.stdout).Invocations
	assert.Assert(t, is.Len(invocations, 1))
	assert.Check(t, is.Equal(invocations[0].Dir, filepath.Join(f.Dir(), "sub")))
}

func TestApp_Run_WhenParseCannotReadTheCommand_ExitsOneWithTheReason(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	const command = `echo "x`
	_, failure := guard.Parse(t.Context(), command, guard.Caller{Dir: f.Dir(), Git: git.Runner{Env: f.Env()}})
	assert.Assert(t, failure != nil, "Parse(%q) did not fail", command)

	got := launch(t, outsideWith(t, f.Env()), "parse", "--cwd", f.Dir(), "--json", "--", command)

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	printed := resulttest.Decode(t, got.stdout)
	assert.Check(t, is.Contains(printed.Reason, failure.Error()))
}

func TestApp_Run_WhenParseCannotReadTheCommand_NextNamesTheTypedCommandQuotedForTheShell(t *testing.T) {
	f := gittest.New(t, trunk)
	t.Chdir(f.Dir())

	got := launch(t, outsideWith(t, f.Env()), "parse", "--cwd", ".", "--json", "--", `echo "x`)

	words, err := shell.Fields(resulttest.Decode(t, got.stdout).Next, func(string) string { return "" })
	assert.NilError(t, err)
	want := []string{"ils", "parse", "--cwd", ".", "--", `echo "x`}
	start := slices.Index(words, "ils")
	assert.Assert(t, start >= 0 && len(words)-start >= len(want), "next words = %q", words)
	assert.Check(t, is.DeepEqual(words[start:start+len(want)], want))
}

func TestApp_Run_WhenParseIsNotGivenADirectoryAndOneCommandWord_Refuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "no directory", args: []string{"parse", "--", "git status"}},
		{name: "no word after the end of flags", args: []string{"parse", "--cwd", ".", "--"}},
		{name: "command before the end of flags", args: []string{"parse", "--cwd", ".", "git status"}},
		{name: "command split into words", args: []string{"parse", "--cwd", ".", "--", "git", "status"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			assert.Check(t, is.Equal(got.code, exitRefused), "ils %s: stdout = %q", strings.Join(tt.args, " "), got.stdout)
		})
	}
}

// ---------------------------------------------------------------------------
// ils guard
// ---------------------------------------------------------------------------

type verdictCase struct {
	name    string
	command string
	code    int
}

func verdicts() []verdictCase {
	return []verdictCase{
		{name: "allowed", command: allowedCommand, code: exitDone},
		{name: "refused", command: refusedCommand, code: exitRefused},
		{name: "asked", command: askedCommand, code: exitAsk},
	}
}

func TestApp_Run_WhenGuardIsGivenACommandWithTheJSONFlag_PrintsTheVerdict(t *testing.T) {
	t.Parallel()

	for _, tt := range verdicts() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir, env := t.TempDir(), gittest.Env(t)
			verdict := judge(t, tt.command, dir, env)

			got := launch(t, outsideWith(t, env), "guard", "--cwd", dir, "--json", "--", tt.command)

			assert.Check(t, is.Equal(got.code, tt.code), "stdout = %q", got.stdout)
			printed := resulttest.Decode(t, got.stdout)
			assert.Check(t, is.Equal(printed.Reason, verdict.Reason))
		})
	}
}

func TestApp_Run_WhenGuardIsGivenACommandWithTheJSONFlag_PrintsTheInvocationsParsePrints(t *testing.T) {
	t.Parallel()

	const command = refusedCommand
	dir, env := t.TempDir(), gittest.Env(t)
	want := parsed(t, launch(t, outsideWith(t, env), "parse", "--cwd", dir, "--json", "--", command).stdout).Invocations
	assert.Assert(t, len(want) > 0, "ils parse printed no invocation for %q", command)

	got := launch(t, outsideWith(t, env), "guard", "--cwd", dir, "--json", "--", command)

	assert.Check(t, is.DeepEqual(guarded(t, got.stdout).Invocations, want, cmpopts.EquateEmpty()))
}

func TestApp_Run_WhenGuardIsNotGivenADirectory_RefusesPointingAtItsHelp(t *testing.T) {
	t.Parallel()

	got := launch(t, outside(t), "guard", "--json", "--", "ils merge")

	assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
	// A command ils does not know is refused too, but points at the help of ils itself.
	assert.Check(t, is.Equal(resulttest.Decode(t, got.stdout).Next, "ils guard --help"))
}

// ---------------------------------------------------------------------------
// The root switches the guard reads
// ---------------------------------------------------------------------------

func TestJudge_WhenIlsIsGivenAnyRootSwitchOfTheCli_AsksAboutTheCommandAfterIt(t *testing.T) {
	t.Parallel()

	var switches []string
	visit := func(f *pflag.Flag) {
		if f.NoOptDefVal == "" {
			return
		}

		switches = append(switches, "--"+f.Name)
		if f.Shorthand != "" {
			switches = append(switches, "-"+f.Shorthand)
		}
	}
	root := new(cli.App).Root()
	root.Flags().VisitAll(visit)
	assert.Assert(t, len(switches) > 0, "the root command has no switch")
	dir := t.TempDir()

	for _, name := range switches {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			command := "ils " + name + " merge"

			got := judge(t, command, dir, nil)

			assert.Check(t, is.Equal(got.Outcome, guard.Ask), "Judge(%q) outcome, reason %q", command, got.Reason)
		})
	}
}

// ---------------------------------------------------------------------------
// ils hook
// ---------------------------------------------------------------------------

// toolUse is the PreToolUse payload Claude Code sends, cut to the fields ils hook reads.
type toolUse struct {
	Input    map[string]string `json:"tool_input,omitempty"`
	ToolName string            `json:"tool_name,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
}

func bash(command, cwd string) toolUse {
	return toolUse{ToolName: "Bash", Input: map[string]string{"command": command}, Cwd: cwd}
}

// encoded returns use as the JSON Claude Code writes on the hook's stdin.
func encoded(t *testing.T, use toolUse) string {
	t.Helper()

	payload, err := json.Marshal(use)
	assert.NilError(t, err)

	return string(payload)
}

func hooking(t *testing.T, env []string, stdin io.Reader) cli.App {
	t.Helper()

	app := outsideWith(t, env)
	app.Stdin = stdin

	return app
}

type hookAsk struct {
	HookSpecificOutput struct {
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

func TestApp_Run_WhenHookClaudeIsGivenACommandTheGuardAllows_ExitsZeroPrintingNothing(t *testing.T) {
	t.Parallel()

	dir, env := t.TempDir(), gittest.Env(t)
	app := hooking(t, env, strings.NewReader(encoded(t, bash(allowedCommand, dir))))

	got := launch(t, app, "hook", "claude")

	assert.Check(t, is.Equal(got.code, exitDone))
	assert.Check(t, is.Equal(got.stdout, ""))
	assert.Check(t, is.Equal(got.stderr, ""))
}

type hookDecision struct {
	HookSpecificOutput struct {
		HookEventName      string `json:"hookEventName"`
		PermissionDecision string `json:"permissionDecision"`
	} `json:"hookSpecificOutput"`
}

func TestApp_Run_WhenHookClaudeIsGivenACommandTheGuardAllowsExplicitly_ExitsZeroPrintingAnAllow(t *testing.T) {
	t.Parallel()

	dir, env := t.TempDir(), gittest.Env(t)
	const command = "ils push"
	verdict := judge(t, command, dir, env)
	assert.Assert(t, verdict.Outcome == guard.Allow && verdict.Explicit, "Judge(%q) = %+v, want an explicit allow", command, verdict)
	app := hooking(t, env, strings.NewReader(encoded(t, bash(command, dir))))

	got := launch(t, app, "hook", "claude")

	assert.Check(t, is.Equal(got.code, exitDone))
	assert.Check(t, is.Equal(got.stderr, ""))
	var printed hookDecision
	assert.Assert(t, json.Unmarshal([]byte(got.stdout), &printed), "stdout = %q", got.stdout)
	assert.Check(t, is.Equal(printed.HookSpecificOutput.HookEventName, "PreToolUse"))
	assert.Check(t, is.Equal(printed.HookSpecificOutput.PermissionDecision, "allow"))
}

func TestApp_Run_WhenHookClaudeIsGivenACommandTheGuardRefuses_ExitsTwoWithTheReasonOnStderr(t *testing.T) {
	t.Parallel()

	dir, env := t.TempDir(), gittest.Env(t)
	command := refusedCommand
	verdict := judge(t, command, dir, env)
	assert.Assert(t, verdict.Outcome == guard.Refuse && verdict.Reason != "", "Judge(%q) = %+v, want a refusal with a reason", command, verdict)
	app := hooking(t, env, strings.NewReader(encoded(t, bash(command, dir))))

	got := launch(t, app, "hook", "claude")

	assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(got.stderr, verdict.Reason))
}

func TestApp_Run_WhenHookClaudeIsGivenACommandTheGuardAsksAbout_ExitsZeroPrintingTheAskAsJSON(t *testing.T) {
	t.Parallel()

	dir, env := t.TempDir(), gittest.Env(t)
	command := askedCommand
	verdict := judge(t, command, dir, env)
	assert.Assert(t, verdict.Outcome == guard.Ask && verdict.Reason != "", "Judge(%q) = %+v, want an ask with a reason", command, verdict)
	app := hooking(t, env, strings.NewReader(encoded(t, bash(command, dir))))

	got := launch(t, app, "hook", "claude")

	assert.Check(t, is.Equal(got.code, exitDone))
	assert.Check(t, is.Equal(got.stderr, ""))
	var printed hookAsk
	assert.Assert(t, json.Unmarshal([]byte(got.stdout), &printed), "stdout = %q", got.stdout)
	assert.Check(t, is.Equal(printed.HookSpecificOutput.PermissionDecision, "ask"))
	assert.Check(t, is.Equal(printed.HookSpecificOutput.PermissionDecisionReason, verdict.Reason))
}

func TestApp_Run_WhenHookClaudeIsGivenAToolOtherThanBash_ExitsZeroPrintingNothing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	stdin := fmt.Sprintf(`{"tool_name":"Edit","cwd":%q,"tool_input":{"command":["x"]}}`, dir)

	got := launch(t, hooking(t, gittest.Env(t), strings.NewReader(stdin)), "hook", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stderr = %q", got.stderr)
	assert.Check(t, is.Equal(got.stdout, ""))
}

func TestApp_Run_WhenHookClaudeCannotReadThePayload_ExitsTwoSayingWhatBroke(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gone, file := filepath.Join(dir, "gone"), filepath.Join(dir, "file")
	assert.NilError(t, os.WriteFile(file, []byte("words"), 0o600))
	// relative names a directory that exists from the package directory, where go test runs.
	relative := filepath.Join("..", "cli")
	tests := []struct {
		stdin io.Reader
		name  string
		want  string
	}{
		{name: "payload that is not JSON", stdin: strings.NewReader(`{"tool_name":`)},
		{name: "no tool name", stdin: strings.NewReader(encoded(t, toolUse{Input: map[string]string{"command": "echo hi"}, Cwd: dir}))},
		{name: "stdin that cannot be read", stdin: iotest.ErrReader(errReaderGone), want: errReaderGone.Error()},
		{name: "command that is not a string", stdin: strings.NewReader(fmt.Sprintf(`{"tool_name":"Bash","cwd":%q,"tool_input":{"command":5}}`, dir))},
		{name: "no command", stdin: strings.NewReader(encoded(t, toolUse{ToolName: "Bash", Input: map[string]string{}, Cwd: dir}))},
		{name: "no directory", stdin: strings.NewReader(encoded(t, toolUse{ToolName: "Bash", Input: map[string]string{"command": "echo hi"}}))},
		{name: "directory that does not exist", stdin: strings.NewReader(encoded(t, bash("echo hi", gone))), want: gone},
		{name: "directory that is relative", stdin: strings.NewReader(encoded(t, bash("echo hi", relative))), want: relative},
		{name: "directory that is a file", stdin: strings.NewReader(encoded(t, bash("echo hi", file))), want: file},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, hooking(t, gittest.Env(t), tt.stdin), "hook", "claude")

			assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
			assert.Check(t, got.stderr != "", "stderr is empty")
			assert.Check(t, is.Contains(got.stderr, tt.want))
		})
	}
}

func TestApp_Run_WhenHookClaudeCannotWriteTheAsk_ExitsTwoWithTheErrorOnStderr(t *testing.T) {
	t.Parallel()

	dir, env := t.TempDir(), gittest.Env(t)
	app := hooking(t, env, strings.NewReader(encoded(t, bash(askedCommand, dir))))
	app.Stdout = shut{err: errReaderGone}

	got := launch(t, app, "hook", "claude")

	assert.Check(t, is.Equal(got.code, exitRefused))
	assert.Check(t, is.Contains(got.stderr, errReaderGone.Error()))
}

func TestApp_Run_WhenHookIsGivenWordsItDoesNotTake_ExitsTwoNamingThemOnStderr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
		args []string
	}{
		{name: "no harness", args: []string{"hook"}},
		{name: "harness it does not know", args: []string{"hook", "codex"}, want: "codex"},
		{name: "word after the harness", args: []string{"hook", "claude", "extra"}, want: "extra"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			app := hooking(t, gittest.Env(t), strings.NewReader(encoded(t, bash(allowedCommand, dir))))

			got := launch(t, app, tt.args...)

			assert.Check(t, is.Equal(got.code, exitRefused), "ils %s: stdout = %q", strings.Join(tt.args, " "), got.stdout)
			assert.Check(t, got.stderr != "", "stderr is empty")
			assert.Check(t, is.Contains(got.stderr, tt.want))
		})
	}
}

// ---------------------------------------------------------------------------
// The hook script
// ---------------------------------------------------------------------------

// shim is the path, from the directory of this package, of the script Claude Code runs as its
// hook, which fronts ils hook claude.
const shim = "../../skills/ils/hook.sh"

// standingIn returns a directory that holds the test binary as ils, and the environment that makes
// it stand in for ils and exit as exit says.
func standingIn(t *testing.T, exit string) (dir, env string) {
	t.Helper()

	dir = t.TempDir()
	gittest.Link(t, dir, "ils")

	return dir, standIn + "=" + exit
}

// throughShim runs the shim with bash, giving it stdin, a PATH that holds only dir, and env, and
// returns what it printed and the code it exited with.
func throughShim(t *testing.T, dir, stdin string, env ...string) ran {
	t.Helper()

	bashPath, err := exec.LookPath("bash")
	assert.NilError(t, err)
	var stdout, stderr strings.Builder
	cmd := exec.CommandContext(t.Context(), bashPath, shim) //nolint:gosec // argv is bash and the shim this package ships
	cmd.Env = append([]string{"PATH=" + dir, "GORACE=" + gittest.RaceSetting()}, env...)
	// A stand-in built for coverage warns on stderr unless it knows where to write its counters.
	if covered := gittest.CoverSetting(); covered != "" {
		cmd.Env = append(cmd.Env, covered)
	}
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err = cmd.Run()
	if _, exited := errors.AsType[*exec.ExitError](err); err != nil && !exited {
		t.Fatalf("run the shim: %v", err)
	}

	return ran{stdout: stdout.String(), stderr: stderr.String(), code: cmd.ProcessState.ExitCode()}
}

// shimPayload returns a payload the shim reads from stdin, for a command that runs in a directory
// that holds nothing.
func shimPayload(t *testing.T) string {
	t.Helper()

	return encoded(t, bash(allowedCommand, t.TempDir()))
}

func TestHookShim_WhenIlsExitsZeroOrTwo_ExitsWithItsCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		exit string
		want int
	}{
		{name: "allowed", exit: "0", want: exitDone},
		{name: "refused", exit: "2", want: exitRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir, env := standingIn(t, tt.exit)

			got := throughShim(t, dir, shimPayload(t), env)

			assert.Check(t, is.Equal(got.code, tt.want), "stderr = %q", got.stderr)
		})
	}
}

func TestHookShim_WhenIlsEndsAnyOtherWay_ExitsTwoNamingTheCodeOnStderr(t *testing.T) {
	t.Parallel()

	dir, env := standingIn(t, "1")

	got := throughShim(t, dir, shimPayload(t), env)

	assert.Check(t, is.Equal(got.code, exitRefused), "stderr = %q", got.stderr)
	assert.Check(t, is.Regexp(`(^|\D)1(\D|$)`, got.stderr))
}

func TestHookShim_WhenIlsCannotStart_ExitsTwoWithAReasonOnStderr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ils  string
	}{
		{name: "not on PATH"},
		{name: "interpreter missing", ils: "#!/nonexistent/interpreter\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if tt.ils != "" {
				assert.NilError(t, os.WriteFile(filepath.Join(dir, "ils"), []byte(tt.ils), 0o700)) //nolint:gosec // the stand-in must be executable
			}

			got := throughShim(t, dir, shimPayload(t))

			assert.Check(t, is.Equal(got.code, exitRefused), "stderr = %q", got.stderr)
			assert.Check(t, got.stderr != "", "stderr is empty")
		})
	}
}
