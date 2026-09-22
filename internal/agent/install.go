package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leoru/gitident-cli/internal/fsutil"
	"github.com/leoru/gitident-cli/internal/paths"
)

// HookArgs, followed by the agent name, is appended to the gitident binary to
// form the hook command.
const HookArgs = "agent hook"

// isOurCommand reports whether a hook command is gitident's.
func isOurCommand(cmd string) bool {
	return strings.Contains(cmd, paths.AppName) && strings.Contains(cmd, " "+HookArgs+" ")
}

// hookCommandOf extracts the command string of a hook entry in any shape.
func hookCommandOf(raw json.RawMessage) string {
	var h struct {
		Command string `json:"command"`
		Bash    string `json:"bash"`
	}
	if json.Unmarshal(raw, &h) != nil {
		return ""
	}
	if h.Command != "" {
		return h.Command
	}
	return h.Bash
}

// EditHook adds (cmd != "") or removes (cmd == "") gitident's hook in the
// content of t's file. It returns the new content (nil: delete the file) and
// whether anything changed. Everything else in the file is kept, in order.
func (t *HookTarget) EditHook(data []byte, cmd string) ([]byte, bool, error) {
	if t.shape == shapeOwnFile {
		if cmd == "" {
			return nil, data != nil, nil
		}
		want := map[string]any{"hooks": map[string]any{t.event: []any{t.entry(cmd)}}}
		for k, v := range t.top {
			want[k] = v
		}
		out, err := json.MarshalIndent(want, "", "  ")
		if err != nil {
			return nil, false, err
		}
		out = append(out, '\n')
		return out, !bytes.Equal(out, data), nil
	}

	root, err := parseObject(data)
	if err != nil {
		return nil, false, err
	}
	hooks, err := root.childObject("hooks")
	if err != nil {
		return nil, false, err
	}
	var list []json.RawMessage
	if raw, ok := hooks.get(t.event); ok {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, false, fmt.Errorf("hooks.%s: %w", t.event, err)
		}
	}
	entry, _ := json.Marshal(t.entry(cmd))

	found, changed := false, false
	var kept []json.RawMessage
	// keep decides for one hook entry: true to keep it.
	keep := func(h json.RawMessage, matcherOK bool) bool {
		c := hookCommandOf(h)
		if !isOurCommand(c) {
			return true
		}
		if cmd != "" && !found && matcherOK && jsonSame(h, entry) {
			found = true
			return true
		}
		changed = true
		return false
	}
	for _, item := range list {
		if t.shape == shapeFlat {
			if keep(item, true) {
				kept = append(kept, item)
			}
			continue
		}
		var g struct {
			Matcher string            `json:"matcher"`
			Hooks   []json.RawMessage `json:"hooks"`
		}
		if json.Unmarshal(item, &g) != nil {
			kept = append(kept, item)
			continue
		}
		var rest []json.RawMessage
		for _, h := range g.Hooks {
			if keep(h, g.Matcher == t.matcher) {
				rest = append(rest, h)
			}
		}
		switch {
		case len(rest) == 0 && len(g.Hooks) > 0:
			continue
		case len(rest) != len(g.Hooks):
			obj, err := parseObject(item)
			if err != nil {
				return nil, false, err
			}
			hb, _ := json.Marshal(rest)
			obj.set("hooks", hb)
			item, _ = obj.MarshalJSON()
		}
		kept = append(kept, item)
	}
	if cmd != "" && !found {
		changed = true
		if t.shape == shapeFlat {
			kept = append(kept, entry)
		} else {
			g, _ := json.Marshal(map[string]any{"matcher": t.matcher, "hooks": []json.RawMessage{entry}})
			kept = append(kept, g)
		}
		for k, v := range t.top {
			if _, ok := root.get(k); !ok {
				vb, _ := json.Marshal(v)
				root.set(k, vb)
			}
		}
	}
	if !changed {
		return data, false, nil
	}
	if len(kept) == 0 {
		hooks.del(t.event)
	} else {
		raw, _ := json.Marshal(kept)
		hooks.set(t.event, raw)
	}
	if hooks.len() == 0 {
		root.del("hooks")
	} else {
		root.setChild("hooks", hooks)
	}
	out, err := root.indent()
	return out, true, err
}

// jsonSame compares two JSON values semantically.
func jsonSame(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return bytes.Equal(xb, yb)
}

