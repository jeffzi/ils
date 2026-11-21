package gittest

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

const (
	// FailedCode is the exit code of a call the fake git fails, the code of a failed git read.
	FailedCode = 128
	// FailedSaid is the line a call the fake git fails prints to stderr.
	FailedSaid = "fatal: this read was made to fail"
	// BrokenCode is the exit code of a fake git that could not do its own work: start the real git,
	// print the faked version, or stall as asked. It is none of the codes internal/git reads as an
	// answer (0, 1 and 2) and not FailedCode, the code of a failed git read, so a broken fake never
	// passes for git's reply.
	BrokenCode = 125

	// realGitEnv holds the path of the real git; its presence makes the test binary act as git.
	realGitEnv = "ILS_GITTEST_REAL_GIT"
	raceEnv    = "GORACE"
	versionEnv = "ILS_GITTEST_VERSION"
	// failOnEnv holds the words a failing call starts with, separated by wordsSep.
	failOnEnv = "ILS_GITTEST_FAIL_ON"
	// failAfterOnEnv holds the words a call that fails after the real git ran starts with,
	// separated by wordsSep.
	failAfterOnEnv = "ILS_GITTEST_FAIL_AFTER_ON"
	// stallOnEnv holds the words a stalling call starts with, separated by wordsSep.
	stallOnEnv = "ILS_GITTEST_STALL_ON"
	stallAtEnv = "ILS_GITTEST_STALL_AT"
	// answerOnEnv holds the words an answered call starts with, separated by wordsSep.
	answerOnEnv = "ILS_GITTEST_ANSWER_ON"
	answerEnv   = "ILS_GITTEST_ANSWER"
	// wordsSep separates the words a variable holds; no word a fake matches on holds a line break.
	wordsSep = "\n"
	// spawnAtEnv holds the address the lingering copy of a spawner holds; its presence makes the
	// test binary act as a spawner.
	spawnAtEnv = "ILS_GITTEST_SPAWN_AT"
	// spawnSaysEnv holds the line a spawner prints before it starts its lingering copy.
	spawnSaysEnv = "ILS_GITTEST_SPAWN_SAYS"
	// lingerAtEnv holds the address a spawner's lingering copy holds; its presence makes the test
	// binary act as that copy.
	lingerAtEnv = "ILS_GITTEST_LINGER_AT"
	// gitName is the name a fake answers to as git, found on PATH under it.
	gitName = "git"
	// gittestPackage is the import path of this package, which names builtRealGit to the linker.
	gittestPackage = "github.com/jeffzi/ils/internal/gittest"
	// fakeGitPackage is the program InstallFakeGit builds.
	fakeGitPackage = gittestPackage + "/git"
)

// builtRealGit is the path of the real git, which InstallFakeGit sets when it builds the fake; it
// is empty in every other build.
var builtRealGit string //nolint:gochecknoglobals // the linker's -X sets only a package-level string

// Fake says how the git FakeGit or InstallFakeGit puts on PATH answers. A call it does not fake
// runs the real git.
//
// A call that more than one setting fakes gets the first that matches, in this order: Version,
// FailOn, FailAfterOn, StallOn, AnswerOn.
type Fake struct {
	// Version, when not empty, is what a call whose arguments are exactly `--version` prints,
	// followed by a line break. Any other call, `version` and `--version --build-options` included,
	// is not faked by it.
	Version string
	// StallAt is the address a call StallOn names connects to; it is unused when StallOn is empty.
	StallAt string
	// Answer is what a call AnswerOn names prints, followed by a line break.
	Answer string
	// FailOn, when not empty, makes a call whose arguments start with these words exit with
	// FailedCode and print FailedSaid to stderr. A last word that ends in `*` matches any argument
	// that starts with what comes before the star, here and in StallOn and AnswerOn.
	FailOn []string
	// FailAfterOn, when not empty, makes a call whose arguments start with these words run the
	// real git, then exit with FailedCode and print FailedSaid to stderr, as a git that did its
	// work and failed after it does.
	FailAfterOn []string
	// StallOn, when not empty, makes a call whose arguments start with these words connect to the
	// address StallAt and wait there until it is killed. A test that hands in the address of a
	// SilentListener learns when the call is under way, and can end its context then.
	StallOn []string
	// AnswerOn, when not empty, makes a call whose arguments start with these words print Answer
	// to stdout and exit zero, an answer the real git might never give.
	AnswerOn []string
}

