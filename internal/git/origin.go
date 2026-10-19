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
	parsed, _, err := parseRemote(raw)

	return parsed, err
}

// parseRemote parses raw as ParseOrigin does and also reports whether the URL reaches origin over
// ssh.
func parseRemote(raw string) (parsed Origin, overSSH bool, err error) {
	host, path, overSSH, ok := splitOrigin(raw)
	if !ok {
		return Origin{}, false, &OriginError{URL: raw}
	}

	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")

	owner, name, found := strings.Cut(path, "/")
	if !found || host == "" || owner == "" || name == "" || strings.Contains(name, "/") {
		return Origin{}, false, &OriginError{URL: raw}
	}

	return Origin{Host: host, Owner: owner, Name: name}, overSSH, nil
}

// splitOrigin returns the host and the owner/name path of a remote URL, and whether the URL reaches
// origin over ssh.
func splitOrigin(raw string) (host, path string, overSSH, ok bool) {
	overHTTPS := strings.HasPrefix(raw, httpsPrefix)
	if overHTTPS || strings.HasPrefix(raw, sshPrefix) {
		parsed, err := url.Parse(raw)
		if err != nil {
			return "", "", false, false
		}

		return parsed.Hostname(), strings.TrimPrefix(parsed.Path, "/"), !overHTTPS, true
	}

	rest, found := strings.CutPrefix(raw, shortSSHPrefix)
	if !found {
		return "", "", false, false
	}

	host, path, ok = strings.Cut(rest, ":")
	if strings.Contains(host, "/") {
		return "", "", false, false
	}

	return host, path, true, ok
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

const (
	sshProgram = "ssh"
	// sshInstallURL is the home of OpenSSH, whose client ships with every operating system ils runs on.
	sshInstallURL = "https://www.openssh.com"
	// hostnameKey leads the line of `ssh -G` that names the host a connection goes to.
	hostnameKey = "hostname "
	// gitHubHost is the host gh files every host under it as.
	gitHubHost = "github.com"
)

// SSH reads, with `ssh -G`, the host an ssh connection goes to, which an ssh config can give
// another name.
type SSH struct {
	// Config, when not empty, is the one config file ssh reads, in place of the user's and the
	// system's.
	Config string
}

// hostname returns the host ssh connects to when asked for host, with every host under github.com
// read as github.com, as gh files it.
//
// An ssh missing from PATH is a *child.ToolMissingError and a non-zero exit is a *child.ExitError.
func (s SSH) hostname(ctx context.Context, host string) (string, error) {
	args := []string{"-G"}
	if s.Config != "" {
		args = append(args, "-F", s.Config)
	}

	// The host comes from a URL: after "--", one that starts with a dash is never read as an option.
	args = append(args, "--", host)

	result, err := invocation{program: sshProgram, installURL: sshInstallURL, args: args}.run(ctx)
	if err != nil {
		return "", err
	}

	for line := range strings.SplitSeq(result.Stdout, "\n") {
		if name, found := strings.CutPrefix(line, hostnameKey); found {
			return foldGitHubHost(strings.TrimSpace(name)), nil
		}
	}

	return "", fmt.Errorf("%s printed no hostname", strings.Join(append([]string{sshProgram}, args...), " "))
}

// foldGitHubHost returns github.com for any host under it, such as ssh.github.com, matched without
// regard to case. The dot in the suffix keeps lookalikes such as fake-github.com as they are.
func foldGitHubHost(host string) string {
	if strings.HasSuffix(strings.ToLower(host), "."+gitHubHost) {
		return gitHubHost
	}

	return host
}

// OriginURLs returns origin's fetch and push URLs, raw and parsed.
//
// The host of a URL that reaches origin over ssh is the host ssh connects to for it, so a Host
// alias of the user's ssh config reads as the HostName it stands for, and a host under github.com
// reads as github.com, as gh files it. An ssh that fails is a *child.ToolMissingError or a
// *child.ExitError. A URL that does not parse is an *OriginError, and both raw URLs are still
// returned.
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
// ssh connects to for it. An https URL never goes through ssh: an ssh config does not apply to it.
func (r Repo) parseOrigin(ctx context.Context, raw string) (Origin, error) {
	parsed, overSSH, err := parseRemote(raw)
	if err != nil || !overSSH {
		return parsed, err
	}

	parsed.Host, err = r.SSH.hostname(ctx, parsed.Host)
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
	if _, ok := errors.AsType[*child.ToolMissingError](err); ok {
		return err
	}

	return &RemoteError{Err: err}
}
