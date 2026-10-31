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

// parseAt returns the invocations of command, read by a caller in dir whose git runs with env.
func parseAt(t *testing.T, command, dir string, env []string) []guard.Invocation {
	t.Helper()

	got, err := guard.Parse(t.Context(), command, guard.Caller{Home: home, Dir: dir, Git: git.Runner{Env: env}})
	assert.NilError(t, err, "Parse(%q)", command)

	return got
}

// lastAt returns the last invocation of command, read by a caller in dir whose git runs with env.
func lastAt(t *testing.T, command, dir string, env []string) guard.Invocation {
	t.Helper()

	got := parseAt(t, command, dir, env)
	assert.Assert(t, len(got) > 0, "Parse(%q) holds no invocation", command)

	return got[len(got)-1]
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
		{"bs", "bisect start"},
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
			name: "long option with a separate value", command: "git --namespace ns push origin",
			wantOptions: []guard.GitOption{{Name: "--namespace", Value: literal("ns")}}, wantSubcommand: "push", wantArgs: []string{"origin"},
		},
		{
			name: "config pair", command: "git -c core.pager=cat log",
			wantOptions: []guard.GitOption{{Name: "-c", Value: literal("core.pager=cat")}}, wantSubcommand: "log", wantArgs: []string{},
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
			name: "value spelled like a subcommand", command: "git -C remove worktree remove x",
			wantOptions: []guard.GitOption{{Name: "-C", Value: literal("remove")}}, wantSubcommand: "worktree", wantArgs: []string{"remove", "x"},
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
		{name: "alias of an alias", command: "git lg -5", wantSubcommand: "log", wantArgs: []string{"--graph", "--oneline", "-5"}},
		{name: "quoted words in an expansion", command: "git cm", wantSubcommand: "commit", wantArgs: []string{"-m", "two words"}},
		{name: "three levels", command: "git one", wantSubcommand: "log", wantArgs: []string{}},
		{name: "backslash escape in an expansion", command: "git spaced", wantSubcommand: "commit", wantArgs: []string{"-m", "a b"}},
		{name: "alias given by -c over the repository's", command: "git -c alias.sw=status sw", wantSubcommand: "status", wantArgs: []string{}},
		{
			name: "alias given by --config-env", command: "SW=status git --config-env=alias.sw=SW sw",
			wantSubcommand: "status", wantArgs: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lastAt(t, tt.command, f.Dir(), f.Env())

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

			got := lastAt(t, tt.command, outsideDir(t), tt.env)

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
		{
			name:    "shell alias given by -c over the repository's",
			command: "git -c alias.sw='!git push' sw",
			want:    []guard.Mark{{Kind: guard.ShellAlias, Name: "sw"}},
		},
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

			got := lastAt(t, tt.command, f.Dir(), f.Env())

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

	_, err := guard.Parse(t.Context(), "git co topic", guard.Caller{Home: home, Dir: f.Dir(), Git: git.Runner{Env: env}})

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
		{name: "directory that holds no repository", dir: outside, command: "git status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lastAt(t, tt.command, tt.dir, f.Env()).Git

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

			got := lastAt(t, tt.command, f.Dir(), f.Env()).Git

			assert.Check(t, got.GitDirUnknown, "Parse(%q) git dir = %q, want unknown", tt.command, got.GitDir)
			assert.Check(t, is.Equal(got.GitDir, ""), "Parse(%q) git dir", tt.command)
		})
	}
}

func TestParse_WhenGitCannotReadTheGitDir_FailsNamingTheCall(t *testing.T) {
	// Not parallel: gittest.FakeGit sets PATH for the whole process.
	f := gittest.New(t, "main")
	env := append(f.Env(), gittest.FakeGit(t, gittest.Fake{FailOn: []string{"rev-parse"}})...)

	_, err := guard.Parse(t.Context(), "git status", guard.Caller{Home: home, Dir: f.Dir(), Git: git.Runner{Env: env}})

	checkFailedCall(t, err, "--absolute-git-dir")
}

func TestParse_WhenNoInvocationRunsGit_StartsNoGit(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	recorder := &git.Recorder{}
	const command = "gh pr list && ls -la | grep x; echo git push"

	_, err := guard.Parse(t.Context(), command, guard.Caller{Home: home, Dir: f.Dir(), Git: git.Runner{Env: f.Env(), Recorder: recorder}})
	assert.NilError(t, err, "Parse(%q)", command)

	assert.Check(t, is.Len(recorder.Calls(), 0), "Parse(%q) git calls", command)
}

