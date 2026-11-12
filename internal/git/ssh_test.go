package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/logtest"
)

// renames is an ssh config that gives github.com another name.
const renames = "Host github-personal\n  HostName github.com\n"

func TestRepo_OriginURLs_WhenURLIsSSHWithAnAlias_ReturnsTheHostSSHConnectsTo(t *testing.T) {
	t.Parallel()
	url := "ssh://git@github-personal/jeffzi/ils.git"
	f := gittest.New(t, trunk)
	f.SetOriginURL(url)
	f.SetPushURL(url)
	f.SetSSHConfig(renames)
	repo := newRepo(f)

	got, err := repo.OriginURLs(t.Context())
	assert.NilError(t, err, "OriginURLs() of %q", url)

	named := ilsOrigin()
	want := git.OriginURLs{Fetch: named, FetchURL: url, Push: []git.PushURL{{Origin: named, URL: url}}}
	assert.Check(t, is.DeepEqual(got, want), "OriginURLs() of %q", url)
}

// noSSHCommand returns repo without the fixture's GIT_SSH_COMMAND in its environment, so that it
// reads a host through the ssh command a test configures, or else through the ssh on PATH.
func noSSHCommand(repo git.Repo) git.Repo {
	repo.Runner.Env = gittest.Without(repo.Runner.Env, "GIT_SSH_COMMAND")

	return repo
}

// firstOnRunnerPath returns repo with the directory of the program at path, in the slash form, put
// first on the PATH of its runner's environment.
func firstOnRunnerPath(repo git.Repo, path string) git.Repo {
	repo.Runner.Env = gittest.FirstOnPath(repo.Runner.Env, filepath.Dir(filepath.FromSlash(path)))

	return repo
}

// plainSSH returns repo with no ssh command, and the test binary first on the PATH of its runner's
// environment under ssh's name, acting as the plain ssh ils runs when no ssh command is configured,
// in the stand-in mode plainSSHEnv takes. The process PATH stays as it is.
func plainSSH(t *testing.T, repo git.Repo, mode string) git.Repo {
	t.Helper()

	repo = firstOnRunnerPath(noSSHCommand(repo), standIn(t, "ssh"))
	repo.Runner.Env = append(repo.Runner.Env, plainSSHEnv+"="+mode)

	return repo
}

// hideSSH returns repo with no ssh command, and an empty directory as the PATH of its runner's
// environment, so that its next ssh call finds no ssh there while the process PATH still holds one.
func hideSSH(t *testing.T, repo git.Repo) git.Repo {
	t.Helper()

	_, err := exec.LookPath("ssh")
	assert.NilError(t, err, "the process PATH holds no ssh, so a lookup on it would find none either")

	repo.Runner.Env = gittest.WithoutSSH(t, repo.Runner.Env)

	return repo
}

// hideFromShell returns repo with no ssh command, and as the PATH of its runner's environment an
// empty directory and, on Windows, the directory of git's shell, so that a command line naming
// program finds no program of that name while the process PATH still holds one. Git for Windows'
// shell crashes when its PATH lacks its own directory, which also holds ssh, so program must be a
// program found elsewhere.
func hideFromShell(t *testing.T, repo git.Repo, program string) git.Repo {
	t.Helper()

	_, err := exec.LookPath(program)
	assert.NilError(t, err, "the process PATH holds no %s, so a lookup on it would find none either", program)

	env := gittest.WithoutSSH(t, repo.Runner.Env)
	if runtime.GOOS == "windows" {
		shell, err := exec.LookPath("sh")
		assert.NilError(t, err, "the process PATH holds no sh, so git's shell is not found")
		env = gittest.FirstOnPath(env, filepath.Dir(shell))
		_, err = exec.LookPath(filepath.Join(filepath.Dir(shell), program))
		assert.Assert(t, err != nil, "the directory of git's shell holds %s, so the shell would find it", program)
	}
	repo.Runner.Env = env

	return repo
}

func TestRepo_OriginURLs_WhenSSHMissingFromRunnerPath_ReturnsToolMissingError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(sshURL)
	repo := hideSSH(t, newRepo(f))

	_, err := repo.OriginURLs(t.Context())

	missing, ok := errors.AsType[*child.ToolMissingError](err)
	assert.Assert(t, ok, "OriginURLs() error = %v, want *child.ToolMissingError", err)
	assert.Check(t, is.Equal(missing.Tool, "ssh"))
}