// Installed reports whether t's file holds a gitident hook.
func (t *HookTarget) Installed() bool {
	data, err := os.ReadFile(t.Path)
	if err != nil {
		return false
	}
	if t.shape == shapeOwnFile {
		return true
	}
	_, changed, err := t.EditHook(data, "")
	return err == nil && changed
}

// ApplyHook installs (cmd != "") or removes the hook, returning whether the
// file changed.
func (t *HookTarget) ApplyHook(cmd string, dryRun bool) (bool, error) {
	old, err := fsutil.ReadFileIfExists(t.Path)
	if err != nil {
		return false, err
	}
	if t.shape == shapeOwnFile && old != nil && !bytes.Contains(old, []byte(paths.AppName)) {
		return false, fmt.Errorf("%s exists and was not written by gitident; not replacing it", paths.Contract(t.Path))
	}
	if old == nil && cmd == "" {
		return false, nil
	}
	out, changed, err := t.EditHook(old, cmd)
	if err != nil {
		return false, fmt.Errorf("%s: %w", paths.Contract(t.Path), err)
	}
	if !changed || dryRun {
		return changed, nil
	}
	if out == nil {
		return true, os.Remove(t.Path)
	}
	return true, fsutil.WriteFileAtomic(t.Path, out, 0o644)
}

// Markers of the gitident section in shared instruction files.
const (
	sectionBegin = "<!-- >>> " + paths.AppName + " (managed by `gitident agent`; edits are overwritten) >>> -->"
	sectionEnd   = "<!-- <<< " + paths.AppName + " <<< -->"
	ownMarker    = "<!-- generated by " + paths.AppName + " -->"
)

func sectionContent() []byte {
	return []byte(sectionBegin + "\n" + Instructions() + sectionEnd + "\n")
}

// spliceSection replaces (or appends, or with section == nil removes) the
// gitident section in a shared markdown file.
func spliceSection(data, section []byte) ([]byte, error) {
	s := string(data)
	b := strings.Index(s, sectionBegin)
	e := strings.Index(s, sectionEnd)
	switch {
	case b < 0 && e < 0:
		if section == nil {
			return data, nil
		}
		if s != "" && !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		if s != "" {
			s += "\n"
		}
		return []byte(s + string(section)), nil
	case b < 0 || e < b:
		return nil, errors.New("the gitident section markers are broken; fix or delete them by hand")
	}
	end := e + len(sectionEnd)
	if end < len(s) && s[end] == '\n' {
		end++
	}
	before, after := s[:b], s[end:]
	if section == nil {
		before = strings.TrimRight(before, "\n")
		if before != "" && after != "" {
			before += "\n\n"
		} else if before != "" {
			before += "\n"
		}
		return []byte(before + strings.TrimLeft(after, "\n")), nil
	}
	return []byte(before + string(section) + after), nil
}

// Own reports whether the whole file is gitident's (not a shared file).
func (n *NotesTarget) Own() bool { return n.own }

// Installed reports whether the notes are in place.
func (n *NotesTarget) Installed() bool {
	data, err := os.ReadFile(n.Path)
	if err != nil {
		return false
	}
	if n.own {
		return bytes.Contains(data, []byte(ownMarker))
	}
	return bytes.Contains(data, []byte(sectionBegin))
}

// ApplyNotes writes (install) or removes the notes, returning whether the
// file changed.
func (n *NotesTarget) ApplyNotes(install, dryRun bool) (bool, error) {
	old, err := fsutil.ReadFileIfExists(n.Path)
	if err != nil {
		return false, err
	}
	var out []byte
	if n.own {
		if old != nil && !bytes.Contains(old, []byte(ownMarker)) {
			if !install {
				return false, nil
			}
			return false, fmt.Errorf("%s exists and was not written by gitident; not replacing it", paths.Contract(n.Path))
		}
		if install {
			out = n.content()
		}
	} else {
		var section []byte
		if install {
			section = n.content()
		}
		if out, err = spliceSection(old, section); err != nil {
			return false, fmt.Errorf("%s: %w", paths.Contract(n.Path), err)
		}
		if len(bytes.TrimSpace(out)) == 0 {
			out = nil
		}
	}
	if bytes.Equal(old, out) {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	if out == nil {
		if err := os.Remove(n.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		if n.own {
			_ = os.Remove(filepath.Dir(n.Path)) // the skill directory, when empty
		}
		return true, nil
	}
	return true, fsutil.WriteFileAtomic(n.Path, out, 0o644)
}
