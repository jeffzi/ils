package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// renames is an ssh config that gives hosts other names: an alias of github.com and an alias of
// ssh.github.com.
const renames = "Host github-personal\n  HostName github.com\n" +
	"Host github-work\n  HostName ssh.github.com\n  Port 443\n"

func TestRepo_OriginURLs_WhenURLIsSSH_ReturnsTheHostSSHConnectsTo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		url  string
		host string
	}{
		{name: "scp-like url with an alias", url: "git@github-personal:jeffzi/ils.git", host: "github.com"},
		{name: "ssh url with an alias", url: "ssh://git@github-personal/jeffzi/ils.git", host: "github.com"},
		{name: "ssh url with an alias and a port", url: "ssh://git@github-personal:2222/jeffzi/ils.git", host: "github.com"},
		{name: "alias of a host under github.com", url: "git@github-work:jeffzi/ils.git", host: "github.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.SetOriginURL(tt.url)
			f.SetPushURL(tt.url)
			f.SetSSHConfig(renames)
			repo := newRepo(f)

			got, err := repo.OriginURLs(t.Context())
			assert.NilError(t, err, "OriginURLs() of %q", tt.url)

			named := git.Origin{Host: tt.host, Owner: "jeffzi", Name: "ils"}
			want := git.OriginURLs{Fetch: named, Push: named, FetchURL: tt.url, PushURL: tt.url}
			assert.Check(t, is.DeepEqual(got, want), "OriginURLs() of %q", tt.url)
		})
	}
}

// noSSHCommand returns repo without the fixture's GIT_SSH_COMMAND in its environment, so that it
// reads a host through the ssh command a test configures, or else through the ssh on PATH.
func noSSHCommand(repo git.Repo) git.Repo {
	repo.Runner.Env = gittest.Without(repo.Runner.Env, "GIT_SSH_COMMAND")

	return repo
}

func TestRepo_OriginURLs_WhenSSHFails_ReturnsExitErrorOfSSH(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(httpsURL)
	f.SetPushURL(sshURL)
	repo := plainSSH(t, newRepo(f), standInRefuse)

	_, err := repo.OriginURLs(t.Context())

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "OriginURLs() error = %v, want *child.ExitError", err)
	assert.Check(t, is.Equal(exitErr.Argv[0], "ssh"))
}

// runnerPath returns the PATH the environment of repo's runner gives, the last one when it gives
// several, matching the key as the system does.
func runnerPath(repo git.Repo) string {
	value := ""

	for _, entry := range repo.Runner.Env {
		key, v, ok := strings.Cut(entry, "=")
		if ok && (key == "PATH" || runtime.GOOS == "windows" && strings.EqualFold(key, "PATH")) {
			value = v
		}
	}

	return value
}

// withRunnerPath returns repo with dirs, in order, as the one PATH of its runner's environment.
func withRunnerPath(repo git.Repo, dirs ...string) git.Repo {
	repo.Runner.Env = append(gittest.Without(repo.Runner.Env, "PATH"), "PATH="+strings.Join(dirs, string(os.PathListSeparator)))

	return repo
}

// onRunnerPath links the test binary under name into a directory of its own put first on the PATH
// of repo's runner environment alone, and returns repo; the process PATH stays as it is.
func onRunnerPath(t *testing.T, repo git.Repo, name string) git.Repo {
	t.Helper()

	return firstOnRunnerPath(repo, standIn(t, name))
}

// firstOnRunnerPath returns repo with the directory of the program at path, in the slash form, put
// first on the PATH of its runner's environment.
func firstOnRunnerPath(repo git.Repo, path string) git.Repo {
	return withRunnerPath(repo, filepath.Dir(filepath.FromSlash(path)), runnerPath(repo))
}

