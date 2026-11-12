package cli_test

import (
	"fmt"
	"maps"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
)

// matcherGroup returns the PreToolUse group on matcher that holds hooks, with no matcher when
// matcher is nil.
func matcherGroup(matcher any, hooks ...any) map[string]any {
	group := map[string]any{"hooks": hooks}
	if matcher != nil {
		group["matcher"] = matcher
	}

	return group
}

// withPreToolUse writes a settings file under home whose PreToolUse hooks are groups, and returns
// its path.
func withPreToolUse(t *testing.T, home string, groups ...any) string {
	t.Helper()

	return withSettings(t, home, encodedJSON(t, map[string]any{"hooks": map[string]any{"PreToolUse": groups}}))
}

func TestApp_Run_WhenInitClaudeReadsTheMatcherOfAGroup_WritesTheGuardEntryThereOnlyWhenItCoversBash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		matcher any
		name    string
		covers  bool
	}{
		{name: "missing", matcher: nil, covers: true},
		{name: "empty", matcher: "", covers: true},
		{name: "star", matcher: "*", covers: true},

		{name: "first name of a list split by a bar with spaces", matcher: "Bash | Edit", covers: true},
		{name: "second name of a list split by a comma", matcher: "Edit, Bash", covers: true},
		{name: "list name that starts the word", matcher: "Bas", covers: false},
		{name: "list name that holds the word", matcher: "BashOutput", covers: false},
		{name: "expression that matches the end of the word", matcher: "sh$", covers: true},

		{name: "expression that matches another word", matcher: "^Edit$", covers: false},
		{name: "expression Go cannot compile", matcher: "(?=Bash).*", covers: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)
			check := commandHook("check")
			path := withPreToolUse(t, home, matcherGroup(tt.matcher, check, commandHook(cli.UserHook)))

			got := launch(t, app, "init", "claude")

			assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
			want := []any{matcherGroup(tt.matcher, check), hookGroup(cli.UserHook)}
			if tt.covers {
				want = []any{matcherGroup(tt.matcher, check, guardEntry(cli.UserHook))}
			}
			assert.Check(t, is.DeepEqual(preToolUse(t, path), want))
		})
	}
}

func TestApp_Run_WhenInitClaudeFindsAGuardEntryInABashGroup_WritesTheEntryOfItsScopeInItsPlace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		hook map[string]any
		name string
	}{
		{name: "user scope", hook: map[string]any{"command": cli.UserHook}},
		{name: "no leading bash", hook: map[string]any{"command": "~/.claude/skills/ils/hook.sh"}},
		{
			name: "path in double quotes closed after its last slash",
			hook: map[string]any{"command": `bash "/Users/Jane Doe/.claude/"skills/ils/hook.sh`},
		},
		{
			name: "path in single quotes",
			hook: map[string]any{"command": `bash '/Users/Jane Doe/.claude/skills/ils/hook.sh'`},
		},
		{
			name: "path with escaped characters",
			hook: map[string]any{"command": `bash /Users/Jane\ Doe/.claude/skills/ils/hook\.sh`},
		},
		{name: "project scope", hook: map[string]any{"command": cli.ProjectHook}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)
			hook := map[string]any{"type": "command"}
			maps.Copy(hook, tt.hook)
			first, last := commandHook("check"), commandHook("lint")
			path := withPreToolUse(t, home, matcherGroup("Bash", first, hook, last))

			got := launch(t, app, "init", "claude")

			assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
			want := []any{matcherGroup("Bash", first, guardEntry(cli.UserHook), last)}
			assert.Check(t, is.DeepEqual(preToolUse(t, path), want))
		})
	}
}

func TestApp_Run_WhenInitClaudeFindsGuardEntriesInSeveralBashGroups_WritesTheEntryOfItsScopeInEach(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	path := withPreToolUse(t, home,
		matcherGroup("Bash", commandHook(cli.UserHook), commandHook(cli.ProjectHook)),
		matcherGroup("*", commandHook(cli.UserHook)),
	)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	entry := guardEntry(cli.UserHook)
	want := []any{matcherGroup("Bash", entry, entry), matcherGroup("*", entry)}
	assert.Check(t, is.DeepEqual(preToolUse(t, path), want))
}

func TestApp_Run_WhenInitClaudeFindsAGuardEntryInAGroupNotOnBash_RemovesTheGroupItLeavesEmpty(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	bash := matcherGroup("Bash", guardEntry(cli.UserHook))
	path := withPreToolUse(t, home, matcherGroup("Edit", commandHook(cli.UserHook)), bash)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.DeepEqual(preToolUse(t, path), []any{bash}))
}