func TestRepo_OriginURLs_WhenContextEndsWhileGitSSHProgramRuns_ReturnsContextError(t *testing.T) {
	t.Parallel()
	f := aliasFixture(t)
	f.SetPushURL(httpsURL)
	addr, started := gittest.SilentListener(t)
	repo := noSSHCommand(newRepo(f))
	repo.Runner.Env = append(append(repo.Runner.Env, gitSSHStandIn(t), sshStandInEnv+"="+standInLinger, helperAddrEnv+"="+addr),
		variantEnv("ssh")...)
	ctx := gittest.Ending(t.Context(), started, context.Canceled)

	_, err := repo.OriginURLs(ctx)

	assert.Check(t, is.ErrorIs(err, context.Canceled))
}

func TestRepo_OriginURLs_WhenSSHPrintsNoHostname_ReturnsErrorNamingTheSSHCall(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(sshURL)
	repo := plainSSH(t, newRepo(f), standInNoHostname)

	_, err := repo.OriginURLs(t.Context())

	assert.Check(t, is.ErrorContains(err, "ssh -G -- github.com"))
	_, exited := errors.AsType[*child.ExitError](err)
	assert.Check(t, !exited, "OriginURLs() error = %v, want an error from ssh's output, not its exit", err)
}

func TestRepo_OriginURLs_WhenConfiguredSSHPrintsNoHostname_ReturnsErrorNamingIt(t *testing.T) {
	t.Parallel()
	f := aliasFixture(t)
	configured := gittest.ShellWord(standIn(t, "ssh"))
	repo := noSSHCommand(newRepo(f))
	repo.Runner.Env = append(repo.Runner.Env, "GIT_SSH_COMMAND="+configured, sshStandInEnv+"="+standInNoHostname)

	_, err := repo.OriginURLs(t.Context())

	assert.Check(t, is.ErrorContains(err, configured))
}

// aliases is an ssh config that gives two hosts other names: work stands for git.example.org, and
// github-work for ssh.github.com, a host under github.com.
const aliases = "Host work\n  HostName git.example.org\nHost github-work\n  HostName ssh.github.com\n"

const (
	// aliasURL names its host by the alias aliases gives git.example.org.
	aliasURL = "git@work:acme/widgets.git"
	// plainHost is the hostname the stand-in for plain ssh answers with, which no configured command
	// gives.
	plainHost = "plain.example"
	// plainAnswer is the stand-in mode that makes plain ssh answer with plainHost.
	plainAnswer = standInAnswer + plainHost
	// standInHost is the hostname a stand-in ssh answers with, which no ssh config gives.
	standInHost = "stand-in.example"
)

// sshReading returns a command line that runs ssh with a config file of its own holding content as
// its one config file.
func sshReading(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	gittest.WriteFile(t, dir, "config", content)

	return "ssh -F " + gittest.ShellWord(filepath.Join(dir, "config"))
}

// aliasFixture returns a new fixture whose origin is aliasURL.
func aliasFixture(t *testing.T) *gittest.Fixture {
	t.Helper()

	f := gittest.New(t, trunk)
	f.SetOriginURL(aliasURL)

	return f
}

// verbatim returns path as it is, the way GIT_SSH names a program.
func verbatim(path string) string { return path }

// answering returns the setting that makes a stand-in ssh answer `-G <host>` with host.
func answering(host string) string { return sshStandInEnv + "=" + standInAnswer + host }

// refusing is the setting that makes a stand-in ssh exit non-zero.
const refusing = sshStandInEnv + "=" + standInRefuse

// exiting returns the setting that makes a stand-in ssh exit with code.
func exiting(code int) string { return sshStandInEnv + "=" + standInExit + strconv.Itoa(code) }

// standIn links the test binary under name into a directory of its own whose name holds a space,
// and returns its path in the slash form, for an ssh command that answers as sshStandInEnv says.
func standIn(t *testing.T, name string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "stand in")
	assert.NilError(t, os.Mkdir(dir, 0o700))

	return filepath.ToSlash(gittest.Link(t, dir, name))
}

// gitSSHStandIn returns the setting that names the stand-in ssh as the program GIT_SSH runs.
func gitSSHStandIn(t *testing.T) string {
	t.Helper()

	return "GIT_SSH=" + standIn(t, "ssh")
}

