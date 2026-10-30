package gittest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jeffzi/ils/internal/git"
)

const (
	// AuthorName is the author and committer name of every commit the fixture makes.
	AuthorName = "Fixture Author"
	// AuthorEmail is the author and committer email of every commit the fixture makes.
	AuthorEmail = "fixture@example.com"

	commitDate    = "2001-02-03T04:05:06Z"
	origin        = "origin"
	dirtyPath     = "README.md"
	stagedPath    = "LICENSE"
	trunkOnlyPath = "trunk-only.txt"
	changelogPath = "CHANGELOG.md"
	scratchRef    = "refs/gittest/squash"

	sshConfigName = "ssh_config"
	sshCommandVar = "GIT_SSH_COMMAND"
	standInDir    = "ssh-stand-in"
	// servingEnv holds the script the stand-in for ssh runs for every call but a read of its config.
	servingEnv = "ILS_GITTEST_SERVING"
)

// ---------------------------------------------------------------------------
// Construction and environment
// ---------------------------------------------------------------------------

// Fixture is a working repository with a bare origin, both inside the test's temporary directory.
type Fixture struct {
	tb        testing.TB
	git       string
	root      string
	dir       string
	originDir string
	peerDir   string
	trunk     string
	env       []string
	serial    int
}

// New builds a working repository whose trunk has one commit pushed to a bare origin whose HEAD names
// trunk, and an empty ssh config of its own that the ssh command of its environment reads alone.
func New(tb testing.TB, trunk string) *Fixture {
	tb.Helper()

	gitPath, err := exec.LookPath("git")
	if err != nil {
		tb.Fatalf("gittest: find git: %v", err)
	}

	// Resolved so that Dir matches what git prints for the repository, which follows symbolic links.
	root, err := filepath.EvalSymlinks(tb.TempDir())
	if err != nil {
		tb.Fatalf("gittest: resolve temporary directory: %v", err)
	}

	home := filepath.Join(root, "home")
	// The fixture's ssh reads this file alone, through the -F in GIT_SSH_COMMAND below, so no ssh
	// reads the user's or the system's config.
	sshConfig := filepath.Join(root, sshConfigName)
	f := &Fixture{
		tb:        tb,
		env:       append(isolatedEnv(home), sshCommandVar+"=ssh -F '"+filepath.ToSlash(sshConfig)+"'"),
		git:       gitPath,
		root:      root,
		dir:       filepath.Join(root, "repo"),
		originDir: filepath.Join(root, "origin.git"),
		trunk:     trunk,
	}

	err = os.MkdirAll(home, 0o750)
	if err != nil {
		tb.Fatalf("gittest: create home directory: %v", err)
	}

	f.SetSSHConfig("")

	f.run(root, "init", "--quiet", "--bare", "--initial-branch="+trunk, f.originDir)
	f.run(root, "init", "--quiet", "--initial-branch="+trunk, f.dir)
	f.Git("config", "--local", "user.name", AuthorName)
	f.Git("config", "--local", "user.email", AuthorEmail)
	f.Git("config", "--local", "commit.gpgsign", "false")
	f.Git("remote", "add", origin, f.originURL())
	WriteFile(f.tb, f.dir, dirtyPath, "fixture\n")
	WriteFile(f.tb, f.dir, stagedPath, "fixture\n")
	f.Git("add", "--", dirtyPath, stagedPath)
	f.Git("commit", "--quiet", "--message", "initial commit")
	f.Push(trunk)

	return f
}

// NewServed builds a fixture standing on branch with one commit past trunk, whose origin URL is
// url, and returns it with the environment that serves ssh URLs of origin from the bare origin.
func NewServed(tb testing.TB, trunk, branch, url string) (*Fixture, []string) {
	tb.Helper()

	f := New(tb, trunk)
	f.Commits(branch, "work on "+branch)
	f.SetOriginURL(url)

	return f, f.ServedEnv(f.OriginDir())
}

// Env returns an environment cut off from the user's git setup, with a home in a temporary
// directory of its own, for a git runner that needs no fixture.
func Env(tb testing.TB) []string {
	tb.Helper()

	return isolatedEnv(tb.TempDir())
}