// Main makes the test binary act as git when FakeGit arranged for it, or as a spawner or its
// lingering copy when Spawner did, and reports whether it did. Run under git's name with no real
// git recorded in the environment, as the program InstallFakeGit builds always is, it acts as git
// and runs the real git built into it for the calls it does not fake.
//
// A package's TestMain calls it before anything else and, when served is true, exits with code
// without running a test.
func Main() (code int, served bool) {
	ctx := context.Background()

	// First: the lingering copy inherits the spawner's environment, spawnAtEnv included.
	if code, served := Linger(); served {
		return code, true
	}

	if addr := os.Getenv(spawnAtEnv); addr != "" {
		return spawn(ctx, addr, os.Getenv(spawnSaysEnv)), true
	}

	realGit := os.Getenv(realGitEnv)
	if realGit == "" {
		if filepath.Base(os.Args[0]) != gitName {
			return 0, false
		}

		if builtRealGit == "" {
			return broken(errors.New("find the real git: none was built into the fake; build it with InstallFakeGit")), true
		}

		realGit = builtRealGit
	}

	return serveGit(ctx, realGit, os.Args[1:]), true
}

// InstallFakeGit builds the fake git as a program of its own, without the race detector, into a
// new temporary directory, puts that directory first on the process PATH, and returns a cleanup
// that puts PATH back and removes the directory.
//
// A package's TestMain calls it once, before running its tests, and calls the cleanup after them.
// The fake answers a call as the settings of Fake.Settings in the call's environment say, and runs
// the real git when the environment carries none, so a test fakes git without touching the process
// environment and can run in parallel. The real git is the git first on PATH when InstallFakeGit
// runs, built into the fake, so the fake finds it whatever PATH a call carries. The fake must run
// from inside this module, where `go build` finds its package.
func InstallFakeGit() (cleanup func(), err error) {
	realGit, err := exec.LookPath(gitName)
	if err != nil {
		return nil, fmt.Errorf("find the real git: %w", err)
	}

	// `go build` splits -ldflags on spaces outside quotes and has no escape inside them.
	if strings.ContainsRune(realGit, '\'') {
		return nil, fmt.Errorf("build the real git %s into the fake: its path holds a single quote", realGit)
	}

	dir, err := os.MkdirTemp("", "ils-fake-git-")
	if err != nil {
		return nil, fmt.Errorf("make a directory for the fake git: %w", err)
	}

	bin := filepath.Join(dir, gitName)
	ldflags := "-ldflags=-X '" + gittestPackage + ".builtRealGit=" + realGit + "'"
	//nolint:gosec // G204: the arguments are constants of this package, a path it made, and the path of git.
	out, err := exec.CommandContext(context.Background(), "go", "build", ldflags, "-o", bin, fakeGitPackage).CombinedOutput()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("build the fake git: %w\n%s", err, out), os.RemoveAll(dir))
	}

	before, hadPath := os.LookupEnv("PATH")
	if err := os.Setenv("PATH", firstOnPath(dir, before)); err != nil {
		return nil, errors.Join(fmt.Errorf("put the fake git first on PATH: %w", err), os.RemoveAll(dir))
	}

	return func() {
		restore := os.Unsetenv("PATH")
		if hadPath {
			restore = os.Setenv("PATH", before)
		}

		// The cleanup runs after the tests, when nothing is left to fail: printing is all that is
		// left to report a directory left behind or a PATH not put back.
		if err := errors.Join(restore, os.RemoveAll(dir)); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "gittest: clean up the fake git: %v\n", err)
		}
	}, nil
}

// Settings returns the environment entries that make the fake git answer as f says, for a git
// runner to append to its environment. Every entry is present, empty when unused, so that they
// replace the settings of an earlier Fake that the environment already carries.
func (f Fake) Settings() []string {
	return []string{
		versionEnv + "=" + f.Version,
		failOnEnv + "=" + strings.Join(f.FailOn, wordsSep),
		failAfterOnEnv + "=" + strings.Join(f.FailAfterOn, wordsSep),
		stallOnEnv + "=" + strings.Join(f.StallOn, wordsSep),
		stallAtEnv + "=" + f.StallAt,
		answerOnEnv + "=" + strings.Join(f.AnswerOn, wordsSep),
		answerEnv + "=" + f.Answer,
	}
}

// Linger makes the test binary act as the lingering copy StartLingerer starts, when it is one, and
// reports whether it is. Main calls it first; a TestMain with modes of its own that the copy's
// inherited environment would also select calls it before them.
func Linger() (code int, served bool) {
	addr := os.Getenv(lingerAtEnv)
	if addr == "" {
		return 0, false
	}

	return Hold(context.Background(), addr), true
}

