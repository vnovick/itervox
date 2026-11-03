package config

// Settings layers (#129, #158, #239). Every layered section (agent,
// automations, dependencies, hooks, hub, polling, server, tracker, workspace)
// can come from three JSONC files as well as from WORKFLOW.md:
//
//	built-in defaults → user (~/.itervox/settings.jsonc: personal defaults)
//	                  → WORKFLOW.md front matter (deprecated for these sections)
//	                  → project (.itervox/settings.jsonc next to WORKFLOW.md, committed)
//	                  → local (.itervox/settings.local.jsonc, gitignored)
//
// The last layer that sets a key wins. The user file holds personal defaults
// for every project, so a project's own front matter keeps winning over it;
// the project and local files win over the front matter (#239), and a key the
// front matter and one of them both set is reported once as a conflict
// (FrontMatterReport). Maps merge key by key; any other value (a list
// included) is replaced whole. WORKFLOW.md keeps the prompt template (its
// body), the schema marker, the sections that are not layered, and the agent
// profiles the profile editor writes.

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

	"github.com/vnovick/itervox/internal/workflow"
)

// Settings layer names, as `itervox config show` prints them.
const (
	LayerDefault  = "default"
	LayerUser     = "user"
	LayerProject  = "project"
	LayerLocal    = "local"
	LayerWorkflow = "workflow"
)

// SettingsLayerNames are the writable settings layers, lowest first (#158).
var SettingsLayerNames = []string{LayerUser, LayerProject, LayerLocal}

// LayeredSections are the WORKFLOW.md sections a settings file may set.
// tracker, hooks, dependencies and automations joined in #239.
var LayeredSections = []string{"agent", "automations", "dependencies", "hooks", "hub", "polling", "server", "tracker", "workspace"}

// ListSections are the layered sections whose value is a list, not an object
// of settings: a settings file sets the whole list.
var ListSections = []string{"automations"}

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

// LocalSettingsPath is the project-local layer's file for a workflow (#158):
// the operator's own overrides, kept out of git.
func LocalSettingsPath(workflowPath string) string {
	return filepath.Join(filepath.Dir(workflowPath), ".itervox", "settings.local.jsonc")
}

// SettingsLayerPath returns the file of a writable settings layer, "" when
// the layer is off (the user layer under ITERVOX_USER_SETTINGS=off) or the
// name is not one of SettingsLayerNames.
func SettingsLayerPath(workflowPath, layer string) string {
	switch layer {
	case LayerUser:
		return UserSettingsPath()
	case LayerProject:
		return ProjectSettingsPath(workflowPath)
	case LayerLocal:
		return LocalSettingsPath(workflowPath)
	}
	return ""
}

// SettingsLayerRank orders the settings layers, lowest first: a layer of a
// higher rank wins over one of a lower rank. WORKFLOW.md's front matter sits
// between the user file (personal defaults, which it overrides) and the
// project and local files (which override it, #239).
func SettingsLayerRank(layer string) int {
	switch layer {
	case LayerUser:
		return 1
	case LayerWorkflow:
		return 2
	case LayerProject:
		return 3
	case LayerLocal:
		return 4
	}
	return 0
}

// settingsLayer is one settings file that exists.
type settingsLayer struct {
	name string
	path string
	data map[string]any
}

// readSettingsLayers reads the user, project and local files, lowest first. A file
// that does not exist is skipped; one that does not parse, or sets a section
// outside LayeredSections, is an error naming the file.
func readSettingsLayers(workflowPath string) ([]settingsLayer, error) {
	layers, _, err := readSettingsLayerFiles(workflowPath)
	return layers, err
}

// SettingsFile is one settings layer file as Load read it (#245): the
// daemon watches each one, with Hash as the watcher's baseline.
type SettingsFile struct {
	Layer  string
	Path   string
	Exists bool
	// Hash is workflow.FileHash of the bytes Load read (workflow.AbsentHash
	// when the file did not exist).
	Hash [32]byte
}

// readSettingsLayerFiles is readSettingsLayers that also reports every layer
// file it looked at — present or not, but not a layer that is off — with the
// hash of the bytes it read.
func readSettingsLayerFiles(workflowPath string) ([]settingsLayer, []SettingsFile, error) {
	var out []settingsLayer
	var files []SettingsFile
	for _, name := range SettingsLayerNames {
		path := SettingsLayerPath(workflowPath, name)
		if path == "" {
			continue
		}
		raw, exists, err := readSettingsRaw(path)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, SettingsFile{Layer: name, Path: path, Exists: exists, Hash: workflow.FileHash(raw, exists)})
		if !exists {
			continue
		}
		data, err := parseSettingsFile(path, raw)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, settingsLayer{name: name, path: path, data: data})
	}
	return out, files, nil
}

// ReadSettingsFile parses a JSONC settings file. It returns nil, nil when the
// file does not exist.
func ReadSettingsFile(path string) (map[string]any, error) {
	raw, exists, err := readSettingsRaw(path)
	if err != nil || !exists {
		return nil, err
	}
	return parseSettingsFile(path, raw)
}

