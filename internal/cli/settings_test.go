package cli_test

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// ruleList returns the permission rules `ils init claude` adds to the list name.
func ruleList(name string) []any {
	rules := cli.PermissionRules()[name]
	got := make([]any, len(rules))
	for i, rule := range rules {
		got[i] = rule
	}

	return got
}

// ruleLists returns the permission rules `ils init claude` adds to each list, by the name of the list.
func ruleLists() map[string]any {
	lists := map[string]any{}
	for name := range cli.PermissionRules() {
		lists[name] = ruleList(name)
	}

	return lists
}

// firstRule returns the first permission rule `ils init claude` adds to the list name.
func firstRule(name string) string {
	return cli.PermissionRules()[name][0]
}

func settingsUnder(dir string) string {
	return filepath.Join(dir, ".claude", "settings.json")
}

// withSettings writes text as the settings file under home and returns its path.
func withSettings(t *testing.T, home, text string) string {
	t.Helper()

	path := settingsUnder(home)
	writeText(t, path, text)

	return path
}

func encodedJSON(t *testing.T, value any) string {
	t.Helper()

	text, err := json.Marshal(value, json.Deterministic(true))
	assert.NilError(t, err)

	return string(text)
}

func decodedFile(t *testing.T, path string) map[string]any {
	t.Helper()

	text := readText(t, path)

	var value map[string]any
	assert.NilError(t, json.Unmarshal([]byte(text), &value), "settings = %s", text)

	return value
}

// preToolUse returns the PreToolUse hooks of the settings file at path.
func preToolUse(t *testing.T, path string) any {
	t.Helper()

	hooks, _ := decodedFile(t, path)["hooks"].(map[string]any)

	return hooks["PreToolUse"]
}

// member returns the member name of the JSON object in text, as JSON text.
func member(t *testing.T, text jsontext.Value, name string) jsontext.Value {
	t.Helper()

	var members map[string]jsontext.Value
	assert.NilError(t, json.Unmarshal(text, &members))
	value, ok := members[name]
	assert.Assert(t, ok, "no member %q in %s", name, text)

	return value
}

// names returns the names of the JSON object in text, in the order the text holds them.
func names(t *testing.T, text jsontext.Value) []string {
	t.Helper()

	dec := jsontext.NewDecoder(bytes.NewReader(text))
	start, err := dec.ReadToken()
	assert.NilError(t, err)
	assert.Assert(t, start.Kind() == '{', "not an object: %s", text)

	var got []string
	for dec.PeekKind() != '}' {
		name, err := dec.ReadToken()
		assert.NilError(t, err)
		got = append(got, name.String())
		assert.NilError(t, dec.SkipValue())
	}

	return got
}

// listsHolding returns the permission lists of settings that hold rule, once per time each holds it.
func listsHolding(settings map[string]any, rule string) []string {
	permissions, _ := settings["permissions"].(map[string]any)

	var got []string
	for _, list := range []string{"allow", "ask", "deny"} {
		entries, _ := permissions[list].([]any)
		for _, entry := range entries {
			if entry == rule {
				got = append(got, list)
			}
		}
	}

	return got
}

// commandHook returns the hook entry that runs command and sets nothing else.
func commandHook(command string) map[string]any {
	return map[string]any{"type": "command", "command": command}
}

// guardEntry returns the hook entry of the guard that runs command, with the timeout and failure
// the guard needs.
func guardEntry(command any) map[string]any {
	return map[string]any{
		"type": "command", "command": command, "timeout": cli.HookTimeout, "onFailure": cli.HookOnFailure,
	}
}

// hookGroup returns the PreToolUse group that runs command as the guard's hook.
func hookGroup(command string) map[string]any {
	return map[string]any{"matcher": "Bash", "hooks": []any{guardEntry(command)}}
}

// lineWith returns the one line of text that holds word, and stops the test unless there is one.
func lineWith(t *testing.T, text, word string) string {
	t.Helper()

	var found []string
	for _, line := range outputLines(text) {
		if strings.Contains(line, word) {
			found = append(found, line)
		}
	}
	assert.Assert(t, is.Len(found, 1), "lines holding %q in stdout = %q", word, text)

	return found[0]
}

// userSettings is a settings file a user wrote: keys out of alphabetical order at every depth, a
// rule in two of the permission lists, and a hook of their own on PreToolUse and on PostToolUse.
const userSettings = `{
  "model": "opus",
  "permissions": {
    "deny": ["Bash(rm *)"],
    "defaultMode": "acceptEdits",
    "allow": ["Read"]
  },
  "env": {"ZED": "1", "ALPHA": "2"},
  "hooks": {
    "PostToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "fmt"}]}],
    "PreToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "check"}]}]
  }
}
`

