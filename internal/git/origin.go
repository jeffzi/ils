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
	// Err, when set, is why a URL of the https:// or ssh:// shape failed to parse.
	Err error
	URL string
}

// Error names the rejected URL and the three shapes ParseOrigin accepts, followed by Err when set.
func (e *OriginError) Error() string {
	msg := fmt.Sprintf(
		"remote URL %q is not https://<host>/<owner>/<name>, ssh://git@<host>/<owner>/<name>, or git@<host>:<owner>/<name>",
		e.URL,
	)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}

	return msg
}

// Unwrap returns why the URL failed to parse, or nil when it parsed but named no repository.
func (e *OriginError) Unwrap() error {
	return e.Err
}

// ParseOrigin parses a remote URL into the host, owner, and name it names.
//
// It accepts `https://<host>/<owner>/<name>`, `ssh://git@<host>[:port]/<owner>/<name>`,
// and `git@<host>:<owner>/<name>`, each with an optional trailing `.git` and
// slash. Any other shape is an *OriginError.
func ParseOrigin(raw string) (Origin, error) {
	parsed, _, err := parseRemote(raw)

	return parsed, err
}

// parseRemote parses raw as ParseOrigin does and also reports whether the URL reaches origin over
// ssh.
func parseRemote(raw string) (parsed Origin, overSSH bool, err error) {
	host, path, overSSH, err := splitOrigin(raw)
	if err != nil {
		return Origin{}, false, err
	}

	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")

	owner, name, found := strings.Cut(path, "/")
	if !found || host == "" || owner == "" || name == "" || strings.Contains(name, "/") {
		return Origin{}, false, &OriginError{URL: raw}
	}

	return Origin{Host: host, Owner: owner, Name: name}, overSSH, nil
}