// sourceCase is one place a user names the ssh command git connects with.
type sourceCase struct {
	// arrange configures the command and returns the settings it adds to the runner's environment.
	arrange func(t *testing.T, f *gittest.Fixture) []string
	name    string
	// host is the host OriginURLs reports for aliasURL.
	host string
}

func sourceCases() []sourceCase {
	const decoys = "Host work\n  HostName decoy.example.org\n"

	return []sourceCase{
		{
			name: "GIT_SSH program whose path holds a space", host: "work.example.org",
			arrange: func(t *testing.T, _ *gittest.Fixture) []string {
				t.Helper()

				return []string{gitSSHStandIn(t), sshStandInEnv + "=" + standInSuffix + ".example.org"}
			},
		},
		{
			name: "GIT_SSH_COMMAND over core.sshCommand", host: "git.example.org",
			arrange: func(t *testing.T, f *gittest.Fixture) []string {
				t.Helper()
				f.Git("config", "core.sshCommand", sshReading(t, decoys))

				return []string{"GIT_SSH_COMMAND=" + sshReading(t, aliases)}
			},
		},
		{
			name: "core.sshCommand over GIT_SSH", host: "git.example.org",
			arrange: func(t *testing.T, f *gittest.Fixture) []string {
				t.Helper()
				f.Git("config", "core.sshCommand", sshReading(t, aliases))

				return []string{gitSSHStandIn(t), answering(standInHost)}
			},
		},
	}
}

func TestRepo_OriginURLs_WhenSSHCommandSet_ReturnsTheHostThatCommandConnectsTo(t *testing.T) {
	t.Parallel()
	for _, tt := range sourceCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := aliasFixture(t)
			repo := noSSHCommand(newRepo(f))
			repo.Runner.Env = append(repo.Runner.Env, tt.arrange(t, f)...)

			got, err := repo.OriginURLs(t.Context())
			assert.NilError(t, err)

			assert.Check(t, is.Equal(got.Fetch.Host, tt.host))
		})
	}
}

func TestRepo_OriginURLs_WhenRunnerSetsNoEnvironment_ReadsTheProcessEnvironment(t *testing.T) {
	// Not parallel: this sets the environment of the whole process, which alone names the ssh
	// command.
	f := aliasFixture(t)
	useProcessEnv(t, append(f.Env(), "GIT_SSH_COMMAND="+sshReading(t, aliases)))
	repo := git.Repo{Runner: git.Runner{Dir: f.Dir()}}

	got, err := repo.OriginURLs(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got.Fetch.Host, "git.example.org"))
}

func TestRepo_OriginURLs_WhenPlainSSHAnswersAHost_FoldsOnlyHostsUnderGitHub(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		answer string
		want   string
	}{
		{name: "host under github.com in mixed case", answer: "SSH.GitHub.com", want: "github.com"},
		{name: "lookalike host ending in github.com", answer: "fake-github.com", want: "fake-github.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := aliasFixture(t)
			repo := plainSSH(t, newRepo(f), standInAnswer+tt.answer)

			got, err := repo.OriginURLs(t.Context())
			assert.NilError(t, err)

			assert.Check(t, is.Equal(got.Fetch.Host, tt.want))
		})
	}
}

// variantCase is one ssh command a user configured, run as its ssh variant says.
type variantCase struct {
	// line, when set, writes the command line that names the stand-in at a path; gittest.ShellWord does
	// otherwise.
	line func(path string) string
	name string
	// command is the name of the stand-in ssh the user configured.
	command string
	// variant, when not empty, is the repository's ssh.variant.
	variant string
	// want is the host OriginURLs reports: standInHost when the command answered `-G <host>`, and
	// plainHost when plain ssh did.
	want string
	// env holds the settings added to the runner's environment.
	env []string
	// refuses makes the stand-in exit non-zero.
	refuses bool
	// program names the stand-in in GIT_SSH in place of GIT_SSH_COMMAND.
	program bool
}

// unclosed returns path after a single quote that nothing closes, a line git cannot split.
func unclosed(path string) string { return "'" + path }

// variantEnv returns the setting that gives git's ssh variant as name.
func variantEnv(name string) []string { return []string{"GIT_SSH_VARIANT=" + name} }