func TestApp_Run_WhenInitClaudeFindsNoSettings_WritesTheHookAndTheRulesAlone(t *testing.T) {
	t.Parallel()

	app, home := installing(t)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	want := map[string]any{
		"hooks":       map[string]any{"PreToolUse": []any{hookGroup(cli.UserHook)}},
		"permissions": ruleLists(),
	}
	assert.Check(t, is.DeepEqual(decodedFile(t, settingsUnder(home)), want))
}

func TestApp_Run_WhenInitClaudeRunsWhereTheGuardDoesNot_WritesTheRulesWithoutAHook(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	app.SetGuardSupported(false)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	want := map[string]any{"permissions": ruleLists()}
	assert.Check(t, is.DeepEqual(decodedFile(t, settingsUnder(home)), want))
}

func TestApp_Run_WhenInitClaudeFindsSettings_KeepsTheOrderOfTheUsersKeys(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	path := withSettings(t, home, userSettings)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	settings := jsontext.Value(readText(t, path))
	assert.Check(t, is.DeepEqual(names(t, settings), []string{"model", "permissions", "env", "hooks"}))
	assert.Check(t, is.DeepEqual(names(t, member(t, settings, "permissions"))[:3], []string{"deny", "defaultMode", "allow"}))
	assert.Check(t, is.DeepEqual(names(t, member(t, settings, "hooks")), []string{"PostToolUse", "PreToolUse"}))
}

func TestApp_Run_WhenInitClaudeFindsSettings_AppendsItsOwnAfterTheUsersValues(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	path := withSettings(t, home, userSettings)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	want := map[string]any{
		"model": "opus",
		"permissions": map[string]any{
			"deny":        slices.Concat([]any{"Bash(rm *)"}, ruleList("deny")),
			"defaultMode": "acceptEdits",
			"allow":       slices.Concat([]any{"Read"}, ruleList("allow")),
			"ask":         ruleList("ask"),
		},
		"env": map[string]any{"ZED": "1", "ALPHA": "2"},
		"hooks": map[string]any{
			"PostToolUse": []any{map[string]any{"matcher": "Edit", "hooks": []any{commandHook("fmt")}}},
			"PreToolUse": []any{
				map[string]any{"matcher": "Edit", "hooks": []any{commandHook("check")}},
				hookGroup(cli.UserHook),
			},
		},
	}
	assert.Check(t, is.DeepEqual(decodedFile(t, path), want))
}

func TestApp_Run_WhenInitClaudeFindsARuleInAnyList_LeavesItThereAlone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rule string
		list string
	}{
		{name: "allow rule moved to ask", rule: firstRule("allow"), list: "ask"},
		{name: "deny rule moved to allow", rule: firstRule("deny"), list: "allow"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)
			path := withSettings(t, home, encodedJSON(t, map[string]any{
				"permissions": map[string]any{tt.list: []any{tt.rule}},
			}))

			got := launch(t, app, "init", "claude")

			assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
			assert.Check(t, is.DeepEqual(listsHolding(decodedFile(t, path), tt.rule), []string{tt.list}))
		})
	}
}

func TestApp_Run_WhenInitClaudeFindsSettingsOfTheWrongShape_ExitsTwoNamingTheFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings string
		noGuard  bool
	}{
		{name: "not JSON", settings: `{"model": `},
		{name: "not an object", settings: `[]`},
		{name: "hooks not an object", settings: `{"hooks": []}`},
		{name: "PreToolUse not an array", settings: `{"hooks": {"PreToolUse": {}}}`},
		{name: "permissions not an object", settings: `{"permissions": "all"}`},
		{name: "allow not an array", settings: `{"permissions": {"allow": {}}}`},
		{
			name: "PreToolUse not an array where the guard does not run", settings: `{"hooks": {"PreToolUse": {}}}`,
			noGuard: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)
			app.SetGuardSupported(!tt.noGuard)
			path := withSettings(t, home, tt.settings)

			got := launch(t, app, "init", "claude", "--json")

			assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
			assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, path))
		})
	}
}

func TestApp_Run_WhenInitClaudeCannotReadTheSettings_ExitsOneNamingTheFile(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	path := settingsUnder(home)
	assert.NilError(t, os.MkdirAll(path, 0o750))

	got := launch(t, app, "init", "claude", "--json")

	assert.Check(t, is.Equal(got.code, exitBroke), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(resulttest.Decode(t, got.stdout).Reason, path))
}

func TestApp_Run_WhenInitClaudeRunsTwice_LeavesTheSettingsAsTheFirstRunLeftThem(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	path := withSettings(t, home, userSettings)
	installed(t, app)
	first := readText(t, path)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Equal(readText(t, path), first))
}

