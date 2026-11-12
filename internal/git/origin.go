package git

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/jeffzi/ils/internal/child"
)

// ---------------------------------------------------------------------------
// Origin URL parsing
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// ErrNoSymref reports an answer from origin that names no default branch: no `ref:` line for HEAD.
var ErrNoSymref = errors.New("origin's HEAD names no branch")

// ErrRemote reports that origin could not be reached, or did not give what was asked of it. The
// failure of the call to origin is wrapped with it.
var ErrRemote = errors.New("origin could not be reached")

// ErrRefused reports that origin answered a call and turned the caller away: it did not accept the
// credentials or the key, denied the caller the repository, or holds no such repository. Running the
// call again changes nothing until the caller fixes what origin refused. Git's *child.ExitError,
// whose Stderr holds what origin and git said, is wrapped with it.
var ErrRefused = errors.New("origin refused the call")

// PushError reports a push that git or origin refused for a ref: a non-fast-forward, a tag origin
// already holds at another commit, a hook of origin that declined it, a ref origin hides, a pre-push
// hook that failed. An origin that never answered, or that hung up before it reported on the ref, is
// an ErrRemote failure instead, and an origin that turned the caller away before the push is an
// ErrRefused failure.
type PushError struct {
	Err error
	// Ref is the full name of the refused ref, such as `refs/heads/main` or `refs/tags/v1.0.0`.
	Ref string
	// Reason is the summary git gives for the ref, such as `[rejected] (fetch first)` or
	// `[remote rejected] (pre-receive hook declined)`.
	Reason string
	// Stdout is what git printed on stdout without the porcelain report git writes at its end,
	// which leaves what a pre-push hook printed there.
	Stdout string
	// Stderr is what git printed on stderr, which holds the lines origin printed (`remote: …`) and
	// those a pre-push hook printed there.
	Stderr string
}

// Error names the refused ref, a branch by its short name and any other ref in full, followed by
// its Detail.
func (e *PushError) Error() string {
	return fmt.Sprintf("push of %s refused: %s", strings.TrimPrefix(e.Ref, BranchRefPrefix), e.Detail())
}

// Detail returns git's reason and the Output, one per line, leaving out either when it is empty.
func (e *PushError) Detail() string {
	return joinPresent(e.Reason, e.Output())
}

// Output returns the trimmed Stdout and the trimmed Stderr, one per line, leaving out either when
// it is empty.
func (e *PushError) Output() string {
	return joinPresent(strings.TrimSpace(e.Stdout), strings.TrimSpace(e.Stderr))
}

// Unwrap returns the *child.ExitError of the refused push.
func (e *PushError) Unwrap() error {
	return e.Err
}

// joinPresent joins the parts that are not empty, one per line.
func joinPresent(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(part string) bool { return part == "" }), "\n")
}

// ---------------------------------------------------------------------------
// Origin reads and fetch
// ---------------------------------------------------------------------------

const (
	// OriginRemote is the name of the remote ils pushes to and fetches from.
	OriginRemote = "origin"

	// remotesRef prefixes the remote-tracking refs, where the clone keeps what it last fetched of
	// origin's branches.
	remotesRef = "refs/remotes/" + OriginRemote + "/"

	symrefLead = "ref: "
	symrefTail = "\tHEAD"

	// absentCode is the exit of `git ls-remote --exit-code` when no ref matched.
	absentCode = 2
)

