package cli_test

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// The commands the hook of the guard runs, at user scope and at project scope.
const (
	userHook    = "bash ~/.claude/skills/ils/hook.sh"
	projectHook = `bash "$CLAUDE_PROJECT_DIR"/.claude/skills/ils/hook.sh`
)

// allowRules returns the permission rules `ils init claude` adds to the allow list.
func allowRules() []any {
	return []any{"Bash(ils push)", "Bash(ils push --wait)", "Bash(ils branch *)", "Bash(ils status)"}
}

// askRules returns the permission rules `ils init claude` adds to the ask list.
func askRules() []any {
	return []any{
		"Bash(ils merge)", "Bash(ils merge *)", "Bash(ils release)", "Bash(ils release *)",
		"Bash(gh pr merge)", "Bash(gh pr merge *)", "Bash(gh release create)", "Bash(gh release create *)",
	}
}

// denyRules returns the permission rules `ils init claude` adds to the deny list.
func denyRules() []any {
	return []any{"Bash(git push)", "Bash(git push *)"}
}

// settingsUnder returns the path of the settings file of the Claude Code directory under dir.
func settingsUnder(dir string) string {
	return filepath.Join(dir, ".claude", "settings.json")
}

// withSettings writes text as the settings file under home and returns its path.
func withSettings(t *testing.T, home, text string) string {
	t.Helper()

	path := settingsUnder(home)
	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	assert.NilError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

// encodedJSON returns value as JSON text.
func encodedJSON(t *testing.T, value any) string {
	t.Helper()

	text, err := json.Marshal(value, json.Deterministic(true))
	assert.NilError(t, err)

	return string(text)
}

// decodedFile reads the JSON file at path and returns its value.
func decodedFile(t *testing.T, path string) map[string]any {
	t.Helper()

	//nolint:gosec // G304: the path is one the test built under t.TempDir().
	text, err := os.ReadFile(path)
	assert.NilError(t, err)

	var value map[string]any
	assert.NilError(t, json.Unmarshal(text, &value), "settings = %s", text)

	return value
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

// hookGroup returns the PreToolUse group that runs command as the guard's hook.
func hookGroup(command string) map[string]any {
	return map[string]any{
		"matcher": "Bash",
		"hooks": []any{map[string]any{
			"type": "command", "command": command, "timeout": float64(15), "onFailure": "block",
		}},
	}
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

// filesUnder returns the content of every regular file under dir, by its path.
func filesUnder(t *testing.T, dir string) map[string]string {
	t.Helper()

	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		//nolint:gosec // G304: the path is one WalkDir found under t.TempDir().
		text, err := os.ReadFile(path)
		files[path] = string(text)

		return err
	})
	assert.NilError(t, err)

	return files
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
		"hooks":       map[string]any{"PreToolUse": []any{hookGroup(userHook)}},
		"permissions": map[string]any{"allow": allowRules(), "ask": askRules(), "deny": denyRules()},
	}
	assert.Check(t, is.DeepEqual(decodedFile(t, settingsUnder(home)), want))
}

func TestApp_Run_WhenInitClaudeRunsWithTheProjectFlag_HooksTheScriptOfTheProject(t *testing.T) {
	t.Parallel()

	f := gittest.New(t, trunk)
	app := over(git.Runner{Dir: f.Dir(), Env: f.Env()})
	app.Environ = []string{"HOME=" + t.TempDir()}
	app.Skills = shipped()
	app.SetGuardSupported(true)

	got := launch(t, app, "init", "claude", "--project")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	hooks, _ := decodedFile(t, settingsUnder(f.Dir()))["hooks"].(map[string]any)
	assert.Check(t, is.DeepEqual(hooks["PreToolUse"], []any{hookGroup(projectHook)}))
}

func TestApp_Run_WhenInitClaudeRunsWhereTheGuardDoesNot_WritesTheRulesWithoutAHook(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	app.SetGuardSupported(false)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	want := map[string]any{
		"permissions": map[string]any{"allow": allowRules(), "ask": askRules(), "deny": denyRules()},
	}
	assert.Check(t, is.DeepEqual(decodedFile(t, settingsUnder(home)), want))
}

func TestApp_Run_WhenInitClaudeRunsWhereTheGuardDoesNot_SaysWhereTheGuardRuns(t *testing.T) {
	t.Parallel()

	app, _ := installing(t)
	app.SetGuardSupported(false)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(got.stdout, cli.GuardUnsupported))
}