func TestApp_Run_WhenInitClaudeFindsAGroupWhoseMatcherIsNoString_LeavesItAsItIs(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	group := matcherGroup(float64(42), commandHook(cli.UserHook))
	path := withPreToolUse(t, home, group)

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	assert.Check(t, is.DeepEqual(preToolUse(t, path), []any{group, hookGroup(cli.UserHook)}))
}

func TestApp_Run_WhenInitClaudeFindsABashHookThatIsNotTheGuards_AddsItsOwnGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "skills directory under a longer name", command: "bash ~/.claude/my-skills/ils/hook.sh"},
		{name: "first word other than bash", command: "/bin/bash ~/.claude/skills/ils/hook.sh"},
		{name: "more than two words", command: "/usr/bin/env bash ~/.claude/skills/ils/hook.sh"},
		{name: "unbalanced quote", command: `bash "~/.claude/skills/ils/hook.sh`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)
			other := matcherGroup("Bash", commandHook(tt.command))
			path := withPreToolUse(t, home, other)

			got := launch(t, app, "init", "claude")

			assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
			assert.Check(t, is.DeepEqual(preToolUse(t, path), []any{other, hookGroup(cli.UserHook)}))
		})
	}
}

func TestApp_Run_WhenInitClaudeFindsGroupsOrHookEntriesOfAnotherShape_LeavesThemWhereTheyAre(t *testing.T) {
	t.Parallel()

	app, home := installing(t)
	guard := commandHook(cli.UserHook)
	hookless := map[string]any{"matcher": "Bash"}
	path := withPreToolUse(t, home, "note", hookless, matcherGroup("Bash", 42, guard))

	got := launch(t, app, "init", "claude")

	assert.Check(t, is.Equal(got.code, exitDone), "stdout = %q", got.stdout)
	want := []any{"note", hookless, matcherGroup("Bash", float64(42), guardEntry(cli.UserHook))}
	assert.Check(t, is.DeepEqual(preToolUse(t, path), want))
}

// inPlaceEntry is the settings text of a PreToolUse group on Bash holding the entry `ils init`
// writes at user scope, its keys in another order than the one `ils init` writes them in.
func inPlaceEntry() string {
	return fmt.Sprintf(`{"matcher": "Bash", "hooks": [{"onFailure": %q, "timeout": %v, "command": %q, "type": "command"}]}`,
		cli.HookOnFailure, cli.HookTimeout, cli.UserHook)
}

func TestApp_Run_WhenInitClaudeFindsGuardEntries_MarksTheHookStepChangedUnlessEveryOneIsInPlace(t *testing.T) {
	t.Parallel()

	const (
		added     = "Added the guard's hook to "
		updated   = "Updated the guard's hook in "
		unchanged = "The guard's hook is already in "
	)
	tests := []struct {
		name     string
		groups   string
		wantLine string
		wantMark string
	}{
		{name: "no guard entry", groups: ``, wantMark: "✓", wantLine: added},
		{name: "every guard entry in place", groups: inPlaceEntry(), wantMark: "–", wantLine: unchanged},
		{
			name:     "a guard entry rewritten",
			groups:   fmt.Sprintf(`{"matcher": "Bash", "hooks": [{"type": "command", "command": %q}]}`, cli.UserHook),
			wantMark: "✓",
			wantLine: updated,
		},
		{
			name: "a guard entry removed",
			groups: fmt.Sprintf(`{"matcher": "Edit", "hooks": [{"type": "command", "command": %q}]}, %s`,
				cli.UserHook, inPlaceEntry()),
			wantMark: "✓",
			wantLine: updated,
		},
		{
			name: "a guard entry in place in a group whose matcher is no string",
			groups: fmt.Sprintf(`{"matcher": 42, "hooks": [{"type": "command", "command": %q, "timeout": %v, "onFailure": %q}]}`,
				cli.UserHook, cli.HookTimeout, cli.HookOnFailure),
			wantMark: "✓",
			wantLine: updated,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, home := installing(t)
			path := withSettings(t, home, `{"hooks": {"PreToolUse": [`+tt.groups+`]}}`)

			got := settingsStepsShown(t, app)

			assert.Check(t, strings.HasPrefix(got[0], tt.wantMark+" "), "line = %q", got[0])
			assert.Check(t, is.Contains(got[0], tt.wantLine+path))
		})
	}
}
