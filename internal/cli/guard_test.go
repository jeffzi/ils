//go:build unix

package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// parsed returns the parse payload of the JSON object out holds, and stops the test when it holds
// none.
func parsed(t *testing.T, out string) *result.Parse {
	t.Helper()

	printed := resulttest.Decode(t, out)
	assert.Assert(t, printed.Payload != nil && printed.Payload.Parse != nil, "output holds no parse payload:\n%s", out)

	return printed.Payload.Parse
}

// literals returns the words that read as texts.
func literals(texts ...string) []result.Word {
	words := make([]result.Word, 0, len(texts))
	for _, text := range texts {
		words = append(words, result.Word{Text: text, Literal: true})
	}

	return words
}

// parsing returns the cli in a directory that is no repository, with git's environment env.
func parsing(t *testing.T, env []string) cli.App {
	t.Helper()

	return over(git.Runner{Dir: t.TempDir(), Env: env})
}

func TestApp_Run_WhenParseIsGivenACommandWithTheJSONFlag_PrintsEveryInvocation(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	want := &result.Parse{Invocations: []result.Invocation{
		{
			Tool: "git", Words: literals("git", "status", "-s"), Dir: f.Dir(),
			Git: &result.GitCall{Subcommand: &result.Word{Text: "status", Literal: true}, Args: literals("-s"), GitDir: filepath.Join(f.Dir(), ".git")},
		},
		{Words: []result.Word{{Text: "echo", Literal: true}, {}}, Dir: f.Dir()},
	}}

	got := launch(t, parsing(t, f.Env()), "parse", "--cwd", f.Dir(), "--json", "--", `git status -s && echo "$x"`)

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.DeepEqual(parsed(t, got.stdout), want, cmpopts.EquateEmpty()))
}

func TestApp_Run_WhenParseIsGivenACommandHoldingAMark_ExitsZeroCarryingIt(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)

	got := launch(t, parsing(t, f.Env()), "parse", "--cwd", f.Dir(), "--json", "--", `"$tool" push`)

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	invocations := parsed(t, got.stdout).Invocations
	assert.Assert(t, is.Len(invocations, 1))
	assert.Check(t, is.DeepEqual(invocations[0].Marks, []result.Mark{{Kind: string(guard.DynamicCommand)}}))
}

func TestApp_Run_WhenParseRunsAsText_PrintsOneLinePerInvocation(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)

	got := launch(t, parsing(t, f.Env()), "parse", "--cwd", f.Dir(), "--", `git status; echo "$x"; echo ""`)

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	lines := strings.Split(strings.TrimSuffix(got.stdout, "\n"), "\n")
	assert.Assert(t, is.Len(lines, 3), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(lines[0], "status"))
	assert.Check(t, is.Contains(lines[0], filepath.Join(f.Dir(), ".git")))
	assert.Check(t, lines[1] != lines[2], "a word that is not literal prints as the empty literal does: %q", lines[1])
}

func TestApp_Run_WhenParseRunsAsText_PrintsWhatItCannotPlaceAndEachMark(t *testing.T) {
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
		{name: "directory outside any repository", dir: t.TempDir(), command: "git status", want: "git dir none"},
		{
			name: "mark naming what it marks", dir: f.Dir(), command: "GIT_CONFIG_COUNT=1 git status",
			want: string(guard.GitVariable) + " (GIT_CONFIG_COUNT)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, parsing(t, f.Env()), "parse", "--cwd", tt.dir, "--", tt.command)

			assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
			lines := strings.Split(strings.TrimSuffix(got.stdout, "\n"), "\n")
			assert.Check(t, is.Contains(lines[len(lines)-1], tt.want), "stdout = %q", got.stdout)
		})
	}
}