// plainSSH returns repo with no ssh command, and the test binary first on the PATH of its runner's
// environment under ssh's name, acting as the plain ssh ils runs when no ssh command is configured,
// in the stand-in mode plainSSHEnv takes.
func plainSSH(t *testing.T, repo git.Repo, mode string) git.Repo {
	t.Helper()

	repo = onRunnerPath(t, noSSHCommand(repo), "ssh")
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

	dirs := []string{t.TempDir()}
	if runtime.GOOS == "windows" {
		shell, err := exec.LookPath("sh")
		assert.NilError(t, err, "the process PATH holds no sh, so git's shell is not found")
		dirs = append(dirs, filepath.Dir(shell))
		_, err = exec.LookPath(filepath.Join(filepath.Dir(shell), program))
		assert.Assert(t, err != nil, "the directory of git's shell holds %s, so the shell would find it", program)
	}

	return withRunnerPath(noSSHCommand(repo), dirs...)
}

func TestRepo_OriginURLs_WhenSSHMissingFromRunnerPath_ReturnsToolMissingError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// command, when not empty, names a stand-in ssh the user configured, which refuses.
		command string
		// env holds the settings added to the runner's environment.
		env []string
	}{
		{name: "no ssh command configured"},
		{name: "command of variant simple", command: "ssh", env: variantEnv("simple")},
		{name: "command of variant auto that refuses", command: "connect", env: variantEnv("auto")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.SetOriginURL(sshURL)
			repo := hideSSH(t, newRepo(f))
			repo.Runner.Env = append(repo.Runner.Env, tt.env...)
			if tt.command != "" {
				repo.Runner.Env = append(repo.Runner.Env, "GIT_SSH_COMMAND="+quoted(standIn(t, tt.command)), refusing)
			}

			_, err := repo.OriginURLs(t.Context())

			missing, ok := errors.AsType[*child.ToolMissingError](err)
			assert.Assert(t, ok, "OriginURLs() error = %v, want *child.ToolMissingError", err)
			assert.Check(t, is.Equal(missing.Tool, "ssh"))
			assert.Check(t, is.Equal(missing.InstallURL, "https://www.openssh.com"))
		})
	}
}

func TestRepo_OriginURLs_WhenContextEndsWhileGitSSHProgramRuns_ReturnsContextError(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(aliasURL)
	addr, started := gittest.SilentListener(t)
	repo := noSSHCommand(newRepo(f))
	repo.Runner.Env = append(append(repo.Runner.Env, gitSSHStandIn(t), sshStandInEnv+"="+standInLinger, helperAddrEnv+"="+addr),
		variantEnv("ssh")...)
	ctx := gittest.Ending(t.Context(), started, context.Canceled)

	_, err := repo.OriginURLs(ctx)

	assert.Check(t, is.ErrorIs(err, context.Canceled))
}

func TestRepo_OriginURLs_WhenNoSSHCommandSet_RunsPlainSSHInTheRunnerDirectory(t *testing.T) {
	t.Parallel()
	const nested = "nested.example"
	f := gittest.New(t, trunk)
	f.SetOriginURL(aliasURL)
	repo := plainSSH(t, newRepo(f), standInWorkDir)
	repo.Runner.Dir = filepath.Join(f.Dir(), nested)
	assert.NilError(t, os.Mkdir(repo.Runner.Dir, 0o700))

	got, err := repo.OriginURLs(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got.Fetch.Host, nested))
}

func TestRepo_OriginURLs_WhenSSHPrintsNoHostname_ReturnsErrorNamingNoHostname(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(sshURL)
	repo := plainSSH(t, newRepo(f), standInNoHostname)

	_, err := repo.OriginURLs(t.Context())

	assert.Check(t, is.ErrorContains(err, "printed no hostname"))
	_, exited := errors.AsType[*child.ExitError](err)
	assert.Check(t, !exited, "OriginURLs() error = %v, want an error from ssh's output, not its exit", err)
	_, missing := errors.AsType[*child.ToolMissingError](err)
	assert.Check(t, !missing, "OriginURLs() error = %v, want an error from ssh's output, not a missing ssh", err)
}

