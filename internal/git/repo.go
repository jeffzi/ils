package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jeffzi/ils/internal/child"
)

// MinimumVersion is the oldest git ils works with: the git of Ubuntu 22.04 LTS.
const MinimumVersion = "2.34.1"

const (
	// versionPrefix starts the first line of `git --version`.
	versionPrefix = "git version "
	// releaseCandidate starts the component git puts after a release candidate's version: its
	// version script turns a tag's -rcN into .rcN.
	releaseCandidate = "rc"
	// versionComponents counts the numeric components of a version: major, minor, patch.
	versionComponents = 3

	origin   = "origin"
	headsRef = "refs/heads/"

	// noRemoteCode is the exit of `git remote get-url` for a remote that is not configured.
	noRemoteCode = 2
	// falseCode is the exit of `git symbolic-ref --quiet` on a detached HEAD, of
	// `git show-ref --verify --quiet` for a ref that does not exist, and of
	// `git merge-base --is-ancestor` when the answer is no, and of `git config --get` for a key that
	// is not set.
	falseCode = 1
	// fatalCode is the exit of a git command that died of a fatal error.
	fatalCode = 128

	commitFormat = "--format=%H %s"
)

// StandingKind says which of the places a command can stand in applies.
type StandingKind int

const (
	// OnBranch is HEAD on a branch of a repository that has an origin.
	OnBranch StandingKind = iota + 1
	// Detached is HEAD on a commit and not on a branch.
	Detached
	// NoOrigin is a repository with no remote named origin.
	NoOrigin
)

// Standing is where a command stands: on a branch, on a detached HEAD, or without an origin.
type Standing struct {
	// Branch is the current branch's name, set only when Kind is OnBranch.
	Branch string
	Kind   StandingKind
}

// Divergence counts the commits each of two tips has that the other lacks.
type Divergence struct {
	Ahead  int
	Behind int
}

// Commit is one commit of a list: its sha and the first line of its message.
type Commit struct {
	SHA     string
	Subject string
}

// Repo makes typed git calls in the repository its runner stands in.
type Repo struct {
	Runner Runner
}

// Standing returns where the command stands.
//
// A repository with no remote named origin is NoOrigin whatever HEAD is on.
func (r Repo) Standing(ctx context.Context) (Standing, error) {
	_, err := r.Runner.Run(ctx, "remote", "get-url", origin)
	if exited(err, noRemoteCode) {
		return Standing{Kind: NoOrigin}, nil
	}

	if err != nil {
		return Standing{}, err
	}

	branch, onBranch, err := r.currentBranch(ctx)
	if err != nil {
		return Standing{}, err
	}

	if !onBranch {
		return Standing{Kind: Detached}, nil
	}

	return Standing{Kind: OnBranch, Branch: branch}, nil
}

// NoRepositoryError reports that git found no repository it can work in.
type NoRepositoryError struct {
	// Err is git's refusal, a *child.ExitError that carries git's message.
	Err error
}

// Error returns git's refusal, which says why it found no repository.
func (e *NoRepositoryError) Error() string {
	return e.Err.Error()
}

// Unwrap returns git's refusal.
func (e *NoRepositoryError) Unwrap() error {
	return e.Err
}

// CheckRepository returns nil when git finds a repository to work in from the runner's directory,
// and a *NoRepositoryError when it dies of a fatal error looking for one.
//
// Git dies the same way when it finds none and when it refuses the one it finds, such as one owned
// by another user; only its message, which the error carries, tells the two apart.
func (r Repo) CheckRepository(ctx context.Context) error {
	_, err := r.Runner.Run(ctx, "rev-parse", "--git-dir")
	if exited(err, fatalCode) {
		return &NoRepositoryError{Err: err}
	}

	return err
}

// HeadSHA returns the sha of the commit HEAD is on.
func (r Repo) HeadSHA(ctx context.Context) (string, error) {
	return r.line(ctx, "rev-parse", "HEAD")
}

// HasCommits reports whether the local branch has a commit; one made with `git switch --orphan`
// has none until the first commit on it.
func (r Repo) HasCommits(ctx context.Context, branch string) (bool, error) {
	return r.holds(ctx, "show-ref", "--verify", "--quiet", headsRef+branch)
}

// GitDir returns the absolute path of the repository's git directory.
func (r Repo) GitDir(ctx context.Context) (string, error) {
	dir, err := r.line(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}

	return filepath.FromSlash(dir), nil
}

