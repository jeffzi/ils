package github

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
)

// Program is the name gh is run under, and the first word of every argv a call is named by.
const Program = "gh"

// Argv returns the full argv of a call of gh with args.
func Argv(args []string) []string {
	return child.Argv(Program, args)
}

const installURL = "https://cli.github.com"

// MinimumVersion is the oldest release of gh that ils works with: the first whose `gh auth status`
// prints JSON, which CheckLogin reads. Releases older than 2.73.0 also fail `gh pr edit` against
// GitHub, even with no project flag, because they still read the Projects (classic) fields that
// GitHub's GraphQL API now rejects.
const MinimumVersion = "2.81.0"

// versionPrefix is what the first line of `gh --version` reads ahead of the version.
const versionPrefix = "gh version "

// Runner runs gh, found through GH_PATH and then PATH. Its zero value logs nothing.
type Runner struct {
	// Logger, when set, receives one debug record per call once it ends: the record child.Exec.Log
	// writes.
	Logger *slog.Logger
	// Env is gh's whole environment; nil inherits this process's. gh itself is found through this
	// process's GH_PATH and PATH whatever Env holds.
	Env []string
}

// Environ returns a copy of the environment gh runs with: the runner's own Env, or the environment
// of this process when it has none.
func (r Runner) Environ() []string {
	return child.Environ(r.Env)
}

// Result is what a call left behind, as child.Output holds it.
type Result = child.Output

// DecodeError reports a call whose stdout was not what its caller expected.
type DecodeError struct {
	Err  error
	Argv []string
}

// Error names the command whose output could not be decoded.
func (e *DecodeError) Error() string {
	return fmt.Sprintf("%s: decode output: %v", child.CommandLine(e.Argv), e.Err)
}

// Unwrap returns Err, so that errors.Is and errors.As reach the decoder's error.
func (e *DecodeError) Unwrap() error {
	return e.Err
}

// Run runs gh with args and returns what it printed.
//
// A gh that cannot be found is a *child.ToolMissingError. A non-zero exit is a
// *child.ExitError, returned together with gh's stdout, since gh prints to stdout
// before some of its non-zero exits; its stderr is the ExitError's. When ctx ends
// before gh does, the error is the context's and the Result is empty, so a
// half-written stdout is never read.
//
// Run does not wait for a process gh started and left behind: once gh has exited or been killed,
// output pipes still held open are closed after a short grace, as child.Program.Run does.
func (r Runner) Run(ctx context.Context, args ...string) (Result, error) {
	program := child.Program{
		LookPath: ghPath, Logger: r.Logger, Tool: Program, InstallURL: installURL, Args: args, Env: r.Env,
		StartWantingAFileIsMissing: true,
	}

	return program.Run(ctx)
}

// The pace of Read: how long it pauses before it reads a failed read again, and how long after
// the first failure of a read it stops reading it again. One pair for every read, so that a GitHub
// hiccup costs any command the same time at most.
const (
	rereadPause  = 2 * time.Second
	rereadWindow = 10 * time.Second
)

// rereadMessage is the message of the debug record Read writes before each pause.
const rereadMessage = "read again"

// Reply is what a read left behind. For a `gh api` read, Stdout holds the body of each page, the
// status block gh printed ahead of each one taken out, and Status and Header are the HTTP status
// and headers of the last page GitHub answered.
type Reply struct {
	// Header is nil when GitHub answered no page, or the read is not a `gh api` read.
	Header http.Header
	Result
	// Status is 0 when GitHub answered no page, or the read is not a `gh api` read.
	Status int
}