// OriginURLs holds origin's fetch and push URLs, raw and parsed.
type OriginURLs struct {
	Fetch    Origin
	Push     Origin
	FetchURL string
	PushURL  string
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
//
// When the fetch and push URLs are the same, the URL is read once, so its failure is returned once;
// when they differ, the error holds the failure of each side that failed.
func (r Repo) OriginURLs(ctx context.Context) (OriginURLs, error) {
	fetchURL, err := r.line(ctx, "remote", "get-url", OriginRemote)
	if err != nil {
		return OriginURLs{}, err
	}

	pushURL, err := r.line(ctx, "remote", "get-url", "--push", OriginRemote)
	if err != nil {
		return OriginURLs{}, err
	}

	fetch, fetchErr := r.parseOrigin(ctx, fetchURL)
	if pushURL == fetchURL {
		return OriginURLs{Fetch: fetch, Push: fetch, FetchURL: fetchURL, PushURL: pushURL}, fetchErr
	}

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
// An origin that does not answer is an ErrRemote failure, and an answer that names no branch is
// ErrNoSymref.
func (r Repo) Trunk(ctx context.Context) (string, error) {
	args := []string{"ls-remote", "--symref", OriginRemote, "HEAD"}

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

		if branch, isBranch := strings.CutPrefix(ref, BranchRefPrefix); isBranch {
			return branch, nil
		}
	}

	return "", fmt.Errorf("%s: %w", child.CommandLine(child.Argv(program, args)), ErrNoSymref)
}

// RemoteBranch reports whether origin has the branch name, and its sha there when it does.
//
// An origin that does not answer is an ErrRemote failure, never an absent branch.
func (r Repo) RemoteBranch(ctx context.Context, name string) (sha string, found bool, err error) {
	ref := BranchRefPrefix + name

	result, err := r.remote(ctx, "ls-remote", "--exit-code", OriginRemote, ref)
	if exited(err, absentCode) {
		return "", false, nil
	}

	if err != nil {
		return "", false, err
	}

	// The pattern also matches a longer ref that ends with it, so only the exact ref counts.
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		if sha, listed, ok := strings.Cut(line, "\t"); ok && listed == ref {
			return sha, true, nil
		}
	}

	return "", false, nil
}

// LastFetchedTrunk returns origin's default branch as the clone last learned it, from origin's HEAD
// among the remote-tracking refs, without asking origin; found is false when the clone holds no
// such ref.
func (r Repo) LastFetchedTrunk(ctx context.Context) (trunk string, found bool, err error) {
	return r.symbolicRef(ctx, remotesRef+"HEAD", remotesRef)
}

// TrunkOrLastFetched returns origin's default branch, asked of origin under ctx, or as the clone
// last fetched it when origin does not answer before ctx ends, or turns the caller away; found is
// false when origin gave no trunk and the clone holds no such ref.
//
// The read of the last fetch runs past ctx's end, so a deadline that cut the call to origin still
// leaves the clone's own answer. An answer from origin that names no branch is ErrNoSymref, never
// the last fetch.
func (r Repo) TrunkOrLastFetched(ctx context.Context) (trunk string, found bool, err error) {
	trunk, err = r.Trunk(ctx)
	if err == nil {
		return trunk, true, nil
	}

	if !errors.Is(err, ErrRemote) && !errors.Is(err, ErrRefused) {
		return "", false, err
	}

	return r.LastFetchedTrunk(context.WithoutCancel(ctx))
}

// LastFetched returns the sha of origin's branch name as last fetched, without asking origin;
// found is false when the clone holds no remote-tracking ref for it.
func (r Repo) LastFetched(ctx context.Context, name string) (sha string, found bool, err error) {
	return r.optionalLine(ctx, "rev-parse", "--verify", "--quiet", remotesRef+name)
}

// BranchesTaking returns the branches that take name, locally and on origin, as git lists them: one
// of that exact name, one whose name is a path prefix of it (fix for fix/x), and one under it (fix/x
// for fix), none of which git can hold beside a branch called name. A branch whose name only ends
// with name (team/fix for fix) does not take it.
//
// Origin is asked at the time of the call: a branch deleted on origin that the clone still knows
// does not take the name, and one pushed since the last fetch does. An origin that does not answer
// is an ErrRemote failure, never an empty list.
func (r Repo) BranchesTaking(ctx context.Context, name string) (local, onOrigin []string, err error) {
	localBranches, err := r.localBranches(ctx, nil)
	if err != nil {
		return nil, nil, err
	}

	originRefs, err := r.remote(ctx, "ls-remote", "--heads", OriginRemote)
	if err != nil {
		return nil, nil, err
	}

	for _, branch := range localBranches {
		if takes(branch, name) {
			local = append(local, branch)
		}
	}

	for line := range strings.SplitSeq(originRefs.Stdout, "\n") {
		_, ref, _ := strings.Cut(line, "\t")
		if branch, isBranch := strings.CutPrefix(ref, BranchRefPrefix); isBranch && takes(branch, name) {
			onOrigin = append(onOrigin, branch)
		}
	}

	return local, onOrigin, nil
}

// takes reports whether branch takes name: whether it is name, or one of the two is a path prefix of
// the other.
func takes(branch, name string) bool {
	return branch == name || strings.HasPrefix(name, branch+"/") || strings.HasPrefix(branch, name+"/")
}

const (
	tagsRef = "refs/tags/"
	// peeledSuffix ends the ref `git ls-remote` lists for the commit an annotated tag points to,
	// after the line for the tag object itself.
	peeledSuffix = "^{}"
)