func TestApp_Run_WhenParseIsGivenGitWithNoSubcommand_PrintsNoneApartFromANonLiteralOne(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)

	got := launch(t, parsing(t, f.Env()), "parse", "--cwd", f.Dir(), "--json", "--", `git --version; git "$x" status`)

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	invocations := parsed(t, got.stdout).Invocations
	assert.Assert(t, is.Len(invocations, 2))
	assert.Assert(t, invocations[0].Git != nil && invocations[1].Git != nil, "stdout = %q", got.stdout)
	assert.Check(t, is.Nil(invocations[0].Git.Subcommand), "git --version subcommand")
	assert.Check(t, is.DeepEqual(invocations[1].Git.Subcommand, &result.Word{}), "git \"$x\" status subcommand")
}

func TestApp_Run_WhenParseRunsAsTextOnGitWithNoSubcommand_PrintsNoSubcommand(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)

	got := launch(t, parsing(t, f.Env()), "parse", "--cwd", f.Dir(), "--", "git --version")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, !strings.Contains(got.stdout, "subcommand"), "stdout = %q", got.stdout)
}

func TestApp_Run_WhenParseRunsWithCDPATHInTheEnvironment_ReportsTheDirectoryAfterARelativeCDUnknown(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	app := parsing(t, f.Env())
	app.Environ = []string{"CDPATH=/srv"}

	got := launch(t, app, "parse", "--cwd", f.Dir(), "--json", "--", "cd sub && echo hi")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	invocations := parsed(t, got.stdout).Invocations
	assert.Assert(t, is.Len(invocations, 2))
	assert.Check(t, is.Equal(invocations[1].Dir, ""))
}

func TestApp_Run_WhenParseRunsWithHOMEInTheEnvironment_ExpandsTheTildeWithIt(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	app := parsing(t, f.Env())
	app.Environ = []string{"HOME=/home/reader"}

	got := launch(t, app, "parse", "--cwd", f.Dir(), "--json", "--", "ls ~/notes")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	invocations := parsed(t, got.stdout).Invocations
	assert.Assert(t, is.Len(invocations, 1))
	assert.Check(t, is.DeepEqual(invocations[0].Words, literals("ls", "/home/reader/notes")))
}

func TestApp_Run_WhenParseIsGivenARelativeDirectory_ReadsItFromTheWorkingDirectory(t *testing.T) {
	f := gittest.New(t, trunk)
	assert.NilError(t, os.Mkdir(filepath.Join(f.Dir(), "sub"), 0o750))
	t.Chdir(f.Dir())

	got := launch(t, parsing(t, f.Env()), "parse", "--cwd", "sub", "--json", "--", "echo hi")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	invocations := parsed(t, got.stdout).Invocations
	assert.Assert(t, is.Len(invocations, 1))
	assert.Check(t, is.Equal(invocations[0].Dir, filepath.Join(f.Dir(), "sub")))
}

func TestApp_Run_WhenParseCannotReadTheCommand_ExitsOneWithTheReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		// arrange returns the directory the command is read in, after setting it up.
		arrange func(t *testing.T, f *gittest.Fixture) string
		name    string
		command string
	}{
		{
			name: "command that does not parse", command: `echo "x`,
			arrange: func(_ *testing.T, f *gittest.Fixture) string { return f.Dir() },
		},
		{
			name: "git dir read that fails", command: "git status",
			arrange: func(t *testing.T, f *gittest.Fixture) string {
				t.Helper()

				assert.NilError(t, os.WriteFile(filepath.Join(f.Dir(), ".git", "config"), []byte("[core\nbroken"), 0o600))

				return f.Dir()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := gittest.New(t, trunk)
			dir := tt.arrange(t, f)
			_, failure := guard.Parse(t.Context(), tt.command, guard.Caller{Dir: dir, Git: git.Runner{Env: f.Env()}})
			assert.Assert(t, failure != nil, "Parse(%q) did not fail", tt.command)

			got := launch(t, parsing(t, f.Env()), "parse", "--cwd", dir, "--json", "--", tt.command)

			assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
			printed := resulttest.Decode(t, got.stdout)
			assert.Check(t, is.Equal(printed.Outcome, result.Broke.String()))
			assert.Check(t, is.Contains(printed.Reason, failure.Error()))
		})
	}
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
