package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writePlugin(t *testing.T, root, name, manifest string) {
	t.Helper()
	dir := filepath.Join(root, ".claude", "plugins", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
}

const fullPluginManifest = `{
  "name": "demo-plugin",
  "description": "demo for testing",
  "skills": [
    { "name": "skill-a", "description": "first", "trigger": "/a", "path": "skills/a.md" },
    { "name": "skill-b", "description": "second" }
  ],
  "hooks": [
    { "event": "PreToolUse", "command": "echo hello" }
  ],
  "agents": [
    { "name": "reviewer", "description": "code reviewer" }
  ],
  "commands": [
    { "name": "/run", "description": "run tests" }
  ]
}`

const malformedManifest = `{ this is not json `
const noNameManifest = `{ "description": "missing name" }`

func TestScanClaudePlugins_ProjectOnly_FullManifest(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	skillPath := filepath.Join(proj, ".claude", "plugins", "demo-plugin", "skills", "a.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte("Plugin skill body mentions mystery-server."), 0o644); err != nil {
		t.Fatalf("write plugin skill body: %v", err)
	}
	writePlugin(t, proj, "demo-plugin", fullPluginManifest)

	plugins, err := scanClaudePlugins(proj, "")
	if err != nil {
		t.Fatalf("scanClaudePlugins: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(plugins))
	}
	p := plugins[0]
	if p.Name != "demo-plugin" {
		t.Errorf("unexpected plugin name %q", p.Name)
	}
	if p.Provider != "claude" || p.Source != "project" {
		t.Errorf("unexpected provider/source: %+v", p)
	}
	if len(p.Skills) != 2 {
		t.Errorf("expected 2 skills, got %d", len(p.Skills))
	}
	if len(p.Hooks) != 1 {
		t.Errorf("expected 1 hook, got %d", len(p.Hooks))
	}
	if len(p.Agents) != 1 {
		t.Errorf("expected 1 agent, got %d", len(p.Agents))
	}
	if len(p.Commands) != 1 {
		t.Errorf("expected 1 command, got %d", len(p.Commands))
	}
	for _, s := range p.Skills {
		if s.Source != "plugin:demo-plugin" {
			t.Errorf("expected plugin-attributed source, got %q for %s", s.Source, s.Name)
		}
	}
	if p.Skills[0].FilePath != skillPath {
		t.Errorf("expected resolved plugin skill path %q, got %q", skillPath, p.Skills[0].FilePath)
	}
	if p.Skills[0].bodyText == "" {
		t.Errorf("expected plugin skill body to be retained for analysis")
	}
	if p.ApproxTokens <= 0 {
		t.Errorf("expected positive ApproxTokens, got %d", p.ApproxTokens)
	}
}

func TestScanClaudePlugins_UserHome(t *testing.T) {
	t.Parallel()
	user := t.TempDir()
	writePlugin(t, user, "u-plugin", fullPluginManifest)

	plugins, err := scanClaudePlugins("", user)
	if err != nil {
		t.Fatalf("scanClaudePlugins: %v", err)
	}
	if len(plugins) != 1 || plugins[0].Source != "user" {
		t.Fatalf("expected 1 user plugin, got %+v", plugins)
	}
}

func TestScanClaudePlugins_MalformedSkipped(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writePlugin(t, proj, "good", fullPluginManifest)
	writePlugin(t, proj, "bad-json", malformedManifest)
	writePlugin(t, proj, "no-name", noNameManifest)

	plugins, err := scanClaudePlugins(proj, "")
	if err != nil {
		t.Fatalf("scanClaudePlugins: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected only the well-formed plugin, got %d: %v", len(plugins), plugins)
	}
}

func TestScanClaudePlugins_MissingRootReturnsNilNil(t *testing.T) {
	t.Parallel()
	plugins, err := scanClaudePlugins(t.TempDir(), "")
	if err != nil {
		t.Fatalf("expected no error on empty dir, got %v", err)
	}
	if plugins != nil {
		t.Errorf("expected nil plugins on empty dir, got %v", plugins)
	}
}
