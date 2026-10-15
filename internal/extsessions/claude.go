package extsessions

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// claudeRecord is the subset of a Claude Code transcript record discovery
// and the transcript reader use. Every field is optional.
type claudeRecord struct {
	Type        string          `json:"type"`
	SessionID   string          `json:"sessionId"`
	Cwd         string          `json:"cwd"`
	Version     string          `json:"version"`
	Timestamp   string          `json:"timestamp"`
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	AITitle     string          `json:"aiTitle"`
	Summary     string          `json:"summary"`
	Message     json.RawMessage `json:"message"`
}

// discoverClaude reads <dir>/<encoded-cwd>/<session-id>.jsonl. Only the
// top level of each project directory holds sessions; sub-directories
// (sub-agent sidechains, tool output) are not sessions of their own.
// prefixes, when non-empty, restrict the project directories read to those
// whose name starts with one of them.
func discoverClaude(dir string, since time.Time, prefixes []string) ([]Session, error) {
	projects, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, p := range projects {
		if !p.IsDir() || !hasAnyPrefix(p.Name(), prefixes) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(dir, p.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if f.IsDir() || !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			info, err := f.Info()
			if err != nil || info.ModTime().Before(since) {
				continue
			}
			s, ok := readClaudeSession(filepath.Join(dir, p.Name(), name), info.ModTime())
			if ok {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

func hasAnyPrefix(name string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// readClaudeSession reads a transcript's metadata: the session ID (file
// name, confirmed by the records), cwd, CLI version and start time from the
// head, the newest title from the tail.
func readClaudeSession(path string, mod time.Time) (Session, bool) {
	s := Session{
		ID:        strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Backend:   BackendClaude,
		Path:      path,
		Format:    FormatClaudeJSONL,
		UpdatedAt: mod,
	}
	if !plausibleSessionID(s.ID) {
		return Session{}, false
	}
	recordID := ""
	_ = headLines(path, headScanBytes, func(line []byte) bool {
		var r claudeRecord
		if json.Unmarshal(line, &r) != nil {
			return true
		}
		if recordID == "" && r.SessionID != "" {
			recordID = r.SessionID
		}
		if s.StartedAt.IsZero() && r.Timestamp != "" {
			s.StartedAt, _ = time.Parse(time.RFC3339Nano, r.Timestamp)
		}
		if s.Cwd == "" && r.Cwd != "" && !r.IsSidechain {
			s.Cwd, s.CLIVersion = r.Cwd, r.Version
		}
		return s.Cwd == "" || s.StartedAt.IsZero() || recordID == ""
	})
	// A file whose records name another session is not this session's
	// transcript (or not a transcript at all).
	if recordID != "" && recordID != s.ID {
		return Session{}, false
	}
	_, _ = tailLines(path, -1, tailScanBytes, func(line []byte, _ int64) bool {
		if head := string(line[:min(len(line), 64)]); !strings.Contains(head, "title") && !strings.Contains(head, "summary") {
			return true
		}
		var r claudeRecord
		if json.Unmarshal(line, &r) != nil {
			return true
		}
		switch {
		case r.Type == "ai-title" && r.AITitle != "":
			s.Title = r.AITitle
		case r.Type == "summary" && r.Summary != "":
			s.Title = r.Summary
		default:
			return true
		}
		return false
	})
	return s, true
}

// plausibleSessionID accepts the IDs the CLIs write (UUIDs) and rejects
// anything that could not be passed safely as one argument.
func plausibleSessionID(id string) bool {
	if len(id) < 8 || len(id) > 128 {
		return false
	}
	for _, c := range []byte(id) {
		if !isASCIIAlnum(c) && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// claudeEntries turns one transcript record into transcript entries.
func claudeEntries(line []byte) []Entry {
	var r claudeRecord
	if json.Unmarshal(line, &r) != nil || r.IsSidechain || r.IsMeta {
		return nil
	}
	if r.Type != "user" && r.Type != "assistant" {
		return nil
	}
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(r.Message, &msg) != nil {
		return nil
	}
	role := cmp.Or(msg.Role, r.Type)
	at := parseTime(r.Timestamp)
	var text string
	if json.Unmarshal(msg.Content, &text) == nil {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []Entry{{Role: role, Kind: KindText, Text: text, At: at}}
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Name    string          `json:"name"`
		Content json.RawMessage `json:"content"`
		IsError bool            `json:"is_error"`
	}
	if json.Unmarshal(msg.Content, &blocks) != nil {
		return nil
	}
	var out []Entry
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, Entry{Role: role, Kind: KindText, Text: b.Text, At: at})
			}
		case "tool_use":
			out = append(out, Entry{Role: role, Kind: KindTool, Text: b.Name, At: at})
		case "tool_result":
			out = append(out, Entry{Role: role, Kind: KindToolResult, Text: toolResultText(b.Content), At: at})
		}
		// thinking, images and unknown block types are not shown.
	}
	return out
}

// toolResultText flattens a tool_result content (a string or text blocks).
func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