// splitOrigin returns the host and the owner/name path of a remote URL, and whether the URL reaches
// origin over ssh. A URL of none of the three shapes is an *OriginError, which wraps the parse
// error of a URL of the https:// or ssh:// shape that failed to parse.
func splitOrigin(raw string) (host, path string, overSSH bool, err error) {
	overHTTPS := strings.HasPrefix(raw, httpsPrefix)
	if overHTTPS || strings.HasPrefix(raw, sshPrefix) {
		parsed, parseErr := url.Parse(raw)
		if parseErr != nil {
			return "", "", false, &OriginError{URL: raw, Err: parseErr}
		}

		return parsed.Hostname(), strings.TrimPrefix(parsed.Path, "/"), !overHTTPS, nil
	}

	rest, found := strings.CutPrefix(raw, shortSSHPrefix)
	if !found {
		return "", "", false, &OriginError{URL: raw}
	}

	host, path, found = strings.Cut(rest, ":")
	if !found || strings.Contains(host, "/") {
		return "", "", false, &OriginError{URL: raw}
	}

	return host, path, true, nil
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

// PushError reports a push that git or origin refused for the branch's ref: a non-fast-forward, a
// hook of origin that declined it, a ref origin hides. An origin that never answered, or that hung
// up before it reported on the ref, is a *RemoteError instead.
type PushError struct {
	Err    error
	Branch string
	// Reason is the summary git gives for the ref, such as `[rejected] (fetch first)` or
	// `[remote rejected] (pre-receive hook declined)`.
	Reason string
	// Stderr is what git printed on stderr, which holds the lines origin printed (`remote: …`).
	Stderr string
}

// Error names the refused branch and git's reason, followed by git's trimmed stderr.
func (e *PushError) Error() string {
	message := fmt.Sprintf("push of %s refused: %s", e.Branch, e.Reason)
	if said := strings.TrimSpace(e.Stderr); said != "" {
		message += "\n" + said
	}

	return message
}

// Unwrap returns the *child.ExitError of the refused push.
func (e *PushError) Unwrap() error {
	return e.Err
}

// OriginURLs returns origin's fetch and push URLs, raw and parsed.
//
// The host of a URL that reaches origin over ssh is the host the ssh command git connects with
// reaches for it, read as sshHostname reads it: a Host alias of an ssh config reads as the HostName
// it stands for, and a host under github.com reads as github.com, as gh files it. The ssh command
// is found on the PATH of the runner's environment, or on this process's PATH when the runner has
// no environment, and runs with that environment. A GIT_SSH program that is not found, of any
// variant, and a command line of variant ssh or auto whose program is not found, are an
// *SSHCommandMissingError; a command line of variant plain is not checked, and plain ssh reads its
// host. A plain ssh missing from PATH is a *child.ToolMissingError; an ssh that exits non-zero is a
// *child.ExitError. A URL that does not parse, or an ssh URL whose host starts with a dash, which
// git refuses to connect to, is an *OriginError, and both raw URLs are still returned.
func (r Repo) OriginURLs(ctx context.Context) (OriginURLs, error) {
	fetchURL, err := r.line(ctx, "remote", "get-url", origin)
	if err != nil {
		return OriginURLs{}, err
	}

	pushURL, err := r.line(ctx, "remote", "get-url", "--push", origin)
	if err != nil {
		return OriginURLs{}, err
	}

	fetch, fetchErr := r.parseOrigin(ctx, fetchURL)
	push, pushErr := r.parseOrigin(ctx, pushURL)

	return OriginURLs{Fetch: fetch, Push: push, FetchURL: fetchURL, PushURL: pushURL}, errors.Join(fetchErr, pushErr)
}

// parseOrigin parses raw as ParseOrigin does, then replaces the host of an ssh URL with the host
// the ssh command git connects with reaches for it. An https URL never goes through ssh: an ssh
// command does not apply to it.
func (r Repo) parseOrigin(ctx context.Context, raw string) (Origin, error) {
	parsed, overSSH, err := parseRemote(raw)
	if err != nil || !overSSH {
		return parsed, err
	}

	// Git refuses such a host, which an ssh command would read as an option.
	if strings.HasPrefix(parsed.Host, "-") {
		return Origin{}, &OriginError{URL: raw}
	}

	parsed.Host, err = r.sshHostname(ctx, parsed.Host)
	if err != nil {
		return Origin{}, err
	}

	return parsed, nil
}

// Trunk returns origin's default branch, asked of origin.
//
// An origin that does not answer is a *RemoteError, and an answer that names no branch is
// ErrNoSymref.
func (r Repo) Trunk(ctx context.Context) (string, error) {
	args := []string{"ls-remote", "--symref", origin, "HEAD"}

	result, err := r.remote(ctx, args...)
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

	return "", fmt.Errorf("%s: %w", child.CommandLine(child.Argv(program, args)), ErrNoSymref)
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

// FastForwardTrunk moves the checked-out trunk to origin's tip of trunk, and leaves it where it is
// when it is already there.
//
// It does what `git pull --ff-only origin <trunk>` does, in two steps so that a failure says which
// side refused: the fetch from origin, whose failure is a *RemoteError, then a fast-forward to what
// it fetched. The fast-forward never merges: a trunk with commits origin's lacks stays where it
// was, and the call fails with git's *child.ExitError. HEAD must be on the trunk, since the
// fast-forward moves the branch HEAD is on: anywhere else, the call fails and moves nothing.
func (r Repo) FastForwardTrunk(ctx context.Context, trunk string) error {
	branch, onBranch, err := r.currentBranch(ctx)
	if err != nil {
		return err
	}

	if !onBranch || branch != trunk {
		return fmt.Errorf("fast-forward %s: HEAD is not on it", trunk)
	}

	fetched, err := r.Fetch(ctx, trunk)
	if err != nil {
		return err
	}

	_, err = r.Runner.Run(ctx, "merge", "--ff-only", "--quiet", fetched)

	return err
}

// Push pushes the branch name to origin under the same name and sets it as the upstream.
//
// It names the branch by its full ref on both sides, so a tag of the same name is never pushed
// in its place. It never forces.
//
// A git that exits non-zero is classed by the status it reports for the ref:
//   - a ref git or origin refused is a *PushError naming git's reason;
//   - a ref reported updated counts as pushed, though origin hung up afterwards;
//   - a ref with no status, because origin never answered or hung up before its report, is a
//     *RemoteError.
//
// Any other failure, such as a context that ended or a git missing from PATH, is returned as it is.
func (r Repo) Push(ctx context.Context, name string) error {
	ref := headsRef + name
	result, err := r.Runner.Run(ctx, "push", "--porcelain", "-u", origin, ref+":"+ref)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	if !ok {
		return err
	}

	status, reported := pushStatus(result.Stdout, ref+":"+ref)

	switch {
	case !reported:
		return &RemoteError{Err: err}
	case status.flag == pushRejected:
		return &PushError{Branch: name, Reason: status.summary, Stderr: exitErr.Stderr, Err: err}
	default:
		return nil
	}
}

// pushRejected is the flag `git push --porcelain` gives a ref it did not update.
const pushRejected = "!"

// refStatus is the status `git push --porcelain` reports for one ref.
type refStatus struct {
	flag    string
	summary string
}

// pushStatus returns the status the output of `git push --porcelain` reports for spec, the
// `<source>:<destination>` pair of refs pushed, and whether it reports one. Each such line is the
// flag, that pair and the summary, separated by tabs.
func pushStatus(stdout, spec string) (refStatus, bool) {
	for line := range strings.SplitSeq(stdout, "\n") {
		flag, rest, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}

		named, summary, _ := strings.Cut(rest, "\t")
		if named == spec {
			return refStatus{flag: flag, summary: summary}, true
		}
	}

	return refStatus{}, false
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
	if _, ok := errors.AsType[*child.ToolMissingError](err); ok {
		return err
	}

	return &RemoteError{Err: err}
}
