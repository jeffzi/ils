package cli_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

const (
	nextHelp = "ils --help"

	// hookLine is a line the renderers reject, since its own wording names a hook.
	hookLine = "The pre-push step ran."

	// outsideText is what `ils push` prints in a directory that is no repository.
	outsideText = "Could not read where HEAD stands: git remote get-url origin: exit status 128: " +
		"fatal: not a git repository (or any of the parent directories): .git\n" +
		"Nothing was written.\nNext: fix what failed, then run ils push again\n"

	// bound is the time a command has from launch.
	bound = 100 * time.Second
	// grace is how long past the bound a run may come back.
	grace = 2 * time.Second
	// held is how long a held call stays open, far past the bound, and heldLeft is the time left
	// under the bound when a run with a held call is launched.
	held     = 30 * time.Second
	heldLeft = 3 * time.Second
)

// ran is what one run of the cli printed and the code it exited with.
type ran struct {
	stdout string
	stderr string
	code   int
}

// shown is the JSON renderer's object as a caller decodes it.
type shown struct {
	Command  string   `json:"command"`
	Outcome  string   `json:"outcome"`
	Next     string   `json:"next"`
	Reason   string   `json:"reason"`
	Excerpt  string   `json:"excerpt"`
	Words    []string `json:"words"`
	Lines    []string `json:"lines"`
	ExitCode int      `json:"exit_code"`
}

// launch runs app with args, launched now unless app says when, and returns what it printed.
func launch(t *testing.T, app cli.App, args ...string) ran {
	t.Helper()

	var stdout, stderr bytes.Buffer

	app.Stdout, app.Stderr = &stdout, &stderr
	if app.Launch.IsZero() {
		app.Launch = time.Now()
	}

	code := app.Run(t.Context(), args)

	return ran{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// outside returns the cli in a directory that is no repository.
func outside(t *testing.T) cli.App {
	t.Helper()

	return cli.App{Git: git.Runner{Dir: t.TempDir()}}
}

// decode reads the one JSON object out holds.
func decode(t *testing.T, out string) shown {
	t.Helper()

	var got shown

	err := json.Unmarshal([]byte(out), &got)
	if err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, out)
	}

	return got
}

// refusal is the grammar refusal of the words typed, for reason, as the JSON renderer shows it.
func refusal(reason string, typed ...string) shown {
	command := ""
	if len(typed) > 0 {
		command = typed[0]
	}

	return shown{Command: command, Words: typed, Outcome: "refused", Reason: reason, Next: nextHelp, ExitCode: exitRefused}
}

// finished is a push that ended as done with nothing to say.
func finished(req landing.Request) result.Result {
	return result.Result{Command: req.Command, Words: req.Words, Outcome: result.Done, Next: result.NextNone()}
}

// worded returns the phrase say builds from text as its own wording, for wording only a run
// knows, such as a path under a temporary directory.
func worded[W ~string](say func(W, ...any) result.Phrase, text string) result.Phrase {
	return say(W(text))
}

// wrote is a push that ended as done with one line, whose own wording names path.
func wrote(path string) cli.Command {
	return func(_ context.Context, _ landing.Deps, req landing.Request) result.Result {
		r := finished(req)
		r.Lines = []result.Phrase{worded(result.Say, "Wrote "+path)}

		return r
	}
}

// stored returns a repository whose git dir is no `.git`, so that a path under it is a git dir
// path to the one who resolved it and to no one else, and a path under that git dir.
func stored(t *testing.T) (f *gittest.Fixture, under string) {
	t.Helper()

	f = gittest.New(t, trunk)
	f.Git("init", "--quiet", "--separate-git-dir", filepath.Join(t.TempDir(), "store"))

	return f, f.Git("rev-parse", "--absolute-git-dir") + "/HEAD"
}

// timed runs `ils push` in a scene of sc, fails the test unless it exits as sc wants, and
// returns with what it printed how long past the bound it came back.
func timed(t *testing.T, sc scenario) (*scene, ran, time.Duration) {
	t.Helper()

	s := newScene(t, sc)
	app := s.app()

	got := launch(t, app, "push")
	late := time.Since(app.Launch.Add(bound))

	if got.code != sc.want {
		t.Fatalf("ils push exit code = %d, want %d; stdout:\n%s\nstderr:\n%s", got.code, sc.want, got.stdout, got.stderr)
	}

	return s, got, late
}

// heldChecks is a first push whose read of the checks is still open at the bound.
func heldChecks() scenario {
	slow := func(sha string) verdict {
		v := passing(sha)
		v.checks.Delay = held

		return v
	}

	return scenario{name: "the read of the checks", script: firstPush(slow), left: heldLeft, want: exitPending}
}

