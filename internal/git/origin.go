package git

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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

	// sshAlias names the one-shot git alias that runs a configured ssh command line through the
	// shell git itself uses. Git runs a program git-<name> found on PATH before an alias of that
	// name, so the name is one no user has a program for.
	sshAlias = "ils-read-ssh-host"
	// unsetCode is the exit of `git config --get` for a key that is not set.
	unsetCode = 1
)

// plainHostname returns the host plain ssh connects to when asked for host, the ssh git runs when
// no ssh command is configured, read from `ssh -G`.
//
// An ssh missing from PATH is a *child.ToolMissingError and a non-zero exit is a *child.ExitError.
func plainHostname(ctx context.Context, host string) (string, error) {
	// The host comes from a URL: after "--", one that starts with a dash is never read as an option.
	args := []string{"-G", "--", host}

	result, err := invocation{program: sshProgram, installURL: sshInstallURL, args: args}.run(ctx)
	if err != nil {
		return "", err
	}

	return hostnameIn(result.Stdout, strings.Join(append([]string{sshProgram}, args...), " "))
}

// hostnameIn returns the host the `ssh -G` output stdout names, read as foldGitHubHost reads it.
// Output with no hostname line is an error naming command, the call that printed it.
func hostnameIn(stdout, command string) (string, error) {
	for line := range strings.SplitSeq(stdout, "\n") {
		if name, found := strings.CutPrefix(line, hostnameKey); found {
			return foldGitHubHost(strings.TrimSpace(name)), nil
		}
	}

	return "", fmt.Errorf("%s printed no hostname", command)
}

// foldGitHubHost returns github.com for any host under it, such as ssh.github.com, matched without
// regard to case. The dot in the suffix keeps lookalikes such as fake-github.com as they are.
func foldGitHubHost(host string) string {
	if strings.HasSuffix(strings.ToLower(host), "."+gitHubHost) {
		return gitHubHost
	}

	return host
}

// sshVariant is the kind of ssh command git takes a configured command for, which decides the
// options git passes it.
type sshVariant int

const (
	// variantAuto is a command git does not recognize: git asks it `-G` and, when that fails,
	// passes it no options, as it does for variantPlain.
	variantAuto sshVariant = iota
	// variantSSH is OpenSSH, which answers `-G`.
	variantSSH
	// variantPlain is plink, putty, tortoiseplink, or simple: git never asks any of them `-G`.
	variantPlain
)

// sshCommand is the ssh command a user configured git to connect with. At most one of its fields is
// set; when neither is, git connects with plain ssh.
type sshCommand struct {
	// line is a command line from GIT_SSH_COMMAND or core.sshCommand, which git hands to its shell.
	line string
	// program is the program GIT_SSH names, which git runs directly.
	program string
}

// configuredSSH returns the ssh command git connects with, in git's order of precedence:
// GIT_SSH_COMMAND, then core.sshCommand, then GIT_SSH. An empty value counts as unset.
func (r Repo) configuredSSH(ctx context.Context) (sshCommand, error) {
	if line, _ := r.Runner.getenv("GIT_SSH_COMMAND"); line != "" {
		return sshCommand{line: line}, nil
	}

	line, _, err := r.config(ctx, "core.sshCommand")
	if err != nil {
		return sshCommand{}, err
	}

	if line != "" {
		return sshCommand{line: line}, nil
	}

	program, _ := r.Runner.getenv("GIT_SSH")

	return sshCommand{program: program}, nil
}

// variant returns the variant git takes command for: GIT_SSH_VARIANT, else ssh.variant, else
// the basename of the command's first word, as `git help config` describes ssh.variant.
func (r Repo) variant(ctx context.Context, command sshCommand) (sshVariant, error) {
	value, set := r.Runner.getenv("GIT_SSH_VARIANT")
	if !set {
		var err error

		value, set, err = r.config(ctx, "ssh.variant")
		if err != nil {
			return variantAuto, err
		}
	}

	if set {
		if chosen := namedVariant(value); chosen != variantAuto {
			return chosen, nil
		}
	}

	word, ok := command.program, true
	if command.line != "" {
		word, ok = firstWord(command.line)
	}

	if !ok {
		return variantAuto, nil
	}

	return basenameVariant(word), nil
}

// namedVariant returns the variant a value of ssh.variant or GIT_SSH_VARIANT names. Git reads any
// value it does not know as ssh.
func namedVariant(value string) sshVariant {
	switch value {
	case "auto":
		return variantAuto
	case "plink", "putty", "tortoiseplink", "simple":
		return variantPlain
	default:
		return variantSSH
	}
}

// basenameVariant returns the variant git takes a command for from the basename of its program,
// matched without regard to case.
func basenameVariant(program string) sshVariant {
	switch strings.ToLower(filepath.Base(program)) {
	case "ssh", "ssh.exe":
		return variantSSH
	case "plink", "plink.exe", "tortoiseplink", "tortoiseplink.exe":
		return variantPlain
	default:
		return variantAuto
	}
}