// Read runs gh with args, a read that changes nothing on GitHub, and reads it again while it fails
// in a way that a later read can mend: gh exiting 1 with no 4xx answer from GitHub, which is what a
// network failure, a 5xx, and a 2xx carrying GraphQL errors all exit with. A 4xx says the request
// itself is wrong, so asking again gets the same answer. Only a `gh api` read reports its status,
// so every exit 1 of any other read is read again.
//
// Each read again comes rereadPause after the last failure, and none starts whose pause would end
// more than rereadWindow after the first failure: the read then returns its last failure, as Run
// does. When ctx ends during a pause, the error is the context's. Every other outcome is returned
// at once, as Run returns it.
//
// A `gh api` read is run with --include, so that gh prints the status block of each page; Read
// takes the blocks out of Stdout and returns the status and headers of the last one, also along
// with the *child.ExitError of a 4xx. A status block that cannot be read is a *DecodeError.
func (r Runner) Read(ctx context.Context, args ...string) (Reply, error) {
	args = withHeaders(args)

	var firstFailure time.Time

	for {
		reply, err := r.readOnce(ctx, args)
		if !mendable(reply, err) {
			return reply, err
		}

		now := time.Now()
		if firstFailure.IsZero() {
			firstFailure = now
		}

		if now.Add(rereadPause).After(firstFailure.Add(rereadWindow)) {
			return reply, err
		}

		r.logReread(ctx, args, reply.Status)

		if err := pause(ctx, rereadPause); err != nil {
			return Reply{}, fmt.Errorf("%s: %w", child.CommandLine(Argv(args)), err)
		}
	}
}

// readOnce runs gh with args once, and takes the status blocks out of the output of a `gh api`
// read. When gh failed, a status block that cannot be read leaves the output as gh printed it and
// the failure as gh's.
func (r Runner) readOnce(ctx context.Context, args []string) (Reply, error) {
	result, err := r.Run(ctx, args...)
	if !isAPI(args) {
		return Reply{Result: result}, err
	}

	body, status, header, blockErr := cutStatusBlocks(result.Stdout)
	if blockErr != nil {
		if err == nil {
			err = &DecodeError{Argv: Argv(args), Err: blockErr}
		}

		return Reply{Result: result}, err
	}

	result.Stdout = body

	return Reply{Result: result, Status: status, Header: header}, err
}

// mendable reports whether a read that ended with reply and err is worth reading again: gh exited
// 1, and the last page GitHub answered, if any, is not a 4xx.
func mendable(reply Reply, err error) bool {
	exitErr, ok := errors.AsType[*child.ExitError](err)
	if !ok || exitErr.Code != 1 {
		return false
	}

	return reply.Status < http.StatusBadRequest || reply.Status >= http.StatusInternalServerError
}

// logReread sends the runner's logger, when it has one, the debug record of a read about to be
// read again: rereadMessage, the command, the pause and, when GitHub answered, the status.
func (r Runner) logReread(ctx context.Context, args []string, status int) {
	if r.Logger == nil {
		return
	}

	attrs := []slog.Attr{slog.String("command", child.CommandLine(Argv(args))), slog.Duration("pause", rereadPause)}
	if status != 0 {
		attrs = append(attrs, slog.Int("status", status))
	}

	r.Logger.LogAttrs(ctx, slog.LevelDebug, rereadMessage, attrs...)
}