func TestRepo_OriginURLs_WhenConfiguredSSHPrintsNoHostname_ReturnsErrorNamingIt(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(aliasURL)
	configured := quoted(standIn(t, "ssh"))
	repo := noSSHCommand(newRepo(f))
	repo.Runner.Env = append(repo.Runner.Env, "GIT_SSH_COMMAND="+configured, sshStandInEnv+"="+standInNoHostname)

	_, err := repo.OriginURLs(t.Context())

	assert.Check(t, is.ErrorContains(err, configured))
	assert.Check(t, is.ErrorContains(err, "printed no hostname"))
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

// sshConfigFile writes content to an ssh config file of its own and returns its path.
func sshConfigFile(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	gittest.WriteFile(t, dir, "config", content)

	return filepath.Join(dir, "config")
}

// sshReading returns a command line that runs ssh with a config file of its own holding content as
// its one config file.
func sshReading(t *testing.T, content string) string {
	t.Helper()

	return "ssh -F " + quoted(sshConfigFile(t, content))
}

// quoted returns path in the slash form between single quotes, one word to a shell.
func quoted(path string) string { return "'" + filepath.ToSlash(path) + "'" }

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
	url     string
	// host is the host OriginURLs reports for url.
	host string
}

// sourceCases are the places a user names the ssh command git connects with.
func sourceCases() []sourceCase {
	const decoys = "Host work\n  HostName decoy.example.org\n"

	return []sourceCase{
		{
			name: "core.sshCommand", url: aliasURL, host: "git.example.org",
			arrange: func(t *testing.T, f *gittest.Fixture) []string {
				t.Helper()
				f.Git("config", "core.sshCommand", sshReading(t, aliases))

				return nil
			},
		},
		{
			name: "GIT_SSH program whose path holds a space", url: aliasURL, host: "work.example.org",
			arrange: func(t *testing.T, _ *gittest.Fixture) []string {
				t.Helper()

				return []string{gitSSHStandIn(t), sshStandInEnv + "=" + standInSuffix + ".example.org"}
			},
		},
		{
			name: "GIT_SSH_COMMAND over core.sshCommand", url: aliasURL, host: "git.example.org",
			arrange: func(t *testing.T, f *gittest.Fixture) []string {
				t.Helper()
				f.Git("config", "core.sshCommand", sshReading(t, decoys))

				return []string{"GIT_SSH_COMMAND=" + sshReading(t, aliases)}
			},
		},
		{
			name: "core.sshCommand over GIT_SSH", url: aliasURL, host: "git.example.org",
			arrange: func(t *testing.T, f *gittest.Fixture) []string {
				t.Helper()
				f.Git("config", "core.sshCommand", sshReading(t, aliases))

				return []string{gitSSHStandIn(t), answering(standInHost)}
			},
		},
	}
}

// foldCases are the places a user names the ssh command, each answering a host under github.com,
// which OriginURLs reports as github.com.
func foldCases() []sourceCase {
	return []sourceCase{
		{
			name: "GIT_SSH program answering a host under github.com", url: aliasURL, host: "github.com",
			arrange: func(t *testing.T, _ *gittest.Fixture) []string {
				t.Helper()

				return []string{gitSSHStandIn(t), answering("ssh.github.com")}
			},
		},
	}
}

func TestRepo_OriginURLs_WhenSSHCommandSet_ReturnsTheHostThatCommandConnectsTo(t *testing.T) {
	t.Parallel()
	for _, tt := range append(sourceCases(), foldCases()...) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.SetOriginURL(tt.url)
			repo := noSSHCommand(newRepo(f))
			repo.Runner.Env = append(repo.Runner.Env, tt.arrange(t, f)...)

			got, err := repo.OriginURLs(t.Context())
			assert.NilError(t, err, "OriginURLs() of %q", tt.url)

			assert.Check(t, is.Equal(got.Fetch.Host, tt.host), "OriginURLs() of %q", tt.url)
		})
	}
}

func TestRepo_OriginURLs_WhenRunnerSetsNoEnvironment_ReadsTheProcessEnvironment(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(aliasURL)
	env := append(f.Env(), originHostEnv+"="+f.Dir(), "GIT_SSH_COMMAND="+sshReading(t, aliases))

	stdout, stderr, err := runSelf(t, env)
	assert.NilError(t, err, "test binary stderr: %s", stderr)

	assert.Check(t, is.Equal(stdout, "git.example.org"))
}

