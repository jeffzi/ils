package cli

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/jeffzi/ils/internal/result"
)

const (
	// settingsFile is the file under claudeDir that Claude Code reads its settings from.
	settingsFile = "settings.json"

	// claudeEvent is the hook event Claude Code runs a hook on before a tool runs, and the
	// hookEventName of the answer it reads from that hook.
	claudeEvent = "PreToolUse"

	// claudeShellTool is the tool_name of Claude Code's shell tool, the one tool the guard judges.
	claudeShellTool = "Bash"

	// hookTimeout and hookOnFailure are the values the guard's hook needs: a timeout long enough
	// for a guard that asks git, and a failure that blocks the command instead of letting it run.
	hookTimeout   = 15
	hookOnFailure = "block"
)

const (
	keyHooks       = "hooks"
	keyPermissions = "permissions"
	keyCommand     = "command"
	keyTimeout     = "timeout"
	keyOnFailure   = "onFailure"
)

// permissionLists are the permission lists of the settings, each with the rules the guard's
// setup adds to it.
//
//nolint:gochecknoglobals // A table of fixed rules, read only.
var permissionLists = []struct {
	name  string
	rules []string
}{
	{name: "allow", rules: []string{"Bash(ils push)", "Bash(ils push --wait)", "Bash(ils branch *)", "Bash(ils status)"}},
	{name: "ask", rules: []string{
		"Bash(ils merge)", "Bash(ils merge *)", "Bash(ils release)", "Bash(ils release *)",
		"Bash(gh pr merge)", "Bash(gh pr merge *)", "Bash(gh release create)", "Bash(gh release create *)",
	}},
	{name: "deny", rules: []string{"Bash(git push)", "Bash(git push *)"}},
}

// shapeError is a settings file that `ils init claude` cannot edit: it is not valid JSON, or a
// value it edits has the wrong type.
type shapeError struct {
	problem string
}

func (e *shapeError) Error() string {
	return e.problem
}

// settingsEdit is what the setup of the guard changes in a settings file.
type settingsEdit struct {
	// file is the settings file as named under the Claude Code directory, and target the file
	// it is written at: the one a link points to, or file itself.
	file   string
	target string
	// text is the new content of target, nil when nothing changes.
	text  []byte
	lines []result.Phrase
	mode  fs.FileMode
}

// planSettings reads the settings file under dir and returns what adding the permission rules,
// and with hook not empty the hook that runs hook, changes in it. A file that is not valid JSON,
// or whose hooks, PreToolUse, permissions, allow, ask or deny has the wrong type, is a
// *shapeError; a file that cannot be read is any other error. A missing file starts empty.
func planSettings(dir, hook string) (settingsEdit, error) {
	file := filepath.Join(dir, settingsFile)
	edit := settingsEdit{file: file, target: file, mode: fileMode}

	target, err := followLink(file)
	if err != nil {
		return edit, err
	}

	edit.target = target

	//nolint:gosec // G304: the path is the settings file of the Claude Code directory.
	text, err := os.ReadFile(edit.target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		text = []byte("{}")
	case err != nil:
		return edit, fmt.Errorf("read %s: %w", file, err)
	default:
		info, statErr := os.Stat(edit.target)
		if statErr != nil {
			return edit, fmt.Errorf("read %s: %w", file, statErr)
		}

		edit.mode = info.Mode().Perm()
	}

	settings, err := settingsObject(file, text)
	if err != nil {
		return edit, err
	}

	if err = edit.apply(&settings, hook); err != nil {
		return edit, err
	}

	if len(edit.lines) == 0 {
		return edit, nil
	}

	whole, err := settings.value()
	if err != nil {
		return edit, fmt.Errorf("encode %s: %w", file, err)
	}

	if err := whole.Indent(jsontext.WithIndent("  ")); err != nil {
		return edit, fmt.Errorf("encode %s: %w", file, err)
	}

	whole = append(whole, '\n')
	edit.text = whole

	return edit, nil
}

// settingsObject returns text, the content of file, as an object, or a *shapeError when it is
// not valid JSON or not an object.
func settingsObject(file string, text []byte) (jsonObject, error) {
	if !jsontext.Value(text).IsValid() {
		return jsonObject{}, &shapeError{problem: fmt.Sprintf("%s is not valid JSON", file)}
	}

	settings, ok, err := readObject(text)
	if err != nil {
		return jsonObject{}, fmt.Errorf("read %s: %w", file, err)
	}

	if !ok {
		return jsonObject{}, &shapeError{problem: fmt.Sprintf("%s holds no JSON object", file)}
	}

	return settings, nil
}

