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

// settingsWithAllow writes the hook `ils init claude` installs, the ask and deny rules it adds, and
// allow as the allow list as the settings file under home and returns its path.
func settingsWithAllow(t *testing.T, home string, allow []any) string {
	t.Helper()

	return withSettings(t, home, encodedJSON(t, map[string]any{
		"hooks": map[string]any{"PreToolUse": []any{hookGroup(cli.UserHook)}},
		"permissions": map[string]any{
			"allow": allow,
			"ask":   ruleList("ask"),
			"deny":  ruleList("deny"),
		},
	}))
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

func TestApp_Run_WhenInitClaudeRunsWhereTheGuardDoesNot_WritesTheRulesBesideTheHooksAsTheyAre(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	app.SetGuardSupported(false)
	groups := []any{
		matcherGroup("Edit", commandHook(cli.UserHook)),
		matcherGroup("Bash", map[string]any{"type": "command", "command": cli.ProjectHook, "async": true}),
	}
	path := withPreToolUse(t, home, groups...)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	want := map[string]any{"hooks": map[string]any{"PreToolUse": groups}, "permissions": ruleLists()}
	assert.Check(t, is.DeepEqual(decodedFile(t, path), want))
}

func TestApp_Run_WhenInitClaudeRunsWhereTheGuardDoesNot_MarksTheHookStepUnchanged(t *testing.T) {
	t.Parallel()

	app, _ := installing(t)
	app.SetGuardSupported(false)

	got := settingsStepsShown(t, app)

	assert.Check(t, strings.HasPrefix(got[0], "– "), "line = %q", got[0])
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

func TestApp_Run_WhenInitClaudeFindsARuleInAnotherList_LeavesItThereAlone(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	rule := firstRule("allow")
	path := withSettings(t, home, encodedJSON(t, map[string]any{
		"permissions": map[string]any{"ask": []any{rule}},
	}))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.DeepEqual(listsHolding(decodedFile(t, path), rule), []string{"ask"}))
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

// refusedSettings runs `ils init claude --json` over a settings file that holds text, written long
// ago, and returns what it printed and the path of the file.
func refusedSettings(t *testing.T, text string) (got ran, path string) {
	t.Helper()

	app, home := installing(t)
	path = withSettings(t, home, text)
	backdated(t, path)

	return launch(t, app, "init", "claude", "--json"), path
}

func TestApp_Run_WhenInitClaudeFindsAMemberTwice_ExitsTwoNamingTheMember(t *testing.T) {
	t.Parallel()

	got, path := refusedSettings(t, `{"theme": "dark", "theme": "light"}`)

	assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
	refusal := resulttest.Decode(t, got.stdout)
	assert.Check(t, is.Contains(withoutPath(refusal.Reason, path), "theme"))
	assert.Check(t, is.Contains(refusal.Next, path))
	assertUntouched(t, path)
}

func TestApp_Run_WhenInitClaudeFindsAnEscapeOfNoCharacter_ExitsTwoWithAnotherReasonThanNotJSON(t *testing.T) {
	t.Parallel()

	notJSON, notJSONPath := refusedSettings(t, `{"theme": `)

	got, path := refusedSettings(t, `{"theme": "\q"}`)

	assert.Check(t, is.Equal(got.code, exitRefused), "stdout = %q", got.stdout)
	reason := withoutPath(resulttest.Decode(t, got.stdout).Reason, path)
	assert.Check(t, reason != withoutPath(resulttest.Decode(t, notJSON.stdout).Reason, notJSONPath), "reason = %q", reason)
	assertUntouched(t, path)
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
	added := strconv.Itoa(len(ruleList("allow")) - 1)
	assert.Check(t, is.Contains(wordsOutside(lineWith(t, got.stdout, "allow"), path), added))
	assert.Check(t, is.Contains(wordsOutside(lineWith(t, got.stdout, "deny"), path), strconv.Itoa(len(ruleList("deny")))))
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

// shippedSkills is the number of step lines `ils init claude` reports before the settings steps:
// one per shipped skill.
const shippedSkills = 2

// settingsStepsShown runs `ils init claude` with app on a terminal, stops the test unless it ends
// done with one step line per shipped skill, the hook and each permission list, and returns the
// lines of the settings steps: the hook's, then those of allow, ask and deny.
func settingsStepsShown(t *testing.T, app cli.App) []string {
	t.Helper()

	app, stdout := onInitScreen(t, app)
	got := shown(t, app, "init", "claude")

	lines := stdout.lines()
	assert.Assert(t, is.Equal(got.code, exitDone), "stdout showed %q", lines)
	assert.Assert(t, is.Len(lines, shippedSkills+4), "stdout showed %q", lines)

	return lines[shippedSkills:]
}

func TestApp_Run_WhenInitClaudeFindsSomeRulesInAnyList_MarksChangedOnlyTheHookAndTheListsThatGrew(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	path := withSettings(t, home, encodedJSON(t, map[string]any{
		"permissions": map[string]any{
			"allow": ruleList("allow")[:1],
			"deny":  ruleList("ask"),
		},
	}))

	got := settingsStepsShown(t, app)

	lists := []string{withoutPath(got[1], path), withoutPath(got[2], path), withoutPath(got[3], path)}
	assertShownSteps(t, got[:1], []shownStep{{mark: "✓", word: path}})
	assertShownSteps(t, lists, []shownStep{{mark: "✓", word: "allow"}, {mark: "–", word: "ask"}, {mark: "✓", word: "deny"}})
}

func TestApp_Run_WhenInitClaudeFindsEverySettingInPlace_LeavesTheFileUntouched(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	path := withSettings(t, home,
		fmt.Sprintf(`{"hooks": {"PreToolUse": [%s]}, "permissions": %s}`, inPlaceEntry(), encodedJSON(t, ruleLists())))
	backdated(t, path)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assertUntouched(t, path)
}

func TestApp_Run_WhenInitClaudeFindsThePushRulesAnEarlierRunAdded_RemovesThemFromAllow(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	allow := append([]any{"Bash(ils push)", "Read", "Bash(ils push --wait)"}, ruleList("allow")...)
	path := settingsWithAllow(t, home, allow)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	permissions, _ := decodedFile(t, path)["permissions"].(map[string]any)
	assert.Check(t, is.DeepEqual(permissions["allow"], append([]any{"Read"}, ruleList("allow")...)))
}

func TestApp_Run_WhenInitClaudeOnlyRemovesThePushRules_CountsTheRemovedRulesInTheAllowStep(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	allow := append([]any{"Bash(ils push)", "Read", "Bash(ils push --wait)"}, ruleList("allow")...)
	path := settingsWithAllow(t, home, allow)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(wordsOutside(lineWith(t, got.stdout, "allow"), path), "2"))
}

func TestApp_Run_WhenInitClaudeAddsARuleAndRemovesThePushRules_CountsBothInTheAllowStep(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	allow := append([]any{"Bash(ils push)", "Bash(ils push --wait)"}, ruleList("allow")[1:]...)
	path := settingsWithAllow(t, home, allow)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	counts := slices.DeleteFunc(wordsOutside(lineWith(t, got.stdout, "allow"), path), func(word string) bool {
		_, err := strconv.Atoi(word)
		return err != nil
	})
	assert.Check(t, is.DeepEqual(counts, []string{"1", "2"}))
}