// DirtyFiles returns the tracked files that differ from HEAD, staged or not; the tree is dirty when
// there is at least one.
//
// Untracked files are not listed. A repository git cannot read is an error.
func (r Repo) DirtyFiles(ctx context.Context) ([]string, error) {
	result, err := r.Runner.Run(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=no")
	if err != nil {
		return nil, err
	}

	const pathStart = len("XY ")

	var files []string

	entries := strings.Split(result.Stdout, "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) <= pathStart {
			continue
		}

		files = append(files, entry[pathStart:])

		// A rename or a copy is followed by one more entry, the path it came from.
		if strings.ContainsAny(entry[:pathStart], "RC") {
			i++
		}
	}

	return files, nil
}

// Divergence returns how many commits a has that b lacks, and how many b has that a lacks.
func (r Repo) Divergence(ctx context.Context, a, b string) (Divergence, error) {
	args := []string{"rev-list", "--left-right", "--count", a + "..." + b}
	call := child.CommandLine(child.Argv(program, args))

	out, err := r.line(ctx, args...)
	if err != nil {
		return Divergence{}, err
	}

	left, right, found := strings.Cut(out, "\t")
	if !found {
		return Divergence{}, fmt.Errorf("%s: unexpected output %q", call, out)
	}

	ahead, err := strconv.Atoi(left)
	if err != nil {
		return Divergence{}, fmt.Errorf("%s: read ahead count: %w", call, err)
	}

	behind, err := strconv.Atoi(right)
	if err != nil {
		return Divergence{}, fmt.Errorf("%s: read behind count: %w", call, err)
	}

	return Divergence{Ahead: ahead, Behind: behind}, nil
}

// Contains reports whether the history of a contains b.
func (r Repo) Contains(ctx context.Context, a, b string) (bool, error) {
	return r.holds(ctx, "merge-base", "--is-ancestor", b, a)
}

// CommitsPast returns the commits on tip that origin's trunk lacks, merges left out, oldest first.
//
// It reads origin/<trunk>, which is current only after a fetch of trunk.
func (r Repo) CommitsPast(ctx context.Context, tip, trunk string) ([]Commit, error) {
	return r.commits(ctx, "--no-merges", tip, excludeTrunk(trunk))
}

// LocalTrunkAhead reports whether the local trunk has anything origin's trunk lacks, and returns
// the commits it has past origin's trunk, merges included, oldest first, so that a local trunk
// whose only such commit is a merge still names it. A local trunk that does
// not exist, or that origin's trunk contains, is not ahead.
//
// It reads origin/<trunk>, which is current only after a fetch of trunk.
func (r Repo) LocalTrunkAhead(ctx context.Context, trunk string) (ahead bool, past []Commit, err error) {
	exists, err := r.HasCommits(ctx, trunk)
	if err != nil || !exists {
		return false, nil, err
	}

	local := headsRef + trunk

	behind, err := r.Contains(ctx, originTrunk(trunk), local)
	if err != nil || behind {
		return false, nil, err
	}

	past, err = r.commits(ctx, local, excludeTrunk(trunk))
	if err != nil {
		return false, nil, err
	}

	return true, past, nil
}

// OwnCommits returns the commits on tip that origin's trunk lacks and that none of the local
// branches named by branches carries, merges left out, oldest first.
//
// It reads origin/<trunk>, which is current only after a fetch of trunk.
func (r Repo) OwnCommits(ctx context.Context, tip, trunk string, branches []string) ([]Commit, error) {
	revisions := []string{"--no-merges", tip, excludeTrunk(trunk)}
	for _, branch := range branches {
		revisions = append(revisions, "^"+headsRef+branch)
	}

	return r.commits(ctx, revisions...)
}

// BranchesContaining returns the local branches that contain sha, the current branch left out.
func (r Repo) BranchesContaining(ctx context.Context, sha string) ([]string, error) {
	current, _, err := r.currentBranch(ctx)
	if err != nil {
		return nil, err
	}

	result, err := r.Runner.Run(ctx, "for-each-ref", "--format=%(refname)", headsRef, "--contains", sha)
	if err != nil {
		return nil, err
	}

	var branches []string

	for line := range strings.SplitSeq(result.Stdout, "\n") {
		branch, isBranch := strings.CutPrefix(line, headsRef)
		if isBranch && branch != current {
			branches = append(branches, branch)
		}
	}

	return branches, nil
}