// StartLingerer starts, under ctx, a copy of the test binary that inherits this process's output
// and holds addr until the test ends, and returns it without waiting for it. The copy's TestMain
// must call Main or Linger.
func StartLingerer(ctx context.Context, addr string) (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find the test binary: %w", err)
	}

	//nolint:gosec // G204: the test binary starts itself; nothing here comes from outside the test.
	lingerer := exec.CommandContext(ctx, self)
	lingerer.Env = append(os.Environ(), lingerAtEnv+"="+addr)
	lingerer.Stdout = os.Stdout
	lingerer.Stderr = os.Stderr

	if err := lingerer.Start(); err != nil {
		return nil, fmt.Errorf("start the lingering copy: %w", err)
	}

	return lingerer, nil
}

// FakeGit puts the test binary first on PATH under git's name for the rest of the test, acting as
// git as fake says once the package's TestMain calls Main.
//
// It sets the process environment, so the test cannot be parallel, and a git runner that inherits
// that environment reaches the fake. It returns the variables the fake reads, for a runner whose
// environment is given whole: such a runner appends them to it.
//
// Called again in the same test, it replaces every setting of the earlier call: a setting the new
// fake leaves empty fakes nothing, and the variables it returns override the earlier call's when a
// runner appends both. It keeps only the real git the first call found: the git first on PATH is
// then the earlier fake, and a fake that ran it for the calls it does not fake would call itself
// forever.
func FakeGit(tb testing.TB, fake Fake) []string {
	tb.Helper()

	realGit := findRealGit(tb)

	// Every setting is set, empty when unused, so that this call's settings replace an earlier
	// call's in the process environment too.
	settings := slices.Concat([]string{realGitEnv + "=" + realGit, raceEnv + "=" + RaceSetting()}, fake.Settings())
	SetEnvEntries(tb, settings)

	LinkAs(tb, gitName)

	return settings
}

// SetEnvEntries sets each NAME=value entry in the process environment for the rest of the test, as
// tb.Setenv does, so the test cannot be parallel.
func SetEnvEntries(tb testing.TB, entries []string) {
	tb.Helper()

	for _, entry := range entries {
		name, value, _ := strings.Cut(entry, "=")
		tb.Setenv(name, value)
	}
}

// findRealGit returns the path of the real git, which a fake runs for the calls it does not fake
// and a Fixture runs for its own setup: the one an earlier FakeGit of the test recorded, or else
// the git first on PATH.
func findRealGit(tb testing.TB) string {
	tb.Helper()

	if realGit := os.Getenv(realGitEnv); realGit != "" {
		return realGit
	}

	realGit, err := exec.LookPath(gitName)
	if err != nil {
		tb.Fatalf("gittest: find the real git: %v", err)
	}

	return realGit
}

// RaceSetting returns the value of GORACE that stops a binary built with -race from sleeping a
// second before every exit, which each call of a fake that the test binary stands in for would pay.
// It keeps the options the environment already holds.
func RaceSetting() string {
	return strings.TrimSpace(os.Getenv(raceEnv) + " atexit_sleep_ms=0")
}

// CoverSetting returns the GOCOVERDIR=<dir> entry that makes a child built for coverage write its
// counters where `go test -cover` collects them, or "" when the test binary runs without coverage.
func CoverSetting() string {
	coverDirFlag := flag.Lookup("test.gocoverdir")
	if coverDirFlag == nil {
		return ""
	}

	dir := coverDirFlag.Value.String()
	if dir == "" {
		return ""
	}

	return "GOCOVERDIR=" + dir
}

// LinkAs puts the test binary first on PATH under the name of tool for the rest of the test, so
// that a call of tool runs the test binary, whose TestMain decides how it answers.
//
// It sets the process environment, so the test cannot be parallel.
func LinkAs(tb testing.TB, tool string) {
	tb.Helper()

	dir := tb.TempDir()
	Link(tb, dir, tool)

	tb.Setenv("PATH", firstOnPath(dir, os.Getenv("PATH")))
}

// Link links the test binary into dir under the name of tool, and returns the path of the link.
func Link(tb testing.TB, dir, tool string) string {
	tb.Helper()

	self, err := os.Executable()
	if err != nil {
		tb.Fatalf("gittest: find the test binary: %v", err)
	}

	path := filepath.Join(dir, tool)
	if err := os.Link(self, path); err != nil {
		tb.Fatalf("gittest: link the test binary as %s: %v", tool, err)
	}

	return path
}

// firstOnPath returns the search path path with dir put first. An empty path gives dir alone: a
// trailing list separator would add an empty entry, which puts the current directory on the search
// path.
func firstOnPath(dir, path string) string {
	if path == "" {
		return dir
	}

	return dir + string(os.PathListSeparator) + path
}

