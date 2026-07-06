package skills

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// scanMCPServers reads MCP server registrations from the standard locations:
//
//   - <projectDir>/.claude/settings.json::mcpServers
//   - <homeDir>/.claude/settings.json::mcpServers
//   - <projectDir>/.mcp.json::mcpServers
//   - <homeDir>/.mcp.json::mcpServers
//
// Returns a deduplicated slice keyed by (Name, Source). Empty file or missing
// `mcpServers` key returns no entries from that source — never an error.
func scanMCPServers(projectDir, homeDir string) ([]MCPServer, error) {
	type entry struct {
		path   string
		source string
	}
	var sources []entry
	if projectDir != "" {
		sources = append(sources,
			entry{filepath.Join(projectDir, ".claude", "settings.json"), "project-settings"},
			entry{filepath.Join(projectDir, ".mcp.json"), "mcp.json"},
		)
	}
	if homeDir != "" {
		sources = append(sources,
			entry{filepath.Join(homeDir, ".claude", "settings.json"), "user-settings"},
			entry{filepath.Join(homeDir, ".mcp.json"), "mcp.json"},
		)
	}

	var out []MCPServer
	seen := make(map[string]struct{})
	for _, s := range sources {
		servers, err := readMCPServersFromJSON(s.path, s.source)
		if err != nil {
			return nil, err
		}
		for _, srv := range servers {
			key := srv.Name + "|" + srv.Source
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, srv)
		}
	}
	return out, nil
}

// mcpServerEntry is the JSON shape of one MCP server registration. Both
// stdio (command + args) and remote (transport + url) shapes are tolerated.
type mcpServerEntry struct {
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	Transport string   `json:"transport"`
	URL       string   `json:"url"`
	Tools     []string `json:"tools"`
}

type mcpEnvelope struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

func readMCPServersFromJSON(path, source string) ([]MCPServer, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var env mcpEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		// A malformed local file should not crash the inventory — log and skip.
		slog.Warn("skills: malformed MCP-bearing JSON", "path", path, "err", err)
		return nil, nil
	}
	if len(env.MCPServers) == 0 {
		return nil, nil
	}
	out := make([]MCPServer, 0, len(env.MCPServers))
	for name, entry := range env.MCPServers {
		transport := entry.Transport
		if transport == "" {
			if entry.URL != "" {
				transport = "http"
			} else if entry.Command != "" {
				transport = "stdio"
			}
		}
		out = append(out, MCPServer{
			Name:      name,
			Transport: transport,
			Command:   entry.Command,
			URL:       entry.URL,
			Source:    source,
			Tools:     entry.Tools,
		})
	}
	return out, nil
}