func readSettingsRaw(path string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("config: read %s: %w", path, err)
	}
	return raw, true, nil
}

// parseSettingsFile is parseSettings with the error naming the file; a
// syntax error already names the line ("hujson: line 3, column 16: ..."),
// and so does a section-shape error (settingsKeyLine).
func parseSettingsFile(path string, raw []byte) (map[string]any, error) {
	data, err := parseSettings(raw)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return data, nil
}

// ParseSettings parses the text of a settings file the way the loader does:
// JSONC, an object whose keys are LayeredSections, each an object (a list
// for ListSections). It is the
// check the dashboard's JSON view is held to before a save.
func ParseSettings(raw []byte) (map[string]any, error) { return parseSettings(raw) }

// parseSettings decodes JSONC into the same shapes the YAML front matter
// produces: objects as map[string]any, whole numbers as int.
func parseSettings(raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	v, err := ParseJSONC(raw)
	if err != nil {
		return nil, err
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("settings must be a JSON object")
	}
	if v, ok := top["$schema"]; ok {
		// Editor support (#158): the file may name its JSON Schema.
		if _, isString := v.(string); !isString {
			return nil, errors.New(`"$schema" must be a string`)
		}
		delete(top, "$schema")
	}
	for k, v := range top {
		if !slices.Contains(LayeredSections, k) {
			return nil, fmt.Errorf("%s%q cannot be set here (settings files cover %s; the rest stays in WORKFLOW.md)",
				settingsKeyLine(raw, k), k, strings.Join(LayeredSections, ", "))
		}
		if slices.Contains(ListSections, k) {
			if _, isList := v.([]any); !isList {
				return nil, fmt.Errorf("%s%q must be a list", settingsKeyLine(raw, k), k)
			}
			continue
		}
		if _, isObject := v.(map[string]any); !isObject {
			return nil, fmt.Errorf("%s%q must be an object of settings", settingsKeyLine(raw, k), k)
		}
	}
	return top, nil
}

// settingsKeyLine returns "line N: " for the value of the top-level key in
// raw, so an error about a section names where it is (#245: a hand edit that
// does not load is logged with its file and line). Empty when the key cannot
// be located.
func settingsKeyLine(raw []byte, key string) string {
	doc, err := hujson.Parse(raw)
	if err != nil {
		return ""
	}
	v := doc.Find("/" + JSONPointer(key))
	if v == nil || v.StartOffset > len(raw) {
		return ""
	}
	return fmt.Sprintf("line %d: ", bytes.Count(raw[:v.StartOffset], []byte("\n"))+1)
}

// ParseJSONC decodes one JSONC value (comments and trailing commas allowed)
// into the shapes the YAML front matter produces: objects as map[string]any,
// arrays as []any, whole numbers as int, other numbers as float64. It is the
// one JSONC reader for every Itervox JSONC file: the settings layers (#129)
// and the rules layers (#157).
func ParseJSONC(raw []byte) (any, error) {
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
	return normalizeJSON(v), nil
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

// mergeLayers merges the layers in SettingsLayerRank order — the user file,
// then the WORKFLOW.md front matter, then the project and local files (#239)
// — and records, for every leaf key path ("agent.max_turns"), the layer that
// set it.
func mergeLayers(front map[string]any, layers []settingsLayer) (map[string]any, map[string]settingsSource) {
	merged := map[string]any{}
	sources := map[string]settingsSource{}
	frontDone := false
	for _, l := range layers {
		if !frontDone && SettingsLayerRank(l.name) > SettingsLayerRank(LayerWorkflow) {
			mergeInto(merged, front, "", settingsSource{layer: LayerWorkflow}, sources)
			frontDone = true
		}
		mergeInto(merged, l.data, "", settingsSource{layer: l.name, path: l.path}, sources)
	}
	if !frontDone {
		mergeInto(merged, front, "", settingsSource{layer: LayerWorkflow}, sources)
	}
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

// SettingAlias returns the other spelling of a layered setting key
// ("server.allow_unauthenticated" ↔ "server.allow_unauthenticated_lan"), if
// it has one. A save that moves a key out of WORKFLOW.md moves both spellings.
func SettingAlias(key string) (string, bool) {
	alt, ok := settingAliases[key]
	return alt, ok
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

// MergedFrontMatter returns WORKFLOW.md's front matter with the settings
// layers merged over it, as the loader sees it before parsing, and the
// prompt template. `itervox export --symphony` (#158) reads the keys a
// project actually sets from it, wherever they live.
func MergedFrontMatter(workflowPath string) (map[string]any, string, error) {
	wf, err := workflow.Load(workflowPath)
	if err != nil {
		return nil, "", err
	}
	layers, err := readSettingsLayers(workflowPath)
	if err != nil {
		return nil, "", err
	}
	merged, _ := mergeLayers(wf.Config, layers)
	return merged, wf.PromptTemplate, nil
}
