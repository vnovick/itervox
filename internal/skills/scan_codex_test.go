package skills

import (
	"path/filepath"
	"testing"
)

func TestScanCodex_AllPathsMissing(t *testing.T) {
	t.Parallel()
	skills, plugins, hooks, instructions, err := scanCodex(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("scanCodex: %v", err)
	}
	if skills != nil || plugins != nil || hooks != nil || instructions != nil {
		t.Errorf("expected all nil for empty dirs, got skills=%v plugins=%v hooks=%v instructions=%v",
			skills, plugins, hooks, instructions)
	}
}

func TestScanCodex_UserSkillsDir(t *testing.T) {
	t.Parallel()
	user := t.TempDir()
	dir := filepath.Join(user, ".codex", "skills", "demo")
	writeFile(t, filepath.Join(dir, "SKILL.md"), wellFormed)

	skills, _, _, _, err := scanCodex("", user)
	if err != nil {
		t.Fatalf("scanCodex: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 codex skill, got %d", len(skills))
	}
	s := skills[0]
	if s.Provider != "codex" || s.Source != "user-codex" {
		t.Errorf("unexpected provider/source: %+v", s)
	}
}

func TestScanCodex_SuperpowersHooks(t *testing.T) {
	t.Parallel()
	user := t.TempDir()
	hooksPath := filepath.Join(user, ".codex", "superpowers", "hooks", "hooks.json")
	writeFile(t, hooksPath, flatHooks)

	_, _, hooks, _, err := scanCodex("", user)
	if err != nil {
		t.Fatalf("scanCodex: %v", err)
	}
	if len(hooks) != 2 {
		t.Fatalf("expected 2 superpowers hooks, got %d", len(hooks))
	}
	for _, h := range hooks {
		if h.Provider != "codex" || h.Source != "superpowers" {
			t.Errorf("unexpected hook provider/source: %+v", h)
		}
	}
}

func TestScanCodex_SkillLockProvenance(t *testing.T) {
	t.Parallel()
	user := t.TempDir()
	skillDir := filepath.Join(user, ".codex", "skills", "my-skill")
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), wellFormed)
	writeFile(t, filepath.Join(user, ".agents", ".skill-lock.json"), `{
		"installed": {
			"my-skill": { "marketplace": "official" }
		}
	}`)

	skills, _, _, _, err := scanCodex("", user)
	if err != nil {
		t.Fatalf("scanCodex: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Source != "marketplace:official" {
		t.Errorf("expected marketplace provenance, got %q", skills[0].Source)
	}
}

func TestScanCodex_PluginScanned(t *testing.T) {
	t.Parallel()
	user := t.TempDir()
	pluginManifest := filepath.Join(user, ".codex", "plugins", "demo-plugin", "plugin.json")
	writeFile(t, pluginManifest, fullPluginManifest)

	_, plugins, _, _, err := scanCodex("", user)
	if err != nil {
		t.Fatalf("scanCodex: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(plugins))
	}
	if plugins[0].Provider != "codex" {
		t.Errorf("expected provider=codex, got %q", plugins[0].Provider)
	}
}
