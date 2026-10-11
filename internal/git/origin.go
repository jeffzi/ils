package git

import (
	"fmt"
	"net/url"
	"strings"
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