// NeedSignals skips the test on Windows, where a shell cannot end a git process by a signal.
func NeedSignals(tb testing.TB) {
	tb.Helper()

	if runtime.GOOS == "windows" {
		tb.Skip("Windows has no signals for a shell to end a git process with")
	}
}

// isolatedEnv returns the process environment with everything that could bring in the user's git
// setup replaced: no system or global config, a home under the fixture, and a fixed identity and date.
func isolatedEnv(home string) []string {
	// XDG_CONFIG_HOME goes too: git reads its default ignore file from there.
	userDirKeys := []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"}

	env := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")

		const gitPrefix = "GIT_"

		head := name[:min(len(name), len(gitPrefix))]
		gitSetup := git.SameKey(head, gitPrefix)
		userDir := slices.ContainsFunc(userDirKeys, func(key string) bool {
			return git.SameKey(name, key)
		})

		return gitSetup || userDir
	})

	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"HOME="+home,
		"USERPROFILE="+home,
		"GIT_AUTHOR_NAME="+AuthorName,
		"GIT_AUTHOR_EMAIL="+AuthorEmail,
		"GIT_AUTHOR_DATE="+commitDate,
		"GIT_COMMITTER_NAME="+AuthorName,
		"GIT_COMMITTER_EMAIL="+AuthorEmail,
		"GIT_COMMITTER_DATE="+commitDate,
	)
}

// Without returns a copy of env with every entry of key removed, matching the key as the system
// does: exactly, or regardless of case on Windows.
func Without(env []string, key string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")

		return git.SameKey(name, key)
	})
}

// FirstOnPath returns a copy of env whose one PATH is the PATH env gives, the last one when it
// gives several, with dir put first.
func FirstOnPath(env []string, dir string) []string {
	path, _ := git.EnvValue(env, "PATH")

	return append(Without(env, "PATH"), "PATH="+firstOnPath(dir, path))
}

// WithoutSSH returns a copy of env, the environment of a fixture, with no ssh command and an empty
// directory as its one PATH, so that git run in it finds no ssh to run.
func WithoutSSH(tb testing.TB, env []string) []string {
	tb.Helper()

	return append(Without(Without(env, sshCommandVar), "PATH"), "PATH="+tb.TempDir())
}

// ---------------------------------------------------------------------------
// Files and scripts
// ---------------------------------------------------------------------------

// Marking returns a command line that creates the file at marker when a shell runs it, whatever
// arguments follow it.
func Marking(marker string) string { return "echo ran > '" + filepath.ToSlash(marker) + "' #" }

// WriteFile writes content to the file name in dir, and fails the test when it cannot.
func WriteFile(tb testing.TB, dir, name, content string) {
	tb.Helper()

	err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
	if err != nil {
		tb.Fatalf("gittest: write %s: %v", name, err)
	}
}

// writeScript writes body as the sh script name in dir, executable by its owner, and fails the
// test when it cannot.
func writeScript(tb testing.TB, dir, name, body string) {
	tb.Helper()

	//nolint:gosec // G306: git or a shell must be able to execute the script.
	err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o700)
	if err != nil {
		tb.Fatalf("gittest: write script %s: %v", name, err)
	}
}

// ---------------------------------------------------------------------------
// Accessors and git runners
// ---------------------------------------------------------------------------

// Dir returns the working repository's directory.
func (f *Fixture) Dir() string { return f.dir }

// OriginDir returns the bare origin's directory.
func (f *Fixture) OriginDir() string { return f.originDir }

// MissingOriginDir returns a location beside the bare origin that holds no repository, for an
// origin that does not answer.
func (f *Fixture) MissingOriginDir() string { return f.originDir + "-missing" }

// Env returns the environment every fixture git call runs with, for a caller's own git runner. Its
// GIT_SSH_COMMAND runs ssh with the fixture's ssh config as its one config file.
func (f *Fixture) Env() []string { return slices.Clone(f.env) }

