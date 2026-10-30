// Package ghtest stands the test binary in for gh, answering from a script and recording each argv.
package ghtest

import (
	"bufio"
	"bytes"
	"context"
	legacy "encoding/json"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/gittest"
)

const (
	// scriptEnv names the script file; set, it makes the test binary act as gh.
	scriptEnv = "ILS_GHTEST_SCRIPT"
	// recordEnv names the file the fake appends each argv to.
	recordEnv = "ILS_GHTEST_RECORD"
	// holdEnv names the local address the fake dials once it starts holding an answer open.
	holdEnv = "ILS_GHTEST_HOLD"

	// acknowledgement is the byte the test process sends back once it has checked a hold; the fake
	// waits for it before it holds its answer open.
	acknowledgement byte = '\n'
)

// The exit codes of a fake that did not answer from its script. Neither is one of gh's own exits
// (0, 1, 2, 4 and 8), which internal/github reads as answers, nor one of gittest's, so a test can
// tell a fake that failed from a gh that answered.
const (
	// BrokenCode is the exit code of a fake that could not read its script or write its record.
	BrokenCode = 122
	// UnscriptedCode is the exit code of a fake asked for an argv its script does not hold.
	UnscriptedCode = 123
)

// ---------------------------------------------------------------------------
// Scripting
// ---------------------------------------------------------------------------

// Answer is what the fake prints and the code it exits with.
type Answer struct {
	Stdout string
	Stderr string
	// Delay is how long the fake holds the call open before it prints and exits.
	Delay time.Duration
	Code  int
}

// installed is what the fake prints for its version when the script holds no answer to
// VersionArgs: a release every command accepts, so that a script need not answer the version read.
const installed = "gh version " + github.MinimumVersion + "\n"

// Any stands in a scripted argv for one argument the fake accepts whatever it holds: a value the
// code under test writes out by hand, such as a field list or a limit, that no test should copy.
// No real argument can equal it, since an argument cannot hold a NUL byte.
const Any = "\x00any"

// fileHolding leads a scripted argument that FileHolding wrote; like Any, no real argument can
// start with it, since an argument cannot hold a NUL byte.
const fileHolding = "\x00file\x00"

// FileHolding stands in a scripted argv for one argument naming a file that holds exactly
// content when the call is made: the file the code under test writes for gh to read, such as
// release notes, whose path no test can know.
//
// The fake checks the file when it answers. Matched after the call, against a recorded argv,
// it fails once the code under test has removed the file.
func FileHolding(content string) string {
	return fileHolding + content
}

// Matches reports whether argv is a call pattern scripts: as long as pattern, and equal to it
// wherever pattern does not hold Any or an argument FileHolding wrote, which matches the path of
// a file holding its content.
func Matches(pattern, argv []string) bool {
	return slices.EqualFunc(pattern, argv, func(want, got string) bool {
		if content, ok := strings.CutPrefix(want, fileHolding); ok {
			return fileHolds(got, content)
		}

		return want == Any || want == got
	})
}

func fileHolds(path, content string) bool {
	//nolint:gosec // G304: the path is one the code under test handed gh, read to check what it wrote.
	held, err := os.ReadFile(path)

	return err == nil && string(held) == content
}

// Call scripts the answer to one argv, given to every call of it.
type Call struct {
	// Args is the argv after gh, matched by Matches.
	Args   []string
	Answer Answer
}

// Says scripts an exit 0 that prints stdout as the one answer to args.
func Says(stdout string, args ...string) Call {
	return Exits(Answer{Stdout: stdout}, args...)
}

// Exits scripts answer as the one answer to args.
func Exits(answer Answer, args ...string) Call {
	return Call{Args: args, Answer: answer}
}

// LoggedOut is what `gh auth status` answers when no host is logged in.
func LoggedOut() Answer {
	return Answer{Stderr: "You are not logged into any GitHub hosts.\n", Code: 1}
}

// ServerError is what a gh call answers when GitHub fails it with HTTP 500.
func ServerError() Answer {
	return Answer{Stderr: "gh: Server Error (HTTP 500)\n", Code: 1}
}

// ---------------------------------------------------------------------------
// Argv of gh calls
// ---------------------------------------------------------------------------

// VersionArgs is the argv of the read of gh's version.
func VersionArgs() []string {
	return []string{"--version"}
}

// LoginArgs is the argv of the login check on host.
func LoginArgs(host string) []string {
	return []string{"auth", "status", "--hostname", host}
}

