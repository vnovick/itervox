package skills

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// scanClaudePlugins discovers `plugin.json` files under
// <projectDir>/.claude/plugins/<name>/plugin.json (and the equivalent under
// homeDir). Each plugin can declare its own skills, hooks, agents, and
// commands; we attribute them to the parent plugin and aggregate token
// estimates across the whole plugin tree.
//
// The contract follows the design draft (see
// `planning/plans/2026-04-16-skills-inventory-design.md`): we scan the
// per-project / per-user `.claude/plugins/<name>/plugin.json` shape. The
// production marketplace layout (`.claude-plugin/plugin.json`) is a separate
// scanner pass and is not in scope for T-79.
//
// homeDir == "" disables the user-home walk.
func scanClaudePlugins(projectDir, homeDir string) ([]Plugin, error) {
	var out []Plugin
	if projectDir != "" {
		walked, err := walkClaudePlugins(filepath.Join(projectDir, ".claude", "plugins"), "project")
		if err != nil {
			return nil, err
		}
		out = append(out, walked...)
	}
	if homeDir != "" {
		walked, err := walkClaudePlugins(filepath.Join(homeDir, ".claude", "plugins"), "user")
		if err != nil {
			return nil, err
		}
		out = append(out, walked...)
	}
	return out, nil
}

// walkClaudePlugins finds plugin manifests under `root`. Real-world Claude
// Code uses two layouts and we check both:
//
//  1. `<root>/<name>/plugin.json`           — the design-draft layout
//  2. `<root>/<name>/.claude-plugin/plugin.json`           — marketplace flat
//  3. `<root>/<name>/plugins/<plugin>/.claude-plugin/plugin.json` — marketplace nested
//
// A missing root returns (nil, nil). Skipping `cache/` (per-version snapshots
// of the same manifests) avoids reporting every cached version as a separate
// plugin — the marketplace copy is the authoritative one.
func walkClaudePlugins(root, source string) ([]Plugin, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	seen := make(map[string]struct{}) // dedup by plugin name
	var out []Plugin

	add := func(manifestPath string) {
		p, ok := parsePluginManifest(manifestPath, source)
		if !ok {
			return
		}
		if _, dup := seen[p.Name]; dup {
			return
		}
		seen[p.Name] = struct{}{}
		out = append(out, p)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Skip per-version cache (`~/.claude/plugins/cache/`) — its contents
		// duplicate `marketplaces/`. `repos/` is also internal; surface only
		// the marketplace + flat layouts.
		if e.Name() == "cache" || e.Name() == "repos" {
			continue
		}
		dir := filepath.Join(root, e.Name())

		// Layout 1: <root>/<name>/plugin.json
		add(filepath.Join(dir, "plugin.json"))
		// Layout 2: <root>/<name>/.claude-plugin/plugin.json
		add(filepath.Join(dir, ".claude-plugin", "plugin.json"))
		// Layout 3: <root>/<name>/plugins/<plugin>/.claude-plugin/plugin.json
		pluginsRoot := filepath.Join(dir, "plugins")
		if subEntries, err := os.ReadDir(pluginsRoot); err == nil {
			for _, sub := range subEntries {
				if !sub.IsDir() {
					continue
				}
				add(filepath.Join(pluginsRoot, sub.Name(), ".claude-plugin", "plugin.json"))
			}
		}
	}
	return out, nil
}

// pluginManifest is the JSON shape we read. Optional fields are tolerated.
type pluginManifest struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Skills      []manifestSkill `json:"skills"`
	Hooks       []manifestHook  `json:"hooks"`
	Agents      []manifestNamed `json:"agents"`
	Commands    []manifestNamed `json:"commands"`
	MCPServers  map[string]any  `json:"mcpServers"`
}

type manifestSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Trigger     string `json:"trigger"`
	Path        string `json:"path"`
}

type manifestHook struct {
	Event   string `json:"event"`
	Command string `json:"command"`
}

type manifestNamed struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

func parsePluginManifest(path, source string) (Plugin, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("skills: cannot read plugin.json", "path", path, "err", err)
		}
		return Plugin{}, false
	}
	var m pluginManifest
	if err := json.Unmarshal(body, &m); err != nil {
		slog.Warn("skills: malformed plugin.json", "path", path, "err", err)
		return Plugin{}, false
	}
	if m.Name == "" {
		slog.Warn("skills: plugin.json missing required 'name' field", "path", path)
		return Plugin{}, false
	}

	pluginSource := "plugin:" + m.Name

	skills := make([]Skill, 0, len(m.Skills))
	manifestDir := filepath.Dir(path)
	for _, s := range m.Skills {
		if s.Name == "" {
			continue
		}
		var triggers []string
		if s.Trigger != "" {
			triggers = []string{s.Trigger}
		}
		skillPath := s.Path
		skillBody := ""
		approxTokens := len(s.Description) / 4
		if s.Path != "" {
			resolvedPath := s.Path
			if !filepath.IsAbs(resolvedPath) {
				resolvedPath = filepath.Join(manifestDir, resolvedPath)
			}
			if body, err := os.ReadFile(resolvedPath); err == nil {
				skillPath = resolvedPath
				skillBody = string(body)
				approxTokens = len(body) / 4
			} else if !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("skills: cannot read plugin skill body", "path", resolvedPath, "err", err)
			}
		}
		skills = append(skills, Skill{
			Name:            s.Name,
			Description:     s.Description,
			Provider:        "claude",
			Source:          pluginSource,
			FilePath:        skillPath,
			ApproxTokens:    approxTokens,
			TriggerPatterns: triggers,
			bodyText:        skillBody,
		})
	}

	hooks := make([]HookEntry, 0, len(m.Hooks))
	for _, h := range m.Hooks {
		if h.Event == "" || h.Command == "" {
			continue
		}
		hooks = append(hooks, HookEntry{
			Event:        h.Event,
			Command:      h.Command,
			Provider:     "claude",
			Source:       pluginSource,
			ApproxTokens: len(h.Command) / 4,
		})
	}

	agents := make([]AgentDef, 0, len(m.Agents))
	for _, a := range m.Agents {
		if a.Name == "" {
			continue
		}
		agentPath := resolvePluginChildPath(manifestDir, a.Path)
		agents = append(agents, AgentDef{
			Name:        a.Name,
			Description: a.Description,
			FilePath:    agentPath,
		})
	}

	commands := make([]CommandDef, 0, len(m.Commands))
	for _, c := range m.Commands {
		if c.Name == "" {
			continue
		}
		commandPath := resolvePluginChildPath(manifestDir, c.Path)
		commands = append(commands, CommandDef{
			Name:        c.Name,
			Description: c.Description,
			FilePath:    commandPath,
		})
	}

	// Aggregate token estimate: manifest body + every child's contribution.
	approxTokens := len(body) / 4
	for _, s := range skills {
		approxTokens += s.ApproxTokens
	}
	for _, h := range hooks {
		approxTokens += h.ApproxTokens
	}

	return Plugin{
		Name:         m.Name,
		Provider:     "claude",
		FilePath:     path,
		Skills:       skills,
		Hooks:        hooks,
		Agents:       agents,
		Commands:     commands,
		Source:       source,
		ApproxTokens: approxTokens,
	}, true
}

func resolvePluginChildPath(manifestDir, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(manifestDir, path)
}