func TestApp_Run_WhenInitClaudeChangesOnlyTheHook_PrintsALineNamingTheSettings(t *testing.T) {
	t.Parallel()

	rules := ruleLists()
	tests := []struct {
		settings map[string]any
		name     string
	}{
		{name: "hook missing", settings: map[string]any{"permissions": rules}},
		{
			name: "hook without timeout and failure",
			settings: map[string]any{
				"permissions": rules,
				"hooks": map[string]any{"PreToolUse": []any{map[string]any{
					"matcher": "Bash",
					"hooks":   []any{commandHook(cli.UserHook)},
				}}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)
			installed(t, app)
			path := withSettings(t, home, encodedJSON(t, tt.settings))

			got := launch(t, app, "init", "claude")

			assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
			assert.Check(t, is.Contains(got.stdout, path))
		})
	}
}

func TestApp_Run_WhenInitClaudeAddsRules_PrintsTheNumberAddedToEachListThatGrew(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	path := withSettings(t, home, encodedJSON(t, map[string]any{
		"hooks": map[string]any{"PreToolUse": []any{hookGroup(cli.UserHook)}},
		"permissions": map[string]any{
			"allow": ruleList("allow")[:1],
			"ask":   ruleList("ask"),
			"deny":  []any{"Bash(rm *)"},
		},
	}))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(wordsOutside(lineWith(t, got.stdout, "allow"), path), "3"))
	assert.Check(t, is.Contains(wordsOutside(lineWith(t, got.stdout, "deny"), path), "2"))
	assert.Check(t, !strings.Contains(got.stdout, "ask"), "stdout = %q", got.stdout)
}

// wordsOutside returns the words of line once path is taken out of it, so that no digit of the
// path reads as a word of the line.
func wordsOutside(line, path string) []string {
	return strings.Fields(strings.ReplaceAll(line, path, ""))
}

// withoutPath returns line once path is taken out of it, so that no word of the path reads as a
// word of the line.
func withoutPath(line, path string) string {
	return strings.ReplaceAll(line, path, "")
}

func TestSettingsSteps_ReportTheHookThenEachListAsChangedOnlyWhenItAddedARule(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := withSettings(t, home, encodedJSON(t, map[string]any{
		"permissions": map[string]any{
			"allow": ruleList("allow")[:1],
			"deny":  ruleList("ask"),
		},
	}))

	got, err := cli.SettingsSteps(filepath.Dir(path), cli.UserHook)

	assert.NilError(t, err)
	kinds := make([]cli.StepKind, len(got))
	for i, step := range got {
		kinds[i] = step.Kind
		assert.Check(t, is.Contains(step.Text, path), "step %d", i)
	}
	assert.Check(t, is.DeepEqual(kinds, []cli.StepKind{cli.StepChanged, cli.StepChanged, cli.StepUnchanged, cli.StepChanged}))
	assert.Assert(t, is.Len(got, 4))
	for i, list := range []string{"allow", "ask", "deny"} {
		assert.Check(t, is.Contains(withoutPath(got[i+1].Text, path), list), "step %d", i+1)
	}
	assert.Check(t, is.Contains(wordsOutside(got[1].Text, path), strconv.Itoa(len(ruleList("allow"))-1)))
	assert.Check(t, is.Contains(wordsOutside(got[3].Text, path), strconv.Itoa(len(ruleList("deny")))))
}

func TestApp_Run_WhenInitClaudeFindsEverySettingInPlace_LeavesTheFileUntouched(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		group   string
		noGuard bool
	}{
		{name: "guard entry keys in another order", group: inPlaceEntry()},
		{
			name: "guard entry with another key where the guard does not run",
			group: fmt.Sprintf(`{"matcher": "Edit", "hooks": [{"type": "command", "command": %q, "async": true}]}`,
				cli.UserHook),
			noGuard: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)
			app.SetGuardSupported(!tt.noGuard)
			installed(t, app)
			text := fmt.Sprintf(`{"hooks": {"PreToolUse": [%s]}, "permissions": %s}`, tt.group, encodedJSON(t, ruleLists()))
			path := withSettings(t, home, text)
			longAgo := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
			assert.NilError(t, os.Chtimes(path, longAgo, longAgo))

			got := launch(t, app, "init", "claude")

			assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
			assert.Check(t, is.Equal(readText(t, path), text))
			info, err := os.Stat(path)
			assert.NilError(t, err)
			assert.Check(t, info.ModTime().Equal(longAgo), "settings written again at %v", info.ModTime())
		})
	}
}