// heldIdentity is a push whose read of the repository's identity is still open at the bound.
func heldIdentity() scenario {
	calls := allowed()
	calls[1].Answers[0].Delay = held

	return scenario{name: "the read of the identity", script: only(calls...), left: heldLeft, want: exitRefused}
}

func TestRun_WhenWordsBreakTheGrammar_RefusesNamingTheWord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
		args   []string
	}{
		{name: "no word", reason: "no command was given"},
		{name: "unknown word", args: []string{"pish"}, reason: "pish is not a command"},
		{name: "extra word", args: []string{"push", "extra"}, reason: "push takes no other word, and extra was given"},
		{name: "branch", args: []string{"branch", "tidy"}, reason: "branch is not in this version"},
		{name: "merge", args: []string{"merge"}, reason: "merge is not in this version"},
		{name: "release", args: []string{"release", "1.2.3"}, reason: "release is not in this version"},
		{name: "status", args: []string{"status"}, reason: "status is not in this version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			want := ran{stdout: "Refused: " + tt.reason + "\nNext: " + nextHelp + "\n", code: exitRefused}
			if diff := cmp.Diff(want, got, cmp.AllowUnexported(ran{})); diff != "" {
				t.Errorf("ils %s mismatch (-want +got):\n%s", strings.Join(tt.args, " "), diff)
			}
		})
	}
}

func TestRun_WhenAskedForHelp_PrintsUsageWithTheByHandSequenceAndExitsZero(t *testing.T) {
	t.Parallel()

	tests := [][]string{{"-h"}, {"--help"}, {"push", "-h"}, {"push", "--help"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), args...)

			if got.code != exitDone || got.stderr != "" {
				t.Errorf("exit code = %d, stderr = %q, want 0 and nothing on stderr", got.code, got.stderr)
			}

			for _, want := range []string{"\nBy hand:\n", "  git push -u origin <name>\n", "\nThen, by hand:\n"} {
				if !strings.Contains(got.stdout, want) {
					t.Errorf("stdout = %q, want it to contain %q", got.stdout, want)
				}
			}
		})
	}
}

func TestRun_Help_ListsEachCommandAndSaysWhichAreNotInThisVersion(t *testing.T) {
	t.Parallel()

	const absent = "not in this version"

	tests := []struct {
		command string
		absent  bool
	}{
		{command: "push"},
		{command: "branch", absent: true},
		{command: "merge", absent: true},
		{command: "release", absent: true},
		{command: "status", absent: true},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), "--help")

			listed := slices.Collect(strings.Lines(got.stdout))
			at := slices.IndexFunc(listed, func(line string) bool { return strings.HasPrefix(line, "  ils "+tt.command) })
			if at < 0 {
				t.Fatalf("usage lists no command %q:\n%s", tt.command, got.stdout)
			}

			if strings.Contains(listed[at], absent) != tt.absent {
				t.Errorf("usage line %q, want it to say %q: %t", listed[at], absent, tt.absent)
			}
		})
	}
}