// Git runs git with args in the working repository and returns its trimmed stdout; a non-zero exit
// fails the test with git's stderr.
func (f *Fixture) Git(args ...string) string {
	f.tb.Helper()

	return f.run(f.dir, args...)
}

// OriginGit runs git with args in the bare origin and returns its trimmed stdout; a non-zero exit
// fails the test with git's stderr.
func (f *Fixture) OriginGit(args ...string) string {
	f.tb.Helper()

	return f.run(f.originDir, args...)
}

// originURL returns the bare origin's location in the slash form git accepts on every platform.
func (f *Fixture) originURL() string { return filepath.ToSlash(f.originDir) }

func (f *Fixture) trunkRef() string { return "refs/heads/" + f.trunk }

func (f *Fixture) run(dir string, args ...string) string {
	f.tb.Helper()

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(f.tb.Context(), f.git, args...) //nolint:gosec // argv comes from test code, never from input
	cmd.Dir = dir
	cmd.Env = f.env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		f.tb.Fatalf("gittest: git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, strings.TrimSpace(stderr.String()))
	}

	return strings.TrimSpace(stdout.String())
}

// ---------------------------------------------------------------------------
// Repository and origin scenarios
// ---------------------------------------------------------------------------

// Commits checks out branch, cutting it from the trunk when it does not exist yet, adds one commit
// per subject, and returns the new shas, oldest first.
func (f *Fixture) Commits(branch string, subjects ...string) []string {
	f.tb.Helper()
	f.checkout(branch)

	shas := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		shas = append(shas, f.commit(f.dir, subject))
	}

	return shas
}

// CommitChangelog checks out branch, cutting it from the trunk when it does not exist yet, commits
// content as CHANGELOG.md with subject, and returns the new sha.
func (f *Fixture) CommitChangelog(branch, subject, content string) string {
	f.tb.Helper()
	f.checkout(branch)

	return f.commitFile(f.dir, changelogPath, content, subject)
}

// checkout checks out branch, cutting it from the trunk when it does not exist yet.
func (f *Fixture) checkout(branch string) {
	f.tb.Helper()

	if f.Git("branch", "--list", branch) == "" {
		f.Git("switch", "--quiet", "--create", branch, f.trunk)
	} else {
		f.Git("switch", "--quiet", branch)
	}
}

// Push puts origin's copy of branch at the local tip.
func (f *Fixture) Push(branch string) {
	f.tb.Helper()
	f.Git("push", "--quiet", origin, branch)
}

// RemoveFromOrigin deletes branch from origin, leaving the local branch as it is.
func (f *Fixture) RemoveFromOrigin(branch string) {
	f.tb.Helper()
	f.Git("push", "--quiet", origin, "--delete", branch)
}

// AdvanceOrigin adds a commit to origin's copy of branch that the working repository has not
// fetched, and returns its sha. The branch must already be on origin.
func (f *Fixture) AdvanceOrigin(branch string) string {
	f.tb.Helper()

	if f.peerDir == "" {
		f.peerDir = filepath.Join(f.root, "peer")
		f.run(f.root, "clone", "--quiet", f.originURL(), f.peerDir)
	}

	f.run(f.peerDir, "fetch", "--quiet", origin)
	f.run(f.peerDir, "switch", "--quiet", "--force-create", branch, origin+"/"+branch)

	sha := f.commit(f.peerDir, "origin change on "+branch)
	f.run(f.peerDir, "push", "--quiet", origin, branch)

	return sha
}

// Diverge gives the local branch and origin's copy one commit each that the other lacks, and
// returns both shas. The branch must already be on origin.
func (f *Fixture) Diverge(branch string) (local, remote string) {
	f.tb.Helper()

	remote = f.AdvanceOrigin(branch)

	f.Git("switch", "--quiet", branch)
	local = f.commit(f.dir, "local change on "+branch)

	return local, remote
}

// Detach detaches HEAD at the commit it is on.
func (f *Fixture) Detach() {
	f.tb.Helper()
	f.Git("switch", "--quiet", "--detach")
}

