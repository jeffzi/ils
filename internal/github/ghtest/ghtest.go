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
	"testing"
	"time"

	"github.com/jeffzi/ils/internal/github"
)

const (
	// scriptEnv names the script file; set, it makes the test binary act as gh.
	scriptEnv = "ILS_GHTEST_SCRIPT"
	// recordEnv names the file the fake appends each argv to.
	recordEnv = "ILS_GHTEST_RECORD"
	// holdEnv names the local address the fake dials once it starts holding an answer open.
	holdEnv = "ILS_GHTEST_HOLD"

	// brokenExit is the exit code of a fake that could not read its script or write its record.
	brokenExit = 2
	// unscriptedExit is the exit code of a fake asked for an argv its script does not hold.
	unscriptedExit = 1
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
const installed = "gh version " + github.MinimumVersion + " (2024-05-29)\n"

// Call scripts the answers to one argv.
type Call struct {
	// Args is the argv after gh, matched exactly.
	Args []string
	// Answers are given in order, one per call, and the last one repeats; none answers with a silent exit 0.
	Answers []Answer
}

// Says scripts an exit 0 that prints stdout as the one answer to args.
func Says(stdout string, args ...string) Call {
	return Exits(Answer{Stdout: stdout}, args...)
}

// Exits scripts answer as the one answer to args.
func Exits(answer Answer, args ...string) Call {
	return Call{Args: args, Answers: []Answer{answer}}
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

// RepoViewArgs is the argv of the read of a repository's identity; slug is host/owner/name.
func RepoViewArgs(slug string) []string {
	return []string{"repo", "view", slug, "--json", "isFork,viewerPermission"}
}

// ListArgs is the argv of the list of the pull requests from the branch head that are in state.
func ListArgs(slug, head, state string) []string {
	return []string{
		"pr", "list", "--repo", slug, "--head", head, "--state", state,
		"--json", "number,url,title,baseRefName,headRefOid,isDraft,isCrossRepository",
	}
}

// CreateArgs is the argv that opens a pull request titled title from head against base, with no body.
func CreateArgs(slug, title, base, head string) []string {
	return []string{"pr", "create", "--repo", slug, "--title", title, "--body", "", "--base", base, "--head", head}
}

// HeadArgs is the argv of the read of pull request number's head commit and merge state.
func HeadArgs(slug string, number int) []string {
	return []string{
		"pr", "view", "--repo", slug, strconv.Itoa(number),
		"--json", "headRefOid,mergeable,mergeStateStatus,isDraft,reviewDecision",
	}
}

// RulesArgs is the argv of the read of the rules on branch; repo is owner/name.
func RulesArgs(host, repo, branch string) []string {
	return []string{"api", "--hostname", host, "--paginate", "repos/" + repo + "/rules/branches/" + branch}
}

// ChecksArgs is the argv of the read of pull request number's checks.
func ChecksArgs(slug string, number int) []string {
	return []string{"pr", "checks", "--repo", slug, strconv.Itoa(number), "--json", "name,bucket,workflow,link"}
}

// RunsArgs is the argv of the list of the workflow runs on the commit sha.
func RunsArgs(slug, sha string) []string {
	return []string{
		"run", "list", "--repo", slug, "--commit", sha, "--limit", "1000",
		"--json", "status,name,workflowName",
	}
}

// LogArgs is the argv of the read of the failed log of the workflow run numbered run.
func LogArgs(slug, run string) []string {
	return []string{"run", "view", "--repo", slug, run, "--log-failed"}
}

// ---------------------------------------------------------------------------
// Canned JSON answers
// ---------------------------------------------------------------------------

// workflow is the workflow every scripted check and run belongs to.
const workflow = "CI"

// RepoViewJSON is `gh repo view`'s answer for a repository that is a fork or not, on which the
// viewer holds permission.
func RepoViewJSON(fork bool, permission string) string {
	return `{"isFork":` + strconv.FormatBool(fork) + `,"viewerPermission":"` + permission + `"}`
}

// Change is one pull request as `gh pr list` prints it.
type Change struct {
	URL    string
	Title  string
	Base   string
	SHA    string
	Number int
	Draft  bool
	// CrossRepository says the head branch lives in another repository, as a fork's does.
	CrossRepository bool
}

// ChangeJSON is one entry of `gh pr list`'s answer, with every field ListArgs asks for.
func ChangeJSON(change Change) string {
	return `{"number":` + strconv.Itoa(change.Number) + `,"url":"` + change.URL + `","title":"` + change.Title +
		`","baseRefName":"` + change.Base + `","headRefOid":"` + change.SHA +
		`","isDraft":` + strconv.FormatBool(change.Draft) +
		`,"isCrossRepository":` + strconv.FormatBool(change.CrossRepository) + `}`
}

// HeadJSON is `gh pr view`'s answer for a pull request at sha that is not a draft.
func HeadJSON(sha, mergeable, status, review string) string {
	return `{"headRefOid":"` + sha + `","mergeable":"` + mergeable + `","mergeStateStatus":"` + status +
		`","isDraft":false,"reviewDecision":"` + review + `"}`
}

// CheckJSON is one entry of `gh pr checks`'s answer, for a check of the workflow CI.
func CheckJSON(name, bucket, link string) string {
	return `{"name":"` + name + `","bucket":"` + bucket + `","workflow":"` + workflow + `","link":"` + link + `"}`
}

// RunJSON is one entry of `gh run list`'s answer, for a run named name of the workflow CI.
func RunJSON(status, name string) string {
	return `{"status":"` + status + `","name":"` + name + `","workflowName":"` + workflow + `"}`
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

	return serve(script, os.Getenv(recordEnv), os.Getenv(holdEnv), os.Args[1:]), true
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

	// A binary built with -race sleeps a second before every exit 0 unless told not to,
	// which a test that polls the fake would pay on each call.
	tb.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
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

	var config net.ListenConfig

	listener, err := config.Listen(tb.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("ghtest: listen for held answers: %v", err)
	}

	var accepting sync.WaitGroup

	held := make(chan struct{})

	tb.Cleanup(func() {
		if err := listener.Close(); err != nil {
			tb.Errorf("ghtest: close the listener for held answers: %v", err)
		}

		accepting.Wait()
	})

	accepting.Go(func() {
		var once sync.Once

		announced := func() { once.Do(func() { close(held) }) }

		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			acknowledgeHold(tb, conn, record, announced)
		}
	})

	return listener.Addr().String(), held
}

// acknowledgeHold reads the hold the fake sends over conn, fails the test unless record already
// holds the call, calls announced, and only then lets the fake go on.
func acknowledgeHold(tb testing.TB, conn net.Conn, record string, announced func()) {
	// The acknowledgement already reached the fake, or the fake is gone and has nothing left to
	// read: a failed close loses nothing.
	defer func() { _ = conn.Close() }()

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
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
	_, _ = conn.Write([]byte{'\n'})
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
		calls[i] = append([]string{github.Program}, args...)
	}

	return calls
}