func TestRun_WhenJSONFlagIsAmongTheWords_RendersJSONWithoutTheFlagInTheWords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want shown
	}{
		{name: "flag alone", args: []string{"--json"}, want: refusal("no command was given")},
		{name: "flag first", args: []string{"--json", "pish"}, want: refusal("pish is not a command", "pish")},
		{name: "flag last", args: []string{"pish", "--json"}, want: refusal("pish is not a command", "pish")},
		{
			name: "flag between", args: []string{"push", "--json", "extra"},
			want: refusal("push takes no other word, and extra was given", "push", "extra"),
		},
		{name: "command of a later version", args: []string{"merge", "--json"}, want: refusal("merge is not in this version", "merge")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, outside(t), tt.args...)

			if got.code != exitRefused || got.stderr != "" {
				t.Errorf("exit code = %d, stderr = %q, want %d and nothing on stderr", got.code, got.stderr, exitRefused)
			}

			if diff := cmp.Diff(tt.want, decode(t, got.stdout), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("JSON on stdout mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRun_Push_OutsideARepository_RendersTheResultOfPush(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// want is the whole of stdout when whole is set, and a part of it otherwise.
		want  string
		args  []string
		whole bool
	}{
		{name: "text", args: []string{"push"}, want: outsideText, whole: true},
		{name: "json", args: []string{"push", "--json"}, want: `"command":"push","words":["push"],"outcome":"broke"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := outside(t)
			app.Git.Env = append(os.Environ(), "LC_ALL=C")

			got := launch(t, app, tt.args...)

			if got.code != exitBroke || got.stderr != "" {
				t.Errorf("exit code = %d, stderr = %q, want %d and nothing on stderr", got.code, got.stderr, exitBroke)
			}

			if !strings.Contains(got.stdout, tt.want) || tt.whole && got.stdout != tt.want {
				t.Errorf("stdout = %q, want %q (the whole of it: %t)", got.stdout, tt.want, tt.whole)
			}
		})
	}
}

func TestRun_WhenRendererRejectsTheResult_ExitsOneWithTheErrorOnStderrAndNothingOnStdout(t *testing.T) {
	t.Parallel()

	hooked := func(_ context.Context, _ landing.Deps, req landing.Request) result.Result {
		r := finished(req)
		r.Lines = []result.Phrase{result.Say(hookLine)}

		return r
	}

	tests := [][]string{{"push"}, {"push", "--json"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			app := outside(t)
			app.Push = hooked

			got := launch(t, app, args...)

			if got.code != exitBroke || got.stdout != "" {
				t.Errorf("exit code = %d, stdout = %q, want %d and nothing on stdout", got.code, got.stdout, exitBroke)
			}

			if !strings.Contains(got.stderr, hookLine) {
				t.Errorf("stderr = %q, want it to name the line %q", got.stderr, hookLine)
			}
		})
	}
}

func TestRun_WhenALineNamesAPathUnderTheGitDirOfTheRepository_ExitsOneWithNothingOnStdout(t *testing.T) {
	t.Parallel()

	tests := [][]string{{"push"}, {"push", "--json"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()

			f, under := stored(t)
			app := cli.App{Git: git.Runner{Dir: f.Dir(), Env: f.Env()}, Push: wrote(under)}

			got := launch(t, app, args...)

			if got.code != exitBroke || got.stdout != "" {
				t.Errorf("exit code = %d, stdout = %q, want %d and nothing on stdout", got.code, got.stdout, exitBroke)
			}

			if !strings.Contains(got.stderr, "Wrote "+under) {
				t.Errorf("stderr = %q, want it to name the line %q", got.stderr, "Wrote "+under)
			}
		})
	}
}

func TestRun_WhenALineNamesAPathUnderTheGitDirOfAnotherDirectory_RendersTheLine(t *testing.T) {
	t.Parallel()

	_, under := stored(t)
	app := outside(t)
	app.Push = wrote(under)

	got := launch(t, app, "push")

	want := ran{stdout: "Wrote " + under + "\nNext: none\n", code: exitDone}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(ran{})); diff != "" {
		t.Errorf("ils push outside a repository mismatch (-want +got):\n%s", diff)
	}
}

func TestRun_Push_GivesTheCommandADeadlineOneHundredSecondsAfterLaunch(t *testing.T) {
	t.Parallel()

	var (
		deadline time.Time
		bounded  bool
	)

	app := outside(t)
	app.Launch = time.Now().Add(-30 * time.Second)
	app.Push = func(ctx context.Context, _ landing.Deps, req landing.Request) result.Result {
		deadline, bounded = ctx.Deadline()

		return finished(req)
	}

	got := launch(t, app, "push")

	if got.code != exitDone {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", got.code, got.stderr)
	}

	if want := app.Launch.Add(bound); !bounded || !deadline.Equal(want) {
		t.Errorf("deadline of the command's context = %v (set: %t), want %v", deadline, bounded, want)
	}
}

func TestRun_Push_NamesTheRequestByTheWordsTypedWithoutTheJSONFlag(t *testing.T) {
	t.Parallel()

	var asked landing.Request

	app := outside(t)
	app.Push = func(_ context.Context, _ landing.Deps, req landing.Request) result.Result {
		asked = req

		return finished(req)
	}

	got := launch(t, app, "--json", "push")

	if got.code != exitDone {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", got.code, got.stderr)
	}

	want := landing.Request{Command: "push", Words: []string{"push"}}
	if diff := cmp.Diff(want, asked); diff != "" {
		t.Errorf("request handed to push mismatch (-want +got):\n%s", diff)
	}
}

func TestRun_Push_WhenTheBoundEndedBeforeAnyRead_RefusesAndPushesNothing(t *testing.T) {
	s := newScene(t, greenFirstPush())
	app := s.app()
	app.Launch = time.Now().Add(-200 * time.Second)

	got := launch(t, app, "push")

	if got.code != exitRefused || !strings.HasPrefix(got.stdout, "Refused: the time limit ended ") {
		t.Errorf("exit code = %d, stdout = %q, want %d and a refusal naming the time limit", got.code, got.stdout, exitRefused)
	}

	if on := s.fixture.OriginGit("for-each-ref", "refs/heads/"+topic); on != "" {
		t.Errorf("origin's copy of %s = %q, want none", topic, on)
	}
}

func TestRun_Push_WhenChecksStillRunAtTheBound_EndsPendingWithoutOutlastingIt(t *testing.T) {
	_, got, late := timed(t, pendingAtTheBound())

	if late > grace {
		t.Errorf("ils push returned %s past the bound, want it back no later than %s past it", late, grace)
	}

	if want := "\nNext: ils push\n"; !strings.HasSuffix(got.stdout, want) {
		t.Errorf("stdout = %q, want it to end with %q", got.stdout, want)
	}
}

func TestRun_Push_WhenACallIsStillOpenAtTheBound_CancelsItAndEndsByTheBound(t *testing.T) {
	const cut = "Refused: the time limit ended while reading the repository's identity on GitHub; nothing was written\n"

	tests := []struct {
		// wantStart and wantEnd are how stdout starts and ends.
		wantStart string
		wantEnd   string
		scenario  scenario
		// delivered says whether the branch is on origin at its tip and its pull request opened.
		delivered bool
	}{
		{scenario: heldChecks(), wantStart: "Pushed " + topic + " at ", wantEnd: "\nNext: ils push\n", delivered: true},
		{scenario: heldIdentity(), wantStart: cut, wantEnd: cut + "Next: ils push\n"},
	}
	for _, tt := range tests {
		t.Run(tt.scenario.name, func(t *testing.T) {
			s, got, late := timed(t, tt.scenario)

			if late > grace {
				t.Errorf("ils push returned %s past the bound, want it back no later than %s past it", late, grace)
			}

			if !strings.HasPrefix(got.stdout, tt.wantStart) || !strings.HasSuffix(got.stdout, tt.wantEnd) {
				t.Errorf("stdout = %q, want it to start with %q and end with %q", got.stdout, tt.wantStart, tt.wantEnd)
			}

			onOrigin := s.fixture.OriginGit("for-each-ref", "--points-at", tipOf(s.fixture), "refs/heads/"+topic)
			opening := slices.ContainsFunc(s.fake.Calls(), func(argv []string) bool {
				return slices.Equal(argv[:min(3, len(argv))], []string{"gh", "pr", "create"})
			})
			if pushed := onOrigin != ""; pushed != tt.delivered || opening != tt.delivered {
				t.Errorf("origin's copy of %s = %q and pull request opened = %t, want both delivered: %t",
					topic, onOrigin, opening, tt.delivered)
			}
		})
	}
}

// both runs `ils push` once in the scene and returns its one result as the text renderer and
// as the JSON renderer print it.
func both(t *testing.T, s *scene) (text, encoded ran) {
	t.Helper()

	var kept result.Result

	app := s.app()
	app.Push = func(ctx context.Context, deps landing.Deps, req landing.Request) result.Result {
		kept = landing.Push(ctx, deps, req)

		return kept
	}
	text = launch(t, app, "push")

	app.Push = func(context.Context, landing.Deps, landing.Request) result.Result { return kept }
	encoded = launch(t, app, "push", "--json")

	return text, encoded
}

// assertCarried fails unless every line of text is in what the JSON object carries: its
// lines, its excerpt, its reason, or its next.
func assertCarried(t *testing.T, text string, got shown) {
	t.Helper()

	carried := append(strings.Split(got.Excerpt, "\n"), got.Lines...)
	carried = append(carried, "Next: "+got.Next, "Refused: "+got.Reason)

	for line := range strings.Lines(text) {
		line = strings.TrimSuffix(line, "\n")
		if !slices.Contains(carried, line) {
			t.Errorf("text line %q is not carried by the JSON: %+v", line, got)
		}
	}
}

func TestRun_Push_ForEveryOutcome_JSONCarriesWhatTheTextShows(t *testing.T) {
	tests := []struct {
		outcome  string
		scenario scenario
	}{
		{outcome: "done", scenario: greenFirstPush()},
		{outcome: "refused", scenario: onTheTrunk()},
		{outcome: "broke", scenario: failedRead()},
		{outcome: "pending", scenario: pendingAtTheBound()},
		{outcome: "delivered-not-green", scenario: redWithLog()},
	}
	for _, tt := range tests {
		t.Run(tt.outcome, func(t *testing.T) {
			s := newScene(t, tt.scenario)

			text, encoded := both(t, s)

			if text.code != tt.scenario.want || encoded.code != tt.scenario.want {
				t.Errorf("exit codes = %d as text and %d as JSON, want %d for both", text.code, encoded.code, tt.scenario.want)
			}

			got := decode(t, encoded.stdout)
			if got.Outcome != tt.outcome || got.ExitCode != tt.scenario.want {
				t.Errorf("JSON outcome = %q (exit %d), want %q (exit %d)", got.Outcome, got.ExitCode, tt.outcome, tt.scenario.want)
			}

			assertCarried(t, text.stdout, got)
		})
	}
}