// Tag is a tag and the sha of the commit it tags.
type Tag struct {
	Name string
	SHA  string
}

// OriginTags returns origin's tags, asked of origin, in the order git lists them, each with the
// commit it tags: an annotated tag gives the commit its tag object points to, never the tag object.
//
// An origin that does not answer is an ErrRemote failure.
func (r Repo) OriginTags(ctx context.Context) ([]Tag, error) {
	result, err := r.remote(ctx, "ls-remote", "--tags", OriginRemote)
	if err != nil {
		return nil, err
	}

	var tags []Tag

	index := map[string]int{}

	for line := range strings.SplitSeq(result.Stdout, "\n") {
		sha, ref, _ := strings.Cut(line, "\t")

		name, isTag := strings.CutPrefix(ref, tagsRef)
		if !isTag {
			continue
		}

		name, isPeeled := strings.CutSuffix(name, peeledSuffix)

		i, seen := index[name]
		switch {
		case !seen:
			index[name] = len(tags)
			tags = append(tags, Tag{Name: name, SHA: sha})
		case isPeeled:
			tags[i].SHA = sha
		}
	}

	return tags, nil
}

const (
	refsPrefix = "refs/"

	// fetchedRefs prefixes the ref Fetch stores a fetch in until it has read it.
	fetchedRefs = "refs/ils/fetched/"
)

// Fetch fetches one ref from origin, a branch name or a full ref, and returns the fetched sha.
//
// A branch name is fetched as the branch, never as a tag of the same name. The fetch is stored in a
// ref under refs/ils/fetched/ named for this call alone, read back from there and deleted, whether
// the fetch or the read succeeds or fails: FETCH_HEAD is shared with every other fetch in the
// worktree, and any fixed name with every fetch in the repository, another ils run in another
// worktree included, which can lock, rewrite or delete them before they are read. A fetch that
// fails is an ErrRefused failure when origin turned the caller away, and an ErrRemote failure
// otherwise.
//
// A fetched branch also moves its remote-tracking ref, refs/remotes/origin/<branch>, to the fetched
// sha, even when the clone's remote.origin.fetch leaves the branch out: every later read of origin's
// branch goes through that ref, and must see the tip this fetch returned. The fetch itself moves no
// remote-tracking ref.
func (r Repo) Fetch(ctx context.Context, ref string) (string, error) {
	if !strings.HasPrefix(ref, refsPrefix) {
		ref = BranchRefPrefix + ref
	}

	fetched := fetchedRefs + rand.Text() + "/" + strings.TrimPrefix(ref, refsPrefix)

	// --no-tags: a fetch that stores a ref also stores every tag of origin on the commits it brought,
	// tags the user never asked for. --refmap=: git would otherwise also move the remote-tracking ref
	// remote.origin.fetch maps the ref to, only if it still holds the sha git read before the fetch,
	// so a concurrent fetch of the same branch that moves it first fails this one.
	var sha string

	_, err := r.remote(ctx, "fetch", "--no-tags", "--refmap=", OriginRemote, "+"+ref+":"+fetched)
	if err == nil {
		sha, err = r.line(ctx, "rev-parse", "--verify", fetched)
	}

	// The ref has served its purpose once read, or once the fetch or the read failed: a fetch can
	// store it and fail after, and keeping it would leave one ref per fetch in the user's
	// repository. Deleting a ref that is not there succeeds, and the delete runs past an ended ctx,
	// as the cleanup of the run it ended.
	_, deleteErr := r.Runner.Run(context.WithoutCancel(ctx), "update-ref", "-d", fetched)
	if err = errors.Join(err, deleteErr); err != nil {
		return "", err
	}

	if branch, isBranch := strings.CutPrefix(ref, BranchRefPrefix); isBranch {
		if _, err := r.Runner.Run(ctx, "update-ref", remotesRef+branch, sha); err != nil {
			return "", err
		}
	}

	return sha, nil
}

// pullRefs prefixes the refs GitHub keeps on origin for its pull requests, each under its number.
const pullRefs = "refs/pull/"

// FetchPullHead fetches from origin the head GitHub keeps of the pull request numbered number, as
// Fetch fetches a ref, and returns its sha.
func (r Repo) FetchPullHead(ctx context.Context, number int) (string, error) {
	return r.Fetch(ctx, pullRefs+strconv.Itoa(number)+"/head")
}

// FetchTag fetches the tag name from origin, as Fetch fetches a ref, and returns its sha.
func (r Repo) FetchTag(ctx context.Context, name string) (string, error) {
	return r.Fetch(ctx, tagsRef+name)
}