// Unlanded returns the commits on tip whose patch neither the merged head nor origin's trunk
// contains, merges left out, oldest first.
//
// It reads origin/<trunk>, which is current only after a fetch of trunk.
func (r Repo) Unlanded(ctx context.Context, mergedHead, tip, trunk string) ([]Commit, error) {
	return r.commits(ctx, "--no-merges", "--cherry-pick", "--right-only", mergedHead+"..."+tip, excludeTrunk(trunk))
}

// Switch puts HEAD on branch. A branch that only origin has is created from origin's copy.
//
// A branch another worktree has checked out, or an untracked file the switch would overwrite, is a
// *child.ExitError that carries git's message, and leaves the tree as it was.
func (r Repo) Switch(ctx context.Context, branch string) error {
	// --guess makes the creation from origin's copy hold whatever checkout.guess says.
	_, err := r.Runner.Run(ctx, "switch", "--quiet", "--guess", branch)

	return err
}

// CreateBranch creates the branch name at origin's trunk as last fetched, with no upstream, and puts
// HEAD on it, carrying uncommitted changes and untracked files.
//
// A changed or untracked file the switch would overwrite is a *child.ExitError that carries git's
// message, which names the files, and leaves HEAD, the tree and the local branches as they were.
func (r Repo) CreateBranch(ctx context.Context, name, trunk string) error {
	_, err := r.Runner.Run(ctx, "switch", "--quiet", "--no-track", "--create", name, originTrunk(trunk))

	return err
}

// BranchNameError reports a name git cannot hold as a branch.
type BranchNameError struct {
	// Err is git's refusal, a *child.ExitError that carries git's message, or nil when git gives
	// none.
	Err  error
	Name string
}

// Error names the refused name, followed by git's refusal when there is one.
func (e *BranchNameError) Error() string {
	message := fmt.Sprintf("%q is not a valid branch name", e.Name)
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}

	return message
}

// Unwrap returns git's refusal of the name.
func (e *BranchNameError) Unwrap() error {
	return e.Err
}

// CheckBranchName returns nil when git can hold name as a branch, and a *BranchNameError when it
// cannot. A name that starts with a dash is refused, which git would read as an option, and so is
// one git reads as another branch's, such as @{-1} for the previous branch.
func (r Repo) CheckBranchName(ctx context.Context, name string) error {
	if strings.HasPrefix(name, "-") {
		return &BranchNameError{Name: name}
	}

	// --branch expands @{-N} to the name of the Nth branch checked out before, so an answer that
	// differs from name means name is not a branch name of its own.
	expanded, err := r.line(ctx, "check-ref-format", "--branch", name)
	if _, refused := errors.AsType[*child.ExitError](err); refused {
		return &BranchNameError{Name: name, Err: err}
	}

	if err != nil {
		return err
	}

	if expanded != name {
		return &BranchNameError{Name: name}
	}

	return nil
}

// Subject returns the first line of the message of the commit sha.
func (r Repo) Subject(ctx context.Context, sha string) (string, error) {
	return r.line(ctx, "show", "--no-patch", "--format=%s", sha)
}

// DetachAtOrigin detaches HEAD at origin's trunk as last fetched, keeping untracked files.
func (r Repo) DetachAtOrigin(ctx context.Context, trunk string) error {
	_, err := r.Runner.Run(ctx, "switch", "--quiet", "--detach", originTrunk(trunk))

	return err
}

// DeleteBranch deletes the local branch even when nothing has merged it, as a squash leaves it.
//
// Deleting the branch HEAD is on is a *child.ExitError that carries git's message.
func (r Repo) DeleteBranch(ctx context.Context, branch string) error {
	_, err := r.Runner.Run(ctx, "branch", "--quiet", "--delete", "--force", branch)

	return err
}

// CheckedOutElsewhere reports whether a worktree other than the current one has branch checked out.
// A worktree detached at the branch's commit does not have it checked out.
func (r Repo) CheckedOutElsewhere(ctx context.Context, branch string) (bool, error) {
	current, err := r.line(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return false, err
	}

	result, err := r.Runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}

	var worktree string

	for line := range strings.SplitSeq(result.Stdout, "\n") {
		if path, isWorktree := strings.CutPrefix(line, "worktree "); isWorktree {
			worktree = path

			continue
		}

		if line != "branch "+headsRef+branch {
			continue
		}

		same, err := sameDirectory(worktree, current)
		if err != nil {
			return false, err
		}

		if !same {
			return true, nil
		}
	}

	return false, nil
}