// pause returns after wait, or with the context's error as soon as ctx ends.
func pause(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isAPI reports whether args are those of a `gh api` call.
func isAPI(args []string) bool {
	return len(args) > 0 && args[0] == "api"
}

// withHeaders returns args with --include after the leading api of a `gh api` call that does not
// already hold it, and args itself otherwise.
func withHeaders(args []string) []string {
	if !isAPI(args) || slices.Contains(args, "--include") {
		return args
	}

	return slices.Concat(args[:1], []string{"--include"}, args[1:])
}

// statusPrefix starts the first line of every status block gh prints.
const statusPrefix = "HTTP/"

// cutStatusBlocks takes the status blocks out of what `gh api --include` printed, and returns the
// bodies left, joined as printed, and the status and headers of the last block.
//
// gh prints one block ahead of each page: the line `HTTP/<version> <code> <reason>`, one
// `Name: value` line per header, then an empty line. No JSON value starts with the H of HTTP/, so
// a body runs from the end of a block to the next line that starts a block, or to the end.
func cutStatusBlocks(stdout string) (body string, status int, header http.Header, err error) {
	var bodies strings.Builder

	for rest := stdout; rest != ""; {
		if !strings.HasPrefix(rest, statusPrefix) {
			page, after := cutBody(rest)
			bodies.WriteString(page)
			rest = after

			continue
		}

		status, header, rest, err = cutStatusBlock(rest)
		if err != nil {
			return "", 0, nil, err
		}
	}

	return bodies.String(), status, header, nil
}

// cutBody splits rest, which starts with a body, at the start of the next status block: the first
// one that follows a whole JSON value. A body that is not JSON runs to the end of rest.
func cutBody(rest string) (body, after string) {
	decoder := jsontext.NewDecoder(strings.NewReader(rest))

	for {
		offset := int(decoder.InputOffset())
		next := len(rest) - len(strings.TrimLeft(rest[offset:], " \t\r\n"))

		if next == len(rest) {
			return rest, ""
		}

		if strings.HasPrefix(rest[next:], statusPrefix) {
			return rest[:next], rest[next:]
		}

		if err := decoder.SkipValue(); err != nil {
			return rest, ""
		}
	}
}

// cutStatusBlock reads the status block that starts rest, and returns its status, its headers,
// and what follows it.
func cutStatusBlock(rest string) (status int, header http.Header, after string, err error) {
	line, after, _ := strings.Cut(rest, "\n")
	fields := strings.Fields(line)

	if len(fields) < 2 {
		return 0, nil, "", fmt.Errorf("read the status line %q: no status", line)
	}

	status, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, nil, "", fmt.Errorf("read the status line %q: %w", line, err)
	}

	header = http.Header{}

	for {
		var ended bool

		line, after, ended = strings.Cut(after, "\n")
		line = strings.TrimSuffix(line, "\r")

		if line == "" {
			return status, header, after, nil
		}

		name, value, ok := strings.Cut(line, ":")
		if !ok || !ended {
			return 0, nil, "", fmt.Errorf("read the header line %q of the %d answer", line, status)
		}

		header.Add(strings.TrimSpace(name), strings.TrimSpace(value))
	}
}

// PathVariable is the environment variable that names the gh to start ahead of the one on PATH.
const PathVariable = "GH_PATH"

// ghPath returns the gh to start, whatever name it is asked for: the one PathVariable names, or else
// the gh on PATH, found as git is. A gh found only through a relative PATH entry is not found.
func ghPath(string) (string, error) {
	if path := os.Getenv(PathVariable); path != "" {
		return path, nil
	}

	path, err := exec.LookPath(Program)
	if err != nil {
		return "", fmt.Errorf("find %s: %w", Program, err)
	}

	return path, nil
}

// checkVersion returns nil when gh is MinimumVersion or newer.
//
// A gh that is older, or whose first line of `gh --version` carries no version that can be
// read, is a *child.ToolTooOldError; a pre-release of MinimumVersion is older than it.
func (r Runner) checkVersion(ctx context.Context) error {
	result, err := r.Run(ctx, "--version")
	if err != nil {
		return err
	}

	requirement := child.Requirement{Tool: Program, Minimum: MinimumVersion, InstallURL: installURL}

	return requirement.CheckOutput(result.Stdout, VersionOf)
}

// VersionOf returns the version that a first line of `gh --version` names, as semver without the
// leading v, or "" when it names none or does not start with "gh version ". A pre-release keeps
// its suffix, so that it reads as older than its release.
func VersionOf(line string) string {
	rest, ok := strings.CutPrefix(line, versionPrefix)
	if !ok {
		return ""
	}

	version, _, _ := strings.Cut(rest, " ")
	if !child.ValidVersion(version) {
		return ""
	}

	return version
}

// loginStatus is the part of `gh auth status --json hosts` that CheckLogin reads: the accounts gh
// holds a login for, by host.
type loginStatus struct {
	Hosts map[string][]loginAccount `json:"hosts"`
}

type loginAccount struct {
	// State is "success" when gh's check of the account passed.
	State string `json:"state"`
	// Error is gh's text for a check that did not pass.
	Error string `json:"error"`
	// Active marks the account gh uses on the host.
	Active bool `json:"active"`
}

