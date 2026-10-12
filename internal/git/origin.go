package git

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jeffzi/ils/internal/child"
)

const (
	httpsPrefix    = "https://"
	sshPrefix      = "ssh://git@"
	shortSSHPrefix = "git@"
)

// Origin is the repository a remote URL names.
type Origin struct {
	// Host is the host name alone, without a port: gh addresses a host by name.
	Host  string
	Owner string
	Name  string
}

// OriginError reports a remote URL that names no host, owner, and name.
type OriginError struct {
	URL string
}

// Error names the rejected URL and the three shapes ParseOrigin accepts.
func (e *OriginError) Error() string {
	return fmt.Sprintf(
		"remote URL %q is not https://<host>/<owner>/<name>, ssh://git@<host>/<owner>/<name>, or git@<host>:<owner>/<name>",
		e.URL,
	)
}

// ParseOrigin parses a remote URL into the host, owner, and name it names.
//
// It accepts `https://<host>/<owner>/<name>`, `ssh://git@<host>[:port]/<owner>/<name>`,
// and `git@<host>:<owner>/<name>`, each with an optional trailing `.git` and
// slash. Any other shape is an *OriginError.
func ParseOrigin(raw string) (Origin, error) {
	host, path, ok := splitOrigin(raw)
	if !ok {
		return Origin{}, &OriginError{URL: raw}
	}

	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")

	owner, name, found := strings.Cut(path, "/")
	if !found || host == "" || owner == "" || name == "" || strings.Contains(name, "/") {
		return Origin{}, &OriginError{URL: raw}
	}

	return Origin{Host: host, Owner: owner, Name: name}, nil
}

// splitOrigin returns the host and the owner/name path of a remote URL.
func splitOrigin(raw string) (host, path string, ok bool) {
	if strings.HasPrefix(raw, httpsPrefix) || strings.HasPrefix(raw, sshPrefix) {
		parsed, err := url.Parse(raw)
		if err != nil {
			return "", "", false
		}

		return parsed.Hostname(), strings.TrimPrefix(parsed.Path, "/"), true
	}

	rest, found := strings.CutPrefix(raw, shortSSHPrefix)
	if !found {
		return "", "", false
	}

	host, path, ok = strings.Cut(rest, ":")
	if strings.Contains(host, "/") {
		return "", "", false
	}

	return host, path, ok
}

// Same reports whether o and other name the same repository, ignoring case.
func (o Origin) Same(other Origin) bool {
	return strings.EqualFold(o.Host, other.Host) &&
		strings.EqualFold(o.Owner, other.Owner) &&
		strings.EqualFold(o.Name, other.Name)
}

const (
	symrefLead = "ref: "
	symrefTail = "\tHEAD"

	// absentCode is the exit of `git ls-remote --exit-code` when no ref matched.
	absentCode = 2
)

// ErrNoSymref reports an answer from origin that names no default branch: no `ref:` line for HEAD.
var ErrNoSymref = errors.New("origin's HEAD names no branch")

// OriginURLs holds origin's fetch and push URLs, raw and parsed.
type OriginURLs struct {
	Fetch    Origin
	Push     Origin
	FetchURL string
	PushURL  string
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

// Unwrap returns the *child.ExitError of the refused push.
func (e *PushError) Unwrap() error {
	return e.Err
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
// A branch name is fetched as the branch, never as a tag of the same name. A fetch that fails is a
// *RemoteError.
func (r Repo) Fetch(ctx context.Context, ref string) (string, error) {
	if !strings.HasPrefix(ref, "refs/") {
		ref = headsRef + ref
	}

	_, err := r.remote(ctx, "fetch", origin, ref)
	if err != nil {
		return "", err
	}

	return r.line(ctx, "rev-parse", "FETCH_HEAD")
}

// Push pushes the branch name to origin under the same name and sets it as the upstream.
//
// It never forces. A push that git or origin refuses is a *PushError.
func (r Repo) Push(ctx context.Context, name string) error {
	_, err := r.Runner.Run(ctx, "push", "-u", origin, name)
	if exitErr, ok := errors.AsType[*child.ExitError](err); ok {
		return &PushError{Branch: name, Stderr: exitErr.Stderr, Err: err}
	}

	return err
}

// remote runs a git call that talks to origin, whose failure is a *RemoteError.
func (r Repo) remote(ctx context.Context, args ...string) (Result, error) {
	result, err := r.Runner.Run(ctx, args...)
	if err != nil {
		return Result{}, remoteFailure(err)
	}

	return result, nil
}

// remoteFailure wraps the failure of a call to origin, leaving a missing git program as it is:
// that failure says nothing about origin.
func remoteFailure(err error) error {
	if _, ok := errors.AsType[*NotFoundError](err); ok {
		return err
	}

	return &RemoteError{Err: err}
}