func TestRepo_OriginURLs_WhenRunnerNamesGitSSHCommandInLowerCase_MatchesTheKeyAsTheSystemDoes(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(aliasURL)
	repo := newRepo(f)
	repo.Runner.Env = append(repo.Runner.Env, "git_ssh_command="+sshReading(t, aliases))
	// Windows matches environment keys without regard to case; other systems keep the fixture's own
	// GIT_SSH_COMMAND, whose empty config leaves the alias as it is.
	want := "work"
	if runtime.GOOS == "windows" {
		want = "git.example.org"
	}

	got, err := repo.OriginURLs(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got.Fetch.Host, want))
}

func TestRepo_OriginURLs_WhenPlainSSHAnswersAHostUnderGitHub_ReportsGitHub(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(aliasURL)
	repo := plainSSH(t, newRepo(f), standInAnswer+"ssh.github.com")

	got, err := repo.OriginURLs(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got.Fetch.Host, "github.com"))
}

func TestRepo_OriginURLs_WhenSSHCommandNeedsTheShell_RunsItAsGitDoes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arrange func(t *testing.T, f *gittest.Fixture, repo *git.Repo)
		name    string
	}{
		{
			name: "variable",
			arrange: func(t *testing.T, _ *gittest.Fixture, repo *git.Repo) {
				t.Helper()
				repo.Runner.Env = append(repo.Runner.Env,
					"ILS_TEST_SSH_CONFIG="+sshConfigFile(t, aliases), `GIT_SSH_COMMAND=ssh -F "$ILS_TEST_SSH_CONFIG"`)
			},
		},
		{
			name: "path relative to the top of the working tree, asked from a subdirectory",
			arrange: func(t *testing.T, f *gittest.Fixture, repo *git.Repo) {
				t.Helper()
				gittest.WriteFile(t, f.Dir(), "ssh-config", aliases)
				repo.Runner.Dir = filepath.Join(f.Dir(), "nested")
				assert.NilError(t, os.Mkdir(repo.Runner.Dir, 0o700))
				f.Git("config", "core.sshCommand", "ssh -F ssh-config")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.SetOriginURL(aliasURL)
			repo := noSSHCommand(newRepo(f))
			tt.arrange(t, f, &repo)

			got, err := repo.OriginURLs(t.Context())
			assert.NilError(t, err)

			assert.Check(t, is.Equal(got.Fetch.Host, "git.example.org"))
		})
	}
}