// apply adds the permission rules to settings, and with hook not empty the hook that runs hook,
// keeping a line for each change. The hooks are checked for their shape even when no hook is
// added, so a file the guard's setup could not edit is refused wherever it runs.
func (e *settingsEdit) apply(settings *jsonObject, hook string) error {
	hooks, err := e.member(*settings, keyHooks, keyHooks)
	if err != nil {
		return err
	}

	groups, err := e.list(hooks, claudeEvent, keyHooks+"."+claudeEvent)
	if err != nil {
		return err
	}

	if hook != "" {
		if err := e.addHook(settings, hooks, groups, hook); err != nil {
			return err
		}
	}

	return e.addRules(settings)
}

// member returns the member name of parent read as an object, an empty object when parent has
// no such member, or a *shapeError naming where when it is no object.
func (e *settingsEdit) member(parent jsonObject, name, where string) (jsonObject, error) {
	return memberType[jsonObject]{kind: "an object", read: readObject}.of(e, parent, name, where)
}

// list returns the member name of parent read as an array, none when parent has no such member,
// or a *shapeError naming where when it is no array.
func (e *settingsEdit) list(parent jsonObject, name, where string) ([]jsontext.Value, error) {
	return memberType[[]jsontext.Value]{kind: "an array", read: readArray}.of(e, parent, name, where)
}

// memberType reads a settings member as a T. Its kind is the phrase a *shapeError uses to say
// the member is not a T.
type memberType[T any] struct {
	read func(jsontext.Value) (T, bool, error)
	kind string
}

// of returns the member name of parent as t.read decodes it, the zero T when parent has no such
// member, or a *shapeError naming where and saying it is not t.kind when t.read finds another type.
func (t memberType[T]) of(e *settingsEdit, parent jsonObject, name, where string) (T, error) {
	var none T

	raw, found := parent.get(name)
	if !found {
		return none, nil
	}

	child, ok, err := t.read(raw)
	if err != nil {
		return none, fmt.Errorf("read %s of %s: %w", where, e.file, err)
	}

	if !ok {
		return none, &shapeError{problem: fmt.Sprintf("%s in %s is not %s", where, e.file, t.kind)}
	}

	return child, nil
}

// addHook makes every hook of groups, the PreToolUse groups of hooks in settings, that runs the
// guard's hook script time out after hookTimeout seconds and block on failure, or adds a group on
// Bash whose one hook runs command when no group holds one.
func (e *settingsEdit) addHook(settings *jsonObject, hooks jsonObject, groups []jsontext.Value, command string) error {
	found, updated := false, false
	for i, group := range groups {
		fixed, held, fixErr := fixGroup(group)
		if fixErr != nil {
			return fmt.Errorf("read the %s hooks of %s: %w", claudeEvent, e.file, fixErr)
		}

		found = found || held
		if fixed != nil {
			groups[i], updated = fixed, true
		}
	}

	switch {
	case !found:
		group, groupErr := guardGroup(command)
		if groupErr != nil {
			return groupErr
		}

		groups = append(groups, group)
		e.lines = append(e.lines, result.Say("Added the guard's hook to %s", e.file))
	case updated:
		e.lines = append(e.lines, result.Say("Set the timeout and the failure of the guard's hook in %s", e.file))
	default:
		return nil
	}

	whole, err := withArray(hooks, claudeEvent, groups)
	if err != nil {
		return err
	}

	settings.set(keyHooks, whole)

	return nil
}

// withArray returns parent as JSON text, with the array elems as its member name.
func withArray(parent jsonObject, name string, elems []jsontext.Value) (jsontext.Value, error) {
	array, err := json.Marshal(elems)
	if err != nil {
		return nil, fmt.Errorf("encode an array: %w", err)
	}

	parent.set(name, array)

	return parent.value()
}

// fixGroup reads group, a PreToolUse group, and reports whether it holds a hook that runs the
// guard's hook script. When such a hook lacks the timeout or the failure the guard needs, it
// returns the group with both set; otherwise a nil group. A group or a hook of another shape is
// left as it is.
func fixGroup(group jsontext.Value) (fixed jsontext.Value, held bool, err error) {
	members, ok, err := readObject(group)
	if err != nil || !ok {
		return nil, false, err
	}

	raw, _ := members.get(keyHooks)
	hooks, ok, err := readArray(raw)
	if err != nil || !ok {
		return nil, false, err
	}

	held, changed, err := fixHooks(hooks)
	if err != nil || !changed {
		return nil, held, err
	}

	fixed, err = withArray(members, keyHooks, hooks)

	return fixed, held, err
}

