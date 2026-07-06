package skills

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

const settingsWithMCP = `{
  "mcpServers": {
    "context7": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-context7"]
    },
    "remote-api": {
      "transport": "http",
      "url": "https://example.com/mcp"
    }
  }
}`

const mcpJSONOnly = `{
  "mcpServers": {
    "filesystem": { "command": "mcp-fs", "args": ["--root", "."] }
  }
}`

const malformedJSON = `not even close to json`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestScanMCPServers_ProjectSettings(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, ".claude", "settings.json"), settingsWithMCP)

	servers, err := scanMCPServers(proj, "")
	if err != nil {
		t.Fatalf("scanMCPServers: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	if servers[0].Name != "context7" || servers[0].Transport != "stdio" {
		t.Errorf("expected context7 stdio, got %+v", servers[0])
	}
	if servers[1].Name != "remote-api" || servers[1].Transport != "http" {
		t.Errorf("expected remote-api http, got %+v", servers[1])
	}
	for _, s := range servers {
		if s.Source != "project-settings" {
			t.Errorf("expected project-settings source, got %q", s.Source)
		}
	}
}

func TestScanMCPServers_McpJSON(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, ".mcp.json"), mcpJSONOnly)

	servers, err := scanMCPServers(proj, "")
	if err != nil {
		t.Fatalf("scanMCPServers: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if servers[0].Name != "filesystem" || servers[0].Source != "mcp.json" {
		t.Errorf("unexpected server: %+v", servers[0])
	}
}

func TestScanMCPServers_BothSources(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, ".claude", "settings.json"), settingsWithMCP)
	writeFile(t, filepath.Join(proj, ".mcp.json"), mcpJSONOnly)

	servers, err := scanMCPServers(proj, "")
	if err != nil {
		t.Fatalf("scanMCPServers: %v", err)
	}
	// 2 from settings.json + 1 from .mcp.json = 3 total
	if len(servers) != 3 {
		t.Fatalf("expected 3 servers, got %d: %+v", len(servers), servers)
	}
}

func TestScanMCPServers_UserHome(t *testing.T) {
	t.Parallel()
	user := t.TempDir()
	writeFile(t, filepath.Join(user, ".claude", "settings.json"), settingsWithMCP)

	servers, err := scanMCPServers("", user)
	if err != nil {
		t.Fatalf("scanMCPServers: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}
	for _, s := range servers {
		if s.Source != "user-settings" {
			t.Errorf("expected user-settings source, got %q", s.Source)
		}
	}
}

func TestScanMCPServers_MalformedSkipped(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, ".claude", "settings.json"), malformedJSON)

	servers, err := scanMCPServers(proj, "")
	if err != nil {
		t.Fatalf("expected no error on malformed JSON, got %v", err)
	}
	if servers != nil {
		t.Errorf("expected nil servers on malformed JSON, got %v", servers)
	}
}

func TestScanMCPServers_MissingFilesReturnsNilNil(t *testing.T) {
	t.Parallel()
	servers, err := scanMCPServers(t.TempDir(), "")
	if err != nil {
		t.Fatalf("expected no error on missing files, got %v", err)
	}
	if servers != nil {
		t.Errorf("expected nil servers on missing files, got %v", servers)
	}
}
