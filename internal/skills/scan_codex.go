package skills

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// scanCodex enumerates Codex-side capabilities across the 8 known paths from
// the design draft. Every path is best-effort — a missing path returns empty
// results, never an error. The returned slices are deliberately separate so
// the caller can attribute Codex-side capabilities distinctly from Claude.
//
//   - ~/.codex/skills/                  (user-codex)
//   - ~/.codex/skills/.system/          (system-codex)
//   - ~/.codex/superpowers/skills/      (superpowers)
//   - ~/.agents/skills/                 (shared-agents)
//   - ~/.codex/plugins/<name>/plugin.json
//   - <project>/.agents/plugins/marketplace.json
//   - ~/.codex/superpowers/hooks/hooks.json
//
// The skill-lock at ~/.agents/.skill-lock.json provides install provenance —
// when a skill is present in `.skill-lock.json::installed`, we surface its
// origin marketplace as `Source: "marketplace:<name>"`.
func scanCodex(projectDir, homeDir string) (skills []Skill, plugins []Plugin, hooks []HookEntry, instructions []InstructionDoc, err error) {
	if homeDir == "" && projectDir == "" {
		return nil, nil, nil, nil, nil
	}

	provenance := loadSkillLock(homeDir)

	if homeDir != "" {
		// Codex skill paths — each becomes a Skill batch with a distinct Source.
		batches := []struct {
			path   string
			source string
		}{
			{filepath.Join(homeDir, ".codex", "skills"), "user-codex"},
			{filepath.Join(homeDir, ".codex", "skills", ".system"), "system-codex"},
			{filepath.Join(homeDir, ".codex", "superpowers", "skills"), "superpowers"},
			{filepath.Join(homeDir, ".agents", "skills"), "shared-agents"},
		}
		for _, b := range batches {
			batch, walkErr := walkCodexSkills(b.path, b.source, provenance)
			if walkErr != nil {
				return nil, nil, nil, nil, walkErr
			}
			skills = append(skills, batch...)
		}

		// Codex plugins.
		pluginsBatch, pluginErr := walkClaudePluginsAt(filepath.Join(homeDir, ".codex", "plugins"), "user-codex", "codex")
		if pluginErr != nil {
			return nil, nil, nil, nil, pluginErr
		}
		plugins = append(plugins, pluginsBatch...)

		// Superpowers hook file.
		spHooks, hookErr := readSuperpowersHooks(filepath.Join(homeDir, ".codex", "superpowers", "hooks", "hooks.json"))
		if hookErr != nil {
			return nil, nil, nil, nil, hookErr
		}
		hooks = append(hooks, spHooks...)
	}

	if projectDir != "" {
		// Project-level marketplace lock — currently informational only; skipped
		// here pending a richer manifest format (the design draft says this
		// file lists installed marketplaces, not capability shapes).
		_ = filepath.Join(projectDir, ".agents", "plugins", "marketplace.json")
	}

	return skills, plugins, hooks, instructions, nil
}

// walkCodexSkills walks a SKILL.md tree like Claude's, but tags Provider="codex".
func walkCodexSkills(root, source string, provenance map[string]string) ([]Skill, error) {
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}

	var out []Skill
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Warn("skills: codex walk error", "path", path, "err", err)
			return nil
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		skill, ok := parseSkillFile(path, source)
		if !ok {
			return nil
		}
		skill.Provider = "codex"
		// Provenance overlay: if .skill-lock.json says this skill came from
		// marketplace M, override the source with "marketplace:M".
		if origin, found := provenance[skill.Name]; found {
			skill.Source = "marketplace:" + origin
		}
		out = append(out, skill)
		return nil
	})
	return out, walkErr
}

// walkClaudePluginsAt is the same as walkClaudePlugins but accepts a custom
// root + source label and lets us tag plugins as `provider="codex"`.
func walkClaudePluginsAt(root, source, provider string) ([]Plugin, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Plugin
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifest := filepath.Join(root, e.Name(), "plugin.json")
		p, ok := parsePluginManifest(manifest, source)
		if !ok {
			continue
		}
		p.Provider = provider
		for i := range p.Skills {
			p.Skills[i].Provider = provider
		}
		out = append(out, p)
	}
	return out, nil
}

// loadSkillLock reads ~/.agents/.skill-lock.json and returns
// {skillName: marketplaceOrigin}. Missing file → empty map.
func loadSkillLock(homeDir string) map[string]string {
	if homeDir == "" {
		return nil
	}
	path := filepath.Join(homeDir, ".agents", ".skill-lock.json")
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		Installed map[string]struct {
			Marketplace string `json:"marketplace"`
		} `json:"installed"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		slog.Warn("skills: malformed .skill-lock.json", "path", path, "err", err)
		return nil
	}
	out := make(map[string]string, len(doc.Installed))
	for name, entry := range doc.Installed {
		if entry.Marketplace != "" {
			out[name] = entry.Marketplace
		}
	}
	return out
}

func readSuperpowersHooks(path string) ([]HookEntry, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	// Two tolerated shapes: same as Claude (`{ "hooks": { ... } }`) or a flat
	// array. Try both.
	var env hooksEnvelope
	if err := json.Unmarshal(body, &env); err == nil && len(env.Hooks) > 0 {
		var out []HookEntry
		for event, items := range env.Hooks {
			for _, item := range items {
				if len(item.Hooks) > 0 {
					for _, child := range item.Hooks {
						if child.Command == "" {
							continue
						}
						out = append(out, HookEntry{
							Event:        event,
							Matcher:      item.Matcher,
							Command:      child.Command,
							Provider:     "codex",
							Source:       "superpowers",
							ApproxTokens: (len(child.Command) * 2) / 4,
						})
					}
				} else if item.Command != "" {
					out = append(out, HookEntry{
						Event:        event,
						Matcher:      item.Matcher,
						Command:      item.Command,
						Provider:     "codex",
						Source:       "superpowers",
						ApproxTokens: (len(item.Command) * 2) / 4,
					})
				}
			}
		}
		return out, nil
	}
	slog.Warn("skills: superpowers hooks.json parse failed", "path", path)
	return nil, nil
}