// sameDirectory reports whether the worktree paths a and b, as git prints them, name one
// directory. Both are compared with their symbolic links resolved, which also cleans them to this
// system's separators: a temporary directory reaches git as /private/var/... on macOS, git prints
// forward slashes on Windows, and a worktree recorded by another tool keeps the spelling it was
// given. A path that cannot be resolved, such as a worktree whose directory was removed, is an
// error naming it.
func sameDirectory(a, b string) (bool, error) {
	resolvedA, errA := filepath.EvalSymlinks(a)
	resolvedB, errB := filepath.EvalSymlinks(b)

	if err := errors.Join(errA, errB); err != nil {
		return false, fmt.Errorf("resolve worktree path: %w", err)
	}

	return resolvedA == resolvedB, nil
}

// CheckVersion returns nil when git is MinimumVersion or newer.
//
// A git that is older, or whose first line of `git --version` names no version that can be read,
// is a *child.ToolTooOldError; a release candidate of a version is older than that version. A git
// missing from PATH is a *child.ToolMissingError.
func (r Repo) CheckVersion(ctx context.Context) error {
	result, err := r.Runner.Run(ctx, "--version")
	if err != nil {
		return err
	}

	requirement := child.Requirement{Tool: program, Minimum: MinimumVersion, InstallURL: installURL}

	return requirement.CheckOutput(result.Stdout, VersionOf)
}

// VersionOf returns the version that a first line of `git --version` names, as semver without the
// leading v, or "" when it names none or does not start with "git version ".
//
// The version is the first three numeric components. A release candidate's .rcN component becomes
// the pre-release -rcN; any other trailing component, such as Git for Windows' .windows.N, is dropped.
func VersionOf(line string) string {
	rest, ok := strings.CutPrefix(line, versionPrefix)
	if !ok {
		return ""
	}

	token, _, _ := strings.Cut(rest, " ")

	parts := strings.Split(token, ".")
	if len(parts) < versionComponents {
		return ""
	}

	version := strings.Join(parts[:versionComponents], ".")
	if len(parts) > versionComponents && strings.HasPrefix(parts[versionComponents], releaseCandidate) {
		version += "-" + parts[versionComponents]
	}

	if !child.ValidVersion(version) {
		return ""
	}

	return version
}

// line runs git with args and returns its stdout without the trailing line break.
func (r Repo) line(ctx context.Context, args ...string) (string, error) {
	result, err := r.Runner.Run(ctx, args...)
	if err != nil {
		return "", err
	}

	return strings.TrimRight(result.Stdout, "\n"), nil
}

// holds runs git with args and reports whether it succeeded; an exit of falseCode means no.
func (r Repo) holds(ctx context.Context, args ...string) (bool, error) {
	_, err := r.Runner.Run(ctx, args...)
	if exited(err, falseCode) {
		return false, nil
	}

	return err == nil, err
}

// optionalLine runs git with args and returns its stdout without the trailing line break, as line
// does; an exit of falseCode means there is no line, which returns an empty value and found false
// rather than an error.
func (r Repo) optionalLine(ctx context.Context, args ...string) (value string, found bool, err error) {
	value, err = r.line(ctx, args...)
	if exited(err, falseCode) {
		return "", false, nil
	}

	return value, err == nil, err
}

// currentBranch returns the branch HEAD is on; onBranch is false on a detached HEAD.
func (r Repo) currentBranch(ctx context.Context) (branch string, onBranch bool, err error) {
	ref, onBranch, err := r.optionalLine(ctx, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || !onBranch {
		return "", false, err
	}

	return strings.TrimPrefix(ref, headsRef), true, nil
}

// commits runs git log over revisions and returns its commits, oldest first.
func (r Repo) commits(ctx context.Context, revisions ...string) ([]Commit, error) {
	args := append([]string{"log", "--reverse", "--topo-order", commitFormat}, revisions...)

	result, err := r.Runner.Run(ctx, args...)
	if err != nil {
		return nil, err
	}

	var commits []Commit

	for line := range strings.SplitSeq(result.Stdout, "\n") {
		if line == "" {
			continue
		}

		sha, subject, _ := strings.Cut(line, " ")
		commits = append(commits, Commit{SHA: sha, Subject: subject})
	}

	return commits, nil
}

// excludeTrunk returns the revision argument that leaves out everything origin's trunk has.
func excludeTrunk(trunk string) string {
	return "^" + originTrunk(trunk)
}

// originTrunk returns the remote-tracking ref of origin's trunk in full. Git resolves the short
// origin/<trunk> to a local branch of that name when one exists, which would stand in for origin's
// trunk.
func originTrunk(trunk string) string {
	return "refs/remotes/" + origin + "/" + trunk
}
