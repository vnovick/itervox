package config

import (
	"fmt"
	"slices"
	"strings"
)

// #239 — one config format. Every layered section (LayeredSections) belongs
// in the settings files; WORKFLOW.md keeps the prompt. A layered key still set
// in the front matter keeps working, and is reported:
//
//   - as deprecated, naming where it moves (`itervox doctor`, the startup log);
//   - as a conflict, when the project or local settings file sets the same
//     key: that file wins (SettingsLayerRank), and the conflict is reported
//     once. A user-file value under the front matter is a personal default
//     the project overrides, not a conflict.

// FrontMatterMovesTo is where `itervox init --update` moves the layered
// front-matter keys: the project settings file, relative to the directory of
// WORKFLOW.md.
const FrontMatterMovesTo = ".itervox/settings.jsonc"

// FrontMatterKeptKeys are the layered keys that stay in WORKFLOW.md: their
// editors write them there (the profile editor writes agent.profiles, `itervox
// models refresh` agent.available_models), and agent.reviewer_prompt is the
// legacy reviewer template `itervox init --update --force` moves into the
// reviewer profile's INSTRUCTIONS.md from there. They are neither deprecated
// nor moved by `itervox init --update` or `itervox import`.
var FrontMatterKeptKeys = []string{"agent.available_models", "agent.profiles", "agent.reviewer_prompt"}

// FrontMatterReport is what the loader found in WORKFLOW.md's front matter
// about the layered sections. Read-only after load.
type FrontMatterReport struct {
	// Deprecated are the layered keys the front matter sets (each still
	// applies unless a settings file sets it too), sorted. A key that is a
	// map of settings is listed by its leaves.
	Deprecated []string
	// Conflicts are the front-matter keys the project or local settings file
	// also sets. That file's value is the one in effect.
	Conflicts []SettingsConflict
}

// SettingsConflict is one key set both in WORKFLOW.md and in a settings file.
type SettingsConflict struct {
	Key string
	// Layer is the highest settings layer that sets the key (the one in
	// effect), and Path its file.
	Layer string
	Path  string
}

// DeprecationMessage is the one-line notice for a deprecated front-matter key.
func DeprecationMessage(key string) string {
	if IsSecretKey(key) {
		return fmt.Sprintf("WORKFLOW.md front matter key %s is deprecated: move it to %s as a $VAR reference (keep the value in the environment or .itervox/.env; `itervox init --update` moves $VAR references and leaves an inline secret in WORKFLOW.md)", key, FrontMatterMovesTo)
	}
	return fmt.Sprintf("WORKFLOW.md front matter key %s is deprecated: it still applies, and moves to %s (run `itervox init --update`)", key, FrontMatterMovesTo)
}

// Message is the one-line notice for a conflict.
func (c SettingsConflict) Message() string {
	return fmt.Sprintf("%s is set both in WORKFLOW.md front matter and in the %s settings file %s; the settings file wins — remove it from WORKFLOW.md", c.Key, c.Layer, c.Path)
}

// frontMatterReport compares the front matter with the settings layers.
func frontMatterReport(front map[string]any, layers []settingsLayer) FrontMatterReport {
	var r FrontMatterReport
	for _, key := range frontMatterKeys(front) {
		if !keptInFrontMatter(key) {
			r.Deprecated = append(r.Deprecated, key)
		}
		for i := len(layers) - 1; i >= 0; i-- {
			if SettingsLayerRank(layers[i].name) <= SettingsLayerRank(LayerWorkflow) {
				break // the user file is below the front matter
			}
			if layerSets(layers[i].data, key) {
				r.Conflicts = append(r.Conflicts, SettingsConflict{Key: key, Layer: layers[i].name, Path: layers[i].path})
				break
			}
		}
	}
	return r
}

// KeptInFrontMatter reports whether key is, or sits under, a
// FrontMatterKeptKeys entry: it stays in WORKFLOW.md.
func KeptInFrontMatter(key string) bool { return keptInFrontMatter(key) }

func keptInFrontMatter(key string) bool {
	return slices.ContainsFunc(FrontMatterKeptKeys, func(k string) bool {
		return key == k || strings.HasPrefix(key, k+".")
	})
}

// FrontMatterLayeredKeys lists the layered keys a front matter sets, sorted:
// a list section by its name, a registered setting by its key (a map setting
// such as tracker.filter whole), and an unregistered key by its leaves.
func FrontMatterLayeredKeys(front map[string]any) []string { return frontMatterKeys(front) }

func frontMatterKeys(front map[string]any) []string {
	var out []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		if v == nil {
			return // an empty YAML section ("tracker:") sets nothing
		}
		m, isMap := asStringMap(v)
		_, registered := SettingFieldFor(prefix)
		if registered || !isMap {
			out = append(out, prefix)
			return
		}
		for k, e := range m {
			walk(prefix+"."+k, e)
		}
	}
	for _, section := range LayeredSections {
		if v, ok := front[section]; ok {
			walk(section, v)
		}
	}
	slices.Sort(out)
	return out
}

// layerSets reports whether a settings layer sets key: the key itself, a
// value below it, or a non-map value at one of its parents (which replaces
// the front matter's map).
func layerSets(data map[string]any, key string) bool {
	var cur any = data
	for _, part := range strings.Split(key, ".") {
		m, ok := asStringMap(cur)
		if !ok {
			return true // a non-map parent replaced the front matter's map
		}
		if cur, ok = m[part]; !ok || cur == nil {
			return false
		}
	}
	return true
}

// IsSecretKey reports whether a setting key names a secret (api_key, token,
// secret, password): its value must stay out of the settings files, which
// hold only a $VAR reference to it.
func IsSecretKey(key string) bool { return secretKey.MatchString(key) }

// IsEnvReference reports whether s is a $VAR reference, the only form a
// secret takes in a settings file.
func IsEnvReference(s string) bool { return envVarRe.MatchString(s) }

// SettingsDataSets reports whether parsed settings data (ParseSettings) sets
// key, as the loader would see it: the key, a value below it, or a non-map
// value at one of its parents.
func SettingsDataSets(data map[string]any, key string) bool { return layerSets(data, key) }

// MergeFrontMatterAndSettings merges project-level settings data (lowest
// first: the project file, then the local one) over a front matter exactly
// as Load does, without parsing the result. `itervox init --update` compares
// it before and after a move to prove the move changes no effective value.
func MergeFrontMatterAndSettings(front map[string]any, settings ...map[string]any) map[string]any {
	names := []string{LayerProject, LayerLocal}
	layers := make([]settingsLayer, 0, len(settings))
	for i, d := range settings {
		layers = append(layers, settingsLayer{name: names[min(i, len(names)-1)], data: d})
	}
	merged, _ := mergeLayers(front, layers)
	return merged
}

// NewSettingsFileText is the text a new settings file starts from: a header
// comment and the schema URL.
func NewSettingsFileText() []byte { return []byte(newSettingsFile) }