// variantCase is one ssh command a user configured, run as its ssh variant says.
type variantCase struct {
	// line, when set, writes the command line that names the stand-in at a path; quoted does
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

// escapedSpaces returns path with a backslash before each space, one word to a shell.
func escapedSpaces(path string) string { return strings.ReplaceAll(path, " ", `\ `) }

// unclosed returns path after a single quote that nothing closes, a line git cannot split.
func unclosed(path string) string { return "'" + path }

// variantEnv returns the setting that gives git's ssh variant as name.
func variantEnv(name string) []string { return []string{"GIT_SSH_VARIANT=" + name} }

// variantCases are the ssh commands a user configures, by name and variant, and the host each
// reads.
func variantCases() []variantCase {
	return []variantCase{
		{name: "command named ssh", command: "ssh", want: standInHost},
		{name: "command named SSH.EXE", command: "SSH.EXE", want: standInHost},
		{name: "command named plink", command: "plink", want: plainHost},
		{name: "command named plink.exe", command: "plink.exe", want: plainHost},
		{name: "command named PLINK.EXE", command: "PLINK.EXE", want: plainHost},
		{name: "command named plink, spaces escaped", command: "plink", line: escapedSpaces, want: plainHost},
		{name: "command line with an unclosed quote", command: "ssh", line: unclosed, want: plainHost},
		{name: "command named TortoisePlink", command: "TortoisePlink", want: plainHost},
		{name: "command named tortoiseplink.exe", command: "tortoiseplink.exe", want: plainHost},
		{name: "unrecognized command that answers", command: "connect", want: standInHost},
		{name: "unrecognized command that refuses", command: "connect", refuses: true, want: plainHost},
		{name: "GIT_SSH program named plink", command: "plink", program: true, want: plainHost},
		{name: "GIT_SSH program unrecognized that refuses", command: "connect", program: true, refuses: true, want: plainHost},
		{name: "GIT_SSH_VARIANT plink", command: "ssh", env: variantEnv("plink"), want: plainHost},
		{name: "GIT_SSH_VARIANT putty", command: "ssh", env: variantEnv("putty"), want: plainHost},
		{name: "GIT_SSH_VARIANT tortoiseplink", command: "ssh", env: variantEnv("tortoiseplink"), want: plainHost},
		{name: "GIT_SSH_VARIANT simple", command: "ssh", env: variantEnv("simple"), want: plainHost},
		{name: "GIT_SSH_VARIANT ssh", command: "plink", env: variantEnv("ssh"), want: standInHost},
		{name: "GIT_SSH_VARIANT auto", command: "connect", env: variantEnv("auto"), want: standInHost},
		{name: "GIT_SSH_VARIANT auto that refuses", command: "connect", env: variantEnv("auto"), refuses: true, want: plainHost},
		{name: "GIT_SSH_VARIANT auto with a command named plink", command: "plink", env: variantEnv("auto"), want: plainHost},
		{name: "ssh.variant auto with a command named plink", command: "plink", variant: "auto", want: plainHost},
		{name: "GIT_SSH_VARIANT unknown", command: "plink", env: variantEnv("openssh"), want: standInHost},
		{name: "ssh.variant simple", command: "ssh", variant: "simple", want: plainHost},
		{name: "ssh.variant putty", command: "ssh", variant: "putty", want: plainHost},
		{name: "ssh.variant ssh", command: "plink", variant: "ssh", want: standInHost},
		{name: "GIT_SSH_VARIANT ssh over ssh.variant", command: "plink", env: variantEnv("ssh"), variant: "simple", want: standInHost},
		{name: "GIT_SSH_VARIANT simple over ssh.variant", command: "ssh", env: variantEnv("simple"), variant: "ssh", want: plainHost},
	}
}

func TestRepo_OriginURLs_WhenSSHCommandConfigured_AsksItOrPlainSSHAsItsVariantSays(t *testing.T) {
	t.Parallel()
	for _, tt := range variantCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.SetOriginURL(aliasURL)
			if tt.variant != "" {
				f.Git("config", "ssh.variant", tt.variant)
			}
			path := standIn(t, tt.command)
			line := quoted
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
		command   string
		key       string
		env       []string
	}{
		{name: "command exits non-zero", command: "ssh", key: "GIT_SSH_COMMAND", configure: quoted},
		{name: "program exits non-zero", command: "ssh", key: "GIT_SSH", configure: verbatim},
		{name: "command named ssh.exe exits non-zero", command: "ssh.exe", key: "GIT_SSH_COMMAND", configure: quoted},
		{
			name: "unrecognized command of variant ssh exits non-zero", command: "connect", key: "GIT_SSH_COMMAND",
			configure: quoted, env: variantEnv("ssh"),
		},
		{
			name: "unrecognized command of an unknown variant exits non-zero", command: "connect",
			key: "GIT_SSH_COMMAND", configure: quoted, env: variantEnv("openssh"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.SetOriginURL(aliasURL)
			configured := tt.configure(standIn(t, tt.command))
			repo := noSSHCommand(newRepo(f))
			repo.Runner.Env = append(append(repo.Runner.Env, tt.key+"="+configured, refusing), tt.env...)

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
	// variant, when not empty, is the repository's ssh.variant.
	variant string
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
			name: "GIT_SSH_COMMAND path with an argument", setting: "GIT_SSH_COMMAND", value: "/no/such/ssh -v",
			program: "/no/such/ssh",
		},
		{
			name: "GIT_SSH_COMMAND unrecognized name alone", setting: "GIT_SSH_COMMAND", value: "no-such-wrapper",
			program: "no-such-wrapper",
		},
		{
			name: "GIT_SSH_COMMAND unrecognized name with an argument", setting: "GIT_SSH_COMMAND",
			value: "no-such-wrapper -v", program: "no-such-wrapper",
		},
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
			name: "GIT_SSH_COMMAND command before a name", setting: "GIT_SSH_COMMAND", value: "command no-such-wrapper",
			program: "no-such-wrapper",
		},
		{
			name: "GIT_SSH_COMMAND env and an assignment before a name", setting: "GIT_SSH_COMMAND",
			value: "env AUDIT=1 no-such-wrapper", program: "no-such-wrapper",
		},
		{name: "core.sshCommand path alone", setting: "core.sshCommand", value: "/no/such/ssh", program: "/no/such/ssh"},
		{
			name: "core.sshCommand unrecognized name with an argument", setting: "core.sshCommand",
			value: "no-such-wrapper -v", program: "no-such-wrapper",
		},
		{name: "GIT_SSH path", setting: "GIT_SSH", value: "/no/such/ssh", program: "/no/such/ssh"},
		{name: "GIT_SSH unrecognized name", setting: "GIT_SSH", value: "no-such-wrapper", program: "no-such-wrapper"},
		{name: "GIT_SSH name found only on the process PATH", setting: "GIT_SSH", value: "ssh", program: "ssh", hide: true},
		{name: "GIT_SSH path named plink", setting: "GIT_SSH", value: "/no/such/plink", program: "/no/such/plink"},
		{
			name: "GIT_SSH unrecognized name of GIT_SSH_VARIANT simple", setting: "GIT_SSH", value: "no-such-wrapper",
			program: "no-such-wrapper", env: variantEnv("simple"),
		},
		{
			name: "GIT_SSH path of ssh.variant putty", setting: "GIT_SSH", value: "/no/such/ssh", program: "/no/such/ssh",
			variant: "putty",
		},
		{
			name: "GIT_SSH name of GIT_SSH_VARIANT simple found only on the process PATH", setting: "GIT_SSH",
			value: "ssh", program: "ssh", env: variantEnv("simple"), hide: true,
		},
	}
}