// CheckLogin returns nil when the account gh uses on host passes gh's check of it.
//
// A gh that cannot be found is a *child.ToolMissingError, and one that lists no account for host
// is a *forge.NotLoggedInError. Every other failure is an error that is not a
// *forge.NotLoggedInError: a check of the active account that did not pass, holding gh's text for
// it; accounts listed for host with none active; a non-zero exit of gh, such as one too old for
// --json, wrapping the *child.ExitError; and stdout that does not decode, a *DecodeError.
func (r Runner) CheckLogin(ctx context.Context, host string) error {
	status, err := runDecoded[loginStatus](ctx, r, "auth", "status", "--hostname", host, "--json", "hosts")
	if err != nil {
		return err
	}

	accounts := status.Hosts[host]
	if len(accounts) == 0 {
		return &forge.NotLoggedInError{Host: host}
	}

	active := slices.IndexFunc(accounts, func(account loginAccount) bool { return account.Active })
	if active < 0 {
		return fmt.Errorf("check the login of %s to %s: no account is active", Program, host)
	}

	if account := accounts[active]; account.State != "success" {
		return fmt.Errorf("check the login of %s to %s: %s", Program, host, account.Error)
	}

	return nil
}

// Alias returns what `gh alias list` says the alias name expands to, and ok false with an empty
// expansion when gh holds no such alias.
//
// It returns the errors of Runner.Run, and a *DecodeError when stdout is not the YAML map of each
// alias's name to its expansion that gh prints.
func (r Runner) Alias(ctx context.Context, name string) (expansion string, ok bool, err error) {
	args := []string{"alias", "list"}

	result, err := r.Run(ctx, args...)
	if err != nil {
		return "", false, err
	}

	var aliases map[string]string
	if err := yaml.Unmarshal([]byte(result.Stdout), &aliases); err != nil {
		return "", false, &DecodeError{Argv: Argv(args), Err: err}
	}

	expansion, ok = aliases[name]

	return expansion, ok, nil
}

// readDecoded reads with args, as Runner.Read does, and decodes the body it returns into a T.
//
// It returns the errors of Runner.Read, and a *DecodeError when the body is not the JSON of a T.
func readDecoded[T any](ctx context.Context, r Runner, args ...string) (T, error) {
	reply, err := r.Read(ctx, args...)
	if err != nil {
		var zero T

		return zero, err
	}

	return decodeOutput[T](reply.Stdout, withHeaders(args))
}

// runDecoded runs gh with args and decodes its stdout into a T.
//
// It returns the errors of Runner.Run, and a *DecodeError when stdout is not
// the JSON of a T.
func runDecoded[T any](ctx context.Context, r Runner, args ...string) (T, error) {
	result, err := r.Run(ctx, args...)
	if err != nil {
		var zero T

		return zero, err
	}

	return decodeOutput[T](result.Stdout, args)
}

// readEachPage reads with args, as Runner.Read does, and decodes the pages of the body it returns
// with decodeEachPage.
//
// It returns the errors of Runner.Read, and a *DecodeError when the body holds no page or a page
// that is not the JSON of a P.
func readEachPage[P any](ctx context.Context, r Runner, args []string) ([]P, error) {
	reply, err := r.Read(ctx, args...)
	if err != nil {
		return nil, err
	}

	return decodeEachPage[P](reply.Stdout, withHeaders(args))
}

func decodeOutput[T any](stdout string, args []string) (T, error) {
	var value T
	if err := json.Unmarshal([]byte(stdout), &value); err != nil {
		var zero T

		return zero, &DecodeError{Argv: Argv(args), Err: err}
	}

	return value, nil
}

// decodeEachPage decodes the JSON values that `gh api --paginate` prints, one per page and
// concatenated, each into a P; stdout holding no value is a *DecodeError.
func decodeEachPage[P any](stdout string, args []string) ([]P, error) {
	decoder := jsontext.NewDecoder(strings.NewReader(stdout))

	var pages []P

	for {
		raw, err := decoder.ReadValue()
		if errors.Is(err, io.EOF) {
			if len(pages) > 0 {
				return pages, nil
			}

			err = io.ErrUnexpectedEOF
		}

		if err != nil {
			return nil, &DecodeError{Argv: Argv(args), Err: err}
		}

		page, err := decodeOutput[P](string(raw), args)
		if err != nil {
			return nil, err
		}

		pages = append(pages, page)
	}
}
