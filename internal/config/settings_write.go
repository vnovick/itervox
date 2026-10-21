package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tailscale/hujson"
	"github.com/vnovick/itervox/internal/atomicfs"
)

// newSettingsFile is what SetSetting starts a missing settings file from.
const newSettingsFile = `// Itervox settings (JSONC: comments and trailing commas are fine).
// Layers: built-in defaults < ~/.itervox/settings.jsonc < .itervox/settings.jsonc < WORKFLOW.md.
// Sections: agent, polling, server, workspace. See the configuration docs.
{
}
`

// SetSetting sets key ("agent.max_concurrent_agents") to value in the JSONC
// settings file at path, creating the file and any missing objects. Comments
// and the layout of everything else in the file are kept (#129). A nil value
// removes the key. The result is checked to parse before it replaces the file.
func SetSetting(path, key string, value any) error {
	return SetSettings(path, SettingAssignment{Key: key, Value: value})
}

// SettingAssignment is one key SetSettings sets (or, for a nil Value,
// removes).
type SettingAssignment struct {
	Key   string
	Value any
}

// SetSettings is SetSetting for several keys at once: they are applied in
// order and the file is written once, so a save of related keys (the reviewer
// profile and auto_review) never leaves the file holding only some of them.
// Nothing is written when no key changes the file's contents.
func SetSettings(path string, settings ...SettingAssignment) error {
	split := make([][]string, len(settings))
	anyValue := false
	for i, s := range settings {
		parts := strings.Split(s.Key, ".")
		if len(parts) < 2 || slices.Contains(parts, "") {
			return fmt.Errorf("config: %q is not a settings key (use section.name, e.g. agent.max_turns)", s.Key)
		}
		if !slices.Contains(LayeredSections, parts[0]) {
			return fmt.Errorf("config: %q cannot be set in a settings file (sections: %s)", s.Key, strings.Join(LayeredSections, ", "))
		}
		split[i] = parts
		anyValue = anyValue || s.Value != nil
	}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if !anyValue {
			return nil
		}
		raw = []byte(newSettingsFile)
	case err != nil:
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	doc, err := hujson.Parse(raw)
	if err != nil {
		return fmt.Errorf("config: %s: %w", path, err)
	}
	changed := false
	for i, s := range settings {
		ops, err := settingPatch(&doc, split[i], s.Value)
		if err != nil {
			return err
		}
		if len(ops) == 0 {
			continue
		}
		patch, err := json.Marshal(ops)
		if err != nil {
			return fmt.Errorf("config: encode %s: %w", s.Key, err)
		}
		if err := doc.Patch(patch); err != nil {
			return fmt.Errorf("config: set %s in %s: %w", s.Key, path, err)
		}
		changed = true
	}
	if !changed {
		return nil
	}
	// Inserted values arrive on one line; Format lays the file out again,
	// keeping every comment.
	doc.Format()
	return WriteJSONCFile(path, doc.Pack(), func(out []byte) error {
		_, err := parseSettings(out)
		return err
	})
}

// PatchJSONC applies RFC 6902 operations to JSONC source and lays the result
// out again. Comments, and every part of the document the operations do not
// replace, are kept. It is the comment-preserving edit behind SetSettings,
// exported for the other JSONC files Itervox writes (the rules layers, #157).
func PatchJSONC(raw []byte, ops []JSONPatchOp) ([]byte, error) {
	doc, err := hujson.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if len(ops) == 0 {
		return raw, nil
	}
	patch, err := json.Marshal(ops)
	if err != nil {
		return nil, fmt.Errorf("config: encode patch: %w", err)
	}
	if err := doc.Patch(patch); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	doc.Format()
	return doc.Pack(), nil
}

// WriteJSONCFile replaces the JSONC file at path with out, atomically, after
// check accepts it (nil check: it must only parse as JSONC). Parent
// directories are created. A refused or failed write leaves the file as it
// was.
func WriteJSONCFile(path string, out []byte, check func([]byte) error) error {
	if check == nil {
		check = func(b []byte) error { _, err := ParseJSONC(b); return err }
	}
	if err := check(out); err != nil {
		return fmt.Errorf("config: %s would not parse after this change: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := atomicfs.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

// JSONPatchOp is one RFC 6902 operation, as SetSettings and PatchJSONC
// apply them.
type JSONPatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// JSONPointer escapes one JSON Pointer reference token ("a/b" → "a~1b").
func JSONPointer(token string) string { return pointerEscaper.Replace(token) }

// settingPatch builds the JSON Patch that sets (or, for nil, removes) the key:
// an "add" for each missing parent object, then the value itself.
func settingPatch(doc *hujson.Value, parts []string, value any) ([]JSONPatchOp, error) {
	var ops []JSONPatchOp
	ptr := ""
	for i, p := range parts {
		ptr += "/" + pointerEscaper.Replace(p)
		found := doc.Find(ptr)
		last := i == len(parts)-1
		switch {
		case last && value == nil:
			if found != nil {
				ops = append(ops, JSONPatchOp{Op: "remove", Path: ptr})
			}
			return ops, nil
		case last:
			ops = append(ops, JSONPatchOp{Op: "add", Path: ptr, Value: value})
		case found == nil:
			if value == nil {
				return nil, nil // nothing to remove
			}
			ops = append(ops, JSONPatchOp{Op: "add", Path: ptr, Value: map[string]any{}})
			// Everything below is new; the remaining parents are added in turn.
			for _, q := range parts[i+1 : len(parts)-1] {
				ptr += "/" + pointerEscaper.Replace(q)
				ops = append(ops, JSONPatchOp{Op: "add", Path: ptr, Value: map[string]any{}})
			}
			ptr += "/" + pointerEscaper.Replace(parts[len(parts)-1])
			return append(ops, JSONPatchOp{Op: "add", Path: ptr, Value: value}), nil
		case found.Value.Kind() != '{':
			return nil, fmt.Errorf("config: %s is not an object in the settings file", strings.Join(parts[:i+1], "."))
		}
	}
	return ops, nil
}
