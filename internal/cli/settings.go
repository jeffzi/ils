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
	"slices"

	"github.com/jeffzi/ils/internal/result"
)

const (
	// settingsFile is the file under claudeDir that Claude Code reads its settings from.
	settingsFile = "settings.json"

	// claudeEvent is the hook event Claude Code runs a hook on before a tool runs, and the
	// hookEventName of the answer it reads from that hook.
	claudeEvent = "PreToolUse"

	// guardUnsupported is the reason the guard refuses every run outside macOS and Linux.
	guardUnsupported = "the guard runs on macOS and Linux"
)

const (
	keyHooks       = "hooks"
	keyPermissions = "permissions"
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

// Error returns the problem the settings file has.
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
	text []byte
	// lines are the step lines of the edit: the hook's, then those of the allow, ask and deny
	// lists.
	lines []stepLine
	mode  fs.FileMode
}

// report adds the line of a step that ended as kind.
func (e *settingsEdit) report(kind stepKind, line result.Phrase) {
	e.lines = append(e.lines, stepLine{phrase: line, kind: kind})
}

// changed reports whether a step of the edit changed the settings.
func (e *settingsEdit) changed() bool {
	return slices.ContainsFunc(e.lines, func(line stepLine) bool { return line.kind == stepChanged })
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

	if !edit.changed() {
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
// keeping a line for each step; with hook empty the hook's step is unchanged and says where the
// guard runs. The hooks are checked for their shape even when no hook is added, so a file the
// guard's setup could not edit is refused wherever it runs.
func (e *settingsEdit) apply(settings *jsonObject, hook string) error {
	hooks, err := e.member(*settings, keyHooks, keyHooks)
	if err != nil {
		return err
	}

	groups, err := e.list(hooks, claudeEvent, keyHooks+"."+claudeEvent)
	if err != nil {
		return err
	}

	if hook == "" {
		e.report(stepUnchanged, result.Say("Added no hook: "+guardUnsupported))
	} else if err := e.addHook(settings, hooks, groups, hook); err != nil {
		return err
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

// jsonString returns text quoted as a JSON string, and panics when text is not valid UTF-8.
func jsonString(text string) jsontext.Value {
	// Quoting fails only on invalid UTF-8, and every caller passes a constant of this package.
	quoted, err := jsontext.AppendQuote(nil, text)
	if err != nil {
		panic(fmt.Sprintf("quote %q: %v", text, err))
	}

	return quoted
}

// addRules adds to each permission list of settings the rules of it that none of the lists holds
// yet, with a line for each list: changed when it grew, unchanged otherwise.
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

	held, err := heldRules(lists)
	if err != nil {
		return err
	}

	grew := false
	for i, list := range permissionLists {
		added := missingRules(list.rules, held)
		if len(added) == 0 {
			e.report(stepUnchanged, result.Say("The %s rules are already in %s", list.name, e.file))

			continue
		}

		array, arrayErr := arrayValue(slices.Concat(lists[i], added))
		if arrayErr != nil {
			return arrayErr
		}

		permissions.set(list.name, array)
		e.report(stepChanged, rulesLine(len(added), list.name, e.file))
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

// heldRules returns every rule that one of the permission lists holds. An entry that is not a
// string holds no rule: it is kept in its list as it is, and no rule counts as held for it.
func heldRules(lists [][]jsontext.Value) (map[string]bool, error) {
	held := map[string]bool{}
	for _, list := range lists {
		for _, entry := range list {
			if entry.Kind() != '"' {
				continue
			}

			var rule string
			if err := json.Unmarshal(entry, &rule); err != nil {
				return nil, fmt.Errorf("read a permission rule: %w", err)
			}

			held[rule] = true
		}
	}

	return held, nil
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

func arrayValue(elems []jsontext.Value) (jsontext.Value, error) {
	array, err := json.Marshal(elems)
	if err != nil {
		return nil, fmt.Errorf("encode an array: %w", err)
	}

	return array, nil
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

// value returns o encoded as one JSON object, its members in order, without the newline the
// encoder writes after it.
func (o jsonObject) value() (jsontext.Value, error) {
	var buf bytes.Buffer
	if err := o.encode(jsontext.NewEncoder(&buf)); err != nil {
		return nil, fmt.Errorf("encode an object: %w", err)
	}

	return jsontext.Value(bytes.TrimSpace(buf.Bytes())), nil
}

// encode writes o to enc as one object, its members in order.
func (o jsonObject) encode(enc *jsontext.Encoder) error {
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}

	for i, name := range o.names {
		if err := enc.WriteToken(jsontext.String(name)); err != nil {
			return err
		}

		if err := enc.WriteValue(o.values[i]); err != nil {
			return err
		}
	}

	return enc.WriteToken(jsontext.EndObject)
}