// DetachOriginHead turns origin's HEAD into a plain ref that names no branch.
func (f *Fixture) DetachOriginHead() {
	f.tb.Helper()
	f.OriginGit("update-ref", "--no-deref", "HEAD", f.OriginGit("rev-parse", "HEAD"))
}

// CorruptIndex overwrites the working repository's index with bytes git cannot parse.
func (f *Fixture) CorruptIndex() {
	f.tb.Helper()
	WriteFile(f.tb, filepath.Join(f.dir, ".git"), "index", strings.Repeat("not what git wrote here\n", 4))
}

// Hook installs script as the working repository's hook name, run by sh.
func (f *Fixture) Hook(name, script string) {
	f.tb.Helper()
	writeScript(f.tb, filepath.Join(f.dir, ".git", "hooks"), name, script+"\n")
}

// OriginHook installs script as the bare origin's hook name, run by sh.
func (f *Fixture) OriginHook(name, script string) {
	f.tb.Helper()
	writeScript(f.tb, filepath.Join(f.originDir, "hooks"), name, script+"\n")
}

// DirtyFile leaves an unstaged change in a tracked file and returns its path relative to Dir.
func (f *Fixture) DirtyFile() string {
	f.tb.Helper()
	WriteFile(f.tb, f.dir, dirtyPath, "dirty\n")

	return dirtyPath
}

// StagedFile stages a change to a tracked file and returns its path relative to Dir.
func (f *Fixture) StagedFile() string {
	f.tb.Helper()
	WriteFile(f.tb, f.dir, stagedPath, "staged\n")
	f.Git("add", "--", stagedPath)

	return stagedPath
}

// Sibling checks out a new branch cut from the first commit of the branch named by of past the
// trunk, adds one commit to it, and returns the new tip.
func (f *Fixture) Sibling(name, of string) string {
	f.tb.Helper()

	first, _, _ := strings.Cut(f.Git("rev-list", "--reverse", f.trunk+".."+of), "\n")
	if first == "" {
		f.tb.Fatalf("gittest: branch %s has no commit past %s to share", of, f.trunk)
	}

	f.Git("switch", "--quiet", "--create", name, first)

	return f.commit(f.dir, "change on "+name)
}

// SetPushURL sets origin's push URL, leaving its fetch URL as it is.
func (f *Fixture) SetPushURL(url string) {
	f.tb.Helper()
	f.Git("remote", "set-url", "--push", origin, url)
}

// SetOriginURL sets origin's URL to url, which need not name a reachable repository.
func (f *Fixture) SetOriginURL(url string) {
	f.tb.Helper()
	f.Git("remote", "set-url", origin, url)
}

// RemoveOrigin removes the remote named origin from the working repository.
func (f *Fixture) RemoveOrigin() {
	f.tb.Helper()
	f.Git("remote", "remove", origin)
}

// PullRef points refs/pull/<number>/head on the bare origin at the local commit sha, the ref
// GitHub keeps for a pull request.
func (f *Fixture) PullRef(number int, sha string) {
	f.tb.Helper()
	f.Git("push", "--quiet", f.originURL(), sha+":refs/pull/"+strconv.Itoa(number)+"/head")
}

// SquashOnOrigin adds a commit to origin's trunk whose tree is that of the local commit sha, as a
// squash merge of sha would, and returns the new sha. Its message is subject, followed by a blank
// line and body when body is not empty. The working repository does not fetch it.
func (f *Fixture) SquashOnOrigin(sha, subject, body string) string {
	f.tb.Helper()
	f.carryToOrigin(sha)

	trunkRef := f.trunkRef()
	parent := f.OriginGit("rev-parse", "--verify", trunkRef)
	args := []string{"commit-tree", sha + "^{tree}", "-p", parent, "-m", subject}
	if body != "" {
		args = append(args, "-m", body)
	}

	squash := f.OriginGit(args...)
	f.OriginGit("update-ref", trunkRef, squash, parent)
	f.dropCarried()

	return squash
}

