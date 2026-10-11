// Package ghtest stands the test binary in for gh, answering from a script and recording each argv.
package ghtest

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	program = "gh"

	// scriptEnv names the script file; set, it makes the test binary act as gh.
	scriptEnv = "ILS_GHTEST_SCRIPT"
	// recordEnv names the file the fake appends each argv to.
	recordEnv = "ILS_GHTEST_RECORD"

	// brokenExit is the exit code of a fake that could not read its script or write its record.
	brokenExit = 2
)

// Answer is what the fake prints and the code it exits with.
type Answer struct {
	Stdout string
	Stderr string
	Code   int
}

// Call scripts the answers to one argv.
type Call struct {
	// Args is the argv after gh, matched exactly.
	Args []string
	// Answers are given in order, one per call, and the last one repeats; none answers with a silent exit 0.
	Answers []Answer
}

// Fake is the handle on a scripted gh.
type Fake struct {
	tb     testing.TB
	record string
}

// Main makes the test binary act as gh when Setup arranged for it, and returns otherwise.
//
// A package's TestMain calls it before anything else.
func Main() {
	script := os.Getenv(scriptEnv)
	if script == "" {
		return
	}

	os.Exit(serve(script, os.Getenv(recordEnv), os.Args[1:]))
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

	encoded, err := json.Marshal(script)
	if err != nil {
		tb.Fatalf("ghtest: encode the script: %v", err)
	}

	dir := tb.TempDir()
	scriptPath := filepath.Join(dir, "script.json")
	if err := os.WriteFile(scriptPath, encoded, 0o600); err != nil {
		tb.Fatalf("ghtest: write the script: %v", err)
	}

	fake := &Fake{tb: tb, record: filepath.Join(dir, "record.jsonl")}

	// A binary built with -race sleeps a second before every exit 0 unless told not to,
	// which a test that polls the fake would pay on each call.
	tb.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	tb.Setenv("GH_PATH", exe)
	tb.Setenv(scriptEnv, scriptPath)
	tb.Setenv(recordEnv, fake.record)

	return fake
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
		calls[i] = append([]string{program}, args...)
	}

	return calls
}

// serve answers one call of the fake and returns its exit code.
func serve(scriptPath, recordPath string, args []string) int {
	script, err := readScript(scriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ghtest: %v\n", err)

		return brokenExit
	}

	recorded, err := readRecord(recordPath)
	if err == nil {
		err = appendRecord(recordPath, args)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "ghtest: %v\n", err)

		return brokenExit
	}

	matches := func(other []string) bool { return slices.Equal(other, args) }

	index := slices.IndexFunc(script, func(call Call) bool { return matches(call.Args) })
	if index < 0 {
		fmt.Fprintf(os.Stderr, "ghtest: no scripted answer for %s %s\n", program, strings.Join(args, " "))

		return 1
	}

	answers := script[index].Answers
	if len(answers) == 0 {
		return 0
	}

	earlier := 0
	for _, call := range recorded {
		if matches(call) {
			earlier++
		}
	}

	answer := answers[min(earlier, len(answers)-1)]
	if _, err := os.Stdout.WriteString(answer.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "ghtest: write stdout: %v\n", err)

		return brokenExit
	}

	fmt.Fprint(os.Stderr, answer.Stderr)

	return answer.Code
}

func readScript(path string) ([]Call, error) {
	//nolint:gosec // G304: the path is the one Setup wrote, handed over in the environment.
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the script: %w", err)
	}

	var script []Call
	if err := json.Unmarshal(encoded, &script); err != nil {
		return nil, fmt.Errorf("decode the script %s: %w", path, err)
	}

	return script, nil
}

// readRecord returns the argv of every call recorded so far, one JSON array per line.
func readRecord(path string) ([][]string, error) {
	//nolint:gosec // G304: the path is the one Setup chose, handed over in the environment.
	recorded, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read the record: %w", err)
	}

	var calls [][]string

	for line := range bytes.Lines(recorded) {
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
