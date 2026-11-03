//go:build replay

package guard_test

import (
	"bytes"
	"cmp"
	"encoding/json/v2"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/guard"
)

// The replay judges every Bash command of the local Claude Code transcripts with the guard, in the
// directory each transcript recorded, and prints how many it refuses or asks about, by reason.
// `task test:replay` runs it. The output names commands from every project on the machine, so it
// stays in the test log.

const (
	// replayDirVar is the directory whose transcripts the replay reads; unset, ~/.claude/projects.
	replayDirVar = "ILS_REPLAY_DIR"
	// replayBeforeVar, when set, is an RFC 3339 time: the replay reads only the calls recorded
	// before it, so that later sessions add no commands. Each is still judged against its
	// repository as it is now.
	replayBeforeVar = "ILS_REPLAY_BEFORE"
	// replayWorkers is how many commands the replay judges at a time.
	replayWorkers = 8
	// replaySamples is how many commands the replay prints for each reason.
	replaySamples = 3
)

// replayed is one distinct Bash command and the directory it ran in.
type replayed struct {
	command string
	dir     string
}

// transcriptEntry is the part of a transcript line the replay reads: an assistant message whose
// content may hold Bash tool calls.
type transcriptEntry struct {
	Type      string    `json:"type"`
	Cwd       string    `json:"cwd"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		Content []struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Input struct {
				Command string `json:"command"`
			} `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

func TestAssess_ReplaysTheBashCommandsOfTheLocalTranscripts(t *testing.T) {
	dir := replayDir(t)
	before := replayBefore(t)

	read, distinct := readTranscripts(t, dir, before)
	judged, skipped := existing(distinct)
	verdicts := judgeAll(t, judged)

	t.Logf("commands read: %d, distinct: %d, judged: %d, skipped (directory gone): %d",
		read, len(distinct), len(judged), skipped)
	logByReason(t, judged, verdicts)
}

// replayDir returns the directory the replay reads, from replayDirVar or ~/.claude/projects.
func replayDir(t *testing.T) string {
	t.Helper()

	if dir := os.Getenv(replayDirVar); dir != "" {
		return dir
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("read the home directory for the default %s: %v", replayDirVar, err)
	}

	return filepath.Join(home, ".claude", "projects")
}

// replayBefore returns the time replayBeforeVar names, and the zero time when it is unset.
func replayBefore(t *testing.T) time.Time {
	t.Helper()

	value := os.Getenv(replayBeforeVar)
	if value == "" {
		return time.Time{}
	}

	before, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("%s=%q is not an RFC 3339 time: %v", replayBeforeVar, value, err)
	}

	return before
}

// readTranscripts returns how many Bash calls the transcripts under dir hold, recorded before
// before when it is not zero, and each distinct command with its directory, in the order first
// read. It fails the test when dir cannot be read or holds no transcript.
func readTranscripts(t *testing.T, dir string, before time.Time) (int, []replayed) {
	t.Helper()

	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
			files = append(files, path)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("read the transcripts under %s: %v", dir, err)
	}

	if len(files) == 0 {
		t.Fatalf("%s holds no Claude Code transcript (*.jsonl)", dir)
	}

	read := 0
	seen := map[replayed]bool{}
	var distinct []replayed
	for _, file := range files {
		for _, call := range bashCalls(t, file, before) {
			read++
			if !seen[call] {
				seen[call] = true
				distinct = append(distinct, call)
			}
		}
	}

	return read, distinct
}

// bashCalls returns each Bash call the transcript file holds, recorded before before when it is not
// zero. A line that is not an entry the replay can read is skipped.
func bashCalls(t *testing.T, file string, before time.Time) []replayed {
	t.Helper()

	data, err := os.ReadFile(file) //nolint:gosec // the replay reads every transcript under the directory it is given
	if err != nil {
		t.Fatalf("read the transcript %s: %v", file, err)
	}

	var calls []replayed
	for line := range bytes.Lines(data) {
		var entry transcriptEntry
		if json.Unmarshal(line, &entry) != nil || entry.Type != "assistant" {
			continue
		}

		if before.IsZero() || entry.Timestamp.Before(before) {
			calls = append(calls, bashCallsOf(entry)...)
		}
	}

	return calls
}

// bashCallsOf returns each Bash call in the content of entry, with the directory entry recorded.
func bashCallsOf(entry transcriptEntry) []replayed {
	var calls []replayed
	for _, part := range entry.Message.Content {
		if part.Type == "tool_use" && part.Name == "Bash" && part.Input.Command != "" {
			calls = append(calls, replayed{command: part.Input.Command, dir: entry.Cwd})
		}
	}

	return calls
}

// existing returns the commands of calls whose directory still exists, and how many it left out.
func existing(calls []replayed) ([]replayed, int) {
	kept := make([]replayed, 0, len(calls))
	for _, call := range calls {
		if info, err := os.Stat(call.dir); err == nil && info.IsDir() && filepath.IsAbs(call.dir) {
			kept = append(kept, call)
		}
	}

	return kept, len(calls) - len(kept)
}

// judgeAll returns the verdict of the guard on each of calls, judged replayWorkers at a time with
// this process's environment, in the order of calls.
func judgeAll(t *testing.T, calls []replayed) []guard.Verdict {
	t.Helper()

	env := map[string]string{}
	for _, entry := range os.Environ() {
		if name, _, ok := strings.Cut(entry, "="); ok {
			env[name], _ = git.EnvValue(os.Environ(), name)
		}
	}

	verdicts := make([]guard.Verdict, len(calls))
	next := make(chan int)
	var wg sync.WaitGroup
	for range replayWorkers {
		wg.Go(func() {
			for i := range next {
				caller := guard.Caller{Env: env, Home: env["HOME"], Dir: calls[i].dir, Git: git.Runner{}, GH: github.Runner{}}
				verdicts[i] = guard.Judge(t.Context(), calls[i].command, caller)
			}
		})
	}

	for i := range calls {
		next <- i
	}
	close(next)
	wg.Wait()

	return verdicts
}

// logByReason logs, for each outcome and reason among verdicts, the verdicts of calls, other than an
// allow, how many verdicts give it and up to replaySamples of their commands on one line each, the
// most frequent reason first.
func logByReason(t *testing.T, calls []replayed, verdicts []guard.Verdict) {
	t.Helper()

	type group struct {
		key     string
		samples []string
		count   int
	}

	groups := map[string]*group{}
	allowed := 0
	for i, v := range verdicts {
		if v.Outcome == guard.Allow {
			allowed++

			continue
		}

		outcome := "refuse"
		if v.Outcome == guard.Ask {
			outcome = "ask"
		}

		key := fmt.Sprintf("%s: %s", outcome, v.Reason)
		g, ok := groups[key]
		if !ok {
			g = &group{key: key}
			groups[key] = g
		}

		g.count++
		if len(g.samples) < replaySamples {
			g.samples = append(g.samples, strings.Join(strings.Fields(calls[i].command), " "))
		}
	}

	sorted := slices.SortedFunc(maps.Values(groups), func(a, b *group) int {
		return cmp.Or(cmp.Compare(b.count, a.count), strings.Compare(a.key, b.key))
	})

	t.Logf("allowed: %d, not allowed: %d", allowed, len(verdicts)-allowed)
	for _, g := range sorted {
		t.Logf("%6d  %s", g.count, g.key)
		for _, sample := range g.samples {
			t.Logf("        %s", sample)
		}
	}
}