// variantCases are the ssh commands a user configures, by name and variant, and the host each
// reads.
func variantCases() []variantCase {
	return []variantCase{
		{name: "command named PLINK.EXE", command: "PLINK.EXE", want: plainHost},
		{name: "command line with an unclosed quote", command: "ssh", line: unclosed, want: plainHost},
		{name: "unrecognized command that answers", command: "connect", want: standInHost},
		{name: "unrecognized command that refuses", command: "connect", refuses: true, want: plainHost},
		{name: "GIT_SSH program named plink", command: "plink", program: true, want: plainHost},
		{name: "GIT_SSH_VARIANT plink", command: "ssh", env: variantEnv("plink"), want: plainHost},
		{name: "GIT_SSH_VARIANT auto with a command named plink", command: "plink", env: variantEnv("auto"), want: plainHost},
		{name: "ssh.variant simple", command: "ssh", variant: "simple", want: plainHost},
		{name: "GIT_SSH_VARIANT ssh over ssh.variant", command: "plink", env: variantEnv("ssh"), variant: "simple", want: standInHost},
	}
}

func TestRepo_OriginURLs_WhenSSHCommandConfigured_AsksItOrPlainSSHAsItsVariantSays(t *testing.T) {
	t.Parallel()
	for _, tt := range variantCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := aliasFixture(t)
			if tt.variant != "" {
				f.Git("config", "ssh.variant", tt.variant)
			}
			path := standIn(t, tt.command)
			line := gittest.ShellWord
			if tt.line != nil {
				line = tt.line
			}
			setting := "GIT_SSH_COMMAND=" + line(path)
			if tt.program {
				setting = "GIT_SSH=" + path
			}
			mode := answering(standInHost)
			if tt.refuses {
				mode = refusing
			}
			repo := plainSSH(t, newRepo(f), plainAnswer)
			repo.Runner.Env = append(append(repo.Runner.Env, setting, mode), tt.env...)

			got, err := repo.OriginURLs(t.Context())
			assert.NilError(t, err)

			assert.Check(t, is.Equal(got.Fetch.Host, tt.want))
		})
	}
}

func TestRepo_OriginURLs_WhenConfiguredSSHFails_ReturnsErrorNamingIt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		configure func(path string) string
		name      string
		key       string
	}{
		{name: "command exits non-zero", key: "GIT_SSH_COMMAND", configure: gittest.ShellWord},
		{name: "program exits non-zero", key: "GIT_SSH", configure: verbatim},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := aliasFixture(t)
			configured := tt.configure(standIn(t, "ssh"))
			repo := noSSHCommand(newRepo(f))
			repo.Runner.Env = append(repo.Runner.Env, tt.key+"="+configured, refusing)

			_, err := repo.OriginURLs(t.Context())

			exitErr, ok := errors.AsType[*child.ExitError](err)
			assert.Assert(t, ok, "OriginURLs() error = %v, want *child.ExitError", err)
			assert.Check(t, is.Equal(exitErr.Argv[0], configured))
			assert.Check(t, is.Contains(exitErr.Stderr, standInRefusal))
		})
	}
}

// missingCase is one ssh command a user configured whose program is not found.
type missingCase struct {
	name string
	// setting is where the command is configured: GIT_SSH_COMMAND, core.sshCommand or GIT_SSH.
	setting string
	// value is the command line, or the program GIT_SSH names.
	value string
	// program is the program the error names, for a command whose program is looked up.
	program string
	// env holds the settings added to the runner's environment.
	env []string
	// hide leaves program off the PATH of the runner's environment while the process PATH stays.
	hide bool
}