// fixHooks sets every hook of hooks that runs the guard's hook script to time out after
// hookTimeout seconds and block on failure, in its place, and reports whether hooks holds such a
// hook and whether one changed.
func fixHooks(hooks []jsontext.Value) (held, changed bool, err error) {
	for i, hook := range hooks {
		entry, ok, readErr := readObject(hook)
		if readErr != nil {
			return false, false, readErr
		}

		if !ok || !runsGuard(entry) {
			continue
		}

		held = true
		timed := setValue(&entry, keyTimeout, jsontext.Value(strconv.Itoa(hookTimeout)))
		blocks := setValue(&entry, keyOnFailure, jsonString(hookOnFailure))
		if !timed && !blocks {
			continue
		}

		if hooks[i], err = entry.value(); err != nil {
			return false, false, err
		}

		changed = true
	}

	return held, changed, nil
}

// runsGuard reports whether hook runs the guard's hook script: its command, read as shell words
// with their quotes removed, is the script's path alone, or bash then the path. Only the ending of
// the path decides, so an expansion in it stays as written.
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

// setValue sets the member name of o to want unless it already holds an equal value, and reports
// whether it changed.
func setValue(o *jsonObject, name string, want jsontext.Value) bool {
	if have, found := o.get(name); found && sameJSON(have, want) {
		return false
	}

	o.set(name, want)

	return true
}

// sameJSON reports whether a and b hold the same JSON value.
func sameJSON(a, b jsontext.Value) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}

	return reflect.DeepEqual(x, y)
}

// jsonString returns text as a JSON string.
func jsonString(text string) jsontext.Value {
	// Quoting fails only on invalid UTF-8, and every caller passes a constant of this package.
	quoted, err := jsontext.AppendQuote(nil, text)
	if err != nil {
		panic(fmt.Sprintf("quote %q: %v", text, err))
	}

	return quoted
}

// guardGroup returns the PreToolUse group on Bash whose one hook runs command.
func guardGroup(command string) (jsontext.Value, error) {
	//nolint:govet // The order of the fields is the order of the keys written, not a layout to pack.
	type hook struct {
		Type      string `json:"type"`
		Command   string `json:"command"`
		Timeout   int    `json:"timeout"`
		OnFailure string `json:"onFailure"`
	}

	group := struct {
		Matcher string `json:"matcher"`
		Hooks   []hook `json:"hooks"`
	}{
		Matcher: claudeShellTool,
		Hooks:   []hook{{Type: "command", Command: command, Timeout: hookTimeout, OnFailure: hookOnFailure}},
	}

	text, err := json.Marshal(group)
	if err != nil {
		return nil, fmt.Errorf("encode the guard's hook: %w", err)
	}

	return text, nil
}

// addRules adds to each permission list of settings the rules of it that none of the lists holds
// yet, with a line for each list that grew.
func (e *settingsEdit) addRules(settings *jsonObject) error {
	permissions, err := e.member(*settings, keyPermissions, keyPermissions)
	if err != nil {
		return err
	}

	lists := make([][]jsontext.Value, len(permissionLists))
	for i, list := range permissionLists {
		if lists[i], err = e.list(permissions, list.name, keyPermissions+"."+list.name); err != nil {
			return err
		}
	}

	held := heldRules(lists)
	grew := false
	for i, list := range permissionLists {
		added := missingRules(list.rules, held)
		if len(added) == 0 {
			continue
		}

		array, arrayErr := json.Marshal(slices.Concat(lists[i], added))
		if arrayErr != nil {
			return fmt.Errorf("encode an array: %w", arrayErr)
		}

		permissions.set(list.name, array)
		e.lines = append(e.lines, rulesLine(len(added), list.name, e.file))
		grew = true
	}

	if !grew {
		return nil
	}

	whole, err := permissions.value()
	if err != nil {
		return err
	}

	settings.set(keyPermissions, whole)

	return nil
}

// missingRules returns, as JSON strings, the rules that held does not hold.
func missingRules(rules []string, held map[string]bool) []jsontext.Value {
	var missing []jsontext.Value
	for _, rule := range rules {
		if !held[rule] {
			missing = append(missing, jsonString(rule))
		}
	}

	return missing
}

// heldRules returns every rule that one of the permission lists holds.
func heldRules(lists [][]jsontext.Value) map[string]bool {
	held := map[string]bool{}
	for _, list := range lists {
		for _, entry := range list {
			var rule string
			if json.Unmarshal(entry, &rule) == nil {
				held[rule] = true
			}
		}
	}

	return held
}

// rulesLine returns the line saying that n rules were added to the permission list of file.
func rulesLine(n int, list, file string) result.Phrase {
	if n == 1 {
		return result.Say("Added 1 rule to the %s list of %s", list, file)
	}

	return result.Say("Added %s rules to the %s list of %s", n, list, file)
}

