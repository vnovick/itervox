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
	parts := strings.Split(key, ".")
	if len(parts) < 2 || slices.Contains(parts, "") {
		return fmt.Errorf("config: %q is not a settings key (use section.name, e.g. agent.max_turns)", key)
	}
	if !slices.Contains(LayeredSections, parts[0]) {
		return fmt.Errorf("config: %q cannot be set in a settings file (sections: %s)", key, strings.Join(LayeredSections, ", "))
	}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if value == nil {
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
	ops, err := settingPatch(&doc, parts, value)
	if err != nil {
		return err
	}
	if len(ops) == 0 {
		return nil
	}
	patch, err := json.Marshal(ops)
	if err != nil {
		return fmt.Errorf("config: encode %s: %w", key, err)
	}
	if err := doc.Patch(patch); err != nil {
		return fmt.Errorf("config: set %s in %s: %w", key, path, err)
	}
	// Inserted values arrive on one line; Format lays the file out again,
	// keeping every comment.
	doc.Format()
	out := doc.Pack()
	if _, err := parseSettings(out); err != nil {
		return fmt.Errorf("config: %s would not parse after setting %s: %w", path, key, err)
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

type patchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// settingPatch builds the JSON Patch that sets (or, for nil, removes) the key:
// an "add" for each missing parent object, then the value itself.
func settingPatch(doc *hujson.Value, parts []string, value any) ([]patchOp, error) {
	var ops []patchOp
	ptr := ""
	for i, p := range parts {
		ptr += "/" + pointerEscaper.Replace(p)
		found := doc.Find(ptr)
		last := i == len(parts)-1
		switch {
		case last && value == nil:
			if found != nil {
				ops = append(ops, patchOp{Op: "remove", Path: ptr})
			}
			return ops, nil
		case last:
			ops = append(ops, patchOp{Op: "add", Path: ptr, Value: value})
		case found == nil:
			if value == nil {
				return nil, nil // nothing to remove
			}
			ops = append(ops, patchOp{Op: "add", Path: ptr, Value: map[string]any{}})
			// Everything below is new; the remaining parents are added in turn.
			for _, q := range parts[i+1 : len(parts)-1] {
				ptr += "/" + pointerEscaper.Replace(q)
				ops = append(ops, patchOp{Op: "add", Path: ptr, Value: map[string]any{}})
			}
			ptr += "/" + pointerEscaper.Replace(parts[len(parts)-1])
			return append(ops, patchOp{Op: "add", Path: ptr, Value: value}), nil
		case found.Value.Kind() != '{':
			return nil, fmt.Errorf("config: %s is not an object in the settings file", strings.Join(parts[:i+1], "."))
		}
	}
	return ops, nil
}