// missingCases are the ssh commands a user configures whose program is not found, by setting and
// shape of the command line.
func missingCases() []missingCase {
	return []missingCase{
		{name: "GIT_SSH_COMMAND path alone", setting: "GIT_SSH_COMMAND", value: "/no/such/ssh", program: "/no/such/ssh"},
		{
			name: "GIT_SSH_COMMAND name found only on the process PATH", setting: "GIT_SSH_COMMAND", value: "go -v",
			program: "go", hide: true,
		},
		{
			name: "GIT_SSH_COMMAND assignment before a name", setting: "GIT_SSH_COMMAND", value: "AUDIT=1 no-such-wrapper -v",
			program: "no-such-wrapper",
		},
		{
			name: "GIT_SSH_COMMAND exec before a name", setting: "GIT_SSH_COMMAND", value: "exec no-such-wrapper -v",
			program: "no-such-wrapper",
		},
		{
			name: "GIT_SSH_COMMAND env and an assignment before a name", setting: "GIT_SSH_COMMAND",
			value: "env AUDIT=1 no-such-wrapper", program: "no-such-wrapper",
		},
		{
			name: "core.sshCommand unrecognized name with an argument", setting: "core.sshCommand",
			value: "no-such-wrapper -v", program: "no-such-wrapper",
		},
		{name: "GIT_SSH path", setting: "GIT_SSH", value: "/no/such/ssh", program: "/no/such/ssh"},
		{name: "GIT_SSH name found only on the process PATH", setting: "GIT_SSH", value: "ssh", program: "ssh", hide: true},
		{name: "GIT_SSH path named plink", setting: "GIT_SSH", value: "/no/such/plink", program: "/no/such/plink"},
	}
}

// missingRepo returns the repository of a new fixture whose origin is aliasURL, a plain ssh
// answering plainHost, and the ssh command of tt.
func missingRepo(t *testing.T, tt missingCase) git.Repo {
	t.Helper()

	f := aliasFixture(t)
	repo := plainSSH(t, newRepo(f), plainAnswer)
	switch {
	case tt.hide && tt.setting == "GIT_SSH":
		repo = hideSSH(t, repo)
	case tt.hide:
		repo = hideFromShell(t, repo, tt.program)
	}
	if tt.setting == "core.sshCommand" {
		f.Git("config", "core.sshCommand", tt.value)
	} else {
		repo.Runner.Env = append(repo.Runner.Env, tt.setting+"="+tt.value)
	}
	repo.Runner.Env = append(repo.Runner.Env, tt.env...)

	return repo
}

func TestRepo_OriginURLs_WhenSSHCommandNotFound_ReturnsSSHCommandMissingError(t *testing.T) {
	// A plain ssh that ran in place of the missing command would answer, failing nothing.
	t.Parallel()
	for _, tt := range missingCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := missingRepo(t, tt)

			_, err := repo.OriginURLs(t.Context())

			missing, ok := errors.AsType[*git.SSHCommandMissingError](err)
			assert.Assert(t, ok, "OriginURLs() error = %v, want *git.SSHCommandMissingError", err)
			assert.Check(t, is.Equal(missing.Setting, tt.setting))
			assert.Check(t, is.Equal(missing.Program, tt.program))
			_, tool := errors.AsType[*child.ToolMissingError](err)
			assert.Check(t, !tool, "OriginURLs() error = %v, want no *child.ToolMissingError", err)
		})
	}
}

func TestRepo_OriginURLs_WhenCommandLineOfVariantPlainNotFound_AsksPlainSSH(t *testing.T) {
	// ils never runs such a line, and its first word may be a shell builtin or an assignment, so it
	// does not look the word up.
	t.Parallel()
	repo := missingRepo(t, missingCase{
		setting: "GIT_SSH_COMMAND", value: "AUDIT=1 no-such-wrapper -v", env: variantEnv("putty"),
	})

	got, err := repo.OriginURLs(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got.Fetch.Host, plainHost))
}

func TestSSHCommandMissingError_Error_WhenProgramNotFound_NamesWhatIsMissing(t *testing.T) {
	t.Parallel()
	err := &git.SSHCommandMissingError{Setting: "core.sshCommand", Program: "no-such-wrapper"}

	got := err.Error()

	assert.Check(t, is.Contains(got, "no-such-wrapper"))
	assert.Check(t, is.Contains(got, "core.sshCommand"))
}

// exitingCase is one ssh command a user configured whose program is found but itself exits with
// the code git gives a program it cannot find.
type exitingCase struct {
	// line writes the command line, given the path of a stand-in named connect that is also first on
	// the PATH of the runner's environment.
	line func(path string) string
	name string
}

// exitingCases are the ssh commands a user configures whose found program exits 127.
func exitingCases() []exitingCase {
	byName := func(string) string { return "connect" }

	return []exitingCase{
		{name: "path exits 127", line: gittest.ShellWord},
		{name: "name on the runner's PATH exits 127", line: byName},
		{name: "env and an option before a name exit 127", line: func(string) string { return "env -u AUDIT connect" }},
	}
}

