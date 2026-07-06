package skills

import (
	"testing"
	"time"
)

func TestScan_StubReturnsInventoryWithScanTime(t *testing.T) {
	t.Parallel()
	before := time.Now()
	inv, err := Scan(t.TempDir(), "", ScanOptions{})
	after := time.Now()

	if err != nil {
		t.Fatalf("Scan returned unexpected error: %v", err)
	}
	if inv == nil {
		t.Fatal("Scan returned nil inventory")
	}
	if inv.ScanTime.Before(before) || inv.ScanTime.After(after) {
		t.Fatalf("ScanTime %v outside expected window [%v, %v]", inv.ScanTime, before, after)
	}
}

func TestScan_EmptyDirsAreFast(t *testing.T) {
	t.Parallel()
	start := time.Now()
	if _, err := Scan(t.TempDir(), "", ScanOptions{}); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Scan on empty dirs took %v — should be <200ms", elapsed)
	}
}

func TestScan_IntegratesAllScanners(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	user := t.TempDir()

	// Skills under both project and user.
	writeSkill(t, proj, "p-skill", wellFormed)
	writeSkill(t, user, "u-skill", wellFormed)

	// Plugin in project.
	writePlugin(t, proj, "demo-plugin", fullPluginManifest)

	// MCP + hooks via project settings.json.
	writeFile(t, proj+"/.claude/settings.json", `{
		"mcpServers": { "ctx7": { "command": "npx" } },
		"hooks": { "PreToolUse": [{ "command": "echo hi" }] }
	}`)

	// Instructions.
	writeFile(t, proj+"/CLAUDE.md", "# project")
	writeFile(t, proj+"/AGENTS.md", "# agents")

	inv, err := Scan(proj, user, ScanOptions{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(inv.Skills) < 2 {
		t.Errorf("expected at least 2 skills (project + user), got %d", len(inv.Skills))
	}
	if len(inv.Plugins) != 1 {
		t.Errorf("expected 1 plugin, got %d", len(inv.Plugins))
	}
	if len(inv.MCPServers) != 1 {
		t.Errorf("expected 1 MCP server, got %d", len(inv.MCPServers))
	}
	if len(inv.Hooks) != 1 {
		t.Errorf("expected 1 hook, got %d", len(inv.Hooks))
	}
	if len(inv.Instructions) != 2 {
		t.Errorf("expected 2 instruction docs, got %d", len(inv.Instructions))
	}
	if inv.ScanTime.IsZero() {
		t.Error("ScanTime not set")
	}
}