// serve answers one call of the fake and returns its exit code; before it holds an answer open,
// it tells the test process at holdAddr and waits for its acknowledgement.
func serve(scriptPath, recordPath, holdAddr string, args []string) int {
	script, err := readScript(scriptPath)
	if err != nil {
		return broken(err)
	}

	recorded, err := readRecord(recordPath)
	if err == nil {
		err = appendRecord(recordPath, args)
	}

	if err != nil {
		return broken(err)
	}

	// Last, so that a scripted answer to the version read is the one found.
	script = append(script, Says(installed, VersionArgs()...))

	index := slices.IndexFunc(script, func(call Call) bool { return slices.Equal(call.Args, args) })
	if index < 0 {
		fmt.Fprintf(os.Stderr, "ghtest: no scripted answer for %s %s\n", github.Program, strings.Join(args, " "))

		return unscriptedExit
	}

	answers := script[index].Answers
	if len(answers) == 0 {
		return 0
	}

	earlier := countOf(recorded, args)

	answer := answers[min(earlier, len(answers)-1)]
	if answer.Delay > 0 {
		if err := announceHold(holdAddr, hold{Args: args, Earlier: earlier}); err != nil {
			return broken(err)
		}

		time.Sleep(answer.Delay)
	}

	if _, err := os.Stdout.WriteString(answer.Stdout); err != nil {
		return broken(fmt.Errorf("write stdout: %w", err))
	}

	fmt.Fprint(os.Stderr, answer.Stderr)

	return answer.Code
}

// announceHold tells the test process, listening at addr, that the fake is about to hold its
// answer to held's call open, and returns once the test process has acknowledged it.
func announceHold(addr string, held hold) (err error) {
	line, err := json.Marshal(held)
	if err != nil {
		return fmt.Errorf("encode the held call: %w", err)
	}

	var dialer net.Dialer

	conn, err := dialer.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("announce the held answer to %s: %w", addr, err)
	}

	defer func() {
		if closeErr := conn.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close the announcement of the held answer: %w", closeErr)
		}
	}()

	if _, err := conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("announce the held answer to %s: %w", addr, err)
	}

	if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
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

	return brokenExit
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
