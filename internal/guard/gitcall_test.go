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
	assert.Check(t, is.Equal(exitErr.Code, gittest.FailedCode))
	assert.Check(t, slices.Contains(exitErr.Argv, arg), "failed call %v, want one holding %q", exitErr.Argv, arg)
}

func TestParse_WhenAGitConfigReadFails_FailsNamingTheCall(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := aliased(t)
	env := append(f.Env(), gittest.FakeGit(t, gittest.Fake{FailOn: []string{"config"}})...)

	_, err := guard.Parse(t.Context(), "git co topic", callerWithEnv(f.Dir(), env))

	checkFailedCall(t, err, "alias.co")
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
		{name: "cd to a missing directory", command: "cd missing && git status"},
		{name: "non-literal -C", command: `git -C "$d" status`},
		{name: "non-literal --git-dir", command: `git --git-dir="$d" status`},
		{name: "non-literal --work-tree", command: `git --work-tree="$d" status`},
		{name: "non-literal GIT_DIR", command: `GIT_DIR="$d" git status`},
		{name: "unknown directory", command: `cd "$d" && git status`},
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

func TestParse_WhenGitRunsAShellCommandItHolds_ParsesItsCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    [][]string
	}{
		{name: "rebase --exec with a separate value", command: "git rebase --exec 'git push origin main' HEAD~1", want: [][]string{{"git", "rebase", "--exec", "git push origin main", "HEAD~1"}, {"git", "push", "origin", "main"}}},
		{name: "rebase --exec with an attached value", command: "git rebase --exec='git push origin main' HEAD~1", want: [][]string{{"git", "rebase", "--exec=git push origin main", "HEAD~1"}, {"git", "push", "origin", "main"}}},
		{name: "rebase --exec spelled as a prefix", command: "git rebase --exe 'git push origin main' HEAD~1", want: [][]string{{"git", "rebase", "--exe", "git push origin main", "HEAD~1"}, {"git", "push", "origin", "main"}}},
		{name: "rebase -x with a separate value", command: "git rebase -x 'git push origin main' HEAD~1", want: [][]string{{"git", "rebase", "-x", "git push origin main", "HEAD~1"}, {"git", "push", "origin", "main"}}},
		{name: "rebase -x with an attached value", command: "git rebase -x' git push origin main' HEAD~1", want: [][]string{{"git", "rebase", "-x git push origin main", "HEAD~1"}, {"git", "push", "origin", "main"}}},
		{name: "rebase -x in a short-flag bundle", command: "git rebase -ix 'git push origin main' HEAD~1", want: [][]string{{"git", "rebase", "-ix", "git push origin main", "HEAD~1"}, {"git", "push", "origin", "main"}}},
		{name: "core.editor given by -c", command: "git -c core.editor='git push origin main' commit", want: [][]string{{"git", "-c", "core.editor=git push origin main", "commit"}, {"git", "push", "origin", "main"}}},
		{name: "pager of one command given by -c", command: "git -c pager.log='git push origin main' log", want: [][]string{{"git", "-c", "pager.log=git push origin main", "log"}, {"git", "push", "origin", "main"}}},
		{name: "key spelled in another case", command: "git -c CORE.Pager='git push origin main' log", want: [][]string{{"git", "-c", "CORE.Pager=git push origin main", "log"}, {"git", "push", "origin", "main"}}},
		{name: "pager key spelled in another case", command: "git -c Pager.Log='git push origin main' log", want: [][]string{{"git", "-c", "Pager.Log=git push origin main", "log"}, {"git", "push", "origin", "main"}}},
		{name: "core.pager given by --config-env", command: "P='git push origin main' git --config-env=core.pager=P log", want: [][]string{{"git", "--config-env=core.pager=P", "log"}, {"git", "push", "origin", "main"}}},
		{name: "core.fsmonitor given by -c", command: "git -c core.fsmonitor='git push origin main' status", want: [][]string{{"git", "-c", "core.fsmonitor=git push origin main", "status"}, {"git", "push", "origin", "main"}}},
		{name: "diff.external given by -c", command: "git -c diff.external='git push origin main' diff", want: [][]string{{"git", "-c", "diff.external=git push origin main", "diff"}, {"git", "push", "origin", "main"}}},
		{name: "diff driver textconv given by -c", command: "git -c diff.x.textconv='git push origin main' diff", want: [][]string{{"git", "-c", "diff.x.textconv=git push origin main", "diff"}, {"git", "push", "origin", "main"}}},
		{name: "filter driver smudge given by -c", command: "git -c filter.x.smudge='git push origin main' status", want: [][]string{{"git", "-c", "filter.x.smudge=git push origin main", "status"}, {"git", "push", "origin", "main"}}},
		{name: "merge driver given by -c", command: "git -c merge.x.driver='git push origin main' status", want: [][]string{{"git", "-c", "merge.x.driver=git push origin main", "status"}, {"git", "push", "origin", "main"}}},
		{name: "credential helper run by the shell", command: "git -c credential.helper='!git push origin main' fetch", want: [][]string{{"git", "-c", "credential.helper=!git push origin main", "fetch"}, {"git", "push", "origin", "main"}}},
		{name: "credential helper of a URL, key in another case", command: "git -c credential.https://h.Helper='!git push origin main' fetch", want: [][]string{{"git", "-c", "credential.https://h.Helper=!git push origin main", "fetch"}, {"git", "push", "origin", "main"}}},
		{name: "credential helper named by its suffix", command: "git -c credential.helper=store fetch", want: [][]string{{"git", "-c", "credential.helper=store", "fetch"}, {"git", "credential-store"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Check(t, is.DeepEqual(texts(got), tt.want), "Parse(%q)", tt.command)
		})
	}
}

func TestParse_WhenAGitValueIsNoShellCommand_ParsesNothingMore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "cherry-pick -x flag", command: "git cherry-pick -x abc123"},
		{name: "-c key git does not run", command: "git -c user.name='git push origin main' log"},
		{name: "empty credential helper", command: "git -c credential.helper= fetch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := only(t, tt.command)

			assert.Check(t, is.Len(got.Marks, 0), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenAShellCommandGitRunsCannotBeRead_MarksIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    guard.MarkKind
		index   int
	}{
		{name: "non-literal rebase --exec value", command: `git rebase --exec "$c" HEAD~1`, want: guard.DynamicScript},
		{name: "non-literal attached rebase -x value", command: `git rebase -x"$c" HEAD~1`, want: guard.DynamicScript},
		{name: "non-literal core.pager value", command: `git -c core.pager="$p" log`, want: guard.DynamicScript},
		{name: "core.pager variable the parse cannot read", command: "git --config-env core.pager=ABSENT log", want: guard.DynamicScript},
		{name: "rebase --exec past the third level", command: "eval eval eval git rebase -x true HEAD~1", index: 3, want: guard.DeepScript},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Assert(t, len(got) > tt.index, "Parse(%q) = %v", tt.command, texts(got))
			assert.Check(t, is.DeepEqual(got[tt.index].Marks, []guard.Mark{{Kind: tt.want}}), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenAGitAliasRunsARebaseExec_ParsesTheExecCommand(t *testing.T) {
	t.Parallel()

	f := aliased(t)
	tests := []struct {
		name    string
		command string
	}{
		{name: "exec given after an alias of rebase", command: "git rb -x 'git push origin main'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parseWith(t, tt.command, callerIn(f, f.Dir()))

			assert.Check(t, is.Contains(texts(got), []string{"git", "push", "origin", "main"}), "Parse(%q)", tt.command)
		})
	}
}
