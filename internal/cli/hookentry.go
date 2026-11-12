package cli

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/jeffzi/ils/internal/result"
)

// keyMatcher is the member of a hook group that names the tools its hooks run for.
const keyMatcher = "matcher"

// addHook makes every guard entry of groups, the PreToolUse groups of hooks in settings, the entry
// that runs command when its group covers Bash, and removes it otherwise, with every group it
// leaves without a hook. When no group that covers Bash held a guard entry, it adds a group on Bash
// holding the entry. It reports the hook step: added when no group held a guard entry, updated
// when an entry was rewritten or removed, and unchanged otherwise.
func (e *settingsEdit) addHook(settings *jsonObject, hooks jsonObject, groups []jsontext.Value, command string) error {
	entry, err := guardEntry(command)
	if err != nil {
		return err
	}

	kept, found, err := fixGroups(groups, entry)
	if err != nil {
		return fmt.Errorf("read the %s hooks of %s: %w", claudeEvent, e.file, err)
	}

	if !found.onBash {
		group, groupErr := guardGroup(entry)
		if groupErr != nil {
			return groupErr
		}

		kept = append(kept, group)
	}

	switch {
	case !found.guarded:
		e.report(stepChanged, result.Say("Added the guard's hook to %s", e.file))
	case found.changed || !found.onBash:
		e.report(stepChanged, result.Say("Updated the guard's hook in %s", e.file))
	default:
		e.report(stepUnchanged, result.Say("The guard's hook is already in %s", e.file))

		return nil
	}

	whole, err := withArray(hooks, claudeEvent, kept)
	if err != nil {
		return err
	}

	settings.set(keyHooks, whole)

	return nil
}

// withArray returns parent as JSON text, with the array elems as its member name.
func withArray(parent jsonObject, name string, elems []jsontext.Value) (jsontext.Value, error) {
	array, err := arrayValue(elems)
	if err != nil {
		return nil, err
	}

	parent.set(name, array)

	return parent.value()
}

// arrayValue returns elems as a JSON array.
func arrayValue(elems []jsontext.Value) (jsontext.Value, error) {
	array, err := json.Marshal(elems)
	if err != nil {
		return nil, fmt.Errorf("encode an array: %w", err)
	}

	return array, nil
}

// fixGroups returns groups with fixGroup applied to each, without the groups it removes, and what
// it found across them: guarded when a group held a guard entry, onBash when a group that covers
// Bash held one, and changed when a group changed.
func fixGroups(groups []jsontext.Value, entry jsontext.Value) ([]jsontext.Value, groupFix, error) {
	kept := make([]jsontext.Value, 0, len(groups)+1)
	var found groupFix
	for _, group := range groups {
		fixed, fix, err := fixGroup(group, entry)
		if err != nil {
			return nil, groupFix{}, err
		}

		found.guarded = found.guarded || fix.guarded
		found.onBash = found.onBash || fix.guarded && fix.onBash
		found.changed = found.changed || fix.changed
		if fixed != nil {
			kept = append(kept, fixed)
		}
	}

	return kept, found, nil
}

// groupFix is what fixGroup found in a PreToolUse group: whether it held a guard entry, whether
// its matcher covers Bash, and whether fixGroup changed it.
type groupFix struct {
	guarded, onBash, changed bool
}

// fixGroup reads group, a PreToolUse group, and returns it with every guard entry made entry when
// its matcher covers Bash, and removed otherwise; nil when that leaves it without a hook. A group
// or a hook of another shape, and a group whose matcher is no string, are returned as they are; the
// last still counts as guarded when it holds a guard entry, so the hook step does not say added.
func fixGroup(group, entry jsontext.Value) (jsontext.Value, groupFix, error) {
	members, ok, err := readObject(group)
	if err != nil || !ok {
		return group, groupFix{}, err
	}

	fix := groupFix{onBash: true}
	if matcher, found := members.get(keyMatcher); found {
		if matcher.Kind() != '"' {
			guarded, guardErr := holdsGuard(members)

			return group, groupFix{guarded: guarded}, guardErr
		}

		var name string
		if err = json.Unmarshal(matcher, &name); err != nil {
			return nil, groupFix{}, fmt.Errorf("read a matcher: %w", err)
		}

		fix.onBash = coversBash(name)
	}

	raw, _ := members.get(keyHooks)
	hooks, ok, err := readArray(raw)
	if err != nil || !ok {
		return group, groupFix{}, err
	}

	kept, err := fixHooks(hooks, entry, &fix)
	if err != nil {
		return nil, groupFix{}, err
	}

	switch {
	case !fix.changed:
		return group, fix, nil
	case len(kept) == 0:
		return nil, fix, nil
	}

	fixed, err := withArray(members, keyHooks, kept)

	return fixed, fix, err
}

// holdsGuard reports whether the hooks of group, a PreToolUse group read as an object, hold a guard
// entry. A group with no hooks array holds none.
func holdsGuard(group jsonObject) (bool, error) {
	raw, _ := group.get(keyHooks)
	hooks, ok, err := readArray(raw)
	if err != nil || !ok {
		return false, err
	}

	for _, hook := range hooks {
		held, isObject, readErr := readObject(hook)
		if readErr != nil {
			return false, readErr
		}

		if isObject && runsGuard(held) {
			return true, nil
		}
	}

	return false, nil
}