// RepoViewArgs is the argv pattern of the read of a repository's identity; slug is host/owner/name.
func RepoViewArgs(slug string) []string {
	return []string{"repo", "view", slug, "--json", Any}
}

// ListArgs is the argv pattern of the list of the pull requests from the branch head, whatever their state.
func ListArgs(slug, head string) []string {
	return []string{
		"pr", "list", "--repo", slug, "--head", head, "--state", "all", "--limit", Any, "--json", Any,
	}
}

// CreateArgs is the argv that opens a pull request titled title from head against base, with no body.
func CreateArgs(slug, title, base, head string) []string {
	return []string{"pr", "create", "--repo", slug, "--title", title, "--body", "", "--base", base, "--head", head}
}

// HeadArgs is the argv pattern of the read of pull request number's head commit and merge state.
func HeadArgs(slug string, number int) []string {
	return []string{"pr", "view", "--repo", slug, strconv.Itoa(number), "--json", Any}
}

// LandArgs is the argv that squash-merges the pull request landing names, with its subject and
// body as the commit message, when its head is still landing's HeadSHA.
func LandArgs(slug string, landing forge.Landing) []string {
	return []string{
		"pr", "merge", strconv.Itoa(landing.Number), "--repo", slug, "--squash", "--match-head-commit", landing.HeadSHA,
		"--subject", landing.Subject, "--body", landing.Body,
	}
}

// DeleteBranchArgs is the argv that deletes a branch of the repository owner/name on host;
// branchPath is the branch as it stands in a URL path, each of its segments escaped.
func DeleteBranchArgs(host, repo, branchPath string) []string {
	return []string{"api", "--hostname", host, "-X", "DELETE", "repos/" + repo + "/git/refs/heads/" + branchPath}
}

// RulesArgs is the argv of the read of the rules on a branch of the repository owner/name on host;
// branchPath is the branch as it stands in a URL path, each of its segments escaped.
func RulesArgs(host, repo, branchPath string) []string {
	return []string{"api", "--hostname", host, "--paginate", "repos/" + repo + "/rules/branches/" + branchPath}
}

// ChecksArgs is the argv pattern of the read of pull request number's checks.
func ChecksArgs(slug string, number int) []string {
	return []string{"pr", "checks", "--repo", slug, strconv.Itoa(number), "--json", Any}
}

// RunsArgs is the argv pattern of the list of the workflow runs on the commit sha.
func RunsArgs(slug, sha string) []string {
	return []string{"run", "list", "--repo", slug, "--commit", sha, "--limit", Any, "--json", Any}
}

// LogArgs is the argv of the read of the failed log of the workflow run numbered run.
func LogArgs(slug, run string) []string {
	return []string{"run", "view", "--repo", slug, run, "--log-failed"}
}

// ReleaseViewArgs is the argv pattern of the read of the GitHub release of tag.
func ReleaseViewArgs(slug, tag string) []string {
	return []string{"release", "view", tag, "--repo", slug, "--json", Any}
}

// ReleaseCreateArgs is the argv pattern that publishes the GitHub release of tag, which origin
// must already hold, titled with tag, with notes as its body, and marked as a pre-release when
// prerelease is set.
func ReleaseCreateArgs(slug, tag, notes string, prerelease bool) []string {
	args := []string{"release", "create", tag, "--repo", slug, "--verify-tag", "--title", tag, "--notes-file", FileHolding(notes)}
	if prerelease {
		args = append(args, "--prerelease")
	}

	return args
}

// MessageArgs is the argv pattern of the read of pull request number's title, body, and the body GitHub
// would compose for its squash merge, in the repository owner/name on host.
func MessageArgs(host, owner, name string, number int) []string {
	return []string{
		"api", "graphql", "--hostname", host,
		"-F", "number=" + strconv.Itoa(number), "-f", "owner=" + owner, "-f", "name=" + name, "-f", Any,
	}
}

// ---------------------------------------------------------------------------
// Canned JSON answers
// ---------------------------------------------------------------------------

// workflow is the workflow every scripted check and run belongs to.
const workflow = "CI"

// RepoViewJSON is `gh repo view`'s answer for a repository that is a fork or not, on which the
// viewer holds permission, and whose settings allow squash merges.
func RepoViewJSON(fork bool, permission string) string {
	return RepoViewSquashJSON(fork, permission, true)
}

// RepoViewSquashJSON is RepoViewJSON for a repository whose settings allow squash merges or not.
func RepoViewSquashJSON(fork bool, permission string, squash bool) string {
	return `{"isFork":` + strconv.FormatBool(fork) + `,"viewerPermission":` + quoted(permission) +
		`,"squashMergeAllowed":` + strconv.FormatBool(squash) + `}`
}

