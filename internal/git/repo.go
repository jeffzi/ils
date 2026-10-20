package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"

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
	const call = "git rev-list --left-right --count"

	out, err := r.line(ctx, "rev-list", "--left-right", "--count", a+"..."+b)
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

	if !semver.IsValid(child.SemverPrefix + version) {
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
	return "^" + origin + "/" + trunk
}