// exitingRepo returns the repository of a new fixture whose origin is aliasURL, a plain ssh
// answering plainHost, and the command line of tt, of variant, in GIT_SSH_COMMAND.
func exitingRepo(t *testing.T, tt exitingCase, variant string) git.Repo {
	t.Helper()

	f := aliasFixture(t)
	path := standIn(t, "connect")
	repo := firstOnRunnerPath(plainSSH(t, newRepo(f), plainAnswer), path)
	line := tt.line(path)
	repo.Runner.Env = append(append(repo.Runner.Env, "GIT_SSH_COMMAND="+line, exiting(127)), variantEnv(variant)...)

	return repo
}

func TestRepo_OriginURLs_WhenFoundSSHCommandExitsAsIfMissing_ReturnsItsExitError(t *testing.T) {
	t.Parallel()
	for _, tt := range exitingCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := exitingRepo(t, tt, "ssh")

			_, err := repo.OriginURLs(t.Context())

			_, ok := errors.AsType[*child.ExitError](err)
			assert.Check(t, ok, "OriginURLs() error = %v, want *child.ExitError", err)
		})
	}
}

// useProcessEnv makes env the whole environment of the process until the test ends. It cannot run
// in a parallel test.
func useProcessEnv(t *testing.T, env []string) {
	t.Helper()

	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "" {
			continue
		}

		t.Setenv(key, "")
		assert.NilError(t, os.Unsetenv(key))
	}

	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
}

func TestRepo_OriginURLs_WhenRunnerSetsNoEnvironment_FindsSSHOnTheProcessPath(t *testing.T) {
	// Not parallel: this sets the environment of the whole process, whose PATH alone holds the
	// stand-in.
	f := aliasFixture(t)
	repo := plainSSH(t, newRepo(f), plainAnswer)
	useProcessEnv(t, repo.Runner.Env)
	repo.Runner.Env = nil

	got, err := repo.OriginURLs(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got.Fetch.Host, plainHost))
}

// callsOf returns the attributes of each exec record in records that names tool, all but the
// duration.
func callsOf(records []logtest.Record, tool string) []map[string]any {
	var calls []map[string]any

	for _, record := range records {
		if record.Message == child.ExecMessage && record.Map()[child.KeyTool] == tool {
			calls = append(calls, record.Call())
		}
	}

	return calls
}

func TestRepo_OriginURLs_WhenLoggerAttachedAndSSHReadsHost_LogsEachSSHRun(t *testing.T) {
	t.Parallel()
	f := aliasFixture(t)
	f.SetPushURL(httpsURL)
	repo := plainSSH(t, newRepo(f), plainAnswer)
	logger, sink := logtest.New()
	repo.Runner.Logger = logger

	_, err := repo.OriginURLs(t.Context())
	assert.NilError(t, err)

	want := logtest.ExecAttrs(t, child.Exec{Tool: "ssh", Args: []string{"-G", "--", "work"}})
	assert.Check(t, is.DeepEqual(callsOf(sink.Records(), "ssh"), []map[string]any{want}))
}

func TestEnvValue(t *testing.T) {
	t.Parallel()
	onWindows := runtime.GOOS == "windows"
	otherCase := ""
	if onWindows {
		otherCase = "/x"
	}
	tests := []struct {
		name    string
		key     string
		want    string
		env     []string
		wantSet bool
	}{
		{name: "set twice keeps the last", env: []string{"TERM=xterm", "TERM=dumb"}, key: "TERM", want: "dumb", wantSet: true},
		{name: "set to nothing", env: []string{"NO_COLOR="}, key: "NO_COLOR", want: "", wantSet: true},
		{name: "only a longer key set", env: []string{"TERMINAL=xterm"}, key: "TERM", wantSet: false},
		{name: "key in another case set", env: []string{"home=/x"}, key: "HOME", want: otherCase, wantSet: onWindows},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, set := git.EnvValue(tt.env, tt.key)

			assert.Check(t, is.Equal(set, tt.wantSet), "EnvValue(%q, %q) set", tt.env, tt.key)
			assert.Check(t, is.Equal(got, tt.want), "EnvValue(%q, %q)", tt.env, tt.key)
		})
	}
}

