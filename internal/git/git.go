package git

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jeffzi/ils/internal/child"
)

const (
	program = "git"
	// installURL is the page git is installed from on every operating system.
	installURL = "https://git-scm.com/downloads"

	// pipeGrace is how long a call waits for a tool's output pipes to close once the tool itself is
	// gone. A process the tool started, such as ssh or a credential helper, can outlive it holding
	// those pipes.
	pipeGrace = time.Second
)

// Recorder keeps the argv of every call made by the runners it is attached to.
type Recorder struct {
	calls [][]string
	mu    sync.Mutex
}

// Calls returns a copy of every recorded argv, in the order the calls started.
func (r *Recorder) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()

	calls := make([][]string, len(r.calls))
	for i, argv := range r.calls {
		calls[i] = slices.Clone(argv)
	}

	return calls
}

func (r *Recorder) record(argv []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, slices.Clone(argv))
}

// Runner runs git in one directory with the environment its caller chose.
type Runner struct {
	// Recorder, when set, receives the argv of every call.
	Recorder *Recorder
	// Logger, when set, receives one debug record per call once the call has ended: the record
	// child.Exec.Log writes.
	Logger *slog.Logger
	Dir    string
	// Env is the child's whole environment; nil inherits this process's.
	Env []string
}

// Result is what a call left behind, as child.Output holds it.
type Result = child.Output

// Run runs git with args and returns its output once it has exited zero.
//
// A git missing from PATH is a *child.ToolMissingError and a non-zero exit is a
// *child.ExitError, returned with what git printed on stdout so that a caller can read the output
// of a failed call. Every other failure returns an empty Result: when ctx ends before git does,
// the error is the context's, so a half-written stdout is never read.
//
// Run does not wait for a process git started and left behind: once git has
// exited or been killed, output pipes still held open are closed after a short
// grace, so a context that ended never turns into a hang.
func (r Runner) Run(ctx context.Context, args ...string) (Result, error) {
	return r.invocation(program, installURL, args).run(ctx)
}

// invocation returns the run of program with args on the runner's behalf: from its directory, with
// its environment, recorded by its recorder and logged by its logger. Every program the package runs
// for a runner starts from here, so that no run drops a field of the runner.
func (r Runner) invocation(program, installURL string, args []string) invocation {
	inv := invocation{
		program:    program,
		installURL: installURL,
		args:       args,
		dir:        r.Dir,
		env:        r.Env,
		logger:     r.Logger,
	}
	if r.Recorder != nil {
		inv.record = r.Recorder.record
	}

	return inv
}

// SameKey reports whether name, the key of an environment entry, is key as the system matches
// keys: exactly, or without regard to case on Windows.
func SameKey(name, key string) bool {
	return name == key || (runtime.GOOS == "windows" && strings.EqualFold(name, key))
}

// EnvValue returns the value key has in env, a list of key=value entries, and whether env sets it.
// A key set twice has its last value, as it does for a child started with env, and keys match as
// SameKey matches them.
func EnvValue(env []string, key string) (string, bool) {
	var (
		value string
		set   bool
	)

	for _, entry := range env {
		name, entryValue, ok := strings.Cut(entry, "=")
		if ok && SameKey(name, key) {
			value, set = entryValue, true
		}
	}

	return value, set
}

// invocation is one run of a tool as a child process, started from an argv and never through a
// shell.
type invocation struct {
	// record, when set, receives the argv once the tool is found and before it starts.
	record func(argv []string)
	// lookPath, when set, finds the tool; Runner.lookPath finds it on the PATH of env, the way git
	// would find a program it starts with env. Nil finds it on this process's PATH, which is how git
	// itself is found whatever env holds.
	lookPath func(name string) (string, error)
	// logger, when set, receives the record child.Exec.Log writes once the run has ended.
	logger     *slog.Logger
	program    string
	installURL string
	dir        string
	args       []string
	env        []string
}

// run runs the tool as invocation.execute does and, when the invocation has a logger, logs the run
// once it has ended.
func (inv invocation) run(ctx context.Context) (Result, error) {
	begun := time.Now()
	result, err := inv.execute(ctx)

	child.Exec{Tool: inv.program, Args: inv.args, Err: err, Took: time.Since(begun)}.Log(ctx, inv.logger)

	return result, err
}

// execute runs the tool and returns its output once it has exited zero.
//
// A tool missing from the PATH it is looked up on is a *child.ToolMissingError, and a run that
// ended returns what child.Ended leaves to its caller. Once the tool has exited or been killed, output pipes a process it
// started still holds are closed after pipeGrace; such a process does not fail a run that exited
// zero.
func (inv invocation) execute(ctx context.Context) (Result, error) {
	lookPath := inv.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	path, err := lookPath(inv.program)
	if err != nil {
		return Result{}, &child.ToolMissingError{Tool: inv.program, InstallURL: inv.installURL, Err: err}
	}

	argv := child.Argv(inv.program, inv.args)
	if inv.record != nil {
		inv.record(argv)
	}

	var stdout, stderr bytes.Buffer

	//nolint:gosec // G204: running a tool with the caller's argv is this package's purpose; no shell is involved.
	cmd := exec.CommandContext(ctx, path, inv.args...)
	cmd.Dir = inv.dir
	cmd.Env = inv.env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = pipeGrace

	// ErrWaitDelay means the tool exited zero and a process it started still holds the pipes: the
	// tool's own output is complete, so the call succeeded.
	runErr := cmd.Run()
	if errors.Is(runErr, exec.ErrWaitDelay) {
		runErr = nil
	}

	return child.Ended(ctx, argv, runErr, Result{Stdout: stdout.String(), Stderr: stderr.String()})
}

// exited reports whether err is git exiting with code.
func exited(err error, code int) bool {
	exitErr, ok := errors.AsType[*child.ExitError](err)

	return ok && exitErr.Code == code
}