// firstWord returns the first word of line as git splits a command line: single and double quotes
// group, and a backslash outside single quotes takes the next character as it is. It returns false
// when git cannot split line: a quote left open or a trailing backslash.
func firstWord(line string) (string, bool) {
	var (
		word  []byte
		quote byte
		first = true
	)

	for i := 0; i < len(line); i++ {
		c := line[i]

		switch {
		case quote == 0 && isSpace(c):
			first = false

			continue
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c

			continue
		case c == quote:
			quote = 0

			continue
		case c == '\\' && quote != '\'':
			i++
			if i == len(line) {
				return "", false
			}

			c = line[i]
		}

		if first {
			word = append(word, c)
		}
	}

	return string(word), quote == 0
}

// isSpace reports whether c is one of the ASCII spaces git splits a command line on.
func isSpace(c byte) bool {
	return strings.IndexByte(" \t\n\v\f\r", c) >= 0
}

// sshHostname returns the host the ssh command git connects with reaches when asked for host, read
// with `-G` as the command's variant allows and with plain `ssh -G` otherwise.
//
// A command of variant ssh that exits non-zero is an error naming the command as configured. A
// command of variant auto that exits non-zero is read as git reads it, as variant simple.
func (r Repo) sshHostname(ctx context.Context, host string) (string, error) {
	command, err := r.configuredSSH(ctx)
	if err != nil {
		return "", err
	}

	if command == (sshCommand{}) {
		return plainHostname(ctx, host)
	}

	variant, err := r.variant(ctx, command)
	if err != nil {
		return "", err
	}

	if variant == variantPlain {
		return plainHostname(ctx, host)
	}

	name, err := r.askSSH(ctx, command, host)
	if _, refused := errors.AsType[*child.ExitError](err); refused && variant == variantAuto {
		return plainHostname(ctx, host)
	}

	return name, err
}

// askSSH runs command with `-G host` and returns the host its output names. A command line runs
// through the shell git uses, from the top of the working tree, as a one-shot shell alias; a program
// runs directly. A non-zero exit is a *child.ExitError whose argv starts with the command as
// configured.
func (r Repo) askSSH(ctx context.Context, command sshCommand, host string) (string, error) {
	var (
		result Result
		err    error
	)

	named := command.program
	if command.line != "" {
		named = command.line
		result, err = r.Runner.Run(ctx, "-c", "alias."+sshAlias+"=!"+command.line, sshAlias, "-G", host)
	} else {
		result, err = invocation{
			program: command.program, installURL: sshInstallURL, args: []string{"-G", host},
			dir: r.Runner.Dir, env: r.Runner.Env,
		}.run(ctx)
	}

	argv := []string{named, "-G", host}
	if exitErr, ok := errors.AsType[*child.ExitError](err); ok {
		return "", &child.ExitError{Argv: argv, Code: exitErr.Code, Stderr: exitErr.Stderr}
	}

	if err != nil {
		return "", err
	}

	return hostnameIn(result.Stdout, strings.Join(argv, " "))
}

// config reads key from the repository's config. A key that is not set, which `git config --get`
// reports by exiting 1, is not an error: it returns an empty value and set false, so a caller can
// tell it from a failure of git itself.
func (r Repo) config(ctx context.Context, key string) (value string, set bool, err error) {
	value, err = r.line(ctx, "config", "--get", key)
	if exited(err, unsetCode) {
		return "", false, nil
	}

	return value, err == nil, err
}

// getenv returns the value key has in the environment git runs with, and whether it is set. A key
// set twice has its last value, as it does for the child; on Windows keys match without regard to
// case, as they do there.
func (r Runner) getenv(key string) (string, bool) {
	if r.Env == nil {
		return os.LookupEnv(key)
	}

	var (
		value string
		set   bool
	)

	for _, entry := range r.Env {
		name, entryValue, ok := strings.Cut(entry, "=")
		if ok && (name == key || runtime.GOOS == "windows" && strings.EqualFold(name, key)) {
			value, set = entryValue, true
		}
	}

	return value, set
}

// OriginURLs returns origin's fetch and push URLs, raw and parsed.
//
// The host of a URL that reaches origin over ssh is the host the ssh command git connects with
// reaches for it, read as sshHostname reads it: a Host alias of an ssh config reads as the HostName
// it stands for, and a host under github.com reads as github.com, as gh files it. An ssh missing
// from PATH is a *child.ToolMissingError, and one that exits non-zero is a *child.ExitError. A URL
// that does not parse, or an ssh URL whose host starts with a dash, which git refuses to connect
// to, is an *OriginError, and both raw URLs are still returned.
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