// fixHooks returns hooks, the hooks of a group, with every guard entry made entry when fix says
// the group covers Bash, and removed otherwise, and records in fix what it found and changed.
func fixHooks(hooks []jsontext.Value, entry jsontext.Value, fix *groupFix) ([]jsontext.Value, error) {
	kept := make([]jsontext.Value, 0, len(hooks))
	for _, hook := range hooks {
		held, ok, err := readObject(hook)
		if err != nil {
			return nil, err
		}

		switch {
		case !ok || !runsGuard(held):
			kept = append(kept, hook)
		case !fix.onBash:
			fix.guarded, fix.changed = true, true
		case sameJSON(hook, entry):
			fix.guarded = true
			kept = append(kept, hook)
		default:
			fix.guarded, fix.changed = true, true
			kept = append(kept, entry)
		}
	}

	return kept, nil
}

// coversBash reports whether matcher, the matcher of a hook group, covers Bash as Claude Code reads
// it: an empty matcher or * covers every tool; one of letters, digits, _, -, spaces, commas and
// bars alone is a list of exact tool names split by bars or commas; any other is a regular
// expression that covers Bash when it matches anywhere in the name. An expression Go cannot
// compile covers nothing.
func coversBash(matcher string) bool {
	if matcher == "" || matcher == "*" {
		return true
	}

	if strings.IndexFunc(matcher, notInNameList) < 0 {
		for name := range strings.FieldsFuncSeq(matcher, func(r rune) bool { return r == '|' || r == ',' }) {
			if strings.TrimSpace(name) == claudeShellTool {
				return true
			}
		}

		return false
	}

	expr, err := regexp.Compile(matcher)

	return err == nil && expr.MatchString(claudeShellTool)
}

// notInNameList reports whether r cannot appear in a matcher that is a list of tool names.
func notInNameList(r rune) bool {
	switch {
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		return false
	default:
		return !strings.ContainsRune("_- ,|", r)
	}
}

// runsGuard reports whether hook runs the guard's hook script: its command, read as shell words
// with their quotes removed, is the script's path alone, or bash then the path. Only the ending of
// the path decides, so an expansion in it stays as written; the separators of the system count as
// slashes, as in the absolute path a CLAUDE_CONFIG_DIR on Windows gives the hook.
func runsGuard(hook jsonObject) bool {
	raw, found := hook.get(keyCommand)
	if !found {
		return false
	}

	var command string
	if err := json.Unmarshal(raw, &command); err != nil {
		return false
	}

	words, ok := commandWords(command)
	if !ok {
		return false
	}

	var path string
	switch {
	case len(words) == 1:
		path = words[0]
	case len(words) == 2 && words[0] == "bash":
		path = words[1]
	default:
		return false
	}

	path = filepath.ToSlash(path)

	return path == guardScript || strings.HasSuffix(path, "/"+guardScript)
}

// commandWords returns the words of command after quote removal, with every expansion as written,
// and reports false unless command is valid shell holding one simple command and nothing else: no
// assignment, no redirection, no second command.
func commandWords(command string) ([]string, bool) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return nil, false
	}

	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Negated || stmt.Background || stmt.Coprocess || len(stmt.Redirs) > 0 || len(call.Assigns) > 0 {
		return nil, false
	}

	words := make([]string, 0, len(call.Args))
	for _, arg := range call.Args {
		var b strings.Builder
		writeUnquoted(&b, command, arg.Parts, false)
		words = append(words, b.String())
	}

	return words, true
}

// writeUnquoted writes parts, the parts of a word of command, to b after quote removal, and every
// expansion as command writes it. inDouble says whether parts sit inside double quotes, where a
// backslash quotes only the characters that keep a meaning there.
func writeUnquoted(b *strings.Builder, command string, parts []syntax.WordPart, inDouble bool) {
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			writeUnescaped(b, p.Value, inDouble)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			writeUnquoted(b, command, p.Parts, true)
		default:
			b.WriteString(command[part.Pos().Offset():part.End().Offset()])
		}
	}
}

// writeUnescaped writes the text raw to b with its quoting backslashes removed. Outside double
// quotes a backslash quotes any character; inside them only $, `, ", \ and a newline, and a
// backslash before anything else is kept. A backslash before a newline joins two lines and goes
// with the newline.
func writeUnescaped(b *strings.Builder, raw string, inDouble bool) {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' || i+1 == len(raw) {
			b.WriteByte(raw[i])

			continue
		}

		next := raw[i+1]
		switch {
		case next == '\n':
			i++
		case !inDouble || strings.IndexByte("$`\"\\", next) >= 0:
			b.WriteByte(next)
			i++
		default:
			b.WriteByte(raw[i])
		}
	}
}

// sameJSON reports whether a and b hold the same JSON value.
func sameJSON(a, b jsontext.Value) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}

	return reflect.DeepEqual(x, y)
}

// guardEntry returns the hook entry `ils init` writes for the guard: one that runs command, times
// out after hookTimeout seconds and blocks on failure, and holds no other key.
func guardEntry(command string) (jsontext.Value, error) {
	//nolint:govet // The order of the fields is the order of the keys written, not a layout to pack.
	entry := struct {
		Type      string `json:"type"`
		Command   string `json:"command"`
		Timeout   int    `json:"timeout"`
		OnFailure string `json:"onFailure"`
	}{Type: "command", Command: command, Timeout: hookTimeout, OnFailure: hookOnFailure}

	text, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("encode the guard's hook: %w", err)
	}

	return text, nil
}

// guardGroup returns the PreToolUse group on Bash whose one hook is entry.
func guardGroup(entry jsontext.Value) (jsontext.Value, error) {
	group := struct {
		Matcher string           `json:"matcher"`
		Hooks   []jsontext.Value `json:"hooks"`
	}{Matcher: claudeShellTool, Hooks: []jsontext.Value{entry}}

	text, err := json.Marshal(group)
	if err != nil {
		return nil, fmt.Errorf("encode the guard's hook: %w", err)
	}

	return text, nil
}
