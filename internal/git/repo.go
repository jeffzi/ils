package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	origin     = "origin"
	headsRef   = "refs/heads/"
	symrefLead = "ref: "
	symrefTail = "\tHEAD"

	// absentCode is the exit of `git ls-remote --exit-code` when no ref matched.
	absentCode = 2
	// noRemoteCode is the exit of `git remote get-url` for a remote that is not configured.
	noRemoteCode = 2
	// falseCode is the exit of `git symbolic-ref --quiet` on a detached HEAD and of
	// `git merge-base --is-ancestor` when the answer is no.
	falseCode = 1

	commitFormat = "--format=%H %s"
)

// ErrNoSymref reports an answer from origin that names no default branch: no `ref:` line for HEAD.
var ErrNoSymref = errors.New("origin's HEAD names no branch")

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

// OriginURLs holds origin's fetch and push URLs, raw and parsed.
type OriginURLs struct {
	Fetch    Origin
	Push     Origin
	FetchURL string
	PushURL  string
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

// RemoteError reports that origin could not be reached, or did not give what was asked of it.
type RemoteError struct {
	Err error
}

// Error says origin could not be reached and appends the underlying failure.
func (e *RemoteError) Error() string {
	return "origin could not be reached: " + e.Err.Error()
}

// Unwrap returns the failure of the call to origin.
func (e *RemoteError) Unwrap() error {
	return e.Err
}

// PushError reports a push that git or origin refused.
type PushError struct {
	Err    error
	Branch string
	// Stderr is what git printed about the refusal.
	Stderr string
}

// Error names the refused branch and git's trimmed stderr.
func (e *PushError) Error() string {
	return fmt.Sprintf("push of %s refused: %s", e.Branch, strings.TrimSpace(e.Stderr))
}

// Unwrap returns the *ExitError of the refused push.
func (e *PushError) Unwrap() error {
	return e.Err
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

// OriginURLs returns origin's fetch and push URLs, raw and parsed.
//
// A URL that does not parse is an *OriginError, and both raw URLs are still returned.
func (r Repo) OriginURLs(ctx context.Context) (OriginURLs, error) {
	fetchURL, err := r.line(ctx, "remote", "get-url", origin)
	if err != nil {
		return OriginURLs{}, err
	}

	pushURL, err := r.line(ctx, "remote", "get-url", "--push", origin)
	if err != nil {
		return OriginURLs{}, err
	}

	fetch, fetchErr := ParseOrigin(fetchURL)
	push, pushErr := ParseOrigin(pushURL)

	return OriginURLs{Fetch: fetch, Push: push, FetchURL: fetchURL, PushURL: pushURL}, errors.Join(fetchErr, pushErr)
}

// Trunk returns origin's default branch, asked of origin.
//
// An origin that does not answer is a *RemoteError, and an answer that names no branch is
// ErrNoSymref.
func (r Repo) Trunk(ctx context.Context) (string, error) {
	result, err := r.remote(ctx, "ls-remote", "--symref", origin, "HEAD")
	if err != nil {
		return "", err
	}

	for line := range strings.SplitSeq(result.Stdout, "\n") {
		ref, found := strings.CutPrefix(line, symrefLead)
		if !found {
			continue
		}

		ref, found = strings.CutSuffix(ref, symrefTail)
		if !found {
			continue
		}

		if branch, isBranch := strings.CutPrefix(ref, headsRef); isBranch {
			return branch, nil
		}
	}

	return "", fmt.Errorf("git ls-remote --symref %s HEAD: %w", origin, ErrNoSymref)
}

// RemoteBranch reports whether origin has the branch name, and its sha there when it does.
//
// An origin that does not answer is a *RemoteError, never an absent branch.
func (r Repo) RemoteBranch(ctx context.Context, name string) (sha string, found bool, err error) {
	ref := headsRef + name

	result, err := r.Runner.Run(ctx, "ls-remote", "--exit-code", origin, ref)
	if exited(err, absentCode) {
		return "", false, nil
	}

	if err != nil {
		return "", false, remoteFailure(err)
	}

	// The pattern also matches a longer ref that ends with it, so only the exact ref counts.
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		if sha, listed, ok := strings.Cut(line, "\t"); ok && listed == ref {
			return sha, true, nil
		}
	}

	return "", false, nil
}

// Fetch fetches one ref from origin, a branch name or a full ref, and returns the fetched sha.
//
// A fetch that fails is a *RemoteError.
func (r Repo) Fetch(ctx context.Context, ref string) (string, error) {
	_, err := r.remote(ctx, "fetch", origin, ref)
	if err != nil {
		return "", err
	}

	return r.line(ctx, "rev-parse", "FETCH_HEAD")
}

// Divergence returns how many commits a has that b lacks, and how many b has that a lacks.
func (r Repo) Divergence(ctx context.Context, a, b string) (Divergence, error) {
	out, err := r.line(ctx, "rev-list", "--left-right", "--count", a+"..."+b)
	if err != nil {
		return Divergence{}, err
	}

	left, right, found := strings.Cut(out, "\t")
	if !found {
		return Divergence{}, fmt.Errorf("git rev-list --left-right --count: unexpected output %q", out)
	}

	ahead, err := strconv.Atoi(left)
	if err != nil {
		return Divergence{}, fmt.Errorf("git rev-list --left-right --count: read ahead count: %w", err)
	}

	behind, err := strconv.Atoi(right)
	if err != nil {
		return Divergence{}, fmt.Errorf("git rev-list --left-right --count: read behind count: %w", err)
	}

	return Divergence{Ahead: ahead, Behind: behind}, nil
}

// Contains reports whether the history of a contains b.
func (r Repo) Contains(ctx context.Context, a, b string) (bool, error) {
	_, err := r.Runner.Run(ctx, "merge-base", "--is-ancestor", b, a)
	if exited(err, falseCode) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	return true, nil
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

// Push pushes the branch name to origin under the same name and sets it as the upstream.
//
// It never forces. A push that git or origin refuses is a *PushError.
func (r Repo) Push(ctx context.Context, name string) error {
	_, err := r.Runner.Run(ctx, "push", "-u", origin, name)
	if exitErr, ok := errors.AsType[*ExitError](err); ok {
		return &PushError{Branch: name, Stderr: exitErr.Stderr, Err: err}
	}

	return err
}

// line runs git with args and returns its stdout without the trailing line break.
func (r Repo) line(ctx context.Context, args ...string) (string, error) {
	result, err := r.Runner.Run(ctx, args...)
	if err != nil {
		return "", err
	}

	return strings.TrimRight(result.Stdout, "\n"), nil
}

// remote runs a git call that talks to origin, whose failure is a *RemoteError.
func (r Repo) remote(ctx context.Context, args ...string) (Result, error) {
	result, err := r.Runner.Run(ctx, args...)
	if err != nil {
		return Result{}, remoteFailure(err)
	}

	return result, nil
}

// currentBranch returns the branch HEAD is on; onBranch is false on a detached HEAD.
func (r Repo) currentBranch(ctx context.Context) (branch string, onBranch bool, err error) {
	ref, err := r.line(ctx, "symbolic-ref", "--quiet", "HEAD")
	if exited(err, falseCode) {
		return "", false, nil
	}

	if err != nil {
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

// exited reports whether err is git exiting with code.
func exited(err error, code int) bool {
	exitErr, ok := errors.AsType[*ExitError](err)

	return ok && exitErr.Code == code
}

// remoteFailure wraps the failure of a call to origin, leaving a missing git program as it is:
// that failure says nothing about origin.
func remoteFailure(err error) error {
	if _, ok := errors.AsType[*NotFoundError](err); ok {
		return err
	}

	return &RemoteError{Err: err}
}
