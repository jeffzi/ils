package git

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"slices"
	"sync"

	"github.com/jeffzi/ils/internal/child"
)

const (
	program = "git"
	// installURL is the page git is installed from on every operating system.
	installURL = "https://git-scm.com/downloads"
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

// invocation returns the run of tool with args on the runner's behalf: from its directory, with
// its environment, recorded by its recorder and logged by its logger. Every program the package runs
// for a runner starts from here, so that no run drops a field of the runner.
func (r Runner) invocation(tool, toolURL string, args []string) invocation {
	inv := invocation{
		program:    tool,
		installURL: toolURL,
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

// invocation is a Runner's call of one tool: the fields child.Program takes, plus the recorder and
// logger the Runner adds.
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

// run runs the tool as child.Program.Run does, found by the invocation's lookPath and, when the
// invocation has a logger, logged once the run has ended.
func (inv invocation) run(ctx context.Context) (Result, error) {
	lookPath := inv.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	return child.Program{
		LookPath:   lookPath,
		Starting:   inv.record,
		Logger:     inv.logger,
		Tool:       inv.program,
		InstallURL: inv.installURL,
		Dir:        inv.dir,
		Args:       inv.args,
		Env:        inv.env,
	}.Run(ctx)
}

// exited reports whether err is git exiting with code.
func exited(err error, code int) bool {
	exitErr, ok := errors.AsType[*child.ExitError](err)

	return ok && exitErr.Code == code
}