// FastForwardTrunk moves the checked-out trunk to origin's tip of trunk, and leaves it where it is
// when it is already there.
//
// It does what `git pull --ff-only origin <trunk>` does, in two steps so that a failure says which
// side refused: the fetch from origin, whose failure is an ErrRefused failure when origin turned the
// caller away and an ErrRemote failure otherwise, then a fast-forward to what it fetched. The
// fast-forward never merges: a trunk with commits origin's lacks stays where it was, and the call
// fails with git's *child.ExitError. HEAD must be on the trunk, since the fast-forward moves the
// branch HEAD is on: anywhere else, the call fails and moves nothing.
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

// ---------------------------------------------------------------------------
// Push and its classification
// ---------------------------------------------------------------------------

// Push pushes the commit sha to origin's branch name. Before the push, it sets that branch as the
// upstream of the local branch name, as `git push -u` would after it, so a failure to set it means
// that nothing was pushed.
//
// It pushes sha rather than the local branch, so a commit that reaches the branch while the push
// runs is never pushed in its place. It names origin's branch by its full ref, so a tag of the same
// name is never updated in its place. It never forces.
//
// A git that exits non-zero is classed by the status it reports for the ref:
//   - a ref git or origin refused is a *PushError naming git's reason;
//   - a ref reported updated counts as pushed, though origin hung up afterwards;
//   - a ref with no status, because origin never answered or hung up before its report, is an
//     ErrRefused failure when origin turned the caller away, and an ErrRemote failure otherwise;
//   - a ref with no status from a git that refused the push itself, such as for a pre-push hook
//     that failed, is a *PushError with no reason.
//
// Any other failure, such as a context that ended or a git missing from PATH, is returned as it is.
func (r Repo) Push(ctx context.Context, name, sha string) error {
	if err := r.setUpstream(ctx, name); err != nil {
		return err
	}

	return r.push(ctx, sha, BranchRefPrefix+name)
}

// setUpstream sets origin's branch name as the upstream of the local branch name, as `git push -u`
// does, which git skips for a push whose source is a commit rather than a branch.
func (r Repo) setUpstream(ctx context.Context, name string) error {
	for _, setting := range [][2]string{{"remote", OriginRemote}, {"merge", BranchRefPrefix + name}} {
		if _, err := r.Runner.Run(ctx, "config", "branch."+name+"."+setting[0], setting[1]); err != nil {
			return fmt.Errorf("set the upstream of %s: %w", name, err)
		}
	}

	return nil
}

// PushTag pushes the local tag name to origin under the same name, and nothing else: it names the
// tag by its full ref on both sides and turns off `push.followTags`. It never forces, so a tag
// origin already holds at another commit is refused. Its failures are classed as Push classes them.
func (r Repo) PushTag(ctx context.Context, name string) error {
	ref := tagsRef + name

	return r.push(ctx, ref, ref, "--no-follow-tags")
}

