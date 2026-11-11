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
	"regexp"
	"slices"
	"strings"

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
	hookTimeout   = "15"
	hookOnFailure = `"block"`
)

const (
	keyHooks       = "hooks"
	keyPermissions = "permissions"
	keyMatcher     = "matcher"
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

// guardHook matches the command of a hook that runs the guard's hook script of the ils skill,
// with or without a leading bash, under any spelling of the directory above the skills.
var guardHook = regexp.MustCompile(`^(?:bash\s+)?(?:\S*/)?skills/ils/hook\.sh"?$`)

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

	if info, err := os.Lstat(file); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(file)
		if err != nil {
			return edit, fmt.Errorf("follow the link %s: %w", file, err)
		}

		edit.target = target
	}

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
	hooks, groups, err := e.hookGroups(*settings)
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

// hookGroups returns the hooks of settings and its PreToolUse groups, or a *shapeError when
// either has the wrong type.
func (e *settingsEdit) hookGroups(settings jsonObject) (jsonObject, []jsontext.Value, error) {
	hooks, err := e.member(settings, keyHooks, keyHooks)
	if err != nil {
		return jsonObject{}, nil, err
	}

	groups, err := e.list(hooks, claudeEvent, keyHooks+"."+claudeEvent)
	if err != nil {
		return jsonObject{}, nil, err
	}

	return hooks, groups, nil
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
	array, err := arrayValue(elems)
	if err != nil {
		return nil, err
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
		timed := setValue(&entry, keyTimeout, hookTimeout)
		blocks := setValue(&entry, keyOnFailure, hookOnFailure)
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

// runsGuard reports whether hook runs the guard's hook script.
func runsGuard(hook jsonObject) bool {
	raw, found := hook.get(keyCommand)
	if !found {
		return false
	}

	var command string
	if err := json.Unmarshal(raw, &command); err != nil {
		return false
	}

	return guardHook.MatchString(strings.TrimSpace(command))
}

// setValue sets the member name of o to want, given as JSON text, unless it already holds an
// equal value, and reports whether it changed.
func setValue(o *jsonObject, name, want string) bool {
	if have, found := o.get(name); found && sameJSON(have, jsontext.Value(want)) {
		return false
	}

	o.set(name, jsontext.Value(want))

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
	hook := jsonObject{}
	hook.set("type", jsonString("command"))
	hook.set(keyCommand, jsonString(command))
	hook.set(keyTimeout, jsontext.Value(hookTimeout))
	hook.set(keyOnFailure, jsontext.Value(hookOnFailure))

	entry, err := hook.value()
	if err != nil {
		return nil, err
	}

	hooks, err := arrayValue([]jsontext.Value{entry})
	if err != nil {
		return nil, err
	}

	group := jsonObject{}
	group.set(keyMatcher, jsonString(claudeShellTool))
	group.set(keyHooks, hooks)

	return group.value()
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

		array, arrayErr := arrayValue(slices.Concat(lists[i], added))
		if arrayErr != nil {
			return arrayErr
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

// write writes the new content of the settings, when there is one, to a file in the directory of
// the target, and renames it over the target, so that a failed write leaves the old file whole.
// The new file keeps the mode of the old one.
func (e settingsEdit) write() error {
	if e.text == nil {
		return nil
	}

	dir := filepath.Dir(e.target)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("write %s: %w", e.file, err)
	}

	temp, err := os.CreateTemp(dir, ".settings-*.json")
	if err != nil {
		return fmt.Errorf("write %s: %w", e.file, err)
	}

	err = writeTemp(temp, e.text, e.mode)
	if err == nil {
		err = os.Rename(temp.Name(), e.target)
	}

	if err != nil {
		return errors.Join(fmt.Errorf("write %s: %w", e.file, err), removeTemp(temp.Name()))
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

	dec := jsontext.NewDecoder(bytes.NewReader(text))
	if _, err := dec.ReadToken(); err != nil {
		return nil, false, fmt.Errorf("read an array: %w", err)
	}

	elems := []jsontext.Value{}
	for dec.PeekKind() != ']' {
		value, err := dec.ReadValue()
		if err != nil {
			return nil, false, fmt.Errorf("read an element: %w", err)
		}

		elems = append(elems, value.Clone())
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
	return encodeComposite(jsontext.BeginObject, jsontext.EndObject, "an object", func(enc *jsontext.Encoder) error {
		for i, name := range o.names {
			if err := enc.WriteToken(jsontext.String(name)); err != nil {
				return err
			}

			if err := enc.WriteValue(o.values[i]); err != nil {
				return err
			}
		}

		return nil
	})
}

// arrayValue returns elems as a JSON array.
func arrayValue(elems []jsontext.Value) (jsontext.Value, error) {
	return encodeComposite(jsontext.BeginArray, jsontext.EndArray, "an array", func(enc *jsontext.Encoder) error {
		for _, elem := range elems {
			if err := enc.WriteValue(elem); err != nil {
				return err
			}
		}

		return nil
	})
}

// encodeComposite returns the JSON text that begin, what body writes, and end make up, stopping at
// the first write that fails with an error saying it could not encode kind.
func encodeComposite(
	begin, end jsontext.Token, kind string, body func(*jsontext.Encoder) error,
) (jsontext.Value, error) {
	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf)

	err := enc.WriteToken(begin)
	if err == nil {
		err = body(enc)
	}

	if err == nil {
		err = enc.WriteToken(end)
	}

	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", kind, err)
	}

	return jsontext.Value(bytes.TrimSpace(buf.Bytes())), nil
}