// serveGit answers one call of the fake git with args and returns its exit code.
func serveGit(ctx context.Context, realGit string, args []string) int {
	if version := os.Getenv(versionEnv); version != "" && slices.Equal(args, []string{"--version"}) {
		return say(os.Stdout, version, "the faked version", 0)
	}

	if startsWith(args, failOnEnv) {
		return say(os.Stderr, FailedSaid, "the failure", FailedCode)
	}

	if startsWith(args, failAfterOnEnv) {
		if code := runRealGit(ctx, realGit, args); code == BrokenCode {
			return code
		}

		return say(os.Stderr, FailedSaid, "the failure", FailedCode)
	}

	if startsWith(args, stallOnEnv) {
		return Hold(ctx, os.Getenv(stallAtEnv))
	}

	if startsWith(args, answerOnEnv) {
		return say(os.Stdout, os.Getenv(answerEnv), "the faked answer", 0)
	}

	return becomeRealGit(realGit, args)
}

// say prints line to w and returns code, or BrokenCode when the print fails; what names the line in
// the message printed then.
func say(w io.Writer, line, what string, code int) int {
	if _, err := fmt.Fprintln(w, line); err != nil {
		return broken(fmt.Errorf("print %s: %w", what, err))
	}

	return code
}

// startsWith reports whether args start with the words the variable key holds, and false when it
// holds none. A last word that ends in `*` matches any argument that starts with what comes before
// the star, so that a call can be named by an argument part of which is made at run time.
func startsWith(args []string, key string) bool {
	words := os.Getenv(key)
	if words == "" {
		return false
	}

	prefix := strings.Split(words, wordsSep)
	if len(args) < len(prefix) {
		return false
	}

	last := len(prefix) - 1
	if start, star := strings.CutSuffix(prefix[last], "*"); star {
		return slices.Equal(args[:last], prefix[:last]) && strings.HasPrefix(args[last], start)
	}

	return slices.Equal(args[:len(prefix)], prefix)
}

// Hold connects to addr and waits for the server to close the connection, which a SilentListener
// does only once the test ends, so a process that calls it stays until it is killed or the test
// ends. It returns BrokenCode, since a held call that ends on its own did not do what the test
// asked of it, after printing why to stderr.
func Hold(ctx context.Context, addr string) int {
	var dialer net.Dialer

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return broken(fmt.Errorf("connect to %s to hold: %w", addr, err))
	}

	// The server never writes, so the copy returns only once it closes the connection.
	_, copyErr := io.Copy(io.Discard, conn)
	closeErr := conn.Close()

	return broken(errors.Join(fmt.Errorf("the server at %s closed the held call", addr), copyErr, closeErr))
}

// Spawner returns the environment entries that make the test binary, started with them, print
// said on a line of its own, start a copy of itself that inherits its output and holds addr until
// the test ends, and exit zero at once: a program that is gone while a process it started still
// holds its output pipes.
//
// The package's TestMain must call Main. A test that hands in the address of a SilentListener
// learns when the copy has started.
func Spawner(addr, said string) []string {
	return []string{spawnAtEnv + "=" + addr, spawnSaysEnv + "=" + said, raceEnv + "=" + RaceSetting()}
}

// spawn prints said, starts a copy of the test binary that inherits this process's output and
// holds addr, and returns zero without waiting for it, or BrokenCode when it cannot.
func spawn(ctx context.Context, addr, said string) int {
	if code := say(os.Stdout, said, "the spawner's line", 0); code != 0 {
		return code
	}

	if _, err := StartLingerer(ctx, addr); err != nil {
		return broken(err)
	}

	return 0
}

// runRealGit runs the real git with args, the environment, and the standard streams, and returns
// its exit code, or BrokenCode when it cannot start it.
func runRealGit(ctx context.Context, realGit string, args []string) int {
	//nolint:gosec // G204: the fake names the real git and passes on the arguments it was given.
	cmd := exec.CommandContext(ctx, realGit, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err == nil {
		return 0
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode()
	}

	return broken(fmt.Errorf("start the real git: %w", err))
}

// becomeRealGit replaces the fake with the real git, run with args, the environment, and the
// standard streams, so that the caller sees git's pid, and a signal that kills git or git's exit
// status reaches it as with the real git. It returns, with BrokenCode, only when it cannot.
func becomeRealGit(realGit string, args []string) int {
	argv := append([]string{realGit}, args...)
	//nolint:gosec // G204: the fake names the real git and passes on the arguments it was given.
	err := syscall.Exec(realGit, argv, os.Environ())

	return broken(fmt.Errorf("start the real git: %w", err))
}

// broken prints why the fake git could not do its own work to stderr and returns BrokenCode.
func broken(err error) int {
	_, _ = fmt.Fprintf(os.Stderr, "gittest: %v\n", err)

	return BrokenCode
}
