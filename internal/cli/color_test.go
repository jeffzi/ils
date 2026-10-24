package cli_test

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/result/resulttest"
)

// encodedEscape is escape as the JSON encoder writes it inside a string.
const encodedEscape = `\u001b`

// noEncodedEscape checks, without stopping the test, that out carries no escape sequence as the
// JSON encoder writes it inside a string; what names out in the failure message.
func noEncodedEscape(t *testing.T, what, out string) {
	t.Helper()

	assert.Check(t, !strings.Contains(strings.ToLower(out), encodedEscape), "a value of %s carries an escape sequence:\n%s", what, out)
}

func TestApp_Run_WhenColorIsForced_RefusalCarriesNoEscapeSequence(t *testing.T) {
	t.Setenv(forceColor, "1")

	tests := [][]string{{"pish"}, {"pish", "--json"}}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got := launch(t, outside(t), args...)

			assert.Check(t, is.Equal(got.code, exitRefused))
			plain(t, "the refusal", got.stdout)
			noEncodedEscape(t, "the refusal", got.stdout)
		})
	}
}

func TestApp_Run_WhenColorIsForced_PushResultCarriesNoEscapeSequence(t *testing.T) {
	t.Setenv(forceColor, "1")

	text, encoded := both(t, newScene(t, redWithLog()))

	plain(t, "the text", text.stdout)
	plain(t, "the JSON", encoded.stdout)
	noEncodedEscape(t, "the JSON", encoded.stdout)
}

func TestApp_Run_WhenNoStreamHasAColorProfile_WritesNoEscapeSequence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      []string
		args     []string
		terminal bool
		logs     bool
	}{
		{name: "no terminal", args: []string{"push", "-d"}, logs: true},
		{name: "terminal forced off a terminal", env: []string{colorTerm, "TTY_FORCE=1"}, args: []string{"push", "-d"}, logs: true},
		{name: "dumb terminal", env: []string{"TERM=dumb"}, terminal: true, args: []string{"push", "-d"}, logs: true},
		{name: "no color on a terminal", env: []string{colorTerm, noColor}, terminal: true, args: []string{"merge", "--debug"}, logs: true},
		{name: "no color over forced color", env: []string{colorTerm, noColor, forced}, args: []string{"push", "-d"}, logs: true},
		{
			name: "no color over forced color on a terminal", env: []string{colorTerm, noColor, forced}, terminal: true,
			args: []string{"push", "-d"}, logs: true,
		},
		{name: "flag over forced color", env: []string{colorTerm, forced}, terminal: true, args: []string{"push", "-d", "--no-color"}, logs: true},
		{
			name: "flag set to true before merge", env: []string{colorTerm, forced}, terminal: true,
			args: []string{"--no-color=true", "merge", "-d"}, logs: true,
		},
		{name: "refusal under no color", env: []string{colorTerm, noColor}, terminal: true, args: []string{"pish"}},
		{name: "flag before an unknown word", env: []string{colorTerm}, terminal: true, args: []string{"--no-color", "pish"}},
		{name: "flag after an unknown flag", env: []string{colorTerm}, terminal: true, args: []string{"push", "--bogus", "--no-color"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, painted(t, tt.env, tt.terminal, tt.terminal), tt.args...)

			assert.Check(t, is.Equal(got.stderr != "", tt.logs), "ils %s wrote stderr = %q", strings.Join(tt.args, " "), got.stderr)
			plain(t, "stdout", got.stdout)
			plain(t, "stderr", got.stderr)
		})
	}
}

func TestApp_Run_WhenStdoutHasAColorProfile_DrawsTheOutcomeLabelInColor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		label    string
		env      []string
		args     []string
		terminal bool
	}{
		{name: "color terminal", label: "Broke", env: []string{colorTerm}, terminal: true, args: []string{"push"}},
		{name: "forced color off a terminal", label: "Broke", env: []string{forced}, args: []string{"push"}},
		{name: "flag set to false", label: "Broke", env: []string{colorTerm}, terminal: true, args: []string{"merge", "--no-color=false"}},
		{name: "unknown word", label: "Refused", env: []string{colorTerm}, terminal: true, args: []string{"pish"}},
		{name: "color flag set to neither", label: "Refused", env: []string{forced}, args: []string{"merge", "--no-color=maybe"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, painted(t, tt.env, tt.terminal, false), tt.args...)

			look := resulttest.Draw(t, got.stdout).LookOf(t, tt.label+": ", tt.label)
			assert.Check(t, look.Color != "", "%s label drawn with no color", tt.label)
		})
	}
}

func TestApp_Run_WhenOneStreamIsAColorTerminal_DrawsColorOnThatStreamOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stdout bool
		stderr bool
	}{
		{name: "stdout", stdout: true},
		{name: "stderr", stderr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := launch(t, painted(t, []string{colorTerm}, tt.stdout, tt.stderr), "push", "-d")

			assert.Check(t, is.Equal(strings.Contains(got.stdout, escape), tt.stdout), "stdout = %q", got.stdout)
			assert.Check(t, is.Equal(strings.Contains(got.stderr, escape), tt.stderr), "stderr = %q", got.stderr)
		})
	}
}

func TestApp_Run_WhenStdoutIsAColorTerminalAndTheJSONFlagIsAmongTheWords_PrintsNoEscapeSequence(t *testing.T) {
	t.Parallel()

	got := launch(t, painted(t, []string{colorTerm, forced}, true, false), "push", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke))
	plain(t, "the JSON", got.stdout)
	noEncodedEscape(t, "the JSON", got.stdout)
}

func TestApp_Run_WhenColorFlagIsAmongTheWords_PrintsWhatTheSameRunPrintsWithoutIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args    []string
		flagged []string
	}{
		{args: []string{"push", "--json"}, flagged: []string{"--no-color", "push", "--json"}},
		{args: []string{"merge", "--json"}, flagged: []string{"merge", "--json", "--no-color=true"}},
		{args: []string{"merge"}, flagged: []string{"merge", "--no-color=false"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.flagged, " "), func(t *testing.T) {
			t.Parallel()

			app := lost(t)
			without := launch(t, app, tt.args...)

			got := launch(t, app, tt.flagged...)

			assert.Check(t, is.Equal(without.stderr, ""))
			assert.Check(t, is.Equal(got.stdout, without.stdout))
			assert.Check(t, is.Equal(got.code, without.code))
			assert.Check(t, is.Equal(got.stderr, ""))
		})
	}
}
