// Package gittest builds real git repositories for tests: a working repository with a bare origin
// under the test's temporary directory, isolated from the user's git configuration.
package gittest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
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
	untrackedPath = "untracked.txt"
	scratchRef    = "refs/gittest/squash"
)

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

// New builds a working repository whose trunk has one commit pushed to a bare origin whose HEAD names trunk.
func New(tb testing.TB, trunk string) *Fixture {
	tb.Helper()

	git, err := exec.LookPath("git")
	if err != nil {
		tb.Fatalf("find git: %v", err)
	}

	// Resolved so that Dir matches what git prints for the repository, which follows symbolic links.
	root, err := filepath.EvalSymlinks(tb.TempDir())
	if err != nil {
		tb.Fatalf("resolve temporary directory: %v", err)
	}

	home := filepath.Join(root, "home")
	f := &Fixture{
		tb:        tb,
		env:       isolatedEnv(home),
		git:       git,
		root:      root,
		dir:       filepath.Join(root, "repo"),
		originDir: filepath.Join(root, "origin.git"),
		trunk:     trunk,
	}

	err = os.MkdirAll(home, 0o750)
	if err != nil {
		tb.Fatalf("create home directory: %v", err)
	}

	f.run(root, "init", "--quiet", "--bare", "--initial-branch="+trunk, f.originDir)
	f.run(root, "init", "--quiet", "--initial-branch="+trunk, f.dir)
	f.Git("config", "--local", "user.name", AuthorName)
	f.Git("config", "--local", "user.email", AuthorEmail)
	f.Git("config", "--local", "commit.gpgsign", "false")
	f.Git("remote", "add", origin, f.originURL())
	f.write(f.dir, dirtyPath, "fixture\n")
	f.write(f.dir, stagedPath, "fixture\n")
	f.Git("add", "--", dirtyPath, stagedPath)
	f.Git("commit", "--quiet", "--message", "initial commit")
	f.Push(trunk)

	return f
}

// isolatedEnv returns the process environment with everything that could bring in the user's git
// setup replaced: no system or global config, a home under the fixture, and a fixed identity and date.
func isolatedEnv(home string) []string {
	env := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)

		// XDG_CONFIG_HOME goes too: git reads its default ignore file from there.
		return strings.HasPrefix(key, "GIT_") || key == "HOME" || key == "USERPROFILE" || key == "XDG_CONFIG_HOME"
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

// Dir returns the working repository's directory.
func (f *Fixture) Dir() string { return f.dir }

// OriginDir returns the bare origin's directory.
func (f *Fixture) OriginDir() string { return f.originDir }

// Env returns the environment every fixture git call runs with, for a caller's own git runner.
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

// Commits checks out branch, cutting it from the trunk when it does not exist yet, adds one commit
// per subject, and returns the new shas, oldest first.
func (f *Fixture) Commits(branch string, subjects ...string) []string {
	f.tb.Helper()

	if f.Git("branch", "--list", branch) == "" {
		f.Git("switch", "--quiet", "--create", branch, f.trunk)
	} else {
		f.Git("switch", "--quiet", branch)
	}

	shas := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		shas = append(shas, f.commit(f.dir, subject))
	}

	return shas
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

// DirtyFile leaves an unstaged change in a tracked file and returns its path relative to Dir.
func (f *Fixture) DirtyFile() string {
	f.tb.Helper()
	f.write(f.dir, dirtyPath, "dirty\n")

	return dirtyPath
}

// StagedFile stages a change to a tracked file and returns its path relative to Dir.
func (f *Fixture) StagedFile() string {
	f.tb.Helper()
	f.write(f.dir, stagedPath, "staged\n")
	f.Git("add", "--", stagedPath)

	return stagedPath
}

// UntrackedFile creates a file git does not track and returns its path relative to Dir.
func (f *Fixture) UntrackedFile() string {
	f.tb.Helper()
	f.write(f.dir, untrackedPath, "untracked\n")

	return untrackedPath
}

// Sibling checks out a new branch cut from the first commit of the branch named by of past the
// trunk, adds one commit to it, and returns the new tip.
func (f *Fixture) Sibling(name, of string) string {
	f.tb.Helper()

	first, _, _ := strings.Cut(f.Git("rev-list", "--reverse", f.trunk+".."+of), "\n")
	if first == "" {
		f.tb.Fatalf("branch %s has no commit past %s to share", of, f.trunk)
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
// squash merge of sha would, and returns the new sha. The working repository does not fetch it.
func (f *Fixture) SquashOnOrigin(sha, subject string) string {
	f.tb.Helper()

	// Pushing to a scratch ref carries the objects of sha to origin without creating a branch.
	f.Git("push", "--quiet", "--force", f.originURL(), sha+":"+scratchRef)

	trunkRef := "refs/heads/" + f.trunk
	parent := f.OriginGit("rev-parse", "--verify", trunkRef)
	squash := f.OriginGit("commit-tree", sha+"^{tree}", "-p", parent, "-m", subject)
	f.OriginGit("update-ref", trunkRef, squash, parent)
	f.OriginGit("update-ref", "-d", scratchRef)

	return squash
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
	f.SquashOnOrigin(tip, subject+" (#"+strconv.Itoa(number)+")")
}

// Serving returns the command that stands in for ssh: it runs, on the repository at dir, the
// service git asked the host for, which is upload-pack for a fetch and receive-pack for a push.
func Serving(dir string) string {
	// Git hands the stand-in the host and then the remote command, such as `git-upload-pack '<path>'`.
	// The service is the first word of that command, and the trailing # turns both into a shell comment.
	return `service="${2%% *}"; git "${service#git-}" '` + filepath.ToSlash(dir) + `' #`
}

// ServedEnv returns the fixture's environment with ssh replaced by a command that serves the
// repository at dir, so that ssh URLs of origin reach a local repository and never the network.
func (f *Fixture) ServedEnv(dir string) []string {
	return append(f.Env(), "GIT_SSH_VARIANT=simple", "GIT_SSH_COMMAND="+Serving(dir))
}

// originURL returns the bare origin's location in the slash form git accepts on every platform.
func (f *Fixture) originURL() string { return filepath.ToSlash(f.originDir) }

// commit adds a commit that creates one new file in the repository at dir and returns its sha.
// File names are unique across the fixture, so commits made on different sides never conflict.
func (f *Fixture) commit(dir, subject string) string {
	f.tb.Helper()

	f.serial++
	name := fmt.Sprintf("change-%d.txt", f.serial)

	f.write(dir, name, subject+"\n")
	f.run(dir, "add", "--", name)
	f.run(dir, "commit", "--quiet", "--message", subject)

	return f.run(dir, "rev-parse", "HEAD")
}

func (f *Fixture) write(dir, name, content string) {
	f.tb.Helper()

	err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
	if err != nil {
		f.tb.Fatalf("write %s: %v", name, err)
	}
}

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
		f.tb.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, strings.TrimSpace(stderr.String()))
	}

	return strings.TrimSpace(stdout.String())
}