// carryToOrigin pushes the local commit sha to a scratch ref on origin, which carries its objects
// there without creating a branch.
func (f *Fixture) carryToOrigin(sha string) {
	f.tb.Helper()
	f.Git("push", "--quiet", "--force", f.originURL(), sha+":"+scratchRef)
}

// dropCarried deletes the scratch ref carryToOrigin made on origin.
func (f *Fixture) dropCarried() {
	f.tb.Helper()
	f.OriginGit("update-ref", "-d", scratchRef)
}

// OriginTag makes an annotated tag name on the bare origin at the local commit sha, whose message
// is name.
func (f *Fixture) OriginTag(name, sha string) {
	f.tb.Helper()
	f.carryToOrigin(sha)
	f.OriginGit("tag", "--annotate", "--message", name, name, sha)
	f.dropCarried()
}

// OriginLightweightTag makes a lightweight tag name on the bare origin at the local commit sha.
func (f *Fixture) OriginLightweightTag(name, sha string) {
	f.tb.Helper()
	f.Git("push", "--quiet", f.originURL(), sha+":refs/tags/"+name)
}

// Publish puts origin's copy of branch at the local tip by the bare origin's path, which reaches
// it whatever origin's URLs name.
func (f *Fixture) Publish(branch string) {
	f.tb.Helper()
	f.Git("push", "--quiet", f.originURL(), branch)
}

// LandPull lands the commit rev names on origin's trunk as the squash merge of pull request
// number, and keeps that commit as the pull request's head. The squash carries the commit's
// subject followed by the number, as GitHub writes it.
func (f *Fixture) LandPull(number int, rev string) {
	f.tb.Helper()

	tip := f.Git("rev-parse", rev)
	subject := f.Git("log", "-1", "--format=%s", tip)

	f.PullRef(number, tip)
	f.SquashOnOrigin(tip, subject+" (#"+strconv.Itoa(number)+")", "")
}

// TrunkWorktree adds a linked worktree with the trunk checked out and returns its directory. The
// trunk must not be checked out in the working repository.
func (f *Fixture) TrunkWorktree() string {
	f.tb.Helper()

	dir := filepath.Join(f.root, "trunk-worktree")
	f.Git("worktree", "add", "--quiet", dir, f.trunk)

	return dir
}

// BranchWorktree checks out the trunk in the working repository, adds a linked worktree with
// branch checked out, and returns that worktree's directory. The branch must already exist.
func (f *Fixture) BranchWorktree(branch string) string {
	f.tb.Helper()

	f.Git("switch", "--quiet", f.trunk)

	dir := filepath.Join(f.root, "branch-worktree")
	f.Git("worktree", "add", "--quiet", dir, branch)

	return dir
}

// AdvanceTrunk adds a commit to the local trunk without checking it out or pushing it, and returns
// its sha. The trunk must not be checked out in any worktree.
func (f *Fixture) AdvanceTrunk() string {
	f.tb.Helper()

	return f.commitOnTrunk(func(dir string) string {
		return f.commit(dir, "local change on "+f.trunk)
	})
}

// UntrackedOnTrunk commits a file to the trunk and pushes it to origin by the bare origin's path,
// whatever origin's URLs name, then leaves an untracked file with other content at the same path
// in Dir, so that checking out the trunk would overwrite it. It returns the path relative to Dir.
// The trunk must not be checked out in any worktree.
func (f *Fixture) UntrackedOnTrunk() string {
	f.tb.Helper()

	f.commitOnTrunk(func(dir string) string {
		return f.commitFile(dir, trunkOnlyPath, "tracked on the trunk\n", "track "+trunkOnlyPath)
	})
	f.Publish(f.trunk)
	WriteFile(f.tb, f.dir, trunkOnlyPath, "untracked here\n")

	return trunkOnlyPath
}

// commitOnTrunk runs commit in a scratch worktree detached at the trunk, moves the trunk to the
// sha commit returns, and returns it. Working detached leaves every other checkout untouched.
func (f *Fixture) commitOnTrunk(commit func(dir string) string) string {
	f.tb.Helper()

	trunkRef := f.trunkRef()
	parent := f.Git("rev-parse", "--verify", trunkRef)

	dir := filepath.Join(f.root, "trunk-scratch")
	f.Git("worktree", "add", "--quiet", "--detach", dir, parent)

	sha := commit(dir)

	f.Git("worktree", "remove", "--force", dir)
	f.Git("update-ref", trunkRef, sha, parent)

	return sha
}