// missingRepo returns the repository of a new fixture whose origin is aliasURL, a plain ssh
// answering plainHost, and the ssh command of tt.
func missingRepo(t *testing.T, tt missingCase) git.Repo {
	t.Helper()

	f := gittest.New(t, trunk)
	f.SetOriginURL(aliasURL)
	if tt.variant != "" {
		f.Git("config", "ssh.variant", tt.variant)
	}
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

// uncheckedCases are command lines of variant plain whose first word is not found. ils never runs
// such a line, and its first word may be a shell builtin or an assignment, so it does not look the
// word up.
func uncheckedCases() []missingCase {
	return []missingCase{
		{name: "GIT_SSH_COMMAND path named plink", setting: "GIT_SSH_COMMAND", value: "/no/such/plink"},
		{
			name: "GIT_SSH_COMMAND assignment before a name, of GIT_SSH_VARIANT putty", setting: "GIT_SSH_COMMAND",
			value: "AUDIT=1 no-such-wrapper -v", env: variantEnv("putty"),
		},
		{
			name: "core.sshCommand path of ssh.variant simple", setting: "core.sshCommand", value: "/no/such/ssh",
			variant: "simple",
		},
	}
}

func TestRepo_OriginURLs_WhenCommandLineOfVariantPlainNotFound_AsksPlainSSH(t *testing.T) {
	t.Parallel()
	for _, tt := range uncheckedCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := missingRepo(t, tt)

			got, err := repo.OriginURLs(t.Context())
			assert.NilError(t, err)

			assert.Check(t, is.Equal(got.Fetch.Host, plainHost))
		})
	}
}

func TestSSHCommandMissingError_Error_WhenProgramNotFound_NamesSettingAndProgram(t *testing.T) {
	t.Parallel()
	err := &git.SSHCommandMissingError{Setting: "core.sshCommand", Program: "no-such-wrapper"}

	got := err.Error()

	assert.Check(t, is.Equal(got, "no-such-wrapper, the ssh command core.sshCommand names, is not found"))
}