// MessageJSON is the message read's answer for a pull request titled title with body, for which
// GitHub would compose mergeBody as the body of its squash merge.
func MessageJSON(title, body, mergeBody string) string {
	return `{"data":{"repository":{"pullRequest":{"title":` + quoted(title) + `,"body":` + quoted(body) +
		`,"viewerMergeBodyText":` + quoted(mergeBody) + `}}}}`
}

// quoted is s as a JSON string.
func quoted(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("encode %q as JSON: %v", s, err))
	}

	return string(encoded)
}

// Change is one pull request as `gh pr list` prints it.
type Change struct {
	URL   string
	Title string
	Base  string
	SHA   string
	// State is GitHub's word for where the pull request stands: OPEN, CLOSED or MERGED.
	State  string
	Number int
	Draft  bool
	// CrossRepository says the head branch lives in another repository, as a fork's does.
	CrossRepository bool
}

// ChangeJSON is one entry of `gh pr list`'s answer, with every field ListArgs asks for.
func ChangeJSON(change Change) string {
	return `{"number":` + strconv.Itoa(change.Number) + `,"url":` + quoted(change.URL) + `,"title":` + quoted(change.Title) +
		`,"baseRefName":` + quoted(change.Base) + `,"headRefOid":` + quoted(change.SHA) +
		`,"isDraft":` + strconv.FormatBool(change.Draft) +
		`,"isCrossRepository":` + strconv.FormatBool(change.CrossRepository) + `,"state":` + quoted(change.State) + `}`
}

// HeadJSON is `gh pr view`'s answer for a pull request at sha that is not a draft.
func HeadJSON(sha, mergeable, status, review string) string {
	return headAnswer{sha: sha, mergeable: mergeable, status: status, review: review}.json()
}

// HeadDraftJSON is `gh pr view`'s answer for a draft pull request at sha that no review decides.
func HeadDraftJSON(sha, mergeable, status string) string {
	return headAnswer{sha: sha, mergeable: mergeable, status: status, draft: true}.json()
}

// headAnswer is what `gh pr view` reports of a pull request's head.
type headAnswer struct {
	sha, mergeable, status, review string
	draft                          bool
}

// json is `gh pr view`'s answer for h.
func (h headAnswer) json() string {
	return `{"headRefOid":` + quoted(h.sha) + `,"mergeable":` + quoted(h.mergeable) + `,"mergeStateStatus":` +
		quoted(h.status) + `,"isDraft":` + strconv.FormatBool(h.draft) + `,"reviewDecision":` + quoted(h.review) + `}`
}

// CheckJSON is one entry of `gh pr checks`'s answer, for a check of the workflow CI.
func CheckJSON(name, bucket, link string) string {
	return `{"name":` + quoted(name) + `,"bucket":` + quoted(bucket) + `,"workflow":` + quoted(workflow) +
		`,"link":` + quoted(link) + `}`
}

// RunJSON is one entry of `gh run list`'s answer, for a run named name of the workflow CI.
func RunJSON(status, name string) string {
	return `{"status":` + quoted(status) + `,"name":` + quoted(name) + `,"workflowName":` + quoted(workflow) + `}`
}

// JSONArray is a JSON array of items, each already JSON, such as the entries ChangeJSON, CheckJSON
// and RunJSON return.
func JSONArray(items ...string) string {
	return "[" + strings.Join(items, ",") + "]"
}

// ---------------------------------------------------------------------------
// Fake setup and serving
// ---------------------------------------------------------------------------

// Fake is the handle on a scripted gh.
type Fake struct {
	tb      testing.TB
	holding <-chan struct{}
	record  string
}

// Main makes the test binary act as gh when Setup arranged for it, and reports whether it did.
//
// A package's TestMain calls it before anything else and, when served is true, exits with code
// without running a test.
func Main() (code int, served bool) {
	script := os.Getenv(scriptEnv)
	if script == "" {
		return 0, false
	}

	places := servePlaces{script: script, record: os.Getenv(recordEnv), holdAddr: os.Getenv(holdEnv)}

	return serve(context.Background(), places, os.Args[1:]), true
}