// commit adds a commit that creates one new file in the repository at dir and returns its sha.
// File names are unique across the fixture, so commits made on different sides never conflict.
func (f *Fixture) commit(dir, subject string) string {
	f.tb.Helper()

	f.serial++
	name := fmt.Sprintf("change-%d.txt", f.serial)

	return f.commitFile(dir, name, subject+"\n", subject)
}

// commitFile writes content to name in the repository at dir, commits it with subject, and returns
// the new sha.
func (f *Fixture) commitFile(dir, name, content, subject string) string {
	f.tb.Helper()

	WriteFile(f.tb, dir, name, content)
	f.run(dir, "add", "--", name)
	f.run(dir, "commit", "--quiet", "--message", subject)

	return f.run(dir, "rev-parse", "HEAD")
}

// ---------------------------------------------------------------------------
// Serving over ssh
// ---------------------------------------------------------------------------

// Serving returns a script that serves the repository at dir: it runs the service git asked the host
// for, which is upload-pack for a fetch and receive-pack for a push. It reads the arguments git
// passes ssh, so it suits ServedBy.
func Serving(dir string) string {
	// Git passes the remote command last, after the options and the host, such as `git-upload-pack
	// '<path>'`. The loop leaves the last argument in remote, and the service is its first word.
	return `for remote do :; done; service="${remote%% *}"; git "${service#git-}" '` + filepath.ToSlash(dir) + `'`
}

// ServedEnv returns the fixture's environment with ssh replaced by a stand-in that serves the
// repository at dir, so that ssh URLs of origin reach a local repository and never the network.
func (f *Fixture) ServedEnv(dir string) []string {
	f.tb.Helper()

	return f.ServedBy(Serving(dir))
}

// ServedBy returns the fixture's environment with ssh replaced by a stand-in that takes the
// options OpenSSH takes. Asked for its config with `-G`, the stand-in answers through the real ssh
// from the fixture's ssh config; every other call runs serving, a shell script that reads the
// arguments git passed ssh.
//
// The ssh command stays the one Env names: the stand-in comes first on PATH under ssh's name.
func (f *Fixture) ServedBy(serving string) []string {
	f.tb.Helper()

	return append(FirstOnPath(f.Env(), f.standIn()), "GIT_SSH_VARIANT=ssh", servingEnv+"="+serving)
}

// standIn writes the stand-in for ssh into a directory of its own and returns that directory.
func (f *Fixture) standIn() string {
	f.tb.Helper()

	const name = "ssh"

	ssh, err := exec.LookPath(name)
	if err != nil {
		f.tb.Fatalf("gittest: find ssh: %v", err)
	}

	script := "for arg do\n" +
		"\tif [ \"$arg\" = -G ]; then exec '" + filepath.ToSlash(ssh) + "' \"$@\"; fi\n" +
		"done\n" +
		"eval \"$" + servingEnv + "\"\n"

	dir := filepath.Join(f.root, standInDir)

	err = os.MkdirAll(dir, 0o750)
	if err != nil {
		f.tb.Fatalf("gittest: create the directory of the stand-in for ssh: %v", err)
	}

	writeScript(f.tb, dir, name, script)

	return dir
}

// SetSSHConfig replaces the content of the fixture's ssh config with content.
func (f *Fixture) SetSSHConfig(content string) {
	f.tb.Helper()
	WriteFile(f.tb, f.root, sshConfigName, content)
}

// RemoveSSHConfig deletes the fixture's ssh config, so that an ssh that reads it exits non-zero.
func (f *Fixture) RemoveSSHConfig() {
	f.tb.Helper()

	err := os.Remove(filepath.Join(f.root, sshConfigName))
	if err != nil {
		f.tb.Fatalf("gittest: remove the ssh config: %v", err)
	}
}
