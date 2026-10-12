package config

// Settings layers (#129). Agent, polling, workspace and server settings can
// come from two JSONC files as well as from WORKFLOW.md:
//
//	built-in defaults → user (~/.itervox/settings.jsonc)
//	                  → project (.itervox/settings.jsonc next to WORKFLOW.md)
//	                  → WORKFLOW.md front matter
//
// The last layer that sets a key wins. Maps merge key by key; any other value
// (a list included) is replaced whole. WORKFLOW.md stays the only home of the
// tracker, hooks, dependencies, automations and the prompt.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

// Settings layer names, as `itervox config show` prints them.
const (
	LayerDefault  = "default"
	LayerUser     = "user"
	LayerProject  = "project"
	LayerWorkflow = "workflow"
)

// LayeredSections are the WORKFLOW.md sections a settings file may set.
var LayeredSections = []string{"agent", "polling", "server", "workspace"}

// UserSettingsEnv overrides the user settings file path; "off" disables the
// user layer.
const UserSettingsEnv = "ITERVOX_USER_SETTINGS"

// UserSettingsPath is the user layer's file: $ITERVOX_USER_SETTINGS, else
// ~/.itervox/settings.jsonc. Empty when the layer is off, or when no home
// directory is known. Under `go test` the user layer is off unless the
// variable names a file, so a developer's own settings never change what the
// tests load.
func UserSettingsPath() string {
	if p := os.Getenv(UserSettingsEnv); p != "" {
		if p == "off" {
			return ""
		}
		return p
	}
	if testing.Testing() {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".itervox", "settings.jsonc")
}

// ProjectSettingsPath is the project layer's file for a workflow.
func ProjectSettingsPath(workflowPath string) string {
	return filepath.Join(filepath.Dir(workflowPath), ".itervox", "settings.jsonc")
}

// settingsLayer is one settings file that exists.
type settingsLayer struct {
	name string
	path string
	data map[string]any
}

// readSettingsLayers reads the user and project files, lowest first. A file
// that does not exist is skipped; one that does not parse, or sets a section
// outside LayeredSections, is an error naming the file.
func readSettingsLayers(workflowPath string) ([]settingsLayer, error) {
	var out []settingsLayer
	for _, l := range []struct{ name, path string }{
		{LayerUser, UserSettingsPath()},
		{LayerProject, ProjectSettingsPath(workflowPath)},
	} {
		if l.path == "" {
			continue
		}
		data, err := ReadSettingsFile(l.path)
		if err != nil {
			return nil, err
		}
		if data != nil {
			out = append(out, settingsLayer{name: l.name, path: l.path, data: data})
		}
	}
	return out, nil
}

// ReadSettingsFile parses a JSONC settings file. It returns nil, nil when the
// file does not exist.
func ReadSettingsFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	data, err := parseSettings(raw)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return data, nil
}

// parseSettings decodes JSONC into the same shapes the YAML front matter
// produces: objects as map[string]any, whole numbers as int.
func parseSettings(raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	// Standardize rewrites its argument in place (comments become spaces).
	std, err := hujson.Standardize(bytes.Clone(raw))
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(std))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("settings must be a JSON object")
	}
	for k, v := range top {
		if !slices.Contains(LayeredSections, k) {
			return nil, fmt.Errorf("%q cannot be set here (settings files cover %s; the rest stays in WORKFLOW.md)",
				k, strings.Join(LayeredSections, ", "))
		}
		if _, isObject := v.(map[string]any); !isObject {
			return nil, fmt.Errorf("%q must be an object of settings", k)
		}
	}
	return normalizeJSON(top).(map[string]any), nil
}

func normalizeJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = normalizeJSON(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = normalizeJSON(e)
		}
		return t
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
		f, _ := t.Float64()
		return f
	}
	return v
}

// mergeLayers merges the settings layers under the WORKFLOW.md front matter
// and records, for every leaf key path ("agent.max_turns"), the layer that
// set it.
func mergeLayers(front map[string]any, layers []settingsLayer) (map[string]any, map[string]settingsSource) {
	merged := map[string]any{}
	sources := map[string]settingsSource{}
	for _, l := range layers {
		mergeInto(merged, l.data, "", settingsSource{layer: l.name, path: l.path}, sources)
	}
	mergeInto(merged, front, "", settingsSource{layer: LayerWorkflow}, sources)
	return merged, sources
}

// settingsSource is where a key's value came from.
type settingsSource struct {
	layer string
	path  string
}

func mergeInto(dst, src map[string]any, prefix string, from settingsSource, sources map[string]settingsSource) {
	for k, v := range src {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if v == nil {
			// An empty YAML section ("agent:") sets nothing.
			continue
		}
		if sub, ok := asStringMap(v); ok {
			child, ok := asStringMap(dst[k])
			if !ok {
				child = map[string]any{}
			} else {
				child = maps.Clone(child)
			}
			delete(sources, key) // a map replaces a lower layer's non-map value
			mergeInto(child, sub, key, from, sources)
			dst[k] = child
			continue
		}
		dst[k] = v
		for p := range sources {
			if strings.HasPrefix(p, key+".") {
				delete(sources, p) // a non-map value replaces a lower layer's map
			}
		}
		// The two spellings of one setting are one setting across layers: a
		// higher layer's value under either replaces a lower layer's under
		// the other. Within one layer both stay, and the parser's own rule
		// (the new spelling wins) decides.
		if alt, ok := settingAliases[key]; ok {
			if src, set := sources[alt]; set && src.layer != from.layer {
				delete(dst, alt[strings.LastIndex(alt, ".")+1:])
				delete(sources, alt)
			}
		}
		sources[key] = from
	}
}

// settingAliases pairs each layered setting with its deprecated spelling,
// both ways.
var settingAliases = map[string]string{
	"server.allow_unauthenticated":     "server.allow_unauthenticated_lan",
	"server.allow_unauthenticated_lan": "server.allow_unauthenticated",
}

func asStringMap(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[fmt.Sprintf("%v", k)] = e
		}
		return out, true
	}
	return nil, false
}