// Setup points GH_PATH at the test binary for the rest of the test and scripts its answers.
//
// It sets process environment, so the test cannot be parallel.
func Setup(tb testing.TB, script ...Call) *Fake {
	tb.Helper()

	// os.Executable carries the .exe suffix on Windows, which GH_PATH needs there.
	exe, err := os.Executable()
	if err != nil {
		tb.Fatalf("ghtest: find the test binary: %v", err)
	}

	encoded, err := json.Marshal(script, legacy.FormatDurationAsNano(true))
	if err != nil {
		tb.Fatalf("ghtest: encode the script: %v", err)
	}

	dir := tb.TempDir()
	scriptPath := filepath.Join(dir, "script.json")
	if err := os.WriteFile(scriptPath, encoded, 0o600); err != nil {
		tb.Fatalf("ghtest: write the script: %v", err)
	}

	record := filepath.Join(dir, "record.jsonl")
	holdAddr, holding := listenForHolds(tb, record)
	fake := &Fake{tb: tb, holding: holding, record: record}

	tb.Setenv("GORACE", gittest.RaceSetting())
	tb.Setenv("GH_PATH", exe)
	tb.Setenv(scriptEnv, scriptPath)
	tb.Setenv(recordEnv, fake.record)
	tb.Setenv(holdEnv, holdAddr)

	return fake
}

// hold is what the fake sends the test process before it holds its answer to Args open.
type hold struct {
	Args []string `json:"args"`
	// Earlier is how many calls with Args the record held before this one.
	Earlier int `json:"earlier"`
}

// listenForHolds returns the address of a local server that the fake dials each time it starts
// holding an answer open, and a channel that closes on the first such dial. The fake runs in a
// child process, so the dial is how the moment reaches the test process.
//
// The fake waits for the server's acknowledgement before it holds its answer, and the server
// sends it only once it has checked the held call against record and closed the channel. So the
// channel is closed before the held call can end, and a fake that dials before recording the
// call fails the test, whatever the scheduling.
func listenForHolds(tb testing.TB, record string) (addr string, holding <-chan struct{}) {
	tb.Helper()

	firstHold := make(chan struct{})

	announced := sync.OnceFunc(func() { close(firstHold) })

	addr = gittest.Serve(tb, func(conn net.Conn) { acknowledgeHold(tb, conn, record, announced) })

	return addr, firstHold
}

// acknowledgeHold reads the hold the fake sends over conn, fails the test unless record already
// holds the call, calls announced, and only then lets the fake go on.
func acknowledgeHold(tb testing.TB, conn net.Conn, record string, announced func()) {
	// The acknowledgement already reached the fake, or the fake is gone and has nothing left to
	// read: a failed close loses nothing.
	defer func() { _ = conn.Close() }()

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		// A fake killed after it dialed but before it finished its line never started holding, so
		// there is no recorded call to check.
		if errors.Is(err, io.EOF) || isReset(err) {
			return
		}

		tb.Errorf("ghtest: read the held call: %v", err)

		return
	}

	if err := checkRecorded(line, record); err != nil {
		tb.Errorf("ghtest: %v", err)
	}

	announced()

	// Closing the channel lets the test cut the run, which kills the fake: a fake gone before the
	// acknowledgement reaches it was going to be killed anyway, and one still there that misses
	// it exits broken on its own.
	_, _ = conn.Write([]byte{acknowledgement})
}

// isReset reports whether err is a peer resetting the connection. On Windows a read from a killed
// peer fails with either the connection-reset code Windows sockets use (Errno 10054) or the code for
// a network name that no longer exists (Errno 64), and Errno.Is maps neither to Go's synthetic
// ECONNRESET there, so all three are checked. Elsewhere both numbers are plain Errno values nothing
// reports.
func isReset(err error) bool {
	const (
		windowsReset    = syscall.Errno(10054)
		windowsPeerGone = syscall.Errno(64)
	)

	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, windowsReset) ||
		errors.Is(err, windowsPeerGone)
}

// checkRecorded returns an error unless record holds the call that line, an encoded hold, names.
func checkRecorded(line []byte, record string) error {
	var held hold
	if err := json.Unmarshal(line, &held); err != nil {
		return fmt.Errorf("decode the held call %q: %w", line, err)
	}

	recorded, err := readRecord(record)
	if err != nil {
		return err
	}

	if countOf(recorded, held.Args) <= held.Earlier {
		return fmt.Errorf("the fake held its answer to %s %s before recording the call",
			github.Program, strings.Join(held.Args, " "))
	}

	return nil
}

// Holding returns a channel that closes once the fake has recorded a call and started holding
// its answer open for the answer's Delay, and before that call can end; it stays open while no
// answer has been held.
func (f *Fake) Holding() <-chan struct{} {
	return f.holding
}