// exitingCase is one ssh command a user configured whose program is found but itself exits with
// the code git gives a program it cannot find.
type exitingCase struct {
	// line writes the command line, given the path of a stand-in named connect that is also first on
	// the PATH of the runner's environment.
	line func(path string) string
	name string
	code int
}

// exitingCases are the ssh commands a user configures whose found program exits 127 or 128.
func exitingCases() []exitingCase {
	byName := func(string) string { return "connect" }

	return []exitingCase{
		{name: "path exits 127", line: quoted, code: 127},
		{name: "path exits 128", line: quoted, code: 128},
		{name: "name on the runner's PATH exits 127", line: byName, code: 127},
		{name: "name on the runner's PATH exits 128", line: byName, code: 128},
		{name: "name on the runner's PATH with an argument exits 127", line: func(string) string { return "connect -v" }, code: 127},
		{name: "exec before a path exits 127", line: func(path string) string { return "exec " + quoted(path) }, code: 127},
		{name: "assignment before a path exits 128", line: func(path string) string { return "AUDIT=1 " + quoted(path) }, code: 128},
		{name: "command before a name exits 128", line: func(string) string { return "command connect" }, code: 128},
		{name: "env and an assignment before a name exit 127", line: func(string) string { return "env AUDIT=1 connect" }, code: 127},
		{name: "env and an option before a name exit 127", line: func(string) string { return "env -u AUDIT connect" }, code: 127},
		{name: "command and an option before a path exit 128", line: func(path string) string { return "command -p " + quoted(path) }, code: 128},
	}
}

// exitingRepo returns the repository of a new fixture whose origin is aliasURL, a plain ssh
// answering plainHost, and the command line of tt, of variant, in GIT_SSH_COMMAND.
func exitingRepo(t *testing.T, tt exitingCase, variant string) (git.Repo, string) {
	t.Helper()

	f := gittest.New(t, trunk)
	f.SetOriginURL(aliasURL)
	path := standIn(t, "connect")
	repo := firstOnRunnerPath(plainSSH(t, newRepo(f), plainAnswer), path)
	line := tt.line(path)
	repo.Runner.Env = append(append(repo.Runner.Env, "GIT_SSH_COMMAND="+line, exiting(tt.code)), variantEnv(variant)...)

	return repo, line
}

func TestRepo_OriginURLs_WhenFoundSSHCommandExitsAsIfMissing_ReturnsExitErrorNamingIt(t *testing.T) {
	t.Parallel()
	for _, tt := range exitingCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo, line := exitingRepo(t, tt, "ssh")

			_, err := repo.OriginURLs(t.Context())

			exitErr, ok := errors.AsType[*child.ExitError](err)
			assert.Assert(t, ok, "OriginURLs() error = %v, want *child.ExitError", err)
			assert.Check(t, is.Equal(exitErr.Argv[0], line))
			assert.Check(t, is.Contains(exitErr.Stderr, standInRefusal))
		})
	}
}

func TestRepo_OriginURLs_WhenFoundSSHCommandOfVariantAutoExitsAsIfMissing_AsksPlainSSH(t *testing.T) {
	t.Parallel()
	for _, tt := range exitingCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo, _ := exitingRepo(t, tt, "auto")

			got, err := repo.OriginURLs(t.Context())
			assert.NilError(t, err)

			assert.Check(t, is.Equal(got.Fetch.Host, plainHost))
		})
	}
}

func TestRepo_OriginURLs_WhenCommandLineNamesNoProgramAndExitsAsIfMissing_ReturnsExitErrorNamingIt(t *testing.T) {
	// The shell runs the -G that ils appends as the program, and finds no such command.
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(aliasURL)
	repo := plainSSH(t, newRepo(f), plainAnswer)
	repo.Runner.Env = append(append(repo.Runner.Env, "GIT_SSH_COMMAND=AUDIT=1"), variantEnv("ssh")...)

	_, err := repo.OriginURLs(t.Context())

	exitErr, ok := errors.AsType[*child.ExitError](err)
	assert.Assert(t, ok, "OriginURLs() error = %v, want *child.ExitError", err)
	assert.Check(t, is.Equal(exitErr.Argv[0], "AUDIT=1"))
}