func TestApp_Run_WhenInitClaudeFindsSettings_KeepsTheOrderOfTheUsersKeys(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	path := withSettings(t, home, userSettings)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	//nolint:gosec // G304: the path is one the test built under t.TempDir().
	text, err := os.ReadFile(path)
	assert.NilError(t, err)
	settings := jsontext.Value(text)
	assert.Check(t, is.DeepEqual(names(t, settings), []string{"model", "permissions", "env", "hooks"}))
	assert.Check(t, is.DeepEqual(names(t, member(t, settings, "permissions"))[:3], []string{"deny", "defaultMode", "allow"}))
	assert.Check(t, is.DeepEqual(names(t, member(t, settings, "env")), []string{"ZED", "ALPHA"}))
	assert.Check(t, is.DeepEqual(names(t, member(t, settings, "hooks")), []string{"PostToolUse", "PreToolUse"}))
}

func TestApp_Run_WhenInitClaudeFindsSettings_KeepsTheUsersValuesAndAddsItsOwnAfterThem(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	path := withSettings(t, home, userSettings)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	check := func(command string) map[string]any {
		return map[string]any{"type": "command", "command": command}
	}
	want := map[string]any{
		"model": "opus",
		"permissions": map[string]any{
			"deny":        slices.Concat([]any{"Bash(rm *)"}, denyRules()),
			"defaultMode": "acceptEdits",
			"allow":       slices.Concat([]any{"Read"}, allowRules()),
			"ask":         askRules(),
		},
		"env": map[string]any{"ZED": "1", "ALPHA": "2"},
		"hooks": map[string]any{
			"PostToolUse": []any{map[string]any{"matcher": "Edit", "hooks": []any{check("fmt")}}},
			"PreToolUse": []any{
				map[string]any{"matcher": "Edit", "hooks": []any{check("check")}},
				hookGroup(userHook),
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
		{name: "allow rule moved to ask", rule: "Bash(ils push)", list: "ask"},
		{name: "ask rule moved to deny", rule: "Bash(gh pr merge)", list: "deny"},
		{name: "deny rule moved to allow", rule: "Bash(git push)", list: "allow"},
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

func TestApp_Run_WhenInitClaudeFindsTheHookInPlace_SetsItsTimeoutAndFailureWhereItIs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		hook    map[string]any
		name    string
		matcher string
	}{
		{name: "user scope", matcher: "Bash", hook: map[string]any{"command": userHook}},
		{name: "no leading bash", matcher: "Bash", hook: map[string]any{"command": "~/.claude/skills/ils/hook.sh"}},
		{name: "HOME variable", matcher: "Bash", hook: map[string]any{"command": "bash $HOME/.claude/skills/ils/hook.sh"}},
		{name: "HOME in braces", matcher: "Bash", hook: map[string]any{"command": "bash ${HOME}/.claude/skills/ils/hook.sh"}},
		{name: "home spelled out", matcher: "Bash", hook: map[string]any{"command": "bash /home/someone/.claude/skills/ils/hook.sh"}},
		{name: "project scope", matcher: "Bash", hook: map[string]any{"command": projectHook}},
		{name: "group of another matcher", matcher: "Bash|Edit", hook: map[string]any{"command": userHook}},
		{
			name: "other timeout and failure", matcher: "Bash",
			hook: map[string]any{"command": userHook, "timeout": 30, "onFailure": "allow"},
		},
		{name: "timeout in place, failure missing", matcher: "Bash", hook: map[string]any{"command": userHook, "timeout": 15}},
		{
			name: "failure in place, timeout missing", matcher: "Bash",
			hook: map[string]any{"command": userHook, "onFailure": "block"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)
			hook := map[string]any{"type": "command"}
			maps.Copy(hook, tt.hook)
			first := map[string]any{"type": "command", "command": "check"}
			group := func(hook map[string]any) map[string]any {
				return map[string]any{"matcher": tt.matcher, "hooks": []any{first, hook}}
			}
			path := withSettings(t, home, encodedJSON(t, map[string]any{
				"hooks": map[string]any{"PreToolUse": []any{group(hook)}},
			}))

			got := launch(t, app, "init", "claude")

			assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
			hooks, _ := decodedFile(t, path)["hooks"].(map[string]any)
			want := map[string]any{
				"type": "command", "command": tt.hook["command"], "timeout": float64(15), "onFailure": "block",
			}
			assert.Check(t, is.DeepEqual(hooks["PreToolUse"], []any{group(want)}))
		})
	}
}

func TestApp_Run_WhenInitClaudeFindsAHookOfAnotherSkill_AddsItsOwnGroup(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	other := map[string]any{
		"matcher": "Bash",
		"hooks":   []any{map[string]any{"type": "command", "command": "bash ~/.claude/skills/other/hook.sh"}},
	}
	path := withSettings(t, home, encodedJSON(t, map[string]any{
		"hooks": map[string]any{"PreToolUse": []any{other}},
	}))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	hooks, _ := decodedFile(t, path)["hooks"].(map[string]any)
	assert.Check(t, is.DeepEqual(hooks["PreToolUse"], []any{other, hookGroup(userHook)}))
}

func TestApp_Run_WhenInitClaudeFindsHookEntriesThatAreNoObjects_LeavesThemWhereTheyAre(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	guard := map[string]any{"type": "command", "command": userHook}
	path := withSettings(t, home, encodedJSON(t, map[string]any{
		"hooks": map[string]any{"PreToolUse": []any{"note", map[string]any{"matcher": "Bash", "hooks": []any{42, guard}}}},
	}))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	hooks, _ := decodedFile(t, path)["hooks"].(map[string]any)
	fixed := map[string]any{"type": "command", "command": userHook, "timeout": float64(15), "onFailure": "block"}
	want := []any{"note", map[string]any{"matcher": "Bash", "hooks": []any{float64(42), fixed}}}
	assert.Check(t, is.DeepEqual(hooks["PreToolUse"], want))
}

func TestApp_Run_WhenInitClaudeFindsSettingsOfTheWrongShape_ExitsTwoWritingNothing(t *testing.T) {
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
		{name: "ask not an array", settings: `{"permissions": {"ask": 1}}`},
		{name: "deny not an array", settings: `{"permissions": {"deny": "Bash(git push)"}}`},
		{name: "hooks not an object where the guard does not run", settings: `{"hooks": []}`, noGuard: true},
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
			printed := resulttest.Decode(t, got.stdout)
			assert.Check(t, is.Equal(printed.Outcome, result.Refused.String()))
			assert.Check(t, is.Contains(printed.Reason, path))
			assert.Check(t, is.DeepEqual(filesUnder(t, home), map[string]string{path: tt.settings}))
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
	printed := resulttest.Decode(t, got.stdout)
	assert.Check(t, is.Equal(printed.Outcome, result.Broke.String()))
	assert.Check(t, is.Contains(printed.Reason, path))
}

func TestApp_Run_WhenInitClaudeRunsTwice_LeavesEveryFileAsTheFirstRunLeftIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings string
	}{
		{name: "no settings at first"},
		{name: "settings of the user at first", settings: userSettings},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)
			if tt.settings != "" {
				withSettings(t, home, tt.settings)
			}
			installed(t, app)
			first := filesUnder(t, home)

			got := launch(t, app, "init", "claude")

			assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
			assert.Check(t, is.DeepEqual(filesUnder(t, home), first))
		})
	}
}