// Calls returns every argv the fake received, each starting with gh, in order.
func (f *Fake) Calls() [][]string {
	f.tb.Helper()

	recorded, err := readRecord(f.record)
	if err != nil {
		f.tb.Fatalf("ghtest: %v", err)
	}

	calls := make([][]string, len(recorded))
	for i, args := range recorded {
		calls[i] = github.Argv(args)
	}

	return calls
}

// servePlaces is where the fake finds its script and keeps its record, and the address of the test
// process it tells of a hold.
type servePlaces struct {
	script, record, holdAddr string
}

// serve answers one call of the fake and returns its exit code; before it holds an answer open,
// it tells the test process at places.holdAddr and waits for its acknowledgement.
func serve(ctx context.Context, places servePlaces, args []string) int {
	script, err := readScript(places.script)
	if err != nil {
		return broken(err)
	}

	recorded, err := readRecord(places.record)
	if err != nil {
		return broken(err)
	}

	if err := appendRecord(places.record, args); err != nil {
		return broken(err)
	}

	// Last, so that a scripted answer to the version read is the one found.
	script = append(script, Says(installed, VersionArgs()...))

	index := slices.IndexFunc(script, func(call Call) bool { return Matches(call.Args, args) })
	if index < 0 {
		fmt.Fprintf(os.Stderr, "ghtest: no scripted answer for %s %s\n", github.Program, strings.Join(args, " "))

		return UnscriptedCode
	}

	earlier := countOf(recorded, args)

	answer := script[index].Answer
	if answer.Delay > 0 {
		if err := announceHold(ctx, places.holdAddr, hold{Args: args, Earlier: earlier}); err != nil {
			return broken(err)
		}

		time.Sleep(answer.Delay)
	}

	if _, err := os.Stdout.WriteString(answer.Stdout); err != nil {
		return broken(fmt.Errorf("write stdout: %w", err))
	}

	if _, err := os.Stderr.WriteString(answer.Stderr); err != nil {
		return broken(fmt.Errorf("write stderr: %w", err))
	}

	return answer.Code
}

// announceHold tells the test process, listening at addr, that the fake is about to hold its
// answer to held's call open, and returns once the test process has acknowledged it or ctx's
// deadline, when it has one, passes.
func announceHold(ctx context.Context, addr string, held hold) (err error) {
	line, err := json.Marshal(held)
	if err != nil {
		return fmt.Errorf("encode the held call: %w", err)
	}

	var dialer net.Dialer

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("announce the held answer to %s: %w", addr, err)
	}

	defer func() {
		if closeErr := conn.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close the announcement of the held answer: %w", closeErr)
		}
	}()

	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return fmt.Errorf("bound the announcement of the held answer to %s: %w", addr, err)
		}
	}

	if _, err := conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("announce the held answer to %s: %w", addr, err)
	}

	var ack [1]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return fmt.Errorf("wait for %s to acknowledge the held answer: %w", addr, err)
	}

	return nil
}

// countOf returns how many of the recorded calls have exactly args.
func countOf(recorded [][]string, args []string) int {
	count := 0

	for _, call := range recorded {
		if slices.Equal(call, args) {
			count++
		}
	}

	return count
}

// broken reports err on stderr and returns the exit code of a fake that could not do its work.
func broken(err error) int {
	fmt.Fprintf(os.Stderr, "ghtest: %v\n", err)

	return BrokenCode
}

func readScript(path string) ([]Call, error) {
	//nolint:gosec // G304: the path is the one Setup wrote, handed over in the environment.
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the script: %w", err)
	}

	var script []Call
	if err := json.Unmarshal(encoded, &script, legacy.FormatDurationAsNano(true)); err != nil {
		return nil, fmt.Errorf("decode the script %s: %w", path, err)
	}

	return script, nil
}

// readRecord returns the argv of every call recorded so far, one JSON array per line.
func readRecord(path string) ([][]string, error) {
	//nolint:gosec // G304: the path is the one Setup chose, handed over in the environment.
	encoded, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read the record: %w", err)
	}

	var calls [][]string

	for line := range bytes.Lines(encoded) {
		var args []string
		if err := json.Unmarshal(line, &args); err != nil {
			return nil, fmt.Errorf("decode the record %s: %w", path, err)
		}

		calls = append(calls, args)
	}

	return calls, nil
}

func appendRecord(path string, args []string) (err error) {
	line, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("encode the record: %w", err)
	}

	//nolint:gosec // G304: the path is the one Setup chose, handed over in the environment.
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open the record: %w", err)
	}

	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close the record: %w", closeErr)
		}
	}()

	if _, err := file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write the record: %w", err)
	}

	return nil
}