func TestParse_WhenAnInvocationMayMoveHeadThroughGitsAliases_ReportsHeadUnknownAfterIt(t *testing.T) {
	t.Parallel()

	f := aliased(t)
	tests := []struct {
		name    string
		command string
		want    []bool
	}{
		{name: "alias of switch", command: "git sw main && git commit -m x", want: []bool{false, true}},
		{name: "alias of a rebase naming a branch", command: "git rb; git commit", want: []bool{false, true}},
		{name: "alias of bisect", command: "git bs; git commit", want: []bool{false, true}},
		{name: "alias of checkout", command: "git co topic && git commit", want: []bool{false, true}},
		{name: "alias that moves nothing", command: "git lg; git commit", want: []bool{false, false}},
		{name: "alias move after the commit", command: "git commit; git sw main", want: []bool{false, false}},
		{name: "alias move in a subshell", command: "(git sw main); git commit", want: []bool{false, true}},
		{name: "alias running a shell command", command: "git sh; git commit", want: []bool{false, true}},
		{name: "word that is neither command nor alias", command: "git taxi; git commit", want: []bool{false, true}},
		{name: "alias nested too deep", command: "git d1; git commit", want: []bool{false, true}},
		{name: "alias in an unknown target", command: `git -C "$d" co; git commit`, want: []bool{false, true}},
		{name: "unknown option before the subcommand", command: "git --sparkle status; git commit", want: []bool{false, true}},
		{name: "option that prints and exits", command: "git --version; git commit", want: []bool{false, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			invocations := parseAt(t, tt.command, f.Dir(), f.Env())

			got := make([]bool, 0, len(invocations))
			for _, inv := range invocations {
				got = append(got, inv.HeadUnknown)
			}

			assert.Check(t, is.DeepEqual(got, tt.want), "Parse(%q) head unknown", tt.command)
		})
	}
}

func TestParse_WhenASubstitutionMovesHeadThroughAnAlias_ReportsHeadUnknownForTheCommandHoldingIt(t *testing.T) {
	t.Parallel()

	f := aliased(t)
	tests := []struct {
		name    string
		command string
	}{
		{name: "argument", command: `git commit -m "$(git sw main)"`},
		{name: "group redirect target", command: `{ git commit; } > "$(git sw main)"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parseAt(t, tt.command, f.Dir(), f.Env())

			var commit []guard.Invocation
			for _, inv := range got {
				if len(inv.Words) > 1 && inv.Words[1].Text == "commit" {
					commit = append(commit, inv)
				}
			}
			assert.Assert(t, is.Len(commit, 1), "Parse(%q) = %v", tt.command, texts(got))
			assert.Check(t, commit[0].HeadUnknown, "Parse(%q) git commit head unknown = false, want true", tt.command)
		})
	}
}

func TestParse_WhenAGlobalOptionPrintsAndExits_ReportsNoSubcommandAndNoMark(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	tests := []struct {
		name    string
		command string
	}{
		{name: "version", command: "git --version"},
		{name: "bare exec path", command: "git --exec-path"},
		{name: "word after the option", command: "git --version status"},
		{name: "bare git", command: "git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lastAt(t, tt.command, f.Dir(), f.Env())

			assert.Check(t, got.Git.NoSubcommand, "Parse(%q) reports a subcommand %+v, want none", tt.command, got.Git.Subcommand)
			assert.Check(t, is.Len(got.Marks, 0), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenTheSubcommandIsNotLiteral_ReportsItPresentAndNotLiteral(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	const command = `git "$x" status`

	got := lastAt(t, command, f.Dir(), f.Env()).Git

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

			got, err := guard.Parse(t.Context(), tt.command, guard.Caller{Home: home, Dir: f.Dir(), Git: git.Runner{Env: f.Env(), Recorder: recorder}})
			assert.NilError(t, err, "Parse(%q)", tt.command)

			assert.Assert(t, is.Len(got, 1), "Parse(%q) = %v", tt.command, texts(got))
			want := []guard.Mark{{Kind: guard.UnknownSubcommand, Name: tt.word}}
			assert.Check(t, is.DeepEqual(got[0].Marks, want), "Parse(%q) marks", tt.command)
			for _, call := range recorder.Calls() {
				assert.Check(t, !slices.Contains(call, "config"), "Parse(%q) read config: %v", tt.command, call)
			}
		})
	}
}

func TestParse_WhenAGlobalOptionLacksItsValue_MarksItAndReportsNoSubcommandOrTarget(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, "main")
	tests := []struct {
		name    string
		command string
		option  string
	}{
		{name: "short option", command: "git -C", option: "-C"},
		{name: "long option", command: "git --git-dir", option: "--git-dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lastAt(t, tt.command, f.Dir(), f.Env())

			want := []guard.Mark{{Kind: guard.MissingGitValue, Name: tt.option}}
			assert.Check(t, is.DeepEqual(got.Marks, want), "Parse(%q) marks", tt.command)
			assert.Check(t, got.Git.NoSubcommand, "Parse(%q) reports a subcommand %+v, want none", tt.command, got.Git.Subcommand)
			assert.Check(t, is.DeepEqual(got.Git.Subcommand, guard.Word{}), "Parse(%q) subcommand", tt.command)
			assert.Check(t, got.Git.GitDirUnknown, "Parse(%q) git dir = %q, want unknown", tt.command, got.Git.GitDir)
			assert.Check(t, is.Equal(got.Git.GitDir, ""), "Parse(%q) git dir", tt.command)
			assert.Check(t, got.Target.Unknown, "Parse(%q) target = %+v, want unknown", tt.command, got.Target)
		})
	}
}