func TestApp_Run_WhenInitClaudeChangesOnlyTheSettings_PrintsNoLineSayingAllIsInPlace(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	installed(t, app)
	inPlace := launch(t, app, "init", "claude").stdout
	withSettings(t, home, "{}")

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, !strings.Contains(got.stdout, strings.TrimSpace(inPlace)), "stdout = %q", got.stdout)
}

func TestApp_Run_WhenInitClaudeChangesOnlyTheHook_PrintsALineNamingTheSettings(t *testing.T) {
	t.Parallel()

	rules := map[string]any{"allow": allowRules(), "ask": askRules(), "deny": denyRules()}
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
					"hooks":   []any{map[string]any{"type": "command", "command": userHook}},
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
	withSettings(t, home, encodedJSON(t, map[string]any{
		"hooks": map[string]any{"PreToolUse": []any{hookGroup(userHook)}},
		"permissions": map[string]any{
			"allow": []any{"Bash(ils push)", "Bash(ils status)"},
			"ask":   askRules(),
			"deny":  []any{"Bash(git push)"},
		},
	}))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.Contains(lineWith(t, got.stdout, "allow"), "2"))
	assert.Check(t, is.Contains(lineWith(t, got.stdout, "deny"), "1"))
	assert.Check(t, !strings.Contains(got.stdout, "ask"), "stdout = %q", got.stdout)
}