// write writes the new content of the settings, when there is one, over the target with
// replaceFile. The new file keeps the mode of the old one.
func (e settingsEdit) write() error {
	if e.text == nil {
		return nil
	}

	if err := replaceFile(e.target, e.text, e.mode); err != nil {
		return fmt.Errorf("write %s: %w", e.file, err)
	}

	return nil
}

// followLink returns the file path leads to when path is a link, and path itself otherwise, also
// when it cannot be read: the write that follows says what is wrong with it. replaceFile takes
// the result, not the link.
func followLink(path string) (string, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("follow the link %s: %w", path, err)
		}

		return target, nil
	}

	return path, nil
}

// replaceFile writes text with mode to a new file in the directory of target, making that
// directory first, and renames it over target. A reader of target sees the old content or the
// new one, never a part of either, and a failed write leaves the old file whole and removes the
// new one. target must not be a link: the rename would replace the link, not the file it leads to.
func replaceFile(target string, text []byte, mode fs.FileMode) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}

	temp, err := os.CreateTemp(dir, "."+filepath.Base(target)+"-*")
	if err != nil {
		return err
	}

	err = writeTemp(temp, text, mode)
	if err == nil {
		err = os.Rename(temp.Name(), target)
	}

	if err != nil {
		return errors.Join(err, removeTemp(temp.Name()))
	}

	return nil
}

// writeTemp writes text to temp, sets its mode and closes it.
func writeTemp(temp *os.File, text []byte, mode fs.FileMode) error {
	_, err := temp.Write(text)
	if err == nil {
		err = temp.Chmod(mode)
	}

	return errors.Join(err, temp.Close())
}

// removeTemp removes the temporary file name, which a failed write leaves behind.
func removeTemp(name string) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", name, err)
	}

	return nil
}

// jsonObject is a JSON object read member by member: its names in the order they came, each
// with its value as read.
type jsonObject struct {
	names  []string
	values []jsontext.Value
}

// readObject returns text read as an object, and false when it holds another kind of value.
func readObject(text jsontext.Value) (jsonObject, bool, error) {
	if text.Kind() != '{' {
		return jsonObject{}, false, nil
	}

	dec := jsontext.NewDecoder(bytes.NewReader(text))
	if _, err := dec.ReadToken(); err != nil {
		return jsonObject{}, false, fmt.Errorf("read an object: %w", err)
	}

	var o jsonObject
	for dec.PeekKind() != '}' {
		token, err := dec.ReadToken()
		if err != nil {
			return jsonObject{}, false, fmt.Errorf("read a name: %w", err)
		}

		// The next read voids the token, so its name is taken first.
		name := token.String()

		value, err := dec.ReadValue()
		if err != nil {
			return jsonObject{}, false, fmt.Errorf("read the value of %s: %w", name, err)
		}

		o.names = append(o.names, name)
		o.values = append(o.values, value.Clone())
	}

	return o, true, nil
}

// readArray returns the elements of text read as an array, and false when it holds another kind
// of value.
func readArray(text jsontext.Value) ([]jsontext.Value, bool, error) {
	if text.Kind() != '[' {
		return nil, false, nil
	}

	elems := []jsontext.Value{}
	if err := json.Unmarshal(text, &elems); err != nil {
		return nil, false, fmt.Errorf("read an array: %w", err)
	}

	return elems, true, nil
}

// get returns the value of the member name, and whether o has one.
func (o jsonObject) get(name string) (jsontext.Value, bool) {
	i := slices.Index(o.names, name)
	if i < 0 {
		return nil, false
	}

	return o.values[i], true
}

// set makes value the value of the member name, in its place when o has one, and last otherwise.
func (o *jsonObject) set(name string, value jsontext.Value) {
	if i := slices.Index(o.names, name); i >= 0 {
		o.values[i] = value

		return
	}

	o.names = append(o.names, name)
	o.values = append(o.values, value)
}

// value returns o as JSON text.
func (o jsonObject) value() (jsontext.Value, error) {
	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf)

	err := enc.WriteToken(jsontext.BeginObject)
	for i := 0; err == nil && i < len(o.names); i++ {
		err = enc.WriteToken(jsontext.String(o.names[i]))
		if err == nil {
			err = enc.WriteValue(o.values[i])
		}
	}

	if err == nil {
		err = enc.WriteToken(jsontext.EndObject)
	}

	if err != nil {
		return nil, fmt.Errorf("encode an object: %w", err)
	}

	return jsontext.Value(bytes.TrimSpace(buf.Bytes())), nil
}
