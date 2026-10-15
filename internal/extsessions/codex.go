package extsessions

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// codexLine is one rollout line: an envelope (FormatCodexRollout) or, in
// the legacy layout, a bare header or response item.
type codexLine struct {
	// Envelope.
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	// Legacy header.
	ID           string          `json:"id"`
	Instructions json.RawMessage `json:"instructions"`
}

// codexMeta is the session_meta payload.
type codexMeta struct {
	ID         string `json:"id"`
	Timestamp  string `json:"timestamp"`
	Cwd        string `json:"cwd"`
	CLIVersion string `json:"cli_version"`
}

// discoverCodex walks <dir>/YYYY/MM/DD/rollout-*.jsonl (and rollouts at
// any depth for older layouts), skipping date directories that end before
// since (with a day of slack for time zones).
func discoverCodex(dir string, since time.Time) ([]Session, error) {
	var out []Session
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			if d != nil && d.IsDir() && path != dir {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != dir && codexDirBefore(dir, path, since) {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(since) {
			return nil
		}
		if s, ok := readCodexSession(path, info.ModTime()); ok {
			out = append(out, s)
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return out, err
}

// codexDirBefore reports whether path is a YYYY, YYYY/MM or YYYY/MM/DD
// directory under root whose whole period ended more than a day before
// since. Other directories are walked.
func codexDirBefore(root, path string, since time.Time) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	nums := make([]int, 0, 3)
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return false
		}
		nums = append(nums, n)
	}
	var end time.Time
	switch len(nums) {
	case 1:
		end = time.Date(nums[0]+1, 1, 1, 0, 0, 0, 0, time.UTC)
	case 2:
		end = time.Date(nums[0], time.Month(nums[1])+1, 1, 0, 0, 0, 0, time.UTC)
	case 3:
		end = time.Date(nums[0], time.Month(nums[1]), nums[2]+1, 0, 0, 0, 0, time.UTC)
	default:
		return false
	}
	return end.Add(24 * time.Hour).Before(since)
}

// readCodexSession reads a rollout's header line.
func readCodexSession(path string, mod time.Time) (Session, bool) {
	s := Session{Backend: BackendCodex, Path: path, UpdatedAt: mod}
	var first []byte
	_ = headLines(path, headScanBytes, func(line []byte) bool {
		if len(line) == 0 {
			return true
		}
		first = append([]byte(nil), line...)
		return false
	})
	var l codexLine
	if json.Unmarshal(first, &l) != nil {
		return Session{}, false
	}
	switch {
	case l.Type == "session_meta":
		var m codexMeta
		if json.Unmarshal(l.Payload, &m) != nil {
			return Session{}, false
		}
		s.ID, s.Cwd, s.CLIVersion = m.ID, m.Cwd, m.CLIVersion
		s.StartedAt = parseTime(cmp.Or(m.Timestamp, l.Timestamp))
		s.Format = FormatCodexRollout
	case l.Type == "" && l.ID != "":
		s.ID = l.ID
		s.StartedAt = parseTime(l.Timestamp)
		s.Format = FormatCodexLegacy
	default:
		return Session{}, false
	}
	if s.ID == "" {
		s.ID = codexIDFromName(filepath.Base(path))
	}
	if !plausibleSessionID(s.ID) {
		return Session{}, false
	}
	return s, true
}

// codexIDFromName is the UUID at the end of rollout-<time>-<uuid>.jsonl.
func codexIDFromName(name string) string {
	stem := strings.TrimSuffix(name, ".jsonl")
	if len(stem) < 36 {
		return ""
	}
	return stem[len(stem)-36:]
}

// codexContextPrefixes mark user messages Codex adds itself (environment
// and instruction context), which are not part of the conversation.
var codexContextPrefixes = []string{"<environment_context>", "<user_instructions>", "# AGENTS.md instructions"}

func isCodexContext(text string) bool {
	for _, p := range codexContextPrefixes {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// codexEntries turns one rollout line into transcript entries.
func codexEntries(line []byte) []Entry {
	var l codexLine
	if json.Unmarshal(line, &l) != nil {
		return nil
	}
	payload := json.RawMessage(line)
	at := parseTime(l.Timestamp)
	switch l.Type {
	case "response_item":
		payload = l.Payload
	case "message", "function_call", "function_call_output", "local_shell_call", "custom_tool_call":
		// legacy bare response item: the line is the payload
	default:
		return nil
	}
	var item struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Name    string          `json:"name"`
		Output  json.RawMessage `json:"output"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(payload, &item) != nil {
		return nil
	}
	switch item.Type {
	case "message":
		if item.Role != "user" && item.Role != "assistant" {
			return nil
		}
		var out []Entry
		for _, c := range item.Content {
			if t := strings.TrimSpace(c.Text); t == "" || isCodexContext(t) {
				continue
			}
			out = append(out, Entry{Role: item.Role, Kind: KindText, Text: c.Text, At: at})
		}
		return out
	case "function_call", "custom_tool_call":
		return []Entry{{Role: "assistant", Kind: KindTool, Text: item.Name, At: at}}
	case "local_shell_call":
		return []Entry{{Role: "assistant", Kind: KindTool, Text: "shell", At: at}}
	case "function_call_output":
		return []Entry{{Role: "user", Kind: KindToolResult, Text: codexOutputText(item.Output), At: at}}
	}
	return nil
}

// codexOutputText flattens a function_call_output output (a string, or an
// object with an output string).
func codexOutputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Output string `json:"output"`
	}
	if json.Unmarshal(raw, &o) == nil {
		return o.Output
	}
	return ""
}