func TestRepo_OriginURLs_WhenGitSSHProgramIsOnlyOnTheRunnerPath_AsksIt(t *testing.T) {
	t.Parallel()
	f := gittest.New(t, trunk)
	f.SetOriginURL(aliasURL)
	repo := onRunnerPath(t, plainSSH(t, newRepo(f), plainAnswer), "connect")
	repo.Runner.Env = append(repo.Runner.Env, gitSSHByBareName()...)

	got, err := repo.OriginURLs(t.Context())
	assert.NilError(t, err)

	assert.Check(t, is.Equal(got.Fetch.Host, standInHost))
}

// gitSSHByBareName returns the settings that name the stand-in connect, found on a PATH, as the
// program GIT_SSH runs, of variant ssh, answering standInHost.
func gitSSHByBareName() []string {
	return append([]string{"GIT_SSH=connect", answering(standInHost)}, variantEnv("ssh")...)
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
	// Not parallel: each row sets the environment of the whole process, whose PATH alone holds the
	// stand-ins.
	tests := []struct {
		name string
		want string
		// env holds the settings added to the process environment.
		env []string
	}{
		{name: "no ssh command configured", want: plainHost},
		{name: "GIT_SSH program named by bare name", want: standInHost, env: gitSSHByBareName()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := gittest.New(t, trunk)
			f.SetOriginURL(aliasURL)
			repo := onRunnerPath(t, plainSSH(t, newRepo(f), plainAnswer), "connect")
			useProcessEnv(t, append(repo.Runner.Env, tt.env...))
			repo.Runner.Env = nil

			got, err := repo.OriginURLs(t.Context())
			assert.NilError(t, err)

			assert.Check(t, is.Equal(got.Fetch.Host, tt.want))
		})
	}
}

// callsOf returns the attributes of each exec record in records that names tool, all but the
// duration.
func callsOf(records []logtest.Record, tool string) []map[string]any {
	var calls []map[string]any

	for _, record := range records {
		if record.Message == child.ExecMessage && record.Map()[child.KeyTool] == tool {
			calls = append(calls, withoutDuration(record))
		}
	}

	return calls
}

func TestRepo_OriginURLs_WhenLoggerAttachedAndSSHReadsHost_LogsEachSSHRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		// arrange returns repo set to read the host through ssh, and the tool that ssh run is
		// logged under.
		arrange func(t *testing.T, repo git.Repo) (git.Repo, string)
		name    string
		args    []string
	}{
		{
			name: "plain ssh", args: []string{"-G", "--", "work"},
			arrange: func(t *testing.T, repo git.Repo) (git.Repo, string) {
				t.Helper()

				return plainSSH(t, repo, plainAnswer), "ssh"
			},
		},
		{
			name: "configured ssh program", args: []string{"-G", "work"},
			arrange: func(t *testing.T, repo git.Repo) (git.Repo, string) {
				t.Helper()
				configured := standIn(t, "ssh")
				repo = noSSHCommand(repo)
				repo.Runner.Env = append(repo.Runner.Env, "GIT_SSH="+configured, answering(standInHost))

				return repo, configured
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := gittest.New(t, trunk)
			f.SetOriginURL(aliasURL)
			repo, tool := tt.arrange(t, newRepo(f))
			logger, sink := logtest.New()
			repo.Runner.Logger = logger

			_, err := repo.OriginURLs(t.Context())
			assert.NilError(t, err)

			calls := callsOf(sink.Records(), tool)
			assert.Assert(t, len(calls) > 0, "OriginURLs() logged no exec record of %q", tool)
			want := execAttrs(t, child.Exec{Tool: tool, Args: tt.args})
			for _, call := range calls {
				assert.Check(t, is.DeepEqual(call, want))
			}
		})
	}
}