// push pushes source to origin's ref with git's flags, classing a git that exits non-zero as Push
// documents.
func (r Repo) push(ctx context.Context, source, ref string, flags ...string) error {
	spec := source + ":" + ref
	args := slices.Concat([]string{"push", "--porcelain"}, flags, []string{OriginRemote, spec})
	result, err := r.Runner.Run(ctx, args...)

	exitErr, ok := errors.AsType[*child.ExitError](err)
	if !ok {
		return err
	}

	hook, status, reported := splitPushReport(result.Stdout, spec)

	switch {
	// A push dies with fatalCode when it cannot reach origin, origin hangs up or origin turns the
	// caller away; one git refuses before it talks to origin, such as for a pre-push hook that
	// failed, exits 1.
	case !reported && exitErr.Code == fatalCode:
		return remoteFailure(err)
	case !reported || status.flag == pushRejected:
		return &PushError{
			Ref:    ref,
			Reason: status.summary,
			Stdout: hook,
			Stderr: exitErr.Stderr,
			Err:    err,
		}
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

// reportLead starts the line of `git push --porcelain` that opens its report, naming the remote.
const reportLead = "To "

// reportEnd is the last line of the report of `git push --porcelain`, which git leaves out when
// origin hung up after the report.
const reportEnd = "Done"

// splitPushReport splits the stdout of `git push --porcelain` for spec, the `<source>:<destination>`
// pair of refs pushed, into what precedes the report git writes at its end once origin answered and
// the status that report gives spec. The report is a `To <url>` line, then one status line (the
// flag, the refs and the summary, separated by tabs) for each ref the push sent (spec, and any tag
// that `push.followTags` adds), then `Done`, which git leaves out when origin hung up after its
// report. The order of the status lines is not relied on: spec is looked up in every line after the
// `To` line.
//
// A pre-push hook runs before git talks to origin, so what precedes the report is the hook's; a
// push git refused before it reached origin has no report, so all of its stdout is the hook's and
// reported is false. The hook shares git's stdout, so when its output does not end in a newline,
// the `To` line starts on the hook's last line, which is split there.
//
// The report starts at the last `To` that a status line of spec follows: a hook line shaped like a
// line of the report, even the status line of spec itself, stays in hook and never decides the
// status when no `To` precedes it, and a later line that only holds `To` does not hide the report.
// Stdout alone cannot tell a block that a refusing hook printed last, a `To` line followed by the
// status line of spec, from git's own report, so that one is read as git's.
func splitPushReport(stdout, spec string) (hook string, status refStatus, reported bool) {
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if lines[len(lines)-1] == reportEnd {
		lines = lines[:len(lines)-1]
	}

	for leadAt, lead := range slices.Backward(lines) {
		// A URL holding `To ` must not split a line git started itself.
		hookEnd := 0
		if !strings.HasPrefix(lead, reportLead) {
			hookEnd = strings.LastIndex(lead, reportLead)
		}

		if hookEnd < 0 {
			continue
		}

		for _, line := range lines[leadAt+1:] {
			if status, reported = statusFor(line, spec); reported {
				hook = strings.Join(append(lines[:leadAt:leadAt], lead[:hookEnd]), "\n")
				return hook, status, true
			}
		}
	}

	return stdout, refStatus{}, false
}

// statusFor returns the status that line reports for spec, and whether line is the status line
// `git push --porcelain` gives spec: the flag, spec and the summary, separated by tabs.
func statusFor(line, spec string) (refStatus, bool) {
	fields := strings.Split(line, "\t")
	if len(fields) != 3 || fields[0] == "" || fields[1] != spec {
		return refStatus{}, false
	}

	return refStatus{flag: fields[0], summary: fields[2]}, true
}

// remote runs a git call that talks to origin, whose failure is classed as remoteFailure classes it.
func (r Repo) remote(ctx context.Context, args ...string) (Result, error) {
	result, err := r.Runner.Run(ctx, args...)
	if err != nil {
		return Result{}, remoteFailure(err)
	}

	return result, nil
}

// remoteFailure wraps the failure of a call to origin: a git that died of a fatal error saying
// origin turned the caller away is an ErrRefused failure, and any other failure an ErrRemote
// failure. A missing git program is left as it is: that failure says nothing about origin.
func remoteFailure(err error) error {
	if _, ok := errors.AsType[*child.ToolMissingError](err); ok {
		return err
	}

	if fatalSaying(err, refusal) {
		return fmt.Errorf("%w: %w", ErrRefused, err)
	}

	return fmt.Errorf("%w: %w", ErrRemote, err)
}

// refusalPhrases returns the lowercase words of a stderr line that says origin turned the caller
// away. ssh's own refusal lists the methods it tried, `Permission denied (password)`, which a
// connect or a local file that the system denies never does; GitHub's answer to HTTP credentials it
// did not accept stays in English whatever language git speaks.
func refusalPhrases() []string {
	return []string{
		"permission denied (",
		"invalid username or ",
		"authentication failed for ",
		"repository not found",
		"the requested url returned error: 403",
	}
}

// refusal reports whether stderr, what a failed call to origin printed, says origin turned the
// caller away: it holds, regardless of case, what origin or git prints for an ssh key or HTTP
// credentials origin did not accept, a caller denied the repository, or a repository origin does not
// hold, as GitHub says it and as git says it of an HTTP 404. A failure to connect never prints one,
// and neither does a local path that holds no repository.
func refusal(stderr string) bool {
	for line := range strings.Lines(strings.ToLower(stderr)) {
		line = strings.TrimSpace(line)

		// Git's own words for an HTTP 404: `fatal: repository '<url>' not found`.
		if strings.HasPrefix(line, "fatal: repository '") && strings.HasSuffix(line, "' not found") {
			return true
		}

		if slices.ContainsFunc(refusalPhrases(), func(phrase string) bool { return strings.Contains(line, phrase) }) {
			return true
		}
	}

	return false
}