func TestWithoutKeys_WhenKeysAreNamed_LeavesEveryOtherEntryInOrder(t *testing.T) {
	t.Parallel()
	env := []string{"HOME=/h", "TERMINAL=xterm", "TERM=dumb", "XDG_CONFIG_HOME=/c", "PATH=/p"}

	got := git.WithoutKeys(env, "HOME", "TERM", "XDG_CONFIG_HOME")

	assert.Check(t, is.DeepEqual(got, []string{"TERMINAL=xterm", "PATH=/p"}))
}

func TestSameKey_WhenCaseDiffers_MatchesOnlyOnWindows(t *testing.T) {
	t.Parallel()

	got := git.SameKey("Path", "PATH")

	assert.Check(t, is.Equal(got, runtime.GOOS == "windows"), "SameKey(%q, %q) on %s", "Path", "PATH", runtime.GOOS)
}

func TestSplitCommandLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		want []string
	}{
		{name: "run of spaces separates once", line: "a  b", want: []string{"a", "b"}},
		{name: "vertical tab does not separate", line: "a\vb", want: []string{"a\vb"}},
		{name: "leading space gives an empty first word", line: " a", want: []string{"", "a"}},
		{name: "trailing space gives an empty last word", line: "a ", want: []string{"a", ""}},
		{name: "single quotes keep a backslash", line: `'a\ b'`, want: []string{`a\ b`}},
		{name: "double quotes group and a backslash escapes", line: `"a \"b\""`, want: []string{`a "b"`}},
		{name: "backslash outside quotes escapes a space", line: `a\ b`, want: []string{"a b"}},
		{name: "empty quotes are an empty word", line: `''`, want: []string{""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := git.SplitCommandLine(tt.line)
			assert.Assert(t, ok, "SplitCommandLine(%q) refused the line", tt.line)

			assert.Check(t, is.DeepEqual(got, tt.want), "SplitCommandLine(%q)", tt.line)
		})
	}
}

func TestSplitCommandLine_WhenGitRefusesTheLine_ReportsFalse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
	}{
		{name: "quote left open", line: `a "b`},
		{name: "backslash at the end", line: `a\`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, ok := git.SplitCommandLine(tt.line)

			assert.Check(t, !ok, "SplitCommandLine(%q) split the line", tt.line)
		})
	}
}

func TestProgramWord(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		line      string
		want      string
		wantFound bool
	}{
		{name: "vertical tab stays inside the program word", line: "ssh\v-v", want: "ssh\v-v", wantFound: true},
		{name: "leading space before the program", line: " ssh -v", want: "ssh", wantFound: true},
		{name: "only an assignment and a trailing space", line: "FOO=bar ", want: "", wantFound: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, found := git.ProgramWord(tt.line)

			assert.Check(t, is.Equal(found, tt.wantFound), "ProgramWord(%q) found", tt.line)
			assert.Check(t, is.Equal(got, tt.want), "ProgramWord(%q)", tt.line)
		})
	}
}

func FuzzSplitCommandLine(f *testing.F) {
	for _, seed := range []string{
		"ssh", `plink.exe -batch`, `ssh -i "my key" -p 22`, `'C:\Program Files\PuTTY\plink.exe' -batch`,
		`GIT_TRACE=1 env FOO=bar ssh -v`, `exec -a name ssh`, `command ssh`, `ssh "unclosed`, `ssh\`,
		" ssh", "ssh ", "ssh\t-v", "ssh\v-v", "ssh\f-v", "ssh\r-v", `""`, `a\ b`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, line string) {
		words, split := git.SplitCommandLine(line)

		program, found := git.ProgramWord(line)

		if found {
			assert.Check(t, split, "ProgramWord(%q) found %q, but SplitCommandLine cannot split the line", line, program)
			assert.Check(t, slices.Contains(words, program), "ProgramWord(%q) = %q, not one of SplitCommandLine's %q", line, program, words)
		}
	})
}

func TestIsName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "letters, digits and underscores", in: "GIT_DIR2", want: true},
		{name: "leading underscore", in: "_x", want: true},
		{name: "empty", in: "", want: false},
		{name: "leading digit", in: "1A", want: false},
		{name: "a character no name holds", in: "a-b", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := git.IsName(tt.in)

			assert.Check(t, is.Equal(got, tt.want), "IsName(%q)", tt.in)
		})
	}
}
