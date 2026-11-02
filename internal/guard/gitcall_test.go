//go:build unix

package guard_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/guard"
)

// literal returns the word that reads as text.
func literal(text string) guard.Word {
	return guard.Word{Text: text, Literal: true}
}

func wordTexts(words []guard.Word) []string {
	got := make([]string, 0, len(words))
	for _, word := range words {
		got = append(got, word.Text)
	}

	return got
}

// quotedPath returns path as one single-quoted shell word.
func quotedPath(path string) string {
	return "'" + filepath.ToSlash(path) + "'"
}

// outsideDir returns a new directory that holds no repository, with its symbolic links resolved.
func outsideDir(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	assert.NilError(t, err)

	return dir
}

// aliased returns a repository whose local config holds the aliases the alias tests read.
func aliased(t *testing.T) *gittest.Fixture {
	t.Helper()

	f := gittest.New(t, "main")
	aliases := [][2]string{
		{"status", "!echo shadowed"},
		{"co", "checkout -b"},
		{"lg", "hist --oneline"},
		{"hist", "log --graph"},
		{"cm", `commit -m "two words"`},
		{"one", "two"},
		{"two", "three"},
		{"three", "log"},
		{"d1", "d2"},
		{"d2", "d3"},
		{"d3", "d4"},
		{"d4", "log"},
		{"sh", "!git push"},
		{"sw", "switch"},
		{"rb", "rebase base main"},
		{"spaced", `commit -m a\ b`},
		{"unclosed", `log "-1`},
		{"dangling", `log -1\`},
	}
	for _, alias := range aliases {
		f.Git("config", "--local", "alias."+alias[0], alias[1])
	}

	return f
}

func TestParse_ReadsTheGlobalOptionsOfAGitInvocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		command        string
		wantSubcommand string
		wantOptions    []guard.GitOption
		wantArgs       []string
	}{
		{
			name: "short option with a separate value", command: "git -C sub status -s",
			wantOptions: []guard.GitOption{{Name: "-C", Value: literal("sub")}}, wantSubcommand: "status", wantArgs: []string{"-s"},
		},
		{
			name: "long option with an attached value", command: "git --git-dir=/srv/x.git status",
			wantOptions: []guard.GitOption{{Name: "--git-dir", Value: literal("/srv/x.git")}}, wantSubcommand: "status", wantArgs: []string{},
		},
		{
			name: "attached value of an option that takes only an attached one", command: "git --exec-path=/x status",
			wantOptions: []guard.GitOption{{Name: "--exec-path", Value: literal("/x")}}, wantSubcommand: "status", wantArgs: []string{},
		},
		{
			name: "attached value holding an equals sign", command: "git --config-env=core.pager=PAGER log",
			wantOptions: []guard.GitOption{{Name: "--config-env", Value: literal("core.pager=PAGER")}}, wantSubcommand: "log", wantArgs: []string{},
		},
		{
			name: "flags", command: "git --no-pager -P --literal-pathspecs add x",
			wantOptions: []guard.GitOption{{Name: "--no-pager"}, {Name: "-P"}, {Name: "--literal-pathspecs"}}, wantSubcommand: "add", wantArgs: []string{"x"},
		},
		{
			name: "global option after the subcommand", command: "git status --bare -C x",
			wantSubcommand: "status", wantArgs: []string{"--bare", "-C", "x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command).Git

			assert.Check(t, is.DeepEqual(got.Options, tt.wantOptions), "Parse(%q) options", tt.command)
			assert.Check(t, is.DeepEqual(got.Subcommand, literal(tt.wantSubcommand)), "Parse(%q) subcommand", tt.command)
			assert.Check(t, is.DeepEqual(wordTexts(got.Args), tt.wantArgs), "Parse(%q) arguments", tt.command)
		})
	}
}

func TestParse_ResolvesTheSubcommandThroughGitsAliases(t *testing.T) {
	t.Parallel()

	f := aliased(t)
	tests := []struct {
		name           string
		command        string
		wantSubcommand string
		wantArgs       []string
	}{
		{name: "command shadowed by an alias", command: "git status -s", wantSubcommand: "status", wantArgs: []string{"-s"}},
		{name: "alias", command: "git co topic", wantSubcommand: "checkout", wantArgs: []string{"-b", "topic"}},
		{name: "quoted words in an expansion", command: "git cm", wantSubcommand: "commit", wantArgs: []string{"-m", "two words"}},
		{name: "three levels", command: "git one", wantSubcommand: "log", wantArgs: []string{}},
		{name: "backslash escape in an expansion", command: "git spaced", wantSubcommand: "commit", wantArgs: []string{"-m", "a b"}},
		{
			name: "command an older git lacks, with no alias of its name", command: "git replay --onto main topic",
			wantSubcommand: "replay", wantArgs: []string{"--onto", "main", "topic"},
		},
		{
			name: "alias given by --config-env", command: "SW=status git --config-env=alias.sw=SW sw",
			wantSubcommand: "status", wantArgs: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lastWith(t, tt.command, callerIn(f, f.Dir()))

			assert.Check(t, is.Len(got.Marks, 0), "Parse(%q) marks", tt.command)
			assert.Check(t, is.DeepEqual(got.Git.Subcommand, literal(tt.wantSubcommand)), "Parse(%q) subcommand", tt.command)
			assert.Check(t, is.DeepEqual(wordTexts(got.Git.Args), tt.wantArgs), "Parse(%q) arguments", tt.command)
		})
	}
}

func TestParse_ReadsAnAliasInTheInvocationsTarget(t *testing.T) {
	t.Parallel()

	f := aliased(t)
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	assert.NilError(t, os.WriteFile(globalConfig, []byte("[alias]\n\tco = checkout -b\n"), 0o600))
	tests := []struct {
		name    string
		command string
		env     []string
	}{
		{name: "repository named by -C", command: "git -C " + quotedPath(f.Dir()) + " co topic", env: f.Env()},
		{name: "global config outside any repository", command: "git co topic", env: append(gittest.Env(t), "GIT_CONFIG_GLOBAL="+globalConfig)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lastWith(t, tt.command, callerWithEnv(outsideDir(t), tt.env))

			assert.Check(t, is.Len(got.Marks, 0), "Parse(%q) marks", tt.command)
			assert.Check(t, is.DeepEqual(got.Git.Subcommand, literal("checkout")), "Parse(%q) subcommand", tt.command)
		})
	}
}

func TestParse_MarksASubcommandItCannotResolve(t *testing.T) {
	t.Parallel()

	f := aliased(t)
	tests := []struct {
		name    string
		command string
		want    []guard.Mark
	}{
		{name: "alias running a shell command", command: "git sh", want: []guard.Mark{{Kind: guard.ShellAlias, Name: "sh"}}},
		{name: "word that is neither command nor alias", command: "git taxi", want: []guard.Mark{{Kind: guard.UnknownSubcommand, Name: "taxi"}}},
		{name: "alias given only by -c", command: "git -c alias.x='!git push' x", want: []guard.Mark{{Kind: guard.ShellAlias, Name: "x"}}},
		{name: "alias read under a non-literal -c", command: `git -c "$v" co`, want: []guard.Mark{{Kind: guard.UnreadableAlias, Name: "co"}}},
		{
			name:    "alias read under --config-env naming a variable the parse cannot read",
			command: "git --config-env alias.sw=ABSENT sw",
			want:    []guard.Mark{{Kind: guard.UnreadableAlias, Name: "sw"}},
		},
		{name: "alias holding an unclosed quote", command: "git unclosed", want: []guard.Mark{{Kind: guard.UnreadableAlias, Name: "unclosed"}}},
		{name: "alias ending in a backslash", command: "git dangling", want: []guard.Mark{{Kind: guard.UnreadableAlias, Name: "dangling"}}},
		{name: "fourth level of expansion", command: "git d1", want: []guard.Mark{{Kind: guard.DeepAlias, Name: "d4"}}},
		{name: "alias in an unknown target", command: `git -C "$d" co`, want: []guard.Mark{{Kind: guard.UnknownSubcommand, Name: "co"}}},
		{
			name:    "command an older git lacks, alias read under a non-literal -c",
			command: `git -c "$v" replay --onto main topic`,
			want:    []guard.Mark{{Kind: guard.UnreadableAlias, Name: "replay"}},
		},
		{
			name:    "command an older git lacks, in an unknown target",
			command: `git -C "$d" replay --onto main topic`,
			want:    []guard.Mark{{Kind: guard.UnreadableAlias, Name: "replay"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lastWith(t, tt.command, callerIn(f, f.Dir()))

			assert.Check(t, is.DeepEqual(got.Marks, tt.want), "Parse(%q) marks", tt.command)
		})
	}
}

// checkFailedCall fails unless err is the faked failure of a git call whose arguments hold arg.
func checkFailedCall(t *testing.T, err error, arg string) {
	t.Helper()

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "Parse() error = %v, want *child.ExitError", err)
	assert.Check(t, slices.Contains(exitErr.Argv, arg), "failed call %v, want one holding %q", exitErr.Argv, arg)
}

func TestParse_WhenAGitConfigReadFails_FailsNamingTheCall(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := aliased(t)
	env := append(f.Env(), gittest.FakeGit(t, gittest.Fake{FailOn: []string{"config"}})...)

	_, err := guard.Parse(t.Context(), "git co topic", callerWithEnv(f.Dir(), env))

	checkFailedCall(t, err, "alias.co")
}

func TestParse_WhenReadingTheAliasOfACommandAnOlderGitLacksFails_FailsNamingTheCall(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := gittest.New(t, "main")
	env := append(f.Env(), gittest.FakeGit(t, gittest.Fake{FailOn: []string{"config"}})...)

	_, err := guard.Parse(t.Context(), "git replay --onto main topic", callerWithEnv(f.Dir(), env))

	checkFailedCall(t, err, "alias.replay")
}

func TestParse_ReportsTheGitDirAnInvocationTargets(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	gitDir := filepath.Join(f.Dir(), ".git")
	outside := outsideDir(t)
	tests := []struct {
		name    string
		dir     string
		command string
		want    string
	}{
		{name: "work tree", dir: f.Dir(), command: "git status", want: gitDir},
		{name: "work tree named by -C", dir: outside, command: "git -C " + quotedPath(f.Dir()) + " status", want: gitDir},
		{name: "git dir named by --git-dir", dir: outside, command: "git --git-dir=" + quotedPath(gitDir) + " status", want: gitDir},
		{name: "git dir named by GIT_DIR", dir: outside, command: "GIT_DIR=" + quotedPath(gitDir) + " git status", want: gitDir},
		{name: "work tree root read as a bare repository", dir: f.Dir(), command: "git --bare status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lastWith(t, tt.command, callerIn(f, tt.dir)).Git

			assert.Check(t, !got.GitDirUnknown, "Parse(%q) reports the git dir unknown", tt.command)
			assert.Check(t, is.Equal(got.GitDir, tt.want), "Parse(%q) git dir", tt.command)
		})
	}
}

func TestParse_WhenTheGitDirCannotBeRead_ReportsItUnknown(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	tests := []struct {
		name    string
		command string
	}{
		{name: "-C to a missing directory", command: "git -C missing status"},
		{name: "non-literal -C", command: `git -C "$d" status`},
		{name: "non-literal --git-dir", command: `git --git-dir="$d" status`},
		{name: "non-literal --work-tree", command: `git --work-tree="$d" status`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lastWith(t, tt.command, callerIn(f, f.Dir())).Git

			assert.Check(t, got.GitDirUnknown, "Parse(%q) git dir = %q, want unknown", tt.command, got.GitDir)
		})
	}
}

func TestParse_WhenGitCannotReadTheGitDir_FailsNamingTheCall(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := gittest.New(t, "main")
	env := append(f.Env(), gittest.FakeGit(t, gittest.Fake{FailOn: []string{"rev-parse"}})...)

	_, err := guard.Parse(t.Context(), "git status", callerWithEnv(f.Dir(), env))

	checkFailedCall(t, err, "--absolute-git-dir")
}

func TestParse_WhenNoInvocationRunsGit_StartsNoGit(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	recorder := &git.Recorder{}
	caller := callerIn(f, f.Dir())
	caller.Git.Recorder = recorder
	const command = "gh pr list && ls -la | grep x; echo git push"

	parseWith(t, command, caller)

	assert.Check(t, is.Len(recorder.Calls(), 0), "Parse(%q) git calls", command)
}

func TestParse_WhenAGlobalOptionPrintsAndExits_ReportsNoSubcommand(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	tests := []struct {
		name    string
		command string
	}{
		{name: "version", command: "git --version"},
		{name: "bare exec path", command: "git --exec-path"},
		{name: "bare git", command: "git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lastWith(t, tt.command, callerIn(f, f.Dir()))

			assert.Check(t, got.Git.NoSubcommand, "Parse(%q) reports a subcommand %+v, want none", tt.command, got.Git.Subcommand)
			assert.Check(t, is.Len(got.Marks, 0), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenTheSubcommandIsNotLiteral_ReportsANonLiteralSubcommand(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	const command = `git "$x" status`

	got := lastWith(t, command, callerIn(f, f.Dir())).Git

	assert.Check(t, !got.NoSubcommand, "Parse(%q) reports no subcommand, want one that is not literal", command)
	assert.Check(t, is.DeepEqual(got.Subcommand, guard.Word{}), "Parse(%q) subcommand", command)
}

func TestParse_WhenAWordCannotNameAnAlias_MarksItWithoutReadingConfig(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	tests := []struct {
		name    string
		command string
		word    string
	}{
		{name: "long option that is not git's", command: "git --sparkle status", word: "--sparkle"},
		{name: "short option holding an attached value", command: "git -C=x status", word: "-C=x"},
		{name: "character git rejects in a config variable name", command: "git a_b", word: "a_b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder := &git.Recorder{}
			caller := callerIn(f, f.Dir())
			caller.Git.Recorder = recorder

			got := parseWith(t, tt.command, caller)

			assert.Assert(t, is.Len(got, 1), "Parse(%q) = %v", tt.command, texts(got))
			want := []guard.Mark{{Kind: guard.UnknownSubcommand, Name: tt.word}}
			assert.Check(t, is.DeepEqual(got[0].Marks, want), "Parse(%q) marks", tt.command)
			for _, call := range recorder.Calls() {
				assert.Check(t, !slices.Contains(call, "config"), "Parse(%q) read config: %v", tt.command, call)
			}
		})
	}
}

func TestParse_WhenAGlobalOptionLacksItsValue_MarksIt(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	const command = "git -C"

	got := lastWith(t, command, callerIn(f, f.Dir()))

	want := []guard.Mark{{Kind: guard.MissingGitValue, Name: "-C"}}
	assert.Check(t, is.DeepEqual(got.Marks, want), "Parse(%q) marks", command)
}

func TestParse_WhenAGlobalOptionLacksItsValue_ReportsNoSubcommandOrTarget(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	const command = "git -C"

	got := lastWith(t, command, callerIn(f, f.Dir()))

	assert.Check(t, got.Git.NoSubcommand, "Parse(%q) reports a subcommand %+v, want none", command, got.Git.Subcommand)
	assert.Check(t, got.Git.GitDirUnknown, "Parse(%q) git dir = %q, want unknown", command, got.Git.GitDir)
	assert.Check(t, got.Target.Unknown, "Parse(%q) target = %+v, want unknown", command, got.Target)
}

func TestJudge_WhenAnAliasNamesACommandAnOlderGitLacks_RefusesNamingIt(t *testing.T) {
	t.Parallel()
	f, caller := standingOn(t, "main", "topic")
	f.Git("config", "--local", "alias.replay", "log")

	checkJudged(t, caller, []judged{
		{name: "alias given by -c", command: "git -c alias.send-email=log send-email x.patch", wantNamed: "send-email", want: guard.Refuse},
		{name: "configured alias", command: "git replay --onto main topic", wantNamed: "replay", want: guard.Refuse},
	})
}
